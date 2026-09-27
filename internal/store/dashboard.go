package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davidsugianto/upsera/internal/model"
)

// ListRecentHeartbeats returns up to perMonitor heartbeats of each of
// teamID's monitors, newest first, keyed by monitor id. Monitors without
// heartbeats are absent.
func (s *Store) ListRecentHeartbeats(ctx context.Context, teamID int64, perMonitor int) (map[int64][]model.Heartbeat, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, h.probe_id, h.time, h.status, h.latency_ms, h.message
		FROM upsera.monitors m
		CROSS JOIN LATERAL (
			SELECT probe_id, time, status, latency_ms, message FROM upsera.heartbeats
			WHERE monitor_id = m.id ORDER BY time DESC LIMIT $2) h
		WHERE m.team_id = $1
		ORDER BY m.id, h.time DESC`, teamID, perMonitor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][]model.Heartbeat)
	for rows.Next() {
		var h model.Heartbeat
		if err := rows.Scan(&h.MonitorID, &h.ProbeID, &h.Time, &h.Status, &h.LatencyMs, &h.Message); err != nil {
			return nil, err
		}
		out[h.MonitorID] = append(out[h.MonitorID], h)
	}
	return out, rows.Err()
}

// CountUptimeSince tallies raw heartbeats at or after since for teamID's
// monitors (only monitorID's when it is non-zero), keyed by monitor id.
func (s *Store) CountUptimeSince(ctx context.Context, teamID, monitorID int64, since time.Time) (map[int64]model.UptimeCount, error) {
	return collectUptime(s.pool.Query(ctx, `
		SELECT h.monitor_id, count(*) FILTER (WHERE h.status <> 3), count(*) FILTER (WHERE h.status IN (1, 2))
		FROM upsera.heartbeats h JOIN upsera.monitors m ON m.id = h.monitor_id
		WHERE m.team_id = $1 AND ($2::bigint = 0 OR h.monitor_id = $2) AND h.time >= $3
		GROUP BY h.monitor_id`, teamID, monitorID, since))
}

// SumUptimeDaily sums the uptime_daily rollups from fromDay (a calendar
// date; time of day ignored) onwards for teamID's monitors (only
// monitorID's when it is non-zero), keyed by monitor id.
func (s *Store) SumUptimeDaily(ctx context.Context, teamID, monitorID int64, fromDay time.Time) (map[int64]model.UptimeCount, error) {
	return collectUptime(s.pool.Query(ctx, `
		SELECT u.monitor_id, sum(u.checks)::bigint, sum(u.up)::bigint
		FROM upsera.uptime_daily u JOIN upsera.monitors m ON m.id = u.monitor_id
		WHERE m.team_id = $1 AND ($2::bigint = 0 OR u.monitor_id = $2) AND u.day >= $3::date
		GROUP BY u.monitor_id`, teamID, monitorID, fromDay.Format(time.DateOnly)))
}

func collectUptime(rows pgx.Rows, err error) (map[int64]model.UptimeCount, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]model.UptimeCount)
	for rows.Next() {
		var id, checks, up int64
		if err := rows.Scan(&id, &checks, &up); err != nil {
			return nil, err
		}
		out[id] = model.UptimeCount{Checks: int(checks), Up: int(up)}
	}
	return out, rows.Err()
}

// ListStatusChanges returns monitorID's retained heartbeats whose status
// differs from the one before, newest first, at most limit. The oldest
// retained heartbeat always counts as a change (with a nil Previous).
func (s *Store) ListStatusChanges(ctx context.Context, monitorID int64, limit int) ([]model.StatusChange, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT time, status, prev, message FROM (
			SELECT time, status, message, lag(status) OVER (ORDER BY time) AS prev
			FROM upsera.heartbeats WHERE monitor_id = $1) t
		WHERE prev IS DISTINCT FROM status ORDER BY time DESC LIMIT $2`, monitorID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.StatusChange, error) {
		var c model.StatusChange
		err := r.Scan(&c.Time, &c.Status, &c.Previous, &c.Message)
		return c, err
	})
}

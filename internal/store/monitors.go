package store

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davidsugianto/upsera/internal/model"
)

const monitorCols = `id, team_id, name, type, config, interval_s, retry_interval_s, retries, timeout_s,
	paused, group_name, tags, coalesce(push_token, ''), created_at, updated_at, parent_id, escalation_policy_id,
	ARRAY(SELECT mc.channel_id FROM upsera.monitor_channels mc WHERE mc.monitor_id = monitors.id ORDER BY mc.channel_id)`

func scanMonitor(row pgx.Row) (model.Monitor, error) {
	var m model.Monitor
	err := row.Scan(&m.ID, &m.TeamID, &m.Name, &m.Type, &m.Config, &m.IntervalS, &m.RetryIntervalS, &m.Retries,
		&m.TimeoutS, &m.Paused, &m.GroupName, &m.Tags, &m.PushToken, &m.CreatedAt, &m.UpdatedAt,
		&m.ParentID, &m.EscalationPolicyID, &m.ChannelIDs)
	if m.Tags == nil {
		m.Tags = []string{}
	}
	if m.ChannelIDs == nil {
		m.ChannelIDs = []int64{}
	}
	return m, mapErr(err)
}

func collectMonitors(rows pgx.Rows, err error) ([]model.Monitor, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Monitor, error) { return scanMonitor(r) })
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func tagsOrEmpty(t []string) []string {
	if t == nil {
		return []string{}
	}
	return t
}

// CreateMonitor inserts m (with its channel links) and returns the stored row.
func (s *Store) CreateMonitor(ctx context.Context, m model.Monitor) (model.Monitor, error) {
	var out model.Monitor
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var id int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO upsera.monitors (team_id, name, type, config, interval_s, retry_interval_s, retries, timeout_s,
				paused, group_name, tags, push_token, parent_id, escalation_policy_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			RETURNING id`,
			m.TeamID, m.Name, m.Type, m.Config, m.IntervalS, m.RetryIntervalS, m.Retries, m.TimeoutS,
			m.Paused, m.GroupName, tagsOrEmpty(m.Tags), nullIfEmpty(m.PushToken), m.ParentID, m.EscalationPolicyID,
		).Scan(&id); err != nil {
			return err
		}
		return finishMonitorWrite(ctx, tx, m.TeamID, id, m.ChannelIDs, &out)
	})
	return out, mapErr(err)
}

// UpdateMonitor replaces the editable fields (and channel links) of monitor
// m.ID in m.TeamID. The type and push token are immutable.
func (s *Store) UpdateMonitor(ctx context.Context, m model.Monitor) (model.Monitor, error) {
	var out model.Monitor
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE upsera.monitors SET name = $3, config = $4, interval_s = $5, retry_interval_s = $6, retries = $7,
				timeout_s = $8, paused = $9, group_name = $10, tags = $11, parent_id = $12,
				escalation_policy_id = $13, updated_at = now()
			WHERE team_id = $1 AND id = $2`,
			m.TeamID, m.ID, m.Name, m.Config, m.IntervalS, m.RetryIntervalS, m.Retries, m.TimeoutS,
			m.Paused, m.GroupName, tagsOrEmpty(m.Tags), m.ParentID, m.EscalationPolicyID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return finishMonitorWrite(ctx, tx, m.TeamID, m.ID, m.ChannelIDs, &out)
	})
	return out, mapErr(err)
}

// finishMonitorWrite replaces monitor id's channel links and re-reads the
// row into out.
func finishMonitorWrite(ctx context.Context, tx pgx.Tx, teamID, id int64, channelIDs []int64, out *model.Monitor) error {
	if _, err := tx.Exec(ctx, "DELETE FROM upsera.monitor_channels WHERE monitor_id = $1", id); err != nil {
		return err
	}
	if len(channelIDs) > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO upsera.monitor_channels (monitor_id, channel_id)
			SELECT $1, c FROM unnest($2::bigint[]) AS c ON CONFLICT DO NOTHING`, id, channelIDs); err != nil {
			return err
		}
	}
	m, err := scanMonitor(tx.QueryRow(ctx, "SELECT "+monitorCols+" FROM upsera.monitors WHERE team_id = $1 AND id = $2", teamID, id))
	*out = m
	return err
}

// DeleteMonitor removes a monitor of teamID and, by cascade, its heartbeats.
func (s *Store) DeleteMonitor(ctx context.Context, teamID, id int64) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM upsera.monitors WHERE team_id = $1 AND id = $2", teamID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetMonitor loads one monitor of teamID.
func (s *Store) GetMonitor(ctx context.Context, teamID, id int64) (model.Monitor, error) {
	return scanMonitor(s.pool.QueryRow(ctx, "SELECT "+monitorCols+" FROM upsera.monitors WHERE team_id = $1 AND id = $2", teamID, id))
}

// ListMonitors returns teamID's monitors ordered by id.
func (s *Store) ListMonitors(ctx context.Context, teamID int64) ([]model.Monitor, error) {
	return collectMonitors(s.pool.Query(ctx, "SELECT "+monitorCols+" FROM upsera.monitors WHERE team_id = $1 ORDER BY id", teamID))
}

// ListAllMonitors returns every monitor on the instance, for the scheduler cache.
func (s *Store) ListAllMonitors(ctx context.Context) ([]model.Monitor, error) {
	return collectMonitors(s.pool.Query(ctx, "SELECT "+monitorCols+" FROM upsera.monitors ORDER BY id"))
}

// InsertHeartbeats bulk-inserts heartbeats with COPY. Heartbeats buffered
// for a monitor deleted before the flush are silently skipped: failing the
// batch would make the scheduler requeue it forever.
func (s *Store) InsertHeartbeats(ctx context.Context, hbs []model.Heartbeat) error {
	if len(hbs) == 0 {
		return nil
	}
	_, err := s.pool.CopyFrom(ctx, pgx.Identifier{Schema, "heartbeats"},
		[]string{"monitor_id", "probe_id", "time", "status", "latency_ms", "message"},
		pgx.CopyFromSlice(len(hbs), func(i int) ([]any, error) {
			h := hbs[i]
			return []any{h.MonitorID, h.ProbeID, h.Time, int16(h.Status), h.LatencyMs, sanitizeMessage(h.Message)}, nil
		}))
	if !isForeignKeyViolation(err) {
		return err
	}
	// Slow path, only after a delete raced a flush: insert the rows whose
	// monitor (and probe) still exist.
	n := len(hbs)
	mon, probe, at := make([]int64, n), make([]int64, n), make([]time.Time, n)
	status, lat, msg := make([]int16, n), make([]int32, n), make([]string, n)
	for i, h := range hbs {
		mon[i], probe[i], at[i] = h.MonitorID, h.ProbeID, h.Time
		status[i], lat[i], msg[i] = int16(h.Status), h.LatencyMs, sanitizeMessage(h.Message)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO upsera.heartbeats (monitor_id, probe_id, time, status, latency_ms, message)
		SELECT h.* FROM unnest($1::bigint[], $2::bigint[], $3::timestamptz[], $4::smallint[], $5::integer[], $6::text[])
			AS h(monitor_id, probe_id, time, status, latency_ms, message)
		WHERE EXISTS (SELECT 1 FROM upsera.monitors m WHERE m.id = h.monitor_id)
		  AND EXISTS (SELECT 1 FROM upsera.probes p WHERE p.id = h.probe_id)`,
		mon, probe, at, status, lat, msg)
	return err
}

// sanitizeMessage strips byte sequences Postgres text columns reject:
// invalid UTF-8 (COPY fails with "invalid byte sequence", SQLSTATE 22021)
// and embedded NUL (SQLSTATE 22P05). Without this, a single bad heartbeat
// message would make InsertHeartbeats fail and the scheduler requeue the
// whole batch forever.
func sanitizeMessage(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	return strings.ReplaceAll(s, "\x00", "")
}

// ListHeartbeats returns monitorID's heartbeats newest first, at most limit,
// optionally only those at or after since (zero means no lower bound).
func (s *Store) ListHeartbeats(ctx context.Context, monitorID int64, since time.Time, limit int) ([]model.Heartbeat, error) {
	var sincePtr *time.Time
	if !since.IsZero() {
		sincePtr = &since
	}
	rows, err := s.pool.Query(ctx, `
		SELECT monitor_id, probe_id, time, status, latency_ms, message FROM upsera.heartbeats
		WHERE monitor_id = $1 AND ($2::timestamptz IS NULL OR time >= $2)
		ORDER BY time DESC LIMIT $3`, monitorID, sincePtr, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Heartbeat, error) {
		var h model.Heartbeat
		err := r.Scan(&h.MonitorID, &h.ProbeID, &h.Time, &h.Status, &h.LatencyMs, &h.Message)
		return h, err
	})
}

// UpsertMonitorStates writes the given states in one batch. States for
// monitors deleted in the meantime are skipped.
func (s *Store) UpsertMonitorStates(ctx context.Context, states []model.MonitorState) error {
	if len(states) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, st := range states {
		b.Queue(`
			INSERT INTO upsera.monitor_state (monitor_id, status, since, last_check_at, consecutive_failures, tls_expires_at,
				flap_count, updated_at)
			SELECT $1, $2, $3, $4, $5, $6, $7, now()
			WHERE EXISTS (SELECT 1 FROM upsera.monitors WHERE id = $1)
			ON CONFLICT (monitor_id) DO UPDATE SET status = EXCLUDED.status, since = EXCLUDED.since,
				last_check_at = EXCLUDED.last_check_at, consecutive_failures = EXCLUDED.consecutive_failures,
				tls_expires_at = EXCLUDED.tls_expires_at, flap_count = EXCLUDED.flap_count, updated_at = now()`,
			st.MonitorID, int16(st.Status), st.Since, st.LastCheckAt, st.ConsecutiveFailures, st.TLSExpiresAt, st.FlapCount)
	}
	return s.pool.SendBatch(ctx, b).Close()
}

// ListMonitorStates returns the persisted state of every monitor.
func (s *Store) ListMonitorStates(ctx context.Context) ([]model.MonitorState, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT monitor_id, status, since, last_check_at, consecutive_failures, tls_expires_at, flap_count
		FROM upsera.monitor_state`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.MonitorState, error) {
		var st model.MonitorState
		err := r.Scan(&st.MonitorID, &st.Status, &st.Since, &st.LastCheckAt, &st.ConsecutiveFailures, &st.TLSExpiresAt,
			&st.FlapCount)
		return st, err
	})
}

// rollupUpsertSQL upserts uptime_daily rows for the heartbeats matching
// whereClause (a SQL condition on the "h" alias of upsera.heartbeats,
// starting placeholders at $2). $1 is always the IANA tz used to bucket
// h.time into calendar days. Maintenance heartbeats are not counted;
// pending counts as up; latency stats use only up heartbeats.
func rollupUpsertSQL(whereClause string) string {
	return `
		INSERT INTO upsera.uptime_daily (monitor_id, day, checks, up, avg_latency_ms, p95_latency_ms)
		SELECT h.monitor_id, (h.time AT TIME ZONE $1)::date,
			count(*) FILTER (WHERE h.status <> 3),
			count(*) FILTER (WHERE h.status IN (1, 2)),
			coalesce(avg(h.latency_ms) FILTER (WHERE h.status = 1), 0)::integer,
			coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY h.latency_ms) FILTER (WHERE h.status = 1), 0)::integer
		FROM upsera.heartbeats h
		WHERE ` + whereClause + `
		GROUP BY 1, 2
		ON CONFLICT (monitor_id, day) DO UPDATE SET checks = EXCLUDED.checks, up = EXCLUDED.up,
			avg_latency_ms = EXCLUDED.avg_latency_ms, p95_latency_ms = EXCLUDED.p95_latency_ms`
}

// Rollup recomputes uptime_daily for every day that may have changed since
// the last run: from the day before the newest rolled-up day (or the oldest
// heartbeat, on the first run) through today. Days are calendar days in tz
// (an IANA name such as "Asia/Jakarta").
func (s *Store) Rollup(ctx context.Context, tz string) (int64, error) {
	tag, err := s.pool.Exec(ctx, rollupUpsertSQL(`h.time >= coalesce(
			((SELECT max(day) FROM upsera.uptime_daily) - 1)::timestamp AT TIME ZONE $1,
			(SELECT min(time) FROM upsera.heartbeats)
		)`), tz)
	return tag.RowsAffected(), err
}

// PruneHeartbeats deletes heartbeats older than before in batches of
// batchSize, so no single statement holds locks for long. If a long outage
// let late heartbeats land after Rollup's watermark had already moved past
// their day, those days would otherwise never be rolled up before their
// heartbeats are gone; so, in the same transaction and before deleting
// anything, every day with a heartbeat older than before is (re-)rolled up
// using tz, guaranteeing pruned data is always reflected in uptime_daily.
func (s *Store) PruneHeartbeats(ctx context.Context, before time.Time, batchSize int, tz string) (int64, error) {
	var total int64
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, rollupUpsertSQL("h.time < $2"), tz, before); err != nil {
			return err
		}
		for {
			tag, err := tx.Exec(ctx, `
				DELETE FROM upsera.heartbeats WHERE ctid = ANY (ARRAY(
					SELECT ctid FROM upsera.heartbeats WHERE time < $1 LIMIT $2))`, before, batchSize)
			if err != nil {
				return err
			}
			total += tag.RowsAffected()
			if tag.RowsAffected() < int64(batchSize) {
				return nil
			}
		}
	})
	return total, err
}

// ListUptimeDaily returns monitorID's rollup rows with from <= day <= to
// (calendar dates, time of day ignored), oldest first.
func (s *Store) ListUptimeDaily(ctx context.Context, monitorID int64, from, to time.Time) ([]model.UptimeDay, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT monitor_id, day, checks, up, avg_latency_ms, p95_latency_ms FROM upsera.uptime_daily
		WHERE monitor_id = $1 AND day BETWEEN $2::date AND $3::date ORDER BY day`,
		monitorID, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.UptimeDay, error) {
		var d model.UptimeDay
		err := r.Scan(&d.MonitorID, &d.Day, &d.Checks, &d.Up, &d.AvgLatencyMs, &d.P95LatencyMs)
		return d, err
	})
}

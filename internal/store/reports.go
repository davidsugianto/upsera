package store

import (
	"context"
	"slices"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// DailyCounts returns, per monitor, the SLI tally and p95 latency of every
// calendar day (in loc) from fromDay to toDay inclusive (dates as
// model.DateIn returns them) that has heartbeats, oldest first. Days before
// model.RawDaysStart(now, loc) come from the uptime_daily rollup (whose up
// counts up, pending and degraded); that day and later come from raw
// heartbeats, so the rollup's lag never matters and no day is counted twice.
// The rollup must be bucketed in the same time zone as loc.
func (s *Store) DailyCounts(ctx context.Context, monitorIDs []int64, fromDay, toDay, now time.Time, loc *time.Location) (map[int64][]model.DayCount, error) {
	out := make(map[int64][]model.DayCount)
	if len(monitorIDs) == 0 || toDay.Before(fromDay) {
		return out, nil
	}
	rawStart := model.RawDaysStart(now, loc)
	splitDay := model.DateIn(rawStart, loc)

	if fromDay.Before(splitDay) {
		rows, err := s.pool.Query(ctx, `
			SELECT monitor_id, day, checks, up, p95_latency_ms FROM upsera.uptime_daily
			WHERE monitor_id = ANY($1) AND day >= $2::date AND day <= $3::date AND day < $4::date`,
			monitorIDs, fromDay.Format(time.DateOnly), toDay.Format(time.DateOnly), splitDay.Format(time.DateOnly))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var d model.DayCount
			if err := rows.Scan(&id, &d.Day, &d.Count.Valid, &d.Count.Good, &d.P95Ms); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = append(out[id], d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	if !toDay.Before(splitDay) {
		lo := rawStart
		if f := startOfDay(fromDay, loc); f.After(lo) {
			lo = f
		}
		hi := startOfDay(toDay.AddDate(0, 0, 1), loc)
		rows, err := s.pool.Query(ctx, `
			SELECT monitor_id, (time AT TIME ZONE $2)::date,
				count(*) FILTER (WHERE status <> 3), count(*) FILTER (WHERE status IN (1, 2, 4)),
				coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE status IN (1, 4)), 0)::integer
			FROM upsera.heartbeats
			WHERE monitor_id = ANY($1) AND time >= $3 AND time < $4
			GROUP BY 1, 2`, monitorIDs, loc.String(), lo, hi)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var d model.DayCount
			if err := rows.Scan(&id, &d.Day, &d.Count.Valid, &d.Count.Good, &d.P95Ms); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = append(out[id], d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	for id := range out {
		slices.SortFunc(out[id], func(a, b model.DayCount) int { return a.Day.Compare(b.Day) })
	}
	return out, nil
}

// startOfDay is 00:00 in loc of the date day (as model.DateIn returns it).
func startOfDay(day time.Time, loc *time.Location) time.Time {
	y, m, d := day.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// SLAAlertStats is one monitor's outage time and alert count in a period.
type SLAAlertStats struct {
	DowntimeS int64
	Alerts    int
}

// SLAAlerts returns, per monitor of teamID, the seconds of [start, end) that
// its alerts covered ([incident_start, resolved_at or now], clipped at both
// ends) and how many alerts started in [start, end). Monitors without any
// overlapping alert are absent.
func (s *Store) SLAAlerts(ctx context.Context, teamID int64, start, end, now time.Time) (map[int64]SLAAlertStats, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT monitor_id,
			coalesce(sum(GREATEST(0, extract(epoch FROM
				LEAST(coalesce(resolved_at, $4), $3) - GREATEST(incident_start, $2)))), 0)::bigint,
			count(*) FILTER (WHERE incident_start >= $2 AND incident_start < $3)
		FROM upsera.alerts
		WHERE team_id = $1 AND incident_start < $3 AND coalesce(resolved_at, $4) > $2
		GROUP BY monitor_id`, teamID, start, end, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]SLAAlertStats)
	for rows.Next() {
		var id int64
		var st SLAAlertStats
		if err := rows.Scan(&id, &st.DowntimeS, &st.Alerts); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

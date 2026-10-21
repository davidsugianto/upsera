package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davidsugianto/upsera/internal/model"
)

const sloCols = `s.id, s.team_id, s.monitor_id, s.target::float8, s.window_days, s.fast_burn, s.slow_burn,
	s.fast_firing_since, s.slow_firing_since, s.created_at, s.updated_at, m.name, m.paused`

func scanSLO(row pgx.Row) (model.SLO, error) {
	var s model.SLO
	err := row.Scan(&s.ID, &s.TeamID, &s.MonitorID, &s.Target, &s.WindowDays, &s.FastBurn, &s.SlowBurn,
		&s.FastFiringSince, &s.SlowFiringSince, &s.CreatedAt, &s.UpdatedAt, &s.MonitorName, &s.MonitorPaused)
	return s, mapErr(err)
}

func collectSLOs(rows pgx.Rows, err error) ([]model.SLO, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.SLO, error) { return scanSLO(r) })
}

// UpsertSLO creates or replaces the SLO of s.MonitorID (which must be a
// monitor of s.TeamID; the caller checks). Replacing it with a different
// target, window or thresholds clears the firing state and moves
// updated_at; the same values change nothing.
func (s *Store) UpsertSLO(ctx context.Context, in model.SLO) (model.SLO, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO upsera.slos AS s (team_id, monitor_id, target, window_days, fast_burn, slow_burn)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (monitor_id) DO UPDATE SET
			target = EXCLUDED.target, window_days = EXCLUDED.window_days,
			fast_burn = EXCLUDED.fast_burn, slow_burn = EXCLUDED.slow_burn,
			fast_firing_since = CASE WHEN (s.target, s.window_days, s.fast_burn, s.slow_burn) =
				(EXCLUDED.target, EXCLUDED.window_days, EXCLUDED.fast_burn, EXCLUDED.slow_burn) THEN s.fast_firing_since END,
			slow_firing_since = CASE WHEN (s.target, s.window_days, s.fast_burn, s.slow_burn) =
				(EXCLUDED.target, EXCLUDED.window_days, EXCLUDED.fast_burn, EXCLUDED.slow_burn) THEN s.slow_firing_since END,
			updated_at = CASE WHEN (s.target, s.window_days, s.fast_burn, s.slow_burn) =
				(EXCLUDED.target, EXCLUDED.window_days, EXCLUDED.fast_burn, EXCLUDED.slow_burn) THEN s.updated_at ELSE now() END
		RETURNING id`,
		in.TeamID, in.MonitorID, in.Target, in.WindowDays, in.FastBurn, in.SlowBurn).Scan(&id)
	if err != nil {
		return model.SLO{}, mapErr(err)
	}
	return s.GetSLO(ctx, in.TeamID, in.MonitorID)
}

// GetSLO returns the SLO of monitorID in teamID, or ErrNotFound.
func (s *Store) GetSLO(ctx context.Context, teamID, monitorID int64) (model.SLO, error) {
	return scanSLO(s.pool.QueryRow(ctx, `SELECT `+sloCols+`
		FROM upsera.slos s JOIN upsera.monitors m ON m.id = s.monitor_id
		WHERE s.team_id = $1 AND s.monitor_id = $2`, teamID, monitorID))
}

// DeleteSLO removes the SLO of monitorID in teamID, or returns ErrNotFound.
func (s *Store) DeleteSLO(ctx context.Context, teamID, monitorID int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM upsera.slos WHERE team_id = $1 AND monitor_id = $2`, teamID, monitorID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListSLOs returns teamID's SLOs sorted by monitor name.
func (s *Store) ListSLOs(ctx context.Context, teamID int64) ([]model.SLO, error) {
	return collectSLOs(s.pool.Query(ctx, `SELECT `+sloCols+`
		FROM upsera.slos s JOIN upsera.monitors m ON m.id = s.monitor_id
		WHERE s.team_id = $1 ORDER BY m.name, m.id`, teamID))
}

// ListAllSLOs returns every team's SLOs (for the burn-rate evaluator).
func (s *Store) ListAllSLOs(ctx context.Context) ([]model.SLO, error) {
	return collectSLOs(s.pool.Query(ctx, `SELECT `+sloCols+`
		FROM upsera.slos s JOIN upsera.monitors m ON m.id = s.monitor_id ORDER BY s.monitor_id`))
}

// SetSLOFiring stores the firing state of monitorID's SLO, unless the SLO
// was replaced since the version updatedAt (then it is a no-op: the new
// version starts with a clear state). updated_at is left alone.
func (s *Store) SetSLOFiring(ctx context.Context, monitorID int64, updatedAt time.Time, fast, slow *time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE upsera.slos SET fast_firing_since = $3, slow_firing_since = $4
		WHERE monitor_id = $1 AND updated_at = $2`, monitorID, updatedAt, fast, slow)
	return err
}

// BurnCounts tallies the heartbeats of monitorIDs in (since[i], now] for
// every since, in one grouped query (one pair of FILTER columns per window).
// Monitors without heartbeats in the widest window are absent.
func (s *Store) BurnCounts(ctx context.Context, monitorIDs []int64, now time.Time, since []time.Time) (map[int64][]model.SLICount, error) {
	out := make(map[int64][]model.SLICount)
	if len(monitorIDs) == 0 || len(since) == 0 {
		return out, nil
	}
	args := []any{monitorIDs, now, slices.MinFunc(since, func(a, b time.Time) int { return a.Compare(b) })}
	var cols strings.Builder
	for _, t := range since {
		args = append(args, t)
		n := len(args)
		fmt.Fprintf(&cols, `,
			count(*) FILTER (WHERE time > $%d AND status <> 3),
			count(*) FILTER (WHERE time > $%d AND status IN (1, 2, 4))`, n, n)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT monitor_id`+cols.String()+`
		FROM upsera.heartbeats
		WHERE monitor_id = ANY($1) AND time > $3 AND time <= $2
		GROUP BY monitor_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		vals := make([]int64, 2*len(since))
		dest := []any{&id}
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		cs := make([]model.SLICount, len(since))
		for i := range cs {
			cs[i] = model.SLICount{Valid: int(vals[2*i]), Good: int(vals[2*i+1])}
		}
		out[id] = cs
	}
	return out, rows.Err()
}

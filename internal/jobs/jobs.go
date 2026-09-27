// Package jobs runs the periodic maintenance tasks: the daily uptime
// rollup, heartbeat retention pruning and expired-session cleanup.
package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

const pruneBatchSize = 10000

// Store is the persistence the maintenance job needs. *store.Store
// satisfies this.
type Store interface {
	Rollup(ctx context.Context, tz string) (int64, error)
	PruneHeartbeats(ctx context.Context, before time.Time, batch int, tz string) (int64, error)
	DeleteExpiredSessions(ctx context.Context) (int64, error)
	GetInstanceSettings(ctx context.Context) (model.InstanceSettings, error)
}

// Options configures Run.
type Options struct {
	Every                time.Duration
	DefaultRetentionDays int
	TZ                   string
	Logger               *slog.Logger
	// OnSettings, if set, is called with the freshly loaded instance
	// settings after every successful load.
	OnSettings func(model.InstanceSettings)
}

// Run performs the rollup, heartbeat retention pruning and expired-session
// cleanup once immediately and then every opts.Every, until ctx is done.
// It never returns early on error: each step is logged and the loop
// continues.
func Run(ctx context.Context, st Store, opts Options) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	every := opts.Every
	if every <= 0 {
		every = time.Hour
	}

	tick(ctx, st, opts, log)

	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick(ctx, st, opts, log)
		}
	}
}

// tick runs one pass: load settings, roll up, prune (only if the rollup
// succeeded) and delete expired sessions, in that order.
func tick(ctx context.Context, st Store, opts Options, log *slog.Logger) {
	retentionDays := opts.DefaultRetentionDays
	settings, err := st.GetInstanceSettings(ctx)
	switch {
	case err != nil:
		log.Warn("jobs: load instance settings failed, using default retention", "error", err, "default_retention_days", retentionDays)
	default:
		if settings.RetentionDays != nil {
			retentionDays = *settings.RetentionDays
		}
		if opts.OnSettings != nil {
			opts.OnSettings(settings)
		}
	}

	rows, err := st.Rollup(ctx, opts.TZ)
	switch {
	case err != nil:
		log.Warn("jobs: rollup failed", "error", err)
	default:
		log.Info("jobs: rollup complete", "rows", rows)
		before := retentionCutoff(opts.TZ, retentionDays)
		pruned, err := st.PruneHeartbeats(ctx, before, pruneBatchSize, opts.TZ)
		if err != nil {
			log.Warn("jobs: prune heartbeats failed", "error", err)
		} else {
			log.Info("jobs: pruned heartbeats", "rows", pruned, "retention_days", retentionDays)
		}
	}

	expired, err := st.DeleteExpiredSessions(ctx)
	if err != nil {
		log.Warn("jobs: delete expired sessions failed", "error", err)
	} else {
		log.Info("jobs: deleted expired sessions", "rows", expired)
	}
}

// retentionCutoff returns the instant retentionDays before the most recent
// local midnight in tz, so retention always covers whole calendar days
// regardless of what time of day the job happens to run (unlike
// time.Now().Add(-N*24h), which slowly drifts the cutoff earlier every day
// the job starts a little late). tz is assumed valid: config validates it
// at startup.
func retentionCutoff(tz string, retentionDays int) time.Time {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	return midnight.AddDate(0, 0, -retentionDays)
}

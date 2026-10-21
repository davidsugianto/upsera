package slo

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// Store is what the evaluator reads and writes. *store.Store satisfies this.
type Store interface {
	// ListAllSLOs returns every SLO with its monitor's name and paused flag.
	ListAllSLOs(ctx context.Context) ([]model.SLO, error)
	// BurnCounts tallies the heartbeats of monitorIDs in (since[i], now],
	// for every since, in one grouped query.
	BurnCounts(ctx context.Context, monitorIDs []int64, now time.Time, since []time.Time) (map[int64][]model.SLICount, error)
	// DailyCounts returns the per-day tallies from fromDay to toDay.
	DailyCounts(ctx context.Context, monitorIDs []int64, fromDay, toDay, now time.Time, loc *time.Location) (map[int64][]model.DayCount, error)
	// SetSLOFiring persists the firing state, unless the SLO was changed
	// (its updated_at moved) since it was loaded.
	SetSLOFiring(ctx context.Context, monitorID int64, updatedAt time.Time, fast, slow *time.Time) error
}

// Notifier receives burn-rate alerts. *alerting.Engine satisfies this; its
// methods only queue work and never block. consumed is the window's budget
// consumed, or -1 when it could not be computed.
type Notifier interface {
	SLOBurn(monitorID int64, level string, target, burn, consumed float64, at time.Time)
	SLOBurnRecovered(monitorID int64, level string, target, burn, consumed float64, at time.Time)
}

// Windows are the multi-window burn-rate windows: an alert fires when both
// its long and short window burn at or above the threshold, and clears when
// the short one drops below it.
type Windows struct {
	FastLong, FastShort, SlowLong, SlowShort time.Duration
}

// DefaultWindows are the Google SRE workbook's: 1h/5m (fast), 6h/30m (slow).
var DefaultWindows = Windows{FastLong: time.Hour, FastShort: 5 * time.Minute, SlowLong: 6 * time.Hour, SlowShort: 30 * time.Minute}

// Options configures an Evaluator.
type Options struct {
	Store    Store
	Notifier Notifier      // nil = log only
	Every    time.Duration // default 1 minute
	// Windows default to DefaultWindows; tests scale them down.
	Windows Windows
	Loc     *time.Location // calendar days of the SLO window (default UTC)
	Logger  *slog.Logger
}

// firing is one SLO's firing state as last known.
type firing struct {
	fast, slow *time.Time
	updatedAt  time.Time // the SLO version it belongs to
}

// Evaluator checks every SLO's burn rates on a timer and notifies when a
// fast or slow burn-rate alert starts or stops firing. It is a loop of its
// own: nothing on the check or alert path waits for it.
type Evaluator struct {
	opts Options
	log  *slog.Logger
	now  func() time.Time

	// mem is the firing state this evaluator decided, per monitor; it wins
	// over the stored state of the same SLO version, so a failed write does
	// not make the next tick notify again.
	mem     map[int64]firing
	failing bool

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New builds an Evaluator. Call Start to run it.
func New(opts Options) *Evaluator {
	if opts.Every <= 0 {
		opts.Every = time.Minute
	}
	if opts.Windows == (Windows{}) {
		opts.Windows = DefaultWindows
	}
	if opts.Loc == nil {
		opts.Loc = time.UTC
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Evaluator{opts: opts, log: log, now: time.Now, mem: make(map[int64]firing)}
}

// Start runs the evaluator until ctx is done or Stop is called.
func (e *Evaluator) Start(ctx context.Context) {
	ctx, e.cancel = context.WithCancel(ctx)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		t := time.NewTicker(e.opts.Every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				e.tick(ctx)
			}
		}
	}()
}

// Stop stops the loop and waits for a running tick to finish.
func (e *Evaluator) Stop() {
	if e.cancel != nil {
		e.cancel()
	}
	e.wg.Wait()
}

// tick evaluates every unpaused SLO once. A store error skips the tick
// (logged once per failure streak).
func (e *Evaluator) tick(ctx context.Context) {
	now := e.now()
	slos, err := e.opts.Store.ListAllSLOs(ctx)
	if err != nil {
		e.fail(ctx, err)
		return
	}
	var active []model.SLO
	ids := make([]int64, 0, len(slos))
	for _, s := range slos {
		if !s.MonitorPaused {
			active = append(active, s)
			ids = append(ids, s.MonitorID)
		}
	}
	if len(active) == 0 {
		e.ok()
		e.mem = make(map[int64]firing)
		return
	}
	w := e.opts.Windows
	counts, err := e.opts.Store.BurnCounts(ctx, ids, now,
		[]time.Time{now.Add(-w.FastLong), now.Add(-w.FastShort), now.Add(-w.SlowLong), now.Add(-w.SlowShort)})
	if err != nil {
		e.fail(ctx, err)
		return
	}
	e.ok()

	seen := make(map[int64]bool, len(slos))
	for _, s := range slos {
		seen[s.MonitorID] = true
	}
	for id := range e.mem {
		if !seen[id] {
			delete(e.mem, id)
		}
	}
	for _, s := range active {
		e.evaluate(ctx, s, counts[s.MonitorID], now)
	}
}

// evaluate applies one SLO's burn counts (FastLong, FastShort, SlowLong,
// SlowShort) at now.
func (e *Evaluator) evaluate(ctx context.Context, s model.SLO, c []model.SLICount, now time.Time) {
	st := firing{fast: s.FastFiringSince, slow: s.SlowFiringSince, updatedAt: s.UpdatedAt}
	if m, ok := e.mem[s.MonitorID]; ok && m.updatedAt.Equal(s.UpdatedAt) {
		st = m
	}
	burn := func(i int) (float64, bool) {
		if i >= len(c) {
			return 0, false
		}
		return BurnRate(c[i], s.Target)
	}
	next := st
	consumed := func() float64 { return e.consumed(ctx, s, now) }
	var changed bool
	step := func(level string, cur **time.Time, threshold float64, long, short int) {
		lv, lok := burn(long)
		sv, sok := burn(short)
		switch {
		case *cur == nil && lok && sok && lv >= threshold && sv >= threshold:
			*cur = new(now)
			changed = true
			e.notify(false, s, level, lv, consumed(), now)
		case *cur != nil && sok && sv < threshold:
			// No data in the short window (maintenance, no checks) keeps
			// the state: there is nothing to say it recovered.
			*cur = nil
			changed = true
			e.notify(true, s, level, sv, consumed(), now)
		}
	}
	step(string(model.SLOFiringFast), &next.fast, s.FastBurn, 0, 1)
	step(string(model.SLOFiringSlow), &next.slow, s.SlowBurn, 2, 3)
	if !changed {
		return
	}
	// Notify first, persist second: after a failed write a restart may
	// notify again, but nothing is lost.
	e.mem[s.MonitorID] = next
	if err := e.opts.Store.SetSLOFiring(ctx, s.MonitorID, s.UpdatedAt, next.fast, next.slow); err != nil && ctx.Err() == nil {
		e.log.Warn("slo: persisting firing state failed", "monitor_id", s.MonitorID, "error", err)
	}
}

// consumed is s's budget consumed over its window, or -1.
func (e *Evaluator) consumed(ctx context.Context, s model.SLO, now time.Time) float64 {
	today := model.DateIn(now, e.opts.Loc)
	days, err := e.opts.Store.DailyCounts(ctx, []int64{s.MonitorID}, today.AddDate(0, 0, 1-s.WindowDays), today, now, e.opts.Loc)
	if err != nil {
		return -1
	}
	v, ok := Consumed(Sum(days[s.MonitorID]), s.Target)
	if !ok {
		return -1
	}
	return v
}

func (e *Evaluator) notify(recovered bool, s model.SLO, level string, burn, consumed float64, at time.Time) {
	if e.opts.Notifier == nil {
		e.log.Info("slo: burn rate", "monitor_id", s.MonitorID, "level", level, "recovered", recovered, "burn", burn)
		return
	}
	if recovered {
		e.opts.Notifier.SLOBurnRecovered(s.MonitorID, level, s.Target, burn, consumed, at)
	} else {
		e.opts.Notifier.SLOBurn(s.MonitorID, level, s.Target, burn, consumed, at)
	}
}

func (e *Evaluator) fail(ctx context.Context, err error) {
	if !e.failing && ctx.Err() == nil {
		e.log.Warn("slo: evaluation skipped, store unavailable", "error", err)
	}
	e.failing = true
}

func (e *Evaluator) ok() {
	if e.failing {
		e.log.Info("slo: store available again")
	}
	e.failing = false
}

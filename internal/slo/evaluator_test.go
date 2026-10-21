package slo

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

type fakeStore struct {
	mu        sync.Mutex
	slos      map[int64]model.SLO
	counts    map[int64][]model.SLICount // per monitor: FastLong, FastShort, SlowLong, SlowShort
	days      map[int64][]model.DayCount
	failList  error
	failBurn  error
	failSet   error
	burnCalls int
	sets      int
	sinces    []time.Time
}

func (f *fakeStore) ListAllSLOs(context.Context) ([]model.SLO, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failList != nil {
		return nil, f.failList
	}
	var out []model.SLO
	for _, s := range f.slos {
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeStore) BurnCounts(_ context.Context, ids []int64, _ time.Time, since []time.Time) (map[int64][]model.SLICount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.burnCalls++
	f.sinces = since
	if f.failBurn != nil {
		return nil, f.failBurn
	}
	out := map[int64][]model.SLICount{}
	for _, id := range ids {
		if c, ok := f.counts[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

func (f *fakeStore) DailyCounts(_ context.Context, ids []int64, _, _, _ time.Time, _ *time.Location) (map[int64][]model.DayCount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int64][]model.DayCount{}
	for _, id := range ids {
		out[id] = f.days[id]
	}
	return out, nil
}

func (f *fakeStore) SetSLOFiring(_ context.Context, monitorID int64, updatedAt time.Time, fast, slow *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets++
	if f.failSet != nil {
		return f.failSet
	}
	s, ok := f.slos[monitorID]
	if !ok || !s.UpdatedAt.Equal(updatedAt) {
		return nil
	}
	s.FastFiringSince, s.SlowFiringSince = fast, slow
	f.slos[monitorID] = s
	return nil
}

func (f *fakeStore) setCounts(id int64, c ...model.SLICount) {
	f.mu.Lock()
	f.counts[id] = c
	f.mu.Unlock()
}

type note struct {
	recovered bool
	monitorID int64
	level     string
	target    float64
	burn      float64
	consumed  float64
}

type fakeNotifier struct {
	mu    sync.Mutex
	notes []note
}

func (n *fakeNotifier) SLOBurn(id int64, level string, target, burn, consumed float64, _ time.Time) {
	n.mu.Lock()
	n.notes = append(n.notes, note{false, id, level, target, burn, consumed})
	n.mu.Unlock()
}

func (n *fakeNotifier) SLOBurnRecovered(id int64, level string, target, burn, consumed float64, _ time.Time) {
	n.mu.Lock()
	n.notes = append(n.notes, note{true, id, level, target, burn, consumed})
	n.mu.Unlock()
}

func (n *fakeNotifier) all() []note {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]note(nil), n.notes...)
}

var t0 = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

func newEval(t *testing.T, slos ...model.SLO) (*Evaluator, *fakeStore, *fakeNotifier) {
	t.Helper()
	fs := &fakeStore{slos: map[int64]model.SLO{}, counts: map[int64][]model.SLICount{}, days: map[int64][]model.DayCount{}}
	for _, s := range slos {
		fs.slos[s.MonitorID] = s
	}
	n := &fakeNotifier{}
	e := New(Options{Store: fs, Notifier: n, Loc: time.UTC, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	e.now = func() time.Time { return t0 }
	return e, fs, n
}

func sloFor(id int64) model.SLO {
	return model.SLO{ID: id, TeamID: 1, MonitorID: id, Target: 99.9, WindowDays: 30, FastBurn: 14.4, SlowBurn: 6,
		UpdatedAt: t0.Add(-time.Hour), MonitorName: "m"}
}

// Error ratios: bad = burn 1000 at 99.9; fine = burn 0; mid = burn 10 (fires
// slow, not fast).
var (
	bad  = model.SLICount{Good: 0, Valid: 10}
	fine = model.SLICount{Good: 10, Valid: 10}
	mid  = model.SLICount{Good: 990, Valid: 1000}
	none = model.SLICount{}
)

func TestEvaluatorFiresOnceAndClears(t *testing.T) {
	e, fs, n := newEval(t, sloFor(1))
	ctx := context.Background()
	fs.days[1] = []model.DayCount{{Day: t0, Count: model.SLICount{Good: 990, Valid: 1000}}}

	fs.setCounts(1, bad, bad, fine, fine) // fast only
	e.tick(ctx)
	e.tick(ctx) // still firing: quiet
	got := n.all()
	if len(got) != 1 || got[0].recovered || got[0].level != "fast" || !approx(got[0].burn, 1000) || got[0].target != 99.9 ||
		!approx(got[0].consumed, 10) {
		t.Fatalf("notes = %+v, want one fast burn", got)
	}
	if s := fs.slos[1]; s.FastFiringSince == nil || !s.FastFiringSince.Equal(t0) || s.SlowFiringSince != nil {
		t.Fatalf("persisted = %+v", s)
	}

	// The long window is still bad but the short one recovered: it clears.
	fs.setCounts(1, bad, fine, fine, fine)
	e.tick(ctx)
	got = n.all()
	if len(got) != 2 || !got[1].recovered || got[1].level != "fast" || got[1].burn != 0 {
		t.Fatalf("notes = %+v, want fast recovered", got)
	}
	if fs.slos[1].FastFiringSince != nil {
		t.Fatal("fast firing state not cleared")
	}
	e.tick(ctx)
	if len(n.all()) != 2 {
		t.Fatal("recovered twice")
	}
}

func TestEvaluatorBothWindowsRequired(t *testing.T) {
	e, fs, n := newEval(t, sloFor(1))
	fs.setCounts(1, bad, fine, bad, fine) // long windows burning, short ones not
	e.tick(context.Background())
	fs.setCounts(1, fine, bad, fine, bad) // short windows burning, long ones not
	e.tick(context.Background())
	fs.setCounts(1, bad, none, bad, none) // no data in the short windows
	e.tick(context.Background())
	if got := n.all(); len(got) != 0 {
		t.Fatalf("notes = %+v, want none", got)
	}
}

func TestEvaluatorFastAndSlowIndependent(t *testing.T) {
	e, fs, n := newEval(t, sloFor(1))
	ctx := context.Background()
	fs.setCounts(1, mid, mid, mid, mid) // burn 10: slow fires, fast does not
	e.tick(ctx)
	if got := n.all(); len(got) != 1 || got[0].level != "slow" || got[0].recovered {
		t.Fatalf("notes = %+v, want slow burn", got)
	}
	fs.setCounts(1, bad, bad, mid, mid) // now fast too
	e.tick(ctx)
	if got := n.all(); len(got) != 2 || got[1].level != "fast" {
		t.Fatalf("notes = %+v, want fast burn", got)
	}
	fs.setCounts(1, bad, fine, mid, mid) // fast clears, slow keeps firing
	e.tick(ctx)
	got := n.all()
	if len(got) != 3 || got[2].level != "fast" || !got[2].recovered {
		t.Fatalf("notes = %+v, want fast recovered only", got)
	}
	if s := fs.slos[1]; s.FastFiringSince != nil || s.SlowFiringSince == nil {
		t.Fatalf("persisted = %+v", s)
	}
}

func TestEvaluatorRestartDoesNotResend(t *testing.T) {
	s := sloFor(1)
	s.FastFiringSince = new(t0.Add(-time.Hour))
	e, fs, n := newEval(t, s)
	fs.setCounts(1, bad, bad, fine, fine)
	e.tick(context.Background())
	if got := n.all(); len(got) != 0 {
		t.Fatalf("restart re-sent: %+v", got)
	}
	if fs.sets != 0 {
		t.Fatalf("persisted %d times without a change", fs.sets)
	}
}

func TestEvaluatorStoreErrors(t *testing.T) {
	e, fs, n := newEval(t, sloFor(1))
	ctx := context.Background()
	fs.setCounts(1, bad, bad, fine, fine)

	fs.failList = errors.New("db down")
	e.tick(ctx)
	fs.failList, fs.failBurn = nil, errors.New("db down")
	e.tick(ctx)
	if len(n.all()) != 0 {
		t.Fatal("notified despite store errors")
	}
	fs.failBurn = nil
	e.tick(ctx)
	if len(n.all()) != 1 {
		t.Fatalf("notes = %+v after recovery, want one", n.all())
	}
}

func TestEvaluatorPersistFailureKeepsGoing(t *testing.T) {
	e, fs, n := newEval(t, sloFor(1))
	ctx := context.Background()
	fs.failSet = errors.New("write failed")
	fs.setCounts(1, bad, bad, fine, fine)
	e.tick(ctx)
	e.tick(ctx) // the in-memory state remembers it fired
	if got := n.all(); len(got) != 1 {
		t.Fatalf("notes = %+v, want exactly one (notify first, persist second)", got)
	}
}

func TestEvaluatorSkipsPausedAndBatches(t *testing.T) {
	paused := sloFor(2)
	paused.MonitorPaused = true
	e, fs, n := newEval(t, sloFor(1), paused, sloFor(3), sloFor(4))
	for id := int64(1); id <= 4; id++ {
		fs.setCounts(id, bad, bad, fine, fine)
	}
	e.tick(context.Background())
	if fs.burnCalls != 1 {
		t.Fatalf("BurnCounts called %d times for 4 SLOs, want 1", fs.burnCalls)
	}
	for _, nt := range n.all() {
		if nt.monitorID == 2 {
			t.Fatalf("paused monitor notified: %+v", nt)
		}
	}
	if len(n.all()) != 3 {
		t.Fatalf("notes = %+v, want 3", n.all())
	}
	want := []time.Time{t0.Add(-time.Hour), t0.Add(-5 * time.Minute), t0.Add(-6 * time.Hour), t0.Add(-30 * time.Minute)}
	for i, w := range want {
		if !fs.sinces[i].Equal(w) {
			t.Fatalf("since[%d] = %v, want %v (default windows)", i, fs.sinces[i], w)
		}
	}
}

func TestEvaluatorConfigChangeClearsMemory(t *testing.T) {
	e, fs, n := newEval(t, sloFor(1))
	ctx := context.Background()
	fs.setCounts(1, bad, bad, fine, fine)
	e.tick(ctx)
	// A PUT replaces the SLO: the firing state is cleared and updated_at moves.
	s := fs.slos[1]
	s.FastFiringSince, s.UpdatedAt = nil, t0
	fs.slos[1] = s
	e.tick(ctx)
	if got := n.all(); len(got) != 2 || got[1].recovered {
		t.Fatalf("notes = %+v, want a fresh fire after the SLO changed", got)
	}
}

func TestEvaluatorLoop(t *testing.T) {
	fs := &fakeStore{slos: map[int64]model.SLO{1: sloFor(1)}, counts: map[int64][]model.SLICount{1: {bad, bad, fine, fine}},
		failList: errors.New("db down")}
	n := &fakeNotifier{}
	e := New(Options{Store: fs, Notifier: n, Every: 5 * time.Millisecond,
		Windows: Windows{FastLong: time.Minute, FastShort: time.Second, SlowLong: time.Hour, SlowShort: time.Minute},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil))})
	e.Start(context.Background())
	time.Sleep(30 * time.Millisecond) // failing ticks: the loop survives
	fs.mu.Lock()
	fs.failList = nil
	fs.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for len(n.all()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("loop never notified")
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.Stop()
	fs.mu.Lock()
	since := fs.sinces
	calls := fs.burnCalls
	fs.mu.Unlock()
	if d := since[0].Sub(since[1]); d != -time.Minute+time.Second {
		t.Fatalf("scaled windows not used: %v", since)
	}
	time.Sleep(20 * time.Millisecond)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.burnCalls != calls {
		t.Fatal("loop still running after Stop")
	}
}

func TestEvaluatorNilNotifier(t *testing.T) {
	fs := &fakeStore{slos: map[int64]model.SLO{1: sloFor(1)}, counts: map[int64][]model.SLICount{1: {bad, bad, fine, fine}}}
	e := New(Options{Store: fs, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	e.tick(context.Background()) // logs only, no panic
	if fs.slos[1].FastFiringSince == nil {
		t.Fatal("state not persisted without a notifier")
	}
}

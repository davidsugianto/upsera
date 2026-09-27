package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/checker"
	"github.com/davidsugianto/upsera/internal/model"
)

// eventually polls cond until it is true or timeout elapses, failing the
// test if it never becomes true.
func eventually(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %s", timeout)
	}
}

func noJitter(time.Duration) time.Duration { return 0 }

func TestSchedulerJitteredStartDelaysFirstCheck(t *testing.T) {
	fs := newFakeStore()
	fc := &fakeChecker{result: checker.Result{Status: model.StatusUp}}
	sched := New(fs, fc, Options{ProbeID: 1, MaxConcurrent: 4, BufferSize: 100, FlushInterval: time.Hour})
	sched.unit = time.Millisecond
	wantWait := 150 * time.Millisecond
	sched.jitter = func(time.Duration) time.Duration { return wantWait }

	m := model.Monitor{ID: 1, Type: model.TypeHTTP, IntervalS: 1000, RetryIntervalS: 20, Retries: 1}
	start := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx, []model.Monitor{m}, nil)
	defer func() { _ = sched.Shutdown(context.Background()) }()

	time.Sleep(wantWait / 2)
	if got := fc.callCount(); got != 0 {
		t.Fatalf("check ran before jitter elapsed: calls=%d", got)
	}
	eventually(t, wantWait+300*time.Millisecond, func() bool { return fc.callCount() >= 1 })
	if elapsed := time.Since(start); elapsed < wantWait-20*time.Millisecond {
		t.Fatalf("first check ran too early: elapsed=%s want>=%s", elapsed, wantWait)
	}
}

func TestSchedulerUpsertRunsFirstCheckImmediately(t *testing.T) {
	fs := newFakeStore()
	fc := &fakeChecker{result: checker.Result{Status: model.StatusUp}}
	sched := New(fs, fc, Options{ProbeID: 1, MaxConcurrent: 4, BufferSize: 100, FlushInterval: time.Hour})
	sched.unit = time.Millisecond
	// If Upsert wrongly jittered its first check, this would delay it far
	// beyond the assertion window below.
	sched.jitter = func(time.Duration) time.Duration { return 500 * time.Millisecond }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx, nil, nil)
	defer func() { _ = sched.Shutdown(context.Background()) }()

	m := model.Monitor{ID: 2, Type: model.TypeHTTP, IntervalS: 1000, RetryIntervalS: 20, Retries: 1}
	sched.Upsert(m)

	eventually(t, 100*time.Millisecond, func() bool { return fc.callCount() >= 1 })
}

func TestSchedulerWorkerConcurrencyCap(t *testing.T) {
	fs := newFakeStore()
	fc := &fakeChecker{result: checker.Result{Status: model.StatusUp}, delay: 30 * time.Millisecond}
	sched := New(fs, fc, Options{ProbeID: 1, MaxConcurrent: 2, BufferSize: 1000, FlushInterval: time.Hour})
	sched.unit = time.Millisecond
	sched.jitter = noJitter

	var monitors []model.Monitor
	for i := int64(1); i <= 6; i++ {
		monitors = append(monitors, model.Monitor{ID: i, Type: model.TypeHTTP, IntervalS: 20, RetryIntervalS: 20, Retries: 1})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx, monitors, nil)
	defer func() { _ = sched.Shutdown(context.Background()) }()

	eventually(t, 800*time.Millisecond, func() bool { return fc.callCount() >= 12 })
	if got := fc.maxSeenConcurrent(); got > 2 {
		t.Fatalf("max observed concurrent checks = %d, want <= 2", got)
	}
}

func TestSchedulerRetryIntervalThenNormalInterval(t *testing.T) {
	fs := newFakeStore()
	fc := &fakeChecker{fn: func(_ model.Monitor, call int) checker.Result {
		if call <= 2 {
			return checker.Result{Status: model.StatusDown, Message: "down"}
		}
		return checker.Result{Status: model.StatusUp}
	}}
	sched := New(fs, fc, Options{ProbeID: 1, MaxConcurrent: 4, BufferSize: 100, FlushInterval: time.Hour})
	sched.unit = time.Millisecond
	sched.jitter = noJitter

	m := model.Monitor{ID: 1, Type: model.TypeHTTP, IntervalS: 300, RetryIntervalS: 20, Retries: 2}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx, []model.Monitor{m}, nil)
	defer func() { _ = sched.Shutdown(context.Background()) }()

	eventually(t, 2*time.Second, func() bool { return fc.callCount() >= 4 })
	times := fc.times()
	if len(times) < 4 {
		t.Fatalf("not enough calls recorded: %d", len(times))
	}
	retryGap1 := times[1].Sub(times[0])
	retryGap2 := times[2].Sub(times[1])
	normalGap := times[3].Sub(times[2])
	const retryCeiling = 150 * time.Millisecond // well under the 300ms normal interval
	if retryGap1 >= retryCeiling {
		t.Fatalf("gap after 1st failure = %s, want < %s (retry interval)", retryGap1, retryCeiling)
	}
	if retryGap2 >= retryCeiling {
		t.Fatalf("gap after 2nd failure = %s, want < %s (retry interval)", retryGap2, retryCeiling)
	}
	if normalGap <= retryCeiling {
		t.Fatalf("gap after recovery = %s, want >= %s (normal interval)", normalGap, retryCeiling)
	}
}

func TestSchedulerUpsertPausedStopsChecks(t *testing.T) {
	fs := newFakeStore()
	fc := &fakeChecker{result: checker.Result{Status: model.StatusUp}}
	sched := New(fs, fc, Options{ProbeID: 1, MaxConcurrent: 4, BufferSize: 100, FlushInterval: time.Hour})
	sched.unit = time.Millisecond
	sched.jitter = noJitter

	m := model.Monitor{ID: 1, Type: model.TypeHTTP, IntervalS: 20, RetryIntervalS: 20, Retries: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx, []model.Monitor{m}, nil)
	defer func() { _ = sched.Shutdown(context.Background()) }()

	eventually(t, 300*time.Millisecond, func() bool { return fc.callCount() >= 2 })

	paused := m
	paused.Paused = true
	sched.Upsert(paused)

	countAtPause := fc.callCount()
	time.Sleep(150 * time.Millisecond)
	if got := fc.callCount(); got > countAtPause+1 { // one already-in-flight check may still land
		t.Fatalf("checks continued after pause: at_pause=%d after=%d", countAtPause, got)
	}
}

func TestSchedulerRemoveStopsChecks(t *testing.T) {
	fs := newFakeStore()
	fc := &fakeChecker{result: checker.Result{Status: model.StatusUp}}
	sched := New(fs, fc, Options{ProbeID: 1, MaxConcurrent: 4, BufferSize: 100, FlushInterval: time.Hour})
	sched.unit = time.Millisecond
	sched.jitter = noJitter

	m := model.Monitor{ID: 1, Type: model.TypeHTTP, IntervalS: 20, RetryIntervalS: 20, Retries: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx, []model.Monitor{m}, nil)
	defer func() { _ = sched.Shutdown(context.Background()) }()

	eventually(t, 300*time.Millisecond, func() bool { return fc.callCount() >= 2 })

	sched.Remove(m.ID)
	if _, ok := sched.State(m.ID); ok {
		t.Fatalf("State still present after Remove")
	}

	countAtRemove := fc.callCount()
	time.Sleep(150 * time.Millisecond)
	if got := fc.callCount(); got > countAtRemove+1 {
		t.Fatalf("checks continued after Remove: at_remove=%d after=%d", countAtRemove, got)
	}
}

func TestSchedulerPushAcceptedUnknownPaused(t *testing.T) {
	fs := newFakeStore()
	fc := &fakeChecker{}
	sched := New(fs, fc, Options{ProbeID: 7, MaxConcurrent: 4, BufferSize: 100, FlushInterval: time.Hour})
	sched.unit = time.Millisecond

	m := model.Monitor{ID: 9, Type: model.TypePush, PushToken: "tok123", IntervalS: 10000, RetryIntervalS: 20, Retries: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx, []model.Monitor{m}, nil)
	defer func() { _ = sched.Shutdown(context.Background()) }()

	if err := sched.Push("no-such-token", model.StatusUp, "", 5); !errors.Is(err, ErrUnknownPushToken) {
		t.Fatalf("Push(unknown) err = %v, want ErrUnknownPushToken", err)
	}

	if err := sched.Push("tok123", model.StatusUp, "", 12); err != nil {
		t.Fatalf("Push(valid) err = %v, want nil", err)
	}
	st, ok := sched.State(m.ID)
	if !ok || st.Status != model.StatusUp {
		t.Fatalf("State after push = %+v (ok=%v), want status up", st, ok)
	}

	paused := m
	paused.Paused = true
	sched.Upsert(paused)
	if err := sched.Push("tok123", model.StatusUp, "", 1); !errors.Is(err, ErrPaused) {
		t.Fatalf("Push(paused) err = %v, want ErrPaused", err)
	}
}

func TestSchedulerPushMissedProducesDownHeartbeat(t *testing.T) {
	fs := newFakeStore()
	fc := &fakeChecker{}
	sched := New(fs, fc, Options{ProbeID: 3, MaxConcurrent: 4, BufferSize: 100, FlushInterval: time.Hour})
	sched.unit = time.Millisecond

	m := model.Monitor{ID: 11, Type: model.TypePush, PushToken: "abc", IntervalS: 30, RetryIntervalS: 20, Retries: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx, []model.Monitor{m}, nil)
	defer func() { _ = sched.Shutdown(context.Background()) }()

	eventually(t, 500*time.Millisecond, func() bool {
		st, ok := sched.State(m.ID)
		return ok && st.Status == model.StatusDown
	})

	if err := sched.flushOnce(context.Background()); err != nil {
		t.Fatalf("flushOnce err = %v", err)
	}
	found := false
	for _, hb := range fs.heartbeatsSnapshot() {
		if hb.MonitorID == m.ID && hb.Status == model.StatusDown {
			found = true
			if hb.Message == "" {
				t.Fatalf("missed-push heartbeat has no message")
			}
		}
	}
	if !found {
		t.Fatalf("no down heartbeat recorded for missed push")
	}
}

func TestSchedulerFlusherRequeuesOnFailureAndRecovers(t *testing.T) {
	fs := newFakeStore()
	fc := &fakeChecker{}
	sched := New(fs, fc, Options{ProbeID: 1, MaxConcurrent: 1, BufferSize: 10, FlushInterval: time.Hour})

	if !sched.Health().DBReachable {
		t.Fatalf("expected DBReachable=true initially")
	}

	cacheMonitor(sched, model.Monitor{ID: 1, Type: model.TypeHTTP, IntervalS: 20, RetryIntervalS: 20, Retries: 1})
	fs.setFail(true, false, false)
	sched.recordHeartbeat(1, time.Now(), model.StatusDown, 5, "boom", nil)

	ctx := context.Background()
	if err := sched.flushOnce(ctx); err == nil {
		t.Fatalf("expected flushOnce to fail while the store is down")
	}
	if sched.Health().DBReachable {
		t.Fatalf("Health.DBReachable should be false after a failed flush")
	}
	if got := sched.Health().BufferedHeartbeats; got != 1 {
		t.Fatalf("buffered heartbeats after failed flush = %d, want 1 (not lost)", got)
	}

	fs.setFail(false, false, false)
	if err := sched.flushOnce(ctx); err != nil {
		t.Fatalf("flushOnce after recovery err = %v", err)
	}
	h := sched.Health()
	if !h.DBReachable {
		t.Fatalf("Health.DBReachable should be true after recovery")
	}
	if h.BufferedHeartbeats != 0 {
		t.Fatalf("buffered heartbeats after recovery = %d, want 0", h.BufferedHeartbeats)
	}
	if h.LastFlushAt.IsZero() {
		t.Fatalf("LastFlushAt should be set after a successful flush")
	}
	if fs.heartbeatCount() != 1 {
		t.Fatalf("store heartbeat count = %d, want 1", fs.heartbeatCount())
	}
}

func TestSchedulerStartSeedsStateFromPersisted(t *testing.T) {
	fs := newFakeStore()
	fc := &fakeChecker{}
	sched := New(fs, fc, Options{ProbeID: 1, MaxConcurrent: 4, BufferSize: 10, FlushInterval: time.Hour})

	persisted := model.MonitorState{
		MonitorID:           42,
		Status:              model.StatusDown,
		Since:               time.Now().Add(-time.Hour),
		LastCheckAt:         time.Now().Add(-time.Minute),
		ConsecutiveFailures: 3,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx, nil, []model.MonitorState{persisted})
	defer func() { _ = sched.Shutdown(context.Background()) }()

	st, ok := sched.State(42)
	if !ok {
		t.Fatalf("expected seeded state for monitor 42")
	}
	if st.ConsecutiveFailures != 3 || st.Status != model.StatusDown {
		t.Fatalf("seeded state = %+v, want failures=3 status=down", st)
	}
}

func TestSchedulerShutdownFlushesRemainingAndStopsWork(t *testing.T) {
	fs := newFakeStore()
	fc := &fakeChecker{result: checker.Result{Status: model.StatusUp}}
	sched := New(fs, fc, Options{ProbeID: 5, MaxConcurrent: 4, BufferSize: 100, FlushInterval: time.Hour})
	sched.unit = time.Millisecond
	sched.jitter = noJitter

	m := model.Monitor{ID: 21, Type: model.TypeHTTP, IntervalS: 20, RetryIntervalS: 20, Retries: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx, []model.Monitor{m}, nil)

	eventually(t, 300*time.Millisecond, func() bool { return fc.callCount() >= 2 })

	err1 := sched.Shutdown(context.Background())
	if err1 != nil {
		t.Fatalf("Shutdown err = %v", err1)
	}
	if fs.heartbeatCount() == 0 {
		t.Fatalf("Shutdown did not flush buffered heartbeats")
	}

	before := fc.callCount()
	time.Sleep(80 * time.Millisecond)
	if got := fc.callCount(); got != before {
		t.Fatalf("checks continued after Shutdown: before=%d after=%d", before, got)
	}

	if err2 := sched.Shutdown(context.Background()); err2 != err1 {
		t.Fatalf("second Shutdown call returned a different result: %v vs %v", err2, err1)
	}

	sched.Upsert(model.Monitor{ID: 99, Type: model.TypeHTTP, IntervalS: 20, RetryIntervalS: 20, Retries: 1})
	if _, ok := sched.State(99); ok {
		t.Fatalf("Upsert after Shutdown should be a no-op")
	}

	sched.Remove(m.ID)
	if _, ok := sched.State(m.ID); !ok {
		t.Fatalf("Remove after Shutdown should be a no-op, state should remain")
	}
}

// A check cut short by an edit (Upsert) or Shutdown reports whatever the
// cancelled context produced; that must not be recorded as a heartbeat.
func TestSchedulerDiscardsChecksAbortedByUpsertOrShutdown(t *testing.T) {
	fs := newFakeStore()
	// Every check takes 100ms; a cancelled one returns early with Down,
	// like a real HTTP check failing with "context canceled".
	fc := &fakeChecker{delay: 100 * time.Millisecond, result: checker.Result{Status: model.StatusUp}}
	abortAware := &abortingChecker{inner: fc}
	sched := New(fs, abortAware, Options{ProbeID: 1, MaxConcurrent: 4, BufferSize: 100, FlushInterval: time.Hour})
	sched.unit = time.Millisecond
	sched.jitter = noJitter

	m := model.Monitor{ID: 1, Type: model.TypeHTTP, IntervalS: 20, RetryIntervalS: 20, Retries: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx, nil, nil)
	sched.Upsert(m)
	eventually(t, time.Second, func() bool { return fc.callCount() >= 1 })
	sched.Upsert(m) // aborts the in-flight first check
	eventually(t, time.Second, func() bool { return fc.callCount() >= 3 })
	if err := sched.Shutdown(context.Background()); err != nil { // aborts another in-flight check
		t.Fatal(err)
	}
	hbs := fs.heartbeatsSnapshot()
	if len(hbs) == 0 {
		t.Fatal("completed checks were not recorded")
	}
	for _, h := range hbs {
		if h.Status != model.StatusUp {
			t.Fatalf("aborted check recorded as %v heartbeat: %+v", h.Status, h)
		}
	}
}

// abortingChecker returns Down when its context was cancelled mid-check.
type abortingChecker struct{ inner *fakeChecker }

func (a *abortingChecker) Check(ctx context.Context, m model.Monitor) checker.Result {
	res := a.inner.Check(ctx, m)
	if ctx.Err() != nil {
		return checker.Result{Status: model.StatusDown, Message: "context canceled"}
	}
	return res
}

// TestSchedulerRemoveRacingUpsertDoesNotLeakLoop races Remove(id) against
// Upsert(id) for the same monitor id many times, matching how the API
// calls the scheduler in practice (Upsert/Remove after every monitor
// write, with no serialization between them per id). Before the fix,
// whichever call lost the internal race for the monitor's slot mutex
// could leave a check loop running that the scheduler no longer tracked
// in slots/monitors: it kept calling Check and recordHeartbeat forever,
// resurrecting s.states[id] (and s.dirty[id]) right after Remove had
// deleted them, and kept driving the checker's call count up forever.
// The bug reproduced within the first few iterations of this loop.
func TestSchedulerRemoveRacingUpsertDoesNotLeakLoop(t *testing.T) {
	fs := newFakeStore()
	fc := &fakeChecker{result: checker.Result{Status: model.StatusUp}}
	sched := New(fs, fc, Options{ProbeID: 1, MaxConcurrent: 32, BufferSize: 4000, FlushInterval: time.Hour})
	sched.unit = time.Millisecond
	sched.jitter = noJitter

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx, nil, nil)
	defer func() { _ = sched.Shutdown(context.Background()) }()

	const id = int64(42)
	m := model.Monitor{ID: id, Type: model.TypeHTTP, IntervalS: 5, RetryIntervalS: 5, Retries: 1}

	for i := range 500 {
		sched.Upsert(m) // seed: the monitor is running before the race starts

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); sched.Upsert(m) }()
		go func() { defer wg.Done(); sched.Remove(id) }()
		wg.Wait()

		sched.Remove(id) // deterministic: this call always has the final word

		if _, ok := sched.State(id); ok {
			t.Fatalf("iteration %d: State present right after the final Remove", i)
		}
	}

	// Give any loop leaked by the race above room to run: it would keep
	// calling Check() and resurrecting s.states[id].
	time.Sleep(100 * time.Millisecond)
	if _, ok := sched.State(id); ok {
		t.Fatal("state was resurrected after the stress loop by a leaked check loop")
	}
	countAfterLoop := fc.callCount()
	time.Sleep(100 * time.Millisecond)
	if got := fc.callCount(); got != countAfterLoop {
		t.Fatalf("checker call count kept increasing after the stress loop (leaked loop): before=%d after=%d", countAfterLoop, got)
	}
}

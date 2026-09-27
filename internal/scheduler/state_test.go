package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/checker"
	"github.com/davidsugianto/upsera/internal/model"
)

func TestNextState(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(i int) time.Time { return t0.Add(time.Duration(i) * time.Minute) }

	type step struct {
		obs          model.Status
		wantStatus   model.Status
		wantFailures int
		wantSince    int // index into at()
	}
	cases := []struct {
		name    string
		retries int
		start   *model.MonitorState
		steps   []step
	}{
		{"retries=1: up, pending, down since first failure", 1, nil, []step{
			{model.StatusUp, model.StatusUp, 0, 0},
			{model.StatusDown, model.StatusPending, 1, 1},
			{model.StatusDown, model.StatusDown, 2, 1},
			{model.StatusDown, model.StatusDown, 3, 1},
		}},
		{"retries=0: straight to down", 0, nil, []step{
			{model.StatusUp, model.StatusUp, 0, 0},
			{model.StatusDown, model.StatusDown, 1, 1},
		}},
		{"down to up resets failures", 0, nil, []step{
			{model.StatusDown, model.StatusDown, 1, 0},
			{model.StatusDown, model.StatusDown, 2, 0},
			{model.StatusUp, model.StatusUp, 0, 2},
			{model.StatusUp, model.StatusUp, 0, 2},
		}},
		{"pending recovers to up", 2, nil, []step{
			{model.StatusDown, model.StatusPending, 1, 0},
			{model.StatusUp, model.StatusUp, 0, 1},
		}},
		{"maintenance to down starts at failures 1", 1,
			&model.MonitorState{Status: model.StatusMaintenance, Since: t0.Add(-time.Hour), ConsecutiveFailures: 5},
			[]step{
				{model.StatusMaintenance, model.StatusMaintenance, 0, -60},
				{model.StatusDown, model.StatusPending, 1, 1},
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var prev model.MonitorState
			have := false
			if tc.start != nil {
				prev, have = *tc.start, true
			}
			for i, s := range tc.steps {
				ns := nextState(prev, have, s.obs, at(i), tc.retries)
				if ns.Status != s.wantStatus || ns.ConsecutiveFailures != s.wantFailures || !ns.Since.Equal(at(s.wantSince)) {
					t.Fatalf("step %d (%v): got %v failures=%d since=%v, want %v failures=%d since=%v",
						i, s.obs, ns.Status, ns.ConsecutiveFailures, ns.Since, s.wantStatus, s.wantFailures, at(s.wantSince))
				}
				if !ns.LastCheckAt.Equal(at(i)) {
					t.Fatalf("step %d: LastCheckAt = %v", i, ns.LastCheckAt)
				}
				prev, have = ns, true
			}
		})
	}
}

type transitionLog struct {
	mu sync.Mutex
	ts []model.Transition
}

func (l *transitionLog) add(tr model.Transition) {
	l.mu.Lock()
	l.ts = append(l.ts, tr)
	l.mu.Unlock()
}

func (l *transitionLog) all() []model.Transition {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]model.Transition(nil), l.ts...)
}

func TestSchedulerEmitsTransitionsWithRetries(t *testing.T) {
	fs := newFakeStore()
	var mu sync.Mutex
	up := false
	fc := &fakeChecker{fn: func(model.Monitor, int) checker.Result {
		mu.Lock()
		defer mu.Unlock()
		if up {
			return checker.Result{Status: model.StatusUp}
		}
		return checker.Result{Status: model.StatusDown, Message: "refused"}
	}}
	var log transitionLog
	sched := New(fs, fc, Options{ProbeID: 1, MaxConcurrent: 4, BufferSize: 100, FlushInterval: time.Hour, OnTransition: log.add})
	sched.unit = time.Millisecond
	sched.jitter = noJitter

	m := model.Monitor{ID: 1, Type: model.TypeHTTP, IntervalS: 20, RetryIntervalS: 20, Retries: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx, []model.Monitor{m}, nil)
	defer func() { _ = sched.Shutdown(context.Background()) }()

	eventually(t, time.Second, func() bool { return len(log.all()) >= 2 })
	mu.Lock()
	up = true
	mu.Unlock()
	eventually(t, time.Second, func() bool { return len(log.all()) >= 3 })

	ts := log.all()
	want := [][2]model.Status{
		{model.StatusUp, model.StatusPending},
		{model.StatusPending, model.StatusDown},
		{model.StatusDown, model.StatusUp},
	}
	for i, w := range want {
		if ts[i].From != w[0] || ts[i].To != w[1] {
			t.Fatalf("transition %d = %v→%v, want %v→%v", i, ts[i].From, ts[i].To, w[0], w[1])
		}
	}
	firstFailure := ts[0].At
	if !ts[1].Since.Equal(firstFailure) {
		t.Fatalf("DOWN Since = %v, want first failure %v", ts[1].Since, firstFailure)
	}
	if !ts[2].PrevSince.Equal(firstFailure) {
		t.Fatalf("recovery PrevSince = %v, want first failure %v", ts[2].PrevSince, firstFailure)
	}
	if ts[1].Message != "refused" {
		t.Fatalf("DOWN message = %q", ts[1].Message)
	}
}

func TestSchedulerPauseEmitsMaintenanceWithoutHeartbeat(t *testing.T) {
	fs := newFakeStore()
	fc := &fakeChecker{result: checker.Result{Status: model.StatusUp}}
	var log transitionLog
	sched := New(fs, fc, Options{ProbeID: 1, MaxConcurrent: 4, BufferSize: 100, FlushInterval: time.Hour, OnTransition: log.add})
	sched.unit = time.Millisecond
	sched.jitter = noJitter

	m := model.Monitor{ID: 1, Type: model.TypeHTTP, IntervalS: 1000, RetryIntervalS: 20, Retries: 0}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx, nil, []model.MonitorState{{MonitorID: 1, Status: model.StatusDown, Since: time.Now(), LastCheckAt: time.Now()}})
	defer func() { _ = sched.Shutdown(context.Background()) }()

	m.Paused = true
	sched.Upsert(m)
	ts := log.all()
	if len(ts) != 1 || ts[0].From != model.StatusDown || ts[0].To != model.StatusMaintenance {
		t.Fatalf("transitions = %+v, want one DOWN→MAINTENANCE", ts)
	}
	if st, _ := sched.State(1); st.Status != model.StatusMaintenance {
		t.Fatalf("state = %v", st.Status)
	}
	if n := sched.Health().BufferedHeartbeats; n != 0 {
		t.Fatalf("pausing buffered %d heartbeats, want 0", n)
	}
	sched.Upsert(m) // still paused: no second transition
	if n := len(log.all()); n != 1 {
		t.Fatalf("re-pausing emitted again: %d transitions", n)
	}
}

func TestSchedulerIgnoresObservationsOfPausedMonitor(t *testing.T) {
	fs := newFakeStore()
	var log transitionLog
	sched := New(fs, &fakeChecker{}, Options{ProbeID: 1, MaxConcurrent: 1, BufferSize: 10, FlushInterval: time.Hour, OnTransition: log.add})
	sched.unit = time.Millisecond
	sched.Start(context.Background(), nil, nil)
	defer func() { _ = sched.Shutdown(context.Background()) }()
	m := model.Monitor{ID: 1, Type: model.TypePush, PushToken: "tok", IntervalS: 1000, RetryIntervalS: 20, Retries: 0, Paused: true}
	sched.Upsert(m) // → MAINTENANCE

	// A Push that passed its paused check just before the pause landed.
	sched.recordHeartbeat(1, time.Now(), model.StatusDown, 1, "late push", nil)
	if st, _ := sched.State(1); st.Status != model.StatusMaintenance {
		t.Fatalf("paused monitor state = %v, want maintenance", st.Status)
	}
	for _, tr := range log.all() {
		if tr.To == model.StatusDown {
			t.Fatalf("paused monitor emitted %+v", tr)
		}
	}
	if n := sched.Health().BufferedHeartbeats; n != 0 {
		t.Fatalf("buffered %d heartbeats for a paused monitor", n)
	}
}

type fakeMaintenance struct{ ids map[int64]bool }

func (f fakeMaintenance) Active(id int64, _ time.Time) (string, bool) {
	if f.ids[id] {
		return "db upgrade", true
	}
	return "", false
}

func TestSchedulerMaintenanceWindowSkipsChecks(t *testing.T) {
	fs := newFakeStore()
	fc := &fakeChecker{result: checker.Result{Status: model.StatusDown}}
	var log transitionLog
	sched := New(fs, fc, Options{ProbeID: 1, MaxConcurrent: 4, BufferSize: 100, FlushInterval: time.Hour,
		OnTransition: log.add, Maintenance: fakeMaintenance{ids: map[int64]bool{1: true}}})
	sched.unit = time.Millisecond
	sched.jitter = noJitter

	m := model.Monitor{ID: 1, Type: model.TypeHTTP, IntervalS: 20, RetryIntervalS: 20, Retries: 0}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx, []model.Monitor{m}, nil)

	eventually(t, time.Second, func() bool { return sched.Health().BufferedHeartbeats >= 3 })
	_ = sched.Shutdown(context.Background())
	if n := fc.callCount(); n != 0 {
		t.Fatalf("checker called %d times during maintenance", n)
	}
	for _, hb := range fs.heartbeatsSnapshot() {
		if hb.Status != model.StatusMaintenance || hb.Message != "maintenance: db upgrade" {
			t.Fatalf("heartbeat = %+v, want MAINTENANCE", hb)
		}
	}
	ts := log.all()
	if len(ts) != 1 || ts[0].To != model.StatusMaintenance {
		t.Fatalf("transitions = %+v, want one →MAINTENANCE", ts)
	}
}

func TestSchedulerFlapDamping(t *testing.T) {
	fs := newFakeStore()
	fc := &fakeChecker{}
	var log transitionLog
	sched := New(fs, fc, Options{ProbeID: 1, MaxConcurrent: 1, BufferSize: 100, FlushInterval: time.Hour, OnTransition: log.add})
	cacheMonitor(sched, model.Monitor{ID: 1, Type: model.TypeHTTP, IntervalS: 20, RetryIntervalS: 20, Retries: 0})
	t0 := time.Now()
	sched.recordHeartbeat(1, t0, model.StatusUp, 1, "", nil)

	obs := []model.Status{model.StatusDown, model.StatusUp, model.StatusDown, model.StatusUp, model.StatusDown,
		model.StatusUp, model.StatusDown}
	for i, o := range obs {
		sched.recordHeartbeat(1, t0.Add(time.Duration(i+1)*time.Minute), o, 1, "", nil)
	}
	started := 0
	for _, tr := range log.all() {
		if tr.FlapChanged && tr.Flapping {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("flap start transitions = %d, want exactly 1 (%+v)", started, log.all())
	}
	if st, _ := sched.State(1); st.FlapCount < flapThreshold {
		t.Fatalf("FlapCount = %d", st.FlapCount)
	}
	// An hour of stability ends flapping.
	sched.recordHeartbeat(1, t0.Add(2*time.Hour), model.StatusDown, 1, "", nil)
	last := log.all()[len(log.all())-1]
	if !last.FlapChanged || last.Flapping {
		t.Fatalf("last transition = %+v, want flapping ended", last)
	}
}

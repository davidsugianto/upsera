package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/checker"
	"github.com/davidsugianto/upsera/internal/model"
)

// fakeBaselines is a scheduler.Baselines double.
type fakeBaselines struct {
	mu    sync.Mutex
	m     map[model.BaselineKey]model.LatencyBaseline
	err   error
	calls int
	since []time.Time
}

func (f *fakeBaselines) LatencyBaselines(_ context.Context, since time.Time) (map[model.BaselineKey]model.LatencyBaseline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.since = append(f.since, since)
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[model.BaselineKey]model.LatencyBaseline, len(f.m))
	for k, v := range f.m {
		out[k] = v
	}
	return out, nil
}

func (f *fakeBaselines) set(m map[model.BaselineKey]model.LatencyBaseline, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m, f.err = m, err
}

func (f *fakeBaselines) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestP95(t *testing.T) {
	cases := []struct {
		in   []int32
		want int32
	}{
		{[]int32{50}, 50},
		{[]int32{10, 20}, 20},           // 0.95 between 10 and 20 → 19.5, rounded
		{[]int32{50, 50, 600}, 545},     // like percentile_cont: 50 + 0.9*550
		{[]int32{600, 50, 50, 50}, 517}, // order does not matter
	}
	for _, c := range cases {
		if got := p95(c.in); got != c.want {
			t.Errorf("p95(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestDegradedVerdict(t *testing.T) {
	enough := model.LatencyBaseline{P95Ms: 50, Samples: 100}
	cases := []struct {
		name     string
		prev     bool
		base     model.LatencyBaseline
		haveBase bool
		window   []int32
		want     bool
	}{
		{"no baseline", false, model.LatencyBaseline{}, false, []int32{900, 900, 900}, false},
		{"no baseline, was degraded", true, model.LatencyBaseline{}, false, []int32{900, 900, 900}, false},
		{"baseline of 99 samples", false, model.LatencyBaseline{P95Ms: 50, Samples: 99}, true, []int32{900, 900, 900}, false},
		{"window of 2 samples", false, enough, true, []int32{900, 900}, false},
		{"window of 2 samples keeps degraded", true, enough, true, []int32{60, 60}, true},
		{"3x but under the floor", false, model.LatencyBaseline{P95Ms: 2, Samples: 500}, true, []int32{90, 90, 90}, false},
		{"just at 3x is not over", false, enough, true, []int32{150, 150, 150}, false},
		{"over 3x and the floor", false, enough, true, []int32{151, 151, 151}, true},
		{"slow target over both", false, enough, true, []int32{600, 600, 600}, true},
		{"between 2x and 3x stays degraded", true, enough, true, []int32{120, 120, 120}, true},
		{"between 2x and 3x does not enter", false, enough, true, []int32{120, 120, 120}, false},
		{"at 2x leaves", true, enough, true, []int32{100, 100, 100}, false},
		{"below 2x leaves", true, enough, true, []int32{40, 40, 40}, false},
		{"small baseline, over floor", false, model.LatencyBaseline{P95Ms: 2, Samples: 500}, true, []int32{150, 150, 150}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := degradedVerdict(c.prev, c.base, c.haveBase, c.window)
			if got != c.want {
				t.Fatalf("degradedVerdict = %v, want %v", got, c.want)
			}
		})
	}
}

// latencyScheduler is a scheduler that is not started: tests drive
// recordHeartbeat directly. Baselines are loaded once, synchronously.
func latencyScheduler(t *testing.T, bl *fakeBaselines, log *transitionLog, m model.Monitor) *Scheduler {
	t.Helper()
	opts := Options{ProbeID: 1, MaxConcurrent: 1, BufferSize: 1000, FlushInterval: time.Hour}
	if bl != nil {
		opts.Baselines = bl
	}
	if log != nil {
		opts.OnTransition = log.add
	}
	s := New(newFakeStore(), &fakeChecker{}, opts)
	s.refreshBaselines(context.Background())
	cacheMonitor(s, m)
	return s
}

func localHTTP() model.Monitor {
	return model.Monitor{ID: 1, TeamID: 7, Name: "api", Type: model.TypeHTTP, IntervalS: 60, RetryIntervalS: 20, Retries: 1, TimeoutS: 10}
}

func baselineOf(monitorID, probeID int64, p95 int32, n int) map[model.BaselineKey]model.LatencyBaseline {
	return map[model.BaselineKey]model.LatencyBaseline{{MonitorID: monitorID, ProbeID: probeID}: {P95Ms: p95, Samples: n}}
}

func TestSingleRegionDegraded(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	up, down := model.StatusUp, model.StatusDown
	type obs struct {
		status  model.Status
		latency int32
	}
	slow, fast := obs{up, 600}, obs{up, 40}
	cases := []struct {
		name     string
		mon      func() model.Monitor
		baseline map[model.BaselineKey]model.LatencyBaseline // nil = Baselines disabled
		obs      []obs
		want     []model.Status
		window   time.Duration // 0 = 3 minutes: the last three checks
	}{
		{"baselines disabled", localHTTP, nil,
			[]obs{slow, slow, slow, slow}, []model.Status{up, up, up, up}, 0},
		{"no baseline for the monitor", localHTTP, baselineOf(2, 1, 50, 500),
			[]obs{slow, slow, slow, slow}, []model.Status{up, up, up, up}, 0},
		{"baseline from 99 samples", localHTTP, baselineOf(1, 1, 50, 99),
			[]obs{slow, slow, slow, slow}, []model.Status{up, up, up, up}, 0},
		{"needs three samples", localHTTP, baselineOf(1, 1, 50, 100),
			[]obs{slow, slow, slow}, []model.Status{up, up, model.StatusDegraded}, 0},
		{"under the floor stays up", localHTTP, baselineOf(1, 1, 2, 1000),
			[]obs{{up, 7}, {up, 7}, {up, 7}, {up, 90}}, []model.Status{up, up, up, up}, 0},
		{"hysteresis", localHTTP, baselineOf(1, 1, 50, 100),
			[]obs{slow, slow, slow, {up, 120}, {up, 120}, {up, 120}, fast, fast, fast},
			[]model.Status{up, up, model.StatusDegraded, model.StatusDegraded, model.StatusDegraded,
				model.StatusDegraded, model.StatusDegraded, model.StatusDegraded, up}, 0},
		{"failure from degraded starts pending", localHTTP, baselineOf(1, 1, 50, 100),
			[]obs{slow, slow, slow, {down, 0}, {down, 0}},
			[]model.Status{up, up, model.StatusDegraded, model.StatusPending, down}, 0},
		{"down to degraded", localHTTP, baselineOf(1, 1, 50, 100),
			[]obs{slow, slow, {down, 0}, {down, 0}, slow},
			[]model.Status{up, up, model.StatusPending, down, model.StatusDegraded}, 15 * time.Minute},
		{"push never degraded", func() model.Monitor {
			m := localHTTP()
			m.Type = model.TypePush
			return m
		}, baselineOf(1, 1, 50, 100), []obs{slow, slow, slow, slow}, []model.Status{up, up, up, up}, 0},
		{"docker never degraded", func() model.Monitor {
			m := localHTTP()
			m.Type = model.TypeDocker
			return m
		}, baselineOf(1, 1, 50, 100), []obs{slow, slow, slow, slow}, []model.Status{up, up, up, up}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var bl *fakeBaselines
			if c.baseline != nil {
				bl = &fakeBaselines{m: c.baseline}
			}
			s := latencyScheduler(t, bl, nil, c.mon())
			s.latWindow = 3 * time.Minute
			if c.window > 0 {
				s.latWindow = c.window
			}
			for i, o := range c.obs {
				ns := s.recordHeartbeat(1, t0.Add(time.Duration(i)*time.Minute), o.status, o.latency, "", nil)
				if ns.Status != c.want[i] {
					t.Fatalf("step %d (%v %d ms): status %v, want %v", i, o.status, o.latency, ns.Status, c.want[i])
				}
				if ns.Status == model.StatusPending && i > 0 && c.want[i-1] == model.StatusDegraded && ns.ConsecutiveFailures != 1 {
					t.Fatalf("step %d: failures = %d, want 1", i, ns.ConsecutiveFailures)
				}
			}
		})
	}
}

func TestDegradedTransitionsAndFlaps(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	log := &transitionLog{}
	s := latencyScheduler(t, &fakeBaselines{m: baselineOf(1, 1, 50, 200)}, log, localHTTP())
	at := func(i int) time.Time { return t0.Add(time.Duration(i) * time.Minute) }

	// Five UP <-> DEGRADED rounds would be flapping if they counted.
	i := 0
	rec := func(st model.Status, ms int32) model.MonitorState {
		ns := s.recordHeartbeat(1, at(i), st, ms, "", nil)
		i++
		return ns
	}
	s.latWindow = 3 * time.Minute // three samples per window
	for range 6 {
		rec(model.StatusUp, 600)
		rec(model.StatusUp, 600)
		ns := rec(model.StatusUp, 600)
		if ns.Status != model.StatusDegraded {
			t.Fatalf("not degraded after slow window: %v", ns.Status)
		}
		rec(model.StatusUp, 10)
		rec(model.StatusUp, 10)
		ns = rec(model.StatusUp, 10)
		if ns.Status != model.StatusUp {
			t.Fatalf("not up after fast window: %v", ns.Status)
		}
		if ns.FlapCount != 0 {
			t.Fatalf("FlapCount = %d after UP <-> DEGRADED", ns.FlapCount)
		}
	}
	var deg, rcv int
	for _, tr := range log.all() {
		if tr.FlapChanged || tr.Flapping {
			t.Fatalf("flap state changed: %+v", tr)
		}
		switch {
		case tr.From == model.StatusUp && tr.To == model.StatusDegraded:
			deg++
			if tr.Region != model.LocalRegion || tr.BaselineMs != 50 || tr.LatencyP95Ms <= 150 || tr.TeamID != 7 {
				t.Fatalf("degraded transition = %+v", tr)
			}
		case tr.From == model.StatusDegraded && tr.To == model.StatusUp:
			rcv++
			if tr.Region != model.LocalRegion || tr.BaselineMs != 50 || tr.LatencyP95Ms > 100 {
				t.Fatalf("latency recovered transition = %+v", tr)
			}
		}
	}
	if deg != 6 || rcv != 6 {
		t.Fatalf("transitions: %d degraded, %d recovered; want 6 each", deg, rcv)
	}

	// DOWN -> DEGRADED is a recovery flap, like DOWN -> UP.
	s.mu.Lock()
	delete(s.flaps, 1)
	s.mu.Unlock()
	rec(model.StatusDown, 0)
	rec(model.StatusDown, 0)
	rec(model.StatusUp, 600)
	rec(model.StatusUp, 600)
	// The first check after DOWN finds too few samples: DOWN -> UP.
	ns := rec(model.StatusUp, 600)
	if ns.Status != model.StatusDegraded {
		t.Fatalf("status = %v", ns.Status)
	}
	if ns.FlapCount != 2 {
		t.Fatalf("FlapCount = %d, want 2 (DOWN, then recovery)", ns.FlapCount)
	}
}

func TestDegradedSinceSemantics(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	s := latencyScheduler(t, &fakeBaselines{m: baselineOf(1, 1, 50, 200)}, nil, localHTTP())
	var ns model.MonitorState
	for i := range 5 {
		ns = s.recordHeartbeat(1, t0.Add(time.Duration(i)*time.Minute), model.StatusUp, 600, "", nil)
	}
	if ns.Status != model.StatusDegraded || !ns.Since.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("state = %v since %v, want degraded since the third check", ns.Status, ns.Since)
	}
}

func TestLatencyWindowExpires(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	s := latencyScheduler(t, &fakeBaselines{m: baselineOf(1, 1, 50, 200)}, nil, localHTTP())
	// Three slow samples spread over more than the window never count together.
	for i := range 3 {
		ns := s.recordHeartbeat(1, t0.Add(time.Duration(i)*10*time.Minute), model.StatusUp, 600, "", nil)
		if ns.Status != model.StatusUp {
			t.Fatalf("sample %d: %v, want up (window holds < 3 samples)", i, ns.Status)
		}
	}
	// The window stays bounded however many samples arrive.
	for i := range 5 * maxWindowSamples {
		s.recordHeartbeat(1, t0.Add(time.Hour+time.Duration(i)*time.Millisecond), model.StatusUp, 10, "", nil)
	}
	s.mu.Lock()
	n := len(s.lat[1][model.LocalRegion].samples)
	s.mu.Unlock()
	if n > maxWindowSamples {
		t.Fatalf("window holds %d samples, cap %d", n, maxWindowSamples)
	}
}

func TestBaselineRefresh(t *testing.T) {
	bl := &fakeBaselines{m: baselineOf(1, 1, 50, 200)}
	s := New(newFakeStore(), &fakeChecker{result: checker.Result{Status: model.StatusUp}}, Options{
		ProbeID: 1, BufferSize: 10, FlushInterval: time.Hour, Baselines: bl, BaselineRefresh: 10 * time.Millisecond,
	})
	s.Start(context.Background(), nil, nil)
	deadline := time.Now().Add(5 * time.Second)
	for bl.callCount() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("baselines loaded %d times, want >= 3", bl.callCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got, ok := s.baseline(1, 1); !ok || got.P95Ms != 50 {
		t.Fatalf("baseline = %+v, %v", got, ok)
	}
	bl.mu.Lock()
	since := bl.since[0]
	bl.mu.Unlock()
	if d := time.Since(since); d < baselineSpan-time.Minute || d > baselineSpan+time.Minute {
		t.Fatalf("since = %v ago, want about %v", d, baselineSpan)
	}

	// A failing load keeps the previous baseline.
	bl.set(nil, errFake)
	n := bl.callCount()
	for bl.callCount() < n+2 {
		time.Sleep(5 * time.Millisecond)
	}
	if got, ok := s.baseline(1, 1); !ok || got.P95Ms != 50 {
		t.Fatalf("after a failed load: baseline = %+v, %v", got, ok)
	}

	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	n = bl.callCount()
	time.Sleep(50 * time.Millisecond)
	if bl.callCount() != n {
		t.Fatal("baseline loop still running after Shutdown")
	}
}

func TestBaselinesNotLoadedKeepsDegraded(t *testing.T) {
	// After a restart the persisted DEGRADED state is kept until the first
	// baseline load, instead of flipping to UP (a false latency_recovered).
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	bl := &fakeBaselines{m: baselineOf(1, 1, 50, 200)}
	s := New(newFakeStore(), &fakeChecker{}, Options{ProbeID: 1, BufferSize: 100, FlushInterval: time.Hour, Baselines: bl})
	cacheMonitor(s, localHTTP())
	s.mu.Lock()
	s.states[1] = model.MonitorState{MonitorID: 1, Status: model.StatusDegraded, Since: t0}
	s.mu.Unlock()
	for i := range 4 {
		if ns := s.recordHeartbeat(1, t0.Add(time.Duration(i)*time.Minute), model.StatusUp, 600, "", nil); ns.Status != model.StatusDegraded {
			t.Fatalf("check %d before baselines loaded: %v", i, ns.Status)
		}
	}
	s.refreshBaselines(context.Background())
	if ns := s.recordHeartbeat(1, t0.Add(5*time.Minute), model.StatusUp, 600, "", nil); ns.Status != model.StatusDegraded {
		t.Fatalf("after load: %v", ns.Status)
	}
}

func TestMultiRegionDegraded(t *testing.T) {
	// Probe ids in the harness are 100 + the region's first byte.
	pid := func(region string) int64 { return int64(100 + int(region[0])) }
	baselines := func(regions ...string) map[model.BaselineKey]model.LatencyBaseline {
		m := map[model.BaselineKey]model.LatencyBaseline{}
		for _, r := range regions {
			m[model.BaselineKey{MonitorID: 1, ProbeID: pid(r)}] = model.LatencyBaseline{P95Ms: 50, Samples: 500}
		}
		return m
	}
	setup := func(t *testing.T) *harness {
		bl := &fakeBaselines{m: baselines("a", "b", "c")}
		h := newHarness(t, []model.Monitor{regionMonitor()}, nil, func(o *Options) { o.Baselines = bl })
		h.s.refreshBaselines(context.Background())
		return h
	}
	slow := func(h *harness, region string, n int) {
		for i := range n {
			h.reportMsg(region, model.StatusUp, time.Duration(n-i)*time.Second, 600, "ok")
		}
	}

	t.Run("one fresh region degraded, others up", func(t *testing.T) {
		h := setup(t)
		h.report("b", model.StatusUp, 0)
		h.report("c", model.StatusUp, 0)
		slow(h, "a", 3)
		if got := h.status(); got != model.StatusDegraded {
			t.Fatalf("status = %v, want degraded", got)
		}
		st, _ := h.s.State(1)
		if st.Regions["a"].Status != model.StatusDegraded || st.Regions["b"].Status != model.StatusUp {
			t.Fatalf("regions = %+v", st.Regions)
		}
		var found bool
		for _, tr := range h.log.all() {
			if tr.To == model.StatusDegraded {
				found = true
				if tr.Region != "a" || tr.LatencyP95Ms != 600 || tr.BaselineMs != 50 {
					t.Fatalf("transition = %+v", tr)
				}
			}
		}
		if !found {
			t.Fatal("no DEGRADED transition")
		}
		hbs := h.heartbeats()
		if last := hbs[len(hbs)-1]; last.Status != model.StatusDegraded {
			t.Fatalf("last heartbeat status = %v", last.Status)
		}
	})

	t.Run("quorum down wins over degraded", func(t *testing.T) {
		h := setup(t)
		slow(h, "a", 3)
		h.report("b", model.StatusDown, 0)
		h.report("c", model.StatusDown, 0)
		if got := h.status(); got != model.StatusDown {
			t.Fatalf("status = %v, want down", got)
		}
	})

	t.Run("pending wins over degraded", func(t *testing.T) {
		h := setup(t)
		m := regionMonitor()
		m.Retries = 2
		h.s.Upsert(m)
		slow(h, "a", 3)
		h.report("b", model.StatusDown, 0)
		h.report("c", model.StatusDown, 0)
		if got := h.status(); got != model.StatusPending {
			t.Fatalf("status = %v, want pending", got)
		}
	})

	t.Run("stale degraded region is ignored", func(t *testing.T) {
		h := setup(t)
		slow(h, "a", 3)
		h.report("b", model.StatusUp, 0)
		h.report("c", model.StatusUp, 0)
		if got := h.status(); got != model.StatusDegraded {
			t.Fatalf("status = %v, want degraded", got)
		}
		// a goes quiet; b and c keep reporting.
		h.clk.advance(3 * time.Minute)
		h.report("b", model.StatusUp, 0)
		h.report("c", model.StatusUp, 0)
		if got := h.status(); got != model.StatusUp {
			t.Fatalf("status = %v, want up (a is stale)", got)
		}
	})

	t.Run("degraded region recovers", func(t *testing.T) {
		h := setup(t)
		h.report("b", model.StatusUp, 0)
		h.report("c", model.StatusUp, 0)
		slow(h, "a", 3)
		h.s.latWindow = time.Second // forget the slow samples
		h.clk.advance(10 * time.Second)
		for i := range 3 {
			h.reportMsg("a", model.StatusUp, time.Duration(3-i)*time.Millisecond, 20, "ok")
		}
		if got := h.status(); got != model.StatusUp {
			t.Fatalf("status = %v, want up", got)
		}
		trs := h.log.all()
		last := trs[len(trs)-1]
		if last.From != model.StatusDegraded || last.To != model.StatusUp || last.Region != "a" || last.BaselineMs != 50 {
			t.Fatalf("last transition = %+v", last)
		}
	})
}

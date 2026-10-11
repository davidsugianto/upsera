package scheduler

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/checker"
	"github.com/davidsugianto/upsera/internal/model"
)

// fakeClock is a manually advanced clock injected as Scheduler.now.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var clockStart = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// harness drives a Scheduler deterministically: fake clock, no sweep timer
// (tests call sweep directly), remote regions only so no check loops run.
type harness struct {
	t     *testing.T
	s     *Scheduler
	clk   *fakeClock
	log   *transitionLog
	fs    *fakeStore
	flush int
}

type harnessOpt func(*Options)

func newHarness(t *testing.T, ms []model.Monitor, states []model.MonitorState, opts ...harnessOpt) *harness {
	t.Helper()
	h := &harness{t: t, clk: &fakeClock{t: clockStart}, log: &transitionLog{}, fs: newFakeStore()}
	o := Options{ProbeID: 1, MaxConcurrent: 2, BufferSize: 1000, FlushInterval: time.Hour, OnTransition: h.log.add}
	for _, f := range opts {
		f(&o)
	}
	// Local checks block until shutdown: harness tests drive results by hand.
	h.s = New(h.fs, &fakeChecker{result: checker.Result{Status: model.StatusUp}, delay: time.Hour}, o)
	h.s.now = h.clk.Now
	h.s.sweepInterval = time.Hour
	h.s.jitter = noJitter
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = h.s.Shutdown(context.Background())
	})
	h.s.Start(ctx, ms, states)
	return h
}

func regionMonitor() model.Monitor {
	return model.Monitor{
		ID: 1, TeamID: 7, Name: "api", Type: model.TypeHTTP, Config: []byte(`{"url":"https://x"}`),
		IntervalS: 60, RetryIntervalS: 20, Retries: 0, TimeoutS: 10,
		Regions: []string{"a", "b", "c"}, Quorum: 2,
	}
}

// report sends one result for monitor 1 from region, timestamped ago before
// the fake clock's now.
func (h *harness) report(region string, status model.Status, ago time.Duration) {
	h.t.Helper()
	h.reportMsg(region, status, ago, 5, status.String())
}

func (h *harness) reportMsg(region string, status model.Status, ago time.Duration, latency int32, msg string) {
	h.t.Helper()
	probeID := int64(100 + int(region[0]))
	h.s.Report(probeID, region, []model.RegionResult{{
		MonitorID: 1, Time: h.clk.Now().Add(-ago), Status: status, LatencyMs: latency, Message: msg,
	}})
}

func (h *harness) status() model.Status {
	h.t.Helper()
	st, ok := h.s.State(1)
	if !ok {
		h.t.Fatalf("no state for monitor 1")
	}
	return st.Status
}

func (h *harness) heartbeats() []model.Heartbeat {
	h.t.Helper()
	if err := h.s.flushOnce(context.Background()); err != nil {
		h.t.Fatalf("flush: %v", err)
	}
	return h.fs.heartbeatsSnapshot()
}

func (h *harness) transitions() [][2]model.Status {
	var out [][2]model.Status
	for _, tr := range h.log.all() {
		out = append(out, [2]model.Status{tr.From, tr.To})
	}
	return out
}

func (h *harness) mustNotHaveDownTransition() {
	h.t.Helper()
	for _, tr := range h.log.all() {
		if tr.To == model.StatusDown {
			h.t.Fatalf("unexpected DOWN transition: %+v", tr)
		}
	}
}

func (h *harness) allUp() {
	h.t.Helper()
	for _, r := range []string{"a", "b", "c"} {
		h.report(r, model.StatusUp, 0)
	}
}

const (
	up      = model.StatusUp
	down    = model.StatusDown
	pending = model.StatusPending
)

func TestRegionsQuorum(t *testing.T) {
	type step struct {
		advance time.Duration
		report  map[string]model.Status // region -> raw result at "now"
		sweep   bool
		want    model.Status
		wantMsg string // substring of the last transition message, "" = unchecked
	}
	staleAfter := 2*time.Minute + time.Second // > 2 * IntervalS(60s)
	cases := []struct {
		name    string
		retries int
		steps   []step
	}{
		{"one down of three is still UP", 0, []step{
			{report: map[string]model.Status{"a": up, "b": up, "c": up}, want: up},
			{report: map[string]model.Status{"a": down}, want: up},
		}},
		{"two down of three is DOWN", 0, []step{
			{report: map[string]model.Status{"a": up, "b": up, "c": up}, want: up},
			{report: map[string]model.Status{"a": down}, want: up},
			{report: map[string]model.Status{"b": down}, want: down, wantMsg: "down"},
		}},
		{"all three down is DOWN", 0, []step{
			{report: map[string]model.Status{"a": down, "b": down, "c": down}, want: down},
		}},
		{"one stale region is UP", 0, []step{
			{report: map[string]model.Status{"a": up, "b": up, "c": up}, want: up},
			{advance: staleAfter, report: map[string]model.Status{"b": up, "c": up}, sweep: true, want: up},
		}},
		{"two stale regions is PENDING probe silent, never DOWN", 0, []step{
			{report: map[string]model.Status{"a": up, "b": up, "c": up}, want: up},
			{advance: staleAfter, report: map[string]model.Status{"c": up}, sweep: true, want: pending,
				wantMsg: "probe silent: 1 of 3 regions reporting (a, b silent)"},
		}},
		{"sweep alone flips to PENDING without any new result", 0, []step{
			{report: map[string]model.Status{"a": up, "b": up, "c": up}, want: up},
			{advance: staleAfter, sweep: true, want: pending, wantMsg: "probe silent: 0 of 3 regions reporting (a, b, c silent)"},
		}},
		{"stale DOWN regions do not keep the monitor DOWN or re-DOWN it", 0, []step{
			{report: map[string]model.Status{"a": down, "b": down, "c": up}, want: down},
			{advance: staleAfter, report: map[string]model.Status{"c": up}, sweep: true, want: pending, wantMsg: "probe silent"},
			{advance: time.Minute, sweep: true, want: pending},
		}},
		{"silent then probes return recovers to UP", 0, []step{
			{report: map[string]model.Status{"a": up, "b": up, "c": up}, want: up},
			{advance: staleAfter, sweep: true, want: pending},
			{report: map[string]model.Status{"a": up, "b": up, "c": up}, want: up},
		}},
		{"retries are counted per region", 1, []step{
			{report: map[string]model.Status{"a": up, "b": up, "c": up}, want: up},
			{advance: time.Second, report: map[string]model.Status{"a": down}, want: up},      // a PENDING
			{advance: time.Second, report: map[string]model.Status{"b": down}, want: pending}, // a,b PENDING >= q
			{advance: time.Second, report: map[string]model.Status{"a": down}, want: pending}, // a DOWN, b PENDING
			{advance: time.Second, report: map[string]model.Status{"b": down}, want: down},    // a,b DOWN
		}},
		{"a region recovering resets its retries", 1, []step{
			{report: map[string]model.Status{"a": up, "b": up, "c": up}, want: up},
			{advance: time.Second, report: map[string]model.Status{"a": down}, want: up},
			{advance: time.Second, report: map[string]model.Status{"a": up}, want: up},
			{advance: time.Second, report: map[string]model.Status{"a": down}, want: up}, // PENDING again, not DOWN
			{advance: time.Second, report: map[string]model.Status{"b": down}, want: pending},
		}},
		{"recovery from DOWN", 0, []step{
			{report: map[string]model.Status{"a": down, "b": down, "c": up}, want: down},
			{advance: time.Second, report: map[string]model.Status{"a": up}, want: up},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := regionMonitor()
			m.Retries = tc.retries
			h := newHarness(t, []model.Monitor{m}, nil)
			for i, st := range tc.steps {
				h.clk.advance(st.advance)
				for _, r := range []string{"a", "b", "c"} { // deterministic order
					if s, ok := st.report[r]; ok {
						h.report(r, s, 0)
					}
				}
				if st.sweep {
					h.s.sweep()
				}
				if got := h.status(); got != st.want {
					t.Fatalf("step %d: status = %v, want %v (transitions %v)", i, got, st.want, h.transitions())
				}
				if st.wantMsg != "" {
					ts := h.log.all()
					if len(ts) == 0 || !strings.Contains(ts[len(ts)-1].Message, st.wantMsg) {
						t.Fatalf("step %d: last transition message = %+v, want to contain %q", i, ts, st.wantMsg)
					}
				}
			}
		})
	}
}

func TestRegionsSilentNeverOpensDown(t *testing.T) {
	h := newHarness(t, []model.Monitor{regionMonitor()}, nil)
	h.allUp()
	// Everything goes quiet for an hour; sweeps run all along.
	for range 60 {
		h.clk.advance(time.Minute)
		h.s.sweep()
	}
	h.mustNotHaveDownTransition()
	if got := h.status(); got != pending {
		t.Fatalf("status = %v, want PENDING", got)
	}
	want := [][2]model.Status{{up, pending}}
	if got := h.transitions(); len(got) != 1 || got[0] != want[0] {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
}

func TestRegionsStateFilled(t *testing.T) {
	h := newHarness(t, []model.Monitor{regionMonitor()}, nil)
	h.clk.advance(time.Second)
	h.reportMsg("a", down, 0, 42, "refused")
	h.reportMsg("b", up, 0, 7, "ok")
	st, _ := h.s.State(1)
	if len(st.Regions) != 2 {
		t.Fatalf("Regions = %+v, want a and b", st.Regions)
	}
	a := st.Regions["a"]
	if a.Status != down || a.ConsecutiveFailures != 1 || a.LatencyMs != 42 || a.Message != "refused" || !a.LastResultAt.Equal(h.clk.Now()) {
		t.Fatalf("region a = %+v", a)
	}
	if st.Regions["b"].Status != up {
		t.Fatalf("region b = %+v", st.Regions["b"])
	}
	// The map handed out is a snapshot: later reports don't mutate it.
	h.report("c", up, 0)
	if _, ok := st.Regions["c"]; ok {
		t.Fatal("State().Regions was mutated by a later Report")
	}
}

func TestRegionsHeartbeats(t *testing.T) {
	h := newHarness(t, []model.Monitor{regionMonitor()}, nil)
	h.clk.advance(time.Second)
	h.reportMsg("a", down, 0, 11, "boom")
	h.reportMsg("b", down, 0, 22, "bang")
	hbs := h.heartbeats()
	if len(hbs) != 2 {
		t.Fatalf("heartbeats = %+v", hbs)
	}
	if hbs[0].ProbeID != int64(100+'a') || hbs[0].LatencyMs != 11 || hbs[0].Message != "boom" || hbs[0].Status != up {
		t.Fatalf("hb[0] = %+v (status is the monitor's state after the result: still UP)", hbs[0])
	}
	if hbs[1].ProbeID != int64(100+'b') || hbs[1].LatencyMs != 22 || hbs[1].Message != "bang" || hbs[1].Status != down {
		t.Fatalf("hb[1] = %+v (status is the monitor's state: DOWN)", hbs[1])
	}
	if !hbs[1].Time.Equal(h.clk.Now()) || hbs[1].MonitorID != 1 {
		t.Fatalf("hb[1] = %+v", hbs[1])
	}
}

func TestRegionsReportIgnores(t *testing.T) {
	local := model.Monitor{ID: 2, Name: "l", Type: model.TypeHTTP, IntervalS: 60, RetryIntervalS: 20}
	paused := regionMonitor()
	paused.ID = 3
	paused.Paused = true
	push := model.Monitor{ID: 4, Name: "p", Type: model.TypePush, PushToken: "tok", IntervalS: 60, Regions: []string{"a", "b"}, Quorum: 1}
	h := newHarness(t, []model.Monitor{regionMonitor(), local, paused, push}, nil)

	send := func(region string, id int64) {
		h.s.Report(9, region, []model.RegionResult{{MonitorID: id, Time: h.clk.Now(), Status: down, Message: "x"}})
	}
	send("a", 999) // unknown monitor
	send("z", 1)   // monitor 1 is not in region z
	send("a", 2)   // local-only monitor
	send("a", 3)   // paused
	send("a", 4)   // push monitors are never remote
	send("local", 1)
	if n := h.s.Health().BufferedHeartbeats; n != 0 {
		t.Fatalf("ignored reports buffered %d heartbeats", n)
	}
	if _, ok := h.s.State(1); ok {
		t.Fatal("ignored report created state")
	}
	if _, ok := h.s.State(2); ok {
		t.Fatal("local-only monitor got state from a probe report")
	}
	if st, _ := h.s.State(3); st.Status != model.StatusMaintenance {
		t.Fatalf("paused monitor state = %v, want MAINTENANCE", st.Status)
	}
	// Invalid result statuses are dropped too.
	h.s.Report(9, "a", []model.RegionResult{{MonitorID: 1, Time: h.clk.Now(), Status: model.StatusPending}})
	if n := h.s.Health().BufferedHeartbeats; n != 0 {
		t.Fatalf("non up/down result buffered %d heartbeats", n)
	}
}

func TestRegionsOutOfOrder(t *testing.T) {
	h := newHarness(t, []model.Monitor{regionMonitor()}, nil)
	h.clk.advance(time.Minute)
	h.report("a", down, 0)
	h.report("b", down, 0)
	if h.status() != down {
		t.Fatalf("status = %v", h.status())
	}
	before, _ := h.s.State(1)

	// An older result from a: heartbeat only, no state change.
	h.reportMsg("a", up, 30*time.Second, 3, "old ok")
	after, _ := h.s.State(1)
	if after.Status != down || after.Regions["a"].Status != down || !after.Regions["a"].LastResultAt.Equal(before.Regions["a"].LastResultAt) {
		t.Fatalf("out-of-order result changed state: %+v", after)
	}
	hbs := h.heartbeats()
	last := hbs[len(hbs)-1]
	if last.Message != "old ok" || last.LatencyMs != 3 || last.Status != down || last.ProbeID != int64(100+'a') {
		t.Fatalf("out-of-order heartbeat = %+v, want history row with the monitor's current status", last)
	}
	if got := h.transitions(); len(got) != 1 {
		t.Fatalf("transitions = %v, want only UP→DOWN", got)
	}
}

func TestRegionsBatchIsProcessedInTimeOrder(t *testing.T) {
	h := newHarness(t, []model.Monitor{regionMonitor()}, nil)
	h.clk.advance(time.Minute)
	now := h.clk.Now()
	// Newest first: sorted by time, the final word is the DOWN at +0.
	h.s.Report(9, "a", []model.RegionResult{
		{MonitorID: 1, Time: now, Status: down, Message: "late"},
		{MonitorID: 1, Time: now.Add(-10 * time.Second), Status: up, Message: "early"},
	})
	st, _ := h.s.State(1)
	if st.Regions["a"].Status != down || st.Regions["a"].Message != "late" {
		t.Fatalf("region a = %+v, want the newest result to win", st.Regions["a"])
	}
}

func TestRegionsMaintenance(t *testing.T) {
	maint := fakeMaintenance{ids: map[int64]bool{1: true}}
	h := newHarness(t, []model.Monitor{regionMonitor()}, nil, func(o *Options) { o.Maintenance = maint })
	h.clk.advance(time.Second)
	h.report("a", down, 0)
	h.report("b", down, 0)
	if h.status() != model.StatusMaintenance {
		t.Fatalf("status = %v, want MAINTENANCE", h.status())
	}
	st, _ := h.s.State(1)
	if len(st.Regions) != 0 {
		t.Fatalf("maintenance results must be ignored, regions = %+v", st.Regions)
	}
	hbs := h.heartbeats()
	if len(hbs) != 2 {
		t.Fatalf("heartbeats = %+v", hbs)
	}
	for _, hb := range hbs {
		if hb.Status != model.StatusMaintenance || hb.Message != "maintenance: db upgrade" {
			t.Fatalf("heartbeat = %+v, want MAINTENANCE", hb)
		}
	}
	// A sweep keeps it in maintenance (and sets it even without a report).
	h.clk.advance(10 * time.Minute)
	h.s.sweep()
	if h.status() != model.StatusMaintenance {
		t.Fatalf("after sweep status = %v", h.status())
	}
	h.mustNotHaveDownTransition()
}

func TestRegionsSweepEntersMaintenanceWithoutReports(t *testing.T) {
	maint := &toggleMaintenance{}
	h := newHarness(t, []model.Monitor{regionMonitor()}, nil, func(o *Options) { o.Maintenance = maint })
	h.allUp()
	maint.set(true)
	h.s.sweep()
	if h.status() != model.StatusMaintenance {
		t.Fatalf("status = %v, want MAINTENANCE", h.status())
	}
	// Window ends: regions are stale from the maintenance gap, but the sweep
	// grants a grace period instead of flashing "probe silent".
	maint.set(false)
	h.clk.advance(30 * time.Minute)
	h.s.sweep()
	if got := h.status(); got != model.StatusMaintenance {
		t.Fatalf("right after the window status = %v, want to wait for results", got)
	}
	h.allUp()
	if h.status() != up {
		t.Fatalf("status = %v, want UP once results arrive", h.status())
	}
}

func TestRegionsMaintenanceEndThenSilentProbes(t *testing.T) {
	maint := &toggleMaintenance{}
	h := newHarness(t, []model.Monitor{regionMonitor()}, nil, func(o *Options) { o.Maintenance = maint })
	h.allUp()
	maint.set(true)
	h.s.sweep()
	maint.set(false)
	h.clk.advance(30 * time.Minute)
	h.s.sweep() // grace starts here
	h.clk.advance(time.Minute)
	h.s.sweep()
	if h.status() != model.StatusMaintenance {
		t.Fatalf("status = %v inside the post-maintenance grace", h.status())
	}
	h.clk.advance(2 * time.Minute)
	h.s.sweep()
	if h.status() != pending {
		t.Fatalf("status = %v, want PENDING (probes never came back)", h.status())
	}
	h.mustNotHaveDownTransition()
}

type toggleMaintenance struct {
	mu sync.Mutex
	on bool
}

func (m *toggleMaintenance) set(v bool) { m.mu.Lock(); m.on = v; m.mu.Unlock() }
func (m *toggleMaintenance) Active(int64, time.Time) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return "window", m.on
}

func TestRegionsPausedMonitorIgnoresResultsAndSweeps(t *testing.T) {
	m := regionMonitor()
	m.Paused = true
	h := newHarness(t, []model.Monitor{m}, nil)
	h.clk.advance(time.Second)
	h.report("a", down, 0)
	h.report("b", down, 0)
	h.clk.advance(time.Hour)
	h.s.sweep()
	if h.status() != model.StatusMaintenance {
		t.Fatalf("paused status = %v", h.status())
	}
	if n := h.s.Health().BufferedHeartbeats; n != 0 {
		t.Fatalf("paused monitor buffered %d heartbeats", n)
	}
	// Unpausing: the regions have no recent data, so nothing flips until the
	// grace period ends, then probe-silent PENDING (never DOWN).
	m.Paused = false
	h.s.Upsert(m)
	h.s.sweep()
	if h.status() != model.StatusMaintenance {
		t.Fatalf("status right after unpause = %v, want to wait", h.status())
	}
	h.clk.advance(2*time.Minute + time.Second)
	h.s.sweep()
	if h.status() != pending {
		t.Fatalf("status after grace = %v, want PENDING silent", h.status())
	}
	h.mustNotHaveDownTransition()
}

func TestRegionsStartupGrace(t *testing.T) {
	h := newHarness(t, []model.Monitor{regionMonitor()}, nil)
	// Within 2*IntervalS of scheduler start unknown regions are neither down
	// nor stale: no state at all yet.
	h.clk.advance(time.Minute)
	h.s.sweep()
	if _, ok := h.s.State(1); ok {
		t.Fatalf("state exists during startup grace with no results: %+v", h.transitions())
	}
	// One region reports: 1 fresh + 2 unknown can still reach quorum 2 → UP.
	h.report("a", up, 0)
	if h.status() != up {
		t.Fatalf("status = %v, want UP while the others are still unknown", h.status())
	}
	h.clk.advance(30 * time.Second) // t = 90s, still inside the 120s grace
	h.s.sweep()
	if h.status() != up {
		t.Fatalf("status = %v during grace", h.status())
	}
	// Grace over (start + 120s): b and c never reported → silent.
	h.clk.advance(31 * time.Second) // t = 121s; a's result is 61s old, still fresh
	h.s.sweep()
	if h.status() != pending {
		t.Fatalf("status = %v after grace, want PENDING", h.status())
	}
	ts := h.log.all()
	if msg := ts[len(ts)-1].Message; msg != "probe silent: 1 of 3 regions reporting (b, c silent)" {
		t.Fatalf("message = %q", msg)
	}
	h.mustNotHaveDownTransition()
}

func TestRegionsStartupGraceNoResultsAtAll(t *testing.T) {
	h := newHarness(t, []model.Monitor{regionMonitor()}, nil)
	h.clk.advance(2*time.Minute + time.Second)
	h.s.sweep()
	if h.status() != pending {
		t.Fatalf("status = %v, want PENDING after grace with nothing reporting", h.status())
	}
	ts := h.log.all()
	if msg := ts[len(ts)-1].Message; msg != "probe silent: 0 of 3 regions reporting (a, b, c silent)" {
		t.Fatalf("message = %q", msg)
	}
}

func TestRegionsStartSeedsPersistedRegionStates(t *testing.T) {
	t0 := clockStart
	seeded := model.MonitorState{
		MonitorID: 1, Status: model.StatusDown, Since: t0.Add(-time.Hour), LastCheckAt: t0.Add(-10 * time.Second),
		ConsecutiveFailures: 3,
		Regions: map[string]model.RegionState{
			"a":    {Status: down, ConsecutiveFailures: 3, LastResultAt: t0.Add(-10 * time.Second), Message: "refused"},
			"b":    {Status: down, ConsecutiveFailures: 3, LastResultAt: t0.Add(-10 * time.Second), Message: "refused"},
			"c":    {Status: up, LastResultAt: t0.Add(-10 * time.Second)},
			"gone": {Status: down, LastResultAt: t0.Add(-10 * time.Second)}, // no longer a region of the monitor
		},
	}
	h := newHarness(t, []model.Monitor{regionMonitor()}, []model.MonitorState{seeded})
	h.s.sweep()
	st, _ := h.s.State(1)
	if st.Status != down || !st.Since.Equal(seeded.Since) {
		t.Fatalf("state = %+v, want to stay DOWN since the seeded time", st)
	}
	if got := h.transitions(); len(got) != 0 {
		t.Fatalf("transitions = %v, want none on a clean restart", got)
	}
	if _, ok := st.Regions["gone"]; ok {
		t.Fatal("stale region from a changed monitor was kept")
	}
	if st.Regions["a"].Message != "refused" {
		t.Fatalf("regions not seeded: %+v", st.Regions)
	}
	// A restart in the middle of an outage keeps DOWN while nothing is
	// reporting yet (grace), and then goes to probe-silent PENDING.
	h.clk.advance(30 * time.Second)
	h.s.sweep()
	if h.status() != down {
		t.Fatalf("status = %v, want DOWN within grace", h.status())
	}
	h.clk.advance(3 * time.Minute)
	h.s.sweep()
	if h.status() != pending {
		t.Fatalf("status = %v, want PENDING after grace without results", h.status())
	}
}

func TestRegionsFlapInteraction(t *testing.T) {
	h := newHarness(t, []model.Monitor{regionMonitor()}, nil)
	h.clk.advance(time.Second)
	h.allUp()
	for i := range 6 {
		h.clk.advance(time.Minute)
		s := down
		if i%2 == 1 {
			s = up
		}
		h.report("a", s, 0)
		h.report("b", s, 0)
	}
	started := 0
	for _, tr := range h.log.all() {
		if tr.FlapChanged && tr.Flapping {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("flap start transitions = %d, want exactly 1 (%+v)", started, h.log.all())
	}
	if st, _ := h.s.State(1); st.FlapCount < flapThreshold {
		t.Fatalf("FlapCount = %d", st.FlapCount)
	}
	// An hour of stability ends flapping (storeStateLocked's decay) on the
	// next stored state.
	h.clk.advance(2 * time.Hour)
	h.allUp()
	ended := 0
	for _, tr := range h.log.all() {
		if tr.FlapChanged && !tr.Flapping {
			ended++
		}
	}
	if ended != 1 {
		t.Fatalf("flap end transitions = %d, want exactly 1 (%+v)", ended, h.log.all())
	}
	if h.status() != up {
		t.Fatalf("status = %v, want UP", h.status())
	}
}

func TestRegionsTransitionFields(t *testing.T) {
	h := newHarness(t, []model.Monitor{regionMonitor()}, nil)
	h.clk.advance(time.Second)
	h.allUp()
	h.clk.advance(time.Minute)
	h.reportMsg("a", down, 0, 1, "refused")
	h.reportMsg("b", down, 0, 1, "timeout")
	ts := h.log.all()
	tr := ts[len(ts)-1]
	if tr.From != up || tr.To != down || tr.TeamID != 7 || tr.MonitorID != 1 || tr.Message != "timeout" {
		t.Fatalf("transition = %+v", tr)
	}
	if !tr.Since.Equal(h.clk.Now()) {
		t.Fatalf("Since = %v, want %v", tr.Since, h.clk.Now())
	}
}

func TestRegionsUpsertDropsRemovedRegions(t *testing.T) {
	m := regionMonitor()
	h := newHarness(t, []model.Monitor{m}, nil)
	h.clk.advance(time.Second)
	h.allUp()
	m.Regions = []string{"a", "b"}
	m.Quorum = 1
	h.s.Upsert(m)
	st, _ := h.s.State(1)
	if _, ok := st.Regions["c"]; ok || len(st.Regions) != 2 {
		t.Fatalf("Regions = %+v, want only a and b", st.Regions)
	}
	if _, ok := h.s.regions[1]["c"]; ok {
		t.Fatal("internal region state of removed region kept")
	}
	// c reporting now is ignored.
	h.report("c", down, 0)
	st, _ = h.s.State(1)
	if _, ok := st.Regions["c"]; ok {
		t.Fatalf("result from removed region accepted: %+v", st.Regions)
	}
	// Back to local-only clears region states.
	m.Regions = nil
	m.Quorum = 0
	h.s.Upsert(m)
	st, _ = h.s.State(1)
	if len(st.Regions) != 0 {
		t.Fatalf("Regions = %+v after becoming local-only", st.Regions)
	}
}

func TestRegionsUpsertQuorumChangeReevaluates(t *testing.T) {
	m := regionMonitor()
	h := newHarness(t, []model.Monitor{m}, nil)
	h.clk.advance(time.Second)
	h.report("a", down, 0)
	h.report("b", up, 0)
	h.report("c", up, 0)
	if h.status() != up {
		t.Fatalf("status = %v", h.status())
	}
	m.Quorum = 1
	h.s.Upsert(m)
	if h.status() != down {
		t.Fatalf("status = %v, want DOWN after quorum 2→1", h.status())
	}
}

func TestRegionsRemoveForgetsState(t *testing.T) {
	h := newHarness(t, []model.Monitor{regionMonitor()}, nil)
	h.clk.advance(time.Second)
	h.allUp()
	h.s.Remove(1)
	h.s.Report(9, "a", []model.RegionResult{{MonitorID: 1, Time: h.clk.Now(), Status: down}})
	if _, ok := h.s.State(1); ok {
		t.Fatal("state resurrected after Remove")
	}
	if len(h.s.regions[1]) != 0 {
		t.Fatalf("region states kept after Remove: %+v", h.s.regions[1])
	}
}

func TestRegionsRemoteOnlyMonitorHasNoCheckLoop(t *testing.T) {
	fs := newFakeStore()
	fc := &fakeChecker{result: checker.Result{Status: model.StatusUp}}
	sched := New(fs, fc, Options{ProbeID: 1, MaxConcurrent: 4, BufferSize: 100, FlushInterval: time.Hour})
	sched.unit = time.Millisecond
	sched.jitter = noJitter
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := regionMonitor()
	m.IntervalS = 20
	sched.Start(ctx, []model.Monitor{m}, nil)
	defer func() { _ = sched.Shutdown(context.Background()) }()
	time.Sleep(100 * time.Millisecond)
	if n := fc.callCount(); n != 0 {
		t.Fatalf("local checker ran %d times for a remote-only monitor", n)
	}
}

func TestRegionsLocalRegionFeedsAggregation(t *testing.T) {
	fs := newFakeStore()
	fc := &fakeChecker{result: checker.Result{Status: model.StatusDown, Message: "local refused", Latency: 12 * time.Millisecond}}
	var log transitionLog
	sched := New(fs, fc, Options{ProbeID: 42, MaxConcurrent: 4, BufferSize: 100, FlushInterval: time.Hour, OnTransition: log.add})
	sched.unit = time.Millisecond
	sched.jitter = noJitter
	sched.sweepInterval = time.Hour

	m := regionMonitor()
	m.Regions = []string{"local", "a"}
	m.Quorum = 2
	m.IntervalS = 1000 // 1s of real time: results stay fresh for the whole test
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx, []model.Monitor{m}, nil)
	defer func() { _ = sched.Shutdown(context.Background()) }()

	eventually(t, time.Second, func() bool {
		st, ok := sched.State(1)
		return ok && st.Regions["local"].Status == model.StatusDown
	})
	if st, _ := sched.State(1); st.Status == model.StatusDown {
		t.Fatalf("one of two regions down must not reach quorum 2: %+v", st)
	}
	// The remote region agrees → quorum reached.
	sched.Report(5, "a", []model.RegionResult{{MonitorID: 1, Time: time.Now(), Status: down, Message: "remote refused"}})
	if st, _ := sched.State(1); st.Status != model.StatusDown {
		t.Fatalf("status = %v, want DOWN", st.Status)
	}
	if err := sched.flushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var sawLocal, sawRemote bool
	for _, hb := range fs.heartbeatsSnapshot() {
		switch hb.ProbeID {
		case 42:
			sawLocal = true
			if hb.Message != "local refused" || hb.LatencyMs != 12 {
				t.Fatalf("local heartbeat = %+v", hb)
			}
		case 5:
			sawRemote = true
		}
	}
	if !sawLocal || !sawRemote {
		t.Fatalf("heartbeats local=%v remote=%v", sawLocal, sawRemote)
	}
}

func TestRegionsLocalLoopMaintenance(t *testing.T) {
	fs := newFakeStore()
	fc := &fakeChecker{result: checker.Result{Status: model.StatusDown}}
	sched := New(fs, fc, Options{ProbeID: 42, MaxConcurrent: 4, BufferSize: 100, FlushInterval: time.Hour,
		Maintenance: fakeMaintenance{ids: map[int64]bool{1: true}}})
	sched.unit = time.Millisecond
	sched.jitter = noJitter
	sched.sweepInterval = time.Hour
	m := regionMonitor()
	m.Regions = []string{"local", "a"}
	m.IntervalS = 20
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx, []model.Monitor{m}, nil)
	eventually(t, time.Second, func() bool { return sched.Health().BufferedHeartbeats >= 2 })
	_ = sched.Shutdown(context.Background())
	if n := fc.callCount(); n != 0 {
		t.Fatalf("checker ran %d times during maintenance", n)
	}
	for _, hb := range fs.heartbeatsSnapshot() {
		if hb.Status != model.StatusMaintenance || hb.ProbeID != 42 {
			t.Fatalf("heartbeat = %+v", hb)
		}
	}
}

func TestRegionsSweepLoopRunsOnTimer(t *testing.T) {
	fs := newFakeStore()
	clk := &fakeClock{t: clockStart}
	var log transitionLog
	sched := New(fs, &fakeChecker{}, Options{ProbeID: 1, MaxConcurrent: 1, BufferSize: 100, FlushInterval: time.Hour, OnTransition: log.add})
	sched.now = clk.Now
	sched.sweepInterval = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx, []model.Monitor{regionMonitor()}, nil)
	defer func() { _ = sched.Shutdown(context.Background()) }()
	clk.advance(10 * time.Minute) // no result ever arrives
	eventually(t, time.Second, func() bool {
		st, ok := sched.State(1)
		return ok && st.Status == model.StatusPending
	})
}

func TestAssignments(t *testing.T) {
	mk := func(id int64, typ model.MonitorType, regions ...string) model.Monitor {
		return model.Monitor{ID: id, Name: "m", Type: typ, Config: []byte(`{"k":1}`), IntervalS: 60, RetryIntervalS: 20,
			Retries: 1, TimeoutS: 10, Regions: regions, Quorum: 1}
	}
	paused := mk(3, model.TypeHTTP, "a")
	paused.Paused = true
	ms := []model.Monitor{
		mk(5, model.TypeTCP, "a"),
		mk(1, model.TypeHTTP, "a", "b"),
		mk(2, model.TypeDNS, "b", "local"),
		paused,
		mk(4, model.TypePush, "a"),
		mk(6, model.TypeDocker, "a"),
		{ID: 7, Name: "local only", Type: model.TypeHTTP, IntervalS: 60, RetryIntervalS: 20},
	}
	h := newHarness(t, ms, nil)

	ids := func(region string) []int64 {
		_, got := h.s.Assignments(region)
		var out []int64
		for _, m := range got {
			out = append(out, m.ID)
		}
		return out
	}
	eq := func(got, want []int64) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	if got := ids("a"); !eq(got, []int64{1, 5}) {
		t.Fatalf("region a = %v, want [1 5] (paused, push, docker excluded; sorted by id)", got)
	}
	if got := ids("b"); !eq(got, []int64{1, 2}) {
		t.Fatalf("region b = %v", got)
	}
	if got := ids("nowhere"); len(got) != 0 {
		t.Fatalf("region nowhere = %v", got)
	}

	va1, _ := h.s.Assignments("a")
	va2, _ := h.s.Assignments("a")
	if va1 == "" || va1 != va2 {
		t.Fatalf("version unstable: %q vs %q", va1, va2)
	}
	vb, _ := h.s.Assignments("b")
	if vb == va1 {
		t.Fatalf("different sets share a version %q", vb)
	}

	// Unrelated changes (another region's monitor, quorum, tags, regions
	// that don't touch "a") leave a's version alone.
	m2 := mk(2, model.TypeDNS, "b", "local")
	m2.Name = "renamed"
	h.s.Upsert(m2)
	m1 := mk(1, model.TypeHTTP, "a", "b")
	m1.Quorum = 2
	m1.Tags = []string{"x"}
	m1.GroupName = "g"
	h.s.Upsert(m1)
	if v, _ := h.s.Assignments("a"); v != va1 {
		t.Fatalf("version changed on irrelevant change: %q → %q", va1, v)
	}

	changes := map[string]func(m *model.Monitor){
		"name":     func(m *model.Monitor) { m.Name = "other" },
		"config":   func(m *model.Monitor) { m.Config = []byte(`{"k":2}`) },
		"interval": func(m *model.Monitor) { m.IntervalS = 120 },
		"retry":    func(m *model.Monitor) { m.RetryIntervalS = 30 },
		"retries":  func(m *model.Monitor) { m.Retries = 2 },
		"timeout":  func(m *model.Monitor) { m.TimeoutS = 5 },
		"type":     func(m *model.Monitor) { m.Type = model.TypeKeyword },
		"pause":    func(m *model.Monitor) { m.Paused = true },
		"leave":    func(m *model.Monitor) { m.Regions = []string{"b"} },
	}
	for name, f := range changes {
		t.Run(name, func(t *testing.T) {
			m := mk(1, model.TypeHTTP, "a", "b")
			f(&m)
			h2 := newHarness(t, ms, nil)
			base, _ := h2.s.Assignments("a")
			h2.s.Upsert(m)
			got, _ := h2.s.Assignments("a")
			if got == base {
				t.Fatalf("version did not change for %s", name)
			}
		})
	}

	// Whitespace-only config differences don't change the version.
	m := mk(1, model.TypeHTTP, "a", "b")
	m.Config = []byte("{ \"k\": 1 }")
	h.s.Upsert(m)
	if v, _ := h.s.Assignments("a"); v != va1 {
		t.Fatalf("config whitespace changed the version")
	}

	// New monitor in region a changes it; removing restores it.
	h.s.Upsert(mk(9, model.TypeHTTP, "a"))
	vNew, got := h.s.Assignments("a")
	if vNew == va1 || len(got) != 3 {
		t.Fatalf("version %q → %q with %d monitors", va1, vNew, len(got))
	}
	h.s.Remove(9)
	if v, _ := h.s.Assignments("a"); v != va1 {
		t.Fatalf("version after removal = %q, want %q", v, va1)
	}
}

func TestAssignmentsReturnsCopies(t *testing.T) {
	m := regionMonitor()
	h := newHarness(t, []model.Monitor{m}, nil)
	_, got := h.s.Assignments("a")
	got[0].Regions[0] = "mutated"
	got[0].Name = "mutated"
	_, again := h.s.Assignments("a")
	if again[0].Name == "mutated" || again[0].Regions[0] == "mutated" {
		t.Fatal("Assignments leaks the scheduler's internal monitor")
	}
}

// Assigned must agree with Assignments for every monitor and region: it is
// the cheap membership test of the probe results path.
func TestAssignedMatchesAssignments(t *testing.T) {
	mk := func(id int64, typ model.MonitorType, regions ...string) model.Monitor {
		return model.Monitor{ID: id, Name: "m", Type: typ, Config: []byte(`{"k":1}`), IntervalS: 60, RetryIntervalS: 20,
			Retries: 1, TimeoutS: 10, Regions: regions, Quorum: 1}
	}
	paused := mk(3, model.TypeHTTP, "a")
	paused.Paused = true
	ms := []model.Monitor{
		mk(1, model.TypeHTTP, "a", "b"),
		mk(2, model.TypeDNS, "b", "local"),
		paused,
		mk(4, model.TypePush, "a"),
		mk(5, model.TypeTCP, "a"),
		mk(6, model.TypeDocker, "a"),
		{ID: 7, Name: "local only", Type: model.TypeHTTP, IntervalS: 60, RetryIntervalS: 20},
	}
	h := newHarness(t, ms, nil)
	for _, region := range []string{"a", "b", "c", "local"} {
		_, assigned := h.s.Assignments(region)
		want := map[int64]bool{}
		for _, m := range assigned {
			want[m.ID] = true
		}
		for id := int64(0); id <= 9; id++ { // 0 and 8-9 do not exist
			if got := h.s.Assigned(region, id); got != want[id] {
				t.Errorf("Assigned(%q, %d) = %v, Assignments says %v", region, id, got, want[id])
			}
		}
	}
}

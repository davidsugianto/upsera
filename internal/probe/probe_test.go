package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/checker"
	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/probeproto"
)

const (
	testToken      = "upr_registration-token"
	testCredential = "upp_secret-credential"
)

// lockedBuffer is a concurrency-safe log sink.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// fakeServer implements the server side of the probe protocol.
type fakeServer struct {
	srv *httptest.Server

	mu            sync.Mutex
	registerCalls int
	assignCalls   int
	notModified   int
	ifNoneMatch   []string
	authHeaders   []string
	resp          probeproto.AssignmentsResponse
	results       []probeproto.Result
	resultPosts   int
	batches       []probeproto.ResultsRequest
	failResults   bool // answer 503
	denyAssign    bool // answer 401
	denyResults   bool // answer 401
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{resp: probeproto.AssignmentsResponse{Version: "v1", Region: "sg-1"}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+probeproto.PathRegister, f.register)
	mux.HandleFunc("GET "+probeproto.PathAssignments, f.assignments)
	mux.HandleFunc("POST "+probeproto.PathResults, f.postResults)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) register(w http.ResponseWriter, r *http.Request) {
	var req probeproto.RegisterRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.registerCalls++
	f.mu.Unlock()
	if req.Token != testToken {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	_ = json.NewEncoder(w).Encode(probeproto.RegisterResponse{Credential: testCredential, ProbeID: 7, Region: "sg-1"})
}

func (f *fakeServer) authed(r *http.Request) bool {
	f.mu.Lock()
	f.authHeaders = append(f.authHeaders, r.Header.Get("Authorization"))
	f.mu.Unlock()
	return r.Header.Get("Authorization") == "Bearer "+testCredential
}

func (f *fakeServer) assignments(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	deny := f.denyAssign
	f.mu.Unlock()
	if deny || !f.authed(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assignCalls++
	inm := r.Header.Get("If-None-Match")
	f.ifNoneMatch = append(f.ifNoneMatch, inm)
	etag := `"` + f.resp.Version + `"`
	w.Header().Set("ETag", etag)
	if inm == etag {
		f.notModified++
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_ = json.NewEncoder(w).Encode(f.resp)
}

func (f *fakeServer) postResults(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	deny := f.denyResults
	f.mu.Unlock()
	if deny || !f.authed(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req probeproto.ResultsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resultPosts++
	if f.failResults {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	f.batches = append(f.batches, req)
	f.results = append(f.results, req.Results...)
	_ = json.NewEncoder(w).Encode(probeproto.ResultsResponse{Accepted: len(req.Results)})
}

func (f *fakeServer) setAssignments(version string, block bool, ms ...probeproto.Assignment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resp = probeproto.AssignmentsResponse{Version: version, Region: "sg-1", BlockPrivateTargets: block, Monitors: ms}
}

func (f *fakeServer) update(fn func(*fakeServer)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeServer) snapshot() (calls, notMod, posts int, results []probeproto.Result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.assignCalls, f.notModified, f.resultPosts, append([]probeproto.Result(nil), f.results...)
}

// fakeChecker records checks and answers via fn.
type fakeChecker struct {
	mu    sync.Mutex
	calls []checkCall
	fn    func(m model.Monitor, n int) checker.Result
}

type checkCall struct {
	at time.Time
	m  model.Monitor
}

func (c *fakeChecker) Check(_ context.Context, m model.Monitor) checker.Result {
	c.mu.Lock()
	c.calls = append(c.calls, checkCall{at: time.Now(), m: m})
	n := len(c.calls)
	fn := c.fn
	c.mu.Unlock()
	if fn != nil {
		return fn(m, n)
	}
	return checker.Result{Status: model.StatusUp, Latency: 12 * time.Millisecond, Message: "ok"}
}

func (c *fakeChecker) snapshot() []checkCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]checkCall(nil), c.calls...)
}

type harness struct {
	agent  *Agent
	done   chan error
	cancel context.CancelFunc
	logs   *lockedBuffer
	dir    string
}

type harnessOpt func(*Config)

func startAgent(t *testing.T, f *fakeServer, dir string, mut ...harnessOpt) *harness {
	t.Helper()
	logs := &lockedBuffer{}
	cfg := Config{
		ServerURL:         f.srv.URL,
		Token:             testToken,
		DataDir:           dir,
		BufferSize:        100,
		MaxConcurrent:     10,
		AllowInsecureHTTP: true,
		Version:           "test-1",
		Logger:            slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		PollInterval:      20 * time.Millisecond,
		FlushInterval:     50 * time.Millisecond,
		unit:              10 * time.Millisecond, // 1 "second" of interval = 10ms
		backoffBase:       10 * time.Millisecond,
		backoffMax:        40 * time.Millisecond,
	}
	for _, m := range mut {
		m(&cfg)
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{agent: a, done: make(chan error, 1), cancel: cancel, logs: logs, dir: dir}
	go func() { h.done <- a.Run(ctx) }()
	t.Cleanup(func() { h.stop(t) })
	return h
}

// exited waits for Run to return on its own.
func (h *harness) exited(t *testing.T) error {
	t.Helper()
	select {
	case err := <-h.done:
		h.done <- err
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not exit")
		return nil
	}
}

func (h *harness) stop(t *testing.T) error {
	t.Helper()
	h.cancel()
	select {
	case err := <-h.done:
		h.done <- err // keep for later stop() calls
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("agent did not stop")
		return nil
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func httpAssignment(id int64, url string) probeproto.Assignment {
	return probeproto.Assignment{
		MonitorID: id, Name: "m", Type: "http",
		Config:    json.RawMessage(`{"url":"` + url + `","method":"GET","accepted_status":["200-299"]}`),
		IntervalS: 5, RetryIntervalS: 2, Retries: 1, TimeoutS: 5,
	}
}

func fakeAssignment(id int64, intervalS, retryS, retries int) probeproto.Assignment {
	return probeproto.Assignment{
		MonitorID: id, Name: "fake", Type: "tcp",
		Config:    json.RawMessage(`{"host":"example.test","port":1}`),
		IntervalS: intervalS, RetryIntervalS: retryS, Retries: retries, TimeoutS: 5,
	}
}

func TestRegistrationWritesCredentialAndSecondStartReuses(t *testing.T) {
	f := newFakeServer(t)
	dir := t.TempDir()
	fc := &fakeChecker{}

	h := startAgent(t, f, dir, func(c *Config) { c.Checker = fc })
	waitFor(t, "first assignments poll", func() bool { c, _, _, _ := f.snapshot(); return c >= 1 })

	credPath := filepath.Join(dir, "credential")
	info, err := os.Stat(credPath)
	if err != nil {
		t.Fatalf("credential file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credential mode = %o, want 600", perm)
	}
	data, _ := os.ReadFile(credPath)
	if strings.TrimSpace(string(data)) != testCredential {
		t.Fatalf("credential file = %q", data)
	}
	if err := h.stop(t); err != nil {
		t.Fatalf("Run returned %v on shutdown", err)
	}
	if strings.Contains(h.logs.String(), testCredential) || strings.Contains(h.logs.String(), testToken) {
		t.Fatalf("secret leaked into logs:\n%s", h.logs.String())
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("data dir has %d entries, want only the credential", len(entries))
	}

	// Second start: no token, no second registration, credential reused.
	startAgent(t, f, dir, func(c *Config) { c.Checker = fc; c.Token = "" })
	waitFor(t, "poll after restart", func() bool { c, _, _, _ := f.snapshot(); return c >= 2 })
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.registerCalls != 1 {
		t.Fatalf("registerCalls = %d, want 1", f.registerCalls)
	}
	for _, h := range f.authHeaders {
		if h != "Bearer "+testCredential {
			t.Fatalf("Authorization = %q", h)
		}
	}
}

func TestExistingCredentialIgnoresToken(t *testing.T) {
	f := newFakeServer(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "credential"), []byte(testCredential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	startAgent(t, f, dir, func(c *Config) { c.Checker = &fakeChecker{} })
	waitFor(t, "poll", func() bool { c, _, _, _ := f.snapshot(); return c >= 1 })
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.registerCalls != 0 {
		t.Fatalf("registerCalls = %d, want 0", f.registerCalls)
	}
}

func TestRegistrationRejectedIsFatal(t *testing.T) {
	f := newFakeServer(t)
	h := startAgent(t, f, t.TempDir(), func(c *Config) { c.Token = "upr_wrong"; c.Checker = &fakeChecker{} })
	err := h.exited(t)
	var fe *FatalError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want *FatalError", err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "credential")); err == nil {
		t.Fatal("credential written despite rejected token")
	}
}

func TestNoCredentialNoTokenIsFatal(t *testing.T) {
	f := newFakeServer(t)
	h := startAgent(t, f, t.TempDir(), func(c *Config) { c.Token = ""; c.Checker = &fakeChecker{} })
	err := h.exited(t)
	var fe *FatalError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want *FatalError", err)
	}
}

func TestAssignmentsCheckedAgainstTargetAndResultsUploaded(t *testing.T) {
	var hits sync.Mutex
	n := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Lock()
		n++
		hits.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	f := newFakeServer(t)
	f.setAssignments("v1", false, httpAssignment(42, target.URL))
	startAgent(t, f, t.TempDir()) // real checker

	waitFor(t, "an uploaded result", func() bool { _, _, _, r := f.snapshot(); return len(r) >= 1 })
	_, _, _, rs := f.snapshot()
	r := rs[0]
	if r.MonitorID != 42 || !r.OK {
		t.Fatalf("result = %+v, want ok for monitor 42", r)
	}
	if r.Time.IsZero() || time.Since(r.Time) > time.Minute {
		t.Fatalf("result time = %v", r.Time)
	}
	f.mu.Lock()
	b := f.batches[0]
	f.mu.Unlock()
	if b.Version != "test-1" || b.SentAt.IsZero() || time.Since(b.SentAt) > time.Minute {
		t.Fatalf("batch = version %q sent_at %v", b.Version, b.SentAt)
	}
	hits.Lock()
	defer hits.Unlock()
	if n < 1 {
		t.Fatal("target never hit")
	}
}

func TestFailedCheckReportsNotOK(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer target.Close()
	f := newFakeServer(t)
	f.setAssignments("v1", false, httpAssignment(1, target.URL))
	startAgent(t, f, t.TempDir())
	waitFor(t, "a result", func() bool { _, _, _, r := f.snapshot(); return len(r) >= 1 })
	_, _, _, rs := f.snapshot()
	if rs[0].OK || rs[0].Message == "" {
		t.Fatalf("result = %+v, want failure with a message", rs[0])
	}
}

func TestNotModifiedKeepsMonitorsRunning(t *testing.T) {
	f := newFakeServer(t)
	f.setAssignments("v1", false, fakeAssignment(5, 2, 1, 0))
	fc := &fakeChecker{}
	startAgent(t, f, t.TempDir(), func(c *Config) { c.Checker = fc })

	waitFor(t, "304 responses", func() bool { _, nm, _, _ := f.snapshot(); return nm >= 3 })
	before := len(fc.snapshot())
	waitFor(t, "checks to continue", func() bool { return len(fc.snapshot()) > before+1 })

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ifNoneMatch[0] != "" {
		t.Fatalf("first request sent If-None-Match %q", f.ifNoneMatch[0])
	}
	if f.ifNoneMatch[len(f.ifNoneMatch)-1] != `"v1"` {
		t.Fatalf("later request If-None-Match = %q, want \"v1\"", f.ifNoneMatch[len(f.ifNoneMatch)-1])
	}
}

func TestBlockPrivateFollowsAssignments(t *testing.T) {
	f := newFakeServer(t)
	f.setAssignments("v1", true)
	h := startAgent(t, f, t.TempDir(), func(c *Config) { c.Checker = &fakeChecker{} })
	waitFor(t, "policy on", func() bool { return h.agent.policy.BlockPrivate() })
	f.setAssignments("v2", false)
	waitFor(t, "policy off", func() bool { return !h.agent.policy.BlockPrivate() })
}

func TestAssignmentChangeRestartsAndRemovalStops(t *testing.T) {
	f := newFakeServer(t)
	a1 := fakeAssignment(9, 2, 1, 0)
	f.setAssignments("v1", false, a1)
	fc := &fakeChecker{}
	startAgent(t, f, t.TempDir(), func(c *Config) { c.Checker = fc })
	waitFor(t, "first checks", func() bool { return len(fc.snapshot()) >= 2 })

	a2 := a1
	a2.Config = json.RawMessage(`{"host":"changed.test","port":2}`)
	a2.TimeoutS = 7
	f.setAssignments("v2", false, a2)
	waitFor(t, "check with new config", func() bool {
		for _, c := range fc.snapshot() {
			if strings.Contains(string(c.m.Config), "changed.test") && c.m.TimeoutS == 7 {
				return true
			}
		}
		return false
	})
	// No more old-config checks once the new one runs.
	calls := fc.snapshot()
	first := -1
	for i, c := range calls {
		if strings.Contains(string(c.m.Config), "changed.test") {
			first = i
			break
		}
	}
	for _, c := range calls[first:] {
		if strings.Contains(string(c.m.Config), "example.test") {
			t.Fatal("old config checked after restart")
		}
	}
	got := calls[0].m
	if got.ID != 9 || got.Name != "fake" || got.Type != model.TypeTCP || got.TimeoutS != 5 {
		t.Fatalf("monitor built from assignment = %+v", got)
	}

	// Removal stops the monitor.
	f.setAssignments("v3", false)
	time.Sleep(60 * time.Millisecond) // let any in-flight check settle
	settled := len(fc.snapshot())
	time.Sleep(150 * time.Millisecond)
	if after := len(fc.snapshot()); after != settled {
		t.Fatalf("checks continued after removal: %d -> %d", settled, after)
	}
}

func TestNonRemoteCapableAssignmentSkipped(t *testing.T) {
	f := newFakeServer(t)
	f.setAssignments("v1", false, probeproto.Assignment{MonitorID: 3, Name: "p", Type: "push", Config: json.RawMessage(`{}`), IntervalS: 2, TimeoutS: 5})
	fc := &fakeChecker{}
	startAgent(t, f, t.TempDir(), func(c *Config) { c.Checker = fc })
	waitFor(t, "poll", func() bool { c, _, _, _ := f.snapshot(); return c >= 2 })
	time.Sleep(100 * time.Millisecond)
	if n := len(fc.snapshot()); n != 0 {
		t.Fatalf("push monitor checked %d times", n)
	}
}

func TestServerDownBuffersThenFlushesAfterRecovery(t *testing.T) {
	f := newFakeServer(t)
	f.update(func(f *fakeServer) { f.failResults = true })
	f.setAssignments("v1", false, fakeAssignment(1, 2, 1, 0))
	fc := &fakeChecker{}
	startAgent(t, f, t.TempDir(), func(c *Config) { c.Checker = fc })

	waitFor(t, "failed uploads and buffered checks", func() bool {
		_, _, posts, _ := f.snapshot()
		return posts >= 2 && len(fc.snapshot()) >= 4
	})
	if _, _, _, r := f.snapshot(); len(r) != 0 {
		t.Fatalf("server accepted %d results while down", len(r))
	}
	f.update(func(f *fakeServer) { f.failResults = false })
	buffered := len(fc.snapshot())
	waitFor(t, "buffered results delivered", func() bool { _, _, _, r := f.snapshot(); return len(r) >= buffered })

	_, _, _, rs := f.snapshot()
	for i := 1; i < len(rs); i++ {
		if rs[i].Time.Before(rs[i-1].Time) {
			t.Fatalf("results out of order at %d", i)
		}
		if rs[i].Time.Equal(rs[i-1].Time) {
			t.Fatalf("duplicate result at %d", i)
		}
	}
}

func TestShutdownFlushesBuffer(t *testing.T) {
	f := newFakeServer(t)
	f.update(func(f *fakeServer) { f.failResults = true })
	f.setAssignments("v1", false, fakeAssignment(1, 3600, 3600, 0))
	fc := &fakeChecker{}
	h := startAgent(t, f, t.TempDir(), func(c *Config) {
		c.Checker = fc
		c.FlushInterval = time.Hour
		c.backoffBase = time.Hour // no retry before shutdown
		c.backoffMax = time.Hour
	})
	waitFor(t, "one failed post", func() bool { _, _, posts, _ := f.snapshot(); return posts >= 1 })
	f.update(func(f *fakeServer) { f.failResults = false })
	if err := h.stop(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, _, _, r := f.snapshot(); len(r) != 1 {
		t.Fatalf("results after shutdown flush = %d, want 1", len(r))
	}
}

func TestUnauthorizedAssignmentsIsFatal(t *testing.T) {
	f := newFakeServer(t)
	f.setAssignments("v1", false)
	h := startAgent(t, f, t.TempDir(), func(c *Config) { c.Checker = &fakeChecker{} })
	waitFor(t, "first poll", func() bool { c, _, _, _ := f.snapshot(); return c >= 1 })
	f.update(func(f *fakeServer) { f.denyAssign = true })
	err := h.exited(t)
	var fe *FatalError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want *FatalError", err)
	}
	if !strings.Contains(err.Error(), "401") && !strings.Contains(strings.ToLower(err.Error()), "credential") {
		t.Fatalf("error should explain the credential problem: %v", err)
	}
}

func TestUnauthorizedResultsIsFatal(t *testing.T) {
	f := newFakeServer(t)
	f.update(func(f *fakeServer) { f.denyResults = true })
	f.setAssignments("v1", false, fakeAssignment(1, 2, 1, 0))
	h := startAgent(t, f, t.TempDir(), func(c *Config) { c.Checker = &fakeChecker{} })
	err := h.exited(t)
	var fe *FatalError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want *FatalError", err)
	}
}

func TestRetryIntervalBehaviour(t *testing.T) {
	f := newFakeServer(t)
	// interval 30 units (300ms), retry 2 units (20ms), 2 retries.
	f.setAssignments("v1", false, fakeAssignment(1, 30, 2, 2))
	fc := &fakeChecker{fn: func(_ model.Monitor, n int) checker.Result {
		// fail, fail, fail, then recover.
		if n <= 3 {
			return checker.Result{Status: model.StatusDown, Message: "boom"}
		}
		return checker.Result{Status: model.StatusUp, Message: "ok"}
	}}
	startAgent(t, f, t.TempDir(), func(c *Config) { c.Checker = fc })
	waitFor(t, "five checks", func() bool { return len(fc.snapshot()) >= 5 })
	calls := fc.snapshot()
	gap := func(i int) time.Duration { return calls[i].at.Sub(calls[i-1].at) }
	// After failure 1 and 2 (<= Retries) -> retry interval.
	if g := gap(1); g > 150*time.Millisecond {
		t.Fatalf("gap after failure 1 = %v, want retry interval", g)
	}
	if g := gap(2); g > 150*time.Millisecond {
		t.Fatalf("gap after failure 2 = %v, want retry interval", g)
	}
	// Failure 3 exceeds Retries -> regular interval.
	if g := gap(3); g < 250*time.Millisecond {
		t.Fatalf("gap after failure 3 = %v, want regular interval", g)
	}
}

func TestNextDelay(t *testing.T) {
	a := probeproto.Assignment{IntervalS: 60, RetryIntervalS: 20, Retries: 2}
	cases := []struct {
		fails int
		want  int
	}{{0, 60}, {1, 20}, {2, 20}, {3, 60}, {10, 60}}
	for _, c := range cases {
		if got := nextDelayS(a, c.fails); got != c.want {
			t.Errorf("fails=%d: got %d want %d", c.fails, got, c.want)
		}
	}
	// Retries=0 never uses the retry interval; retry interval 0 falls back.
	if got := nextDelayS(probeproto.Assignment{IntervalS: 60, RetryIntervalS: 20, Retries: 0}, 1); got != 60 {
		t.Errorf("retries=0: got %d", got)
	}
	if got := nextDelayS(probeproto.Assignment{IntervalS: 60, RetryIntervalS: 0, Retries: 3}, 1); got != 60 {
		t.Errorf("retry interval 0: got %d", got)
	}
	// Interval 0 is clamped to something sane.
	if got := nextDelayS(probeproto.Assignment{}, 0); got < 1 {
		t.Errorf("zero interval: got %d", got)
	}
}

func TestNewRefusesPlainHTTPWithoutFlag(t *testing.T) {
	_, err := New(Config{ServerURL: "http://probe.example.com", DataDir: t.TempDir()})
	if err == nil {
		t.Fatal("expected error for http:// without ALLOW_INSECURE_HTTP")
	}
	if _, err := New(Config{ServerURL: "http://probe.example.com", DataDir: t.TempDir(), AllowInsecureHTTP: true}); err != nil {
		t.Fatalf("http with flag: %v", err)
	}
	if _, err := New(Config{ServerURL: "https://probe.example.com/", DataDir: t.TempDir()}); err != nil {
		t.Fatalf("https: %v", err)
	}
	if _, err := New(Config{ServerURL: "", DataDir: t.TempDir()}); err == nil {
		t.Fatal("expected error for empty server url")
	}
	if _, err := New(Config{ServerURL: "ftp://x", DataDir: t.TempDir()}); err == nil {
		t.Fatal("expected error for ftp scheme")
	}
}

func TestRingBufferDropsOldest(t *testing.T) {
	rb := newRing(3)
	for i := int64(1); i <= 5; i++ {
		rb.Push(probeproto.Result{MonitorID: i})
	}
	if rb.Len() != 3 || rb.Dropped() != 2 {
		t.Fatalf("len=%d dropped=%d, want 3/2", rb.Len(), rb.Dropped())
	}
	got := rb.Take(10)
	if len(got) != 3 || got[0].MonitorID != 3 || got[2].MonitorID != 5 {
		t.Fatalf("take = %+v", got)
	}
	if rb.Len() != 0 {
		t.Fatalf("len after take = %d", rb.Len())
	}
}

func TestRingBufferTakeLimitAndPutFront(t *testing.T) {
	rb := newRing(4)
	for i := int64(1); i <= 4; i++ {
		rb.Push(probeproto.Result{MonitorID: i})
	}
	batch := rb.Take(2) // 1,2
	if len(batch) != 2 || rb.Len() != 2 {
		t.Fatalf("batch=%d len=%d", len(batch), rb.Len())
	}
	rb.PutFront(batch) // back to 1,2,3,4
	if rb.Len() != 4 || rb.Dropped() != 0 {
		t.Fatalf("len=%d dropped=%d", rb.Len(), rb.Dropped())
	}
	all := rb.Take(10)
	for i, r := range all {
		if r.MonitorID != int64(i+1) {
			t.Fatalf("order broken: %+v", all)
		}
	}

	// PutFront into a buffer that filled meanwhile drops the oldest of the batch.
	batch = []probeproto.Result{{MonitorID: 1}, {MonitorID: 2}, {MonitorID: 3}}
	rb.Push(probeproto.Result{MonitorID: 10})
	rb.Push(probeproto.Result{MonitorID: 11})
	rb.PutFront(batch) // space for 2 -> keep 2,3
	if rb.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", rb.Dropped())
	}
	all = rb.Take(10)
	ids := []int64{}
	for _, r := range all {
		ids = append(ids, r.MonitorID)
	}
	want := []int64{2, 3, 10, 11}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v", ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
}

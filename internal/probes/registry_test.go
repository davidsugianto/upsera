package probes

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

type fakeStore struct {
	mu    sync.Mutex
	calls [][]model.ProbeSeen
	err   error
}

func (f *fakeStore) TouchProbes(_ context.Context, seen []model.ProbeSeen) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, append([]model.ProbeSeen(nil), seen...))
	return nil
}

func (f *fakeStore) all() [][]model.ProbeSeen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]model.ProbeSeen(nil), f.calls...)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type event struct {
	region string
	at     time.Time
}

type harness struct {
	r         *Registry
	clk       *clock
	st        *fakeStore
	mu        sync.Mutex
	silent    []event
	recovered []event
}

func (h *harness) silents() []event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]event(nil), h.silent...)
}

func (h *harness) recoveries() []event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]event(nil), h.recovered...)
}

func newHarness() *harness {
	h := &harness{
		clk: &clock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)},
		st:  &fakeStore{},
	}
	h.r = NewRegistry(Options{
		Store:       h.st,
		SilentAfter: time.Minute,
		Now:         h.clk.now,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnSilent: func(p model.ProbeAuth, since time.Time) {
			h.mu.Lock()
			h.silent = append(h.silent, event{p.Region, since})
			h.mu.Unlock()
		},
		OnRecovered: func(p model.ProbeAuth, at time.Time) {
			h.mu.Lock()
			h.recovered = append(h.recovered, event{p.Region, at})
			h.mu.Unlock()
		},
	})
	return h
}

func auth(id int64, region, cred string) model.ProbeAuth {
	p := model.ProbeAuth{ID: id, Name: region, Region: region}
	if cred != "" {
		sum := sha256.Sum256([]byte(cred))
		p.TokenHash = sum[:]
	}
	return p
}

func TestAuthenticate(t *testing.T) {
	h := newHarness()
	h.r.Set([]model.ProbeAuth{auth(1, "sg-1", "upp_good"), auth(2, "fra-1", "upp_other")})
	if p, ok := h.r.Authenticate("upp_good"); !ok || p.ID != 1 || p.Region != "sg-1" {
		t.Fatalf("hit = %+v %v", p, ok)
	}
	for _, c := range []string{"", "upp_bad", "ups_good", "upp_goo", "upp_good "} {
		if _, ok := h.r.Authenticate(c); ok {
			t.Errorf("Authenticate(%q) succeeded", c)
		}
	}
	h.r.Remove(1)
	if _, ok := h.r.Authenticate("upp_good"); ok {
		t.Fatal("removed probe still authenticates")
	}
	h.r.Add(auth(3, "ams-1", "upp_new"))
	if p, ok := h.r.Authenticate("upp_new"); !ok || p.ID != 3 {
		t.Fatalf("added probe: %+v %v", p, ok)
	}
}

func TestAddReplacingCredentialRevokesOldOne(t *testing.T) {
	h := newHarness()
	h.r.Add(auth(1, "sg-1", "upp_old"))
	h.r.Add(auth(1, "sg-1", "upp_new"))
	if _, ok := h.r.Authenticate("upp_old"); ok {
		t.Fatal("old credential still works")
	}
	if _, ok := h.r.Authenticate("upp_new"); !ok {
		t.Fatal("new credential rejected")
	}
}

func TestUnregisteredCannotAuthenticateOrGoSilent(t *testing.T) {
	h := newHarness()
	h.r.Set([]model.ProbeAuth{auth(1, "sg-1", "")})
	h.clk.advance(time.Hour)
	h.r.check()
	if len(h.silents()) != 0 {
		t.Fatalf("unregistered probe reported silent: %v", h.silents())
	}
	if h.r.Online("sg-1") {
		t.Fatal("unregistered probe is online")
	}
}

func TestSilentAfterStartupGraceThenOnce(t *testing.T) {
	h := newHarness()
	start := h.clk.now()
	h.r.Set([]model.ProbeAuth{auth(1, "sg-1", "upp_a")})

	h.clk.advance(59 * time.Second)
	h.r.check()
	if len(h.silents()) != 0 {
		t.Fatal("silent inside the startup grace")
	}
	h.clk.advance(2 * time.Second) // 61s since start
	h.r.check()
	h.r.check()
	h.clk.advance(time.Hour)
	h.r.check()
	got := h.silents()
	if len(got) != 1 || got[0].region != "sg-1" || !got[0].at.Equal(start) {
		t.Fatalf("silent events = %+v, want exactly one since %v", got, start)
	}
	if len(h.recoveries()) != 0 {
		t.Fatal("recovered without contact")
	}
}

func TestSeenKeepsProbeAlive(t *testing.T) {
	h := newHarness()
	h.r.Set([]model.ProbeAuth{auth(1, "sg-1", "upp_a")})
	for range 5 {
		h.clk.advance(30 * time.Second)
		h.r.Seen(1, "v1")
		h.r.check()
	}
	if len(h.silents()) != 0 || len(h.recoveries()) != 0 {
		t.Fatalf("events for a live probe: %v %v", h.silents(), h.recoveries())
	}
}

func TestRecoveredOncePerSilence(t *testing.T) {
	h := newHarness()
	h.r.Set([]model.ProbeAuth{auth(1, "sg-1", "upp_a")})
	h.clk.advance(2 * time.Minute)
	h.r.check()
	if len(h.silents()) != 1 {
		t.Fatal("expected silent")
	}
	at := h.clk.now()
	h.r.Seen(1, "v1")
	h.r.Seen(1, "v1")
	got := h.recoveries()
	if len(got) != 1 || got[0].region != "sg-1" || !got[0].at.Equal(at) {
		t.Fatalf("recovered = %+v", got)
	}
	// a second silence is reported again
	h.clk.advance(2 * time.Minute)
	h.r.check()
	if len(h.silents()) != 2 {
		t.Fatalf("silents = %d, want 2", len(h.silents()))
	}
	h.r.Seen(1, "v1")
	if len(h.recoveries()) != 2 {
		t.Fatalf("recoveries = %d, want 2", len(h.recoveries()))
	}
}

func TestSeenWithoutSilenceDoesNotRecover(t *testing.T) {
	h := newHarness()
	h.r.Set([]model.ProbeAuth{auth(1, "sg-1", "upp_a")})
	h.r.Seen(1, "v1")
	h.r.Seen(99, "v1") // unknown ids are ignored
	if len(h.recoveries()) != 0 {
		t.Fatal("recovered without silence")
	}
}

func TestCallbacksRunWithoutLocks(t *testing.T) {
	h := newHarness()
	var r *Registry
	r = NewRegistry(Options{
		Store: h.st, SilentAfter: time.Minute, Now: h.clk.now,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnSilent: func(p model.ProbeAuth, _ time.Time) {
			r.Online(p.Region) // would deadlock if a lock were held
			r.Seen(p.ID, "v")  // recovers synchronously, reentering
		},
		OnRecovered: func(p model.ProbeAuth, _ time.Time) { r.Remove(p.ID) },
	})
	r.Set([]model.ProbeAuth{auth(1, "sg-1", "upp_a")})
	h.clk.advance(2 * time.Minute)
	done := make(chan struct{})
	go func() { r.check(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: callback ran under a registry lock")
	}
}

func TestOnline(t *testing.T) {
	h := newHarness()
	h.r.Set([]model.ProbeAuth{auth(1, "sg-1", "upp_a")})
	if !h.r.Online(model.LocalRegion) {
		t.Fatal("local must always be online")
	}
	if h.r.Online("nowhere") {
		t.Fatal("unknown region online")
	}
	if h.r.Online("sg-1") {
		t.Fatal("never seen probe online")
	}
	h.r.Seen(1, "v1")
	if !h.r.Online("sg-1") {
		t.Fatal("just seen probe offline")
	}
	h.clk.advance(time.Minute)
	if !h.r.Online("sg-1") {
		t.Fatal("offline exactly at the limit")
	}
	h.clk.advance(time.Second)
	if h.r.Online("sg-1") {
		t.Fatal("online past SilentAfter")
	}
}

func TestOnlineUsesPersistedLastSeen(t *testing.T) {
	h := newHarness()
	p := auth(1, "sg-1", "upp_a")
	p.LastSeenAt = new(h.clk.now().Add(-10 * time.Second))
	h.r.Set([]model.ProbeAuth{p})
	if !h.r.Online("sg-1") {
		t.Fatal("persisted last_seen within SilentAfter should count")
	}
}

func TestFlushWritesDirtyOnly(t *testing.T) {
	h := newHarness()
	h.r.Set([]model.ProbeAuth{auth(1, "sg-1", "upp_a"), auth(2, "fra-1", "upp_b")})
	ctx := context.Background()
	if err := h.r.flush(ctx); err != nil || len(h.st.all()) != 0 {
		t.Fatalf("empty flush: %v %v", err, h.st.all())
	}
	h.r.Seen(1, "v1.2")
	seenAt := h.clk.now()
	h.clk.advance(time.Second)
	if err := h.r.flush(ctx); err != nil {
		t.Fatal(err)
	}
	calls := h.st.all()
	if len(calls) != 1 || len(calls[0]) != 1 || calls[0][0].ID != 1 || calls[0][0].Version != "v1.2" || !calls[0][0].At.Equal(seenAt) {
		t.Fatalf("flush calls = %+v", calls)
	}
	if err := h.r.flush(ctx); err != nil || len(h.st.all()) != 1 {
		t.Fatalf("second flush wrote again: %v", h.st.all())
	}
}

func TestFlushFailureRetries(t *testing.T) {
	h := newHarness()
	h.r.Set([]model.ProbeAuth{auth(1, "sg-1", "upp_a")})
	h.r.Seen(1, "v1")
	h.st.mu.Lock()
	h.st.err = errors.New("db down")
	h.st.mu.Unlock()
	if err := h.r.flush(context.Background()); err == nil {
		t.Fatal("want error")
	}
	h.st.mu.Lock()
	h.st.err = nil
	h.st.mu.Unlock()
	if err := h.r.flush(context.Background()); err != nil || len(h.st.all()) != 1 {
		t.Fatalf("retry: %v %v", err, h.st.all())
	}
}

func TestShutdownFinalFlush(t *testing.T) {
	h := newHarness()
	h.r.Set([]model.ProbeAuth{auth(1, "sg-1", "upp_a")})
	ctx, cancel := context.WithCancel(context.Background())
	h.r.Start(ctx)
	h.r.Seen(1, "v9")
	cancel()
	if err := h.r.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := h.st.all()
	if len(calls) == 0 || calls[len(calls)-1][0].Version != "v9" {
		t.Fatalf("no final flush: %+v", calls)
	}
}

func TestBackgroundLoopsFlushAndDetectSilence(t *testing.T) {
	h := newHarness()
	h.r = NewRegistry(Options{
		Store: h.st, SilentAfter: 50 * time.Millisecond, CheckEvery: 5 * time.Millisecond, FlushEvery: 5 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnSilent: func(p model.ProbeAuth, since time.Time) {
			h.mu.Lock()
			h.silent = append(h.silent, event{p.Region, since})
			h.mu.Unlock()
		},
	})
	h.r.Set([]model.ProbeAuth{auth(1, "sg-1", "upp_a")})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.r.Start(ctx)
	h.r.Seen(1, "v1")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && (len(h.silents()) == 0 || len(h.st.all()) == 0) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(h.silents()) != 1 || len(h.st.all()) == 0 {
		t.Fatalf("silents=%v flushes=%v", h.silents(), h.st.all())
	}
	if err := h.r.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

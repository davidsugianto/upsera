package alerting

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/notify"
)

// Teams of the probe tests.
const (
	pTeamA int64 = 1 // two monitors in sg-1, default telegram + non-default smtp
	pTeamB int64 = 2 // monitor in sg-1+fra-1, two default channels
	pTeamC int64 = 3 // only a paused monitor in sg-1
	pTeamD int64 = 4 // monitor in sg-1 but no default channel
	pTeamE int64 = 5 // only fra-1 and local monitors
)

func probeMonitor(id, team int64, paused bool, regions ...string) model.Monitor {
	return model.Monitor{ID: id, TeamID: team, Name: "m", Type: model.TypeHTTP, Paused: paused, Regions: regions}
}

func newProbeHarness(t *testing.T) (*Engine, *recorder, *fakeStore) {
	t.Helper()
	rec := &recorder{}
	st := &fakeStore{alerts: map[string]model.Alert{}}
	e := New(Options{
		Store: st,
		Notifiers: map[model.ChannelType]notify.Notifier{
			model.ChannelTelegram: fakeNotifier{"telegram", rec},
			model.ChannelSMTP:     fakeNotifier{"smtp", rec},
		},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		FlushInterval: time.Hour,
	})
	e.load(Snapshot{
		Monitors: []model.Monitor{
			probeMonitor(1, pTeamA, false, "local", "sg-1"),
			probeMonitor(2, pTeamA, false, "sg-1"),
			probeMonitor(3, pTeamB, false, "sg-1", "fra-1"),
			probeMonitor(4, pTeamC, true, "sg-1"),
			probeMonitor(5, pTeamD, false, "sg-1"),
			probeMonitor(6, pTeamE, false, "fra-1"),
			probeMonitor(7, pTeamE, false), // local only
		},
		Channels: []model.Channel{
			{ID: 1, TeamID: pTeamA, Type: model.ChannelTelegram, Name: "a-tg", IsDefault: true, Config: json.RawMessage(`{}`)},
			{ID: 2, TeamID: pTeamA, Type: model.ChannelSMTP, Name: "a-mail", Config: json.RawMessage(`{}`)},
			{ID: 3, TeamID: pTeamB, Type: model.ChannelTelegram, Name: "b-tg", IsDefault: true, Config: json.RawMessage(`{}`)},
			{ID: 4, TeamID: pTeamB, Type: model.ChannelSMTP, Name: "b-mail", IsDefault: true, Config: json.RawMessage(`{}`)},
			{ID: 5, TeamID: pTeamC, Type: model.ChannelTelegram, Name: "c-tg", IsDefault: true, Config: json.RawMessage(`{}`)},
			{ID: 6, TeamID: pTeamD, Type: model.ChannelSMTP, Name: "d-mail", Config: json.RawMessage(`{}`)},
			{ID: 7, TeamID: pTeamE, Type: model.ChannelTelegram, Name: "e-tg", IsDefault: true, Config: json.RawMessage(`{}`)},
		},
	}, &fakeStates{m: map[int64]model.MonitorState{}})
	t.Cleanup(func() { e.disp.Close(context.Background()) })
	return e, rec, st
}

func names(ss []sent) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.Channel)
	}
	return out
}

func TestProbeSilentNotifiesAffectedTeamsDefaultsOnly(t *testing.T) {
	e, rec, _ := newProbeHarness(t)
	since := time.Date(2026, 1, 1, 11, 58, 0, 0, time.UTC)
	e.ProbeSilent("sg-1", since)
	e.drain()

	// team A (default only), team B (both defaults); not C (paused), D (no default), E (other region)
	eventually(t, func() bool { return len(rec.matching(model.EventProbeSilent, "")) == 3 })
	quiet()
	got := rec.matching(model.EventProbeSilent, "")
	if len(got) != 3 {
		t.Fatalf("sends = %v", names(got))
	}
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.Channel] = true
		ev := s.Ev
		if ev.Region != "sg-1" || !ev.At.Equal(since) || ev.Ackable || ev.AlertID != "" || ev.MonitorID != 0 {
			t.Errorf("event = %+v", ev)
		}
		if notify.Title(ev) != "[PROBE SILENT] sg-1" || !strings.Contains(notify.Body(ev), "sg-1") {
			t.Errorf("text = %q / %q", notify.Title(ev), notify.Body(ev))
		}
	}
	for _, want := range []string{"a-tg", "b-tg", "b-mail"} {
		if !seen[want] {
			t.Errorf("missing send to %s (got %v)", want, names(got))
		}
	}
	if len(rec.all()) != 3 {
		t.Errorf("unexpected extra sends: %v", names(rec.all()))
	}
}

func TestProbeSilentOncePerTeamDespiteManyMonitors(t *testing.T) {
	e, rec, _ := newProbeHarness(t)
	e.ProbeSilent("sg-1", time.Unix(1000, 0))
	e.drain()
	eventually(t, func() bool { return len(rec.matching(model.EventProbeSilent, "a-tg")) >= 1 })
	quiet()
	if n := len(rec.matching(model.EventProbeSilent, "a-tg")); n != 1 {
		t.Fatalf("team A has 2 monitors in sg-1 but got %d messages", n)
	}
}

func TestProbeRecovered(t *testing.T) {
	e, rec, _ := newProbeHarness(t)
	at := time.Date(2026, 1, 1, 12, 5, 0, 0, time.UTC)
	e.ProbeRecovered("fra-1", at)
	e.drain()
	eventually(t, func() bool { return len(rec.matching(model.EventProbeRecovered, "")) == 3 })
	quiet()
	got := rec.matching(model.EventProbeRecovered, "")
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.Channel] = true
		if s.Ev.Region != "fra-1" || !s.Ev.At.Equal(at) || s.Ev.Ackable {
			t.Errorf("event = %+v", s.Ev)
		}
		if notify.Title(s.Ev) != "[PROBE UP] fra-1" {
			t.Errorf("title = %q", notify.Title(s.Ev))
		}
	}
	// team B (fra-1 monitor) both defaults, team E default
	for _, want := range []string{"b-tg", "b-mail", "e-tg"} {
		if !seen[want] {
			t.Errorf("missing send to %s (got %v)", want, names(got))
		}
	}
}

func TestProbeEventsNothingForUnusedRegion(t *testing.T) {
	e, rec, _ := newProbeHarness(t)
	e.ProbeSilent("ams-9", time.Now())
	e.ProbeRecovered("ams-9", time.Now())
	e.ProbeSilent(model.LocalRegion, time.Now()) // team A and E have local monitors with defaults
	e.drain()
	quiet()
	for _, s := range rec.all() {
		if s.Ev.Region == "ams-9" {
			t.Fatalf("send for an unused region: %+v", s)
		}
	}
}

func TestProbeEventsIgnorePausedWhenMonitorLaterUnpaused(t *testing.T) {
	e, rec, _ := newProbeHarness(t)
	e.UpsertMonitor(probeMonitor(4, pTeamC, false, "sg-1")) // team C resumes
	e.ProbeSilent("sg-1", time.Now())
	e.drain()
	eventually(t, func() bool { return len(rec.matching(model.EventProbeSilent, "c-tg")) == 1 })
}

func TestProbeEventsAreQueuedNotBlocking(t *testing.T) {
	e, rec, _ := newProbeHarness(t)
	e.mu.Lock() // a held engine lock must not block the callers
	done := make(chan struct{})
	go func() {
		e.ProbeSilent("sg-1", time.Now())
		e.ProbeRecovered("sg-1", time.Now())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		e.mu.Unlock()
		t.Fatal("ProbeSilent/ProbeRecovered blocked")
	}
	e.mu.Unlock()
	quiet()
	if len(rec.all()) != 0 {
		t.Fatal("sent before the loop processed the queue")
	}
	e.drain()
	eventually(t, func() bool { return len(rec.all()) == 6 })
}

func TestProbeEventsProcessedByRunningLoop(t *testing.T) {
	e, rec, _ := newProbeHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.runCtx, e.cancel = context.WithCancel(ctx)
	e.wg.Add(1)
	go e.loop(e.runCtx)
	e.ProbeSilent("sg-1", time.Now())
	eventually(t, func() bool { return len(rec.matching(model.EventProbeSilent, "")) == 3 })
	e.cancel()
	e.wg.Wait()
}

func TestProbeEventShutdownDrains(t *testing.T) {
	e, rec, _ := newProbeHarness(t)
	e.ProbeSilent("sg-1", time.Now())
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(rec.matching(model.EventProbeSilent, "")); n != 3 {
		t.Fatalf("shutdown sent %d, want 3", n)
	}
}

func TestProbeNotificationLogEntries(t *testing.T) {
	e, rec, st := newProbeHarness(t)
	since := time.Unix(1_767_268_800, 0)
	e.ProbeSilent("sg-1", since)
	e.drain()
	eventually(t, func() bool { return len(rec.matching(model.EventProbeSilent, "")) == 3 })
	eventually(t, func() bool {
		e.outMu.Lock()
		defer e.outMu.Unlock()
		return len(e.logs) == 3
	})
	if err := e.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.logs) != 3 {
		t.Fatalf("logs = %+v", st.logs)
	}
	for _, l := range st.logs {
		if l.Event != model.EventProbeSilent || l.MonitorID != nil || l.AlertID != nil || l.ChannelID == nil || !l.OK ||
			l.DedupeKey != "probe:sg-1:1767268800:silent" {
			t.Errorf("log entry = %+v", l)
		}
	}
}

func TestProbeDedupeKeys(t *testing.T) {
	e, _, _ := newProbeHarness(t)
	var keys []string
	e.disp = notify.NewDispatcher(map[model.ChannelType]notify.Notifier{
		model.ChannelTelegram: fakeNotifier{"telegram", &recorder{}},
		model.ChannelSMTP:     fakeNotifier{"smtp", &recorder{}},
	}, func(at notify.Attempt) { e.outMu.Lock(); keys = append(keys, at.Job.DedupeKey); e.outMu.Unlock() }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.ProbeSilent("sg-1", time.Unix(500, 0))
	e.ProbeRecovered("sg-1", time.Unix(900, 0))
	e.drain()
	e.disp.Close(context.Background())
	e.outMu.Lock()
	defer e.outMu.Unlock()
	want := map[string]int{"probe:sg-1:500:silent": 3, "probe:sg-1:900:recovered": 3}
	got := map[string]int{}
	for _, k := range keys {
		got[k]++
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("key %s sent %d times, want %d (all %v)", k, got[k], n, got)
		}
	}
}

package alerting

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/notify"
)

// latency applies a transition into or out of DEGRADED, as the scheduler
// emits it (with the region's latency).
func (h *harness) latency(id int64, from, to model.Status) model.Transition {
	h.t.Helper()
	h.states.set(id, model.MonitorState{Status: to, Since: h.clock})
	tr := model.Transition{MonitorID: id, TeamID: teamID, From: from, To: to, At: h.clock, Since: h.clock,
		Region: "sg-1", LatencyP95Ms: 640, BaselineMs: 52}
	h.e.Publish(tr)
	h.e.drain()
	return tr
}

func TestDegradedSendsOneMessageToStepZeroAndNoAlert(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.latency(1, model.StatusUp, model.StatusDegraded)
	eventually(t, func() bool { return len(h.rec.matching(model.EventDegraded, "")) == 1 })
	h.advance(time.Hour) // no escalation to step 1 (smtp)
	quiet()
	got := h.rec.all()
	if len(got) != 1 || got[0].Channel != "telegram" {
		t.Fatalf("sends = %+v, want one degraded to telegram", got)
	}
	ev := got[0].Ev
	if ev.Region != "sg-1" || ev.LatencyP95Ms != 640 || ev.BaselineMs != 52 || ev.MonitorName != "api" ||
		ev.Ackable || ev.AlertID != "" {
		t.Fatalf("degraded event = %+v", ev)
	}
	if a := h.alert(1); a != nil {
		t.Fatalf("DEGRADED opened an alert: %+v", a)
	}

	h.advance(time.Minute)
	h.latency(1, model.StatusDegraded, model.StatusUp)
	eventually(t, func() bool { return len(h.rec.matching(model.EventLatencyRecovered, "")) == 1 })
	quiet()
	if got := h.rec.matching(model.EventLatencyRecovered, "telegram"); len(got) != 1 || got[0].Ev.Region != "sg-1" {
		t.Fatalf("latency_recovered sends = %+v", got)
	}
	if n := len(h.rec.all()); n != 2 {
		t.Fatalf("%d sends in total, want 2", n)
	}
	if a := h.alert(1); a != nil {
		t.Fatalf("alert opened: %+v", a)
	}
}

func TestDownToDegradedResolvesWithoutDegradedMessage(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.transition(1, model.StatusUp, model.StatusDown)
	eventually(t, func() bool { return len(h.rec.matching(model.EventDown, "telegram")) == 1 })
	h.advance(5 * time.Second)
	h.latency(1, model.StatusDown, model.StatusDegraded)
	if a := h.alert(1); a != nil {
		t.Fatalf("alert still open after DOWN -> DEGRADED: %+v", a)
	}
	eventually(t, func() bool { return len(h.rec.matching(model.EventRecovered, "telegram")) == 1 })
	quiet()
	if n := len(h.rec.matching(model.EventDegraded, "")); n != 0 {
		t.Fatalf("%d degraded messages after DOWN -> DEGRADED, want 0", n)
	}
}

func TestPendingToDegradedSendsNothing(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.latency(1, model.StatusPending, model.StatusDegraded)
	quiet()
	if got := h.rec.all(); len(got) != 0 {
		t.Fatalf("sends = %+v, want none", got)
	}
}

func TestDegradedRecoveryReleasesSuppressedChildren(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "parent", nil), monitor(2, "child", new(int64(1)))}, nil, nil)
	h.transition(1, model.StatusUp, model.StatusDown)
	h.transition(2, model.StatusUp, model.StatusDown)
	if a := h.alert(2); a == nil || !a.Suppressed {
		t.Fatalf("child alert = %+v, want suppressed", a)
	}
	h.latency(1, model.StatusDown, model.StatusDegraded)
	eventually(t, func() bool {
		for _, s := range h.rec.matching(model.EventDown, "telegram") {
			if s.Ev.MonitorName == "child" {
				return true
			}
		}
		return false
	})
	if a := h.alert(2); a.Suppressed {
		t.Fatalf("child alert = %+v, want released", a)
	}
}

func TestDegradedSuppressedByDownParent(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "parent", nil), monitor(2, "child", new(int64(1)))}, nil, nil)
	h.transition(1, model.StatusUp, model.StatusDown)
	eventually(t, func() bool { return len(h.rec.matching(model.EventDown, "telegram")) == 1 })
	h.latency(2, model.StatusUp, model.StatusDegraded)
	h.latency(2, model.StatusDegraded, model.StatusUp)
	quiet()
	for _, s := range h.rec.all() {
		if s.Ev.MonitorName == "child" {
			t.Fatalf("child notified while its parent is down: %+v", s)
		}
	}
}

func TestDegradedNotSentWhilePaused(t *testing.T) {
	m := monitor(1, "api", nil)
	m.Paused = true
	h := newHarness(t, []model.Monitor{m}, nil, nil)
	h.latency(1, model.StatusUp, model.StatusDegraded)
	quiet()
	if got := h.rec.all(); len(got) != 0 {
		t.Fatalf("sends = %+v, want none for a paused monitor", got)
	}
}

func TestDegradedDedupeOnReplay(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	var keys []string
	h.e.disp.Close(context.Background())
	h.e.disp = notify.NewDispatcher(map[model.ChannelType]notify.Notifier{
		model.ChannelTelegram: fakeNotifier{"telegram", h.rec},
		model.ChannelSMTP:     fakeNotifier{"smtp", h.rec},
	}, func(at notify.Attempt) { h.e.outMu.Lock(); keys = append(keys, at.Job.DedupeKey); h.e.outMu.Unlock() },
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	tr := h.latency(1, model.StatusUp, model.StatusDegraded)
	h.e.Publish(tr) // the same transition again
	h.e.drain()
	h.advance(time.Minute)
	back := h.latency(1, model.StatusDegraded, model.StatusUp)
	h.e.Publish(back)
	h.e.drain()
	h.e.disp.Close(context.Background())

	h.e.outMu.Lock()
	defer h.e.outMu.Unlock()
	want := []string{
		"degraded:1:" + strconv.FormatInt(tr.At.Unix(), 10),
		"latency_recovered:1:" + strconv.FormatInt(back.At.Unix(), 10),
	}
	if len(keys) != len(want) || keys[0] != want[0] || keys[1] != want[1] {
		t.Fatalf("dedupe keys = %v, want %v", keys, want)
	}
}

func TestLoadResolvesAlertOfDegradedMonitor(t *testing.T) {
	start := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)
	open := []model.Alert{{ID: "a1", TeamID: teamID, MonitorID: 1, IncidentStart: start, OpenedAt: start,
		Step: 0, NotifiedChannelIDs: []int64{chTelegram}}}
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, open, func(s *fakeStates) {
		s.set(1, model.MonitorState{Status: model.StatusDegraded, Since: start.Add(30 * time.Minute)})
	})
	if a := h.alert(1); a != nil {
		t.Fatalf("alert of a DEGRADED monitor still open after load: %+v", a)
	}
	eventually(t, func() bool { return len(h.rec.matching(model.EventRecovered, "telegram")) == 1 })
	quiet()
	if n := len(h.rec.matching(model.EventDegraded, "")); n != 0 {
		t.Fatalf("load sent %d degraded messages", n)
	}
}

func TestLatencyRecoveredAfterPending(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.latency(1, model.StatusUp, model.StatusDegraded)
	eventually(t, func() bool { return len(h.rec.matching(model.EventDegraded, "")) == 1 })
	h.advance(time.Minute)
	h.latency(1, model.StatusDegraded, model.StatusPending)
	h.advance(time.Minute)
	h.latency(1, model.StatusPending, model.StatusUp)
	eventually(t, func() bool { return len(h.rec.matching(model.EventLatencyRecovered, "telegram")) == 1 })
	quiet()
	if n := len(h.rec.all()); n != 2 {
		t.Fatalf("%d sends, want degraded + latency_recovered only: %+v", n, h.rec.all())
	}
}

func TestLatencyRecoveredAfterPendingBackToDegradedThenUp(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.latency(1, model.StatusUp, model.StatusDegraded)
	h.advance(time.Minute)
	h.latency(1, model.StatusDegraded, model.StatusPending)
	h.advance(time.Minute)
	h.latency(1, model.StatusPending, model.StatusDegraded) // still slow: nothing new to say
	h.advance(time.Minute)
	h.latency(1, model.StatusDegraded, model.StatusUp)
	eventually(t, func() bool { return len(h.rec.matching(model.EventLatencyRecovered, "")) == 1 })
	quiet()
	if n := len(h.rec.matching(model.EventDegraded, "")); n != 1 {
		t.Fatalf("%d degraded messages, want 1", n)
	}
	if n := len(h.rec.matching(model.EventLatencyRecovered, "")); n != 1 {
		t.Fatalf("%d latency_recovered messages, want 1", n)
	}
}

func TestNoLatencyRecoveredAfterDownRecovery(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.latency(1, model.StatusUp, model.StatusDegraded)
	eventually(t, func() bool { return len(h.rec.matching(model.EventDegraded, "")) == 1 })
	h.advance(time.Minute)
	h.latency(1, model.StatusDegraded, model.StatusDown)
	eventually(t, func() bool { return len(h.rec.matching(model.EventDown, "telegram")) == 1 })
	h.advance(time.Minute)
	h.latency(1, model.StatusDown, model.StatusUp)
	eventually(t, func() bool { return len(h.rec.matching(model.EventRecovered, "telegram")) == 1 })
	quiet()
	if n := len(h.rec.matching(model.EventLatencyRecovered, "")); n != 0 {
		t.Fatalf("%d latency_recovered messages after a DOWN recovery, want 0", n)
	}
}

func TestNoLatencyRecoveredWithoutDegradedMessage(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.latency(1, model.StatusPending, model.StatusDegraded) // not announced (see TestPendingToDegradedSendsNothing)
	h.advance(time.Minute)
	h.latency(1, model.StatusDegraded, model.StatusPending)
	h.advance(time.Minute)
	h.latency(1, model.StatusPending, model.StatusUp)
	quiet()
	if got := h.rec.all(); len(got) != 0 {
		t.Fatalf("sends = %+v, want none", got)
	}
}

func TestLatencyRecoveredAfterRestartWhileDegraded(t *testing.T) {
	start := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, func(s *fakeStates) {
		s.set(1, model.MonitorState{Status: model.StatusDegraded, Since: start})
	})
	h.latency(1, model.StatusDegraded, model.StatusPending)
	h.advance(time.Minute)
	h.latency(1, model.StatusPending, model.StatusUp)
	eventually(t, func() bool { return len(h.rec.matching(model.EventLatencyRecovered, "telegram")) == 1 })
}

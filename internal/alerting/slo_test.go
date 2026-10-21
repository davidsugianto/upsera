package alerting

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/notify"
)

func TestSLOBurnSendsOneMessageAndNoAlert(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.e.SLOBurn(1, "fast", 99.9, 1000, 1.25, h.clock)
	h.e.drain()
	eventually(t, func() bool { return len(h.rec.matching(model.EventSLOBurn, "")) == 1 })
	h.advance(time.Hour) // no escalation to step 1 (smtp)
	quiet()
	got := h.rec.all()
	if len(got) != 1 || got[0].Channel != "telegram" {
		t.Fatalf("sends = %+v, want one slo_burn to telegram", got)
	}
	ev := got[0].Ev
	if ev.SLOLevel != "fast" || ev.SLOTarget != 99.9 || ev.BurnRate != 1000 || ev.BudgetConsumed != 1.25 ||
		ev.MonitorName != "api" || ev.Ackable || ev.AlertID != "" {
		t.Fatalf("event = %+v", ev)
	}
	if a := h.alert(1); a != nil {
		t.Fatalf("SLO burn opened an alert: %+v", a)
	}

	h.e.SLOBurnRecovered(1, "fast", 99.9, 0.5, 1.3, h.clock.Add(time.Minute))
	h.e.drain()
	eventually(t, func() bool { return len(h.rec.matching(model.EventSLOBurnRecovered, "telegram")) == 1 })
	quiet()
	if n := len(h.rec.all()); n != 2 {
		t.Fatalf("%d sends, want 2", n)
	}
}

func TestSLOBurnNotSuppressedByDownParent(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "parent", nil), monitor(2, "child", new(int64(1)))}, nil, nil)
	h.transition(1, model.StatusUp, model.StatusDown)
	h.e.SLOBurn(2, "slow", 99, 7, 0.4, h.clock)
	h.e.drain()
	eventually(t, func() bool { return len(h.rec.matching(model.EventSLOBurn, "")) == 1 })
}

func TestSLOBurnIgnoresUnknownAndPausedMonitors(t *testing.T) {
	m := monitor(2, "paused", nil)
	m.Paused = true
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil), m}, nil, nil)
	h.e.SLOBurn(99, "fast", 99.9, 1000, 1, h.clock)
	h.e.SLOBurn(2, "fast", 99.9, 1000, 1, h.clock)
	h.e.drain()
	quiet()
	if got := h.rec.all(); len(got) != 0 {
		t.Fatalf("sends = %+v, want none", got)
	}
}

func TestSLOBurnDedupeOnReplay(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	var keys []string
	h.e.disp.Close(context.Background())
	h.e.disp = notify.NewDispatcher(map[model.ChannelType]notify.Notifier{
		model.ChannelTelegram: fakeNotifier{"telegram", h.rec},
		model.ChannelSMTP:     fakeNotifier{"smtp", h.rec},
	}, func(at notify.Attempt) { h.e.outMu.Lock(); keys = append(keys, at.Job.DedupeKey); h.e.outMu.Unlock() },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	at := time.Unix(1700000000, 0)
	h.e.SLOBurn(1, "fast", 99.9, 1000, 1, at)
	h.e.SLOBurn(1, "fast", 99.9, 1000, 1, at) // replayed
	h.e.SLOBurn(1, "slow", 99.9, 50, 1, at)   // the other level is its own message
	h.e.SLOBurnRecovered(1, "fast", 99.9, 0, 1, at.Add(time.Minute))
	h.e.drain()
	h.e.disp.Close(context.Background())
	h.e.outMu.Lock()
	defer h.e.outMu.Unlock()
	want := []string{
		"slo:1:fast:fire:1700000000",
		"slo:1:slow:fire:1700000000",
		fmt.Sprintf("slo:1:fast:clear:%d", at.Add(time.Minute).Unix()),
	}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys = %v, want %v", keys, want)
		}
	}
}

func TestSLOBurnDoesNotBlock(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.e.mu.Lock()
	done := make(chan struct{})
	go func() {
		h.e.SLOBurn(1, "fast", 99.9, 1000, 1, time.Now())
		h.e.SLOBurnRecovered(1, "fast", 99.9, 0, 1, time.Now())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		h.e.mu.Unlock()
		t.Fatal("SLOBurn blocked on the engine lock")
	}
	h.e.mu.Unlock()
	h.e.drain()
	eventually(t, func() bool { return len(h.rec.all()) == 2 })
}

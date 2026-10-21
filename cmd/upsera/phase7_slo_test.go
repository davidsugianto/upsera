package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/config"
	"github.com/davidsugianto/upsera/internal/slo"
	"github.com/davidsugianto/upsera/internal/testutil"
)

// slWindows scale the burn-rate windows down to seconds. The slow windows
// stay long, so only the fast alert can clear within the test.
var slWindows = slo.Windows{FastLong: 8 * time.Second, FastShort: 3 * time.Second, SlowLong: time.Hour, SlowShort: 30 * time.Minute}

// slPushFor reports status to pushURL every 150ms for d, in the background;
// the returned func waits for it. Errors are ignored (the server may be
// stopping when the test ends).
func slPushFor(t *testing.T, pushURL, status string, d time.Duration) (wait func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for end := time.Now().Add(d); time.Now().Before(end); {
			if resp, err := http.Get(pushURL + "?status=" + status); err == nil {
				resp.Body.Close()
			}
			time.Sleep(150 * time.Millisecond)
		}
	}()
	t.Cleanup(func() { <-done })
	return func() { <-done }
}

// TestPhase7SLODoneWhen: "a monitor with a 99.9% SLO whose target is down
// fires ONE fast burn-rate notification, the SLO shows it firing with more
// than its budget consumed, it recovers once the short window is clean, and
// the month's SLA report says the SLO was missed".
func TestPhase7SLODoneWhen(t *testing.T) {
	dbURL := testutil.PostgresURL(t)
	tg, tgSrv := newFakeTelegram(t)

	prev := tweakSLOOptions
	tweakSLOOptions = func(o *slo.Options) {
		o.Every = 200 * time.Millisecond
		o.Windows = slWindows
	}
	t.Cleanup(func() { tweakSLOOptions = prev })

	base, _ := startServer(t, dbURL, func(c *config.Config) { c.TelegramAPIURL = tgSrv.URL })
	teamURL, bearer := setupAdmin(t, base)
	call(t, http.DefaultClient, "POST", teamURL+"/channels", bearer, map[string]any{
		"type": "telegram", "name": "tg", "is_default": true,
		"config": map[string]any{"bot_token": "123456:" + strings.Repeat("A", 35), "chat_id": "4242"},
	}, http.StatusCreated, nil)
	var mon struct {
		ID      int64  `json:"id"`
		PushURL string `json:"push_url"`
	}
	call(t, http.DefaultClient, "POST", teamURL+"/monitors", bearer, map[string]any{
		"name": "sl", "type": "push", "interval_s": 3600, "retries": 0, "config": map[string]any{},
	}, http.StatusCreated, &mon)
	sloURL := fmt.Sprintf("%s/monitors/%d/slo", teamURL, mon.ID)
	call(t, http.DefaultClient, "PUT", sloURL, bearer, map[string]any{"target": 99.9}, http.StatusOK, nil)

	slPushFor(t, mon.PushURL, "up", time.Second)()
	// The target goes down.
	downDone := slPushFor(t, mon.PushURL, "down", 5*time.Second)
	waitFor(t, 30*time.Second, func() bool { return len(tg.withPrefix("[SLO BURN] sl (fast)")) == 1 })
	text := tg.withPrefix("[SLO BURN] sl (fast)")[0].Text
	if !strings.Contains(text, "99.9%") || !strings.Contains(text, "Fast burn") {
		t.Fatalf("burn message = %q", text)
	}

	type status struct {
		Status struct {
			Firing         string   `json:"firing"`
			BudgetConsumed *float64 `json:"budget_consumed"`
		} `json:"status"`
	}
	var got status
	call(t, http.DefaultClient, "GET", sloURL, bearer, nil, http.StatusOK, &got)
	if got.Status.Firing != "fast" || got.Status.BudgetConsumed == nil || *got.Status.BudgetConsumed <= 1 {
		t.Fatalf("SLO status = %+v (consumed %v), want fast with budget_consumed > 1", got.Status, got.Status.BudgetConsumed)
	}
	downDone()
	time.Sleep(time.Second) // the down pushes ended; still one fast message
	if n := len(tg.withPrefix("[SLO BURN] sl (fast)")); n != 1 {
		t.Fatalf("%d fast burn messages, want 1", n)
	}

	// The target recovers: once the short window holds only good checks the
	// fast alert clears.
	slPushFor(t, mon.PushURL, "up", 8*time.Second)
	waitFor(t, 30*time.Second, func() bool { return len(tg.withPrefix("[SLO RECOVERED] sl (fast)")) == 1 })
	call(t, http.DefaultClient, "GET", sloURL, bearer, nil, http.StatusOK, &got)
	if got.Status.Firing == "fast" {
		t.Fatalf("still firing fast after recovery: %+v", got.Status)
	}
	time.Sleep(time.Second)
	if a, b := len(tg.withPrefix("[SLO BURN] sl (fast)")), len(tg.withPrefix("[SLO RECOVERED] sl (fast)")); a != 1 || b != 1 {
		t.Fatalf("%d fast burn and %d fast recovered messages, want one each", a, b)
	}

	var report struct {
		Partial bool `json:"partial"`
		Rows    []struct {
			MonitorID int64   `json:"monitor_id"`
			SLOMet    *bool   `json:"slo_met"`
			UptimePct float64 `json:"uptime_pct"`
		} `json:"rows"`
	}
	call(t, http.DefaultClient, "GET", teamURL+"/reports/sla", bearer, nil, http.StatusOK, &report)
	if !report.Partial || len(report.Rows) != 1 || report.Rows[0].MonitorID != mon.ID || report.Rows[0].SLOMet == nil ||
		*report.Rows[0].SLOMet {
		t.Fatalf("SLA report = %+v", report)
	}
}

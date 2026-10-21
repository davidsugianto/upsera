package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/config"
	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/scheduler"
	"github.com/davidsugianto/upsera/internal/store"
	"github.com/davidsugianto/upsera/internal/testutil"
)

// dgLatencyWindow shortens the 15-minute latency window so the test sees
// both edges within minutes: at the minimum 20s interval it holds the last
// three checks.
const dgLatencyWindow = 50 * time.Second

// dgSlowTarget answers "ok", after delay when slow is set.
func dgSlowTarget(t *testing.T, slow *atomic.Bool, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slow.Load() {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestPhase7DegradedDoneWhen: "a target that answers far slower than its
// 7-day baseline turns the monitor DEGRADED with one notification and no
// alert; when it is fast again, one latency-recovered message and UP".
func TestPhase7DegradedDoneWhen(t *testing.T) {
	if testing.Short() {
		t.Skip("takes about two minutes")
	}
	dbURL := testutil.PostgresURL(t)
	tg, tgSrv := newFakeTelegram(t)

	prev := tweakSchedulerOptions
	tweakSchedulerOptions = func(o *scheduler.Options) {
		o.BaselineRefresh = 300 * time.Millisecond
		o.LatencyWindow = dgLatencyWindow
	}
	t.Cleanup(func() { tweakSchedulerOptions = prev })

	var slow atomic.Bool
	target := dgSlowTarget(t, &slow, 700*time.Millisecond)

	base, _ := startServer(t, dbURL, func(c *config.Config) { c.TelegramAPIURL = tgSrv.URL })
	teamURL, bearer := setupAdmin(t, base)
	call(t, http.DefaultClient, "POST", teamURL+"/channels", bearer, map[string]any{
		"type": "telegram", "name": "tg", "is_default": true,
		"config": map[string]any{"bot_token": "123456:" + strings.Repeat("A", 35), "chat_id": "4242"},
	}, http.StatusCreated, nil)
	var mon struct {
		ID int64 `json:"id"`
	}
	call(t, http.DefaultClient, "POST", teamURL+"/monitors", bearer, map[string]any{
		"name": "dg", "type": "http", "interval_s": 20, "retry_interval_s": 20, "timeout_s": 5, "retries": 0,
		"config": map[string]any{"url": target.URL},
	}, http.StatusCreated, &mon)

	// Seven days of ~50 ms history from the built-in checker: the baseline.
	st, err := store.Open(context.Background(), dbURL, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	probeID, err := st.LocalProbeID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var hbs []model.Heartbeat
	for i := range 300 {
		hbs = append(hbs, model.Heartbeat{MonitorID: mon.ID, ProbeID: probeID,
			Time: now.Add(-time.Duration(i+1) * 33 * time.Minute), Status: model.StatusUp, LatencyMs: int32(45 + i%10)})
	}
	if err := st.InsertHeartbeats(context.Background(), hbs); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		b, err := st.LatencyBaselines(context.Background(), now.Add(-7*24*time.Hour))
		return err == nil && b[model.BaselineKey{MonitorID: mon.ID, ProbeID: probeID}].Samples >= 300
	})

	state := func() string {
		var m struct {
			State *struct {
				Status string `json:"status"`
			} `json:"state"`
		}
		call(t, http.DefaultClient, "GET", fmt.Sprintf("%s/monitors/%d", teamURL, mon.ID), bearer, nil, http.StatusOK, &m)
		if m.State == nil {
			return ""
		}
		return m.State.Status
	}
	waitFor(t, 30*time.Second, func() bool { return state() == "up" })
	time.Sleep(time.Second) // let the baseline loop pick up the history

	slow.Store(true)
	waitFor(t, 75*time.Second, func() bool { return len(tg.withPrefix("[DEGRADED] dg")) == 1 })
	if got := state(); got != "degraded" {
		t.Fatalf("state = %q, want degraded", got)
	}
	text := tg.withPrefix("[DEGRADED] dg")[0].Text
	if !strings.Contains(text, `"local"`) || !strings.Contains(text, "baseline of 5") {
		t.Fatalf("degraded message = %q", text)
	}
	if n := len(tg.withPrefix("[DOWN]")); n != 0 {
		t.Fatalf("DEGRADED fired %d DOWN alerts", n)
	}
	var alerts struct {
		Alerts []struct{} `json:"alerts"`
	}
	call(t, http.DefaultClient, "GET", teamURL+"/alerts", bearer, nil, http.StatusOK, &alerts)
	if len(alerts.Alerts) != 0 {
		t.Fatalf("DEGRADED opened %d alerts", len(alerts.Alerts))
	}

	slow.Store(false)
	waitFor(t, 100*time.Second, func() bool { return len(tg.withPrefix("[LATENCY OK] dg")) == 1 })
	if got := state(); got != "up" {
		t.Fatalf("state = %q, want up", got)
	}
	time.Sleep(time.Second)
	if n, m := len(tg.withPrefix("[DEGRADED] dg")), len(tg.withPrefix("[LATENCY OK] dg")); n != 1 || m != 1 {
		t.Fatalf("got %d degraded and %d latency-recovered messages, want one each", n, m)
	}
}

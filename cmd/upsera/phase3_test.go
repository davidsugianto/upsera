package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/testutil"
)

type sseEvent struct {
	name string
	data string
}

// TestPhase3LiveDashboard exercises the dashboard backend end to end: the
// live event stream carries transitions and alert changes, the overview and
// event history endpoints reflect stored heartbeats, the SPA is served for
// client routes, and open streams do not hold up shutdown.
func TestPhase3LiveDashboard(t *testing.T) {
	base, stop := startServer(t, testutil.PostgresURL(t), nil)
	teamURL, bearer := setupAdmin(t, base)

	var mon struct {
		ID      int64  `json:"id"`
		PushURL string `json:"push_url"`
	}
	call(t, http.DefaultClient, "POST", teamURL+"/monitors", bearer, map[string]any{
		"name": "cron", "type": "push", "interval_s": 3600, "retries": 0, "config": map[string]any{},
	}, http.StatusCreated, &mon)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", teamURL+"/events", nil)
	req.Header.Set("Authorization", bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	stream := make(chan sseEvent, 64)
	go func() {
		defer close(stream)
		sc := bufio.NewScanner(resp.Body)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.data = strings.TrimPrefix(line, "data: ")
			case line == "" && ev.name != "":
				stream <- ev
				ev = sseEvent{}
			}
		}
	}()
	// next returns the next event named name, skipping others.
	next := func(name string) string {
		t.Helper()
		timeout := time.After(10 * time.Second)
		for {
			select {
			case ev, ok := <-stream:
				if !ok {
					t.Fatalf("stream ended waiting for %q", name)
				}
				if ev.name == name {
					return ev.data
				}
			case <-timeout:
				t.Fatalf("no %q event within 10s", name)
			}
		}
	}
	type alertEvent struct {
		MonitorID  int64      `json:"monitor_id"`
		ResolvedAt *time.Time `json:"resolved_at"`
	}
	expect := func(to string, resolved bool) {
		t.Helper()
		if data := next("monitor"); !strings.Contains(data, `"to":"`+to+`"`) ||
			!strings.Contains(data, fmt.Sprintf(`"monitor_id":%d`, mon.ID)) {
			t.Fatalf("monitor event %s, want to=%s for monitor %d", data, to, mon.ID)
		}
		var a alertEvent
		data := next("alert")
		if err := json.Unmarshal([]byte(data), &a); err != nil {
			t.Fatal(err)
		}
		if a.MonitorID != mon.ID || (a.ResolvedAt != nil) != resolved || !strings.Contains(data, `"resolved_at":`) {
			t.Fatalf("alert event %s, want monitor %d resolved=%v", data, mon.ID, resolved)
		}
	}

	call(t, http.DefaultClient, "GET", mon.PushURL+"?status=down&msg=boom", "", nil, http.StatusOK, nil)
	expect("down", false)
	call(t, http.DefaultClient, "GET", mon.PushURL+"?status=up", "", nil, http.StatusOK, nil)
	expect("up", true)

	type overview struct {
		Monitors []struct {
			MonitorID  int64    `json:"monitor_id"`
			Uptime24h  *float64 `json:"uptime_24h"`
			Heartbeats []struct {
				Status string `json:"status"`
			} `json:"heartbeats"`
		} `json:"monitors"`
	}
	var ov overview
	waitFor(t, 10*time.Second, func() bool {
		call(t, http.DefaultClient, "GET", teamURL+"/monitor-overview", bearer, nil, http.StatusOK, &ov)
		return len(ov.Monitors) == 1 && len(ov.Monitors[0].Heartbeats) == 2
	})
	if m := ov.Monitors[0]; m.MonitorID != mon.ID || m.Heartbeats[0].Status != "down" ||
		m.Uptime24h == nil || *m.Uptime24h != 50 {
		t.Fatalf("overview = %+v", ov)
	}

	var hist struct {
		Events []struct {
			Status         string  `json:"status"`
			PreviousStatus *string `json:"previous_status"`
		} `json:"events"`
	}
	call(t, http.DefaultClient, "GET", fmt.Sprintf("%s/monitors/%d/events", teamURL, mon.ID), bearer, nil, http.StatusOK, &hist)
	if e := hist.Events; len(e) != 2 || e[0].Status != "up" || e[0].PreviousStatus == nil ||
		*e[0].PreviousStatus != "down" || e[1].Status != "down" || e[1].PreviousStatus != nil {
		t.Fatalf("status events = %+v", hist.Events)
	}

	spa, err := http.Get(base + "/t/1/monitors")
	if err != nil {
		t.Fatal(err)
	}
	spa.Body.Close()
	if spa.StatusCode != http.StatusOK || !strings.HasPrefix(spa.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("SPA route: %d %s", spa.StatusCode, spa.Header.Get("Content-Type"))
	}
	call(t, http.DefaultClient, "GET", base+"/api/nope", "", nil, http.StatusNotFound, nil)

	// The stream is still open: shutdown must close it rather than wait out
	// the HTTP server's shutdown deadline.
	start := time.Now()
	if err := stop(); err != nil {
		t.Fatalf("run returned %v", err)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("shutdown took %s with an open event stream", d)
	}
}

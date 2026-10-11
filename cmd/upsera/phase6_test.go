package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/checker"
	"github.com/davidsugianto/upsera/internal/config"
	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/probe"
	"github.com/davidsugianto/upsera/internal/testutil"
)

// regionChecker stands in for one region's network path to the target: the
// test "firewalls" the region by flipping blocked.
type regionChecker struct{ blocked atomic.Bool }

func (c *regionChecker) Check(_ context.Context, _ model.Monitor) checker.Result {
	if c.blocked.Load() {
		return checker.Result{Status: model.StatusDown, Latency: 5 * time.Millisecond, Message: "connection timed out"}
	}
	return checker.Result{Status: model.StatusUp, Latency: 12 * time.Millisecond, Message: "200 OK"}
}

// remoteProbe is a running upsera-probe agent inside the test process.
type remoteProbe struct {
	region string
	chk    *regionChecker
	stop   context.CancelFunc
	done   chan error
}

func (p *remoteProbe) shutdown(t *testing.T) {
	t.Helper()
	p.stop()
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		t.Fatalf("probe %s did not stop", p.region)
	}
}

// TestPhase6DoneWhen: "firewalling the target from one probe's region fires
// no alert, from two regions fires DOWN, and stopping one probe fires 'probe
// silent', not DOWN". A monitor is checked from three regions with quorum 2
// by three real probe agents registered against the real server.
func TestPhase6DoneWhen(t *testing.T) {
	if testing.Short() {
		t.Skip("takes about two minutes")
	}
	dbURL := testutil.PostgresURL(t)
	tg, tgSrv := newFakeTelegram(t)
	base, _ := startServer(t, dbURL, func(c *config.Config) {
		c.TelegramAPIURL = tgSrv.URL
		c.ProbeSilentAfter = 4 * time.Second
	})

	// Setup as the instance admin (probe management needs a session).
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}
	var me struct {
		Teams []struct {
			ID int64 `json:"id"`
		} `json:"teams"`
		CSRFToken string `json:"csrf_token"`
	}
	call(t, browser, "POST", base+"/api/setup", "", map[string]any{
		"email": "admin@example.com", "name": "Admin", "password": "correct horse battery", "team_name": "Ops",
	}, http.StatusCreated, &me)
	teamURL := fmt.Sprintf("%s/api/teams/%d", base, me.Teams[0].ID)
	var tok struct {
		Token string `json:"token"`
	}
	callCSRF(t, browser, "POST", teamURL+"/tokens", me.CSRFToken, map[string]any{"name": "ci", "scope": "write"}, &tok)
	bearer := "Bearer " + tok.Token

	call(t, http.DefaultClient, "POST", teamURL+"/channels", bearer, map[string]any{
		"type": "telegram", "name": "tg", "is_default": true,
		"config": map[string]any{"bot_token": "123456:" + strings.Repeat("A", 35), "chat_id": "4242"},
	}, http.StatusCreated, nil)

	// Three probes, one region each.
	startProbe := func(region string) *remoteProbe {
		var created struct {
			Token string `json:"registration_token"`
		}
		callCSRF(t, browser, "POST", base+"/api/admin/probes", me.CSRFToken,
			map[string]any{"name": region, "region": region}, &created)
		chk := &regionChecker{}
		agent, err := probe.New(probe.Config{
			ServerURL: base, Token: created.Token, DataDir: t.TempDir(), AllowInsecureHTTP: true,
			Version: "test", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			PollInterval: time.Second, FlushInterval: time.Second, Checker: chk,
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		p := &remoteProbe{region: region, chk: chk, stop: cancel, done: make(chan error, 1)}
		go func() { p.done <- agent.Run(ctx) }()
		t.Cleanup(cancel)
		return p
	}
	sg, fra, jkt := startProbe("sg-1"), startProbe("fra-1"), startProbe("jkt-1")

	var mon struct {
		ID int64 `json:"id"`
	}
	call(t, http.DefaultClient, "POST", teamURL+"/monitors", bearer, map[string]any{
		"name": "web", "type": "http", "interval_s": 20, "retry_interval_s": 20, "timeout_s": 5, "retries": 0,
		"config":  map[string]any{"url": "http://target.invalid/"},
		"regions": []string{"sg-1", "fra-1", "jkt-1"}, "quorum": 2,
	}, http.StatusCreated, &mon)

	type monState struct {
		State *struct {
			Status string `json:"status"`
		} `json:"state"`
		RegionStates map[string]struct {
			Status string `json:"status"`
		} `json:"region_states"`
	}
	get := func() (st monState) {
		call(t, http.DefaultClient, "GET", fmt.Sprintf("%s/monitors/%d", teamURL, mon.ID), bearer, nil, http.StatusOK, &st)
		return st
	}
	status := func() string {
		if s := get(); s.State != nil {
			return s.State.Status
		}
		return ""
	}
	regionStatus := func(region string) string { return get().RegionStates[region].Status }

	waitFor(t, 60*time.Second, func() bool {
		s := get()
		return s.State != nil && s.State.Status == "up" && len(s.RegionStates) == 3
	})

	// 1. One region cut off: its region goes down, the monitor stays UP and
	// nobody is alerted.
	sg.chk.blocked.Store(true)
	waitFor(t, 45*time.Second, func() bool { return regionStatus("sg-1") == "down" })
	time.Sleep(25 * time.Second) // more than a full interval of quiet
	if s := status(); s != "up" {
		t.Fatalf("1 of 3 regions down: monitor = %q, want up", s)
	}
	if n := len(tg.withPrefix("[DOWN]")); n != 0 {
		t.Fatalf("1 of 3 regions down fired %d DOWN alerts", n)
	}

	// 2. A second region cut off: quorum reached, DOWN fires.
	fra.chk.blocked.Store(true)
	waitFor(t, 60*time.Second, func() bool { return len(tg.withPrefix("[DOWN] web")) == 1 })
	if s := status(); s != "down" {
		t.Fatalf("2 of 3 regions down: monitor = %q, want down", s)
	}

	// Both paths open again: recovery.
	sg.chk.blocked.Store(false)
	fra.chk.blocked.Store(false)
	waitFor(t, 60*time.Second, func() bool { return len(tg.withPrefix("[UP] web")) == 1 })

	// 3. Stopping one probe fires "probe silent" for its region and the
	// monitor does not go DOWN: two regions still report.
	jkt.shutdown(t)
	waitFor(t, 30*time.Second, func() bool { return len(tg.withPrefix("[PROBE SILENT] jkt-1")) == 1 })
	time.Sleep(45 * time.Second) // jkt-1's results are now stale; the others carry the quorum
	if s := status(); s != "up" {
		t.Fatalf("1 probe silent: monitor = %q, want up", s)
	}

	// Fewer fresh regions than the quorum: PENDING (probe silent), never DOWN.
	fra.shutdown(t)
	waitFor(t, 30*time.Second, func() bool { return len(tg.withPrefix("[PROBE SILENT] fra-1")) == 1 })
	waitFor(t, 90*time.Second, func() bool { return status() == "pending" })
	if n := len(tg.withPrefix("[DOWN] web")); n != 1 {
		t.Fatalf("silent probes fired DOWN alerts: %d DOWN messages in total, want only the first", n)
	}
}

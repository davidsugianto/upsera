package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/store"
	"github.com/davidsugianto/upsera/internal/testutil"
)

// TestPhase1DoneWhen exercises the phase 1 acceptance criterion end to end
// through the real server: monitors created via the REST API store
// heartbeats, and buffered heartbeats are flushed on shutdown.
func TestPhase1DoneWhen(t *testing.T) {
	dbURL := testutil.PostgresURL(t)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "all systems ok")
	}))
	defer target.Close()

	base, stop := startServer(t, dbURL, nil)
	port := strings.TrimPrefix(base, "http://127.0.0.1:")
	if code := healthcheck(port); code != 0 {
		t.Fatalf("healthcheck subcommand exit code %d", code)
	}

	// First-run setup logs the admin in; use the session to mint a write
	// token, then drive monitors the way automation would.
	teamURL, bearer := setupAdmin(t, base)

	type monitor struct {
		ID      int64  `json:"id"`
		PushURL string `json:"push_url"`
	}
	var httpMon, pushMon monitor
	call(t, http.DefaultClient, "POST", teamURL+"/monitors", bearer, map[string]any{
		"name": "target", "type": "keyword", "interval_s": 20,
		"config": map[string]any{"url": target.URL, "keyword": "systems ok"},
	}, http.StatusCreated, &httpMon)
	call(t, http.DefaultClient, "POST", teamURL+"/monitors", bearer, map[string]any{
		"name": "nightly backup", "type": "push", "interval_s": 3600, "config": map[string]any{},
	}, http.StatusCreated, &pushMon)
	if !strings.HasPrefix(pushMon.PushURL, base+"/api/push/") {
		t.Fatalf("push_url = %q", pushMon.PushURL)
	}
	call(t, http.DefaultClient, "GET", pushMon.PushURL+"?status=up&msg=backup+done&ping=42", "", nil, http.StatusOK, nil)

	type heartbeats struct {
		Heartbeats []struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"heartbeats"`
	}
	// The created monitor checks immediately; heartbeats reach Postgres on
	// the next flush and are served back by the API.
	for _, id := range []int64{httpMon.ID, pushMon.ID} {
		var hb heartbeats
		waitFor(t, 30*time.Second, func() bool {
			call(t, http.DefaultClient, "GET", fmt.Sprintf("%s/monitors/%d/heartbeats", teamURL, id), bearer, nil, http.StatusOK, &hb)
			return len(hb.Heartbeats) > 0
		})
		if hb.Heartbeats[0].Status != "up" {
			t.Fatalf("monitor %d first heartbeat = %+v, want up", id, hb.Heartbeats[0])
		}
	}

	// A push right before shutdown must still be persisted by the final flush.
	call(t, http.DefaultClient, "POST", pushMon.PushURL+"?status=down&msg=disk+full", "", nil, http.StatusOK, nil)
	if err := stop(); err != nil {
		t.Fatalf("run returned %v", err)
	}

	st, err := store.Open(context.Background(), dbURL, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hbs, err := st.ListHeartbeats(context.Background(), pushMon.ID, time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	// Heartbeats store the resulting state: with the default retries (1)
	// a first failure is PENDING.
	if len(hbs) == 0 || hbs[0].Status != model.StatusPending || hbs[0].Message != "disk full" {
		t.Fatalf("last push not flushed on shutdown: %+v", hbs)
	}
}

func callCSRF(t *testing.T, c *http.Client, method, url, csrf string, body, out any) {
	t.Helper()
	doCall(t, c, method, url, map[string]string{"X-CSRF-Token": csrf}, body, http.StatusCreated, out)
}

func call(t *testing.T, c *http.Client, method, url, auth string, body any, want int, out any) {
	t.Helper()
	h := map[string]string{}
	if auth != "" {
		h["Authorization"] = auth
	}
	doCall(t, c, method, url, h, body, want, out)
}

func doCall(t *testing.T, c *http.Client, method, url string, headers map[string]string, body any, want int, out any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, url, resp.StatusCode, want, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: decode: %v: %s", method, url, err, raw)
		}
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s", timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

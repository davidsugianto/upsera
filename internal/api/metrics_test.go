package api_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/api"
	"github.com/davidsugianto/upsera/internal/config"
	"github.com/davidsugianto/upsera/internal/events"
	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
	"github.com/davidsugianto/upsera/internal/testutil"
)

func TestMetrics(t *testing.T) {
	ctx := context.Background()
	st, _ := testutil.Store(t)
	runner := newFakeRunner()
	srv := httptest.NewServer(api.NewRouter(api.Deps{
		Store: st, Runner: runner, Alerting: newFakeAlerting(), Maintenance: newFakeMaintenance(),
		Policy: netpolicy.New(""), Config: config.Config{BaseURL: "http://upsera.test", Port: 3080},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test", Events: events.NewHub(),
	}))
	t.Cleanup(srv.Close)
	base := srv.URL

	admin := newClient(t)
	resp, body := do(t, admin, http.MethodPost, base+"/api/setup", "", "", map[string]any{
		"email": "admin@example.com", "name": "Admin", "password": "correct horse battery", "team_name": "Ops",
	})
	requireStatus(t, resp, http.StatusCreated, body)
	var me meResponse
	decodeInto(t, body, &me)
	csrf, teamID := me.CSRFToken, me.Teams[0].ID

	resp, body = do(t, admin, http.MethodPost, base+"/api/teams", csrf, "", map[string]any{"name": "Other"})
	requireStatus(t, resp, http.StatusCreated, body)
	otherID := int64(body["id"].(float64))

	token := func(team int64, scope string) string {
		resp, body := do(t, admin, http.MethodPost, fmt.Sprintf("%s/api/teams/%d/tokens", base, team), csrf, "", map[string]any{
			"name": "prom " + scope, "scope": scope,
		})
		requireStatus(t, resp, http.StatusCreated, body)
		return body["token"].(string)
	}
	monitor := func(team int64, name string, extra map[string]any) int64 {
		in := map[string]any{"name": name, "type": "tcp", "config": map[string]any{"host": "example.com", "port": 443}}
		for k, v := range extra {
			in[k] = v
		}
		resp, body := do(t, admin, http.MethodPost, fmt.Sprintf("%s/api/teams/%d/monitors", base, team), csrf, "", in)
		requireStatus(t, resp, http.StatusCreated, body)
		return int64(body["id"].(float64))
	}

	upID := monitor(teamID, `web "prod"`, map[string]any{"group_name": "edge"})
	downID := monitor(teamID, "db", nil)
	pausedID := monitor(teamID, "paused one", map[string]any{"paused": true})
	_ = pausedID
	otherMonitor := monitor(otherID, "other team secret", nil)
	_ = otherMonitor

	expires := time.Now().Add(48 * time.Hour)
	runner.states[upID] = model.MonitorState{MonitorID: upID, Status: model.StatusUp, LastCheckAt: time.Now(), TLSExpiresAt: &expires}
	runner.states[downID] = model.MonitorState{MonitorID: downID, Status: model.StatusDown, LastCheckAt: time.Now()}

	probe, err := st.LocalProbeID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertHeartbeats(ctx, []model.Heartbeat{
		{MonitorID: upID, ProbeID: probe, Time: time.Now().Add(-time.Minute), Status: model.StatusUp, LatencyMs: 999},
		{MonitorID: upID, ProbeID: probe, Time: time.Now(), Status: model.StatusUp, LatencyMs: 42},
	}); err != nil {
		t.Fatal(err)
	}

	get := func(bearer string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodGet, base+"/metrics", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	t.Run("requires a token", func(t *testing.T) {
		for _, bearer := range []string{"", "ups_bogus", "ups_" + strings.Repeat("A", 43)} {
			resp, _ := get(bearer)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("bearer %q: status = %d, want 401", bearer, resp.StatusCode)
			}
			if resp.Header.Get("WWW-Authenticate") == "" {
				t.Errorf("bearer %q: missing WWW-Authenticate", bearer)
			}
		}
	})

	t.Run("session cookie is not enough", func(t *testing.T) {
		resp, err := admin.Get(base + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})

	for _, scope := range []string{"read", "write"} {
		t.Run(scope+" token exports its own team only", func(t *testing.T) {
			resp, out := get(token(teamID, scope))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d: %s", resp.StatusCode, out)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
				t.Errorf("content-type = %q", ct)
			}
			if resp.Header.Get("Cache-Control") != "no-store" {
				t.Errorf("cache-control = %q, want no-store", resp.Header.Get("Cache-Control"))
			}
			for _, want := range []string{
				"# TYPE upsera_monitor_up gauge",
				fmt.Sprintf(`upsera_monitor_up{monitor_id="%d",monitor="web \"prod\"",type="tcp",group="edge"} 1`, upID),
				fmt.Sprintf(`upsera_monitor_up{monitor_id="%d",monitor="db",type="tcp",group=""} 0`, downID),
				fmt.Sprintf(`upsera_monitor_status{monitor_id="%d",monitor="web \"prod\"",type="tcp",group="edge"} 1`, upID),
				fmt.Sprintf(`upsera_monitor_status{monitor_id="%d",monitor="db",type="tcp",group=""} 0`, downID),
				fmt.Sprintf(`upsera_monitor_response_ms{monitor_id="%d",monitor="web \"prod\"",type="tcp",group="edge"} 42`, upID),
				fmt.Sprintf(`upsera_monitor_paused{monitor_id="%d",monitor="paused one",type="tcp",group=""} 1`, pausedID),
				fmt.Sprintf(`upsera_monitor_paused{monitor_id="%d",monitor="db",type="tcp",group=""} 0`, downID),
				fmt.Sprintf(`upsera_monitor_cert_expiry_seconds{monitor_id="%d"`, upID),
			} {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q in:\n%s", want, out)
				}
			}
			if strings.Contains(out, "other team secret") {
				t.Errorf("leaked another team's monitor:\n%s", out)
			}
			// No state yet (paused, never checked): no up/status sample.
			if strings.Contains(out, fmt.Sprintf(`upsera_monitor_up{monitor_id="%d"`, pausedID)) {
				t.Errorf("paused monitor without state should have no up sample:\n%s", out)
			}
			// Cert expiry only where known.
			if strings.Contains(out, fmt.Sprintf(`upsera_monitor_cert_expiry_seconds{monitor_id="%d"`, downID)) {
				t.Errorf("cert expiry for monitor without TLS:\n%s", out)
			}
		})
	}

	t.Run("other team token sees only its monitors", func(t *testing.T) {
		resp, out := get(token(otherID, "read"))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if !strings.Contains(out, "other team secret") || strings.Contains(out, `monitor="db"`) {
			t.Errorf("wrong team scoping:\n%s", out)
		}
	})

	t.Run("method not allowed", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, base+"/metrics", nil)
		req.Header.Set("Authorization", "Bearer "+token(teamID, "read"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", resp.StatusCode)
		}
	})
}

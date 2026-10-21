package api_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/api"
	"github.com/davidsugianto/upsera/internal/config"
	"github.com/davidsugianto/upsera/internal/events"
	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
	"github.com/davidsugianto/upsera/internal/store"
	"github.com/davidsugianto/upsera/internal/testutil"
)

// sloEnv is a server with an admin (team owner), a viewer, read and write
// tokens, and a second team.
type sloEnv struct {
	t                 *testing.T
	st                *store.Store
	base              string
	admin, viewer     *http.Client
	csrf, viewerCSRF  string
	teamID, otherID   int64
	readTok, writeTok string
	loc               *time.Location
}

func newSLOEnv(t *testing.T) *sloEnv {
	t.Helper()
	st, _ := testutil.Store(t)
	const tz = "Asia/Jakarta"
	srv := httptest.NewServer(api.NewRouter(api.Deps{
		Store: st, Runner: newFakeRunner(), Alerting: newFakeAlerting(), Maintenance: newFakeMaintenance(),
		Policy: netpolicy.New(""), Config: config.Config{BaseURL: "http://upsera.test", Port: 3080, TimeZone: tz},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test", Events: events.NewHub(),
	}))
	t.Cleanup(srv.Close)
	loc, _ := time.LoadLocation(tz)
	e := &sloEnv{t: t, st: st, base: srv.URL, admin: newClient(t), viewer: newClient(t), loc: loc}

	resp, body := do(t, e.admin, http.MethodPost, e.base+"/api/setup", "", "", map[string]any{
		"email": "admin@example.com", "name": "Admin", "password": "correct horse battery", "team_name": "Ops",
	})
	requireStatus(t, resp, http.StatusCreated, body)
	var me meResponse
	decodeInto(t, body, &me)
	e.csrf, e.teamID = me.CSRFToken, me.Teams[0].ID
	resp, body = do(t, e.admin, http.MethodPost, e.base+"/api/teams", e.csrf, "", map[string]any{"name": "Other"})
	requireStatus(t, resp, http.StatusCreated, body)
	e.otherID = int64(body["id"].(float64))

	resp, body = do(t, e.admin, http.MethodPost, e.base+"/api/admin/users", e.csrf, "", map[string]any{
		"email": "viewer@example.com", "name": "Viewer", "password": "viewer password", "is_admin": false,
	})
	requireStatus(t, resp, http.StatusCreated, body)
	resp, body = do(t, e.admin, http.MethodPost, fmt.Sprintf("%s/api/teams/%d/members", e.base, e.teamID), e.csrf, "",
		map[string]any{"email": "viewer@example.com", "role": "viewer"})
	requireStatus(t, resp, http.StatusCreated, body)
	resp, body = do(t, e.viewer, http.MethodPost, e.base+"/api/auth/login", "", "", map[string]any{
		"email": "viewer@example.com", "password": "viewer password",
	})
	requireStatus(t, resp, http.StatusOK, body)
	var vme meResponse
	decodeInto(t, body, &vme)
	e.viewerCSRF = vme.CSRFToken

	token := func(scope string) string {
		resp, body := do(t, e.admin, http.MethodPost, fmt.Sprintf("%s/api/teams/%d/tokens", e.base, e.teamID), e.csrf, "",
			map[string]any{"name": "t " + scope, "scope": scope})
		requireStatus(t, resp, http.StatusCreated, body)
		return body["token"].(string)
	}
	e.readTok, e.writeTok = token("read"), token("write")
	return e
}

func (e *sloEnv) monitor(team int64, name string, paused bool) int64 {
	e.t.Helper()
	resp, body := do(e.t, e.admin, http.MethodPost, fmt.Sprintf("%s/api/teams/%d/monitors", e.base, team), e.csrf, "", map[string]any{
		"name": name, "type": "tcp", "paused": paused, "config": map[string]any{"host": "example.com", "port": 443},
	})
	requireStatus(e.t, resp, http.StatusCreated, body)
	return int64(body["id"].(float64))
}

func (e *sloEnv) sloURL(team, monitor int64) string {
	return fmt.Sprintf("%s/api/teams/%d/monitors/%d/slo", e.base, team, monitor)
}

func (e *sloEnv) beats(monitor int64, statuses ...model.Status) {
	e.t.Helper()
	probe, err := e.st.LocalProbeID(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	now := time.Now()
	hbs := make([]model.Heartbeat, len(statuses))
	for i, s := range statuses {
		hbs[i] = model.Heartbeat{MonitorID: monitor, ProbeID: probe, Time: now.Add(-time.Duration(i+1) * time.Second),
			Status: s, LatencyMs: int32(10 * (i + 1))}
	}
	if err := e.st.InsertHeartbeats(context.Background(), hbs); err != nil {
		e.t.Fatal(err)
	}
}

// keysOf lists m's keys, without the "$schema" link huma adds to every
// response body.
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if k != "$schema" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func requireKeys(t *testing.T, what string, m map[string]any, want ...string) {
	t.Helper()
	sort.Strings(want)
	if got := keysOf(m); !slices.Equal(got, want) {
		t.Fatalf("%s keys = %v, want %v", what, got, want)
	}
}

func TestSLOAPI(t *testing.T) {
	e := newSLOEnv(t)
	mon := e.monitor(e.teamID, "api", false)
	url := e.sloURL(e.teamID, mon)

	t.Run("no SLO yet", func(t *testing.T) {
		resp, body := do(t, e.admin, http.MethodGet, url, "", "", nil)
		requireStatus(t, resp, http.StatusNotFound, body)
		resp, body = do(t, e.admin, http.MethodDelete, url, e.csrf, "", nil)
		requireStatus(t, resp, http.StatusNotFound, body)
	})

	t.Run("put with defaults and the contract shape", func(t *testing.T) {
		resp, body := do(t, e.admin, http.MethodPut, url, e.csrf, "", map[string]any{"target": 99.9})
		requireStatus(t, resp, http.StatusOK, body)
		requireKeys(t, "body", body, "slo", "status", "series")
		slo := body["slo"].(map[string]any)
		requireKeys(t, "slo", slo, "monitor_id", "target", "window_days", "fast_burn", "slow_burn")
		if slo["monitor_id"] != float64(mon) || slo["target"] != 99.9 || slo["window_days"] != float64(30) ||
			slo["fast_burn"] != 14.4 || slo["slow_burn"] != float64(6) {
			t.Fatalf("slo = %v", slo)
		}
		// No data: sli, budget and burn rates are absent, not null or 0.
		requireKeys(t, "status", body["status"].(map[string]any), "good", "valid", "firing")
		if body["status"].(map[string]any)["firing"] != "none" {
			t.Fatalf("status = %v", body["status"])
		}
		series := body["series"].([]any)
		if len(series) != 30 {
			t.Fatalf("series length = %d, want window_days (30)", len(series))
		}
		first, last := series[0].(map[string]any), series[29].(map[string]any)
		requireKeys(t, "point", last, "day", "budget_consumed")
		today := time.Now().In(e.loc).Format(time.DateOnly)
		if last["day"] != today || first["day"] != time.Now().In(e.loc).AddDate(0, 0, -29).Format(time.DateOnly) {
			t.Fatalf("series days %v .. %v, want 29 days ago .. %s", first["day"], last["day"], today)
		}
	})

	t.Run("status from data", func(t *testing.T) {
		up, down, maint, deg := model.StatusUp, model.StatusDown, model.StatusMaintenance, model.StatusDegraded
		e.beats(mon, up, up, deg, down, maint)
		resp, body := do(t, e.admin, http.MethodGet, url, "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		st := body["status"].(map[string]any)
		requireKeys(t, "status", st, "sli", "good", "valid", "budget_consumed", "burn_rate_1h", "burn_rate_6h", "firing")
		if st["good"] != float64(3) || st["valid"] != float64(4) || st["sli"] != 0.75 {
			t.Fatalf("status = %v", st)
		}
		if c := st["budget_consumed"].(float64); c < 249 || c > 251 {
			t.Fatalf("budget_consumed = %v, want 250", c)
		}
		last := body["series"].([]any)[29].(map[string]any)
		requireKeys(t, "point", last, "day", "sli", "budget_consumed")
	})

	t.Run("validation", func(t *testing.T) {
		for _, in := range []map[string]any{
			{},
			{"target": 0},
			{"target": 100},
			{"target": -1},
			{"target": 99.9999},
			{"target": 99.9, "window_days": 6},
			{"target": 99.9, "window_days": 91},
			{"target": 99.9, "fast_burn": 5, "slow_burn": 6},
			{"target": 99.9, "slow_burn": 0.5},
			{"target": 99.9, "fast_burn": 1001},
		} {
			resp, body := do(t, e.admin, http.MethodPut, url, e.csrf, "", in)
			if resp.StatusCode != http.StatusUnprocessableEntity && resp.StatusCode != http.StatusBadRequest {
				t.Errorf("PUT %v: status %d, want 422: %v", in, resp.StatusCode, body)
			}
		}
		resp, body := do(t, e.admin, http.MethodGet, url, "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		if body["slo"].(map[string]any)["target"] != 99.9 {
			t.Fatalf("a rejected PUT changed the SLO: %v", body["slo"])
		}
	})

	t.Run("changing the SLO clears the firing state", func(t *testing.T) {
		s, err := e.st.GetSLO(context.Background(), e.teamID, mon)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.st.SetSLOFiring(context.Background(), mon, s.UpdatedAt, new(time.Now()), new(time.Now())); err != nil {
			t.Fatal(err)
		}
		_, body := do(t, e.admin, http.MethodGet, url, "", "", nil)
		if body["status"].(map[string]any)["firing"] != "fast" {
			t.Fatalf("firing = %v, want fast (wins over slow)", body["status"])
		}
		_, body = do(t, e.admin, http.MethodPut, url, e.csrf, "", map[string]any{"target": 99.9})
		if body["status"].(map[string]any)["firing"] != "fast" {
			t.Fatalf("same values cleared the state: %v", body["status"])
		}
		resp, body := do(t, e.admin, http.MethodPut, url, e.csrf, "", map[string]any{
			"target": 99.5, "window_days": 7, "fast_burn": 20, "slow_burn": 2,
		})
		requireStatus(t, resp, http.StatusOK, body)
		slo := body["slo"].(map[string]any)
		if slo["target"] != 99.5 || slo["window_days"] != float64(7) || slo["fast_burn"] != float64(20) ||
			slo["slow_burn"] != float64(2) || body["status"].(map[string]any)["firing"] != "none" {
			t.Fatalf("after change: %v", body)
		}
		if n := len(body["series"].([]any)); n != 7 {
			t.Fatalf("series length = %d, want 7", n)
		}
	})

	t.Run("roles", func(t *testing.T) {
		resp, body := do(t, e.viewer, http.MethodGet, url, "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		resp, body = do(t, e.viewer, http.MethodPut, url, e.viewerCSRF, "", map[string]any{"target": 99})
		requireStatus(t, resp, http.StatusForbidden, body)
		resp, body = do(t, e.viewer, http.MethodDelete, url, e.viewerCSRF, "", nil)
		requireStatus(t, resp, http.StatusForbidden, body)
		resp, body = do(t, http.DefaultClient, http.MethodGet, url, "", e.readTok, nil)
		requireStatus(t, resp, http.StatusOK, body)
		resp, body = do(t, http.DefaultClient, http.MethodPut, url, "", e.readTok, map[string]any{"target": 99})
		requireStatus(t, resp, http.StatusForbidden, body)
		resp, body = do(t, http.DefaultClient, http.MethodGet, fmt.Sprintf("%s/api/teams/%d/slos", e.base, e.teamID), "", e.readTok, nil)
		requireStatus(t, resp, http.StatusOK, body)
		resp, body = do(t, http.DefaultClient, http.MethodPut, url, "", e.writeTok, map[string]any{"target": 99.5, "window_days": 7, "fast_burn": 20, "slow_burn": 2})
		requireStatus(t, resp, http.StatusOK, body)
	})

	t.Run("cross team and missing monitors are 404", func(t *testing.T) {
		foreign := e.monitor(e.otherID, "foreign", false)
		for _, u := range []string{e.sloURL(e.teamID, foreign), e.sloURL(e.teamID, 999999)} {
			resp, body := do(t, e.admin, http.MethodPut, u, e.csrf, "", map[string]any{"target": 99})
			requireStatus(t, resp, http.StatusNotFound, body)
			resp, body = do(t, e.admin, http.MethodGet, u, "", "", nil)
			requireStatus(t, resp, http.StatusNotFound, body)
			resp, body = do(t, e.admin, http.MethodDelete, u, e.csrf, "", nil)
			requireStatus(t, resp, http.StatusNotFound, body)
		}
		// A write token of the team cannot reach the other team at all.
		resp, body := do(t, http.DefaultClient, http.MethodPut, e.sloURL(e.otherID, foreign), "", e.writeTok, map[string]any{"target": 99})
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusForbidden {
			t.Fatalf("token on another team: %d %v", resp.StatusCode, body)
		}
	})

	t.Run("list", func(t *testing.T) {
		zeta := e.monitor(e.teamID, "zeta", false)
		alpha := e.monitor(e.teamID, "alpha", true) // paused: still listed
		for _, id := range []int64{zeta, alpha} {
			resp, body := do(t, e.admin, http.MethodPut, e.sloURL(e.teamID, id), e.csrf, "", map[string]any{"target": 99})
			requireStatus(t, resp, http.StatusOK, body)
		}
		resp, body := do(t, e.viewer, http.MethodGet, fmt.Sprintf("%s/api/teams/%d/slos", e.base, e.teamID), "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		requireKeys(t, "list body", body, "slos")
		items := body["slos"].([]any)
		var names []string
		for _, it := range items {
			m := it.(map[string]any)
			requireKeys(t, "item", m, "monitor_id", "monitor_name", "target", "window_days", "fast_burn", "slow_burn", "status")
			names = append(names, m["monitor_name"].(string))
		}
		if !slices.Equal(names, []string{"alpha", "api", "zeta"}) {
			t.Fatalf("names = %v, want sorted by monitor name", names)
		}
		if st := items[1].(map[string]any)["status"].(map[string]any); st["valid"] != float64(4) {
			t.Fatalf("api status in the list = %v", st)
		}
	})

	t.Run("delete and audit", func(t *testing.T) {
		resp, body := do(t, e.admin, http.MethodDelete, url, e.csrf, "", nil)
		requireStatus(t, resp, http.StatusNoContent, body)
		resp, body = do(t, e.admin, http.MethodDelete, url, e.csrf, "", nil)
		requireStatus(t, resp, http.StatusNotFound, body)
		resp, body = do(t, e.admin, http.MethodGet, url, "", "", nil)
		requireStatus(t, resp, http.StatusNotFound, body)

		resp, body = do(t, e.admin, http.MethodGet, fmt.Sprintf("%s/api/teams/%d/audit?limit=200", e.base, e.teamID), "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		var sets, dels int
		for _, it := range body["entries"].([]any) {
			m := it.(map[string]any)
			if m["target_id"] != float64(mon) {
				continue
			}
			switch m["action"] {
			case "slo.set":
				sets++
			case "slo.delete":
				dels++
			}
		}
		if sets < 3 || dels != 1 {
			t.Fatalf("audit: %d slo.set, %d slo.delete for the monitor", sets, dels)
		}
	})
}

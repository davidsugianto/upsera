package api_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/api"
	"github.com/davidsugianto/upsera/internal/config"
	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
	"github.com/davidsugianto/upsera/internal/scheduler"
	"github.com/davidsugianto/upsera/internal/testutil"
)

// fakeRunner is a minimal in-memory stand-in for the scheduler, matching
// api.Runner exactly.
type fakeRunner struct {
	mu          sync.Mutex
	monitors    map[int64]model.Monitor
	states      map[int64]model.MonitorState
	upsertCalls int
	removeCalls int
	health      scheduler.Health
	lastPushMsg string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{monitors: map[int64]model.Monitor{}, states: map[int64]model.MonitorState{}}
}

func (f *fakeRunner) Upsert(m model.Monitor) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.monitors[m.ID] = m
	f.upsertCalls++
}

func (f *fakeRunner) Remove(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.monitors, id)
	delete(f.states, id)
	f.removeCalls++
}

func (f *fakeRunner) Push(token string, status model.Status, msg string, latencyMs int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastPushMsg = msg
	for _, m := range f.monitors {
		if m.Type != model.TypePush || m.PushToken != token {
			continue
		}
		if m.Paused {
			return scheduler.ErrPaused
		}
		f.states[m.ID] = model.MonitorState{
			MonitorID: m.ID, Status: status, Since: time.Now(), LastCheckAt: time.Now(),
		}
		return nil
	}
	return scheduler.ErrUnknownPushToken
}

func (f *fakeRunner) lastMessage() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastPushMsg
}

func (f *fakeRunner) State(id int64) (model.MonitorState, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.states[id]
	return st, ok
}

func (f *fakeRunner) Health() scheduler.Health {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.health
}

func (f *fakeRunner) counts() (upserts, removes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.upsertCalls, f.removeCalls
}

// newTestServer starts one httptest server backed by a real Postgres
// container and a fake scheduler.
func newTestServer(t *testing.T) (base string, runner *fakeRunner, policy *netpolicy.Policy) {
	t.Helper()
	st, _ := testutil.Store(t)
	runner = newFakeRunner()
	runner.health = scheduler.Health{DBReachable: true, BufferedHeartbeats: 3, DroppedHeartbeats: 1}
	policy = netpolicy.New("")
	cfg := config.Config{BaseURL: "http://upsera.test", Port: 3080}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := api.NewRouter(api.Deps{Store: st, Runner: runner, Policy: policy, Config: cfg, Logger: logger, Version: "test"})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL, runner, policy
}

func newClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

// do sends a JSON request (body may be nil), returning the response and its
// decoded JSON body (nil map if the body is empty).
func do(t *testing.T, client *http.Client, method, url, csrf, bearer string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode body %q: %v", raw, err)
		}
	}
	return resp, out
}

func decodeInto(t *testing.T, m map[string]any, v any) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func requireStatus(t *testing.T, resp *http.Response, want int, body map[string]any) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status = %d, want %d, body = %v", resp.Request.Method, resp.Request.URL, resp.StatusCode, want, body)
	}
}

type meResponse struct {
	User struct {
		ID      int64  `json:"id"`
		Email   string `json:"email"`
		IsAdmin bool   `json:"is_admin"`
	} `json:"user"`
	Teams []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Role string `json:"role"`
	} `json:"teams"`
	CSRFToken string `json:"csrf_token"`
}

type monitorResponse struct {
	ID             int64           `json:"id"`
	TeamID         int64           `json:"team_id"`
	Name           string          `json:"name"`
	Type           string          `json:"type"`
	Config         json.RawMessage `json:"config"`
	IntervalS      int             `json:"interval_s"`
	RetryIntervalS int             `json:"retry_interval_s"`
	Retries        int             `json:"retries"`
	TimeoutS       int             `json:"timeout_s"`
	Paused         bool            `json:"paused"`
	PushURL        string          `json:"push_url"`
}

func TestAPI(t *testing.T) {
	base, runner, policy := newTestServer(t)
	admin := newClient(t)

	// --- setup ---
	resp, body := do(t, admin, http.MethodGet, base+"/api/setup", "", "", nil)
	requireStatus(t, resp, http.StatusOK, body)
	if body["needed"] != true {
		t.Fatalf("setup needed = %v, want true", body["needed"])
	}

	resp, body = do(t, admin, http.MethodPost, base+"/api/setup", "", "", map[string]any{
		"email": "admin@example.com", "name": "Admin", "password": "correct horse battery", "team_name": "Ops",
	})
	requireStatus(t, resp, http.StatusCreated, body)
	var me meResponse
	decodeInto(t, body, &me)
	if !me.User.IsAdmin || len(me.Teams) != 1 || me.CSRFToken == "" {
		t.Fatalf("unexpected setup response: %+v", me)
	}
	adminCSRF := me.CSRFToken
	teamID := me.Teams[0].ID

	resp, body = do(t, admin, http.MethodGet, base+"/api/setup", "", "", nil)
	requireStatus(t, resp, http.StatusOK, body)
	if body["needed"] != false {
		t.Fatalf("setup needed = %v, want false after setup", body["needed"])
	}

	resp, body = do(t, newClient(t), http.MethodPost, base+"/api/setup", "", "", map[string]any{
		"email": "second@example.com", "name": "Second", "password": "irrelevant password", "team_name": "X",
	})
	requireStatus(t, resp, http.StatusConflict, body)

	t.Run("login wrong password then rate limit", func(t *testing.T) {
		anon := newClient(t)
		for i := 0; i < 10; i++ {
			resp, body := do(t, anon, http.MethodPost, base+"/api/auth/login", "", "", map[string]any{
				"email": "admin@example.com", "password": "wrong password",
			})
			requireStatus(t, resp, http.StatusUnauthorized, body)
		}
		resp, body := do(t, anon, http.MethodPost, base+"/api/auth/login", "", "", map[string]any{
			"email": "admin@example.com", "password": "wrong password",
		})
		requireStatus(t, resp, http.StatusTooManyRequests, body)

		// A different email from the same IP is a different bucket.
		resp, body = do(t, anon, http.MethodPost, base+"/api/auth/login", "", "", map[string]any{
			"email": "unknown@example.com", "password": "whatever it is",
		})
		requireStatus(t, resp, http.StatusUnauthorized, body)

		// While blocked, even the correct password is refused (429, not 401).
		resp, body = do(t, anon, http.MethodPost, base+"/api/auth/login", "", "", map[string]any{
			"email": "admin@example.com", "password": "correct horse battery",
		})
		requireStatus(t, resp, http.StatusTooManyRequests, body)
	})

	t.Run("csrf required for cookie post", func(t *testing.T) {
		resp, body := do(t, admin, http.MethodPost, base+"/api/teams", "", "", map[string]any{"name": "No CSRF"})
		requireStatus(t, resp, http.StatusForbidden, body)

		resp, body = do(t, admin, http.MethodPost, base+"/api/teams", adminCSRF, "", map[string]any{"name": "Second Team"})
		requireStatus(t, resp, http.StatusCreated, body)
	})

	var secondTeamID int64
	resp, body = do(t, admin, http.MethodGet, base+"/api/teams", "", "", nil)
	requireStatus(t, resp, http.StatusOK, body)
	for _, tm := range body["teams"].([]any) {
		m := tm.(map[string]any)
		if m["name"] == "Second Team" {
			secondTeamID = int64(m["id"].(float64))
		}
	}
	if secondTeamID == 0 {
		t.Fatal("second team not found")
	}

	// A viewer and an outsider account, both created via the admin endpoint.
	resp, body = do(t, admin, http.MethodPost, base+"/api/admin/users", adminCSRF, "", map[string]any{
		"email": "viewer@example.com", "name": "Viewer", "password": "viewer password", "is_admin": false,
	})
	requireStatus(t, resp, http.StatusCreated, body)
	resp, body = do(t, admin, http.MethodPost, base+"/api/admin/users", adminCSRF, "", map[string]any{
		"email": "outsider@example.com", "name": "Outsider", "password": "outsider password", "is_admin": false,
	})
	requireStatus(t, resp, http.StatusCreated, body)

	resp, body = do(t, admin, http.MethodPost, fmt.Sprintf("%s/api/teams/%d/members", base, teamID), adminCSRF, "", map[string]any{
		"email": "viewer@example.com", "role": "viewer",
	})
	requireStatus(t, resp, http.StatusCreated, body)

	viewer := newClient(t)
	resp, body = do(t, viewer, http.MethodPost, base+"/api/auth/login", "", "", map[string]any{
		"email": "viewer@example.com", "password": "viewer password",
	})
	requireStatus(t, resp, http.StatusOK, body)
	var viewerMe meResponse
	decodeInto(t, body, &viewerMe)

	outsider := newClient(t)
	resp, body = do(t, outsider, http.MethodPost, base+"/api/auth/login", "", "", map[string]any{
		"email": "outsider@example.com", "password": "outsider password",
	})
	requireStatus(t, resp, http.StatusOK, body)

	monitorsURL := fmt.Sprintf("%s/api/teams/%d/monitors", base, teamID)

	t.Run("viewer cannot create", func(t *testing.T) {
		resp, body := do(t, viewer, http.MethodPost, monitorsURL, viewerMe.CSRFToken, "", map[string]any{
			"name": "nope", "type": "tcp", "config": map[string]any{"host": "example.com", "port": 443},
		})
		requireStatus(t, resp, http.StatusForbidden, body)
	})

	t.Run("non-member gets 404", func(t *testing.T) {
		resp, body := do(t, outsider, http.MethodGet, monitorsURL, "", "", nil)
		requireStatus(t, resp, http.StatusNotFound, body)
	})

	t.Run("invalid config is 422", func(t *testing.T) {
		resp, body := do(t, admin, http.MethodPost, monitorsURL, adminCSRF, "", map[string]any{
			"name": "bad tcp", "type": "tcp", "config": map[string]any{"host": "example.com", "port": 999999},
		})
		requireStatus(t, resp, http.StatusUnprocessableEntity, body)
	})

	t.Run("timeout must be less than interval", func(t *testing.T) {
		resp, body := do(t, admin, http.MethodPost, monitorsURL, adminCSRF, "", map[string]any{
			"name": "bad timeout", "type": "tcp", "config": map[string]any{"host": "example.com", "port": 443},
			"interval_s": 30, "timeout_s": 30,
		})
		requireStatus(t, resp, http.StatusUnprocessableEntity, body)
	})

	// --- monitor CRUD round trip, with Runner.Upsert/Remove and defaults ---
	upsertsBefore, _ := runner.counts()
	resp, body = do(t, admin, http.MethodPost, monitorsURL, adminCSRF, "", map[string]any{
		"name": "example tcp", "type": "tcp", "config": map[string]any{"host": "example.com", "port": 443},
	})
	requireStatus(t, resp, http.StatusCreated, body)
	var created monitorResponse
	decodeInto(t, body, &created)
	if created.IntervalS != model.DefaultIntervalS || created.RetryIntervalS != model.DefaultRetryS ||
		created.Retries != model.DefaultRetries || created.TimeoutS != model.DefaultTimeoutS {
		t.Fatalf("defaults not applied: %+v", created)
	}
	upsertsAfter, _ := runner.counts()
	if upsertsAfter != upsertsBefore+1 {
		t.Fatalf("Runner.Upsert not called on create: before=%d after=%d", upsertsBefore, upsertsAfter)
	}

	monitorURL := fmt.Sprintf("%s/%d", monitorsURL, created.ID)

	t.Run("type change on update is 422", func(t *testing.T) {
		resp, body := do(t, admin, http.MethodPut, monitorURL, adminCSRF, "", map[string]any{
			"name": "example tcp", "type": "ping", "config": map[string]any{"host": "example.com"},
		})
		requireStatus(t, resp, http.StatusUnprocessableEntity, body)
	})

	resp, body = do(t, admin, http.MethodPut, monitorURL, adminCSRF, "", map[string]any{
		"name": "example tcp renamed", "type": "tcp", "config": map[string]any{"host": "example.org", "port": 8443},
		"interval_s": 90,
	})
	requireStatus(t, resp, http.StatusOK, body)
	var updated monitorResponse
	decodeInto(t, body, &updated)
	if updated.Name != "example tcp renamed" || updated.IntervalS != 90 {
		t.Fatalf("update did not apply: %+v", updated)
	}

	resp, body = do(t, admin, http.MethodGet, monitorURL, "", "", nil)
	requireStatus(t, resp, http.StatusOK, body)

	removesBefore := runner.removeCalls
	resp, body = do(t, admin, http.MethodDelete, monitorURL, adminCSRF, "", nil)
	requireStatus(t, resp, http.StatusNoContent, body)
	if runner.removeCalls != removesBefore+1 {
		t.Fatalf("Runner.Remove not called on delete")
	}

	t.Run("audit entries recorded for monitor create", func(t *testing.T) {
		resp, body := do(t, admin, http.MethodGet, fmt.Sprintf("%s/api/teams/%d/audit", base, teamID), "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		entries, _ := body["entries"].([]any)
		found := false
		for _, e := range entries {
			m := e.(map[string]any)
			if m["action"] == "monitor.create" {
				found = true
			}
		}
		if !found {
			t.Fatalf("no monitor.create audit entry among %v", entries)
		}
	})

	t.Run("last owner removal is conflict", func(t *testing.T) {
		resp, body := do(t, admin, http.MethodDelete,
			fmt.Sprintf("%s/api/teams/%d/members/%d", base, teamID, me.User.ID), adminCSRF, "", nil)
		requireStatus(t, resp, http.StatusConflict, body)
	})

	t.Run("bearer tokens", func(t *testing.T) {
		resp, body := do(t, admin, http.MethodPost, fmt.Sprintf("%s/api/teams/%d/tokens", base, teamID), adminCSRF, "", map[string]any{
			"name": "reader", "scope": "read",
		})
		requireStatus(t, resp, http.StatusCreated, body)
		readToken, _ := body["token"].(string)
		if !strings.HasPrefix(readToken, "ups_") {
			t.Fatalf("unexpected token shape: %q", readToken)
		}

		resp, body = do(t, admin, http.MethodPost, fmt.Sprintf("%s/api/teams/%d/tokens", base, teamID), adminCSRF, "", map[string]any{
			"name": "writer", "scope": "write",
		})
		requireStatus(t, resp, http.StatusCreated, body)
		writeToken, _ := body["token"].(string)

		resp, body = do(t, admin, http.MethodPost, fmt.Sprintf("%s/api/teams/%d/tokens", base, secondTeamID), adminCSRF, "", map[string]any{
			"name": "other team", "scope": "read",
		})
		requireStatus(t, resp, http.StatusCreated, body)
		otherTeamToken, _ := body["token"].(string)

		anon := newClient(t)
		resp, body = do(t, anon, http.MethodGet, monitorsURL, "", readToken, nil)
		requireStatus(t, resp, http.StatusOK, body)

		resp, body = do(t, anon, http.MethodPost, monitorsURL, "", readToken, map[string]any{
			"name": "should fail", "type": "tcp", "config": map[string]any{"host": "example.com", "port": 443},
		})
		requireStatus(t, resp, http.StatusForbidden, body)

		resp, body = do(t, anon, http.MethodPost, monitorsURL, "", writeToken, map[string]any{
			"name": "via write token", "type": "tcp", "config": map[string]any{"host": "example.com", "port": 443},
		})
		requireStatus(t, resp, http.StatusCreated, body)

		resp, body = do(t, anon, http.MethodGet, monitorsURL, "", otherTeamToken, nil)
		requireStatus(t, resp, http.StatusNotFound, body)

		resp, body = do(t, anon, http.MethodGet, fmt.Sprintf("%s/api/teams/%d/tokens", base, teamID), "", readToken, nil)
		requireStatus(t, resp, http.StatusForbidden, body)
	})

	t.Run("push endpoint", func(t *testing.T) {
		resp, body := do(t, admin, http.MethodPost, monitorsURL, adminCSRF, "", map[string]any{
			"name": "push me", "type": "push", "config": map[string]any{},
		})
		requireStatus(t, resp, http.StatusCreated, body)
		var pushMon monitorResponse
		decodeInto(t, body, &pushMon)
		if pushMon.PushURL == "" {
			t.Fatalf("push monitor missing push_url: %+v", pushMon)
		}
		token := pushMon.PushURL[strings.LastIndex(pushMon.PushURL, "/")+1:]

		anon := newClient(t)
		resp, body = do(t, anon, http.MethodGet, fmt.Sprintf("%s/api/push/%s?status=up", base, token), "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		if body["ok"] != true {
			t.Fatalf("push response = %v", body)
		}

		resp, body = do(t, anon, http.MethodGet, base+"/api/push/does-not-exist", "", "", nil)
		requireStatus(t, resp, http.StatusNotFound, body)

		resp, body = do(t, admin, http.MethodPost, monitorsURL, adminCSRF, "", map[string]any{
			"name": "paused push", "type": "push", "config": map[string]any{}, "paused": true,
		})
		requireStatus(t, resp, http.StatusCreated, body)
		var pausedPush monitorResponse
		decodeInto(t, body, &pausedPush)
		pausedToken := pausedPush.PushURL[strings.LastIndex(pausedPush.PushURL, "/")+1:]

		resp, body = do(t, anon, http.MethodGet, fmt.Sprintf("%s/api/push/%s", base, pausedToken), "", "", nil)
		requireStatus(t, resp, http.StatusConflict, body)
	})

	t.Run("admin settings", func(t *testing.T) {
		resp, body := do(t, viewer, http.MethodPut, base+"/api/admin/settings", viewerMe.CSRFToken, "", map[string]any{
			"block_private_targets": true,
		})
		requireStatus(t, resp, http.StatusForbidden, body)

		resp, body = do(t, admin, http.MethodPut, base+"/api/admin/settings", adminCSRF, "", map[string]any{
			"block_private_targets": true, "retention_days": 30,
		})
		requireStatus(t, resp, http.StatusOK, body)
		if !policy.BlockPrivate() {
			t.Fatal("Policy.SetBlockPrivate was not applied")
		}
	})

	t.Run("openapi document", func(t *testing.T) {
		resp, err := http.Get(base + "/api/openapi.json")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var doc map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
			t.Fatalf("openapi.json is not valid JSON: %v", err)
		}
		paths, _ := doc["paths"].(map[string]any)
		if _, ok := paths["/api/teams/{teamID}/monitors"]; !ok {
			t.Fatalf("openapi paths missing monitors route: %v", pathKeys(paths))
		}
	})

	t.Run("healthz", func(t *testing.T) {
		resp, err := http.Get(base + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var h map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
			t.Fatal(err)
		}
		if h["db"] != "up" || h["status"] != "ok" {
			t.Fatalf("unexpected healthz body: %v", h)
		}
	})

	t.Run("logout clears the session", func(t *testing.T) {
		resp, body := do(t, admin, http.MethodGet, base+"/api/auth/me", "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)

		resp, body = do(t, admin, http.MethodPost, base+"/api/auth/logout", adminCSRF, "", nil)
		requireStatus(t, resp, http.StatusNoContent, body)

		resp, body = do(t, admin, http.MethodGet, base+"/api/auth/me", "", "", nil)
		requireStatus(t, resp, http.StatusUnauthorized, body)
	})
}

func pathKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestAuthMiddlewareDBOutage confirms that a store error other than
// store.ErrNotFound while resolving the caller (a DB outage, not a
// missing/expired credential) fails the request with 503 instead of
// silently treating the caller as unauthenticated.
func TestAuthMiddlewareDBOutage(t *testing.T) {
	st, _ := testutil.Store(t)
	runner := newFakeRunner()
	policy := netpolicy.New("")
	cfg := config.Config{BaseURL: "http://upsera.test", Port: 3080}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := api.NewRouter(api.Deps{Store: st, Runner: runner, Policy: policy, Config: cfg, Logger: logger, Version: "test"})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	base := srv.URL

	// Garbage-but-validly-encoded credentials: once the DB is down the
	// store lookup itself fails, regardless of whether the value maps to a
	// real session or token.
	sessionClient := newClient(t)
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	sessionClient.Jar.SetCookies(u, []*http.Cookie{{
		Name:  "upsera_session",
		Value: base64.RawURLEncoding.EncodeToString([]byte("garbage session value")),
	}})
	bearer := base64.RawURLEncoding.EncodeToString([]byte("garbage token value"))

	st.Close()

	t.Run("session cookie lookup", func(t *testing.T) {
		resp, body := do(t, sessionClient, http.MethodGet, base+"/api/auth/me", "", "", nil)
		requireStatus(t, resp, http.StatusServiceUnavailable, body)
	})

	t.Run("bearer token lookup", func(t *testing.T) {
		resp, body := do(t, newClient(t), http.MethodGet, base+"/api/auth/me", "", bearer, nil)
		requireStatus(t, resp, http.StatusServiceUnavailable, body)
	})
}

// TestPushSanitizesMessage confirms a push message containing a NUL byte
// and an invalid UTF-8 byte reaches the runner already sanitized, so it can
// never fail to persist as heartbeat text.
func TestPushSanitizesMessage(t *testing.T) {
	base, runner, _ := newTestServer(t)
	admin := newClient(t)

	resp, body := do(t, admin, http.MethodPost, base+"/api/setup", "", "", map[string]any{
		"email": "admin@example.com", "name": "Admin", "password": "correct horse battery", "team_name": "Ops",
	})
	requireStatus(t, resp, http.StatusCreated, body)
	var me meResponse
	decodeInto(t, body, &me)
	teamID := me.Teams[0].ID

	resp, body = do(t, admin, http.MethodPost, fmt.Sprintf("%s/api/teams/%d/monitors", base, teamID), me.CSRFToken, "", map[string]any{
		"name": "push me", "type": "push", "config": map[string]any{},
	})
	requireStatus(t, resp, http.StatusCreated, body)
	var mon monitorResponse
	decodeInto(t, body, &mon)
	if mon.PushURL == "" {
		t.Fatalf("push monitor missing push_url: %+v", mon)
	}
	token := mon.PushURL[strings.LastIndex(mon.PushURL, "/")+1:]

	rawMsg := "bad\x00nul\xffbyte"
	pushURL := fmt.Sprintf("%s/api/push/%s?status=up&msg=%s", base, token, url.QueryEscape(rawMsg))
	resp, body = do(t, newClient(t), http.MethodGet, pushURL, "", "", nil)
	requireStatus(t, resp, http.StatusOK, body)

	want := "badnul\uFFFDbyte"
	if got := runner.lastMessage(); got != want {
		t.Fatalf("runner message = %q, want %q", got, want)
	}
}

// TestOpenAPIEnums is a contract test for the review checklist item "the
// spec can't drift": model.Status, model.MonitorType, model.Role and
// model.TokenScope implement huma.SchemaProvider, and this asserts the
// generated spec actually reflects their enum values (huma could stop
// honoring SchemaProvider, or a field could lose its type, without this).
func TestOpenAPIEnums(t *testing.T) {
	base, _, _ := newTestServer(t)

	resp, err := http.Get(base + "/api/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var spec map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
		t.Fatal(err)
	}

	schemaEnum := func(name, field string) []string {
		t.Helper()
		schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
		sch, ok := schemas[name].(map[string]any)
		if !ok {
			t.Fatalf("no schema %q in spec", name)
		}
		prop, ok := sch["properties"].(map[string]any)[field].(map[string]any)
		if !ok {
			t.Fatalf("no property %q on schema %q", field, name)
		}
		if prop["type"] != "string" {
			t.Fatalf("%s.%s type = %v, want string", name, field, prop["type"])
		}
		rawEnum, ok := prop["enum"].([]any)
		if !ok {
			t.Fatalf("%s.%s has no enum in spec", name, field)
		}
		got := make([]string, len(rawEnum))
		for i, v := range rawEnum {
			got[i] = v.(string)
		}
		return got
	}

	wantTypes := make([]string, len(model.MonitorTypes))
	for i, mt := range model.MonitorTypes {
		wantTypes[i] = string(mt)
	}
	if got := schemaEnum("MonitorBody", "type"); !slicesEqual(got, wantTypes) {
		t.Fatalf("MonitorBody.type enum = %v, want %v", got, wantTypes)
	}

	wantStatuses := []string{
		model.StatusDown.String(), model.StatusUp.String(), model.StatusPending.String(), model.StatusMaintenance.String(),
	}
	if got := schemaEnum("MonitorStateBody", "status"); !slicesEqual(got, wantStatuses) {
		t.Fatalf("MonitorStateBody.status enum = %v, want %v", got, wantStatuses)
	}

	wantRoles := []string{string(model.RoleOwner), string(model.RoleEditor), string(model.RoleViewer)}
	if got := schemaEnum("AddMemberInputBody", "role"); !slicesEqual(got, wantRoles) {
		t.Fatalf("AddMemberInputBody.role enum = %v, want %v", got, wantRoles)
	}

	wantScopes := []string{string(model.ScopeRead), string(model.ScopeWrite)}
	if got := schemaEnum("CreateTokenInputBody", "scope"); !slicesEqual(got, wantScopes) {
		t.Fatalf("CreateTokenInputBody.scope enum = %v, want %v", got, wantScopes)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davidsugianto/upsera/internal/api"
	"github.com/davidsugianto/upsera/internal/config"
	"github.com/davidsugianto/upsera/internal/events"
	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
	"github.com/davidsugianto/upsera/internal/probeproto"
	"github.com/davidsugianto/upsera/internal/store"
	"github.com/davidsugianto/upsera/internal/testutil"
)

// fakeProbes implements api.ProbeRegistry with a hash map, like the real one.
type fakeProbes struct {
	mu     sync.Mutex
	byHash map[[32]byte]model.ProbeAuth
	online map[string]bool
	seen   map[int64]string
	seenN  map[int64]int
	adds   int
	rms    []int64
}

func newFakeProbes() *fakeProbes {
	return &fakeProbes{
		byHash: map[[32]byte]model.ProbeAuth{}, online: map[string]bool{},
		seen: map[int64]string{}, seenN: map[int64]int{},
	}
}

func (f *fakeProbes) Authenticate(c string) (model.ProbeAuth, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.byHash[sha256.Sum256([]byte(c))]
	return p, ok
}

func (f *fakeProbes) Seen(id int64, version string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seenN[id]++
	if version != "" {
		f.seen[id] = version
	}
}

func (f *fakeProbes) Online(region string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.online[region]
}

func (f *fakeProbes) Add(p model.ProbeAuth) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adds++
	var h [32]byte
	copy(h[:], p.TokenHash)
	f.byHash[h] = p
}

func (f *fakeProbes) Remove(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rms = append(f.rms, id)
	for h, p := range f.byHash {
		if p.ID == id {
			delete(f.byHash, h)
		}
	}
}

func (f *fakeProbes) seenCount(id int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seenN[id]
}

func (f *fakeProbes) addCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.adds
}

func (f *fakeProbes) rmCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rms)
}

func (f *fakeProbes) setOnline(region string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.online[region] = true
}

func (f *fakeProbes) version(id int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen[id]
}

type reportCall struct {
	probeID int64
	region  string
	rs      []model.RegionResult
}

// fakeSink implements api.ProbeSink.
type fakeSink struct {
	mu      sync.Mutex
	version string
	byReg   map[string][]model.Monitor
	reports []reportCall
	// assignmentsCalls counts Assignments calls (the heavy listing).
	assignmentsCalls int
}

func (f *fakeSink) Report(probeID int64, region string, rs []model.RegionResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, reportCall{probeID, region, rs})
}

func (f *fakeSink) Assignments(region string) (string, []model.Monitor) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assignmentsCalls++
	return f.version, f.byReg[region]
}

func (f *fakeSink) Assigned(region string, id int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.byReg[region] {
		if m.ID == id {
			return true
		}
	}
	return false
}

func (f *fakeSink) listings() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.assignmentsCalls
}

func (f *fakeSink) set(version string, byReg map[string][]model.Monitor) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.version, f.byReg = version, byReg
}

func (f *fakeSink) calls() []reportCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]reportCall(nil), f.reports...)
}

type probeEnv struct {
	base      string
	dbURL     string
	runner    *fakeRunner
	policy    *netpolicy.Policy
	probes    *fakeProbes
	sink      *fakeSink
	admin     *http.Client
	csrf      string
	teamID    int64
	editor    *http.Client
	editorCSR string
}

func newProbeEnv(t *testing.T) *probeEnv { return newProbeEnvWith(t, nil) }

func newProbeEnvWith(t *testing.T, tweak func(*config.Config)) *probeEnv {
	t.Helper()
	st, dbURL := testutil.Store(t)
	e := &probeEnv{
		dbURL: dbURL, runner: newFakeRunner(), policy: netpolicy.New(""),
		probes: newFakeProbes(), sink: &fakeSink{byReg: map[string][]model.Monitor{}},
	}
	e.runner.health.DBReachable = true
	cfg := config.Config{BaseURL: "http://upsera.test", Port: 3080}
	if tweak != nil {
		tweak(&cfg)
	}
	h := api.NewRouter(api.Deps{
		Store: st, Runner: e.runner, Alerting: newFakeAlerting(), Maintenance: newFakeMaintenance(),
		Policy: e.policy, Config: cfg,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test", Events: events.NewHub(),
		Probes: e.probes, ProbeSink: e.sink,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	e.base = srv.URL

	e.admin = newClient(t)
	resp, body := do(t, e.admin, http.MethodPost, e.base+"/api/setup", "", "", map[string]any{
		"email": "admin@example.com", "name": "Admin", "password": "correct horse battery", "team_name": "Ops",
	})
	requireStatus(t, resp, http.StatusCreated, body)
	var me meResponse
	decodeInto(t, body, &me)
	e.csrf, e.teamID = me.CSRFToken, me.Teams[0].ID

	resp, body = do(t, e.admin, http.MethodPost, e.base+"/api/admin/users", e.csrf, "", map[string]any{
		"email": "editor@example.com", "name": "Editor", "password": "editor password", "is_admin": false,
	})
	requireStatus(t, resp, http.StatusCreated, body)
	resp, body = do(t, e.admin, http.MethodPost, fmt.Sprintf("%s/api/teams/%d/members", e.base, e.teamID), e.csrf, "", map[string]any{
		"email": "editor@example.com", "role": "editor",
	})
	requireStatus(t, resp, http.StatusCreated, body)
	e.editor = newClient(t)
	resp, body = do(t, e.editor, http.MethodPost, e.base+"/api/auth/login", "", "", map[string]any{
		"email": "editor@example.com", "password": "editor password",
	})
	requireStatus(t, resp, http.StatusOK, body)
	var em meResponse
	decodeInto(t, body, &em)
	e.editorCSR = em.CSRFToken
	return e
}

// teamToken creates a team API token with the given scope.
func (e *probeEnv) teamToken(t *testing.T, scope string) string {
	t.Helper()
	resp, body := do(t, e.admin, http.MethodPost, fmt.Sprintf("%s/api/teams/%d/tokens", e.base, e.teamID), e.csrf, "", map[string]any{
		"name": "tok-" + scope, "scope": scope,
	})
	requireStatus(t, resp, http.StatusCreated, body)
	return body["token"].(string)
}

type createdProbe struct {
	ID    int64
	Token string
}

func (e *probeEnv) createProbe(t *testing.T, name, region string) createdProbe {
	t.Helper()
	resp, body := do(t, e.admin, http.MethodPost, e.base+"/api/admin/probes", e.csrf, "", map[string]any{"name": name, "region": region})
	requireStatus(t, resp, http.StatusCreated, body)
	p := body["probe"].(map[string]any)
	return createdProbe{ID: int64(p["id"].(float64)), Token: body["registration_token"].(string)}
}

func (e *probeEnv) register(t *testing.T, token, version string) (*http.Response, map[string]any) {
	t.Helper()
	return do(t, newClient(t), http.MethodPost, e.base+probeproto.PathRegister, "", "", probeproto.RegisterRequest{Token: token, Version: version})
}

// registered creates a probe and registers it, returning id and credential.
func (e *probeEnv) registered(t *testing.T, name, region string) (int64, string) {
	t.Helper()
	p := e.createProbe(t, name, region)
	resp, body := e.register(t, p.Token, "0.1.0")
	requireStatus(t, resp, http.StatusOK, body)
	return p.ID, body["credential"].(string)
}

func rawRequest(t *testing.T, method, url string, hdr map[string]string, body []byte) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func TestProbeAdmin(t *testing.T) {
	e := newProbeEnv(t)
	probesURL := e.base + "/api/admin/probes"
	readTok := e.teamToken(t, "read")
	writeTok := e.teamToken(t, "write")

	t.Run("admin only", func(t *testing.T) {
		for _, c := range []struct {
			method, url string
			body        any
		}{
			{http.MethodGet, probesURL, nil},
			{http.MethodPost, probesURL, map[string]any{"name": "x", "region": "x"}},
			{http.MethodPost, probesURL + "/2/registration-token", nil},
			{http.MethodDelete, probesURL + "/2", nil},
		} {
			resp, body := do(t, e.editor, c.method, c.url, e.editorCSR, "", c.body)
			requireStatus(t, resp, http.StatusForbidden, body)
			for _, tok := range []string{readTok, writeTok} {
				resp, body = do(t, newClient(t), c.method, c.url, "", tok, c.body)
				requireStatus(t, resp, http.StatusForbidden, body)
			}
			resp, body = do(t, newClient(t), c.method, c.url, "", "", c.body)
			requireStatus(t, resp, http.StatusUnauthorized, body)
		}
	})

	t.Run("create validation", func(t *testing.T) {
		for _, c := range []struct{ name, region string }{
			{"local", "sg-1"}, {"sg", "local"}, {"Upper", "sg-1"}, {"sg", "-bad"}, {"sg", "has space"},
			{"", "sg-1"}, {"sg", strings.Repeat("a", 41)},
		} {
			resp, body := do(t, e.admin, http.MethodPost, probesURL, e.csrf, "", map[string]any{"name": c.name, "region": c.region})
			requireStatus(t, resp, http.StatusUnprocessableEntity, body)
		}
	})

	var sg createdProbe
	t.Run("create list token delete", func(t *testing.T) {
		resp, body := do(t, e.admin, http.MethodPost, probesURL, e.csrf, "", map[string]any{"name": "Sg", "region": "sg-1"})
		requireStatus(t, resp, http.StatusUnprocessableEntity, body) // uppercase name

		resp, body = do(t, e.admin, http.MethodPost, probesURL, e.csrf, "", map[string]any{"name": "singapore", "region": "sg-1"})
		requireStatus(t, resp, http.StatusCreated, body)
		if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
			t.Fatalf("Cache-Control = %q", cc)
		}
		tok, _ := body["registration_token"].(string)
		if !strings.HasPrefix(tok, "upr_") || body["registration_expires_at"] == nil {
			t.Fatalf("bad create response: %v", body)
		}
		p := body["probe"].(map[string]any)
		if p["registered"] != false || p["is_local"] != false || p["online"] != false || p["region"] != "sg-1" {
			t.Fatalf("probe body: %v", p)
		}
		sg = createdProbe{ID: int64(p["id"].(float64)), Token: tok}

		resp, body = do(t, e.admin, http.MethodPost, probesURL, e.csrf, "", map[string]any{"name": "singapore", "region": "other"})
		requireStatus(t, resp, http.StatusConflict, body)
		resp, body = do(t, e.admin, http.MethodPost, probesURL, e.csrf, "", map[string]any{"name": "other", "region": "sg-1"})
		requireStatus(t, resp, http.StatusConflict, body)

		resp, body = do(t, e.admin, http.MethodGet, probesURL, "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		list := body["probes"].([]any)
		if len(list) != 2 {
			t.Fatalf("probes = %v", list)
		}
		var local, remote map[string]any
		for _, p := range list {
			m := p.(map[string]any)
			if m["is_local"] == true {
				local = m
			} else {
				remote = m
			}
		}
		if local == nil || local["online"] != true || local["registered"] != true || remote == nil || remote["name"] != "singapore" {
			t.Fatalf("list = %v", list)
		}

		// local probe: no token, no delete
		localID := int64(local["id"].(float64))
		resp, body = do(t, e.admin, http.MethodPost, fmt.Sprintf("%s/%d/registration-token", probesURL, localID), e.csrf, "", nil)
		requireStatus(t, resp, http.StatusUnprocessableEntity, body)
		resp, body = do(t, e.admin, http.MethodDelete, fmt.Sprintf("%s/%d", probesURL, localID), e.csrf, "", nil)
		requireStatus(t, resp, http.StatusUnprocessableEntity, body)
		resp, body = do(t, e.admin, http.MethodPost, probesURL+"/99999/registration-token", e.csrf, "", nil)
		requireStatus(t, resp, http.StatusNotFound, body)
		resp, body = do(t, e.admin, http.MethodDelete, probesURL+"/99999", e.csrf, "", nil)
		requireStatus(t, resp, http.StatusNotFound, body)

		// new token revokes the credential
		resp, body = e.register(t, sg.Token, "0.1.0")
		requireStatus(t, resp, http.StatusOK, body)
		cred := body["credential"].(string)
		if _, ok := e.probes.Authenticate(cred); !ok {
			t.Fatal("credential should authenticate after registration")
		}
		resp, body = do(t, e.admin, http.MethodPost, fmt.Sprintf("%s/%d/registration-token", probesURL, sg.ID), e.csrf, "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		newTok, _ := body["registration_token"].(string)
		if !strings.HasPrefix(newTok, "upr_") || newTok == sg.Token || body["registration_expires_at"] == nil {
			t.Fatalf("bad token response: %v", body)
		}
		if _, ok := e.probes.Authenticate(cred); ok {
			t.Fatal("old credential must be revoked from the registry")
		}
		resp, _ = rawRequest(t, http.MethodGet, e.base+probeproto.PathAssignments, map[string]string{"Authorization": "Bearer " + cred}, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("revoked credential status = %d", resp.StatusCode)
		}
		sg.Token = newTok

		// monitors using the region block delete
		resp, body = do(t, e.admin, http.MethodPost, fmt.Sprintf("%s/api/teams/%d/monitors", e.base, e.teamID), e.csrf, "", map[string]any{
			"name": "m", "type": "tcp", "config": map[string]any{"host": "example.com", "port": 443}, "regions": []string{"sg-1"},
		})
		requireStatus(t, resp, http.StatusCreated, body)
		mid := int64(body["id"].(float64))
		resp, body = do(t, e.admin, http.MethodDelete, fmt.Sprintf("%s/%d", probesURL, sg.ID), e.csrf, "", nil)
		requireStatus(t, resp, http.StatusConflict, body)
		resp, body = do(t, e.admin, http.MethodDelete, fmt.Sprintf("%s/api/teams/%d/monitors/%d", e.base, e.teamID, mid), e.csrf, "", nil)
		requireStatus(t, resp, http.StatusNoContent, body)

		before := e.probes.rmCount()
		resp, body = do(t, e.admin, http.MethodDelete, fmt.Sprintf("%s/%d", probesURL, sg.ID), e.csrf, "", nil)
		requireStatus(t, resp, http.StatusNoContent, body)
		if e.probes.rmCount() != before+1 {
			t.Fatal("registry.Remove not called on delete")
		}
		resp, body = do(t, e.admin, http.MethodGet, probesURL, "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		if n := len(body["probes"].([]any)); n != 1 {
			t.Fatalf("probes after delete = %d", n)
		}
	})

	t.Run("regions endpoint", func(t *testing.T) {
		e.createProbe(t, "frankfurt", "fra-1")
		e.probes.setOnline("fra-1")
		for _, c := range []struct {
			client *http.Client
			csrf   string
			bearer string
		}{{e.admin, "", ""}, {e.editor, "", ""}, {newClient(t), "", readTok}} {
			resp, body := do(t, c.client, http.MethodGet, e.base+"/api/regions", c.csrf, c.bearer, nil)
			requireStatus(t, resp, http.StatusOK, body)
			got := map[string]bool{}
			for _, r := range body["regions"].([]any) {
				m := r.(map[string]any)
				got[m["name"].(string)] = m["online"].(bool)
			}
			// fra-1 is unregistered so never online, whatever the registry says
			if got["local"] != true || got["fra-1"] != false {
				t.Fatalf("regions = %v", got)
			}
		}
		resp, body := do(t, newClient(t), http.MethodGet, e.base+"/api/regions", "", "", nil)
		requireStatus(t, resp, http.StatusUnauthorized, body)
	})
}

func TestProbeRegistration(t *testing.T) {
	e := newProbeEnv(t)

	t.Run("success reuse and generic failures", func(t *testing.T) {
		p := e.createProbe(t, "singapore", "sg-1")
		resp, body := e.register(t, p.Token, "1.2.3\nevil")
		requireStatus(t, resp, http.StatusOK, body)
		cred, _ := body["credential"].(string)
		if !strings.HasPrefix(cred, "upp_") || body["region"] != "sg-1" || int64(body["probe_id"].(float64)) != p.ID {
			t.Fatalf("register response: %v", body)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("registration response must not be cacheable")
		}
		if e.probes.addCount() != 1 {
			t.Fatalf("registry adds = %d", e.probes.addCount())
		}
		auth, ok := e.probes.Authenticate(cred)
		if !ok || auth.ID != p.ID || auth.Region != "sg-1" {
			t.Fatalf("authenticate: %+v %v", auth, ok)
		}
		if v := e.probes.version(p.ID); v != "1.2.3evil" {
			t.Fatalf("version = %q (control characters must be stripped)", v)
		}

		// reuse, unknown, wrong prefix, empty: identical generic 401
		var first []byte
		for i, tok := range []string{p.Token, "upr_" + strings.Repeat("A", 43), "ups_whatever", cred, "", strings.Repeat("x", 5000)} {
			b, _ := json.Marshal(probeproto.RegisterRequest{Token: tok})
			resp, raw := rawRequest(t, http.MethodPost, e.base+probeproto.PathRegister, map[string]string{"Content-Type": "application/json"}, b)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("token %d: status = %d, body %s", i, resp.StatusCode, raw)
			}
			if i == 0 {
				first = raw
			} else if !bytes.Equal(first, raw) {
				t.Fatalf("token %d: body differs: %s vs %s", i, raw, first)
			}
			if bytes.Contains(raw, []byte(tok)) && tok != "" {
				t.Fatal("error body must not echo the token")
			}
		}
		if e.probes.addCount() != 1 {
			t.Fatalf("registry adds after failures = %d", e.probes.addCount())
		}
	})

	t.Run("expired", func(t *testing.T) {
		p := e.createProbe(t, "frankfurt", "fra-1")
		conn, err := pgx.Connect(context.Background(), e.dbURL)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(context.Background())
		if _, err := conn.Exec(context.Background(),
			"UPDATE upsera.probes SET registration_expires_at = now() - interval '1 hour' WHERE id = $1", p.ID); err != nil {
			t.Fatal(err)
		}
		resp, body := e.register(t, p.Token, "1")
		requireStatus(t, resp, http.StatusUnauthorized, body)
	})

	t.Run("bad bodies", func(t *testing.T) {
		resp, _ := rawRequest(t, http.MethodPost, e.base+probeproto.PathRegister, nil, []byte("{not json"))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid json status = %d", resp.StatusCode)
		}
		resp, _ = rawRequest(t, http.MethodPost, e.base+probeproto.PathRegister, nil, []byte(`{"token":"`+strings.Repeat("a", 64<<10)+`"}`))
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("oversize status = %d", resp.StatusCode)
		}
		resp, _ = rawRequest(t, http.MethodGet, e.base+probeproto.PathRegister, nil, nil)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("GET register status = %d", resp.StatusCode)
		}
	})

	t.Run("rate limited per IP", func(t *testing.T) {
		e2 := newProbeEnv(t)
		var got429 bool
		for i := 0; i < 12; i++ {
			resp, body := e2.register(t, "upr_"+strings.Repeat("B", 43), "")
			if resp.StatusCode == http.StatusTooManyRequests {
				got429 = true
				if i < 10 {
					t.Fatalf("limited after only %d attempts", i)
				}
				if resp.Header.Get("Retry-After") == "" {
					t.Fatal("missing Retry-After")
				}
				continue
			}
			requireStatus(t, resp, http.StatusUnauthorized, body)
		}
		if !got429 {
			t.Fatal("never rate limited")
		}
		// the limit also covers a valid token
		p := e2.createProbe(t, "tokyo", "tyo-1")
		resp, body := e2.register(t, p.Token, "")
		requireStatus(t, resp, http.StatusTooManyRequests, body)
	})
}

// registerFrom posts a registration attempt carrying the given
// X-Forwarded-For header (empty = none) and returns the status code.
func (e *probeEnv) registerFrom(t *testing.T, xff string) int {
	t.Helper()
	hdr := map[string]string{"Content-Type": "application/json"}
	if xff != "" {
		hdr["X-Forwarded-For"] = xff
	}
	body, _ := json.Marshal(probeproto.RegisterRequest{Token: "upr_" + strings.Repeat("C", 43)})
	resp, _ := rawRequest(t, http.MethodPost, e.base+probeproto.PathRegister, hdr, body)
	return resp.StatusCode
}

// Behind a reverse proxy every request comes from the proxy's address; the
// registration limit must then count per real client, or anyone on the
// internet can lock real probes out of registering.
func TestProbeRegistrationRateLimitBehindProxy(t *testing.T) {
	t.Run("trusted proxy: clients are limited separately", func(t *testing.T) {
		e := newProbeEnvWith(t, func(c *config.Config) {
			c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")}
		})
		// A flood from one client exhausts only that client's allowance.
		for i := 0; i < 12; i++ {
			e.registerFrom(t, "203.0.113.9")
		}
		if got := e.registerFrom(t, "203.0.113.9"); got != http.StatusTooManyRequests {
			t.Fatalf("flooding client status = %d, want 429", got)
		}
		if got := e.registerFrom(t, "198.51.100.7"); got != http.StatusUnauthorized {
			t.Fatalf("another client status = %d, want 401 (not rate limited)", got)
		}
		// Client-supplied leading entries cannot dodge the limit: the
		// rightmost non-proxy entry is the client.
		for i := 0; i < 12; i++ {
			e.registerFrom(t, fmt.Sprintf("10.9.9.%d, 192.0.2.50", i))
		}
		if got := e.registerFrom(t, "10.9.9.200, 192.0.2.50"); got != http.StatusTooManyRequests {
			t.Fatalf("spoofed leading entries status = %d, want 429", got)
		}
	})

	t.Run("untrusted peer: X-Forwarded-For is ignored", func(t *testing.T) {
		e := newProbeEnv(t)
		var limited bool
		for i := 0; i < 12; i++ {
			if e.registerFrom(t, fmt.Sprintf("192.0.2.%d", i+1)) == http.StatusTooManyRequests {
				limited = true
			}
		}
		if !limited {
			t.Fatal("rotating X-Forwarded-For evaded the limit from an untrusted peer")
		}
	})
}

func remoteMonitor(id int64, region string) model.Monitor {
	return model.Monitor{
		ID: id, Name: fmt.Sprintf("mon-%d", id), Type: model.TypeHTTP,
		Config:    json.RawMessage(`{"url":"https://example.com","headers":{"X-Secret":"s3cret"}}`),
		IntervalS: 60, RetryIntervalS: 20, Retries: 2, TimeoutS: 10, Regions: []string{region},
	}
}

func TestProbeCredentialIsolation(t *testing.T) {
	e := newProbeEnv(t)
	_, cred := e.registered(t, "singapore", "sg-1")
	team := e.teamToken(t, "write")
	assignURL := e.base + probeproto.PathAssignments
	monitorsURL := fmt.Sprintf("%s/api/teams/%d/monitors", e.base, e.teamID)

	// sanity: the credential works on the probe endpoint
	resp, _ := rawRequest(t, http.MethodGet, assignURL, map[string]string{"Authorization": "Bearer " + cred}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid credential status = %d", resp.StatusCode)
	}

	t.Run("probe credential on other APIs", func(t *testing.T) {
		for _, u := range []string{monitorsURL, e.base + "/api/regions", e.base + "/api/admin/probes", e.base + "/api/auth/me"} {
			resp, body := do(t, newClient(t), http.MethodGet, u, "", cred, nil)
			requireStatus(t, resp, http.StatusUnauthorized, body)
		}
		// a registration token is no bearer credential either
		p := e.createProbe(t, "tokyo", "tyo-1")
		resp, body := do(t, newClient(t), http.MethodGet, monitorsURL, "", p.Token, nil)
		requireStatus(t, resp, http.StatusUnauthorized, body)
	})

	t.Run("other credentials on probe endpoints", func(t *testing.T) {
		p := e.createProbe(t, "osaka", "osa-1")
		for name, hdr := range map[string]string{
			"team token":         "Bearer " + team,
			"registration token": "Bearer " + p.Token,
			"bare credential":    cred,
			"no prefix":          "Bearer " + strings.TrimPrefix(cred, "upp_"),
			"garbage":            "Bearer upp_nope",
			"lower scheme":       "bearer " + cred,
			"empty":              "",
			"oversize":           "Bearer upp_" + strings.Repeat("A", 500),
		} {
			for _, ep := range []struct{ method, path string }{
				{http.MethodGet, probeproto.PathAssignments}, {http.MethodPost, probeproto.PathResults},
			} {
				resp, raw := rawRequest(t, ep.method, e.base+ep.path, map[string]string{"Authorization": hdr, "Content-Type": "application/json"}, []byte(`{}`))
				if resp.StatusCode != http.StatusUnauthorized {
					t.Fatalf("%s on %s: status = %d", name, ep.path, resp.StatusCode)
				}
				if !strings.Contains(string(raw), "unauthorized") || strings.Contains(string(raw), cred) {
					t.Fatalf("%s: unexpected body %s", name, raw)
				}
			}
		}
	})

	t.Run("session cookie does not authenticate probe endpoints", func(t *testing.T) {
		// e.admin holds a valid session cookie (and could pass CSRF).
		resp, body := do(t, e.admin, http.MethodGet, assignURL, "", "", nil)
		requireStatus(t, resp, http.StatusUnauthorized, body)
		resp, body = do(t, e.admin, http.MethodPost, e.base+probeproto.PathResults, e.csrf, "", probeproto.ResultsRequest{SentAt: time.Now()})
		requireStatus(t, resp, http.StatusUnauthorized, body)
	})
}

func TestProbeAssignments(t *testing.T) {
	e := newProbeEnv(t)
	id, cred := e.registered(t, "singapore", "sg-1")
	hdr := map[string]string{"Authorization": "Bearer " + cred}
	url := e.base + probeproto.PathAssignments

	paused := remoteMonitor(3, "sg-1")
	paused.Paused = true
	push := remoteMonitor(4, "sg-1")
	push.Type = model.TypePush
	e.sink.set("v1", map[string][]model.Monitor{
		"sg-1":  {remoteMonitor(1, "sg-1"), remoteMonitor(2, "sg-1"), remoteMonitor(5, "fra-1"), paused, push},
		"fra-1": {remoteMonitor(9, "fra-1")},
	})

	seenBefore := e.probes.seenCount(id)
	resp, raw := rawRequest(t, http.MethodGet, url, hdr, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, raw)
	}
	var got probeproto.AssignmentsResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != "v1" || got.Region != "sg-1" || got.BlockPrivateTargets {
		t.Fatalf("response: %+v", got)
	}
	if len(got.Monitors) != 2 || got.Monitors[0].MonitorID != 1 || got.Monitors[1].MonitorID != 2 {
		t.Fatalf("monitors must only be the probe's own unpaused remote ones: %+v", got.Monitors)
	}
	a := got.Monitors[0]
	if a.Type != "http" || a.IntervalS != 60 || a.RetryIntervalS != 20 || a.Retries != 2 || a.TimeoutS != 10 ||
		!strings.Contains(string(a.Config), "s3cret") {
		t.Fatalf("assignment: %+v config=%s", a, a.Config)
	}
	if e.probes.seenCount(id) != seenBefore+1 {
		t.Fatal("Seen not recorded")
	}
	if resp.Header.Get("ETag") != `"v1"` {
		t.Fatalf("ETag = %q", resp.Header.Get("ETag"))
	}

	t.Run("304", func(t *testing.T) {
		for _, inm := range []string{`"v1"`, `W/"v1"`, `"x", "v1"`, `*`} {
			h := map[string]string{"Authorization": hdr["Authorization"], "If-None-Match": inm}
			resp, raw := rawRequest(t, http.MethodGet, url, h, nil)
			if resp.StatusCode != http.StatusNotModified || len(raw) != 0 || resp.Header.Get("ETag") != `"v1"` {
				t.Fatalf("If-None-Match %s: status %d body %q etag %q", inm, resp.StatusCode, raw, resp.Header.Get("ETag"))
			}
		}
		h := map[string]string{"Authorization": hdr["Authorization"], "If-None-Match": `"old"`}
		if resp, _ := rawRequest(t, http.MethodGet, url, h, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("stale etag status = %d", resp.StatusCode)
		}
		// unauthenticated conditional requests must not leak 304
		if resp, _ := rawRequest(t, http.MethodGet, url, map[string]string{"If-None-Match": `"v1"`}, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauthenticated status = %d", resp.StatusCode)
		}
		// version change
		e.sink.set("v2", map[string][]model.Monitor{"sg-1": {remoteMonitor(1, "sg-1")}})
		h["If-None-Match"] = `"v1"`
		resp, raw := rawRequest(t, http.MethodGet, url, h, nil)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != `"v2"` {
			t.Fatalf("after change: status %d etag %q %s", resp.StatusCode, resp.Header.Get("ETag"), raw)
		}
	})

	t.Run("block_private_targets", func(t *testing.T) {
		e.policy.SetBlockPrivate(true)
		defer e.policy.SetBlockPrivate(false)
		h := map[string]string{"Authorization": hdr["Authorization"], "If-None-Match": `"v2"`}
		resp, raw := rawRequest(t, http.MethodGet, url, h, nil)
		var got probeproto.AssignmentsResponse
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		// flipping the setting must not be hidden behind a 304
		if resp.StatusCode != http.StatusOK || !got.BlockPrivateTargets || got.Version == "v2" {
			t.Fatalf("status %d resp %+v", resp.StatusCode, got)
		}
		h["If-None-Match"] = resp.Header.Get("ETag")
		if resp, _ := rawRequest(t, http.MethodGet, url, h, nil); resp.StatusCode != http.StatusNotModified {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})
}

func postResults(t *testing.T, e *probeEnv, cred string, body any) (*http.Response, []byte) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return rawRequest(t, http.MethodPost, e.base+probeproto.PathResults,
		map[string]string{"Authorization": "Bearer " + cred, "Content-Type": "application/json"}, b)
}

func TestProbeResults(t *testing.T) {
	e := newProbeEnv(t)
	id, cred := e.registered(t, "singapore", "sg-1")
	e.sink.set("v1", map[string][]model.Monitor{"sg-1": {remoteMonitor(1, "sg-1"), remoteMonitor(2, "sg-1")}})

	t.Run("accepted, converted and filtered", func(t *testing.T) {
		now := time.Now()
		exp := now.Add(48 * time.Hour)
		resp, raw := postResults(t, e, cred, probeproto.ResultsRequest{
			SentAt: now, Version: "9.9.9",
			Results: []probeproto.Result{
				{MonitorID: 2, Time: now.Add(-20 * time.Second), OK: false, LatencyMs: -5, Message: "boom"},
				{MonitorID: 1, Time: now.Add(-40 * time.Second), OK: true, LatencyMs: 12, Message: "ok", TLSExpiresAt: &exp},
				{MonitorID: 77, Time: now, OK: true},                     // not assigned to the region
				{MonitorID: 1, Time: time.Time{}, OK: true},              // no time
				{MonitorID: 1, Time: now.Add(-48 * time.Hour), OK: true}, // far too old
			},
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, raw)
		}
		var out probeproto.ResultsResponse
		_ = json.Unmarshal(raw, &out)
		if out.Accepted != 2 {
			t.Fatalf("accepted = %d", out.Accepted)
		}
		calls := e.sink.calls()
		if len(calls) != 1 || calls[0].probeID != id || calls[0].region != "sg-1" || len(calls[0].rs) != 2 {
			t.Fatalf("report calls: %+v", calls)
		}
		rs := calls[0].rs
		// sorted by time: monitor 1 (-40s) first
		if rs[0].MonitorID != 1 || rs[0].Status != model.StatusUp || rs[0].LatencyMs != 12 || rs[0].TLSExpiresAt == nil ||
			!rs[0].TLSExpiresAt.Equal(exp) {
			t.Fatalf("rs[0] = %+v", rs[0])
		}
		if rs[1].MonitorID != 2 || rs[1].Status != model.StatusDown || rs[1].LatencyMs != 0 || rs[1].Message != "boom" {
			t.Fatalf("rs[1] = %+v", rs[1])
		}
		if e.probes.version(id) != "9.9.9" {
			t.Fatalf("version = %q", e.probes.version(id))
		}
	})

	t.Run("time shift and future clamp", func(t *testing.T) {
		before := len(e.sink.calls())
		now := time.Now()
		// the probe's clock is 1h behind the server's
		sent := now.Add(-time.Hour)
		resp, raw := postResults(t, e, cred, probeproto.ResultsRequest{
			SentAt: sent,
			Results: []probeproto.Result{
				{MonitorID: 1, Time: sent.Add(-30 * time.Second), OK: true},
				{MonitorID: 2, Time: sent.Add(10 * time.Minute), OK: true}, // "future" even after shifting
			},
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, raw)
		}
		rs := e.sink.calls()[before].rs
		if d := now.Sub(rs[0].Time); d < 29*time.Second || d > 40*time.Second {
			t.Fatalf("shifted time is %v ago, want ~30s", d)
		}
		if rs[1].Time.After(time.Now()) || time.Since(rs[1].Time) > 5*time.Second {
			t.Fatalf("future time not clamped to now: %v", rs[1].Time)
		}
	})

	t.Run("limits and validation", func(t *testing.T) {
		before := len(e.sink.calls())
		now := time.Now()
		many := make([]probeproto.Result, probeproto.MaxResultsPerBatch+1)
		for i := range many {
			many[i] = probeproto.Result{MonitorID: 1, Time: now, OK: true}
		}
		resp, _ := postResults(t, e, cred, probeproto.ResultsRequest{SentAt: now, Results: many})
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("too many results: status = %d", resp.StatusCode)
		}
		resp, _ = postResults(t, e, cred, probeproto.ResultsRequest{SentAt: now, Results: []probeproto.Result{
			{MonitorID: 1, Time: now, OK: true, Message: strings.Repeat("x", probeproto.MaxBatchBytes)},
		}})
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("too many bytes: status = %d", resp.StatusCode)
		}
		resp, _ = postResults(t, e, cred, probeproto.ResultsRequest{Results: []probeproto.Result{{MonitorID: 1, Time: now}}})
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("missing sent_at: status = %d", resp.StatusCode)
		}
		resp, _ = rawRequest(t, http.MethodPost, e.base+probeproto.PathResults,
			map[string]string{"Authorization": "Bearer " + cred}, []byte(`{"sent_at":"nope"}`))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("bad json: status = %d", resp.StatusCode)
		}
		resp, _ = rawRequest(t, http.MethodPost, e.base+probeproto.PathResults,
			map[string]string{"Authorization": "Bearer " + cred}, []byte(`{"sent_at":"`+now.Format(time.RFC3339)+`"} {}`))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("trailing data: status = %d", resp.StatusCode)
		}
		// empty batch is fine and reports nothing
		resp, _ = postResults(t, e, cred, probeproto.ResultsRequest{SentAt: now})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("empty batch: status = %d", resp.StatusCode)
		}
		if len(e.sink.calls()) != before {
			t.Fatal("rejected or empty batches must not reach the sink")
		}
	})
}

func TestMonitorRegions(t *testing.T) {
	e := newProbeEnv(t)
	e.createProbe(t, "singapore", "sg-1")
	e.createProbe(t, "frankfurt", "fra-1")
	monitorsURL := fmt.Sprintf("%s/api/teams/%d/monitors", e.base, e.teamID)
	tcp := map[string]any{"host": "example.com", "port": 443}

	create := func(extra map[string]any, typ string, cfg any) (*http.Response, map[string]any) {
		b := map[string]any{"name": "m", "type": typ, "config": cfg}
		for k, v := range extra {
			b[k] = v
		}
		return do(t, e.admin, http.MethodPost, monitorsURL, e.csrf, "", b)
	}

	t.Run("defaults", func(t *testing.T) {
		resp, body := create(nil, "tcp", tcp)
		requireStatus(t, resp, http.StatusCreated, body)
		if fmt.Sprint(body["regions"]) != "[local]" || body["quorum"] != float64(1) {
			t.Fatalf("regions/quorum = %v/%v", body["regions"], body["quorum"])
		}
		rs, ok := body["region_states"].(map[string]any)
		if !ok || len(rs) != 0 {
			t.Fatalf("region_states = %#v, want empty object", body["region_states"])
		}
	})

	t.Run("multi region default quorum", func(t *testing.T) {
		resp, body := create(map[string]any{"regions": []string{"local", "sg-1", "fra-1"}}, "tcp", tcp)
		requireStatus(t, resp, http.StatusCreated, body)
		if fmt.Sprint(body["regions"]) != "[local sg-1 fra-1]" || body["quorum"] != float64(2) {
			t.Fatalf("regions/quorum = %v/%v", body["regions"], body["quorum"])
		}
		id := int64(body["id"].(float64))
		// explicit quorum on update
		resp, body = do(t, e.admin, http.MethodPut, fmt.Sprintf("%s/%d", monitorsURL, id), e.csrf, "", map[string]any{
			"name": "m", "type": "tcp", "config": tcp, "regions": []string{"sg-1", "fra-1"}, "quorum": 1,
		})
		requireStatus(t, resp, http.StatusOK, body)
		if fmt.Sprint(body["regions"]) != "[sg-1 fra-1]" || body["quorum"] != float64(1) {
			t.Fatalf("after update: %v/%v", body["regions"], body["quorum"])
		}
		// the runner got the regions
		if m := e.runner.monitors[id]; len(m.Regions) != 2 || m.Quorum != 1 {
			t.Fatalf("runner monitor: %+v", m)
		}
		// invalid update leaves it unchanged
		resp, body = do(t, e.admin, http.MethodPut, fmt.Sprintf("%s/%d", monitorsURL, id), e.csrf, "", map[string]any{
			"name": "m", "type": "tcp", "config": tcp, "regions": []string{"sg-1"}, "quorum": 2,
		})
		requireStatus(t, resp, http.StatusUnprocessableEntity, body)
	})

	t.Run("update without regions keeps them", func(t *testing.T) {
		resp, body := create(map[string]any{"regions": []string{"sg-1", "fra-1"}, "quorum": 1}, "tcp", tcp)
		requireStatus(t, resp, http.StatusCreated, body)
		id := int64(body["id"].(float64))
		// A client that does not know about regions renames the monitor.
		resp, body = do(t, e.admin, http.MethodPut, fmt.Sprintf("%s/%d", monitorsURL, id), e.csrf, "", map[string]any{
			"name": "renamed", "type": "tcp", "config": tcp,
		})
		requireStatus(t, resp, http.StatusOK, body)
		if fmt.Sprint(body["regions"]) != "[sg-1 fra-1]" || body["quorum"] != float64(1) {
			t.Fatalf("after update without regions: %v/%v, want [sg-1 fra-1]/1", body["regions"], body["quorum"])
		}
		if m := e.runner.monitors[id]; len(m.Regions) != 2 || m.Quorum != 1 || m.Name != "renamed" {
			t.Fatalf("runner monitor: %+v", m)
		}
		// Regions given without a quorum: majority of the new list.
		resp, body = do(t, e.admin, http.MethodPut, fmt.Sprintf("%s/%d", monitorsURL, id), e.csrf, "", map[string]any{
			"name": "renamed", "type": "tcp", "config": tcp, "regions": []string{"local", "sg-1", "fra-1"},
		})
		requireStatus(t, resp, http.StatusOK, body)
		if body["quorum"] != float64(2) {
			t.Fatalf("quorum = %v, want majority 2", body["quorum"])
		}
		// A quorum without regions applies to the kept regions and is validated.
		resp, body = do(t, e.admin, http.MethodPut, fmt.Sprintf("%s/%d", monitorsURL, id), e.csrf, "", map[string]any{
			"name": "renamed", "type": "tcp", "config": tcp, "quorum": 3,
		})
		requireStatus(t, resp, http.StatusOK, body)
		if fmt.Sprint(body["regions"]) != "[local sg-1 fra-1]" || body["quorum"] != float64(3) {
			t.Fatalf("quorum-only update: %v/%v", body["regions"], body["quorum"])
		}
		resp, body = do(t, e.admin, http.MethodPut, fmt.Sprintf("%s/%d", monitorsURL, id), e.csrf, "", map[string]any{
			"name": "renamed", "type": "tcp", "config": tcp, "quorum": 4,
		})
		requireStatus(t, resp, http.StatusUnprocessableEntity, body)
		// Going back to local only is explicit.
		resp, body = do(t, e.admin, http.MethodPut, fmt.Sprintf("%s/%d", monitorsURL, id), e.csrf, "", map[string]any{
			"name": "renamed", "type": "tcp", "config": tcp, "regions": []string{"local"},
		})
		requireStatus(t, resp, http.StatusOK, body)
		if fmt.Sprint(body["regions"]) != "[local]" || body["quorum"] != float64(1) {
			t.Fatalf("explicit local: %v/%v", body["regions"], body["quorum"])
		}
	})

	t.Run("validation", func(t *testing.T) {
		for name, c := range map[string]struct {
			extra map[string]any
			typ   string
			cfg   any
		}{
			"unknown region":            {map[string]any{"regions": []string{"mars-1"}}, "tcp", tcp},
			"unknown with local":        {map[string]any{"regions": []string{"local", "mars-1"}}, "tcp", tcp},
			"duplicates":                {map[string]any{"regions": []string{"sg-1", "sg-1"}}, "tcp", tcp},
			"bad name":                  {map[string]any{"regions": []string{"SG 1"}}, "tcp", tcp},
			"remote on push":            {map[string]any{"regions": []string{"sg-1"}}, "push", map[string]any{}},
			"remote with local on push": {map[string]any{"regions": []string{"local", "sg-1"}}, "push", map[string]any{}},
			"quorum zero":               {map[string]any{"regions": []string{"sg-1"}, "quorum": 0}, "tcp", tcp},
			"quorum too high":           {map[string]any{"regions": []string{"sg-1", "fra-1"}, "quorum": 3}, "tcp", tcp},
			"quorum vs local":           {map[string]any{"quorum": 2}, "tcp", tcp},
			"too many regions":          {map[string]any{"regions": []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}}, "tcp", tcp},
		} {
			resp, body := create(c.extra, c.typ, c.cfg)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("%s: status = %d, want 422, body %v", name, resp.StatusCode, body)
			}
		}
		// local-only push stays fine
		resp, body := create(map[string]any{"regions": []string{"local"}}, "push", map[string]any{})
		requireStatus(t, resp, http.StatusCreated, body)
	})

	t.Run("region_states and heartbeat region", func(t *testing.T) {
		resp, body := create(map[string]any{"regions": []string{"local", "sg-1"}, "quorum": 1}, "tcp", tcp)
		requireStatus(t, resp, http.StatusCreated, body)
		id := int64(body["id"].(float64))
		at := time.Now().UTC().Truncate(time.Second)
		e.runner.mu.Lock()
		e.runner.states[id] = model.MonitorState{
			MonitorID: id, Status: model.StatusUp, Since: at, LastCheckAt: at,
			Regions: map[string]model.RegionState{
				"sg-1":  {Status: model.StatusDown, ConsecutiveFailures: 3, LastResultAt: at, LatencyMs: 0, Message: "refused"},
				"local": {Status: model.StatusUp, LastResultAt: at, LatencyMs: 12, Message: "ok"},
			},
		}
		e.runner.mu.Unlock()

		check := func(m map[string]any) {
			t.Helper()
			rs, ok := m["region_states"].(map[string]any)
			if !ok || len(rs) != 2 {
				t.Fatalf("region_states = %#v", m["region_states"])
			}
			sg := rs["sg-1"].(map[string]any)
			if sg["status"] != "down" && sg["status"] != float64(model.StatusDown) {
				t.Fatalf("sg-1 = %v", sg)
			}
			if sg["consecutive_failures"] != float64(3) || sg["message"] != "refused" || sg["last_result_at"] == nil {
				t.Fatalf("sg-1 = %v", sg)
			}
			if rs["local"].(map[string]any)["latency_ms"] != float64(12) {
				t.Fatalf("local = %v", rs["local"])
			}
		}
		resp, body = do(t, e.admin, http.MethodGet, fmt.Sprintf("%s/%d", monitorsURL, id), "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		check(body)
		resp, body = do(t, e.admin, http.MethodGet, monitorsURL, "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		found := false
		for _, m := range body["monitors"].([]any) {
			if int64(m.(map[string]any)["id"].(float64)) == id {
				check(m.(map[string]any))
				found = true
			}
		}
		if !found {
			t.Fatal("monitor missing from list")
		}

		// heartbeats carry their probe's region
		st, _ := store.Open(context.Background(), e.dbURL, 2)
		defer st.Close()
		ps, err := st.ListProbes(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var localID, sgID int64
		for _, p := range ps {
			switch p.Region {
			case "local":
				localID = p.ID
			case "sg-1":
				sgID = p.ID
			}
		}
		now := time.Now().UTC()
		if err := st.InsertHeartbeats(context.Background(), []model.Heartbeat{
			{MonitorID: id, ProbeID: localID, Time: now.Add(-2 * time.Second), Status: model.StatusUp, LatencyMs: 5},
			{MonitorID: id, ProbeID: sgID, Time: now.Add(-1 * time.Second), Status: model.StatusUp, LatencyMs: 9},
		}); err != nil {
			t.Fatal(err)
		}
		resp, body = do(t, e.admin, http.MethodGet, fmt.Sprintf("%s/%d/heartbeats", monitorsURL, id), "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		regions := map[int64]string{}
		for _, h := range body["heartbeats"].([]any) {
			m := h.(map[string]any)
			regions[int64(m["probe_id"].(float64))] = m["region"].(string)
		}
		if regions[localID] != "local" || regions[sgID] != "sg-1" {
			t.Fatalf("heartbeat regions = %v", regions)
		}
	})
}

// The results path only needs membership, so it must not build the full
// assignment listing (copy, sort, hash) for every batch.
func TestProbeResultsDoNotListAssignments(t *testing.T) {
	e := newProbeEnv(t)
	_, cred := e.registered(t, "singapore", "sg-1")
	e.sink.set("v1", map[string][]model.Monitor{"sg-1": {remoteMonitor(11, "sg-1")}})
	resp, raw := postResults(t, e, cred, probeproto.ResultsRequest{
		SentAt: time.Now(), Results: []probeproto.Result{
			{MonitorID: 11, Time: time.Now(), OK: true, LatencyMs: 5},
			{MonitorID: 99, Time: time.Now(), OK: true}, // not assigned: dropped
		},
	})
	var out probeproto.ResultsResponse
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &out) != nil || out.Accepted != 1 {
		t.Fatalf("status %d accepted %d, want 200 and 1: %s", resp.StatusCode, out.Accepted, raw)
	}
	if n := e.sink.listings(); n != 0 {
		t.Fatalf("Assignments called %d times on the results path, want 0", n)
	}
}

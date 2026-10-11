package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/davidsugianto/upsera/internal/api"
	"github.com/davidsugianto/upsera/internal/config"
	"github.com/davidsugianto/upsera/internal/events"
	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
	"github.com/davidsugianto/upsera/internal/testutil"
)

// fakeStatusCache records what the API tells the public status page
// subsystem.
type fakeStatusCache struct {
	mu        sync.Mutex
	calls     []string
	reloadErr error
}

func (f *fakeStatusCache) InvalidatePage(slug string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "invalidate:"+slug)
}

func (f *fakeStatusCache) InvalidateAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "invalidate-all")
}

func (f *fakeStatusCache) ReloadDomains(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "reload")
	return f.reloadErr
}

// take returns the calls recorded since the last take.
func (f *fakeStatusCache) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

func (f *fakeStatusCache) setReloadErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloadErr = err
}

func statusPageReq(slug string, items ...map[string]any) map[string]any {
	if items == nil {
		items = []map[string]any{}
	}
	return map[string]any{"slug": slug, "title": "Status " + slug, "published": true, "items": items}
}

func withField(m map[string]any, kv ...any) map[string]any {
	out := make(map[string]any, len(m)+len(kv)/2)
	for k, v := range m {
		out[k] = v
	}
	for i := 0; i < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func statusItem(monitorID int64, section, name string) map[string]any {
	it := map[string]any{"monitor_id": monitorID}
	if section != "" {
		it["section"] = section
	}
	if name != "" {
		it["display_name"] = name
	}
	return it
}

// sameInstant reports whether the JSON timestamp v is the instant want.
func sameInstant(v any, want string) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	got, err := time.Parse(time.RFC3339Nano, s)
	exp, _ := time.Parse(time.RFC3339, want)
	return err == nil && got.Equal(exp)
}

func asList(t *testing.T, body map[string]any, key string) []map[string]any {
	t.Helper()
	raw, ok := body[key].([]any)
	if !ok {
		t.Fatalf("body[%q] is not a list: %v", key, body)
	}
	out := make([]map[string]any, len(raw))
	for i, r := range raw {
		out[i] = r.(map[string]any)
	}
	return out
}

func itemNames(t *testing.T, body map[string]any) []string {
	t.Helper()
	var out []string
	for _, it := range asList(t, body, "items") {
		out = append(out, fmt.Sprintf("%v/%v", it["section"], it["display_name"]))
	}
	return out
}

func TestStatusPagesAPI(t *testing.T) {
	st, dbURL := testutil.Store(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(ctx) })

	cache := &fakeStatusCache{}
	h := api.NewRouter(api.Deps{
		Store: st, Runner: newFakeRunner(), Alerting: newFakeAlerting(), Maintenance: newFakeMaintenance(),
		Policy: netpolicy.New(""), StatusPages: cache,
		Config: config.Config{BaseURL: "http://upsera.test", Port: 3080},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test", Events: events.NewHub(),
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	base := srv.URL

	// ---- accounts: admin (owner of two teams), viewer, outsider ----
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
	otherTeamID := int64(body["id"].(float64))

	login := func(email, password, role string) (*http.Client, string) {
		t.Helper()
		resp, body := do(t, admin, http.MethodPost, base+"/api/admin/users", csrf, "", map[string]any{
			"email": email, "name": role, "password": password, "is_admin": false,
		})
		requireStatus(t, resp, http.StatusCreated, body)
		if role != "outsider" {
			resp, body = do(t, admin, http.MethodPost, fmt.Sprintf("%s/api/teams/%d/members", base, teamID), csrf, "", map[string]any{
				"email": email, "role": role,
			})
			requireStatus(t, resp, http.StatusCreated, body)
		}
		c := newClient(t)
		resp, body = do(t, c, http.MethodPost, base+"/api/auth/login", "", "", map[string]any{"email": email, "password": password})
		requireStatus(t, resp, http.StatusOK, body)
		var m meResponse
		decodeInto(t, body, &m)
		return c, m.CSRFToken
	}
	viewer, viewerCSRF := login("viewer@example.com", "viewer password", "viewer")
	editor, editorCSRF := login("editor@example.com", "editor password", "editor")
	outsider, outsiderCSRF := login("outsider@example.com", "outsider password", "outsider")

	mkMonitor := func(team int64, name string) int64 {
		t.Helper()
		m, err := st.CreateMonitor(ctx, model.Monitor{TeamID: team, Name: name, Type: model.TypeTCP,
			Config: json.RawMessage(`{"host":"db","port":5432}`), IntervalS: 60, RetryIntervalS: 20, Retries: 1, TimeoutS: 10})
		if err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	web, db, cdn := mkMonitor(teamID, "web-prod-1"), mkMonitor(teamID, "postgres main"), mkMonitor(teamID, "cdn")
	foreign := mkMonitor(otherTeamID, "foreign")

	pagesURL := fmt.Sprintf("%s/api/teams/%d/status-pages", base, teamID)
	pageURL := func(id int64) string { return fmt.Sprintf("%s/%d", pagesURL, id) }
	write := func(method, url string, payload any) (*http.Response, map[string]any) {
		return do(t, admin, method, url, csrf, "", payload)
	}
	read := func(url string) (*http.Response, map[string]any) {
		return do(t, admin, http.MethodGet, url, "", "", nil)
	}
	wantCalls := func(t *testing.T, want ...string) {
		t.Helper()
		if got := cache.take(); !slices.Equal(got, want) {
			t.Fatalf("cache calls = %v, want %v", got, want)
		}
	}
	cache.take()

	createPage := func(slug string, items ...map[string]any) int64 {
		t.Helper()
		resp, body := write(http.MethodPost, pagesURL, statusPageReq(slug, items...))
		requireStatus(t, resp, http.StatusCreated, body)
		cache.take()
		return int64(body["id"].(float64))
	}

	t.Run("authorization", func(t *testing.T) {
		pid := createPage("rbac", statusItem(web, "", ""))
		// Viewers read.
		resp, body := do(t, viewer, http.MethodGet, pagesURL, "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		resp, body = do(t, viewer, http.MethodGet, pageURL(pid), "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		resp, body = do(t, viewer, http.MethodGet, pageURL(pid)+"/incidents", "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		resp, body = do(t, viewer, http.MethodGet, pageURL(pid)+"/subscribers", "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		// ... but never write.
		for _, c := range []struct {
			method, url string
			payload     any
		}{
			{http.MethodPost, pagesURL, statusPageReq("viewer-made")},
			{http.MethodPut, pageURL(pid), statusPageReq("rbac")},
			{http.MethodDelete, pageURL(pid), nil},
			{http.MethodPost, pageURL(pid) + "/incidents", map[string]any{"title": "x", "severity": "minor"}},
			{http.MethodPut, pageURL(pid) + "/incidents/1", map[string]any{"title": "x", "severity": "minor"}},
			{http.MethodPost, pageURL(pid) + "/incidents/1/updates", map[string]any{"status": "resolved", "body": "x"}},
			{http.MethodPost, pageURL(pid) + "/incidents/1/publish", nil},
			{http.MethodDelete, pageURL(pid) + "/incidents/1", nil},
			{http.MethodPost, pageURL(pid) + "/subscribers", map[string]any{"url": "https://hooks.example.com/x"}},
			{http.MethodDelete, pageURL(pid) + "/subscribers/1", nil},
		} {
			resp, body := do(t, viewer, c.method, c.url, viewerCSRF, "", c.payload)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("viewer %s %s: status %d, body %v", c.method, c.url, resp.StatusCode, body)
			}
		}
		// Outsiders cannot even see the team.
		for _, c := range []struct{ method, url string }{
			{http.MethodGet, pagesURL}, {http.MethodGet, pageURL(pid)}, {http.MethodGet, pageURL(pid) + "/incidents"},
			{http.MethodGet, pageURL(pid) + "/subscribers"}, {http.MethodDelete, pageURL(pid)},
		} {
			resp, body := do(t, outsider, c.method, c.url, outsiderCSRF, "", nil)
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("outsider %s %s: status %d, body %v", c.method, c.url, resp.StatusCode, body)
			}
		}
		// Anonymous callers are rejected.
		resp, body = do(t, newClient(t), http.MethodGet, pagesURL, "", "", nil)
		requireStatus(t, resp, http.StatusUnauthorized, body)
		// Editors write.
		resp, body = do(t, editor, http.MethodPut, pageURL(pid), editorCSRF, "", statusPageReq("rbac", statusItem(db, "", "")))
		requireStatus(t, resp, http.StatusOK, body)
		cache.take()
		if pages := cache.take(); len(pages) != 0 {
			t.Fatalf("unexpected calls %v", pages)
		}
	})

	t.Run("custom domain is instance-admin only", func(t *testing.T) {
		pid := createPage("domain-admin", statusItem(web, "", ""))
		withDomain := func(slug, domain string) map[string]any {
			return withField(statusPageReq(slug, statusItem(web, "", "")), "custom_domain", domain)
		}
		domainOf := func() any {
			_, body := read(pageURL(pid))
			return body["custom_domain"]
		}

		// An editor cannot claim a domain, on create or update.
		resp, body := do(t, editor, http.MethodPost, pagesURL, editorCSRF, "", withDomain("editor-claim", "status.claimed.example.com"))
		requireStatus(t, resp, http.StatusForbidden, body)
		resp, body = do(t, editor, http.MethodPut, pageURL(pid), editorCSRF, "", withDomain("domain-admin", "status.claimed.example.com"))
		requireStatus(t, resp, http.StatusForbidden, body)
		if domainOf() != nil {
			t.Fatalf("an editor set the domain: %v", domainOf())
		}

		// A write token cannot either.
		resp, body = write(http.MethodPost, fmt.Sprintf("%s/api/teams/%d/tokens", base, teamID), map[string]any{"name": "ci", "scope": "write"})
		requireStatus(t, resp, http.StatusCreated, body)
		bearer := body["token"].(string)
		resp, body = do(t, newClient(t), http.MethodPut, pageURL(pid), "", bearer, withDomain("domain-admin", "status.claimed.example.com"))
		requireStatus(t, resp, http.StatusForbidden, body)

		// An instance admin can.
		resp, body = write(http.MethodPut, pageURL(pid), withDomain("domain-admin", "status.owned.example.com"))
		requireStatus(t, resp, http.StatusOK, body)
		if domainOf() != "status.owned.example.com" {
			t.Fatalf("domain = %v", domainOf())
		}

		// An editor may still edit the page while the domain stays as it is...
		edit := withField(withDomain("domain-admin", "Status.Owned.Example.com"), "title", "Edited by editor")
		resp, body = do(t, editor, http.MethodPut, pageURL(pid), editorCSRF, "", edit)
		requireStatus(t, resp, http.StatusOK, body)
		if body["title"] != "Edited by editor" || body["custom_domain"] != "status.owned.example.com" {
			t.Fatalf("editor edit = %v", body)
		}
		// ...but cannot change it to another name.
		resp, body = do(t, editor, http.MethodPut, pageURL(pid), editorCSRF, "", withDomain("domain-admin", "other.owned.example.com"))
		requireStatus(t, resp, http.StatusForbidden, body)
		// Leaving custom_domain out (or null) means "unchanged" for an editor,
		// so scripts that do not know the admin-set domain still work, and
		// cannot remove it either.
		resp, body = do(t, editor, http.MethodPut, pageURL(pid), editorCSRF, "", withField(statusPageReq("domain-admin", statusItem(web, "", "")), "title", "Edited again"))
		requireStatus(t, resp, http.StatusOK, body)
		if body["title"] != "Edited again" || body["custom_domain"] != "status.owned.example.com" {
			t.Fatalf("editor edit without a domain = %v", body)
		}
		resp, body = do(t, editor, http.MethodPut, pageURL(pid), editorCSRF, "", withDomain("domain-admin", ""))
		requireStatus(t, resp, http.StatusOK, body)
		if domainOf() != "status.owned.example.com" {
			t.Fatalf("domain after the editor tried to blank it = %v", domainOf())
		}

		// An instance admin leaving it out still removes it (full replace).
		resp, body = write(http.MethodPut, pageURL(pid), statusPageReq("domain-admin", statusItem(web, "", "")))
		requireStatus(t, resp, http.StatusOK, body)
		if domainOf() != nil {
			t.Fatalf("admin removal left %v", domainOf())
		}
		cache.take()
	})

	t.Run("validation", func(t *testing.T) {
		good := statusPageReq("valid-slug", statusItem(web, "", ""))
		many := make([]map[string]any, 101)
		for i := range many {
			many[i] = statusItem(int64(i+1), "", "")
		}
		cases := map[string]map[string]any{
			"slug upper case":        withField(good, "slug", "Bad"),
			"slug too short":         withField(good, "slug", "a"),
			"slug leading hyphen":    withField(good, "slug", "-bad"),
			"slug underscore":        withField(good, "slug", "bad_slug"),
			"slug slash":             withField(good, "slug", "a/b"),
			"slug too long":          withField(good, "slug", strings.Repeat("a", 64)),
			"slug reserved api":      withField(good, "slug", "api"),
			"slug reserved status":   withField(good, "slug", "status"),
			"slug reserved healthz":  withField(good, "slug", "healthz"),
			"slug reserved metrics":  withField(good, "slug", "metrics"),
			"slug missing":           withField(good, "slug", ""),
			"title blank":            withField(good, "title", "   "),
			"title empty":            withField(good, "title", ""),
			"title too long":         withField(good, "title", strings.Repeat("t", 101)),
			"description too long":   withField(good, "description", strings.Repeat("d", 501)),
			"theme unknown":          withField(good, "theme", "neon"),
			"logo http":              withField(good, "logo_url", "http://example.com/l.png"),
			"logo script":            withField(good, "logo_url", "javascript:alert(1)"),
			"logo userinfo":          withField(good, "logo_url", "https://user:pw@example.com/l.png"),
			"logo no host":           withField(good, "logo_url", "https:///l.png"),
			"logo too long":          withField(good, "logo_url", "https://example.com/"+strings.Repeat("a", 500)),
			"domain with scheme":     withField(good, "custom_domain", "https://status.example.com"),
			"domain with port":       withField(good, "custom_domain", "status.example.com:8443"),
			"domain with path":       withField(good, "custom_domain", "status.example.com/x"),
			"domain without dot":     withField(good, "custom_domain", "localhost"),
			"domain ipv4":            withField(good, "custom_domain", "10.0.0.1"),
			"domain numeric tld":     withField(good, "custom_domain", "999.999.999.999"),
			"domain ipv6":            withField(good, "custom_domain", "[::1]"),
			"domain empty label":     withField(good, "custom_domain", "a..example.com"),
			"domain trailing dot":    withField(good, "custom_domain", "status.example.com."),
			"domain leading hyphen":  withField(good, "custom_domain", "-a.example.com"),
			"domain underscore":      withField(good, "custom_domain", "under_score.example.com"),
			"domain wildcard":        withField(good, "custom_domain", "*.example.com"),
			"domain space":           withField(good, "custom_domain", "a b.example.com"),
			"domain is the base":     withField(good, "custom_domain", "upsera.test"),
			"domain is the base (U)": withField(good, "custom_domain", "Upsera.TEST"),
			"domain too long":        withField(good, "custom_domain", strings.Repeat("a", 250)+".example.com"),
			"section too long":       withField(good, "items", []map[string]any{statusItem(web, strings.Repeat("s", 101), "")}),
			"display name too long":  withField(good, "items", []map[string]any{statusItem(web, "", strings.Repeat("n", 101))}),
			"monitor id zero":        withField(good, "items", []map[string]any{{"monitor_id": 0}}),
			"monitor missing":        withField(good, "items", []map[string]any{{"display_name": "x"}}),
			"monitor unknown":        withField(good, "items", []map[string]any{statusItem(999999, "", "")}),
			"monitor of other team":  withField(good, "items", []map[string]any{statusItem(web, "", ""), statusItem(foreign, "", "")}),
			"monitor listed twice":   withField(good, "items", []map[string]any{statusItem(web, "A", "one"), statusItem(web, "B", "two")}),
			"too many items":         withField(good, "items", many),
		}
		for name, payload := range cases {
			resp, body := write(http.MethodPost, pagesURL, payload)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("%s: status %d, body %v", name, resp.StatusCode, body)
			}
		}
		if calls := cache.take(); len(calls) != 0 {
			t.Fatalf("invalid requests must not touch the cache: %v", calls)
		}
		resp, body := read(pagesURL)
		requireStatus(t, resp, http.StatusOK, body)
		for _, p := range asList(t, body, "pages") {
			if p["slug"] == "valid-slug" || p["slug"] == "Bad" {
				t.Fatalf("an invalid request created a page: %v", p)
			}
		}
		// Same table against update.
		pid := createPage("validate-update", statusItem(web, "", ""))
		for name, payload := range cases {
			resp, body := write(http.MethodPut, pageURL(pid), payload)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("update %s: status %d, body %v", name, resp.StatusCode, body)
			}
		}
		resp, body = read(pageURL(pid))
		requireStatus(t, resp, http.StatusOK, body)
		if body["slug"] != "validate-update" || len(asList(t, body, "items")) != 1 {
			t.Fatalf("failed updates changed the page: %v", body)
		}
		if calls := cache.take(); len(calls) != 0 {
			t.Fatalf("invalid updates must not touch the cache: %v", calls)
		}
		resp, body = write(http.MethodDelete, pageURL(pid), nil)
		requireStatus(t, resp, http.StatusNoContent, body)
		cache.take()
	})

	var acmeID int64
	t.Run("create", func(t *testing.T) {
		resp, body := write(http.MethodPost, pagesURL, map[string]any{
			"slug": "acme", "title": "  Acme Status ", "description": " All our systems ", "logo_url": "https://example.com/logo.png",
			"theme": "dark", "published": true, "custom_domain": " Status.Acme.COM ",
			"items": []map[string]any{
				statusItem(db, "Backend", ""),
				statusItem(web, "", "Website"),
				statusItem(cdn, "Backend", "Edge network"),
			},
		})
		requireStatus(t, resp, http.StatusCreated, body)
		acmeID = int64(body["id"].(float64))
		if body["team_id"] != float64(teamID) || body["slug"] != "acme" || body["title"] != "Acme Status" || body["description"] != "All our systems" ||
			body["theme"] != "dark" || body["published"] != true || body["logo_url"] != "https://example.com/logo.png" ||
			body["custom_domain"] != "status.acme.com" || body["url"] != "https://status.acme.com" ||
			body["created_at"] == nil || body["updated_at"] == nil {
			t.Fatalf("created page = %v", body)
		}
		// Array order is the position; display names default to the monitor's name.
		if got := itemNames(t, body); !slices.Equal(got, []string{"Backend/postgres main", "/Website", "Backend/Edge network"}) {
			t.Fatalf("items = %v", got)
		}
		for i, it := range asList(t, body, "items") {
			if it["id"] == nil || it["monitor_id"] == nil {
				t.Fatalf("item %d = %v", i, it)
			}
		}
		wantCalls(t, "invalidate:acme", "reload")

		resp, body = do(t, viewer, http.MethodGet, pageURL(acmeID), "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		if got := itemNames(t, body); len(got) != 3 || body["slug"] != "acme" {
			t.Fatalf("get = %v", body)
		}
	})

	t.Run("defaults and the slug url", func(t *testing.T) {
		resp, body := write(http.MethodPost, pagesURL, map[string]any{"slug": "plain", "title": "Plain"})
		requireStatus(t, resp, http.StatusCreated, body)
		if body["theme"] != "auto" || body["published"] != false || body["custom_domain"] != nil || body["logo_url"] != "" ||
			body["description"] != "" || body["url"] != "http://upsera.test/status/plain" {
			t.Fatalf("defaults = %v", body)
		}
		if items, ok := body["items"].([]any); !ok || len(items) != 0 {
			t.Fatalf("items must be an empty list, got %#v", body["items"])
		}
		wantCalls(t, "invalidate:plain", "reload")
		resp, body = write(http.MethodPost, pagesURL, withField(statusPageReq("blank-domain"), "custom_domain", "  "))
		requireStatus(t, resp, http.StatusCreated, body)
		if body["custom_domain"] != nil {
			t.Fatalf("blank domain must be none: %v", body)
		}
		resp, body = write(http.MethodPost, pagesURL, withField(statusPageReq("null-domain"), "custom_domain", nil))
		requireStatus(t, resp, http.StatusCreated, body)
		cache.take()
	})

	t.Run("list is per team", func(t *testing.T) {
		resp, body := read(pagesURL)
		requireStatus(t, resp, http.StatusOK, body)
		slugs := map[string]bool{}
		for _, p := range asList(t, body, "pages") {
			slugs[p["slug"].(string)] = true
			if p["team_id"] != float64(teamID) {
				t.Fatalf("foreign page in list: %v", p)
			}
		}
		if !slugs["acme"] || !slugs["plain"] {
			t.Fatalf("slugs = %v", slugs)
		}
		resp, body = read(fmt.Sprintf("%s/api/teams/%d/status-pages", base, otherTeamID))
		requireStatus(t, resp, http.StatusOK, body)
		if pages := asList(t, body, "pages"); len(pages) != 0 {
			t.Fatalf("other team sees pages: %v", pages)
		}
	})

	t.Run("duplicates are conflicts that say what collided", func(t *testing.T) {
		resp, body := write(http.MethodPost, pagesURL, statusPageReq("acme"))
		requireStatus(t, resp, http.StatusConflict, body)
		if msg, _ := body["detail"].(string); !strings.Contains(msg, "slug") {
			t.Fatalf("message %q must name the slug", msg)
		}
		resp, body = write(http.MethodPost, pagesURL, withField(statusPageReq("another"), "custom_domain", "STATUS.acme.com"))
		requireStatus(t, resp, http.StatusConflict, body)
		if msg, _ := body["detail"].(string); !strings.Contains(msg, "domain") {
			t.Fatalf("message %q must name the domain", msg)
		}
		// The slug namespace is global: another team cannot take it either.
		resp, body = write(http.MethodPost, fmt.Sprintf("%s/api/teams/%d/status-pages", base, otherTeamID), statusPageReq("acme"))
		requireStatus(t, resp, http.StatusConflict, body)
		if calls := cache.take(); len(calls) != 0 {
			t.Fatalf("conflicts must not touch the cache: %v", calls)
		}
	})

	t.Run("a failing domain reload does not fail the request", func(t *testing.T) {
		cache.setReloadErr(errors.New("boom"))
		defer cache.setReloadErr(nil)
		resp, body := write(http.MethodPost, pagesURL, statusPageReq("reload-fails"))
		requireStatus(t, resp, http.StatusCreated, body)
		wantCalls(t, "invalidate:reload-fails", "reload")
	})

	t.Run("update replaces settings and items and invalidates old and new slug", func(t *testing.T) {
		resp, body := write(http.MethodPut, pageURL(acmeID), map[string]any{
			"slug": "acme-status", "title": "Acme", "theme": "light", "published": false, "custom_domain": nil,
			"items": []map[string]any{statusItem(cdn, "Only", ""), statusItem(web, "Only", "Site")},
		})
		requireStatus(t, resp, http.StatusOK, body)
		if body["slug"] != "acme-status" || body["theme"] != "light" || body["published"] != false || body["custom_domain"] != nil ||
			body["url"] != "http://upsera.test/status/acme-status" || body["description"] != "" || body["logo_url"] != "" {
			t.Fatalf("updated = %v", body)
		}
		if got := itemNames(t, body); !slices.Equal(got, []string{"Only/cdn", "Only/Site"}) {
			t.Fatalf("items = %v", got)
		}
		wantCalls(t, "invalidate:acme-status", "invalidate:acme", "reload")

		// Same slug: invalidated once.
		resp, body = write(http.MethodPut, pageURL(acmeID), statusPageReq("acme-status", statusItem(db, "", "DB")))
		requireStatus(t, resp, http.StatusOK, body)
		if got := itemNames(t, body); !slices.Equal(got, []string{"/DB"}) {
			t.Fatalf("items = %v", got)
		}
		wantCalls(t, "invalidate:acme-status", "reload")

		// The old slug is free again.
		resp, body = write(http.MethodPost, pagesURL, statusPageReq("acme"))
		requireStatus(t, resp, http.StatusCreated, body)
		cache.take()
	})

	t.Run("update conflicts and missing monitors change nothing", func(t *testing.T) {
		resp, body := write(http.MethodPut, pageURL(acmeID), statusPageReq("acme", statusItem(web, "", "")))
		requireStatus(t, resp, http.StatusConflict, body)
		resp, body = write(http.MethodPut, pageURL(acmeID), statusPageReq("acme-status", statusItem(foreign, "", "")))
		requireStatus(t, resp, http.StatusUnprocessableEntity, body)
		resp, body = read(pageURL(acmeID))
		requireStatus(t, resp, http.StatusOK, body)
		if got := itemNames(t, body); body["slug"] != "acme-status" || !slices.Equal(got, []string{"/DB"}) {
			t.Fatalf("page changed: %v", body)
		}
		if calls := cache.take(); len(calls) != 0 {
			t.Fatalf("failed updates must not touch the cache: %v", calls)
		}
	})

	t.Run("a page of another team is not found", func(t *testing.T) {
		otherPagesURL := fmt.Sprintf("%s/api/teams/%d/status-pages", base, otherTeamID)
		for _, c := range []struct {
			method string
			suffix string
			body   any
		}{
			{http.MethodGet, "", nil},
			{http.MethodPut, "", statusPageReq("acme-status")},
			{http.MethodDelete, "", nil},
			{http.MethodGet, "/incidents", nil},
			{http.MethodPost, "/incidents", map[string]any{"title": "x", "severity": "minor"}},
			{http.MethodGet, "/subscribers", nil},
			{http.MethodPost, "/subscribers", map[string]any{"url": "https://hooks.example.com/x"}},
		} {
			resp, body := do(t, admin, c.method, fmt.Sprintf("%s/%d%s", otherPagesURL, acmeID, c.suffix), csrf, "", c.body)
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s: status %d, body %v", c.method, c.suffix, resp.StatusCode, body)
			}
		}
		resp, body := read(pageURL(99999999))
		requireStatus(t, resp, http.StatusNotFound, body)
		if calls := cache.take(); len(calls) != 0 {
			t.Fatalf("not-found requests must not touch the cache: %v", calls)
		}
	})

	t.Run("delete", func(t *testing.T) {
		pid := createPage("to-delete", statusItem(web, "", ""))
		resp, body := write(http.MethodDelete, pageURL(pid), nil)
		requireStatus(t, resp, http.StatusNoContent, body)
		wantCalls(t, "invalidate:to-delete", "reload")
		resp, body = read(pageURL(pid))
		requireStatus(t, resp, http.StatusNotFound, body)
		resp, body = write(http.MethodDelete, pageURL(pid), nil)
		requireStatus(t, resp, http.StatusNotFound, body)
		wantCalls(t)
	})

	// ---- incidents and subscribers ----
	livePage := createPage("live", statusItem(web, "", "Website"))
	incidentsURL := pageURL(livePage) + "/incidents"
	subscribersURL := pageURL(livePage) + "/subscribers"
	deliveries := func(incidentID int64) int {
		t.Helper()
		var n int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM upsera.subscriber_deliveries WHERE incident_id = $1", incidentID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	const secretHook = "https://hooks.example.com/services/T000/B000/secret-token"

	t.Run("webhook subscribers", func(t *testing.T) {
		invalid := []string{"ftp://hooks.example.com/x", "not a url", "https://user:pw@hooks.example.com/x", "https:///path", "hooks.example.com/x",
			"javascript:alert(1)", "https://hooks.example.com/" + strings.Repeat("a", 500)}
		for _, u := range invalid {
			resp, body := write(http.MethodPost, subscribersURL, map[string]any{"url": u})
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("url %q: status %d, body %v", u, resp.StatusCode, body)
			}
		}
		resp, body := write(http.MethodPost, subscribersURL, map[string]any{"url": ""})
		requireStatus(t, resp, http.StatusUnprocessableEntity, body)

		resp, body = write(http.MethodPost, subscribersURL, map[string]any{"url": secretHook})
		requireStatus(t, resp, http.StatusCreated, body)
		if body["kind"] != "webhook" || body["confirmed"] != true || body["target"] != "https://hooks.example.com/…" || body["id"] == nil {
			t.Fatalf("created subscriber = %v", body)
		}
		if strings.Contains(fmt.Sprint(body), "secret-token") {
			t.Fatalf("response leaks the webhook secret: %v", body)
		}
		hookID := int64(body["id"].(float64))
		// The stored target is the full URL.
		subs, err := st.ListSubscribers(ctx, livePage)
		if err != nil || len(subs) != 1 || subs[0].Target != secretHook || !subs[0].Confirmed || subs[0].Kind != model.SubscriberWebhook {
			t.Fatalf("stored = %+v %v", subs, err)
		}
		// Duplicates (also by letter case) are conflicts.
		resp, body = write(http.MethodPost, subscribersURL, map[string]any{"url": secretHook})
		requireStatus(t, resp, http.StatusConflict, body)
		resp, body = write(http.MethodPost, subscribersURL, map[string]any{"url": strings.ToUpper(secretHook)})
		requireStatus(t, resp, http.StatusConflict, body)

		// Email subscribers come from the public page; one confirmed, one not.
		for _, e := range []struct {
			target    string
			confirmed bool
		}{{"reader@example.com", true}, {"pending@example.com", false}} {
			if _, _, err := st.CreateSubscriber(ctx, model.Subscriber{PageID: livePage, Kind: model.SubscriberEmail, Target: e.target, Confirmed: e.confirmed}); err != nil {
				t.Fatal(err)
			}
		}

		resp, body = do(t, viewer, http.MethodGet, subscribersURL, "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		list := asList(t, body, "subscribers")
		if len(list) != 3 {
			t.Fatalf("subscribers = %v", list)
		}
		if raw, _ := json.Marshal(body); strings.Contains(string(raw), "secret-token") || strings.Contains(string(raw), "services/T000") {
			t.Fatalf("list leaks the webhook secret: %s", raw)
		}
		byTarget := map[string]map[string]any{}
		for _, s := range list {
			byTarget[s["target"].(string)] = s
			if s["id"] == nil || s["created_at"] == nil {
				t.Fatalf("subscriber = %v", s)
			}
		}
		if s := byTarget["https://hooks.example.com/…"]; s == nil || s["kind"] != "webhook" || s["confirmed"] != true {
			t.Fatalf("webhook row = %v", s)
		}
		if s := byTarget["reader@example.com"]; s == nil || s["kind"] != "email" || s["confirmed"] != true {
			t.Fatalf("email row = %v", s)
		}
		if s := byTarget["pending@example.com"]; s == nil || s["confirmed"] != false {
			t.Fatalf("unconfirmed row = %v", s)
		}

		// Delete is page scoped.
		otherPage := createPage("other-page")
		resp, body = write(http.MethodDelete, fmt.Sprintf("%s/%d/subscribers/%d", pagesURL, otherPage, hookID), nil)
		requireStatus(t, resp, http.StatusNotFound, body)
		if _, err := st.GetSubscriber(ctx, hookID); err != nil {
			t.Fatalf("subscriber of another page was deleted: %v", err)
		}
		resp, body = write(http.MethodDelete, fmt.Sprintf("%s/%d", subscribersURL, 99999999), nil)
		requireStatus(t, resp, http.StatusNotFound, body)
		resp, body = write(http.MethodDelete, fmt.Sprintf("%s/%d", subscribersURL, hookID), nil)
		requireStatus(t, resp, http.StatusNoContent, body)
		resp, body = write(http.MethodDelete, fmt.Sprintf("%s/%d", subscribersURL, hookID), nil)
		requireStatus(t, resp, http.StatusNotFound, body)
		// Put it back for the incident tests.
		resp, body = write(http.MethodPost, subscribersURL, map[string]any{"url": secretHook})
		requireStatus(t, resp, http.StatusCreated, body)
		cache.take()
	})

	var incID int64
	t.Run("incident lifecycle", func(t *testing.T) {
		// Invalid input.
		for name, payload := range map[string]map[string]any{
			"blank title":    {"title": "  ", "severity": "minor"},
			"no severity":    {"title": "x"},
			"bad severity":   {"title": "x", "severity": "apocalyptic"},
			"bad status":     {"title": "x", "severity": "minor", "status": "exploded"},
			"long title":     {"title": strings.Repeat("t", 201), "severity": "minor"},
			"long body":      {"title": "x", "severity": "minor", "body": strings.Repeat("b", 2001)},
			"bad started_at": {"title": "x", "severity": "minor", "started_at": "yesterday"},
		} {
			resp, body := write(http.MethodPost, incidentsURL, payload)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("%s: status %d, body %v", name, resp.StatusCode, body)
			}
		}
		wantCalls(t)

		// A draft stays private: no deliveries.
		resp, body := write(http.MethodPost, incidentsURL, map[string]any{
			"title": " Database latency ", "severity": "major", "body": "Investigating slow queries", "draft": true,
		})
		requireStatus(t, resp, http.StatusCreated, body)
		incID = int64(body["id"].(float64))
		if body["title"] != "Database latency" || body["severity"] != "major" || body["status"] != "investigating" || body["draft"] != true ||
			body["auto"] != false || body["published_at"] != nil || body["resolved_at"] != nil || body["started_at"] == nil || body["page_id"] != float64(livePage) {
			t.Fatalf("draft = %v", body)
		}
		ups := asList(t, body, "updates")
		if len(ups) != 1 || ups[0]["body"] != "Investigating slow queries" || ups[0]["status"] != "investigating" {
			t.Fatalf("updates = %v", ups)
		}
		wantCalls(t, "invalidate:live")
		if n := deliveries(incID); n != 0 {
			t.Fatalf("a draft enqueued %d deliveries", n)
		}

		// Publishing notifies the confirmed subscribers once (webhook + confirmed email).
		resp, body = write(http.MethodPost, fmt.Sprintf("%s/%d/publish", incidentsURL, incID), nil)
		requireStatus(t, resp, http.StatusOK, body)
		if body["draft"] != false || body["published_at"] == nil {
			t.Fatalf("published = %v", body)
		}
		wantCalls(t, "invalidate:live")
		if n := deliveries(incID); n != 2 {
			t.Fatalf("publish enqueued %d deliveries, want 2", n)
		}
		resp, body = write(http.MethodPost, fmt.Sprintf("%s/%d/publish", incidentsURL, incID), nil)
		requireStatus(t, resp, http.StatusOK, body)
		if n := deliveries(incID); n != 2 {
			t.Fatalf("second publish enqueued again: %d", n)
		}
		cache.take()

		// Edit.
		resp, body = write(http.MethodPut, fmt.Sprintf("%s/%d", incidentsURL, incID), map[string]any{"title": "Database outage", "severity": "critical"})
		requireStatus(t, resp, http.StatusOK, body)
		if body["title"] != "Database outage" || body["severity"] != "critical" || body["status"] != "investigating" {
			t.Fatalf("edited = %v", body)
		}
		wantCalls(t, "invalidate:live")
		for name, payload := range map[string]map[string]any{
			"blank title": {"title": " ", "severity": "minor"},
			"bad":         {"title": "x", "severity": "meh"},
		} {
			resp, body := write(http.MethodPut, fmt.Sprintf("%s/%d", incidentsURL, incID), payload)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("edit %s: status %d, body %v", name, resp.StatusCode, body)
			}
		}

		// Timeline updates.
		for name, payload := range map[string]map[string]any{
			"empty body":   {"status": "identified", "body": ""},
			"blank body":   {"status": "identified", "body": "   "},
			"long body":    {"status": "identified", "body": strings.Repeat("b", 2001)},
			"bad status":   {"status": "fixed-ish", "body": "x"},
			"missing body": {"status": "identified"},
		} {
			resp, body := write(http.MethodPost, fmt.Sprintf("%s/%d/updates", incidentsURL, incID), payload)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("update %s: status %d, body %v", name, resp.StatusCode, body)
			}
		}
		wantCalls(t)
		resp, body = write(http.MethodPost, fmt.Sprintf("%s/%d/updates", incidentsURL, incID), map[string]any{"status": "identified", "body": "Found the slow query"})
		requireStatus(t, resp, http.StatusCreated, body)
		if body["status"] != "identified" || body["resolved_at"] != nil {
			t.Fatalf("after update = %v", body)
		}
		resp, body = write(http.MethodPost, fmt.Sprintf("%s/%d/updates", incidentsURL, incID), map[string]any{"status": "resolved", "body": "  Fixed.  "})
		requireStatus(t, resp, http.StatusCreated, body)
		if body["status"] != "resolved" || body["resolved_at"] == nil {
			t.Fatalf("after resolve = %v", body)
		}
		ups = asList(t, body, "updates")
		if len(ups) != 3 || ups[0]["body"] != "Investigating slow queries" || ups[1]["body"] != "Found the slow query" ||
			ups[2]["body"] != "Fixed." || ups[2]["status"] != "resolved" {
			t.Fatalf("timeline must be oldest first: %v", ups)
		}
		wantCalls(t, "invalidate:live", "invalidate:live")
		if n := deliveries(incID); n != 2+2+2 {
			t.Fatalf("deliveries = %d, want 6", n)
		}
		// Reopening clears resolved_at.
		resp, body = write(http.MethodPost, fmt.Sprintf("%s/%d/updates", incidentsURL, incID), map[string]any{"status": "monitoring", "body": "It is back"})
		requireStatus(t, resp, http.StatusCreated, body)
		if body["status"] != "monitoring" || body["resolved_at"] != nil {
			t.Fatalf("reopened = %v", body)
		}
		cache.take()
	})

	t.Run("incident creation variants and listing", func(t *testing.T) {
		// Published immediately by default, resolved from the start, explicit start time.
		resp, body := write(http.MethodPost, incidentsURL, map[string]any{
			"title": "Past maintenance overrun", "severity": "minor", "status": "resolved", "body": "Done", "started_at": "2026-01-02T03:04:05Z",
		})
		requireStatus(t, resp, http.StatusCreated, body)
		pastID := int64(body["id"].(float64))
		if body["draft"] != false || body["published_at"] == nil || body["status"] != "resolved" || body["resolved_at"] == nil ||
			!sameInstant(body["started_at"], "2026-01-02T03:04:05Z") || !sameInstant(body["resolved_at"], "2026-01-02T03:04:05Z") {
			t.Fatalf("published incident = %v", body)
		}
		if n := deliveries(pastID); n != 2 {
			t.Fatalf("immediate publish enqueued %d deliveries, want 2", n)
		}
		wantCalls(t, "invalidate:live")

		// Without a first body there is an empty timeline.
		resp, body = write(http.MethodPost, incidentsURL, map[string]any{"title": "Quiet", "severity": "minor", "draft": true})
		requireStatus(t, resp, http.StatusCreated, body)
		if ups, ok := body["updates"].([]any); !ok || len(ups) != 0 {
			t.Fatalf("updates must be an empty list: %#v", body["updates"])
		}
		quietID := int64(body["id"].(float64))
		cache.take()

		// An auto-drafted incident (from an alert) is flagged.
		alertID := uuid.Must(uuid.NewV7()).String()
		now := time.Now().UTC().Truncate(time.Microsecond)
		if err := st.UpsertAlerts(ctx, []model.Alert{{ID: alertID, TeamID: teamID, MonitorID: web, IncidentStart: now, OpenedAt: now, Step: -1}}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateIncident(ctx, model.Incident{PageID: livePage, Title: "Website is down", Severity: model.SeverityMajor,
			Status: model.IncidentInvestigating, Draft: true, AutoAlertID: &alertID, StartedAt: now}, ""); err != nil {
			t.Fatal(err)
		}

		// The list includes drafts, newest first, with timelines.
		resp, body = do(t, viewer, http.MethodGet, incidentsURL, "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		list := asList(t, body, "incidents")
		var titles []string
		for _, in := range list {
			titles = append(titles, in["title"].(string))
		}
		if !slices.Equal(titles, []string{"Website is down", "Quiet", "Database outage", "Past maintenance overrun"}) {
			t.Fatalf("incident list = %v", titles)
		}
		if list[0]["auto"] != true || list[0]["draft"] != true || list[1]["auto"] != false {
			t.Fatalf("flags = %v / %v", list[0], list[1])
		}
		if ups := asList(t, list[2], "updates"); len(ups) != 4 || ups[3]["body"] != "It is back" {
			t.Fatalf("timeline in list = %v", ups)
		}

		// Incidents of another page are not reachable through this one.
		other := createPage("elsewhere")
		for _, c := range []struct {
			method, suffix string
			payload        any
		}{
			{http.MethodPut, "", map[string]any{"title": "x", "severity": "minor"}},
			{http.MethodPost, "/updates", map[string]any{"status": "resolved", "body": "x"}},
			{http.MethodPost, "/publish", nil},
			{http.MethodDelete, "", nil},
		} {
			resp, body := write(c.method, fmt.Sprintf("%s/%d/incidents/%d%s", pagesURL, other, quietID, c.suffix), c.payload)
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s on a foreign page: status %d, body %v", c.method, c.suffix, resp.StatusCode, body)
			}
			resp, body = write(c.method, fmt.Sprintf("%s/%d%s", incidentsURL, 99999999, c.suffix), c.payload)
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s on a missing incident: status %d, body %v", c.method, c.suffix, resp.StatusCode, body)
			}
		}
		wantCalls(t)

		// Delete removes the incident and its queued deliveries.
		resp, body = write(http.MethodDelete, fmt.Sprintf("%s/%d", incidentsURL, incID), nil)
		requireStatus(t, resp, http.StatusNoContent, body)
		wantCalls(t, "invalidate:live")
		if n := deliveries(incID); n != 0 {
			t.Fatalf("deliveries left after delete: %d", n)
		}
		resp, body = write(http.MethodDelete, fmt.Sprintf("%s/%d", incidentsURL, incID), nil)
		requireStatus(t, resp, http.StatusNotFound, body)
		resp, body = read(incidentsURL)
		requireStatus(t, resp, http.StatusOK, body)
		if got := asList(t, body, "incidents"); len(got) != 3 {
			t.Fatalf("incidents after delete = %d", len(got))
		}
		cache.take()
	})

	t.Run("an incident on an unpublished page notifies nobody", func(t *testing.T) {
		pid := createPage("not-live", statusItem(web, "", ""))
		resp, body := write(http.MethodPost, pageURL(pid)+"/subscribers", map[string]any{"url": "https://hooks.example.com/other"})
		requireStatus(t, resp, http.StatusCreated, body)
		resp, body = write(http.MethodPut, pageURL(pid), withField(statusPageReq("not-live", statusItem(web, "", "")), "published", false))
		requireStatus(t, resp, http.StatusOK, body)
		resp, body = write(http.MethodPost, pageURL(pid)+"/incidents", map[string]any{"title": "x", "severity": "minor"})
		requireStatus(t, resp, http.StatusCreated, body)
		if n := deliveries(int64(body["id"].(float64))); n != 0 {
			t.Fatalf("deliveries = %d", n)
		}
		cache.take()
	})

	t.Run("audit entries exist and hold no secrets", func(t *testing.T) {
		resp, body := read(fmt.Sprintf("%s/api/teams/%d/audit?limit=200", base, teamID))
		requireStatus(t, resp, http.StatusOK, body)
		seen := map[string]bool{}
		for _, e := range asList(t, body, "entries") {
			seen[e["action"].(string)] = true
		}
		for _, want := range []string{
			"status_page.create", "status_page.update", "status_page.delete",
			"incident.create", "incident.update", "incident.add_update", "incident.publish", "incident.delete",
			"subscriber.create", "subscriber.delete",
		} {
			if !seen[want] {
				t.Errorf("no %s audit entry", want)
			}
		}
		raw, _ := json.Marshal(body)
		for _, secret := range []string{"secret-token", "services/T000", "hooks.example.com", "reader@example.com", "pending@example.com"} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("audit log leaks %q", secret)
			}
		}
		// Titles and ids are recorded.
		if !strings.Contains(string(raw), "Database outage") || !strings.Contains(string(raw), "acme-status") {
			t.Errorf("audit details lack the incident title or page slug: %s", raw)
		}
	})
}

func TestOpenAPIStatusPages(t *testing.T) {
	raw, err := api.OpenAPISpec("test")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	enumOf := func(schema, field string) []string {
		t.Helper()
		sch, ok := schemas[schema].(map[string]any)
		if !ok {
			t.Fatalf("no schema %q", schema)
		}
		prop, ok := sch["properties"].(map[string]any)[field].(map[string]any)
		if !ok {
			t.Fatalf("no property %s.%s", schema, field)
		}
		rawEnum, ok := prop["enum"].([]any)
		if !ok {
			t.Fatalf("%s.%s has no enum", schema, field)
		}
		out := make([]string, len(rawEnum))
		for i, v := range rawEnum {
			out[i] = v.(string)
		}
		return out
	}
	themes := []string{"auto", "light", "dark"}
	severities := []string{"minor", "major", "critical"}
	statuses := []string{"investigating", "identified", "monitoring", "resolved"}
	for _, c := range []struct {
		schema, field string
		want          []string
	}{
		{"StatusPageBody", "theme", themes}, {"StatusPageRequestBody", "theme", themes},
		{"IncidentBody", "severity", severities}, {"IncidentBody", "status", statuses},
		{"IncidentRequestBody", "severity", severities}, {"IncidentRequestBody", "status", statuses},
		{"IncidentEditRequestBody", "severity", severities},
		{"IncidentUpdateBody", "status", statuses}, {"IncidentUpdateRequestBody", "status", statuses},
		{"SubscriberBody", "kind", []string{"email", "webhook"}},
	} {
		if got := enumOf(c.schema, c.field); !slices.Equal(got, c.want) {
			t.Errorf("%s.%s enum = %v, want %v", c.schema, c.field, got, c.want)
		}
	}
	for _, name := range []string{"StatusPageBody", "StatusPageRequestBody", "StatusPageItemBody", "StatusPageItemRequest",
		"IncidentBody", "IncidentUpdateBody", "IncidentRequestBody", "IncidentUpdateRequestBody", "SubscriberBody", "WebhookSubscriberRequestBody"} {
		if _, ok := schemas[name]; !ok {
			t.Errorf("schema %s missing from the OpenAPI document", name)
		}
	}
	paths := spec["paths"].(map[string]any)
	for path, methods := range map[string][]string{
		"/api/teams/{teamID}/status-pages":                                         {"get", "post"},
		"/api/teams/{teamID}/status-pages/{pageID}":                                {"get", "put", "delete"},
		"/api/teams/{teamID}/status-pages/{pageID}/incidents":                      {"get", "post"},
		"/api/teams/{teamID}/status-pages/{pageID}/incidents/{incidentID}":         {"put", "delete"},
		"/api/teams/{teamID}/status-pages/{pageID}/incidents/{incidentID}/updates": {"post"},
		"/api/teams/{teamID}/status-pages/{pageID}/incidents/{incidentID}/publish": {"post"},
		"/api/teams/{teamID}/status-pages/{pageID}/subscribers":                    {"get", "post"},
		"/api/teams/{teamID}/status-pages/{pageID}/subscribers/{subscriberID}":     {"delete"},
	} {
		p, ok := paths[path].(map[string]any)
		if !ok {
			t.Errorf("path %s missing", path)
			continue
		}
		for _, m := range methods {
			if _, ok := p[m]; !ok {
				t.Errorf("%s %s missing", strings.ToUpper(m), path)
			}
		}
	}
}

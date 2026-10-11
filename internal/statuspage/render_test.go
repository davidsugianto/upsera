package statuspage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/store"
)

// renderTestPage is a published page with three items in two sections.
func renderTestPage() model.StatusPage {
	return model.StatusPage{
		ID: 7, TeamID: 1, Slug: "acme", Title: "Acme Status", Description: "Live status of Acme",
		Theme: model.ThemeAuto, Published: true,
		Items: []model.StatusPageItem{
			{ID: 1, Section: "Core", MonitorID: 101, DisplayName: "Public API", Position: 0},
			{ID: 2, Section: "Core", MonitorID: 102, DisplayName: "Website", Position: 1},
			{ID: 3, Section: "Extras", MonitorID: 103, DisplayName: "Docs", Position: 2},
		},
	}
}

// renderTestStore serves page under its slug; every other slug is not found.
func renderTestStore(page model.StatusPage) *fakeStore {
	return &fakeStore{GetPublishedPageBySlugFn: func(_ context.Context, slug string) (model.StatusPage, error) {
		if slug == page.Slug {
			return page, nil
		}
		return model.StatusPage{}, store.ErrNotFound
	}}
}

// renderTestGet performs one request against h and returns the recorder.
func renderTestGet(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func renderTestStates(st ...model.Status) fakeStates {
	m := fakeStates{}
	for i, s := range st {
		m[int64(101+i)] = model.MonitorState{MonitorID: int64(101 + i), Status: s}
	}
	return m
}

var renderTestCellRE = regexp.MustCompile(`<span class="cell (c-[a-z]+)" title="([^"]+)"></span>`)

func TestRenderBanner(t *testing.T) {
	up, down, pending, maint := model.StatusUp, model.StatusDown, model.StatusPending, model.StatusMaintenance
	tests := []struct {
		name   string
		states fakeStates
		want   string
		class  string
	}{
		{"all up", renderTestStates(up, up, up), "All systems operational", "b-up"},
		{"pending is operational", renderTestStates(up, pending, pending), "All systems operational", "b-up"},
		{"one down", renderTestStates(up, down, up), "Partial outage", "b-warn"},
		{"all down", renderTestStates(down, down, down), "Major outage", "b-down"},
		{"maintenance", renderTestStates(up, maint, up), "Under maintenance", "b-maint"},
		{"down beats maintenance", renderTestStates(maint, down, up), "Partial outage", "b-warn"},
		{"no states", fakeStates{}, "Status unknown", "b-none"},
		{"one known state", fakeStates{103: {MonitorID: 103, Status: up}}, "All systems operational", "b-up"},
		{"down with unknown others", fakeStates{101: {Status: down}}, "Partial outage", "b-warn"},
	}
	all := []string{"All systems operational", "Partial outage", "Major outage", "Under maintenance", "Status unknown"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(t, renderTestStore(renderTestPage()), tt.states, nil)
			rec := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, `class="banner `+tt.class+`" role="status"`) {
				t.Errorf("banner class %s with role=status missing", tt.class)
			}
			for _, label := range all {
				if got := strings.Contains(body, ">"+label+"<"); got != (label == tt.want) {
					t.Errorf("label %q present = %v, want %v", label, got, label == tt.want)
				}
			}
		})
	}
}

func TestRenderUptimeBar(t *testing.T) {
	page := renderTestPage()
	page.Items = page.Items[:1]
	today := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	from := today.AddDate(0, 0, -89)
	day := func(back, checks, up int) model.UptimeDay {
		return model.UptimeDay{MonitorID: 101, Day: today.AddDate(0, 0, -back), Checks: checks, Up: up}
	}
	var gotIDs []int64
	var gotFrom, gotToday time.Time
	st := renderTestStore(page)
	st.StatusPageUptimeFn = func(_ context.Context, ids []int64, f, td time.Time) (map[int64][]model.UptimeDay, error) {
		gotIDs, gotFrom, gotToday = ids, f, td
		return map[int64][]model.UptimeDay{101: {
			day(89, 2000, 2000), // oldest cell: perfect
			day(10, 2000, 1999), // 99.95% -> green
			day(5, 1000, 990),   // 99.00% -> amber
			day(3, 1000, 949),   // 94.90% -> red
			day(2, 100, 95),     // 95.00% -> amber boundary
			day(0, 100, 100),    // today
		}}, nil
	}
	svc := newTestService(t, st, renderTestStates(model.StatusUp), nil)
	body := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme").Body.String()

	if len(gotIDs) != 1 || gotIDs[0] != 101 || !gotFrom.Equal(from) || !gotToday.Equal(today) {
		t.Errorf("uptime args = %v %v %v, want [101] %v %v", gotIDs, gotFrom, gotToday, from, today)
	}
	cells := renderTestCellRE.FindAllStringSubmatch(body, -1)
	if len(cells) != 90 {
		t.Fatalf("cells = %d, want 90", len(cells))
	}
	want := map[int][2]string{ // index -> class, title
		0:  {"c-up", "2026-07-13: 100.00%"},
		1:  {"c-none", "2026-07-14: no data"},
		79: {"c-up", "2026-09-30: 99.95%"},
		84: {"c-warn", "2026-10-05: 99.00%"},
		86: {"c-down", "2026-10-07: 94.90%"},
		87: {"c-warn", "2026-10-08: 95.00%"},
		88: {"c-none", "2026-10-09: no data"},
		89: {"c-up", "2026-10-10: 100.00%"},
	}
	for i, w := range want {
		if cells[i][1] != w[0] || cells[i][2] != w[1] {
			t.Errorf("cell %d = %s %q, want %s %q", i, cells[i][1], cells[i][2], w[0], w[1])
		}
	}
	// Window totals: 6133 up of 6200 checks.
	checks, up := 2000+2000+1000+1000+100+100, 2000+1999+990+949+95+100
	if w := pagePercent(up, checks) + " uptime"; !strings.Contains(body, w) {
		t.Errorf("window uptime %q missing", w)
	}
	if !strings.Contains(body, "Uptime over the last 90 days: 98.91%") {
		t.Errorf("sr-only summary missing:\n%s", body)
	}
}

func TestRenderUptimeNoData(t *testing.T) {
	page := renderTestPage()
	page.Items = page.Items[:1]
	svc := newTestService(t, renderTestStore(page), fakeStates{}, nil)
	body := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme").Body.String()
	cells := renderTestCellRE.FindAllStringSubmatch(body, -1)
	if len(cells) != 90 {
		t.Fatalf("cells = %d, want 90", len(cells))
	}
	for i, c := range cells {
		if c[1] != "c-none" || !strings.HasSuffix(c[2], ": no data") {
			t.Fatalf("cell %d = %v, want grey no data", i, c)
		}
	}
	for _, w := range []string{"<span>- uptime</span>", ">No data<", "No uptime data"} {
		if !strings.Contains(body, w) {
			t.Errorf("%q missing", w)
		}
	}
}

func TestRenderPercent(t *testing.T) {
	for _, tt := range []struct {
		up, checks int
		want       string
	}{
		{100, 100, "100.00%"}, {99999, 100000, "99.99%"}, {1, 3, "33.33%"}, {0, 10, "0.00%"}, {1999, 2000, "99.95%"},
	} {
		if got := pagePercent(tt.up, tt.checks); got != tt.want {
			t.Errorf("pagePercent(%d, %d) = %s, want %s", tt.up, tt.checks, got, tt.want)
		}
	}
}

func TestRenderSections(t *testing.T) {
	page := model.StatusPage{
		ID: 7, Slug: "acme", Title: "T", Theme: model.ThemeLight, Published: true,
		Items: []model.StatusPageItem{
			{Section: "Zeta", MonitorID: 1, DisplayName: "first", Position: 0},
			{Section: "", MonitorID: 2, DisplayName: "loose", Position: 1},
			{Section: "Alpha", MonitorID: 3, DisplayName: "third", Position: 2},
			{Section: "Zeta", MonitorID: 4, DisplayName: "fourth", Position: 3},
		},
	}
	svc := newTestService(t, renderTestStore(page), fakeStates{}, nil)
	body := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme").Body.String()
	var order []string
	for _, w := range []string{">Zeta<", ">first<", ">fourth<", ">loose<", ">Alpha<", ">third<"} {
		order = append(order, w)
		if !strings.Contains(body, w) {
			t.Fatalf("%s missing", w)
		}
	}
	// Zeta (first, fourth) comes first, then the unnamed section, then Alpha.
	pos := func(s string) int { return strings.Index(body, s) }
	if !(pos(">Zeta<") < pos(">first<") && pos(">first<") < pos(">fourth<") && pos(">fourth<") < pos(">loose<") && pos(">loose<") < pos(">Alpha<") && pos(">Alpha<") < pos(">third<")) {
		t.Errorf("sections out of order: %v", order)
	}
	if strings.Count(body, "<h2 id=\"sec-") != 2 {
		t.Errorf("want 2 section headings (the unnamed section has none), got %d", strings.Count(body, "<h2 id=\"sec-"))
	}
}

func TestRenderIncidents(t *testing.T) {
	now := testNow
	ago := func(d time.Duration) *time.Time { return new(now.Add(-d)) }
	st := renderTestStore(renderTestPage())
	var gotDrafts bool
	var gotLimit int
	st.ListIncidentsFn = func(_ context.Context, pageID int64, drafts bool, limit int) ([]model.Incident, error) {
		gotDrafts, gotLimit = drafts, limit
		return []model.Incident{
			{ID: 1, PageID: pageID, Title: "Active outage", Severity: model.SeverityMajor, Status: model.IncidentIdentified,
				StartedAt: now.Add(-time.Hour), PublishedAt: ago(time.Hour), Updates: []model.IncidentUpdate{
					{Status: model.IncidentInvestigating, Body: "first look", CreatedAt: now.Add(-50 * time.Minute)},
					{Status: model.IncidentIdentified, Body: "found the cause", CreatedAt: now.Add(-10 * time.Minute)},
				}},
			{ID: 2, PageID: pageID, Title: "Recent fix", Severity: model.SeverityMinor, Status: model.IncidentResolved,
				StartedAt: now.Add(-48 * time.Hour), ResolvedAt: ago(47 * time.Hour), PublishedAt: ago(48 * time.Hour),
				Updates: []model.IncidentUpdate{{Status: model.IncidentResolved, Body: "all good now", CreatedAt: now.Add(-47 * time.Hour)}}},
			{ID: 3, PageID: pageID, Title: "Ancient history", Severity: model.SeverityMinor, Status: model.IncidentResolved,
				StartedAt: now.Add(-30 * 24 * time.Hour), ResolvedAt: ago(29 * 24 * time.Hour), PublishedAt: ago(30 * 24 * time.Hour)},
			{ID: 4, PageID: pageID, Title: "Secret draft", Draft: true, Status: model.IncidentInvestigating, StartedAt: now},
		}, nil
	}
	svc := newTestService(t, st, fakeStates{}, nil)
	body := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme").Body.String()

	if gotDrafts || gotLimit != 50 {
		t.Errorf("ListIncidents(drafts=%v, limit=%d), want false, 50", gotDrafts, gotLimit)
	}
	active, past, ok := strings.Cut(body, `id="past-h"`)
	if !ok {
		t.Fatal("past incidents section missing")
	}
	for _, w := range []string{"Active incident", "Active outage", `id="incident-1"`, "found the cause", "first look"} {
		if !strings.Contains(active, w) {
			t.Errorf("active part misses %q", w)
		}
	}
	if strings.Index(active, "found the cause") > strings.Index(active, "first look") {
		t.Error("timeline must list the newest update first")
	}
	for _, w := range []string{"Recent fix", `id="incident-2"`, "all good now"} {
		if !strings.Contains(past, w) {
			t.Errorf("past part misses %q", w)
		}
	}
	for _, w := range []string{"Ancient history", "Secret draft"} {
		if strings.Contains(body, w) {
			t.Errorf("%q must not be shown", w)
		}
	}
	if strings.Contains(active, "Recent fix") || strings.Contains(past, "Active outage") {
		t.Error("incident shown in the wrong section")
	}
}

func TestRenderNoActiveIncidents(t *testing.T) {
	svc := newTestService(t, renderTestStore(renderTestPage()), fakeStates{}, nil)
	body := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme").Body.String()
	if strings.Contains(body, "Active incidents") {
		t.Error("active incidents heading shown without incidents")
	}
	if !strings.Contains(body, "No incidents in the last 14 days") {
		t.Error("empty past incidents note missing")
	}
}

func TestRenderPrivacyAndEscaping(t *testing.T) {
	const xss = `<script>alert(1)</script>`
	page := renderTestPage()
	page.Title = "T " + xss
	page.Description = "D " + xss
	page.Items = []model.StatusPageItem{
		{Section: "S " + xss, MonitorID: 987654, DisplayName: "Public API", Position: 0},
		{Section: "S " + xss, MonitorID: 987655, DisplayName: "N " + xss, Position: 1},
	}
	st := renderTestStore(page)
	st.ListIncidentsFn = func(_ context.Context, pageID int64, _ bool, _ int) ([]model.Incident, error) {
		return []model.Incident{{ID: 5, PageID: pageID, Title: "I " + xss, Severity: model.SeverityMinor, Status: model.IncidentMonitoring,
			StartedAt: testNow, Updates: []model.IncidentUpdate{{Status: model.IncidentMonitoring, Body: "B " + xss, CreatedAt: testNow}}}}, nil
	}
	states := fakeStates{987654: {Status: model.StatusUp, MonitorID: 987654}, 987655: {Status: model.StatusDown, MonitorID: 987655}}
	svc := newTestService(t, st, states, nil)
	body := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme").Body.String()

	if strings.Contains(body, "<script") || strings.Contains(body, xss) {
		t.Error("unescaped <script> in output")
	}
	if n := strings.Count(body, "&lt;script&gt;alert(1)&lt;/script&gt;"); n < 6 {
		t.Errorf("escaped payload found %d times, want >= 6 (title x2-3, description, section, name, incident title, update)", n)
	}
	for _, leak := range []string{"987654", "987655", "monitor"} {
		if strings.Contains(body, leak) {
			t.Errorf("output leaks %q", leak)
		}
	}
	if strings.Contains(body, "javascript:") || strings.Contains(body, "<script") {
		t.Error("page must not contain scripts")
	}
}

func TestRenderLogo(t *testing.T) {
	for _, tt := range []struct {
		logo string
		want bool
	}{
		{"https://cdn.example.com/logo.png", true},
		{"HTTPS://cdn.example.com/logo.png", true},
		{"http://cdn.example.com/logo.png", false},
		{"javascript:alert(1)", false},
		{"data:image/png;base64,AAAA", false},
		{"//cdn.example.com/logo.png", false},
		{"", false},
	} {
		t.Run(tt.logo, func(t *testing.T) {
			page := renderTestPage()
			page.LogoURL = tt.logo
			svc := newTestService(t, renderTestStore(page), fakeStates{}, nil)
			body := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme").Body.String()
			if got := strings.Contains(body, `<img class="logo"`); got != tt.want {
				t.Errorf("logo shown = %v, want %v", got, tt.want)
			}
			if tt.want && !strings.Contains(body, `src="`+tt.logo+`"`) {
				t.Error("logo src missing")
			}
		})
	}
}

func TestRenderThemeAndStructure(t *testing.T) {
	for _, th := range []model.Theme{model.ThemeAuto, model.ThemeLight, model.ThemeDark, "bogus"} {
		page := renderTestPage()
		page.Theme = th
		svc := newTestService(t, renderTestStore(page), fakeStates{}, nil)
		body := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme").Body.String()
		want := string(th)
		if !th.Valid() {
			want = "auto"
		}
		if !strings.Contains(body, `<html lang="en" data-theme="`+want+`">`) {
			t.Errorf("theme %q: data-theme %q missing", th, want)
		}
	}
	svc := newTestService(t, renderTestStore(renderTestPage()), fakeStates{}, nil)
	body := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme").Body.String()
	for _, w := range []string{
		`<meta name="viewport"`, "<style>", "prefers-color-scheme", "<h1>Acme Status</h1>",
		"Powered by Upsera", `<a href="/status/acme/feed.xml">RSS feed</a>`,
		`<form class="subscribe" method="post" action="/status/acme/subscribe">`,
		`<input id="email" type="email" name="email"`,
		`name="website" tabindex="-1" autocomplete="off"`,
	} {
		if !strings.Contains(body, w) {
			t.Errorf("page misses %q", w)
		}
	}
	if strings.Contains(body, "<script") || regexp.MustCompile(`\son[a-z]+=`).MatchString(body) {
		t.Error("page contains script or inline handlers")
	}
}

func TestRenderResponseHeaders(t *testing.T) {
	svc := newTestService(t, renderTestStore(renderTestPage()), fakeStates{}, nil)
	for _, target := range []string{"/status/acme", "/status/acme/feed.xml"} {
		rec := renderTestGet(svc.Handler(), http.MethodGet, target)
		for k, v := range map[string]string{
			"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; img-src https: data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'",
			"X-Content-Type-Options":  "nosniff",
			// same-origin, not no-referrer: a browser form POST from a
			// no-referrer page sends "Origin: null", which the subscribe
			// handler (rightly) refuses.
			"Referrer-Policy": "same-origin",
			"Cache-Control":   "public, max-age=30",
		} {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s %s = %q, want %q", target, k, got, v)
			}
		}
	}
	if ct := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme").Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestRenderStoreErrorIsNotCached(t *testing.T) {
	var calls atomic.Int32
	st := renderTestStore(renderTestPage())
	st.ListIncidentsFn = func(context.Context, int64, bool, int) ([]model.Incident, error) {
		if calls.Add(1) == 1 {
			return nil, context.DeadlineExceeded
		}
		return nil, nil
	}
	svc := newTestService(t, st, fakeStates{}, nil)
	rec := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme")
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "deadline") {
		t.Fatalf("first = %d %q, want a generic 500", rec.Code, rec.Body)
	}
	if rec := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme"); rec.Code != http.StatusOK {
		t.Errorf("second = %d, want 200 (errors are not cached)", rec.Code)
	}
}

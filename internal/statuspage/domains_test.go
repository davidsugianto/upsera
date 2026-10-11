package statuspage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/davidsugianto/upsera/internal/model"
)

func newDomainTestRequest(method, host, path string) *http.Request {
	req := httptest.NewRequest(method, "http://"+host+path, nil)
	req.Host = host
	return req
}

func serveDomainTest(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// domainTestService has a page "acme" on status.acme.test and a store that
// lists it as a published custom domain.
func domainTestService(t *testing.T) *Service {
	t.Helper()
	page := renderTestPage()
	page.CustomDomain = new("status.acme.test")
	st := renderTestStore(page)
	st.ListPublishedCustomDomainsFn = func(context.Context) ([]model.PageDomain, error) {
		return []model.PageDomain{{Domain: "Status.Acme.Test", Slug: "acme"}}, nil
	}
	svc := newTestService(t, st, fakeStates{}, nil)
	if err := svc.ReloadDomains(context.Background()); err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestDomainHostMiddleware(t *testing.T) {
	svc := domainTestService(t)
	var passed []string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		passed = append(passed, r.Host+r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	})
	h := svc.HostMiddleware(next)

	t.Run("root serves the page with root-relative links", func(t *testing.T) {
		for _, host := range []string{"status.acme.test", "STATUS.acme.test:8443", "status.acme.test."} {
			rec := serveDomainTest(h, newDomainTestRequest(http.MethodGet, host, "/"))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status = %d", host, rec.Code)
			}
			body := rec.Body.String()
			for _, w := range []string{
				`action="/subscribe"`, `<a href="/feed.xml">RSS feed</a>`, `href="/feed.xml"`, "<h1>Acme Status</h1>",
			} {
				if !strings.Contains(body, w) {
					t.Errorf("%s: page misses %q", host, w)
				}
			}
			if strings.Contains(body, "/status/") {
				t.Errorf("%s: page still links under /status/", host)
			}
		}
	})
	t.Run("feed", func(t *testing.T) {
		rec := serveDomainTest(h, newDomainTestRequest(http.MethodGet, "status.acme.test", "/feed.xml"))
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/rss+xml") {
			t.Errorf("feed = %d %q", rec.Code, rec.Header().Get("Content-Type"))
		}
	})
	t.Run("other paths are 404", func(t *testing.T) {
		for _, p := range []string{"/api/tls-allowed", "/api/auth/login", "/assets/app.js", "/status/acme", "/status/other/feed.xml", "/feed.xml/", "/index.html", "//", "/robots.txt"} {
			rec := serveDomainTest(h, newDomainTestRequest(http.MethodGet, "status.acme.test", p))
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s = %d, want 404", p, rec.Code)
			}
		}
	})
	t.Run("unknown hosts pass through untouched", func(t *testing.T) {
		for _, host := range []string{"upsera.test", "evil.test", "acme.test", "x.status.acme.test"} {
			rec := serveDomainTest(h, newDomainTestRequest(http.MethodGet, host, "/api/monitors"))
			if rec.Code != http.StatusTeapot {
				t.Errorf("%s = %d, want passthrough", host, rec.Code)
			}
		}
		if len(passed) != 4 || passed[0] != "upsera.test/api/monitors" {
			t.Errorf("next saw %v", passed)
		}
	})
	t.Run("main host requests are left to next", func(t *testing.T) {
		body := serveDomainTest(h, newDomainTestRequest(http.MethodGet, "upsera.test", "/status/acme")).Code
		if body != http.StatusTeapot { // the middleware only claims custom domains
			t.Errorf("main host = %d", body)
		}
	})
}

func TestDomainReload(t *testing.T) {
	list := []model.PageDomain{{Domain: "a.test", Slug: "a"}}
	var fail bool
	st := &fakeStore{ListPublishedCustomDomainsFn: func(context.Context) ([]model.PageDomain, error) {
		if fail {
			return nil, errors.New("db down")
		}
		return list, nil
	}}
	svc := newTestService(t, st, fakeStates{}, nil)
	ctx := context.Background()
	allowed := func(d string) int {
		rec := httptest.NewRecorder()
		svc.TLSAllowed(rec, httptest.NewRequest(http.MethodGet, "/api/tls-allowed?domain="+d, nil))
		return rec.Code
	}
	if allowed("a.test") != http.StatusNotFound {
		t.Error("domains are served before the first reload")
	}
	if err := svc.ReloadDomains(ctx); err != nil || allowed("a.test") != http.StatusOK {
		t.Fatalf("after reload: err %v, allowed %d", err, allowed("a.test"))
	}
	list = []model.PageDomain{{Domain: "b.test", Slug: "b"}, {Domain: "", Slug: "empty"}}
	if err := svc.ReloadDomains(ctx); err != nil {
		t.Fatal(err)
	}
	if allowed("a.test") != http.StatusNotFound || allowed("b.test") != http.StatusOK {
		t.Error("reload did not replace the map")
	}
	fail = true
	if err := svc.ReloadDomains(ctx); err == nil {
		t.Error("store error not returned")
	}
	if allowed("b.test") != http.StatusOK {
		t.Error("a failed reload must keep the previous map")
	}
}

func TestTLSAllowed(t *testing.T) {
	svc := domainTestService(t)
	for _, tt := range []struct {
		name, method, target string
		want                 int
		body                 string
	}{
		{"known", http.MethodGet, "/api/tls-allowed?domain=status.acme.test", http.StatusOK, "ok"},
		{"case and space", http.MethodGet, "/api/tls-allowed?domain=+Status.ACME.test+", http.StatusOK, "ok"},
		{"unknown", http.MethodGet, "/api/tls-allowed?domain=evil.test", http.StatusNotFound, ""},
		{"subdomain of known", http.MethodGet, "/api/tls-allowed?domain=x.status.acme.test", http.StatusNotFound, ""},
		{"empty", http.MethodGet, "/api/tls-allowed?domain=", http.StatusBadRequest, ""},
		{"blank", http.MethodGet, "/api/tls-allowed?domain=%20", http.StatusBadRequest, ""},
		{"missing", http.MethodGet, "/api/tls-allowed", http.StatusBadRequest, ""},
		{"post", http.MethodPost, "/api/tls-allowed?domain=status.acme.test", http.StatusMethodNotAllowed, ""},
		{"head", http.MethodHead, "/api/tls-allowed?domain=status.acme.test", http.StatusMethodNotAllowed, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			svc.TLSAllowed(rec, httptest.NewRequest(tt.method, tt.target, nil))
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.body != "" && rec.Body.String() != tt.body {
				t.Errorf("body = %q, want %q", rec.Body, tt.body)
			}
		})
	}
}

func TestDomainNormalizeHost(t *testing.T) {
	for in, want := range map[string]string{
		"Status.Acme.Test": "status.acme.test", "status.acme.test:8443": "status.acme.test",
		"status.acme.test.": "status.acme.test", " a.test ": "a.test", "[::1]:8080": "::1", "": "",
	} {
		if got := normalizeHost(in); got != want {
			t.Errorf("normalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

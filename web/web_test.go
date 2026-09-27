package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func serve(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("%s %s: missing CSP, got %q", method, target, csp)
	}
	return rec
}

func TestHandlerServesBuiltApp(t *testing.T) {
	h := newHandler(fstest.MapFS{
		"dist/app/index.html":  {Data: []byte("<html>app</html>")},
		"dist/app/assets/a.js": {Data: []byte("console.log(1)")},
	})

	rec := serve(t, h, http.MethodGet, "/assets/a.js")
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log(1)" {
		t.Fatalf("asset: %d %q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("asset Cache-Control = %q, want immutable", cc)
	}

	for _, p := range []string{"/", "/t/1/monitors", "/index.html", "/assets/missing.js"} {
		rec = serve(t, h, http.MethodGet, p)
		if rec.Code != http.StatusOK || rec.Body.String() != "<html>app</html>" {
			t.Fatalf("%s: %d %q, want index.html", p, rec.Code, rec.Body.String())
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("%s: Cache-Control = %q, want no-cache", p, cc)
		}
	}

	for _, p := range []string{"/api", "/api/nope"} {
		rec = serve(t, h, http.MethodGet, p)
		if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Fatalf("%s: %d %s, want 404 problem+json", p, rec.Code, rec.Header().Get("Content-Type"))
		}
	}

	if rec = serve(t, h, http.MethodPost, "/"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /: %d, want 405", rec.Code)
	}
}

func TestHandlerWithoutBuild(t *testing.T) {
	h := newHandler(fstest.MapFS{"dist/README.md": {Data: []byte("placeholder")}})
	rec := serve(t, h, http.MethodGet, "/")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "not built") {
		t.Fatalf("GET /: %d %q, want the not-built page", rec.Code, rec.Body.String())
	}
}

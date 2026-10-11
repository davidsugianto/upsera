package statuspage

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
)

// domainMap maps custom domains (lower-case host names) to page slugs.
type domainMap struct {
	mu sync.RWMutex
	m  map[string]string
}

func newDomainMap() *domainMap { return &domainMap{m: make(map[string]string)} }

func (d *domainMap) lookup(host string) (slug string, ok bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	slug, ok = d.m[host]
	return slug, ok
}

func (d *domainMap) replace(m map[string]string) {
	d.mu.Lock()
	d.m = m
	d.mu.Unlock()
}

// customDomainKey marks requests that arrived on a page's custom domain, so
// the page links relative to the root instead of /status/<slug>.
type customDomainKey struct{}

func isCustomDomain(r *http.Request) bool {
	v, _ := r.Context().Value(customDomainKey{}).(bool)
	return v
}

// normalizeHost lower-cases a Host header value and strips the port and any
// trailing dot.
func normalizeHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// customDomainPaths are the only paths served on a custom domain, mapped to
// their suffix under /status/<slug>. Everything else is a 404 so the
// dashboard and API are never reachable there.
var customDomainPaths = map[string]string{
	"/":            "",
	"/feed.xml":    "/feed.xml",
	"/subscribe":   "/subscribe",
	"/confirm":     "/confirm",
	"/unsubscribe": "/unsubscribe",
}

// InvalidatePage drops the cached rendering of the page with this slug.
func (s *Service) InvalidatePage(slug string) { s.cache.invalidate(slug) }

// InvalidateAll drops every cached page.
func (s *Service) InvalidateAll() { s.cache.clear() }

// ReloadDomains reloads the custom-domain map from the store. On error the
// previous map stays in place.
func (s *Service) ReloadDomains(ctx context.Context) error {
	list, err := s.opts.Store.ListPublishedCustomDomains(ctx)
	if err != nil {
		return err
	}
	m := make(map[string]string, len(list))
	for _, d := range list {
		if host := normalizeHost(d.Domain); host != "" {
			m[host] = d.Slug
		}
	}
	s.domains.replace(m)
	return nil
}

// HostMiddleware serves a page when the request Host is its custom domain
// and calls next otherwise.
func (s *Service) HostMiddleware(next http.Handler) http.Handler {
	pages := s.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slug, ok := s.domains.lookup(normalizeHost(r.Host))
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		suffix, ok := customDomainPaths[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		r = r.Clone(context.WithValue(r.Context(), customDomainKey{}, true))
		r.URL.Path = "/status/" + slug + suffix
		r.URL.RawPath = ""
		pages.ServeHTTP(w, r)
	})
}

// TLSAllowed answers GET /api/tls-allowed?domain=: 200 only for the custom
// domain of a published page.
func (s *Service) TLSAllowed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	domain := normalizeHost(r.URL.Query().Get("domain"))
	if domain == "" {
		http.Error(w, "domain is required", http.StatusBadRequest)
		return
	}
	if _, ok := s.domains.lookup(domain); !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

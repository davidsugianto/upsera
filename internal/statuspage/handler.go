package statuspage

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/davidsugianto/upsera/internal/store"
)

// renderTimeout bounds one cache-miss render; it runs detached from the
// triggering request so one client hanging up cannot fail the requests
// waiting on the same render.
const renderTimeout = 10 * time.Second

// Handler serves /status/{slug}, /status/{slug}/feed.xml and, via
// mountSubscribe, the subscribe/confirm/unsubscribe routes.
func (s *Service) Handler() http.Handler {
	r := chi.NewRouter()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r.Method(method, "/status/{slug}", http.HandlerFunc(s.servePage))
		r.Method(method, "/status/{slug}/", http.HandlerFunc(s.servePage))
		r.Method(method, "/status/{slug}/feed.xml", http.HandlerFunc(s.serveFeed))
	}
	s.mountSubscribe(r)
	return r
}

func (s *Service) servePage(w http.ResponseWriter, r *http.Request) {
	custom := isCustomDomain(r)
	variant := variantPage
	if custom {
		variant = variantPageCustom
	}
	s.serveCached(w, r, variant, func(ctx context.Context, slug string) (cachedResponse, error) {
		return s.renderPage(ctx, slug, custom)
	})
}

func (s *Service) serveFeed(w http.ResponseWriter, r *http.Request) {
	variant := variantFeed
	if isCustomDomain(r) {
		variant = variantFeedCustom
	}
	s.serveCached(w, r, variant, s.renderFeed)
}

func (s *Service) serveCached(w http.ResponseWriter, r *http.Request, variant string, render func(ctx context.Context, slug string) (cachedResponse, error)) {
	slug := chi.URLParam(r, "slug")
	resp, err := s.cache.get(slug, variant, func() (cachedResponse, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), renderTimeout)
		defer cancel()
		return render(ctx, slug)
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.opts.Logger.Error("render status page", "slug", slug, "variant", variant, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", resp.contentType)
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src https: data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	// same-origin keeps the real Origin on the subscribe form's POST (under
	// no-referrer browsers send "Origin: null") while still sending no
	// referrer to other sites such as the logo host.
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Cache-Control", "public, max-age=30")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(resp.body)
}

package statuspage

import (
	"context"
	"crypto/sha256"
	"errors"
	"html/template"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/store"
)

const (
	subscribeBodyLimit = 4 << 10
	subscribeIPMax     = 5 // subscribe requests per client IP per window
	subscribeEmailMax  = 3 // subscribe requests per (page, email) per window
	subscribeWindow    = time.Hour
	// limiterMaxKeys bounds each limiter map. When a new key arrives and the
	// map is still full after sweeping expired keys, limiterEvict random keys
	// are dropped to make room: refusing instead would let a flood of made-up
	// addresses lock every real visitor out.
	limiterMaxKeys    = 50_000
	limiterEvict      = limiterMaxKeys / 100
	limiterSweepEvery = time.Minute
	// confirmCapPage and confirmCapGlobal cap the confirmation mails queued
	// per page and per instance in subscribeWindow, however many addresses
	// (or user+alias@ variants of one inbox) a client cycles through.
	confirmCapPage    = 60
	confirmCapGlobal  = 300
	subscribeAccepted = "If that address is valid, we have sent a confirmation email. Click the link in it to start receiving updates."
)

// subscribeLimiter rate-limits public subscribe requests in memory: a
// sliding window per client IP and per (page, lower-cased email). Memory is
// bounded: windows hold at most their limit in timestamps, expired keys are
// swept at most once a minute, and each map is capped at limiterMaxKeys.
type subscribeLimiter struct {
	now       func() time.Time
	mu        sync.Mutex
	ips       map[string][]time.Time
	emails    map[[sha256.Size]byte][]time.Time
	lastSweep time.Time
}

func newSubscribeLimiter(now func() time.Time) *subscribeLimiter {
	return &subscribeLimiter{
		now:    now,
		ips:    make(map[string][]time.Time),
		emails: make(map[[sha256.Size]byte][]time.Time),
	}
}

// admit records an attempt under k in m unless k already has limit attempts in
// the window; it then returns how long until the oldest one expires.
func admit[K comparable](m map[K][]time.Time, k K, limit int, now time.Time) (time.Duration, bool) {
	cutoff := now.Add(-subscribeWindow)
	ts := m[k]
	kept := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limit {
		m[k] = kept
		return kept[0].Add(subscribeWindow).Sub(now), false
	}
	if len(kept) == 0 {
		if _, known := m[k]; !known && len(m) >= limiterMaxKeys {
			evictKeys(m, limiterEvict)
		}
	}
	m[k] = append(kept, now)
	return 0, true
}

// evictKeys deletes n arbitrary keys from m (map iteration order is random).
func evictKeys[K comparable](m map[K][]time.Time, n int) {
	for k := range m {
		if n <= 0 {
			return
		}
		delete(m, k)
		n--
	}
}

// sweepWindows drops expired attempts and empty keys from m.
func sweepWindows[K comparable](m map[K][]time.Time, now time.Time) {
	cutoff := now.Add(-subscribeWindow)
	for k, ts := range m {
		kept := ts[:0]
		for _, t := range ts {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(m, k)
		} else {
			m[k] = kept
		}
	}
}

func (l *subscribeLimiter) maybeSweep(now time.Time) {
	if now.Sub(l.lastSweep) < limiterSweepEvery {
		return
	}
	l.lastSweep = now
	sweepWindows(l.ips, now)
	sweepWindows(l.emails, now)
}

// allowIP counts a subscribe request from ip. When refused it returns how
// long to wait.
func (l *subscribeLimiter) allowIP(ip string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.maybeSweep(now)
	return admit(l.ips, ip, subscribeIPMax, now)
}

// allowEmail counts a subscribe request for email on pageID, case-insensitively.
func (l *subscribeLimiter) allowEmail(pageID int64, email string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.maybeSweep(now)
	k := sha256.Sum256([]byte(strconv.FormatInt(pageID, 10) + "|" + strings.ToLower(email)))
	return admit(l.emails, k, subscribeEmailMax, now)
}

// size is the number of live keys (tests).
func (l *subscribeLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.ips) + len(l.emails)
}

// mountSubscribe adds the subscribe, confirm and unsubscribe routes to r
// (r is the router built by Handler, paths include the /status prefix). GET
// of confirm/unsubscribe only shows a button; the POST does the work, so link
// prefetchers in mail scanners change nothing.
func (s *Service) mountSubscribe(r chi.Router) {
	r.Post("/status/{slug}/subscribe", s.handleSubscribe)
	r.Get("/status/{slug}/confirm", s.handleLinkPage(tokenConfirm))
	r.Post("/status/{slug}/confirm", s.handleConfirm)
	r.Get("/status/{slug}/unsubscribe", s.handleLinkPage(tokenUnsubscribe))
	r.Post("/status/{slug}/unsubscribe", s.handleUnsubscribe)
}

// sameOrigin reports whether an Origin header names the request's own host.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// validEmail returns the address if in is a plain, ASCII addr-spec with a
// dotted domain, at most 254 characters, and no display name or comment.
func validEmail(in string) (string, bool) {
	in = strings.TrimSpace(in)
	if in == "" || len(in) > 254 {
		return "", false
	}
	for i := 0; i < len(in); i++ {
		if c := in[i]; c <= ' ' || c >= 0x7f {
			return "", false
		}
	}
	a, err := mail.ParseAddress(in)
	if err != nil || a.Address != in {
		return "", false
	}
	at := strings.LastIndexByte(in, '@')
	if at < 1 || len(in[:at]) > 64 {
		return "", false
	}
	domain := in[at+1:]
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") ||
		strings.Contains(domain, "..") || strings.HasPrefix(domain, "[") {
		return "", false
	}
	return in, true
}

// subscribePage loads the published page for the slug in the URL, or writes
// the error page and returns false.
func (s *Service) subscribePage(w http.ResponseWriter, r *http.Request) (model.StatusPage, bool) {
	page, err := s.opts.Store.GetPublishedPageBySlug(r.Context(), chi.URLParam(r, "slug"))
	switch {
	case err == nil:
		return page, true
	case errors.Is(err, store.ErrNotFound):
		s.renderResult(w, http.StatusNotFound, nil, resultView{Title: "Page not found", Message: "This status page does not exist."})
	default:
		s.opts.Logger.Error("statuspage: load page", "err", err)
		s.renderResult(w, http.StatusServiceUnavailable, nil, resultView{Title: "Try again later", Message: "Something went wrong on our side. Please try again in a few minutes."})
	}
	return model.StatusPage{}, false
}

func (s *Service) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	page, ok := s.subscribePage(w, r)
	if !ok {
		return
	}
	if !sameOrigin(r) {
		s.renderResult(w, http.StatusForbidden, &page, resultView{Title: "Request refused", Message: "This request came from another site. Please use the form on the status page."})
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/x-www-form-urlencoded" {
		s.renderResult(w, http.StatusUnsupportedMediaType, &page, resultView{Title: "Unsupported request", Message: "Please use the subscribe form on the status page."})
		return
	}
	if !s.parseSmallForm(w, r, &page) {
		return
	}
	accepted := resultView{Title: "Check your inbox", Message: subscribeAccepted}
	if r.PostForm.Get("website") != "" { // honeypot: look successful, do nothing
		s.renderResult(w, http.StatusOK, &page, accepted)
		return
	}
	if wait, ok := s.limiter.allowIP(s.clientKey(r)); !ok {
		s.renderTooMany(w, &page, wait)
		return
	}
	email, ok := validEmail(r.PostForm.Get("email"))
	if !ok {
		s.renderResult(w, http.StatusUnprocessableEntity, &page, resultView{Title: "Invalid email address", Message: "Please enter a valid email address, for example name@example.com."})
		return
	}
	if wait, ok := s.limiter.allowEmail(page.ID, email); !ok {
		s.renderTooMany(w, &page, wait)
		return
	}
	if capped, err := s.confirmMailsCapped(r.Context(), page.ID); err != nil {
		s.opts.Logger.Error("statuspage: count confirmation mails", "page", page.ID, "err", err)
		s.renderResult(w, http.StatusServiceUnavailable, &page, resultView{Title: "Try again later", Message: "Something went wrong on our side. Please try again in a few minutes."})
		return
	} else if capped {
		s.renderTooMany(w, &page, subscribeWindow/6)
		return
	}
	sub, created, err := s.opts.Store.CreateSubscriber(r.Context(), model.Subscriber{
		PageID: page.ID, Kind: model.SubscriberEmail, Target: email,
	})
	if err == nil && (created || !sub.Confirmed) {
		err = s.opts.Store.EnqueueConfirm(r.Context(), sub.ID)
	}
	if err != nil {
		s.opts.Logger.Error("statuspage: subscribe", "page", page.ID, "err", err)
		s.renderResult(w, http.StatusServiceUnavailable, &page, resultView{Title: "Try again later", Message: "Something went wrong on our side. Please try again in a few minutes."})
		return
	}
	s.renderResult(w, http.StatusOK, &page, accepted)
}

// confirmMailsCapped reports whether this page, or the instance, has queued
// its quota of confirmation mails for the current window. It runs before any
// subscriber is created, and answers identically whatever address was typed,
// so it reveals nothing about who is subscribed.
func (s *Service) confirmMailsCapped(ctx context.Context, pageID int64) (bool, error) {
	since := s.opts.Now().Add(-subscribeWindow)
	n, err := s.opts.Store.CountConfirmDeliveries(ctx, since, pageID)
	if err != nil {
		return false, err
	}
	if n >= confirmCapPage {
		return true, nil
	}
	n, err = s.opts.Store.CountConfirmDeliveries(ctx, since, 0)
	if err != nil {
		return false, err
	}
	return n >= confirmCapGlobal, nil
}

// parseSmallForm reads a form body of at most subscribeBodyLimit bytes into
// r.Form / r.PostForm, or writes the error page and returns false.
func (s *Service) parseSmallForm(w http.ResponseWriter, r *http.Request, page *model.StatusPage) bool {
	r.Body = http.MaxBytesReader(w, r.Body, subscribeBodyLimit)
	if err := r.ParseForm(); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.renderResult(w, http.StatusRequestEntityTooLarge, page, resultView{Title: "Request too large", Message: "That request was too large."})
		} else {
			s.renderResult(w, http.StatusBadRequest, page, resultView{Title: "Bad request", Message: "We could not read that request."})
		}
		return false
	}
	return true
}

func (s *Service) renderTooMany(w http.ResponseWriter, page *model.StatusPage, wait time.Duration) {
	secs := int((wait + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	s.renderResult(w, http.StatusTooManyRequests, page, resultView{Title: "Too many requests", Message: "Too many sign-up attempts. Please try again later."})
}

// linkToken reads the token from the query (t) or, on POST, the form.
func linkToken(r *http.Request) string {
	if t := r.URL.Query().Get("t"); t != "" {
		return t
	}
	return r.PostForm.Get("t")
}

// verifyLink validates the token in r for the given purpose. It returns the
// subscriber id, or writes the invalid/expired page and returns false.
func (s *Service) verifyLink(w http.ResponseWriter, r *http.Request, page *model.StatusPage, purpose byte) (int64, bool) {
	var (
		id  int64
		err error
	)
	if purpose == tokenConfirm {
		id, err = s.parseConfirmToken(linkToken(r))
	} else {
		id, err = s.parseUnsubscribeToken(linkToken(r))
	}
	if err != nil {
		s.renderInvalidLink(w, page, errors.Is(err, errTokenExpired))
		return 0, false
	}
	return id, true
}

func (s *Service) renderInvalidLink(w http.ResponseWriter, page *model.StatusPage, expired bool) {
	msg := "This link is invalid or has expired."
	if expired {
		msg += " Please subscribe again from the status page to get a new confirmation email."
	}
	s.renderResult(w, http.StatusBadRequest, page, resultView{Title: "Link not valid", Message: msg})
}

// handleLinkPage serves the GET of a confirm/unsubscribe link: a page with a
// button that POSTs to the same URL. It has no side effects.
func (s *Service) handleLinkPage(purpose byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page, ok := s.subscribePage(w, r)
		if !ok {
			return
		}
		if _, ok := s.verifyLink(w, r, &page, purpose); !ok {
			return
		}
		v := resultView{Title: "Confirm subscription", Message: "Confirm that you want to receive email updates about incidents on " + page.Title + ".", Button: "Confirm subscription"}
		if purpose == tokenUnsubscribe {
			v = resultView{Title: "Unsubscribe", Message: "Stop receiving email updates about incidents on " + page.Title + "?", Button: "Unsubscribe"}
		}
		s.renderResult(w, http.StatusOK, &page, v)
	}
}

// errWrongPage marks a signed token whose subscriber is on another page.
var errWrongPage = errors.New("statuspage: subscriber on another page")

// subscriberFor loads the subscriber behind a verified token and requires
// it to belong to page (a token minted for another page is invalid).
func (s *Service) subscriberFor(r *http.Request, page model.StatusPage, id int64) (model.Subscriber, error) {
	sub, err := s.opts.Store.GetSubscriber(r.Context(), id)
	if err != nil {
		return sub, err
	}
	if sub.PageID != page.ID {
		return sub, errWrongPage
	}
	return sub, nil
}

func (s *Service) renderUnavailable(w http.ResponseWriter, page *model.StatusPage, err error) {
	s.opts.Logger.Error("statuspage: subscriber link", "err", err)
	s.renderResult(w, http.StatusServiceUnavailable, page, resultView{Title: "Try again later", Message: "Something went wrong on our side. Please try again in a few minutes."})
}

func (s *Service) handleConfirm(w http.ResponseWriter, r *http.Request) {
	page, ok := s.subscribePage(w, r)
	if !ok || !s.parseSmallForm(w, r, &page) {
		return
	}
	id, ok := s.verifyLink(w, r, &page, tokenConfirm)
	if !ok {
		return
	}
	if _, err := s.subscriberFor(r, page, id); err != nil {
		switch {
		case errors.Is(err, errWrongPage):
			s.renderInvalidLink(w, &page, false)
		case errors.Is(err, store.ErrNotFound): // removed or purged: subscribe again
			s.renderInvalidLink(w, &page, true)
		default:
			s.renderUnavailable(w, &page, err)
		}
		return
	}
	if err := s.opts.Store.ConfirmSubscriber(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.renderInvalidLink(w, &page, true)
		} else {
			s.renderUnavailable(w, &page, err)
		}
		return
	}
	s.renderResult(w, http.StatusOK, &page, resultView{Title: "Subscription confirmed", Message: "You will now receive email updates about incidents on " + page.Title + "."})
}

func (s *Service) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	page, ok := s.subscribePage(w, r)
	if !ok || !s.parseSmallForm(w, r, &page) {
		return
	}
	id, ok := s.verifyLink(w, r, &page, tokenUnsubscribe)
	if !ok {
		return
	}
	done := resultView{Title: "Unsubscribed", Message: "You will no longer receive email updates about incidents on " + page.Title + "."}
	sub, err := s.subscriberFor(r, page, id)
	switch {
	case err == nil:
	case errors.Is(err, errWrongPage):
		s.renderInvalidLink(w, &page, false)
		return
	case errors.Is(err, store.ErrNotFound):
		// A signed token whose subscriber is gone: already unsubscribed.
		s.renderResult(w, http.StatusOK, &page, done)
		return
	default:
		s.renderUnavailable(w, &page, err)
		return
	}
	if err := s.opts.Store.DeleteSubscriber(r.Context(), page.ID, sub.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.renderUnavailable(w, &page, err)
		return
	}
	s.renderResult(w, http.StatusOK, &page, done)
}

// resultView is the data of the result page template. Page is the status
// page for the link back, or nil. Button, when set, renders a POST form to
// the current URL.
type resultView struct {
	Title   string
	Message string
	Button  string
	Page    *model.StatusPage
	Back    string
}

// renderResult writes a small self-contained HTML page. Everything is
// escaped by html/template; the page runs no script.
func (s *Service) renderResult(w http.ResponseWriter, code int, page *model.StatusPage, v resultView) {
	if page != nil {
		v.Page = page
		v.Back = s.pageBase(*page)
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := resultTmpl.Execute(w, v); err != nil {
		s.opts.Logger.Error("statuspage: render result", "err", err)
	}
}

var resultTmpl = template.Must(template.New("result").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{.Title}}{{with .Page}} · {{.Title}}{{end}}</title>
<style>
:root{color-scheme:light dark;--bg:#F4F7F5;--surface:#FFFFFF;--ink:#111A14;--muted:#5B665F;--line:#D8E0DB;--brand:#C2185B;--link:#C2185B}
@media (prefers-color-scheme:dark){:root{--bg:#090C10;--surface:#0F141A;--ink:#E4EAE6;--muted:#9AA6A0;--line:#222B33;--link:#F06292}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);font:16px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif;display:flex;min-height:100vh;align-items:center;justify-content:center;padding:16px}
main{background:var(--surface);border:1px solid var(--line);border-radius:12px;max-width:480px;width:100%;padding:24px}
h1{font-size:1.25rem;margin:0 0 8px}
p{margin:0 0 16px;color:var(--muted)}
button{background:var(--brand);color:#fff;border:0;border-radius:8px;padding:10px 18px;font:inherit;font-weight:600;cursor:pointer}
button:focus-visible,a:focus-visible{outline:2px solid var(--link);outline-offset:2px}
a{color:var(--link)}
.back{margin:16px 0 0;font-size:.9rem}
</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
<p>{{.Message}}</p>
{{if .Button}}<form method="post"><button type="submit">{{.Button}}</button></form>{{end}}
{{if .Back}}<p class="back"><a href="{{.Back}}">Back to {{.Page.Title}}</a></p>{{end}}
</main>
</body>
</html>
`))

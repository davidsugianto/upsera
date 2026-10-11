package statuspage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/store"
)

// subscribeTestEnv is a Service mounted on a chi router over a fakeStore with
// call recording for the subscribe-related store methods.
type subscribeTestEnv struct {
	svc      *Service
	handler  http.Handler
	store    *fakeStore
	created  []model.Subscriber
	enqueued []int64
	confirm  []int64
	deleted  [][2]int64
	subs     map[int64]model.Subscriber
	now      time.Time
}

func newSubscribeTestEnv(t *testing.T) *subscribeTestEnv {
	t.Helper()
	e := &subscribeTestEnv{store: &fakeStore{}, subs: map[int64]model.Subscriber{}, now: testNow}
	e.store.GetPublishedPageBySlugFn = func(_ context.Context, slug string) (model.StatusPage, error) {
		switch slug {
		case "acme":
			return model.StatusPage{ID: 1, Slug: "acme", Title: "Acme <b>Status</b>", Published: true}, nil
		case "other":
			return model.StatusPage{ID: 2, Slug: "other", Title: "Other", Published: true}, nil
		}
		return model.StatusPage{}, store.ErrNotFound
	}
	e.store.CreateSubscriberFn = func(_ context.Context, sub model.Subscriber) (model.Subscriber, bool, error) {
		e.created = append(e.created, sub)
		for _, ex := range e.subs {
			if ex.PageID == sub.PageID && strings.EqualFold(ex.Target, sub.Target) {
				return ex, false, nil
			}
		}
		sub.ID = int64(len(e.subs) + 100)
		e.subs[sub.ID] = sub
		return sub, true, nil
	}
	e.store.EnqueueConfirmFn = func(_ context.Context, id int64) error {
		e.enqueued = append(e.enqueued, id)
		return nil
	}
	e.store.GetSubscriberFn = func(_ context.Context, id int64) (model.Subscriber, error) {
		if sub, ok := e.subs[id]; ok {
			return sub, nil
		}
		return model.Subscriber{}, store.ErrNotFound
	}
	e.store.ConfirmSubscriberFn = func(_ context.Context, id int64) error {
		e.confirm = append(e.confirm, id)
		sub := e.subs[id]
		sub.Confirmed = true
		e.subs[id] = sub
		return nil
	}
	e.store.DeleteSubscriberFn = func(_ context.Context, pageID, id int64) error {
		e.deleted = append(e.deleted, [2]int64{pageID, id})
		if _, ok := e.subs[id]; !ok {
			return store.ErrNotFound
		}
		delete(e.subs, id)
		return nil
	}
	e.svc = newTestService(t, e.store, nil, func(o *Options) { o.Now = func() time.Time { return e.now } })
	r := chi.NewRouter()
	e.svc.mountSubscribe(r)
	e.handler = r
	return e
}

func (e *subscribeTestEnv) do(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// post sends a form POST to path from remote (default 192.0.2.1).
func (e *subscribeTestEnv) post(path string, form url.Values, mut func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if mut != nil {
		mut(req)
	}
	return e.do(req)
}

func (e *subscribeTestEnv) subscribe(email string) *httptest.ResponseRecorder {
	return e.post("/status/acme/subscribe", url.Values{"email": {email}}, nil)
}

func (e *subscribeTestEnv) get(path string) *httptest.ResponseRecorder {
	return e.do(httptest.NewRequest(http.MethodGet, path, nil))
}

func (e *subscribeTestEnv) addSub(id, pageID int64, confirmed bool) model.Subscriber {
	sub := model.Subscriber{ID: id, PageID: pageID, Kind: model.SubscriberEmail, Target: "u" + strconv.FormatInt(id, 10) + "@example.com", Confirmed: confirmed}
	e.subs[id] = sub
	return sub
}

func subscribeTestWantStatus(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, code, rec.Body.String())
	}
}

func TestSubscribeHappyPath(t *testing.T) {
	e := newSubscribeTestEnv(t)
	rec := e.subscribe("Jane@Example.com")
	subscribeTestWantStatus(t, rec, 200)
	if !strings.Contains(rec.Body.String(), subscribeAccepted) {
		t.Fatalf("body missing uniform message: %s", rec.Body.String())
	}
	if len(e.created) != 1 || e.created[0].PageID != 1 || e.created[0].Kind != model.SubscriberEmail ||
		e.created[0].Target != "Jane@Example.com" || e.created[0].Confirmed {
		t.Fatalf("created = %+v", e.created)
	}
	if len(e.enqueued) != 1 || e.enqueued[0] != e.subs[100].ID {
		t.Fatalf("enqueued = %v", e.enqueued)
	}
}

func TestSubscribeSecurityHeaders(t *testing.T) {
	e := newSubscribeTestEnv(t)
	for _, rec := range []*httptest.ResponseRecorder{e.subscribe("a@example.com"), e.subscribe("bad"), e.get("/status/nope/confirm?t=x")} {
		h := rec.Header()
		want := map[string]string{
			"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'",
			"X-Content-Type-Options":  "nosniff",
			"Referrer-Policy":         "no-referrer",
			"Cache-Control":           "no-store",
		}
		for k, v := range want {
			if h.Get(k) != v {
				t.Errorf("%s = %q, want %q", k, h.Get(k), v)
			}
		}
		if !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
			t.Errorf("Content-Type = %q", h.Get("Content-Type"))
		}
	}
}

func TestSubscribeExistingUnconfirmedReEnqueues(t *testing.T) {
	e := newSubscribeTestEnv(t)
	e.addSub(5, 1, false)
	e.subs[5] = model.Subscriber{ID: 5, PageID: 1, Kind: model.SubscriberEmail, Target: "jane@example.com"}
	fresh := newSubscribeTestEnv(t).subscribe("new@example.com")

	rec := e.subscribe("JANE@example.com")
	subscribeTestWantStatus(t, rec, 200)
	if len(e.enqueued) != 1 || e.enqueued[0] != 5 {
		t.Fatalf("enqueued = %v, want [5]", e.enqueued)
	}
	if rec.Body.String() != fresh.Body.String() {
		t.Fatal("response for existing unconfirmed differs from a new subscription")
	}
}

func TestSubscribeExistingConfirmedIsUniform(t *testing.T) {
	e := newSubscribeTestEnv(t)
	e.subs[5] = model.Subscriber{ID: 5, PageID: 1, Kind: model.SubscriberEmail, Target: "jane@example.com", Confirmed: true}
	fresh := newSubscribeTestEnv(t).subscribe("new@example.com")

	rec := e.subscribe("jane@example.com")
	subscribeTestWantStatus(t, rec, 200)
	if len(e.enqueued) != 0 {
		t.Fatalf("enqueued for confirmed subscriber: %v", e.enqueued)
	}
	if rec.Body.String() != fresh.Body.String() || rec.Code != fresh.Code {
		t.Fatal("response for confirmed subscriber differs from a new subscription")
	}
}

func TestSubscribeInvalidEmail(t *testing.T) {
	long := strings.Repeat("a", 64) + "@" + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 63) + ".com"
	for name, in := range map[string]string{
		"empty":          "",
		"blank":          "   ",
		"display name":   "Jane <jane@example.com>",
		"quoted name":    `"Jane" <jane@example.com>`,
		"no domain dot":  "jane@localhost",
		"no at":          "jane.example.com",
		"no local":       "@example.com",
		"too long":       long,
		"trailing dot":   "jane@example.",
		"leading dot":    "jane@.example.com",
		"double dot":     "jane@example..com",
		"domain literal": "jane@[192.0.2.1]",
		"comment":        "jane@example.com (Jane)",
		"two addresses":  "a@example.com, b@example.com",
		"inner space":    "ja ne@example.com",
		"newline":        "jane@example.com\r\nBcc: x@example.com",
		"quoted local":   `"a b"@example.com`,
		"non ascii":      "jané@example.com",
		"local too long": strings.Repeat("a", 65) + "@example.com",
	} {
		t.Run(name, func(t *testing.T) {
			e := newSubscribeTestEnv(t)
			rec := e.subscribe(in)
			subscribeTestWantStatus(t, rec, 422)
			if len(e.created)+len(e.enqueued) != 0 {
				t.Fatalf("store touched for invalid email %q", in)
			}
		})
	}
	t.Run("whitespace is trimmed", func(t *testing.T) {
		e := newSubscribeTestEnv(t)
		subscribeTestWantStatus(t, e.subscribe("  jane@example.com \n"), 200)
		if len(e.created) != 1 || e.created[0].Target != "jane@example.com" {
			t.Fatalf("created = %+v", e.created)
		}
	})
	t.Run("email in query only is ignored", func(t *testing.T) {
		e := newSubscribeTestEnv(t)
		rec := e.post("/status/acme/subscribe?email=jane@example.com", url.Values{}, nil)
		subscribeTestWantStatus(t, rec, 422)
	})
}

func TestSubscribeHoneypot(t *testing.T) {
	e := newSubscribeTestEnv(t)
	good := e.subscribe("ok@example.com")
	e.created, e.enqueued = nil, nil

	rec := e.post("/status/acme/subscribe", url.Values{"email": {"bot@example.com"}, "website": {"http://spam.test"}}, nil)
	subscribeTestWantStatus(t, rec, 200)
	if len(e.created)+len(e.enqueued) != 0 {
		t.Fatalf("honeypot reached the store: %v %v", e.created, e.enqueued)
	}
	if rec.Body.String() != good.Body.String() {
		t.Fatal("honeypot response differs from success")
	}
}

func TestSubscribeIPLimit(t *testing.T) {
	e := newSubscribeTestEnv(t)
	for i := 0; i < 5; i++ {
		subscribeTestWantStatus(t, e.subscribe("user"+strconv.Itoa(i)+"@example.com"), 200)
	}
	rec := e.subscribe("user6@example.com")
	subscribeTestWantStatus(t, rec, 429)
	if secs, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || secs < 1 || secs > 3600 {
		t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	if len(e.created) != 5 {
		t.Fatalf("created = %d, want 5", len(e.created))
	}
	// Another client is unaffected; X-Forwarded-For is ignored.
	rec = e.post("/status/acme/subscribe", url.Values{"email": {"x@example.com"}}, func(r *http.Request) {
		r.RemoteAddr = "198.51.100.9:4000"
	})
	subscribeTestWantStatus(t, rec, 200)
	rec = e.post("/status/acme/subscribe", url.Values{"email": {"y@example.com"}}, func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "203.0.113.77")
	})
	subscribeTestWantStatus(t, rec, 429)
	// The window slides.
	e.now = e.now.Add(time.Hour + time.Second)
	subscribeTestWantStatus(t, e.subscribe("later@example.com"), 200)
}

func TestSubscribeInvalidEmailsCountTowardIPLimit(t *testing.T) {
	e := newSubscribeTestEnv(t)
	for i := 0; i < 5; i++ {
		subscribeTestWantStatus(t, e.subscribe("bad"), 422)
	}
	subscribeTestWantStatus(t, e.subscribe("bad"), 429)
}

func TestSubscribeEmailLimit(t *testing.T) {
	e := newSubscribeTestEnv(t)
	for i, em := range []string{"Jane@example.com", "jane@EXAMPLE.com", "JANE@example.com"} {
		ip := "198.51.100." + strconv.Itoa(i+1) + ":1"
		rec := e.post("/status/acme/subscribe", url.Values{"email": {em}}, func(r *http.Request) { r.RemoteAddr = ip })
		subscribeTestWantStatus(t, rec, 200)
	}
	rec := e.post("/status/acme/subscribe", url.Values{"email": {"jane@example.com"}}, func(r *http.Request) { r.RemoteAddr = "198.51.100.50:1" })
	subscribeTestWantStatus(t, rec, 429)
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
	if len(e.enqueued) != 3 {
		t.Fatalf("enqueued = %d, want 3", len(e.enqueued))
	}
	// A different address from a fresh IP is fine.
	rec = e.post("/status/acme/subscribe", url.Values{"email": {"john@example.com"}}, func(r *http.Request) { r.RemoteAddr = "198.51.100.51:1" })
	subscribeTestWantStatus(t, rec, 200)
}

func TestSubscribeBodyAndContentType(t *testing.T) {
	e := newSubscribeTestEnv(t)

	big := url.Values{"email": {"a@example.com"}, "pad": {strings.Repeat("x", 5000)}}
	subscribeTestWantStatus(t, e.post("/status/acme/subscribe", big, nil), 413)

	for _, ct := range []string{"application/json", "multipart/form-data; boundary=x", "text/plain", ""} {
		rec := e.post("/status/acme/subscribe", url.Values{"email": {"a@example.com"}}, func(r *http.Request) {
			if ct == "" {
				r.Header.Del("Content-Type")
			} else {
				r.Header.Set("Content-Type", ct)
			}
		})
		subscribeTestWantStatus(t, rec, 415)
	}
	rec := e.post("/status/acme/subscribe", url.Values{"email": {"a@example.com"}}, func(r *http.Request) {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	})
	subscribeTestWantStatus(t, rec, 200)
	if len(e.created) != 1 {
		t.Fatalf("created = %d, want 1", len(e.created))
	}
}

func TestSubscribeOrigin(t *testing.T) {
	for name, tc := range map[string]struct {
		origin string
		host   string
		want   int
	}{
		"none":           {"", "upsera.test", 200},
		"same":           {"https://upsera.test", "upsera.test", 200},
		"same case":      {"https://UPSERA.test", "upsera.test", 200},
		"same with port": {"http://localhost:8080", "localhost:8080", 200},
		"other site":     {"https://evil.test", "upsera.test", 403},
		"other port":     {"http://localhost:9999", "localhost:8080", 403},
		"suffix trick":   {"https://upsera.test.evil.test", "upsera.test", 403},
		"null origin":    {"null", "upsera.test", 403},
		"garbage":        {"::::", "upsera.test", 403},
		"custom domain":  {"https://status.acme.test", "status.acme.test", 200},
	} {
		t.Run(name, func(t *testing.T) {
			e := newSubscribeTestEnv(t)
			rec := e.post("/status/acme/subscribe", url.Values{"email": {"a@example.com"}}, func(r *http.Request) {
				r.Host = tc.host
				if tc.origin != "" {
					r.Header.Set("Origin", tc.origin)
				}
			})
			subscribeTestWantStatus(t, rec, tc.want)
			if tc.want == 403 && len(e.created) != 0 {
				t.Fatal("store touched after origin mismatch")
			}
		})
	}
}

func TestSubscribeUnknownPage(t *testing.T) {
	e := newSubscribeTestEnv(t)
	subscribeTestWantStatus(t, e.post("/status/nope/subscribe", url.Values{"email": {"a@example.com"}}, nil), 404)
	subscribeTestWantStatus(t, e.get("/status/nope/confirm?t=x"), 404)
	subscribeTestWantStatus(t, e.get("/status/nope/unsubscribe?t=x"), 404)
	if len(e.created) != 0 {
		t.Fatal("subscriber created for unknown page")
	}
}

func TestSubscribeStoreErrors(t *testing.T) {
	boom := errors.New("db down secret-detail")
	for name, hook := range map[string]func(*subscribeTestEnv){
		"page lookup": func(e *subscribeTestEnv) {
			e.store.GetPublishedPageBySlugFn = func(context.Context, string) (model.StatusPage, error) {
				return model.StatusPage{}, boom
			}
		},
		"create": func(e *subscribeTestEnv) {
			e.store.CreateSubscriberFn = func(context.Context, model.Subscriber) (model.Subscriber, bool, error) {
				return model.Subscriber{}, false, boom
			}
		},
		"enqueue": func(e *subscribeTestEnv) {
			e.store.EnqueueConfirmFn = func(context.Context, int64) error { return boom }
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newSubscribeTestEnv(t)
			hook(e)
			rec := e.subscribe("a@example.com")
			subscribeTestWantStatus(t, rec, 503)
			if strings.Contains(rec.Body.String(), "secret-detail") {
				t.Fatal("store error leaked into the page")
			}
		})
	}
}

func TestSubscribeEscapesOutput(t *testing.T) {
	e := newSubscribeTestEnv(t)
	for _, rec := range []*httptest.ResponseRecorder{
		e.subscribe("a@example.com"),
		e.get("/status/acme/confirm?t=" + url.QueryEscape("<script>alert(1)</script>")),
	} {
		body := rec.Body.String()
		if strings.Contains(body, "<b>Status</b>") || strings.Contains(body, "<script>") {
			t.Fatalf("unescaped markup in page: %s", body)
		}
	}
	rec := e.subscribe("a@example.com")
	if !strings.Contains(rec.Body.String(), `href="https://upsera.test/status/acme"`) {
		t.Fatalf("missing back link: %s", rec.Body.String())
	}
	// A hostile-looking address never appears in any response.
	rec = e.subscribe(`"><script>x</script>@example.com`)
	if strings.Contains(rec.Body.String(), "<script>") {
		t.Fatal("email echoed unescaped")
	}
}

func TestSubscribeMethods(t *testing.T) {
	e := newSubscribeTestEnv(t)
	subscribeTestWantStatus(t, e.get("/status/acme/subscribe"), 405)
	subscribeTestWantStatus(t, e.do(httptest.NewRequest(http.MethodPut, "/status/acme/confirm?t=x", nil)), 405)
}

func TestConfirmGetHasNoSideEffects(t *testing.T) {
	e := newSubscribeTestEnv(t)
	sub := e.addSub(7, 1, false)
	link := strings.TrimPrefix(e.svc.confirmLink(model.StatusPage{ID: 1, Slug: "acme"}, sub), "https://upsera.test")

	rec := e.get(link)
	subscribeTestWantStatus(t, rec, 200)
	body := rec.Body.String()
	if !strings.Contains(body, "Confirm subscription") || !strings.Contains(body, `<form method="post">`) {
		t.Fatalf("no confirm button: %s", body)
	}
	if len(e.confirm) != 0 || e.subs[7].Confirmed {
		t.Fatal("GET confirmed the subscriber")
	}
	// GET with a bad token shows the error and still touches nothing.
	subscribeTestWantStatus(t, e.get("/status/acme/confirm?t=junk"), 400)
	subscribeTestWantStatus(t, e.get("/status/acme/confirm"), 400)
}

func TestConfirmPost(t *testing.T) {
	e := newSubscribeTestEnv(t)
	sub := e.addSub(7, 1, false)
	tok := e.svc.confirmToken(sub.ID)

	t.Run("token in query", func(t *testing.T) {
		rec := e.post("/status/acme/confirm?t="+tok, url.Values{}, nil)
		subscribeTestWantStatus(t, rec, 200)
		if len(e.confirm) != 1 || e.confirm[0] != 7 || !e.subs[7].Confirmed {
			t.Fatalf("confirm calls = %v", e.confirm)
		}
	})
	t.Run("token in form is idempotent", func(t *testing.T) {
		rec := e.post("/status/acme/confirm", url.Values{"t": {tok}}, nil)
		subscribeTestWantStatus(t, rec, 200)
		if !strings.Contains(rec.Body.String(), "Subscription confirmed") {
			t.Fatalf("body: %s", rec.Body.String())
		}
	})
}

func TestConfirmRejectsBadTokens(t *testing.T) {
	e := newSubscribeTestEnv(t)
	e.addSub(7, 1, false)
	e.addSub(8, 2, false)
	good := e.svc.confirmToken(7)
	flipped := []byte(good)
	flipped[len(flipped)-5] ^= 1

	e.now = testNow.Add(confirmTTL + time.Hour)
	expired := good // minted at testNow, now past expiry
	e.now = testNow

	cross := e.svc.confirmToken(8) // subscriber on page "other", used on "acme"

	cases := map[string]struct {
		tok      string
		wantText string
	}{
		"tampered":      {string(flipped), "invalid or has expired"},
		"wrong purpose": {e.svc.unsubscribeToken(7), "invalid or has expired"},
		"cross page":    {cross, "invalid or has expired"},
		"empty":         {"", "invalid or has expired"},
		"unknown sub":   {e.svc.confirmToken(999), "subscribe again"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec := e.post("/status/acme/confirm?t="+c.tok, url.Values{}, nil)
			subscribeTestWantStatus(t, rec, 400)
			if !strings.Contains(rec.Body.String(), c.wantText) {
				t.Fatalf("body missing %q: %s", c.wantText, rec.Body.String())
			}
		})
	}
	t.Run("expired", func(t *testing.T) {
		e.now = testNow.Add(confirmTTL + time.Hour)
		defer func() { e.now = testNow }()
		rec := e.post("/status/acme/confirm?t="+expired, url.Values{}, nil)
		subscribeTestWantStatus(t, rec, 400)
		if b := rec.Body.String(); !strings.Contains(b, "invalid or has expired") || !strings.Contains(b, "subscribe again") {
			t.Fatalf("body: %s", b)
		}
		subscribeTestWantStatus(t, e.get("/status/acme/confirm?t="+expired), 400)
	})
	if len(e.confirm) != 0 {
		t.Fatalf("a bad token confirmed %v", e.confirm)
	}
}

func TestConfirmStoreErrors(t *testing.T) {
	e := newSubscribeTestEnv(t)
	sub := e.addSub(7, 1, false)
	e.store.ConfirmSubscriberFn = func(context.Context, int64) error { return errors.New("db down") }
	subscribeTestWantStatus(t, e.post("/status/acme/confirm?t="+e.svc.confirmToken(sub.ID), url.Values{}, nil), 503)
	e.store.GetSubscriberFn = func(context.Context, int64) (model.Subscriber, error) {
		return model.Subscriber{}, errors.New("db down")
	}
	subscribeTestWantStatus(t, e.post("/status/acme/confirm?t="+e.svc.confirmToken(sub.ID), url.Values{}, nil), 503)
}

func TestUnsubscribeGetHasNoSideEffects(t *testing.T) {
	e := newSubscribeTestEnv(t)
	sub := e.addSub(7, 1, true)
	link := strings.TrimPrefix(e.svc.unsubscribeLink(model.StatusPage{ID: 1, Slug: "acme"}, sub), "https://upsera.test")

	rec := e.get(link)
	subscribeTestWantStatus(t, rec, 200)
	if !strings.Contains(rec.Body.String(), "Unsubscribe") || !strings.Contains(rec.Body.String(), `<form method="post">`) {
		t.Fatalf("no unsubscribe button: %s", rec.Body.String())
	}
	if len(e.deleted) != 0 || len(e.subs) != 1 {
		t.Fatal("GET deleted the subscriber")
	}
	subscribeTestWantStatus(t, e.get("/status/acme/unsubscribe?t=junk"), 400)
	// A confirm token does not open the unsubscribe page.
	subscribeTestWantStatus(t, e.get("/status/acme/unsubscribe?t="+e.svc.confirmToken(7)), 400)
}

func TestUnsubscribePost(t *testing.T) {
	e := newSubscribeTestEnv(t)
	e.addSub(7, 1, true)
	tok := e.svc.unsubscribeToken(7)

	// RFC 8058 one-click: token only in the query, body is List-Unsubscribe=One-Click.
	rec := e.post("/status/acme/unsubscribe?t="+tok, url.Values{"List-Unsubscribe": {"One-Click"}}, nil)
	subscribeTestWantStatus(t, rec, 200)
	if len(e.deleted) != 1 || e.deleted[0] != [2]int64{1, 7} {
		t.Fatalf("deleted = %v", e.deleted)
	}
	if _, ok := e.subs[7]; ok {
		t.Fatal("subscriber still exists")
	}
	// Twice is fine.
	rec = e.post("/status/acme/unsubscribe", url.Values{"t": {tok}}, nil)
	subscribeTestWantStatus(t, rec, 200)
	if !strings.Contains(rec.Body.String(), "Unsubscribed") {
		t.Fatalf("body: %s", rec.Body.String())
	}
	// No body at all also works.
	req := httptest.NewRequest(http.MethodPost, "/status/acme/unsubscribe?t="+tok, nil)
	subscribeTestWantStatus(t, e.do(req), 200)
}

func TestUnsubscribeRejectsBadTokens(t *testing.T) {
	e := newSubscribeTestEnv(t)
	e.addSub(7, 1, true)
	e.addSub(8, 2, true)
	good := e.svc.unsubscribeToken(7)
	flipped := []byte(good)
	flipped[len(flipped)-5] ^= 1

	for name, tok := range map[string]string{
		"tampered":      string(flipped),
		"wrong purpose": e.svc.confirmToken(7),
		"cross page":    e.svc.unsubscribeToken(8),
		"empty":         "",
	} {
		t.Run(name, func(t *testing.T) {
			rec := e.post("/status/acme/unsubscribe?t="+tok, url.Values{}, nil)
			subscribeTestWantStatus(t, rec, 400)
			if !strings.Contains(rec.Body.String(), "invalid or has expired") {
				t.Fatalf("body: %s", rec.Body.String())
			}
		})
	}
	if len(e.deleted) != 0 || len(e.subs) != 2 {
		t.Fatalf("a bad token deleted something: %v", e.deleted)
	}
}

func TestUnsubscribeNeverExpires(t *testing.T) {
	e := newSubscribeTestEnv(t)
	e.addSub(7, 1, true)
	tok := e.svc.unsubscribeToken(7)
	e.now = testNow.Add(5 * 365 * 24 * time.Hour)
	subscribeTestWantStatus(t, e.post("/status/acme/unsubscribe?t="+tok, url.Values{}, nil), 200)
	if len(e.subs) != 0 {
		t.Fatal("old unsubscribe link did not work")
	}
}

func TestUnsubscribeStoreError(t *testing.T) {
	e := newSubscribeTestEnv(t)
	e.addSub(7, 1, true)
	e.store.DeleteSubscriberFn = func(context.Context, int64, int64) error { return errors.New("db down") }
	subscribeTestWantStatus(t, e.post("/status/acme/unsubscribe?t="+e.svc.unsubscribeToken(7), url.Values{}, nil), 503)
}

func TestSubscribeCustomDomainLinks(t *testing.T) {
	e := newSubscribeTestEnv(t)
	dom := "status.acme.test"
	e.store.GetPublishedPageBySlugFn = func(context.Context, string) (model.StatusPage, error) {
		return model.StatusPage{ID: 1, Slug: "acme", Title: "Acme", CustomDomain: &dom, Published: true}, nil
	}
	rec := e.subscribe("a@example.com")
	subscribeTestWantStatus(t, rec, 200)
	if !strings.Contains(rec.Body.String(), `href="https://status.acme.test"`) {
		t.Fatalf("back link not on the custom domain: %s", rec.Body.String())
	}
}

// However many addresses or +aliases an attacker uses, the number of
// confirmation mails a page, and the instance, send per hour is capped.
func TestSubscribeConfirmMailCaps(t *testing.T) {
	cases := map[string]struct {
		count    func(pageID int64) int
		wantCode int
	}{
		"below both caps": {func(int64) int { return 1 }, 200},
		"page cap reached": {func(p int64) int {
			if p != 0 {
				return confirmCapPage
			}
			return 1
		}, http.StatusTooManyRequests},
		"global cap reached": {func(p int64) int {
			if p == 0 {
				return confirmCapGlobal
			}
			return 1
		}, http.StatusTooManyRequests},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newSubscribeTestEnv(t)
			var since time.Time
			e.store.CountConfirmDeliveriesFn = func(_ context.Context, s time.Time, pageID int64) (int, error) {
				since = s
				return tc.count(pageID), nil
			}
			rec := e.subscribe("jane@example.com")
			subscribeTestWantStatus(t, rec, tc.wantCode)
			if tc.wantCode == http.StatusTooManyRequests {
				if len(e.created) != 0 || len(e.enqueued) != 0 {
					t.Fatalf("a capped request still created a subscriber or a mail: %v %v", e.created, e.enqueued)
				}
				if rec.Header().Get("Retry-After") == "" {
					t.Fatal("missing Retry-After")
				}
			} else if len(e.enqueued) != 1 {
				t.Fatalf("enqueued = %v", e.enqueued)
			}
			if want := e.now.Add(-time.Hour); !since.Equal(want) {
				t.Fatalf("counted since %v, want the last hour (%v)", since, want)
			}
		})
	}

	t.Run("a store error is a 503, not a free pass", func(t *testing.T) {
		e := newSubscribeTestEnv(t)
		e.store.CountConfirmDeliveriesFn = func(context.Context, time.Time, int64) (int, error) { return 0, errors.New("db down") }
		subscribeTestWantStatus(t, e.subscribe("jane@example.com"), http.StatusServiceUnavailable)
		if len(e.created) != 0 {
			t.Fatal("subscriber created while the cap could not be checked")
		}
	})
}

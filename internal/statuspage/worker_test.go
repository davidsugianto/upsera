package statuspage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/store"
)

// workerMail records what sendMail was asked to send.
type workerMail struct {
	mu   sync.Mutex
	sent []workerSent
	err  error
}

type workerSent struct {
	cfg           model.SMTPSettings
	to            []string
	subject, body string
}

func (m *workerMail) send(_ context.Context, cfg model.SMTPSettings, to []string, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, workerSent{cfg, to, subject, body})
	return nil
}

func (m *workerMail) all() []workerSent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]workerSent(nil), m.sent...)
}

// workerOutcome records how deliveries were marked.
type workerOutcome struct {
	mu       sync.Mutex
	sent     []int64
	failed   map[int64]string
	attempts int
	retry    time.Duration
	// deferred are deliveries rescheduled without using up an attempt.
	deferred   map[int64]string
	deferRetry time.Duration
}

func workerHooks(st *fakeStore, ds ...model.Delivery) *workerOutcome {
	o := &workerOutcome{failed: map[int64]string{}, deferred: map[int64]string{}}
	var claimed atomic.Bool
	st.ClaimDeliveriesFn = func(context.Context, time.Time, int, time.Duration) ([]model.Delivery, error) {
		if claimed.Swap(true) {
			return nil, nil
		}
		return ds, nil
	}
	st.MarkDeliverySentFn = func(_ context.Context, id int64) error {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.sent = append(o.sent, id)
		return nil
	}
	st.MarkDeliveryFailedFn = func(_ context.Context, id int64, msg string, max int, retry time.Duration) error {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.failed[id] = msg
		o.attempts, o.retry = max, retry
		return nil
	}
	st.DeferDeliveryFn = func(_ context.Context, id int64, msg string, retry time.Duration) error {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.deferred[id] = msg
		o.deferRetry = retry
		return nil
	}
	return o
}

func workerSMTP() *model.SMTPSettings {
	return &model.SMTPSettings{Host: "smtp.test", Port: 587, Username: "u", Password: "hunter2-secret", From: "status@upsera.test", TLS: "starttls"}
}

func workerEmailDelivery(id int64, kind model.DeliveryKind, confirmed bool) model.Delivery {
	in := mailTestIncident()
	d := model.Delivery{
		ID: id, Kind: kind, Page: mailTestPage(),
		Subscriber: model.Subscriber{ID: 5, PageID: 7, Kind: model.SubscriberEmail, Target: "ann@example.test", Confirmed: confirmed},
	}
	if kind == model.DeliveryIncident {
		d.Incident = &in
		d.UpdateID = new(int64(2))
	}
	return d
}

func TestWorkerAutoDraft(t *testing.T) {
	cand := model.DraftCandidate{PageID: 7, MonitorID: 3, DisplayName: "Website", AlertID: "alert-1", StartedAt: testNow.Add(-10 * time.Minute)}
	var (
		mu      sync.Mutex
		created []model.Incident
		bodies  []string
		before  []time.Time
		seen    = map[string]bool{}
	)
	st := &fakeStore{
		AutoDraftCandidatesFn: func(_ context.Context, b time.Time) ([]model.DraftCandidate, error) {
			mu.Lock()
			defer mu.Unlock()
			before = append(before, b)
			return []model.DraftCandidate{cand}, nil
		},
		CreateIncidentFn: func(_ context.Context, in model.Incident, body string) (model.Incident, error) {
			mu.Lock()
			defer mu.Unlock()
			if seen[*in.AutoAlertID] {
				return model.Incident{}, store.ErrConflict
			}
			seen[*in.AutoAlertID] = true
			created = append(created, in)
			bodies = append(bodies, body)
			return in, nil
		},
	}
	var logs bytes.Buffer
	s := newTestService(t, st, nil, func(o *Options) {
		o.DraftAfter = 7 * time.Minute
		o.Loc = time.FixedZone("WIB", 7*3600)
		o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})

	s.workerPass(t.Context())
	s.workerPass(t.Context()) // same candidate again: ErrConflict

	mu.Lock()
	defer mu.Unlock()
	if len(created) != 1 {
		t.Fatalf("created %d incidents, want 1", len(created))
	}
	for _, b := range before {
		if want := testNow.Add(-7 * time.Minute); !b.Equal(want) {
			t.Errorf("AutoDraftCandidates before = %v, want %v", b, want)
		}
	}
	if len(before) != 2 {
		t.Errorf("AutoDraftCandidates called %d times, want 2", len(before))
	}
	in := created[0]
	if in.PageID != 7 || in.Title != "Website: service disruption" || in.Severity != model.SeverityMajor ||
		in.Status != model.IncidentInvestigating || !in.Draft || *in.AutoAlertID != "alert-1" || !in.StartedAt.Equal(cand.StartedAt) {
		t.Errorf("incident = %+v", in)
	}
	if !strings.Contains(bodies[0], "Website") || !strings.Contains(bodies[0], "WIB") || !strings.Contains(bodies[0], "review") {
		t.Errorf("first body = %q", bodies[0])
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("ErrConflict was logged as an error:\n%s", logs.String())
	}
}

func TestWorkerAutoDraftErrorsDoNotStopPass(t *testing.T) {
	var resolved atomic.Int32
	st := &fakeStore{
		AutoDraftCandidatesFn: func(context.Context, time.Time) ([]model.DraftCandidate, error) {
			return []model.DraftCandidate{{PageID: 1, AlertID: "a"}, {PageID: 2, AlertID: "b"}}, nil
		},
		CreateIncidentFn: func(_ context.Context, in model.Incident, _ string) (model.Incident, error) {
			if in.PageID == 1 {
				return model.Incident{}, errors.New("db down")
			}
			return in, nil
		},
		ResolveAutoIncidentsFn: func(context.Context, string) (int, error) { resolved.Add(1); return 0, nil },
	}
	s := newTestService(t, st, nil, nil)
	s.workerPass(t.Context())
	if resolved.Load() != 1 {
		t.Errorf("resolve ran %d times, want 1", resolved.Load())
	}
}

func TestWorkerResolve(t *testing.T) {
	var body string
	st := &fakeStore{ResolveAutoIncidentsFn: func(_ context.Context, b string) (int, error) { body = b; return 2, nil }}
	s := newTestService(t, st, nil, nil)
	s.workerPass(t.Context())
	if body != "The affected service has recovered." {
		t.Errorf("resolve body = %q", body)
	}

	st.ResolveAutoIncidentsFn = func(context.Context, string) (int, error) { return 0, errors.New("boom") }
	s.workerPass(t.Context()) // logged, no panic
}

func TestWorkerDeliverConfirm(t *testing.T) {
	mail := &workerMail{}
	t.Run("sends mail", func(t *testing.T) {
		st := &fakeStore{GetSMTPSettingsFn: func(context.Context) (*model.SMTPSettings, error) { return workerSMTP(), nil }}
		d := workerEmailDelivery(1, model.DeliveryConfirm, false)
		out := workerHooks(st, d)
		s := newTestService(t, st, nil, nil)
		s.sendMail = mail.send
		s.workerPass(t.Context())

		sent := mail.all()
		if len(sent) != 1 || len(out.sent) != 1 || len(out.failed) != 0 {
			t.Fatalf("sent %d mails, marked sent %v failed %v", len(sent), out.sent, out.failed)
		}
		if sent[0].subject != "Confirm your subscription to Acme Status" || sent[0].to[0] != "ann@example.test" ||
			sent[0].cfg.Host != "smtp.test" || !strings.Contains(sent[0].body, s.confirmLink(d.Page, d.Subscriber)) {
			t.Errorf("mail = %+v", sent[0])
		}
	})
	t.Run("already confirmed is not mailed", func(t *testing.T) {
		mail := &workerMail{}
		st := &fakeStore{GetSMTPSettingsFn: func(context.Context) (*model.SMTPSettings, error) { return workerSMTP(), nil }}
		out := workerHooks(st, workerEmailDelivery(2, model.DeliveryConfirm, true))
		s := newTestService(t, st, nil, nil)
		s.sendMail = mail.send
		s.workerPass(t.Context())
		if len(mail.all()) != 0 || len(out.sent) != 1 || out.sent[0] != 2 {
			t.Errorf("mails %d, marked sent %v", len(mail.all()), out.sent)
		}
	})
}

func TestWorkerDeliverIncidentEmail(t *testing.T) {
	t.Run("sends notice to confirmed subscriber", func(t *testing.T) {
		mail := &workerMail{}
		st := &fakeStore{GetSMTPSettingsFn: func(context.Context) (*model.SMTPSettings, error) { return workerSMTP(), nil }}
		d := workerEmailDelivery(1, model.DeliveryIncident, true)
		d.Subscriber.Target = "ann@example.test\r\nBcc: x@evil.test"
		out := workerHooks(st, d)
		s := newTestService(t, st, nil, nil)
		s.sendMail = mail.send
		s.workerPass(t.Context())

		sent := mail.all()
		if len(sent) != 1 || len(out.sent) != 1 {
			t.Fatalf("sent %d, marked %v", len(sent), out.sent)
		}
		if sent[0].subject != "[Acme Status] Resolved: API outage" {
			t.Errorf("subject = %q", sent[0].subject)
		}
		if strings.ContainsAny(sent[0].to[0], "\r\n") {
			t.Errorf("recipient has line breaks: %q", sent[0].to[0])
		}
		if !strings.Contains(sent[0].body, "All good again.") {
			t.Errorf("body = %s", sent[0].body)
		}
	})
	t.Run("unconfirmed subscriber is skipped", func(t *testing.T) {
		mail := &workerMail{}
		st := &fakeStore{GetSMTPSettingsFn: func(context.Context) (*model.SMTPSettings, error) { return workerSMTP(), nil }}
		out := workerHooks(st, workerEmailDelivery(1, model.DeliveryIncident, false))
		s := newTestService(t, st, nil, nil)
		s.sendMail = mail.send
		s.workerPass(t.Context())
		if len(mail.all()) != 0 || len(out.sent) != 1 {
			t.Errorf("mails %d, marked sent %v", len(mail.all()), out.sent)
		}
	})
	t.Run("nil SMTP settings keep retrying without using up attempts", func(t *testing.T) {
		st := &fakeStore{}
		d1 := workerEmailDelivery(1, model.DeliveryIncident, true)
		out := workerHooks(st, d1)
		s := newTestService(t, st, nil, nil)
		s.sendMail = (&workerMail{}).send
		s.workerPass(t.Context())
		// Deferred, not failed: an attempt is not used up while SMTP is
		// unset, so an admin who configures it later still delivers.
		if out.deferred[1] != "SMTP is not configured on this instance" || out.deferRetry != 10*time.Minute || len(out.failed) != 0 {
			t.Errorf("deferred %v retry %v failed %v", out.deferred, out.deferRetry, out.failed)
		}

		d3 := workerEmailDelivery(2, model.DeliveryIncident, true)
		d3.Attempts = 40
		out = workerHooks(st, d3)
		s.workerPass(t.Context())
		if out.deferred[2] == "" || out.deferRetry != 10*time.Minute || len(out.failed) != 0 {
			t.Errorf("after 40 attempts: deferred %v retry %v failed %v", out.deferred, out.deferRetry, out.failed)
		}
	})
	t.Run("send error is sanitized", func(t *testing.T) {
		mail := &workerMail{err: errors.New("auth failed for u with password hunter2-secret\r\nsecond line")}
		st := &fakeStore{GetSMTPSettingsFn: func(context.Context) (*model.SMTPSettings, error) { return workerSMTP(), nil }}
		out := workerHooks(st, workerEmailDelivery(1, model.DeliveryIncident, true))
		s := newTestService(t, st, nil, nil)
		s.sendMail = mail.send
		s.workerPass(t.Context())
		msg := out.failed[1]
		if msg == "" || strings.Contains(msg, "hunter2-secret") || strings.ContainsAny(msg, "\r\n") {
			t.Errorf("failure message = %q", msg)
		}
		if len(out.sent) != 0 {
			t.Errorf("marked sent: %v", out.sent)
		}
	})
}

func workerWebhookDelivery(id int64, target string) model.Delivery {
	in := mailTestIncident()
	return model.Delivery{
		ID: id, Kind: model.DeliveryIncident, Page: mailTestPage(), Incident: &in, UpdateID: new(int64(2)),
		Subscriber: model.Subscriber{ID: 9, PageID: 7, Kind: model.SubscriberWebhook, Target: target, Confirmed: true},
	}
}

func TestWorkerDeliverWebhook(t *testing.T) {
	t.Run("success posts the payload", func(t *testing.T) {
		var (
			mu      sync.Mutex
			payload map[string]any
			header  http.Header
			method  string
		)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			defer mu.Unlock()
			method, header = r.Method, r.Header.Clone()
			_ = json.Unmarshal(b, &payload)
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		st := &fakeStore{}
		out := workerHooks(st, workerWebhookDelivery(1, srv.URL+"/hook?secret=abc"))
		s := newTestService(t, st, nil, nil)
		s.workerPass(t.Context())

		mu.Lock()
		defer mu.Unlock()
		if len(out.sent) != 1 || len(out.failed) != 0 {
			t.Fatalf("sent %v failed %v", out.sent, out.failed)
		}
		if method != http.MethodPost || header.Get("Content-Type") != "application/json" || header.Get("User-Agent") != "Upsera" {
			t.Errorf("method %q headers %v", method, header)
		}
		if payload["event"] != "incident" || payload["page"].(map[string]any)["title"] != "Acme Status" ||
			payload["incident"].(map[string]any)["title"] != "API outage" ||
			payload["update"].(map[string]any)["body"] != "All good again." {
			t.Errorf("payload = %v", payload)
		}
	})

	t.Run("non-2xx fails without the URL", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		st := &fakeStore{}
		out := workerHooks(st, workerWebhookDelivery(1, srv.URL+"/hook?secret=abc"))
		s := newTestService(t, st, nil, nil)
		s.workerPass(t.Context())
		if out.failed[1] != "webhook returned HTTP 500" || len(out.sent) != 0 || out.attempts != 10 || out.retry != 30*time.Second {
			t.Errorf("failed %v sent %v attempts %d retry %v", out.failed, out.sent, out.attempts, out.retry)
		}
	})

	t.Run("redirect is not followed", func(t *testing.T) {
		var hit atomic.Bool
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Store(true) }))
		defer target.Close()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusFound)
		}))
		defer srv.Close()
		st := &fakeStore{}
		out := workerHooks(st, workerWebhookDelivery(1, srv.URL))
		s := newTestService(t, st, nil, nil)
		s.workerPass(t.Context())
		if hit.Load() {
			t.Error("redirect target was requested")
		}
		if out.failed[1] != "webhook returned HTTP 302" {
			t.Errorf("failed %v", out.failed)
		}
	})

	t.Run("link-local target is blocked", func(t *testing.T) {
		st := &fakeStore{}
		out := workerHooks(st, workerWebhookDelivery(1, "http://169.254.169.254/latest/meta-data?token=s3cret"))
		s := newTestService(t, st, nil, nil)
		s.workerPass(t.Context())
		msg := out.failed[1]
		if msg == "" || len(out.sent) != 0 {
			t.Fatalf("failed %v sent %v", out.failed, out.sent)
		}
		for _, leak := range []string{"169.254", "s3cret", "http"} {
			if strings.Contains(msg, leak) {
				t.Errorf("failure message %q leaks %q", msg, leak)
			}
		}
	})

	t.Run("invalid URL fails", func(t *testing.T) {
		st := &fakeStore{}
		out := workerHooks(st, workerWebhookDelivery(1, "ftp://example.test/x"))
		s := newTestService(t, st, nil, nil)
		s.workerPass(t.Context())
		if out.failed[1] == "" {
			t.Errorf("failed %v", out.failed)
		}
	})
}

func TestWorkerDeliverBacklogDrains(t *testing.T) {
	var (
		mu     sync.Mutex
		calls  []int
		sentN  int
		leases []time.Duration
	)
	batch := func(n int) []model.Delivery {
		ds := make([]model.Delivery, n)
		for i := range ds {
			ds[i] = workerEmailDelivery(int64(i+1), model.DeliveryConfirm, true) // confirmed: marked sent, no mail
		}
		return ds
	}
	st := &fakeStore{
		ClaimDeliveriesFn: func(_ context.Context, now time.Time, limit int, lease time.Duration) ([]model.Delivery, error) {
			mu.Lock()
			defer mu.Unlock()
			if !now.Equal(testNow) || limit != 20 {
				t.Errorf("claim now %v limit %d", now, limit)
			}
			leases = append(leases, lease)
			calls = append(calls, len(calls))
			if len(calls) <= 2 {
				return batch(20), nil
			}
			return batch(3), nil
		},
		MarkDeliverySentFn: func(context.Context, int64) error { mu.Lock(); sentN++; mu.Unlock(); return nil },
	}
	s := newTestService(t, st, nil, nil)
	s.workerPass(t.Context())
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 || sentN != 43 {
		t.Errorf("claims %d, sent %d; want 3 and 43", len(calls), sentN)
	}
	if leases[0] != 2*time.Minute {
		t.Errorf("lease = %v", leases[0])
	}

	// A permanent backlog is capped per pass.
	calls = nil
	st.ClaimDeliveriesFn = func(context.Context, time.Time, int, time.Duration) ([]model.Delivery, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, 0)
		return batch(20), nil
	}
	mu.Unlock()
	s.workerPass(t.Context())
	mu.Lock()
	if len(calls) != 5 {
		t.Errorf("claims in a capped pass = %d, want 5", len(calls))
	}
}

func TestWorkerRunStopsAndSurvivesErrors(t *testing.T) {
	var passes atomic.Int32
	st := &fakeStore{
		AutoDraftCandidatesFn: func(context.Context, time.Time) ([]model.DraftCandidate, error) {
			passes.Add(1)
			return nil, errors.New("db down")
		},
		ResolveAutoIncidentsFn: func(context.Context, string) (int, error) { return 0, errors.New("db down") },
		ClaimDeliveriesFn: func(context.Context, time.Time, int, time.Duration) ([]model.Delivery, error) {
			return nil, errors.New("db down")
		},
	}
	s := newTestService(t, st, nil, func(o *Options) { o.WorkerEvery = 5 * time.Millisecond })

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	deadline := time.After(2 * time.Second)
	for passes.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("only %d passes ran", passes.Load())
		case <-time.After(2 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestWorkerRunFirstPassIsImmediate(t *testing.T) {
	var passes atomic.Int32
	st := &fakeStore{AutoDraftCandidatesFn: func(context.Context, time.Time) ([]model.DraftCandidate, error) {
		passes.Add(1)
		return nil, nil
	}}
	s := newTestService(t, st, nil, func(o *Options) { o.WorkerEvery = time.Hour })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.After(2 * time.Second)
	for passes.Load() < 1 {
		select {
		case <-deadline:
			t.Fatal("no immediate pass")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestWorkerRetryBackoff(t *testing.T) {
	for _, tc := range []struct {
		attempts int
		want     time.Duration
	}{
		{0, 30 * time.Second}, {1, time.Minute}, {2, 2 * time.Minute}, {3, 4 * time.Minute},
		{7, 64 * time.Minute}, {8, 2 * time.Hour}, {9, 2 * time.Hour}, {60, 2 * time.Hour},
	} {
		if got := deliveryRetryAfter(tc.attempts); got != tc.want {
			t.Errorf("deliveryRetryAfter(%d) = %v, want %v", tc.attempts, got, tc.want)
		}
	}
}

// A stuck outbox (a blackholed SMTP server) must not delay auto-drafting or
// auto-resolving incidents.
func TestWorkerDeliveryDoesNotBlockDrafting(t *testing.T) {
	release := make(chan struct{})
	var drafts atomic.Int32
	st := &fakeStore{
		AutoDraftCandidatesFn: func(context.Context, time.Time) ([]model.DraftCandidate, error) {
			drafts.Add(1)
			return nil, nil
		},
		ClaimDeliveriesFn: func(ctx context.Context, _ time.Time, _ int, _ time.Duration) ([]model.Delivery, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil, nil
		},
	}
	s := newTestService(t, st, nil, func(o *Options) { o.WorkerEvery = 5 * time.Millisecond })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	deadline := time.After(2 * time.Second)
	for drafts.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("drafting stalled behind delivery: %d passes", drafts.Load())
		case <-time.After(2 * time.Millisecond):
		}
	}
	close(release)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestWorkerDeliveryPanicIsContained(t *testing.T) {
	st := &fakeStore{GetSMTPSettingsFn: func(context.Context) (*model.SMTPSettings, error) { return workerSMTP(), nil }}
	boom, fine := workerEmailDelivery(1, model.DeliveryIncident, true), workerEmailDelivery(2, model.DeliveryIncident, true)
	fine.Subscriber.Target = "bob@example.test"
	out := workerHooks(st, boom, fine)
	s := newTestService(t, st, nil, nil)
	s.sendMail = func(_ context.Context, _ model.SMTPSettings, to []string, _, _ string) error {
		if to[0] == "ann@example.test" {
			panic("template exploded")
		}
		return nil
	}
	s.workerPass(t.Context())
	if out.failed[1] == "" || strings.Contains(out.failed[1], "exploded") {
		t.Errorf("the panicking delivery must be marked failed with a generic message: %q", out.failed[1])
	}
	if len(out.sent) != 1 || out.sent[0] != 2 {
		t.Errorf("the next delivery must still be sent: %v", out.sent)
	}
}

// After a few email failures in a row the rest of the pass skips email
// without using up their attempts (the server is probably down).
func TestWorkerEmailBreaker(t *testing.T) {
	st := &fakeStore{GetSMTPSettingsFn: func(context.Context) (*model.SMTPSettings, error) { return workerSMTP(), nil }}
	var ds []model.Delivery
	for i := int64(1); i <= 6; i++ {
		d := workerEmailDelivery(i, model.DeliveryIncident, true)
		d.Attempts = 1
		ds = append(ds, d)
	}
	var tried []int64
	var mu sync.Mutex
	attempts := map[int64]int{}
	retries := map[int64]time.Duration{}
	st.ClaimDeliveriesFn = func(context.Context, time.Time, int, time.Duration) ([]model.Delivery, error) {
		if len(tried) > 0 {
			return nil, nil
		}
		tried = append(tried, 0)
		return ds, nil
	}
	st.MarkDeliveryFailedFn = func(_ context.Context, id int64, _ string, max int, retry time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		attempts[id], retries[id] = max, retry
		return nil
	}
	deferred := map[int64]time.Duration{}
	st.DeferDeliveryFn = func(_ context.Context, id int64, _ string, retry time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		deferred[id] = retry
		return nil
	}
	calls := 0
	s := newTestService(t, st, nil, nil)
	s.sendMail = func(context.Context, model.SMTPSettings, []string, string, string) error {
		calls++
		return errors.New("connection refused")
	}
	s.workerPass(t.Context())
	if calls != 3 {
		t.Fatalf("sendMail calls = %d, want 3 before the breaker opens", calls)
	}
	for id := int64(1); id <= 3; id++ { // really failed attempts: normal backoff
		if attempts[id] != deliveryAttempts || retries[id] != deliveryRetryAfter(1) {
			t.Errorf("delivery %d: max %d retry %v", id, attempts[id], retries[id])
		}
	}
	for id := int64(4); id <= 6; id++ { // skipped: deferred, no attempt used up
		if _, failed := attempts[id]; failed || deferred[id] != time.Minute {
			t.Errorf("skipped delivery %d: marked failed %v, deferred for %v", id, failed, deferred[id])
		}
	}
}

func TestWorkerDeliveryPassBudget(t *testing.T) {
	old := deliveryPassBudget
	deliveryPassBudget = time.Nanosecond
	t.Cleanup(func() { deliveryPassBudget = old })

	st := &fakeStore{GetSMTPSettingsFn: func(context.Context) (*model.SMTPSettings, error) { return workerSMTP(), nil }}
	workerHooks(st,
		workerEmailDelivery(1, model.DeliveryIncident, true),
		workerEmailDelivery(2, model.DeliveryIncident, true),
		workerEmailDelivery(3, model.DeliveryIncident, true))
	mail := &workerMail{}
	s := newTestService(t, st, nil, nil)
	s.sendMail = func(ctx context.Context, cfg model.SMTPSettings, to []string, sub, body string) error {
		time.Sleep(2 * time.Millisecond)
		return mail.send(ctx, cfg, to, sub, body)
	}
	s.workerPass(t.Context())
	// The first delivery runs; once the budget is spent the rest stay leased
	// in the store and are picked up by a later pass.
	if got := len(mail.all()); got != 1 {
		t.Fatalf("mails sent in a pass with no budget left: %d, want 1", got)
	}
}

// Each delivery pass first drops notices that are a day old, whether they
// kept failing or were waiting for SMTP, and keeps delivering even when that
// housekeeping fails.
func TestWorkerExpiresOldDeliveries(t *testing.T) {
	var before time.Time
	expired := 0
	order := []string{}
	st := &fakeStore{
		ExpireDeliveriesFn: func(_ context.Context, b time.Time) (int64, error) {
			before = b
			expired++
			order = append(order, "expire")
			return 3, nil
		},
		ClaimDeliveriesFn: func(context.Context, time.Time, int, time.Duration) ([]model.Delivery, error) {
			order = append(order, "claim")
			return nil, nil
		},
	}
	s := newTestService(t, st, nil, nil)
	s.workerPass(t.Context())
	if want := testNow.Add(-24 * time.Hour); !before.Equal(want) {
		t.Fatalf("expired before %v, want %v (24h before now)", before, want)
	}
	if len(order) != 2 || order[0] != "expire" || order[1] != "claim" {
		t.Fatalf("order = %v, want expire then claim", order)
	}

	st.ExpireDeliveriesFn = func(context.Context, time.Time) (int64, error) { return 0, errors.New("db down") }
	order = nil
	s.workerPass(t.Context())
	if len(order) != 1 || order[0] != "claim" {
		t.Fatalf("a failing expiry must not stop delivery: %v", order)
	}
}

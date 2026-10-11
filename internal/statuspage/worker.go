package statuspage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
	"github.com/davidsugianto/upsera/internal/store"
)

const (
	deliveryBatch   = 20
	deliveryBatches = 5 // per pass, so a backlog drains without starving the next pass
	deliveryLease   = 2 * time.Minute
	deliveryTimeout = 30 * time.Second
	// deliveryAttempts attempts with exponential backoff (30s doubling, capped
	// at 2h) ride out a mail or webhook outage of a few hours before a notice
	// is given up on.
	deliveryAttempts   = 10
	deliveryBackoff    = 30 * time.Second
	deliveryBackoffMax = 2 * time.Hour
	// deliveryMaxAge is how old a still-undelivered notice may get before it is
	// dropped instead of sent late.
	deliveryMaxAge = 24 * time.Hour
	// noSMTPRetry is how often a delivery is retried while the instance has
	// no SMTP settings; deferred retries never use up attempts.
	noSMTPRetry = 10 * time.Minute
	// emailBreakerAfter consecutive email failures make the rest of a pass skip
	// email (the server is probably down); skipped deliveries retry after
	// breakerRetry without using up an attempt.
	emailBreakerAfter = 3
	breakerRetry      = time.Minute
	webhookTimeout    = 15 * time.Second
	markTimeout       = 10 * time.Second
	maxErrLen         = 200
)

// deliveryPassBudget bounds one delivery pass (a variable so tests can
// shrink it): once it is spent no further delivery is started, and the rest
// of the claimed batch stays leased and is retried by a later pass.
var deliveryPassBudget = 2 * time.Minute

// deliveryRetryAfter is the wait before retry number attempts+1: 30s,
// doubling, capped at deliveryBackoffMax.
func deliveryRetryAfter(attempts int) time.Duration {
	if attempts >= 8 { // 30s << 8 already exceeds the cap
		return deliveryBackoffMax
	}
	return min(deliveryBackoff<<attempts, deliveryBackoffMax)
}

// Run is the background worker. Two independent loops, each running once
// immediately and then every Options.WorkerEvery until ctx is done, keep a
// slow mail or webhook server from delaying incident handling: one
// auto-drafts incidents and resolves them when their alert recovers, the
// other delivers the subscriber outbox. A delivery in progress finishes
// before Run returns. Errors are logged, never fatal.
func (s *Service) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.loop(ctx, s.incidentPass)
	}()
	s.loop(ctx, s.deliveryPass)
	wg.Wait()
}

// loop runs pass now and then every WorkerEvery until ctx is done.
func (s *Service) loop(ctx context.Context, pass func(context.Context)) {
	pass(ctx)
	t := time.NewTicker(s.opts.WorkerEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pass(ctx)
		}
	}
}

// guarded runs f, logging instead of propagating a panic.
func (s *Service) guarded(what string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			s.opts.Logger.Error("status page worker panicked", "step", what, "panic", fmt.Sprint(r))
		}
	}()
	f()
}

// incidentPass drafts incidents for new outages and resolves recovered ones.
func (s *Service) incidentPass(ctx context.Context) {
	s.guarded("incidents", func() {
		s.autoDraft(ctx)
		s.resolveAuto(ctx)
	})
}

// deliveryPass drops notices that are too old, then sends the due part of
// the subscriber outbox.
func (s *Service) deliveryPass(ctx context.Context) {
	s.guarded("deliveries", func() {
		s.expireOld(ctx)
		s.deliverOutbox(ctx)
	})
}

// expireOld gives up on deliveries older than deliveryMaxAge. A failure is
// logged and delivery goes on.
func (s *Service) expireOld(ctx context.Context) {
	n, err := s.opts.Store.ExpireDeliveries(ctx, s.opts.Now().Add(-deliveryMaxAge))
	if err != nil {
		s.opts.Logger.Error("status page delivery expiry failed", "err", err)
		return
	}
	if n > 0 {
		s.opts.Logger.Warn("status page notices expired undelivered", "count", n)
	}
}

// workerPass is one sequential tick of both loops (used by tests).
func (s *Service) workerPass(ctx context.Context) {
	s.incidentPass(ctx)
	s.deliveryPass(ctx)
}

// autoDraft creates a draft incident for every open alert on a published
// page's monitor that has lasted DraftAfter and has no incident yet. The
// store refuses a second incident for the same (page, alert) with
// ErrConflict, which is how repeated passes and restarts stay idempotent.
func (s *Service) autoDraft(ctx context.Context) {
	cands, err := s.opts.Store.AutoDraftCandidates(ctx, s.opts.Now().Add(-s.opts.DraftAfter))
	if err != nil {
		s.opts.Logger.Error("status page auto-draft query failed", "err", err)
		return
	}
	for _, c := range cands {
		if ctx.Err() != nil {
			return
		}
		in := model.Incident{
			PageID:      c.PageID,
			Title:       c.DisplayName + ": service disruption",
			Severity:    model.SeverityMajor,
			Status:      model.IncidentInvestigating,
			Draft:       true,
			AutoAlertID: new(c.AlertID),
			StartedAt:   c.StartedAt,
		}
		body := fmt.Sprintf("Automatically detected: %s has been failing its checks since %s. A team member will review and publish an update.",
			c.DisplayName, c.StartedAt.In(s.opts.Loc).Format(timeFormat))
		if _, err := s.opts.Store.CreateIncident(ctx, in, body); err != nil {
			if !errors.Is(err, store.ErrConflict) {
				s.opts.Logger.Error("status page auto-draft failed", "page_id", c.PageID, "err", err)
			}
			continue
		}
	}
}

// resolveAuto resolves auto-drafted incidents whose alert has recovered.
func (s *Service) resolveAuto(ctx context.Context) {
	n, err := s.opts.Store.ResolveAutoIncidents(ctx, "The affected service has recovered.")
	if err != nil {
		s.opts.Logger.Error("status page auto-resolve failed", "err", err)
		return
	}
	if n > 0 {
		s.InvalidateAll()
	}
}

// deliverOutbox sends due deliveries, up to deliveryBatches full batches.
func (s *Service) deliverOutbox(ctx context.Context) {
	start := time.Now()
	handled, emailFails := 0, 0
	for range deliveryBatches {
		if ctx.Err() != nil {
			return
		}
		ds, err := s.opts.Store.ClaimDeliveries(ctx, s.opts.Now(), deliveryBatch, deliveryLease)
		if err != nil {
			s.opts.Logger.Error("status page delivery claim failed", "err", err)
			return
		}
		for _, d := range ds {
			// At least one delivery always runs, so a pass makes progress even
			// with a tiny budget; the rest stay leased and are retried later.
			if ctx.Err() != nil || (handled > 0 && time.Since(start) > deliveryPassBudget) {
				return
			}
			handled++
			isEmail := d.Subscriber.Kind == model.SubscriberEmail
			if isEmail && emailFails >= emailBreakerAfter {
				s.deferDelivery(ctx, d, "email delivery paused: the mail server is failing", breakerRetry)
				continue
			}
			err := s.handleDelivery(ctx, d)
			switch {
			case !isEmail || err == nil || errors.Is(err, errNoSMTP):
				if err == nil && isEmail {
					emailFails = 0
				}
			default:
				emailFails++
			}
		}
		if len(ds) < deliveryBatch {
			return
		}
	}
}

// deferDelivery reschedules d after retry without using up an attempt.
func (s *Service) deferDelivery(ctx context.Context, d model.Delivery, msg string, retry time.Duration) {
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), markTimeout)
	defer cancel()
	if err := s.opts.Store.DeferDelivery(mctx, d.ID, msg, retry); err != nil {
		s.opts.Logger.Error("status page delivery reschedule failed", "delivery_id", d.ID, "err", err)
	}
}

// handleDelivery sends one delivery and records the outcome, returning the
// send error. It runs to completion even when ctx is cancelled mid-way, and
// a panic while sending counts as a failed attempt instead of leaving the
// delivery (and its batch) leased.
func (s *Service) handleDelivery(ctx context.Context, d model.Delivery) (err error) {
	base := context.WithoutCancel(ctx)
	dctx, cancel := context.WithTimeout(base, deliveryTimeout)
	defer cancel()
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.opts.Logger.Error("status page delivery panicked", "delivery_id", d.ID, "panic", fmt.Sprint(r))
				err = errors.New("internal error while sending")
			}
		}()
		err = s.deliver(dctx, d)
	}()

	mctx, mcancel := context.WithTimeout(base, markTimeout)
	defer mcancel()
	if err == nil {
		if merr := s.opts.Store.MarkDeliverySent(mctx, d.ID); merr != nil {
			s.opts.Logger.Error("status page delivery mark sent failed", "delivery_id", d.ID, "err", merr)
		}
		return nil
	}
	s.opts.Logger.Warn("status page delivery failed", "delivery_id", d.ID, "kind", d.Kind, "attempt", d.Attempts+1, "err", err)
	if errors.Is(err, errNoSMTP) {
		// Not the notice's fault: keep it pending, without using up an
		// attempt, until an admin configures SMTP.
		if merr := s.opts.Store.DeferDelivery(mctx, d.ID, err.Error(), noSMTPRetry); merr != nil {
			s.opts.Logger.Error("status page delivery reschedule failed", "delivery_id", d.ID, "err", merr)
		}
		return err
	}
	if merr := s.opts.Store.MarkDeliveryFailed(mctx, d.ID, err.Error(), deliveryAttempts, deliveryRetryAfter(d.Attempts)); merr != nil {
		s.opts.Logger.Error("status page delivery mark failed failed", "delivery_id", d.ID, "err", merr)
	}
	return err
}

// errNoSMTP is retried indefinitely: an admin may configure SMTP at any
// time, and a notice should not be lost because they had not yet.
var errNoSMTP = errors.New("SMTP is not configured on this instance")

// deliver sends d. The returned error is safe to store and show: it never
// holds the SMTP password or a webhook URL.
func (s *Service) deliver(ctx context.Context, d model.Delivery) error {
	sub := d.Subscriber
	switch d.Kind {
	case model.DeliveryConfirm:
		if sub.Confirmed || sub.Kind != model.SubscriberEmail {
			return nil
		}
		subject, body := s.confirmMessage(d.Page, s.confirmLink(d.Page, sub))
		return s.sendEmail(ctx, sub.Target, subject, body)
	case model.DeliveryIncident:
		if d.Incident == nil {
			return errors.New("delivery has no incident")
		}
		switch sub.Kind {
		case model.SubscriberEmail:
			if !sub.Confirmed {
				return nil
			}
			subject, body := s.incidentMessage(d.Page, *d.Incident, d.UpdateID, s.unsubscribeLink(d.Page, sub))
			return s.sendEmail(ctx, sub.Target, subject, body)
		case model.SubscriberWebhook:
			return s.postWebhook(ctx, sub.Target, s.webhookPayload(d.Page, *d.Incident, d.UpdateID))
		}
		return fmt.Errorf("unknown subscriber kind %q", sub.Kind)
	}
	return fmt.Errorf("unknown delivery kind %q", d.Kind)
}

// sendEmail sends one message through the instance SMTP settings.
func (s *Service) sendEmail(ctx context.Context, to, subject, body string) error {
	cfg, err := s.opts.Store.GetSMTPSettings(ctx)
	if err != nil {
		return errors.New("could not load SMTP settings")
	}
	if cfg == nil {
		return errNoSMTP
	}
	if err := s.sendMail(ctx, *cfg, []string{oneLine(to)}, oneLine(subject), body); err != nil {
		return fmt.Errorf("sending email failed: %s", scrubError(err, cfg.Password))
	}
	return nil
}

// scrubError renders err on one short line without the secret.
func scrubError(err error, secret string) string {
	msg := oneLine(err.Error())
	if secret != "" {
		msg = strings.ReplaceAll(msg, secret, "***")
	}
	if len(msg) > maxErrLen {
		msg = msg[:maxErrLen] + "..."
	}
	return msg
}

// postWebhook POSTs payload as JSON to target. It dials through the
// outbound policy, ignores proxies, does not follow redirects and discards
// the response. Errors use a fixed vocabulary: the URL may hold secrets.
func (s *Service) postWebhook(ctx context.Context, target string, payload webhookBody) error {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("webhook URL is invalid")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return errors.New("could not encode webhook payload")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(b))
	if err != nil {
		return errors.New("webhook URL is invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Upsera")

	policy := s.opts.Policy
	if policy == nil {
		policy = netpolicy.New("")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // a proxy would dial on our behalf and bypass the policy
	tr.DialContext = policy.DialContext
	tr.DisableKeepAlives = true
	client := &http.Client{
		Transport:     tr,
		Timeout:       webhookTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()

	resp, err := client.Do(req)
	if err != nil {
		switch {
		case errors.Is(err, netpolicy.ErrBlocked):
			return errors.New("webhook target refused by outbound policy")
		case errors.Is(err, context.DeadlineExceeded):
			return errors.New("webhook request timed out")
		}
		return errors.New("webhook request failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}

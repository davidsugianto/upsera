// Package statuspage serves the public status pages (server-rendered HTML,
// RSS, subscribe/confirm/unsubscribe, custom-domain routing) and runs the
// background worker that auto-drafts incidents from outages and delivers
// incident notices to subscribers.
//
// File ownership while Phase 5 is built in parallel:
//
//	service.go                      the contract (interfaces, options, helpers)
//	handler.go, render.go, cache.go,
//	domains.go, templates/, *.css   public page, feed, cache, custom domains
//	subscribe.go, token.go          subscribe/confirm/unsubscribe, signed links, rate limits
//	worker.go, mail.go              auto-draft, outbox delivery, message bodies
package statuspage

import (
	"context"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
	"github.com/davidsugianto/upsera/internal/notify"
)

// Store is the subset of *store.Store the service uses.
type Store interface {
	GetPublishedPageBySlug(ctx context.Context, slug string) (model.StatusPage, error)
	ListPublishedCustomDomains(ctx context.Context) ([]model.PageDomain, error)
	StatusPageUptime(ctx context.Context, monitorIDs []int64, fromDay, todayStart time.Time) (map[int64][]model.UptimeDay, error)
	ListIncidents(ctx context.Context, pageID int64, includeDrafts bool, limit int) ([]model.Incident, error)

	CreateSubscriber(ctx context.Context, sub model.Subscriber) (model.Subscriber, bool, error)
	GetSubscriber(ctx context.Context, id int64) (model.Subscriber, error)
	ConfirmSubscriber(ctx context.Context, id int64) error
	DeleteSubscriber(ctx context.Context, pageID, id int64) error
	EnqueueConfirm(ctx context.Context, subscriberID int64) error
	// CountConfirmDeliveries counts confirmation mails queued since the given
	// time for one page (pageID 0 = the whole instance).
	CountConfirmDeliveries(ctx context.Context, since time.Time, pageID int64) (int, error)

	AutoDraftCandidates(ctx context.Context, startedBefore time.Time) ([]model.DraftCandidate, error)
	CreateIncident(ctx context.Context, in model.Incident, firstBody string) (model.Incident, error)
	ResolveAutoIncidents(ctx context.Context, body string) (int, error)
	ClaimDeliveries(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]model.Delivery, error)
	MarkDeliverySent(ctx context.Context, id int64) error
	MarkDeliveryFailed(ctx context.Context, id int64, errMsg string, maxAttempts int, retryAfter time.Duration) error
	// DeferDelivery reschedules a delivery without using up an attempt.
	DeferDelivery(ctx context.Context, id int64, errMsg string, retryAfter time.Duration) error
	// ExpireDeliveries drops pending deliveries created before the cutoff.
	ExpireDeliveries(ctx context.Context, before time.Time) (int64, error)
	GetSMTPSettings(ctx context.Context) (*model.SMTPSettings, error)
}

// States gives the live state of a monitor (the scheduler). The public
// page's banner is built from it, so rendering never queries the database
// for current status.
type States interface {
	State(id int64) (model.MonitorState, bool)
}

// Options configure a Service. Zero values get the defaults noted.
type Options struct {
	Store  Store
	States States
	// Secret is the key for signed confirm/unsubscribe links (APP_SECRET).
	Secret []byte
	// BaseURL is the instance's public URL without a trailing slash; pages
	// without a custom domain live at BaseURL/status/<slug>.
	BaseURL string
	// TrustedProxies are the reverse proxies whose X-Forwarded-For header is
	// believed when working out the client address for rate limits; empty =
	// use the connection's peer address.
	TrustedProxies []netip.Prefix
	// Loc is the server time zone: day boundaries of the uptime bars.
	Loc *time.Location // default UTC
	// Policy is the outbound target policy for webhook deliveries and SMTP.
	Policy *netpolicy.Policy
	Logger *slog.Logger
	// Now is the clock (tests); default time.Now.
	Now func() time.Time
	// CacheTTL is how long rendered pages are cached; default 30s.
	CacheTTL time.Duration
	// DraftAfter is how long a monitor listed on a published page must have
	// had an open alert before an incident is auto-drafted; default 5m.
	DraftAfter time.Duration
	// WorkerEvery is the worker's tick; default 10s.
	WorkerEvery time.Duration
}

// Service is the status page subsystem. Create it with New.
type Service struct {
	opts    Options
	cache   *pageCache
	domains *domainMap
	limiter *subscribeLimiter

	// sendMail delivers one plain-text email through the instance SMTP
	// settings; tests replace it. Default: notify.SendMail via opts.Policy.
	sendMail func(ctx context.Context, cfg model.SMTPSettings, to []string, subject, body string) error
}

// New builds a Service; it starts nothing (see Run, and ReloadDomains).
func New(opts Options) *Service {
	if opts.Loc == nil {
		opts.Loc = time.UTC
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = 30 * time.Second
	}
	if opts.DraftAfter <= 0 {
		opts.DraftAfter = 5 * time.Minute
	}
	if opts.WorkerEvery <= 0 {
		opts.WorkerEvery = 10 * time.Second
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	s := &Service{
		opts:    opts,
		cache:   newPageCache(opts.CacheTTL, opts.Now),
		domains: newDomainMap(),
		limiter: newSubscribeLimiter(opts.Now),
	}
	s.sendMail = func(ctx context.Context, cfg model.SMTPSettings, to []string, subject, body string) error {
		return notify.SendMail(ctx, opts.Policy, cfg, to, subject, body)
	}
	return s
}

// pageBase is the absolute URL of a page without a trailing slash: the
// custom domain (https) when it has one, else BaseURL/status/<slug>.
func (s *Service) pageBase(p model.StatusPage) string {
	if p.CustomDomain != nil && *p.CustomDomain != "" {
		return "https://" + *p.CustomDomain
	}
	return s.opts.BaseURL + "/status/" + p.Slug
}

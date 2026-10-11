package api

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/store"
)

// StatusPageCache is what the dashboard API tells the public status page
// subsystem when pages change (so cached renderings and the custom-domain
// map stay current). *statuspage.Service implements it.
type StatusPageCache interface {
	InvalidatePage(slug string)
	InvalidateAll()
	ReloadDomains(ctx context.Context) error
}

type noopStatusPageCache struct{}

func (noopStatusPageCache) InvalidatePage(string)               {}
func (noopStatusPageCache) InvalidateAll()                      {}
func (noopStatusPageCache) ReloadDomains(context.Context) error { return nil }

const (
	maxStatusPageItems = 100
	incidentListLimit  = 100
)

var (
	statusSlugRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)
	dnsLabelRE    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	reservedSlugs = map[string]bool{
		"api": true, "assets": true, "static": true, "admin": true, "status": true,
		"new": true, "healthz": true, "metrics": true,
	}
)

// ---- Status pages ----

type statusPagePathInput struct {
	TeamID int64 `path:"teamID"`
	PageID int64 `path:"pageID"`
}

// StatusPageItemRequest lists one monitor on a page. Its position is its
// index in the request's items array.
type StatusPageItemRequest struct {
	Section     string `json:"section,omitempty" maxLength:"100" doc:"Heading the item is shown under; empty for none."`
	MonitorID   int64  `json:"monitor_id" minimum:"1"`
	DisplayName string `json:"display_name,omitempty" maxLength:"100" doc:"Public name; defaults to the monitor's name."`
}

// StatusPageRequestBody is shared by create and update; update replaces the
// page's settings and items.
type StatusPageRequestBody struct {
	Slug         string                  `json:"slug" minLength:"2" maxLength:"63" doc:"URL path segment: lower-case letters, digits and hyphens."`
	Title        string                  `json:"title" minLength:"1" maxLength:"100"`
	Description  string                  `json:"description,omitempty" maxLength:"500"`
	LogoURL      string                  `json:"logo_url,omitempty" maxLength:"500" doc:"Empty, or an https:// URL."`
	Theme        model.Theme             `json:"theme,omitempty"`
	CustomDomain *string                 `json:"custom_domain,omitempty" nullable:"true" maxLength:"253" doc:"Host name the page is also served on, e.g. status.example.com. Only an instance admin can set, change or remove it; for anyone else leaving it out keeps the current value."`
	Published    bool                    `json:"published,omitempty" doc:"Only published pages are served publicly."`
	Items        []StatusPageItemRequest `json:"items,omitempty" maxItems:"100"`
}

type createStatusPageInput struct {
	TeamID int64 `path:"teamID"`
	Body   StatusPageRequestBody
}

type updateStatusPageInput struct {
	TeamID int64 `path:"teamID"`
	PageID int64 `path:"pageID"`
	Body   StatusPageRequestBody
}

// StatusPageItemBody is one monitor on a page, as shown publicly.
type StatusPageItemBody struct {
	ID          int64  `json:"id"`
	Section     string `json:"section"`
	MonitorID   int64  `json:"monitor_id"`
	DisplayName string `json:"display_name"`
}

// StatusPageBody is a status page with its items.
type StatusPageBody struct {
	ID           int64                `json:"id"`
	TeamID       int64                `json:"team_id"`
	Slug         string               `json:"slug"`
	Title        string               `json:"title"`
	Description  string               `json:"description"`
	LogoURL      string               `json:"logo_url"`
	Theme        model.Theme          `json:"theme"`
	CustomDomain *string              `json:"custom_domain" nullable:"true"`
	Published    bool                 `json:"published"`
	URL          string               `json:"url" doc:"Public address of the page."`
	Items        []StatusPageItemBody `json:"items"`
	CreatedAt    time.Time            `json:"created_at"`
	UpdatedAt    time.Time            `json:"updated_at"`
}

type statusPageOutput struct {
	Body StatusPageBody
}

type listStatusPagesOutput struct {
	Body struct {
		Pages []StatusPageBody `json:"pages"`
	}
}

// statusPageURL is the public address of p.
func statusPageURL(d Deps, p model.StatusPage) string {
	if p.CustomDomain != nil {
		return "https://" + *p.CustomDomain
	}
	return strings.TrimRight(d.Config.BaseURL, "/") + "/status/" + p.Slug
}

func toStatusPageBody(d Deps, p model.StatusPage) StatusPageBody {
	items := make([]StatusPageItemBody, len(p.Items))
	for i, it := range p.Items {
		items[i] = StatusPageItemBody{ID: it.ID, Section: it.Section, MonitorID: it.MonitorID, DisplayName: it.DisplayName}
	}
	return StatusPageBody{
		ID: p.ID, TeamID: p.TeamID, Slug: p.Slug, Title: p.Title, Description: p.Description, LogoURL: p.LogoURL,
		Theme: p.Theme, CustomDomain: p.CustomDomain, Published: p.Published, URL: statusPageURL(d, p),
		Items: items, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

// validateCustomDomain normalizes a custom domain (trimmed, lower-cased;
// "" means none) and checks it is a plain DNS host name that is not the
// Upsera host itself.
func validateCustomDomain(d Deps, raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	v := strings.ToLower(strings.TrimSpace(*raw))
	if v == "" {
		return nil, nil
	}
	bad := func(why string) (*string, error) {
		return nil, huma.Error422UnprocessableEntity("custom_domain " + why)
	}
	if len(v) > 253 {
		return bad("is too long")
	}
	if _, err := netip.ParseAddr(v); err == nil {
		return bad("must be a host name, not an IP address")
	}
	labels := strings.Split(v, ".")
	if len(labels) < 2 {
		return bad("must be a fully qualified host name such as status.example.com")
	}
	for _, l := range labels {
		if !dnsLabelRE.MatchString(l) {
			return bad("must be a plain host name (letters, digits and hyphens; no scheme, port or path)")
		}
	}
	if tld := labels[len(labels)-1]; strings.Trim(tld, "0123456789") == "" {
		return bad("must be a host name, not an IP address")
	}
	if base, err := url.Parse(d.Config.BaseURL); err == nil && strings.EqualFold(base.Hostname(), v) {
		return bad("must differ from this server's own host name")
	}
	return &v, nil
}

// validateLogoURL accepts "" or an https URL without credentials.
func validateLogoURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return "", huma.Error422UnprocessableEntity("logo_url must be an https:// URL")
	}
	return raw, nil
}

// buildStatusPage validates body and resolves its items against teamID's
// monitors. Display names default to the monitor's name.
// sameDomain reports whether two optional custom domains are the same
// (both already normalized to lower case, nil and "" meaning none).
func sameDomain(a, b *string) bool {
	av, bv := "", ""
	if a != nil {
		av = *a
	}
	if b != nil {
		bv = *b
	}
	return av == bv
}

// resolveCustomDomain decides which custom domain a save results in. A
// domain is claimed for the whole instance and, once published, approved for
// a TLS certificate, so only an instance admin using a session may set,
// change or remove it; team editors and API tokens must not be able to
// squat on a name. For them, leaving the field out (or null) means
// "unchanged", so they can keep editing a page whose domain an admin set;
// naming a different domain is a 403. For an admin the request is the full
// replacement, as for every other field.
func resolveCustomDomain(ctx context.Context, current, requested *string) (*string, error) {
	if sameDomain(current, requested) {
		return requested, nil
	}
	if _, err := requireAdmin(ctx); err == nil {
		return requested, nil
	}
	if requested == nil {
		return current, nil
	}
	return nil, huma.Error403Forbidden("only an instance admin can set or change a custom domain")
}

func buildStatusPage(ctx context.Context, d Deps, teamID int64, body StatusPageRequestBody) (model.StatusPage, error) {
	invalid := func(msg string) (model.StatusPage, error) {
		return model.StatusPage{}, huma.Error422UnprocessableEntity(msg)
	}
	slug := body.Slug
	if !statusSlugRE.MatchString(slug) {
		return invalid("slug must be 2-63 characters: lower-case letters, digits and hyphens, not starting with a hyphen")
	}
	if reservedSlugs[slug] {
		return invalid("slug \"" + slug + "\" is reserved")
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		return invalid("title is required")
	}
	theme := body.Theme
	if theme == "" {
		theme = model.ThemeAuto
	}
	if !theme.Valid() {
		return invalid("theme must be auto, light or dark")
	}
	logo, err := validateLogoURL(body.LogoURL)
	if err != nil {
		return model.StatusPage{}, err
	}
	domain, err := validateCustomDomain(d, body.CustomDomain)
	if err != nil {
		return model.StatusPage{}, err
	}
	if len(body.Items) > maxStatusPageItems {
		return invalid("a status page can list at most 100 monitors")
	}
	seen := make(map[int64]bool, len(body.Items))
	items := make([]model.StatusPageItem, len(body.Items))
	for i, it := range body.Items {
		if seen[it.MonitorID] {
			return invalid("a monitor can be listed only once per page")
		}
		seen[it.MonitorID] = true
		m, err := d.Store.GetMonitor(ctx, teamID, it.MonitorID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return invalid("monitor " + strconv.FormatInt(it.MonitorID, 10) + " does not exist in this team")
			}
			return model.StatusPage{}, mapStoreErr(d, err)
		}
		name := strings.TrimSpace(it.DisplayName)
		if name == "" {
			name = m.Name
			if utf8.RuneCountInString(name) > 100 {
				name = string([]rune(name)[:100])
			}
		}
		items[i] = model.StatusPageItem{Section: strings.TrimSpace(it.Section), MonitorID: m.ID, DisplayName: name, Position: i}
	}
	return model.StatusPage{
		TeamID: teamID, Slug: slug, Title: title, Description: strings.TrimSpace(body.Description), LogoURL: logo,
		Theme: theme, CustomDomain: domain, Published: body.Published, Items: items,
	}, nil
}

// mapStatusPageErr is mapStoreErr for page writes: a unique violation says
// which setting collided, and a missing row can only be a monitor deleted
// since validation.
func mapStatusPageErr(d Deps, err error) error {
	switch {
	case errors.Is(err, store.ErrConflict):
		msg := err.Error()
		switch {
		case strings.Contains(msg, "status_pages_slug_key"):
			return huma.Error409Conflict("a status page with that slug already exists")
		case strings.Contains(msg, "status_pages_custom_domain_key"):
			return huma.Error409Conflict("that custom domain is already used by another status page")
		case strings.Contains(msg, "status_page_items"):
			return huma.Error409Conflict("a monitor can be listed only once per page")
		}
	case errors.Is(err, store.ErrNotFound):
		return huma.Error422UnprocessableEntity("a listed monitor no longer exists")
	}
	return mapStoreErr(d, err)
}

// statusPagesChanged drops the cached renderings of slugs and refreshes the
// custom-domain map. Failure to reload is logged, not fatal: the change is
// already saved.
func statusPagesChanged(ctx context.Context, d Deps, slugs ...string) {
	done := make(map[string]bool, len(slugs))
	for _, s := range slugs {
		if s != "" && !done[s] {
			done[s] = true
			d.StatusPages.InvalidatePage(s)
		}
	}
	if err := d.StatusPages.ReloadDomains(ctx); err != nil {
		d.Logger.Warn("status pages: reloading custom domains failed", "error", err)
	}
}

// ---- Incidents ----

type incidentPathInput struct {
	TeamID     int64 `path:"teamID"`
	PageID     int64 `path:"pageID"`
	IncidentID int64 `path:"incidentID"`
}

// IncidentRequestBody creates an incident, optionally with a first
// timeline entry. It is published at once unless draft is set.
type IncidentRequestBody struct {
	Title     string               `json:"title" minLength:"1" maxLength:"200"`
	Severity  model.Severity       `json:"severity"`
	Status    model.IncidentStatus `json:"status,omitempty" doc:"Defaults to investigating."`
	Body      string               `json:"body,omitempty" maxLength:"2000" doc:"Text of the first timeline entry."`
	Draft     bool                 `json:"draft,omitempty" doc:"Keep the incident private until it is published."`
	StartedAt *time.Time           `json:"started_at,omitempty" doc:"Defaults to now."`
}

// IncidentEditRequestBody changes an incident's title and severity.
type IncidentEditRequestBody struct {
	Title    string         `json:"title" minLength:"1" maxLength:"200"`
	Severity model.Severity `json:"severity"`
}

// IncidentUpdateRequestBody appends a timeline entry and sets the status.
type IncidentUpdateRequestBody struct {
	Status model.IncidentStatus `json:"status"`
	Body   string               `json:"body" minLength:"1" maxLength:"2000"`
}

type createIncidentInput struct {
	TeamID int64 `path:"teamID"`
	PageID int64 `path:"pageID"`
	Body   IncidentRequestBody
}

type editIncidentInput struct {
	TeamID     int64 `path:"teamID"`
	PageID     int64 `path:"pageID"`
	IncidentID int64 `path:"incidentID"`
	Body       IncidentEditRequestBody
}

type addIncidentUpdateInput struct {
	TeamID     int64 `path:"teamID"`
	PageID     int64 `path:"pageID"`
	IncidentID int64 `path:"incidentID"`
	Body       IncidentUpdateRequestBody
}

// IncidentUpdateBody is one timeline entry.
type IncidentUpdateBody struct {
	ID        int64                `json:"id"`
	Status    model.IncidentStatus `json:"status"`
	Body      string               `json:"body"`
	CreatedAt time.Time            `json:"created_at"`
}

// IncidentBody is an incident with its timeline, oldest entry first.
type IncidentBody struct {
	ID          int64                `json:"id"`
	PageID      int64                `json:"page_id"`
	Title       string               `json:"title"`
	Severity    model.Severity       `json:"severity"`
	Status      model.IncidentStatus `json:"status"`
	Draft       bool                 `json:"draft"`
	Auto        bool                 `json:"auto" doc:"Created automatically from an alert."`
	StartedAt   time.Time            `json:"started_at"`
	ResolvedAt  *time.Time           `json:"resolved_at" nullable:"true"`
	PublishedAt *time.Time           `json:"published_at" nullable:"true"`
	Updates     []IncidentUpdateBody `json:"updates"`
	CreatedAt   time.Time            `json:"created_at"`
	UpdatedAt   time.Time            `json:"updated_at"`
}

func toIncidentBody(in model.Incident) IncidentBody {
	ups := make([]IncidentUpdateBody, len(in.Updates))
	for i, u := range in.Updates {
		ups[i] = IncidentUpdateBody{ID: u.ID, Status: u.Status, Body: u.Body, CreatedAt: u.CreatedAt}
	}
	return IncidentBody{
		ID: in.ID, PageID: in.PageID, Title: in.Title, Severity: in.Severity, Status: in.Status, Draft: in.Draft,
		Auto: in.AutoAlertID != nil, StartedAt: in.StartedAt, ResolvedAt: in.ResolvedAt, PublishedAt: in.PublishedAt,
		Updates: ups, CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt,
	}
}

type incidentOutput struct {
	Body IncidentBody
}

type listIncidentsOutput struct {
	Body struct {
		Incidents []IncidentBody `json:"incidents"`
	}
}

// ---- Subscribers ----

type subscriberPathInput struct {
	TeamID       int64 `path:"teamID"`
	PageID       int64 `path:"pageID"`
	SubscriberID int64 `path:"subscriberID"`
}

// WebhookSubscriberRequestBody adds a webhook that receives incident
// notices as JSON POSTs.
type WebhookSubscriberRequestBody struct {
	URL string `json:"url" minLength:"1" maxLength:"500" doc:"http:// or https:// URL, without credentials."`
}

type createWebhookSubscriberInput struct {
	TeamID int64 `path:"teamID"`
	PageID int64 `path:"pageID"`
	Body   WebhookSubscriberRequestBody
}

// SubscriberBody is one subscriber of a page. Webhook targets are shortened
// to scheme and host because the full URL may embed a secret.
type SubscriberBody struct {
	ID        int64                `json:"id"`
	Kind      model.SubscriberKind `json:"kind"`
	Target    string               `json:"target"`
	Confirmed bool                 `json:"confirmed"`
	CreatedAt time.Time            `json:"created_at"`
}

func toSubscriberBody(s model.Subscriber) SubscriberBody {
	target := s.Target
	if s.Kind == model.SubscriberWebhook {
		target = maskWebhookURL(target)
	}
	return SubscriberBody{ID: s.ID, Kind: s.Kind, Target: target, Confirmed: s.Confirmed, CreatedAt: s.CreatedAt}
}

// maskWebhookURL keeps scheme and host and hides the rest.
func maskWebhookURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "…"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// validateWebhookURL accepts an http(s) URL with a host and no credentials.
func validateWebhookURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", huma.Error422UnprocessableEntity("url must be an http:// or https:// URL")
	}
	if u.User != nil {
		return "", huma.Error422UnprocessableEntity("url must not contain a user name or password")
	}
	return raw, nil
}

type subscriberOutput struct {
	Body SubscriberBody
}

type listSubscribersOutput struct {
	Body struct {
		Subscribers []SubscriberBody `json:"subscribers"`
	}
}

// loadStatusPage returns pageID if it belongs to teamID, else 404.
func loadStatusPage(ctx context.Context, d Deps, teamID, pageID int64) (model.StatusPage, error) {
	p, err := d.Store.GetStatusPage(ctx, teamID, pageID)
	if err != nil {
		return model.StatusPage{}, mapStoreErr(d, err)
	}
	return p, nil
}

func registerStatusPageRoutes(api huma.API, d Deps) {
	huma.Get(api, "/api/teams/{teamID}/status-pages", func(ctx context.Context, in *teamPathInput) (*listStatusPagesOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		pages, err := d.Store.ListStatusPages(ctx, in.TeamID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listStatusPagesOutput{}
		out.Body.Pages = make([]StatusPageBody, len(pages))
		for i, p := range pages {
			out.Body.Pages[i] = toStatusPageBody(d, p)
		}
		return out, nil
	})

	huma.Get(api, "/api/teams/{teamID}/status-pages/{pageID}", func(ctx context.Context, in *statusPagePathInput) (*statusPageOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		p, err := loadStatusPage(ctx, d, in.TeamID, in.PageID)
		if err != nil {
			return nil, err
		}
		return &statusPageOutput{Body: toStatusPageBody(d, p)}, nil
	})

	huma.Post(api, "/api/teams/{teamID}/status-pages", func(ctx context.Context, in *createStatusPageInput) (*statusPageOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		page, err := buildStatusPage(ctx, d, in.TeamID, in.Body)
		if err != nil {
			return nil, err
		}
		if page.CustomDomain, err = resolveCustomDomain(ctx, nil, page.CustomDomain); err != nil {
			return nil, err
		}
		created, err := d.Store.CreateStatusPage(ctx, page)
		if err != nil {
			return nil, mapStatusPageErr(d, err)
		}
		statusPagesChanged(ctx, d, created.Slug)
		writeAudit(ctx, d, &in.TeamID, act, "status_page.create", "status_page", &created.ID,
			map[string]any{"name": created.Title, "slug": created.Slug, "custom_domain": created.CustomDomain, "published": created.Published})
		return &statusPageOutput{Body: toStatusPageBody(d, created)}, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated })

	huma.Put(api, "/api/teams/{teamID}/status-pages/{pageID}", func(ctx context.Context, in *updateStatusPageInput) (*statusPageOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		existing, err := loadStatusPage(ctx, d, in.TeamID, in.PageID)
		if err != nil {
			return nil, err
		}
		page, err := buildStatusPage(ctx, d, in.TeamID, in.Body)
		if err != nil {
			return nil, err
		}
		if page.CustomDomain, err = resolveCustomDomain(ctx, existing.CustomDomain, page.CustomDomain); err != nil {
			return nil, err
		}
		page.ID = existing.ID
		updated, err := d.Store.UpdateStatusPage(ctx, page)
		if err != nil {
			return nil, mapStatusPageErr(d, err)
		}
		statusPagesChanged(ctx, d, updated.Slug, existing.Slug)
		writeAudit(ctx, d, &in.TeamID, act, "status_page.update", "status_page", &updated.ID,
			map[string]any{"name": updated.Title, "slug": updated.Slug, "custom_domain": updated.CustomDomain, "published": updated.Published})
		return &statusPageOutput{Body: toStatusPageBody(d, updated)}, nil
	})

	huma.Delete(api, "/api/teams/{teamID}/status-pages/{pageID}", func(ctx context.Context, in *statusPagePathInput) (*emptyOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		existing, err := loadStatusPage(ctx, d, in.TeamID, in.PageID)
		if err != nil {
			return nil, err
		}
		if err := d.Store.DeleteStatusPage(ctx, in.TeamID, in.PageID); err != nil {
			return nil, mapStoreErr(d, err)
		}
		statusPagesChanged(ctx, d, existing.Slug)
		writeAudit(ctx, d, &in.TeamID, act, "status_page.delete", "status_page", &existing.ID,
			map[string]any{"name": existing.Title, "slug": existing.Slug})
		return nil, nil
	})

	// ---- Incidents ----

	huma.Get(api, "/api/teams/{teamID}/status-pages/{pageID}/incidents", func(ctx context.Context, in *statusPagePathInput) (*listIncidentsOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		if _, err := loadStatusPage(ctx, d, in.TeamID, in.PageID); err != nil {
			return nil, err
		}
		list, err := d.Store.ListIncidents(ctx, in.PageID, true, incidentListLimit)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listIncidentsOutput{}
		out.Body.Incidents = make([]IncidentBody, len(list))
		for i, inc := range list {
			out.Body.Incidents[i] = toIncidentBody(inc)
		}
		return out, nil
	})

	huma.Post(api, "/api/teams/{teamID}/status-pages/{pageID}/incidents", func(ctx context.Context, in *createIncidentInput) (*incidentOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		page, err := loadStatusPage(ctx, d, in.TeamID, in.PageID)
		if err != nil {
			return nil, err
		}
		title := strings.TrimSpace(in.Body.Title)
		if title == "" {
			return nil, huma.Error422UnprocessableEntity("title is required")
		}
		if !in.Body.Severity.Valid() {
			return nil, huma.Error422UnprocessableEntity("severity must be minor, major or critical")
		}
		status := in.Body.Status
		if status == "" {
			status = model.IncidentInvestigating
		}
		if !status.Valid() {
			return nil, huma.Error422UnprocessableEntity("invalid status")
		}
		inc := model.Incident{PageID: page.ID, Title: title, Severity: in.Body.Severity, Status: status, Draft: in.Body.Draft}
		if in.Body.StartedAt != nil {
			inc.StartedAt = *in.Body.StartedAt
		}
		created, err := d.Store.CreateIncident(ctx, inc, strings.TrimSpace(in.Body.Body))
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		d.StatusPages.InvalidatePage(page.Slug)
		writeAudit(ctx, d, &in.TeamID, act, "incident.create", "incident", &created.ID,
			map[string]any{"page_id": page.ID, "title": created.Title, "draft": created.Draft})
		return &incidentOutput{Body: toIncidentBody(created)}, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated })

	huma.Put(api, "/api/teams/{teamID}/status-pages/{pageID}/incidents/{incidentID}", func(ctx context.Context, in *editIncidentInput) (*incidentOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		page, err := loadStatusPage(ctx, d, in.TeamID, in.PageID)
		if err != nil {
			return nil, err
		}
		title := strings.TrimSpace(in.Body.Title)
		if title == "" {
			return nil, huma.Error422UnprocessableEntity("title is required")
		}
		if !in.Body.Severity.Valid() {
			return nil, huma.Error422UnprocessableEntity("severity must be minor, major or critical")
		}
		updated, err := d.Store.UpdateIncident(ctx, page.ID, in.IncidentID, title, in.Body.Severity)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		d.StatusPages.InvalidatePage(page.Slug)
		writeAudit(ctx, d, &in.TeamID, act, "incident.update", "incident", &updated.ID,
			map[string]any{"page_id": page.ID, "title": updated.Title})
		return &incidentOutput{Body: toIncidentBody(updated)}, nil
	})

	huma.Post(api, "/api/teams/{teamID}/status-pages/{pageID}/incidents/{incidentID}/updates", func(ctx context.Context, in *addIncidentUpdateInput) (*incidentOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		page, err := loadStatusPage(ctx, d, in.TeamID, in.PageID)
		if err != nil {
			return nil, err
		}
		body := strings.TrimSpace(in.Body.Body)
		if body == "" {
			return nil, huma.Error422UnprocessableEntity("body is required")
		}
		if !in.Body.Status.Valid() {
			return nil, huma.Error422UnprocessableEntity("invalid status")
		}
		updated, err := d.Store.AddIncidentUpdate(ctx, page.ID, in.IncidentID, in.Body.Status, body)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		d.StatusPages.InvalidatePage(page.Slug)
		writeAudit(ctx, d, &in.TeamID, act, "incident.add_update", "incident", &updated.ID,
			map[string]any{"page_id": page.ID, "title": updated.Title, "status": updated.Status})
		return &incidentOutput{Body: toIncidentBody(updated)}, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated })

	huma.Post(api, "/api/teams/{teamID}/status-pages/{pageID}/incidents/{incidentID}/publish", func(ctx context.Context, in *incidentPathInput) (*incidentOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		page, err := loadStatusPage(ctx, d, in.TeamID, in.PageID)
		if err != nil {
			return nil, err
		}
		published, err := d.Store.PublishIncident(ctx, page.ID, in.IncidentID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		d.StatusPages.InvalidatePage(page.Slug)
		writeAudit(ctx, d, &in.TeamID, act, "incident.publish", "incident", &published.ID,
			map[string]any{"page_id": page.ID, "title": published.Title})
		return &incidentOutput{Body: toIncidentBody(published)}, nil
	})

	huma.Delete(api, "/api/teams/{teamID}/status-pages/{pageID}/incidents/{incidentID}", func(ctx context.Context, in *incidentPathInput) (*emptyOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		page, err := loadStatusPage(ctx, d, in.TeamID, in.PageID)
		if err != nil {
			return nil, err
		}
		existing, err := d.Store.GetIncident(ctx, page.ID, in.IncidentID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		if err := d.Store.DeleteIncident(ctx, page.ID, in.IncidentID); err != nil {
			return nil, mapStoreErr(d, err)
		}
		d.StatusPages.InvalidatePage(page.Slug)
		writeAudit(ctx, d, &in.TeamID, act, "incident.delete", "incident", &existing.ID,
			map[string]any{"page_id": page.ID, "title": existing.Title})
		return nil, nil
	})

	// ---- Subscribers ----

	huma.Get(api, "/api/teams/{teamID}/status-pages/{pageID}/subscribers", func(ctx context.Context, in *statusPagePathInput) (*listSubscribersOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		if _, err := loadStatusPage(ctx, d, in.TeamID, in.PageID); err != nil {
			return nil, err
		}
		subs, err := d.Store.ListSubscribers(ctx, in.PageID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listSubscribersOutput{}
		out.Body.Subscribers = make([]SubscriberBody, len(subs))
		for i, s := range subs {
			out.Body.Subscribers[i] = toSubscriberBody(s)
		}
		return out, nil
	})

	huma.Post(api, "/api/teams/{teamID}/status-pages/{pageID}/subscribers", func(ctx context.Context, in *createWebhookSubscriberInput) (*subscriberOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		page, err := loadStatusPage(ctx, d, in.TeamID, in.PageID)
		if err != nil {
			return nil, err
		}
		target, err := validateWebhookURL(in.Body.URL)
		if err != nil {
			return nil, err
		}
		sub, created, err := d.Store.CreateSubscriber(ctx, model.Subscriber{
			PageID: page.ID, Kind: model.SubscriberWebhook, Target: target, Confirmed: true,
		})
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		if !created {
			return nil, huma.Error409Conflict("that webhook is already subscribed")
		}
		// The target is not audited: webhook URLs can contain secrets.
		writeAudit(ctx, d, &in.TeamID, act, "subscriber.create", "subscriber", &sub.ID,
			map[string]any{"page_id": page.ID, "kind": sub.Kind})
		return &subscriberOutput{Body: toSubscriberBody(sub)}, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated })

	huma.Delete(api, "/api/teams/{teamID}/status-pages/{pageID}/subscribers/{subscriberID}", func(ctx context.Context, in *subscriberPathInput) (*emptyOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		page, err := loadStatusPage(ctx, d, in.TeamID, in.PageID)
		if err != nil {
			return nil, err
		}
		sub, err := d.Store.GetSubscriber(ctx, in.SubscriberID)
		if err != nil || sub.PageID != page.ID {
			if err == nil {
				err = store.ErrNotFound
			}
			return nil, mapStoreErr(d, err)
		}
		if err := d.Store.DeleteSubscriber(ctx, page.ID, sub.ID); err != nil {
			return nil, mapStoreErr(d, err)
		}
		writeAudit(ctx, d, &in.TeamID, act, "subscriber.delete", "subscriber", &sub.ID,
			map[string]any{"page_id": page.ID, "kind": sub.Kind})
		return nil, nil
	})
}

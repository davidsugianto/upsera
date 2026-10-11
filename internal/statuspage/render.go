package statuspage

import (
	"bytes"
	"context"
	"embed"
	"encoding/xml"
	"fmt"
	"html/template"
	"sort"
	"strings"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/store"
)

const (
	uptimeDays    = 90
	pastIncidents = 14 * 24 * time.Hour
	feedLimit     = 20
	incidentLimit = 50
)

//go:embed status.css
var statusCSS string

//go:embed templates/*.html
var templateFS embed.FS

var pageTemplate = template.Must(template.ParseFS(templateFS, "templates/*.html")).Lookup("page.html")

// pageView is everything the page template may show. Nothing else reaches
// the HTML: no monitor ids, names, targets or heartbeat messages.
type pageView struct {
	Title, Description string
	LogoURL            string // https only, else ""
	Theme              string
	Base               string // "/status/<slug>", or "" on a custom domain
	FeedURL            string
	CSS                template.CSS // the embedded stylesheet, never user data
	Banner             bannerView
	Active, Past       []incidentView
	Sections           []sectionView
}

type bannerView struct{ Class, Label string }

type sectionView struct {
	Index int
	Name  string
	Items []itemView
}

type itemView struct {
	Name, StateClass, StateLabel string
	Uptime, Summary              string
	Cells                        []cellView
}

type cellView struct{ Class, Title string }

type incidentView struct {
	ID                      int64
	Title                   string
	Severity, SeverityLabel string
	StatusLabel             string
	Started, StartedISO     string
	Resolved, ResolvedISO   string
	Updates                 []updateView
}

type updateView struct {
	StatusLabel, Body, Time, TimeISO string
}

// loadPage fetches the published page; unknown and unpublished pages are
// both store.ErrNotFound.
func (s *Service) loadPage(ctx context.Context, slug string) (model.StatusPage, error) {
	page, err := s.opts.Store.GetPublishedPageBySlug(ctx, slug)
	if err != nil {
		return model.StatusPage{}, err
	}
	if !page.Published {
		return model.StatusPage{}, store.ErrNotFound
	}
	return page, nil
}

// renderPage builds the HTML of the page with this slug. custom links
// relative to the root instead of /status/<slug>.
func (s *Service) renderPage(ctx context.Context, slug string, custom bool) (cachedResponse, error) {
	page, err := s.loadPage(ctx, slug)
	if err != nil {
		return cachedResponse{}, err
	}
	incidents, err := s.opts.Store.ListIncidents(ctx, page.ID, false, incidentLimit)
	if err != nil {
		return cachedResponse{}, fmt.Errorf("list incidents: %w", err)
	}
	now := s.opts.Now().In(s.opts.Loc)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.opts.Loc)
	fromDay := todayStart.AddDate(0, 0, -(uptimeDays - 1))

	var uptime map[int64][]model.UptimeDay
	if ids := pageMonitorIDs(page.Items); len(ids) > 0 {
		if uptime, err = s.opts.Store.StatusPageUptime(ctx, ids, fromDay, todayStart); err != nil {
			return cachedResponse{}, fmt.Errorf("uptime: %w", err)
		}
	}

	v := pageView{
		Title: page.Title, Description: page.Description,
		Theme: string(page.Theme),
		Base:  "/status/" + page.Slug,
		CSS:   template.CSS(statusCSS),
	}
	if custom {
		v.Base = ""
	}
	v.FeedURL = v.Base + "/feed.xml"
	if !page.Theme.Valid() {
		v.Theme = string(model.ThemeAuto)
	}
	if strings.HasPrefix(strings.ToLower(page.LogoURL), "https://") {
		v.LogoURL = page.LogoURL
	}
	v.Banner = s.banner(page.Items)
	v.Sections = s.sections(page.Items, uptime, fromDay)
	for _, in := range incidents {
		if in.Draft {
			continue
		}
		switch {
		case !pageResolved(in):
			v.Active = append(v.Active, s.incidentView(in))
		case s.opts.Now().Sub(pageResolvedAt(in)) <= pastIncidents:
			v.Past = append(v.Past, s.incidentView(in))
		}
	}

	var buf bytes.Buffer
	if err := pageTemplate.Execute(&buf, v); err != nil {
		return cachedResponse{}, fmt.Errorf("execute template: %w", err)
	}
	return cachedResponse{body: buf.Bytes(), contentType: "text/html; charset=utf-8"}, nil
}

func pageMonitorIDs(items []model.StatusPageItem) []int64 {
	seen := make(map[int64]bool, len(items))
	var ids []int64
	for _, it := range items {
		if !seen[it.MonitorID] {
			seen[it.MonitorID] = true
			ids = append(ids, it.MonitorID)
		}
	}
	return ids
}

// banner summarises the live monitor states of the page's items. PENDING
// counts as operational.
func (s *Service) banner(items []model.StatusPageItem) bannerView {
	var down, maint, known int
	for _, it := range items {
		st, ok := s.opts.States.State(it.MonitorID)
		if !ok {
			continue
		}
		known++
		switch st.Status {
		case model.StatusDown:
			down++
		case model.StatusMaintenance:
			maint++
		}
	}
	switch {
	case down > 0 && down == len(items):
		return bannerView{"down", "Major outage"}
	case down > 0:
		return bannerView{"warn", "Partial outage"}
	case maint > 0:
		return bannerView{"maint", "Under maintenance"}
	case known == 0:
		return bannerView{"none", "Status unknown"}
	}
	return bannerView{"up", "All systems operational"}
}

// sections groups items by section name in order of first appearance.
func (s *Service) sections(items []model.StatusPageItem, uptime map[int64][]model.UptimeDay, fromDay time.Time) []sectionView {
	items = append([]model.StatusPageItem(nil), items...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Position < items[j].Position })
	var out []sectionView
	at := make(map[string]int)
	for _, it := range items {
		i, ok := at[it.Section]
		if !ok {
			i = len(out)
			at[it.Section] = i
			out = append(out, sectionView{Index: i, Name: it.Section})
		}
		out[i].Items = append(out[i].Items, s.itemView(it, uptime[it.MonitorID], fromDay))
	}
	return out
}

func (s *Service) itemView(it model.StatusPageItem, days []model.UptimeDay, fromDay time.Time) itemView {
	v := itemView{Name: it.DisplayName, StateClass: "none", StateLabel: "No data"}
	if st, ok := s.opts.States.State(it.MonitorID); ok {
		switch st.Status {
		case model.StatusDown:
			v.StateClass, v.StateLabel = "down", "Outage"
		case model.StatusMaintenance:
			v.StateClass, v.StateLabel = "maint", "Maintenance"
		default:
			v.StateClass, v.StateLabel = "up", "Operational"
		}
	}

	type tally struct{ checks, up int }
	byDay := make(map[string]tally, len(days))
	var total tally
	for _, d := range days {
		t := byDay[d.Day.Format(time.DateOnly)]
		t.checks += d.Checks
		t.up += d.Up
		byDay[d.Day.Format(time.DateOnly)] = t
		total.checks += d.Checks
		total.up += d.Up
	}
	v.Cells = make([]cellView, uptimeDays)
	var bad int
	for i := range v.Cells {
		day := fromDay.AddDate(0, 0, i).Format(time.DateOnly)
		t := byDay[day]
		switch {
		case t.checks == 0:
			v.Cells[i] = cellView{"c-none", day + ": no data"}
		case t.up*1000 >= t.checks*995:
			v.Cells[i] = cellView{"c-up", day + ": " + pagePercent(t.up, t.checks)}
		case t.up*100 >= t.checks*95:
			v.Cells[i] = cellView{"c-warn", day + ": " + pagePercent(t.up, t.checks)}
			bad++
		default:
			v.Cells[i] = cellView{"c-down", day + ": " + pagePercent(t.up, t.checks)}
			bad++
		}
	}
	v.Uptime = "-"
	v.Summary = v.Name + ": " + strings.ToLower(v.StateLabel) + ". No uptime data for the last 90 days."
	if total.checks > 0 {
		v.Uptime = pagePercent(total.up, total.checks)
		v.Summary = fmt.Sprintf("%s: %s. Uptime over the last 90 days: %s, with %d days below 99.5%%.",
			v.Name, strings.ToLower(v.StateLabel), v.Uptime, bad)
	}
	return v
}

// percent formats up/checks as a percentage with two decimals, truncated so
// that anything short of perfect never reads as 100%.
func pagePercent(up, checks int) string {
	n := up * 10000 / checks
	return fmt.Sprintf("%d.%02d%%", n/100, n%100)
}

func pageResolved(in model.Incident) bool {
	return in.Status == model.IncidentResolved || in.ResolvedAt != nil
}

// pageResolvedAt is when a resolved incident was resolved.
func pageResolvedAt(in model.Incident) time.Time {
	switch {
	case in.ResolvedAt != nil:
		return *in.ResolvedAt
	case len(in.Updates) > 0:
		return in.Updates[len(in.Updates)-1].CreatedAt
	}
	return in.UpdatedAt
}

func pageStatusLabel(st model.IncidentStatus) string {
	switch st {
	case model.IncidentInvestigating:
		return "Investigating"
	case model.IncidentIdentified:
		return "Identified"
	case model.IncidentMonitoring:
		return "Monitoring"
	case model.IncidentResolved:
		return "Resolved"
	}
	return string(st)
}

func pageSeverityLabel(sv model.Severity) string {
	switch sv {
	case model.SeverityMinor:
		return "Minor"
	case model.SeverityMajor:
		return "Major"
	case model.SeverityCritical:
		return "Critical"
	}
	return string(sv)
}

// incidentView renders an incident with its updates newest first.
func (s *Service) incidentView(in model.Incident) incidentView {
	v := incidentView{
		ID: in.ID, Title: in.Title,
		Severity: string(in.Severity), SeverityLabel: pageSeverityLabel(in.Severity),
		StatusLabel: pageStatusLabel(in.Status),
	}
	v.Started, v.StartedISO = s.formatTime(in.StartedAt)
	if pageResolved(in) {
		v.Resolved, v.ResolvedISO = s.formatTime(pageResolvedAt(in))
	}
	for i := len(in.Updates) - 1; i >= 0; i-- {
		u := in.Updates[i]
		uv := updateView{StatusLabel: pageStatusLabel(u.Status), Body: u.Body}
		uv.Time, uv.TimeISO = s.formatTime(u.CreatedAt)
		v.Updates = append(v.Updates, uv)
	}
	return v
}

func (s *Service) formatTime(t time.Time) (human, iso string) {
	return t.In(s.opts.Loc).Format("2 Jan 2006 15:04 MST"), t.UTC().Format(time.RFC3339)
}

// RSS 2.0 feed.

type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title         string    `xml:"title"`
	Link          string    `xml:"link"`
	Description   string    `xml:"description"`
	LastBuildDate string    `xml:"lastBuildDate"`
	Items         []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string  `xml:"title"`
	Link        string  `xml:"link"`
	GUID        rssGUID `xml:"guid"`
	PubDate     string  `xml:"pubDate"`
	Description string  `xml:"description"`
}

type rssGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

// renderFeed builds the RSS feed of the page's last published incidents.
func (s *Service) renderFeed(ctx context.Context, slug string) (cachedResponse, error) {
	page, err := s.loadPage(ctx, slug)
	if err != nil {
		return cachedResponse{}, err
	}
	incidents, err := s.opts.Store.ListIncidents(ctx, page.ID, false, feedLimit)
	if err != nil {
		return cachedResponse{}, fmt.Errorf("list incidents: %w", err)
	}
	base := s.pageBase(page)
	desc := page.Description
	if desc == "" {
		desc = "Status updates for " + page.Title
	}
	feed := rssFeed{Version: "2.0", Channel: rssChannel{
		Title: page.Title, Link: base, Description: desc,
		LastBuildDate: s.opts.Now().UTC().Format(time.RFC1123Z),
	}}
	for _, in := range incidents {
		if in.Draft {
			continue
		}
		link := fmt.Sprintf("%s#incident-%d", base, in.ID)
		published := in.StartedAt
		if in.PublishedAt != nil {
			published = *in.PublishedAt
		}
		d := "Status: " + pageStatusLabel(in.Status)
		if n := len(in.Updates); n > 0 && in.Updates[n-1].Body != "" {
			d += ". " + in.Updates[n-1].Body
		}
		feed.Channel.Items = append(feed.Channel.Items, rssItem{
			Title: in.Title, Link: link,
			GUID:        rssGUID{IsPermaLink: "false", Value: link},
			PubDate:     published.UTC().Format(time.RFC1123Z),
			Description: d,
		})
	}
	body, err := xml.MarshalIndent(feed, "", "  ")
	if err != nil {
		return cachedResponse{}, fmt.Errorf("marshal feed: %w", err)
	}
	return cachedResponse{
		body:        append([]byte(xml.Header), body...),
		contentType: "application/rss+xml; charset=utf-8",
	}, nil
}

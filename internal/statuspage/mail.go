package statuspage

import (
	"fmt"
	"strings"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// timeFormat is how times appear in emails, in the server time zone.
const timeFormat = "2006-01-02 15:04 MST"

// oneLine removes CR and LF so a value cannot add mail headers.
func oneLine(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// confirmMessage is the double opt-in email for an email subscriber.
func (s *Service) confirmMessage(page model.StatusPage, link string) (subject, body string) {
	subject = oneLine("Confirm your subscription to " + page.Title)
	body = fmt.Sprintf("Confirm that you want email notices about incidents on %s:\n\n%s\n\n"+
		"If you did not ask for this, ignore this email and you will not be subscribed.\n",
		page.Title, link)
	return subject, body
}

// pickUpdate returns the update the delivery announces: the one with id,
// else the latest; nil when the incident has no updates.
func pickUpdate(in model.Incident, id *int64) *model.IncidentUpdate {
	if id != nil {
		for i := range in.Updates {
			if in.Updates[i].ID == *id {
				return &in.Updates[i]
			}
		}
	}
	if n := len(in.Updates); n > 0 {
		return &in.Updates[n-1]
	}
	return nil
}

// incidentResolved reports whether the notice announces a resolution: the
// incident is resolved and so is the announced update (if any).
func incidentResolved(in model.Incident, up *model.IncidentUpdate) bool {
	return in.Status == model.IncidentResolved && (up == nil || up.Status == model.IncidentResolved)
}

// incidentMessage is the plain-text notice for an email subscriber.
func (s *Service) incidentMessage(page model.StatusPage, in model.Incident, updateID *int64, unsubscribeLink string) (subject, body string) {
	up := pickUpdate(in, updateID)
	subject = "[" + page.Title + "] "
	if incidentResolved(in, up) {
		subject += "Resolved: "
	}
	subject = oneLine(subject + in.Title)

	at := in.StartedAt
	if up != nil {
		at = up.CreatedAt
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nSeverity: %s\nStatus: %s\nTime: %s\n", in.Title, in.Severity, in.Status, at.In(s.opts.Loc).Format(timeFormat))
	if up != nil {
		fmt.Fprintf(&b, "\nUpdate (%s):\n%s\n", up.Status, up.Body)
	}
	fmt.Fprintf(&b, "\nStatus page: %s\n\nUnsubscribe: %s\n", s.pageBase(page), unsubscribeLink)
	return subject, b.String()
}

// webhookBody is the JSON sent to webhook subscribers.
type webhookBody struct {
	Event string `json:"event"`
	Page  struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	} `json:"page"`
	Incident struct {
		ID         int64   `json:"id"`
		Title      string  `json:"title"`
		Severity   string  `json:"severity"`
		Status     string  `json:"status"`
		StartedAt  string  `json:"started_at"`
		ResolvedAt *string `json:"resolved_at"`
	} `json:"incident"`
	Update *webhookUpdate `json:"update"`
}

type webhookUpdate struct {
	Status    string `json:"status"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// webhookPayload builds the webhook body; Update is nil without updates.
func (s *Service) webhookPayload(page model.StatusPage, in model.Incident, updateID *int64) webhookBody {
	var p webhookBody
	p.Event = "incident"
	p.Page.Title = page.Title
	p.Page.URL = s.pageBase(page)
	p.Incident.ID = in.ID
	p.Incident.Title = in.Title
	p.Incident.Severity = string(in.Severity)
	p.Incident.Status = string(in.Status)
	p.Incident.StartedAt = in.StartedAt.UTC().Format(time.RFC3339)
	if in.ResolvedAt != nil {
		p.Incident.ResolvedAt = new(in.ResolvedAt.UTC().Format(time.RFC3339))
	}
	if up := pickUpdate(in, updateID); up != nil {
		p.Update = &webhookUpdate{Status: string(up.Status), Body: up.Body, CreatedAt: up.CreatedAt.UTC().Format(time.RFC3339)}
	}
	return p
}

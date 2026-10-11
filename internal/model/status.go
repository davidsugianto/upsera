package model

import (
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// stringEnum builds the OpenAPI schema of a string enum.
func stringEnum(vals ...string) *huma.Schema {
	enum := make([]any, len(vals))
	for i, v := range vals {
		enum[i] = v
	}
	return &huma.Schema{Type: huma.TypeString, Enum: enum}
}

// Theme is a status page's colour scheme ("auto" follows the visitor).
type Theme string

const (
	ThemeAuto  Theme = "auto"
	ThemeLight Theme = "light"
	ThemeDark  Theme = "dark"
)

func (t Theme) Valid() bool { return t == ThemeAuto || t == ThemeLight || t == ThemeDark }

func (Theme) Schema(huma.Registry) *huma.Schema { return stringEnum("auto", "light", "dark") }

// Severity of an incident.
type Severity string

const (
	SeverityMinor    Severity = "minor"
	SeverityMajor    Severity = "major"
	SeverityCritical Severity = "critical"
)

func (s Severity) Valid() bool {
	return s == SeverityMinor || s == SeverityMajor || s == SeverityCritical
}

func (Severity) Schema(huma.Registry) *huma.Schema { return stringEnum("minor", "major", "critical") }

// IncidentStatus is where an incident is in its lifecycle.
type IncidentStatus string

const (
	IncidentInvestigating IncidentStatus = "investigating"
	IncidentIdentified    IncidentStatus = "identified"
	IncidentMonitoring    IncidentStatus = "monitoring"
	IncidentResolved      IncidentStatus = "resolved"
)

func (s IncidentStatus) Valid() bool {
	switch s {
	case IncidentInvestigating, IncidentIdentified, IncidentMonitoring, IncidentResolved:
		return true
	}
	return false
}

func (IncidentStatus) Schema(huma.Registry) *huma.Schema {
	return stringEnum("investigating", "identified", "monitoring", "resolved")
}

// SubscriberKind is how a subscriber is notified.
type SubscriberKind string

const (
	SubscriberEmail   SubscriberKind = "email"
	SubscriberWebhook SubscriberKind = "webhook"
)

func (k SubscriberKind) Valid() bool { return k == SubscriberEmail || k == SubscriberWebhook }

func (SubscriberKind) Schema(huma.Registry) *huma.Schema { return stringEnum("email", "webhook") }

// StatusPage is a public page built from a team's monitors. Only
// Published pages are served; Items carry the public names, so monitor
// names, targets and messages are never shown.
type StatusPage struct {
	ID           int64
	TeamID       int64
	Slug         string
	Title        string
	Description  string
	LogoURL      string // https URL or ""
	Theme        Theme
	CustomDomain *string // lower-case host name, unique across the instance
	Published    bool
	Items        []StatusPageItem // ordered by Position
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// StatusPageItem puts one monitor on a page under a section heading.
type StatusPageItem struct {
	ID          int64
	Section     string // "" = no heading
	MonitorID   int64
	DisplayName string
	Position    int
}

// PageDomain maps a published page's custom domain to its slug.
type PageDomain struct {
	Domain string
	Slug   string
}

// Incident is a message on a status page. Draft incidents are visible
// only in the dashboard; auto-drafted ones carry the alert they came from.
type Incident struct {
	ID          int64
	PageID      int64
	Title       string
	Severity    Severity
	Status      IncidentStatus
	Draft       bool
	AutoAlertID *string // alert UUID for auto-drafted incidents
	StartedAt   time.Time
	ResolvedAt  *time.Time
	PublishedAt *time.Time
	Updates     []IncidentUpdate // oldest first
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// IncidentUpdate is one timeline entry of an incident.
type IncidentUpdate struct {
	ID         int64
	IncidentID int64
	Status     IncidentStatus
	Body       string
	CreatedAt  time.Time
}

// Subscriber receives published incident notices for one page. Email
// subscribers must confirm; webhook subscribers are added by editors and
// are confirmed from the start.
type Subscriber struct {
	ID        int64
	PageID    int64
	Kind      SubscriberKind
	Target    string // email address or webhook URL
	Confirmed bool
	CreatedAt time.Time
}

// DeliveryKind is what a subscriber_deliveries row sends.
type DeliveryKind string

const (
	DeliveryConfirm  DeliveryKind = "confirm"
	DeliveryIncident DeliveryKind = "incident"
)

// Delivery is one outbox entry with everything needed to send it.
type Delivery struct {
	ID         int64
	Kind       DeliveryKind
	Attempts   int
	Subscriber Subscriber
	Page       StatusPage // no Items
	Incident   *Incident  // incident deliveries only, with Updates
	UpdateID   *int64     // the update being announced; nil = the incident as a whole
}

// DraftCandidate is an open alert on a monitor listed on a published page
// that has no incident yet.
type DraftCandidate struct {
	PageID      int64
	MonitorID   int64
	DisplayName string // the item's public name
	AlertID     string
	StartedAt   time.Time // alert incident_start
}

// UptimeDay is defined in model.go; PageUptime is one monitor's recent days.
type PageUptime struct {
	MonitorID int64
	Days      []UptimeDay // ascending; includes today
}

// SMTPSettings is the instance-wide mail server used for subscriber email.
type SMTPSettings struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	From     string `json:"from"`
	TLS      string `json:"tls"` // "starttls", "tls" or "none"
}

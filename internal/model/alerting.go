package model

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// stringEnumSchema builds the OpenAPI schema of a string enum from its
// exported value list, so the spec can never drift from Valid.
func stringEnumSchema[T ~string](vals []T) *huma.Schema {
	enum := make([]any, len(vals))
	for i, v := range vals {
		enum[i] = string(v)
	}
	return &huma.Schema{Type: huma.TypeString, Enum: enum}
}

// ChannelType selects the notifier of a notification channel.
type ChannelType string

const (
	ChannelTelegram ChannelType = "telegram"
	ChannelDiscord  ChannelType = "discord"
	ChannelSlack    ChannelType = "slack"     // incoming webhook, no buttons
	ChannelSlackApp ChannelType = "slack_app" // bot + Socket Mode, Acknowledge button
	ChannelSMTP     ChannelType = "smtp"
	ChannelWebhook  ChannelType = "webhook"
)

// ChannelTypes lists every supported channel type, in display order.
var ChannelTypes = []ChannelType{
	ChannelTelegram, ChannelDiscord, ChannelSlack, ChannelSlackApp, ChannelSMTP, ChannelWebhook,
}

// Valid reports whether t is a supported channel type.
func (t ChannelType) Valid() bool { return slices.Contains(ChannelTypes, t) }

// Schema implements huma.SchemaProvider.
func (ChannelType) Schema(huma.Registry) *huma.Schema { return stringEnumSchema(ChannelTypes) }

// Channel is a notification channel. Config is the plaintext JSON config
// (it is encrypted at rest). DecryptErr is set, and Config nil, when the
// stored config cannot be decrypted.
type Channel struct {
	ID         int64
	TeamID     int64
	Type       ChannelType
	Name       string
	Config     json.RawMessage
	IsDefault  bool
	DecryptErr string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// EscalationStep notifies ChannelIDs, then waits DelayS before the next
// step fires (unless the alert is acknowledged or resolved).
type EscalationStep struct {
	ChannelIDs []int64
	DelayS     int
}

// EscalationPolicy is an ordered list of escalation steps.
type EscalationPolicy struct {
	ID        int64
	TeamID    int64
	Name      string
	Steps     []EscalationStep
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AlertEvent is the kind of a notification.
type AlertEvent string

const (
	EventDown       AlertEvent = "down"
	EventRecovered  AlertEvent = "recovered"
	EventFlapping   AlertEvent = "flapping"
	EventCertExpiry AlertEvent = "cert_expiry"
	EventTest       AlertEvent = "test"
)

// AlertEvents lists every notification kind.
var AlertEvents = []AlertEvent{EventDown, EventRecovered, EventFlapping, EventCertExpiry, EventTest}

// Valid reports whether e is a known event kind.
func (e AlertEvent) Valid() bool { return slices.Contains(AlertEvents, e) }

// Schema implements huma.SchemaProvider.
func (AlertEvent) Schema(huma.Registry) *huma.Schema { return stringEnumSchema(AlertEvents) }

// AckSource is where an acknowledgement came from.
type AckSource string

const (
	AckWeb      AckSource = "web"
	AckSlack    AckSource = "slack"
	AckTelegram AckSource = "telegram"
)

// AckSources lists every acknowledgement source.
var AckSources = []AckSource{AckWeb, AckSlack, AckTelegram}

// Valid reports whether s is a known acknowledgement source.
func (s AckSource) Valid() bool { return slices.Contains(AckSources, s) }

// Schema implements huma.SchemaProvider.
func (AckSource) Schema(huma.Registry) *huma.Schema { return stringEnumSchema(AckSources) }

// AckBy identifies who acknowledged an alert. UserID is set only for web
// acknowledgements by a logged-in user.
type AckBy struct {
	UserID *int64
	Source AckSource
	Name   string
}

// Alert resolutions.
const (
	ResolutionRecovered   = "recovered"
	ResolutionMaintenance = "maintenance"
)

// Alert is one outage of one monitor, from the DOWN transition until it is
// resolved. At most one alert per monitor is open (ResolvedAt nil).
type Alert struct {
	ID                 string // UUID v7
	TeamID             int64
	MonitorID          int64
	IncidentStart      time.Time
	OpenedAt           time.Time
	Message            string
	Step               int // index of the last fired escalation step, -1 = none
	NextEscalationAt   *time.Time
	NotifiedChannelIDs []int64
	Suppressed         bool
	Flapping           bool
	AckedAt            *time.Time
	AckedByUserID      *int64
	AckSource          AckSource // "" until acknowledged
	AckedByName        string
	ResolvedAt         *time.Time
	Resolution         string // "", ResolutionRecovered or ResolutionMaintenance
	UpdatedAt          time.Time
}

// NotificationLogEntry records one send attempt on one channel.
type NotificationLogEntry struct {
	ID        int64
	TeamID    int64
	ChannelID *int64
	MonitorID *int64
	AlertID   *string
	Event     AlertEvent
	DedupeKey string
	Attempt   int
	OK        bool
	Error     string
	At        time.Time
}

// Recurrence is how a maintenance window repeats.
type Recurrence string

const (
	RecurNone   Recurrence = "none"
	RecurDaily  Recurrence = "daily"
	RecurWeekly Recurrence = "weekly"
)

// Recurrences lists every recurrence.
var Recurrences = []Recurrence{RecurNone, RecurDaily, RecurWeekly}

// Valid reports whether r is a known recurrence.
func (r Recurrence) Valid() bool { return slices.Contains(Recurrences, r) }

// Schema implements huma.SchemaProvider.
func (Recurrence) Schema(huma.Registry) *huma.Schema { return stringEnumSchema(Recurrences) }

// MaintenanceWindow puts MonitorIDs into MAINTENANCE (no checks, no
// alerts) between StartsAt and EndsAt, optionally repeating daily or
// weekly at the same local wall-clock time.
type MaintenanceWindow struct {
	ID         int64
	TeamID     int64
	Name       string
	StartsAt   time.Time
	EndsAt     time.Time
	Recurrence Recurrence
	MonitorIDs []int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Transition is a monitor state change, emitted by the scheduler onto the
// alerting event bus.
type Transition struct {
	MonitorID int64
	From, To  Status
	At        time.Time
	// Since is the new state's Since (for DOWN: the incident start, i.e.
	// the first failed check).
	Since time.Time
	// PrevSince is the previous state's Since (for DOWN→UP: the outage
	// start).
	PrevSince time.Time
	Message   string
	// Flapping is the flap state after this transition; FlapChanged is
	// set when this transition started or ended flapping.
	Flapping    bool
	FlapChanged bool
}

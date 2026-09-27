// Package model holds the domain types shared by the store, scheduler,
// checkers and API.
package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// Status is the result of one check, or the current state of a monitor.
// Values match what is stored in heartbeats.status and monitor_state.status.
type Status int16

const (
	StatusDown        Status = 0
	StatusUp          Status = 1
	StatusPending     Status = 2
	StatusMaintenance Status = 3
)

func (s Status) String() string {
	switch s {
	case StatusDown:
		return "down"
	case StatusUp:
		return "up"
	case StatusPending:
		return "pending"
	case StatusMaintenance:
		return "maintenance"
	}
	return fmt.Sprintf("status(%d)", int16(s))
}

// MarshalText encodes the status by name so the API shows "up"/"down".
func (s Status) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// UnmarshalText parses a status name.
func (s *Status) UnmarshalText(b []byte) error {
	switch string(b) {
	case "down":
		*s = StatusDown
	case "up":
		*s = StatusUp
	case "pending":
		*s = StatusPending
	case "maintenance":
		*s = StatusMaintenance
	default:
		return fmt.Errorf("unknown status %q", b)
	}
	return nil
}

// Schema implements huma.SchemaProvider so the OpenAPI spec shows status as
// a string enum instead of the underlying int16, and can never drift from
// String/UnmarshalText.
func (Status) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{
		Type: huma.TypeString,
		Enum: []any{
			StatusDown.String(), StatusUp.String(), StatusPending.String(), StatusMaintenance.String(),
		},
	}
}

// MonitorType selects the checker.
type MonitorType string

const (
	TypeHTTP    MonitorType = "http"
	TypeKeyword MonitorType = "keyword"
	TypeTCP     MonitorType = "tcp"
	TypePing    MonitorType = "ping"
	TypeDNS     MonitorType = "dns"
	TypePush    MonitorType = "push"
)

// MonitorTypes lists every supported type, in display order.
var MonitorTypes = []MonitorType{TypeHTTP, TypeKeyword, TypeTCP, TypePing, TypeDNS, TypePush}

// Valid reports whether t is a supported monitor type.
func (t MonitorType) Valid() bool {
	for _, v := range MonitorTypes {
		if v == t {
			return true
		}
	}
	return false
}

// Schema implements huma.SchemaProvider so the OpenAPI spec enum can never
// drift from MonitorTypes.
func (MonitorType) Schema(huma.Registry) *huma.Schema {
	enum := make([]any, len(MonitorTypes))
	for i, t := range MonitorTypes {
		enum[i] = string(t)
	}
	return &huma.Schema{Type: huma.TypeString, Enum: enum}
}

// Monitor is one configured check. Config holds the type-specific settings
// as validated JSON (see checker.ValidateConfig).
type Monitor struct {
	ID             int64
	TeamID         int64
	Name           string
	Type           MonitorType
	Config         json.RawMessage
	IntervalS      int
	RetryIntervalS int
	Retries        int
	TimeoutS       int
	Paused         bool
	GroupName      string
	Tags           []string
	PushToken      string // only for push monitors; empty otherwise
	// ParentID is the monitor this one depends on: while the parent is
	// DOWN, this monitor's alerts are suppressed.
	ParentID           *int64
	EscalationPolicyID *int64
	// ChannelIDs are the channels alerted when no escalation policy is
	// set (empty = the team's default channels). Always sorted.
	ChannelIDs []int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Monitor field limits, enforced by the API and by table CHECK constraints.
const (
	MinIntervalS     = 20
	MaxIntervalS     = 86400
	MaxRetries       = 10
	MinTimeoutS      = 1
	DefaultIntervalS = 60
	DefaultRetryS    = 20
	DefaultRetries   = 1
	DefaultTimeoutS  = 10
	MaxMessageLen    = 200
)

// Heartbeat is one check result from one probe.
type Heartbeat struct {
	MonitorID int64
	ProbeID   int64
	Time      time.Time
	Status    Status
	LatencyMs int32
	Message   string
}

// TruncateMessage sanitizes a heartbeat message (replacing invalid UTF-8
// with U+FFFD and stripping NUL bytes, which Postgres text columns reject)
// then caps it at MaxMessageLen runes.
func TruncateMessage(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.ReplaceAll(s, "\x00", "")
	r := []rune(s)
	if len(r) <= MaxMessageLen {
		return s
	}
	return string(r[:MaxMessageLen-1]) + "…"
}

// MonitorState is the persisted runtime state of a monitor. It survives
// restarts; the scheduler keeps the live copy in memory.
type MonitorState struct {
	MonitorID           int64
	Status              Status
	Since               time.Time
	LastCheckAt         time.Time
	ConsecutiveFailures int
	TLSExpiresAt        *time.Time
	// FlapCount is the number of DOWN/UP state changes in the last hour.
	FlapCount int
}

// Role is a user's role within one team.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return r == RoleOwner || r == RoleEditor || r == RoleViewer }

// AtLeast reports whether r grants at least the permissions of min.
func (r Role) AtLeast(min Role) bool { return r.rank() >= min.rank() }

func (r Role) rank() int {
	switch r {
	case RoleOwner:
		return 3
	case RoleEditor:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// Schema implements huma.SchemaProvider so the OpenAPI spec enum can never
// drift from RoleOwner/RoleEditor/RoleViewer.
func (Role) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{
		Type: huma.TypeString,
		Enum: []any{string(RoleOwner), string(RoleEditor), string(RoleViewer)},
	}
}

// User is an account. IsAdmin marks an instance admin.
type User struct {
	ID           int64
	Email        string
	Name         string
	PasswordHash string
	IsAdmin      bool
	CreatedAt    time.Time
}

// Team owns monitors, tokens and (later) channels and status pages.
type Team struct {
	ID        int64
	Name      string
	CreatedAt time.Time
}

// TeamMembership is a team together with the caller's role in it.
type TeamMembership struct {
	Team Team
	Role Role
}

// Member is a user's membership in a team.
type Member struct {
	UserID int64
	Email  string
	Name   string
	Role   Role
}

// Session is a logged-in browser session.
type Session struct {
	ID        int64
	UserID    int64
	CSRFToken string
	ExpiresAt time.Time
}

// TokenScope limits what an API token may do.
type TokenScope string

const (
	ScopeRead  TokenScope = "read"
	ScopeWrite TokenScope = "write"
)

// Valid reports whether s is a known scope.
func (s TokenScope) Valid() bool { return s == ScopeRead || s == ScopeWrite }

// Schema implements huma.SchemaProvider so the OpenAPI spec enum can never
// drift from ScopeRead/ScopeWrite.
func (TokenScope) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Enum: []any{string(ScopeRead), string(ScopeWrite)}}
}

// APIToken is a team-scoped bearer token. Only its SHA-256 hash is stored.
type APIToken struct {
	ID         int64
	TeamID     int64
	Name       string
	Scope      TokenScope
	CreatedBy  *int64
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
}

// AuditEntry records who changed what. Exactly one of ActorUserID and
// ActorTokenID is set for API-driven changes.
type AuditEntry struct {
	ID           int64
	TeamID       *int64
	ActorUserID  *int64
	ActorTokenID *int64
	Action       string
	TargetType   string
	TargetID     *int64
	Details      json.RawMessage
	At           time.Time
}

// InstanceSettings are instance-wide settings managed by instance admins.
// RetentionDays nil means "use HEARTBEAT_RETENTION_DAYS".
type InstanceSettings struct {
	BlockPrivateTargets bool
	RetentionDays       *int
	UpdatedAt           time.Time
}

// UptimeDay is one row of the daily rollup.
type UptimeDay struct {
	MonitorID    int64
	Day          time.Time
	Checks       int
	Up           int
	AvgLatencyMs int
	P95LatencyMs int
}

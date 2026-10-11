package model

import (
	"slices"
	"time"
)

// LocalRegion is the region of the server's built-in checker (probe "local").
const LocalRegion = "local"

// Region limits.
const (
	MaxRegions = 10
	// CredentialPrefix starts every probe credential ("upp_..."); registration
	// tokens start with RegistrationPrefix. Team API tokens use "ups_".
	CredentialPrefix   = "upp_"
	RegistrationPrefix = "upr_"
)

// Probe is a remote checker (or the built-in "local" one). A probe exists
// from the moment an admin creates it; Registered turns true once its
// one-time registration token has been exchanged for a credential.
type Probe struct {
	ID                    int64
	Name                  string
	Region                string
	Registered            bool
	RegistrationExpiresAt *time.Time // nil once redeemed
	LastSeenAt            *time.Time
	Version               string
	CreatedAt             time.Time
}

// ProbeAuth is what the in-memory probe registry needs to authenticate a
// probe: the SHA-256 of its credential (nil while unregistered).
type ProbeAuth struct {
	ID         int64
	Name       string
	Region     string
	TokenHash  []byte
	LastSeenAt *time.Time
}

// ProbeSeen is a last-contact observation to persist.
type ProbeSeen struct {
	ID      int64
	At      time.Time
	Version string
}

// RegionResult is one raw check result from one region (a probe or the
// local checker): Status is StatusUp or StatusDown only. Retries are
// counted by the server, per region.
type RegionResult struct {
	MonitorID    int64
	Time         time.Time
	Status       Status
	LatencyMs    int32
	Message      string
	TLSExpiresAt *time.Time
}

// RegionState is the live state of one monitor in one region, kept in
// MonitorState.Regions (and persisted as JSON in monitor_state.regions).
type RegionState struct {
	Status              Status    `json:"status"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	LastResultAt        time.Time `json:"last_result_at"`
	LatencyMs           int32     `json:"latency_ms"`
	Message             string    `json:"message"`
}

// EffectiveRegions returns the regions m is checked from; a monitor loaded
// without regions (zero value) is checked from LocalRegion.
func (m Monitor) EffectiveRegions() []string {
	if len(m.Regions) == 0 {
		return []string{LocalRegion}
	}
	return m.Regions
}

// EffectiveQuorum returns the quorum, defaulting to 1.
func (m Monitor) EffectiveQuorum() int {
	if m.Quorum < 1 {
		return 1
	}
	return m.Quorum
}

// LocalOnly reports whether m is checked only by the built-in checker. Such
// monitors keep the original single-region code path.
func (m Monitor) LocalOnly() bool {
	r := m.EffectiveRegions()
	return len(r) == 1 && r[0] == LocalRegion
}

// InRegion reports whether m is checked from region.
func (m Monitor) InRegion(region string) bool {
	return slices.Contains(m.EffectiveRegions(), region)
}

// DefaultQuorum is the majority of n regions: n/2+1.
func DefaultQuorum(n int) int { return n/2 + 1 }

// RemoteCapable reports whether a monitor of type t can run on a remote
// probe (push and docker monitors are local only).
func (t MonitorType) RemoteCapable() bool {
	switch t {
	case TypeHTTP, TypeKeyword, TypeTCP, TypePing, TypeDNS:
		return true
	}
	return false
}

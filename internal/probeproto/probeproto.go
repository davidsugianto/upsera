// Package probeproto holds the wire types of the probe protocol, shared by
// the server (internal/api) and the agent (internal/probe).
//
//	POST /api/probe/register     RegisterRequest   -> RegisterResponse (no auth)
//	GET  /api/probe/assignments  Bearer upp_...    -> AssignmentsResponse (ETag / If-None-Match -> 304)
//	POST /api/probe/results      Bearer upp_...    ResultsRequest -> ResultsResponse
package probeproto

import (
	"encoding/json"
	"time"
)

const (
	PathRegister    = "/api/probe/register"
	PathAssignments = "/api/probe/assignments"
	PathResults     = "/api/probe/results"

	// Limits enforced by the server on POST /api/probe/results.
	MaxResultsPerBatch = 1000
	MaxBatchBytes      = 1 << 20

	// PollInterval is how often the agent polls assignments.
	PollInterval = 15 * time.Second
)

type RegisterRequest struct {
	Token   string `json:"token"`
	Version string `json:"version"`
}

type RegisterResponse struct {
	Credential string `json:"credential"` // "upp_...", shown once
	ProbeID    int64  `json:"probe_id"`
	Region     string `json:"region"`
}

// Assignment is one monitor a probe must check. Config is the monitor's
// validated type-specific JSON, unredacted.
type Assignment struct {
	MonitorID      int64           `json:"monitor_id"`
	Name           string          `json:"name"`
	Type           string          `json:"type"` // http keyword tcp ping dns
	Config         json.RawMessage `json:"config"`
	IntervalS      int             `json:"interval_s"`
	RetryIntervalS int             `json:"retry_interval_s"`
	Retries        int             `json:"retries"`
	TimeoutS       int             `json:"timeout_s"`
}

type AssignmentsResponse struct {
	// Version changes whenever the assignment set changes (also the ETag).
	Version             string       `json:"version"`
	Region              string       `json:"region"`
	BlockPrivateTargets bool         `json:"block_private_targets"`
	Monitors            []Assignment `json:"monitors"`
}

// Result is one raw check result. OK is true for an UP check; retries are
// counted by the server.
type Result struct {
	MonitorID    int64      `json:"monitor_id"`
	Time         time.Time  `json:"time"`
	OK           bool       `json:"ok"`
	LatencyMs    int32      `json:"latency_ms"`
	Message      string     `json:"message"`
	TLSExpiresAt *time.Time `json:"tls_expires_at,omitempty"`
}

type ResultsRequest struct {
	// SentAt is the probe's clock when the batch was sent; the server shifts
	// result times by (server now - SentAt) and clamps future times to now.
	SentAt  time.Time `json:"sent_at"`
	Version string    `json:"version"` // agent version
	Results []Result  `json:"results"`
}

type ResultsResponse struct {
	Accepted int `json:"accepted"`
}

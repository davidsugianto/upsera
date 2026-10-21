package model

// BaselineKey identifies one latency baseline: a monitor as checked by one
// probe (one region).
type BaselineKey struct {
	MonitorID int64
	ProbeID   int64
}

// LatencyBaseline is the p95 latency of a monitor in one region over the
// baseline period, and how many heartbeats it was computed from.
type LatencyBaseline struct {
	P95Ms   int32
	Samples int
}

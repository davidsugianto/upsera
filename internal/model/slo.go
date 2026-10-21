package model

import (
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// SLO defaults and limits (the table's CHECK constraints match).
const (
	DefaultSLOWindowDays = 30
	MinSLOWindowDays     = 7
	MaxSLOWindowDays     = 90
	DefaultFastBurn      = 14.4
	DefaultSlowBurn      = 6.0
	MaxBurn              = 1000.0
)

// SLO is a monitor's availability objective: Target percent of valid checks
// good over the last WindowDays calendar days. FastBurn / SlowBurn are the
// burn-rate thresholds of the multi-window alerts; *FiringSince is set while
// that alert is firing (persisted so a restart never re-sends).
type SLO struct {
	ID              int64
	TeamID          int64
	MonitorID       int64
	Target          float64
	WindowDays      int
	FastBurn        float64
	SlowBurn        float64
	FastFiringSince *time.Time
	SlowFiringSince *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	// MonitorName and MonitorPaused come from the monitor (list queries).
	MonitorName   string
	MonitorPaused bool
}

// SLOFiring is which burn-rate alert of an SLO is firing.
type SLOFiring string

const (
	SLOFiringNone SLOFiring = "none"
	SLOFiringSlow SLOFiring = "slow"
	SLOFiringFast SLOFiring = "fast"
)

// SLOFirings lists every firing state.
var SLOFirings = []SLOFiring{SLOFiringNone, SLOFiringSlow, SLOFiringFast}

// Schema implements huma.SchemaProvider.
func (SLOFiring) Schema(huma.Registry) *huma.Schema { return stringEnumSchema(SLOFirings) }

// Firing is the SLO's firing state; fast wins when both fire.
func (s SLO) Firing() SLOFiring {
	switch {
	case s.FastFiringSince != nil:
		return SLOFiringFast
	case s.SlowFiringSince != nil:
		return SLOFiringSlow
	}
	return SLOFiringNone
}

// SLICount is a tally of checks for an SLI: Valid excludes maintenance, Good
// counts up, pending and degraded.
type SLICount struct{ Good, Valid int }

// Add returns the sum of c and o.
func (c SLICount) Add(o SLICount) SLICount { return SLICount{c.Good + o.Good, c.Valid + o.Valid} }

// DayCount is one calendar day of one monitor: its SLI tally and the p95
// latency of its up and degraded checks (0 without any).
type DayCount struct {
	Day   time.Time // the date, at 00:00 UTC
	Count SLICount
	P95Ms int
}

// RawDaysStart is where daily totals switch from the uptime_daily rollup to
// raw heartbeats: the start of yesterday in loc. Days before it come from the
// rollup, it and later from heartbeats, so the hourly rollup's lag never
// matters and no day is counted twice. Every caller uses this one split.
func RawDaysStart(now time.Time, loc *time.Location) time.Time {
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d-1, 0, 0, 0, 0, loc)
}

// DateIn is t's calendar date in loc, as 00:00 UTC of that date (the form
// of DayCount.Day and of Postgres date columns).
func DateIn(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

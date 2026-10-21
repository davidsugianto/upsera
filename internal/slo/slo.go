// Package slo holds the error-budget maths of service level objectives and
// the burn-rate evaluator that notifies when a budget burns too fast.
//
// SLI = good / valid checks (good: up, pending, degraded; valid: good + down;
// maintenance is not counted). The error budget of a target T (percent) is
// 1 - T/100; budget consumed = (1 - SLI) / budget and the burn rate of a
// window = its error rate / budget. Without valid checks there is no value
// (ok false): nothing here ever divides by zero or returns NaN/Inf.
package slo

import (
	"math"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// Budget is the error budget of target (percent), and false for a target
// outside (0, 100).
func Budget(target float64) (float64, bool) {
	if !(target > 0 && target < 100) {
		return 0, false
	}
	return 1 - target/100, true
}

// SLI is c's good ratio, false without valid checks.
func SLI(c model.SLICount) (float64, bool) {
	if c.Valid <= 0 || c.Good < 0 {
		return 0, false
	}
	return math.Min(float64(c.Good)/float64(c.Valid), 1), true
}

// errorRatio is the fraction of budget used by c's error rate.
func errorRatio(c model.SLICount, target float64) (float64, bool) {
	sli, ok := SLI(c)
	if !ok {
		return 0, false
	}
	budget, ok := Budget(target)
	if !ok {
		return 0, false
	}
	v := (1 - sli) / budget
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// Consumed is the share of the error budget c used up (1 = exhausted; it
// can exceed 1).
func Consumed(c model.SLICount, target float64) (float64, bool) { return errorRatio(c, target) }

// BurnRate is how many times faster than sustainable c burns the budget: c's
// error rate over the budget. For one window it is the same number as
// Consumed; the name says what it means.
func BurnRate(c model.SLICount, target float64) (float64, bool) { return errorRatio(c, target) }

// Sum adds up days.
func Sum(days []model.DayCount) model.SLICount {
	var c model.SLICount
	for _, d := range days {
		c = c.Add(d.Count)
	}
	return c
}

// Point is one calendar day of an SLO's series: that day's SLI (nil without
// valid checks) and the budget consumed by the window so far (cumulative up
// to and including the day; 0 while there were no valid checks yet).
type Point struct {
	Day      time.Time
	SLI      *float64
	Consumed float64
}

// Series returns one Point per calendar day from (a date at 00:00 UTC, as
// DayCount.Day) for n days, from days (any order; days outside the range are
// ignored).
func Series(days []model.DayCount, from time.Time, n int, target float64) []Point {
	byDay := make(map[string]model.SLICount, len(days))
	for _, d := range days {
		k := d.Day.Format(time.DateOnly)
		byDay[k] = byDay[k].Add(d.Count)
	}
	out := make([]Point, n)
	var cum model.SLICount
	for i := range out {
		day := from.AddDate(0, 0, i)
		c := byDay[day.Format(time.DateOnly)]
		cum = cum.Add(c)
		out[i].Day = day
		if v, ok := SLI(c); ok {
			out[i].SLI = new(v)
		}
		if v, ok := Consumed(cum, target); ok {
			out[i].Consumed = v
		}
	}
	return out
}

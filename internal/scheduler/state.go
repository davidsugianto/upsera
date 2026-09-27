package scheduler

import (
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// Flap damping: a monitor is flapping while it made at least flapThreshold
// DOWN/UP changes within the last flapWindow.
const (
	flapWindow    = time.Hour
	flapThreshold = 5
)

// nextState applies one observation (StatusUp, StatusDown or
// StatusMaintenance) at time at to prev. A failed check makes the monitor
// PENDING until it has failed more than retries times in a row, then
// DOWN; the DOWN state's Since is the first failed check, i.e. the start
// of the outage.
func nextState(prev model.MonitorState, havePrev bool, obs model.Status, at time.Time, retries int) model.MonitorState {
	ns := model.MonitorState{
		MonitorID:    prev.MonitorID,
		LastCheckAt:  at,
		TLSExpiresAt: prev.TLSExpiresAt,
		FlapCount:    prev.FlapCount,
	}
	same := func(s model.Status) bool { return havePrev && prev.Status == s }
	switch obs {
	case model.StatusMaintenance, model.StatusUp:
		ns.Status = obs
		ns.Since = at
		if same(obs) {
			ns.Since = prev.Since
		}
	default:
		failures := 1
		if same(model.StatusPending) || same(model.StatusDown) {
			failures = prev.ConsecutiveFailures + 1
		}
		ns.ConsecutiveFailures = failures
		ns.Status = model.StatusPending
		if failures > retries {
			ns.Status = model.StatusDown
		}
		ns.Since = at
		if same(ns.Status) || (same(model.StatusPending) && ns.Status == model.StatusDown) {
			ns.Since = prev.Since
		}
	}
	return ns
}

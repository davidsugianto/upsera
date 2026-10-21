package alerting

import (
	"fmt"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// sloEvent is a queued slo_burn / slo_burn_recovered notification.
type sloEvent struct {
	kind                   model.AlertEvent
	monitorID              int64
	level                  string
	target, burn, consumed float64
	at                     time.Time
}

// SLOBurn reports that the fast or slow (level) burn-rate alert of monitor
// monitorID's SLO (target percent) started firing at the given burn rate,
// with consumed of the window's budget used (-1 = unknown). It queues the
// work for the event loop and never blocks (the SLO evaluator calls it).
func (e *Engine) SLOBurn(monitorID int64, level string, target, burn, consumed float64, at time.Time) {
	e.queueSLO(sloEvent{model.EventSLOBurn, monitorID, level, target, burn, consumed, at})
}

// SLOBurnRecovered reports that a burn-rate alert stopped firing; like
// SLOBurn it only queues.
func (e *Engine) SLOBurnRecovered(monitorID int64, level string, target, burn, consumed float64, at time.Time) {
	e.queueSLO(sloEvent{model.EventSLOBurnRecovered, monitorID, level, target, burn, consumed, at})
}

func (e *Engine) queueSLO(se sloEvent) {
	e.busMu.Lock()
	e.sloPending = append(e.sloPending, se)
	e.busMu.Unlock()
	select {
	case e.signal <- struct{}{}:
	default:
	}
}

// handleSLOLocked sends one message to step 0 of the monitor's channels,
// like a certificate warning. It never opens, acknowledges or escalates an
// alert, and a DOWN parent does not suppress it (a burning budget is real).
// Unknown and paused monitors are skipped; a replay of the same event is
// dropped. Callers hold e.mu.
func (e *Engine) handleSLOLocked(se sloEvent) {
	m, ok := e.monitors[se.monitorID]
	if !ok || m.Paused {
		return
	}
	edge := "fire"
	if se.kind == model.EventSLOBurnRecovered {
		edge = "clear"
	}
	key := fmt.Sprintf("slo:%d:%s:%s:%d", m.ID, se.level, edge, se.at.Unix())
	slot := fmt.Sprintf("%d:%s", m.ID, se.level)
	if e.sloSent[slot] == key {
		return
	}
	e.sloSent[slot] = key
	ev := e.eventLocked(se.kind, m)
	ev.At, ev.SLOLevel, ev.SLOTarget, ev.BurnRate, ev.BudgetConsumed = se.at, se.level, se.target, se.burn, se.consumed
	e.sendStepLocked(m, 0, ev, key)
}

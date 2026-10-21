package alerting

import (
	"fmt"

	"github.com/davidsugianto/upsera/internal/model"
)

// latencyLocked sends the notification-only DEGRADED messages of t: UP →
// DEGRADED sends "degraded", and the next return to UP "latency_recovered",
// once each, to step 0 of m's channels (like a certificate warning). The
// return counts when the monitor was announced as degraded, even through
// PENDING; going DOWN supersedes the announcement (the DOWN alert and its
// recovery say it all). Neither message opens, acknowledges or escalates an
// alert; DOWN → DEGRADED is a recovery and is handled as one by
// handleLocked. Nothing is sent while m is paused or while an ancestor is
// DOWN (maintenance transitions never get here). Callers hold e.mu.
func (e *Engine) latencyLocked(m model.Monitor, t model.Transition) {
	var kind model.AlertEvent
	switch {
	case t.From == model.StatusUp && t.To == model.StatusDegraded:
		kind = model.EventDegraded
	case t.To == model.StatusUp && (t.From == model.StatusDegraded || e.latencyAnnounced[m.ID]):
		kind = model.EventLatencyRecovered
	default:
		if t.To == model.StatusDown {
			delete(e.latencyAnnounced, m.ID)
		}
		return
	}
	if m.Paused || e.ancestorDownLocked(m) {
		if kind == model.EventLatencyRecovered {
			delete(e.latencyAnnounced, m.ID)
		}
		return
	}
	if kind == model.EventDegraded {
		e.latencyAnnounced[m.ID] = true
	} else {
		delete(e.latencyAnnounced, m.ID)
	}
	key := fmt.Sprintf("%s:%d:%d", kind, m.ID, t.At.Unix())
	if e.latencySent[m.ID] == key {
		return // the same transition again
	}
	e.latencySent[m.ID] = key
	ev := e.eventLocked(kind, m)
	ev.At, ev.Message = t.At, t.Message
	ev.Region, ev.LatencyP95Ms, ev.BaselineMs = t.Region, t.LatencyP95Ms, t.BaselineMs
	e.sendStepLocked(m, 0, ev, key)
}

package alerting

import (
	"fmt"
	"slices"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/notify"
)

// probeEvent is a queued probe_silent / probe_recovered notification.
type probeEvent struct {
	kind   model.AlertEvent
	region string
	at     time.Time
}

// ProbeSilent reports that the probe of region has been out of contact since
// the given time. It queues the work for the event loop and never blocks.
func (e *Engine) ProbeSilent(region string, since time.Time) {
	e.queueProbe(probeEvent{model.EventProbeSilent, region, since})
}

// ProbeRecovered reports that the probe of region is in contact again at the
// given time. It queues the work for the event loop and never blocks.
func (e *Engine) ProbeRecovered(region string, at time.Time) {
	e.queueProbe(probeEvent{model.EventProbeRecovered, region, at})
}

func (e *Engine) queueProbe(pe probeEvent) {
	e.busMu.Lock()
	e.probePending = append(e.probePending, pe)
	e.busMu.Unlock()
	select {
	case e.signal <- struct{}{}:
	default:
	}
}

// handleProbeLocked tells every team with an unpaused monitor checked from
// pe.region, once, on that team's default channels. A team without default
// channels hears nothing. Callers hold e.mu.
func (e *Engine) handleProbeLocked(pe probeEvent) {
	var teams []int64
	for _, m := range e.monitors {
		if !m.Paused && m.InRegion(pe.region) && !slices.Contains(teams, m.TeamID) {
			teams = append(teams, m.TeamID)
		}
	}
	slices.Sort(teams)
	suffix := "silent"
	if pe.kind == model.EventProbeRecovered {
		suffix = "recovered"
	}
	key := fmt.Sprintf("probe:%s:%d:%s", pe.region, pe.at.Unix(), suffix)
	for _, team := range teams {
		ev := notify.Event{Kind: pe.kind, TeamID: team, Region: pe.region, At: pe.at}
		for _, c := range e.channelListLocked() {
			if c.TeamID != team || !c.IsDefault {
				continue
			}
			ev.ChannelName = c.Name
			e.disp.Enqueue(notify.Job{Channel: c, Event: ev, DedupeKey: key})
		}
	}
}

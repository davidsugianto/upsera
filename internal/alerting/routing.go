package alerting

import (
	"slices"

	"github.com/davidsugianto/upsera/internal/model"
)

// stepsLocked returns m's escalation steps: its policy's steps when it has
// a policy with at least one step, otherwise one step notifying the
// monitor's own channels, or the team's default channels when it has none.
func (e *Engine) stepsLocked(m model.Monitor) []model.EscalationStep {
	if m.EscalationPolicyID != nil {
		if p, ok := e.policies[*m.EscalationPolicyID]; ok && len(p.Steps) > 0 {
			return p.Steps
		}
	}
	ids := m.ChannelIDs
	if len(ids) == 0 {
		ids = nil
		for _, c := range e.channels {
			if c.TeamID == m.TeamID && c.IsDefault {
				ids = append(ids, c.ID)
			}
		}
		slices.Sort(ids)
	}
	return []model.EscalationStep{{ChannelIDs: ids, DelayS: 0}}
}

// resolveChannelsLocked returns the cached channels for ids, skipping ids
// no longer cached.
func (e *Engine) resolveChannelsLocked(ids []int64) []model.Channel {
	out := make([]model.Channel, 0, len(ids))
	for _, id := range ids {
		if c, ok := e.channels[id]; ok {
			out = append(out, c)
		}
	}
	return out
}

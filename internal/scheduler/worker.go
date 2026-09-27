package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

const maxStartupJitter = 30 * time.Second

// runCheckLoop runs one active (non-push) monitor until ctx is done. When
// jittered, the first check is delayed by a random amount in
// [0, min(interval, 30s)); otherwise the first check runs immediately.
func (s *Scheduler) runCheckLoop(ctx context.Context, m model.Monitor, jittered bool, done chan struct{}) {
	defer close(done)
	defer s.wg.Done()

	interval := time.Duration(m.IntervalS) * s.unit
	if jittered {
		jitterCap := interval
		if jitterCap > maxStartupJitter {
			jitterCap = maxStartupJitter
		}
		if wait := s.jitter(jitterCap); wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}

	for {
		if name, ok := s.maintenanceActive(m.ID, time.Now()); ok {
			s.recordHeartbeat(m.ID, time.Now(), model.StatusMaintenance, 0,
				model.TruncateMessage("maintenance: "+name), nil)
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
			continue
		}
		select {
		case s.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		start := time.Now()
		res := s.ck.Check(ctx, m)
		<-s.sem
		if ctx.Err() != nil {
			// Stopped mid-check (edit, removal or shutdown): the result is
			// an artifact of the cancellation, not of the target.
			return
		}

		ns := s.recordHeartbeat(m.ID, start, res.Status, int32(res.Latency.Milliseconds()),
			model.TruncateMessage(res.Message), res.TLSExpiresAt)

		next := interval
		if ns.Status == model.StatusPending {
			next = time.Duration(m.RetryIntervalS) * s.unit
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(next):
		}
	}
}

// maintenanceActive reports the active maintenance window of monitor id,
// if any.
func (s *Scheduler) maintenanceActive(id int64, t time.Time) (string, bool) {
	if s.opts.Maintenance == nil {
		return "", false
	}
	return s.opts.Maintenance.Active(id, t)
}

// emit hands tr to OnTransition. Callers hold s.mu (see Options).
func (s *Scheduler) emit(tr *model.Transition) {
	if tr != nil && s.opts.OnTransition != nil {
		s.opts.OnTransition(*tr)
	}
}

// runPushLoop watches a push monitor's deadline: resetCh (fired by Push)
// pushes the deadline out by one interval; if the deadline fires first, it
// records a Down heartbeat and re-arms for another interval. The monitor's
// start time counts as the last push.
func (s *Scheduler) runPushLoop(ctx context.Context, m model.Monitor, resetCh <-chan struct{}, done chan struct{}) {
	defer close(done)
	defer s.wg.Done()

	interval := time.Duration(m.IntervalS) * s.unit
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-resetCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(interval)
		case <-timer.C:
			if name, ok := s.maintenanceActive(m.ID, time.Now()); ok {
				s.recordHeartbeat(m.ID, time.Now(), model.StatusMaintenance, 0,
					model.TruncateMessage("maintenance: "+name), nil)
			} else {
				msg := fmt.Sprintf("no push received within %ds", m.IntervalS)
				s.recordHeartbeat(m.ID, time.Now(), model.StatusDown, 0, model.TruncateMessage(msg), nil)
			}
			timer.Reset(interval)
		}
	}
}

// recordHeartbeat applies one observation (StatusUp, StatusDown or
// StatusMaintenance) of monitor id at time at: it runs the state machine
// against the cached state, buffers a heartbeat carrying the resulting
// state for the flusher, updates (and dirties) the in-memory state and
// emits a Transition when the state or the flap state changed. It returns
// the new state. If id is no longer in s.monitors (Remove dropped it,
// racing this call from an in-flight check or Push), it records nothing
// at all: writing the buffer or states/dirty here would resurrect an id
// the scheduler has already forgotten. Likewise a non-maintenance
// observation of a paused monitor (a Push that raced the pause) is
// dropped: nothing would ever move the monitor out of that state.
func (s *Scheduler) recordHeartbeat(id int64, at time.Time, obs model.Status, latencyMs int32, msg string, tlsAt *time.Time) model.MonitorState {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.monitors[id]
	if !ok {
		return model.MonitorState{}
	}
	if m.Paused && obs != model.StatusMaintenance {
		return s.states[id]
	}
	prev, havePrev := s.states[id]
	ns := nextState(prev, havePrev, obs, at, m.Retries)
	ns.MonitorID = id
	if tlsAt != nil {
		ns.TLSExpiresAt = tlsAt
	}
	s.buf.Add(model.Heartbeat{
		MonitorID: id,
		ProbeID:   s.opts.ProbeID,
		Time:      at,
		Status:    ns.Status,
		LatencyMs: latencyMs,
		Message:   msg,
	})
	// Emitted under s.mu so transitions reach OnTransition in the order
	// they were applied, even for concurrent observations of one monitor.
	tr := s.storeStateLocked(prev, havePrev, ns, msg)
	if tr != nil {
		tr.TeamID = m.TeamID
	}
	s.emit(tr)
	return ns
}

// enterPausedMaintenance moves a just-paused monitor into MAINTENANCE
// (without recording a heartbeat) and emits the transition.
func (s *Scheduler) enterPausedMaintenance(m model.Monitor) {
	s.mu.Lock()
	if _, ok := s.monitors[m.ID]; !ok {
		s.mu.Unlock()
		return
	}
	prev, havePrev := s.states[m.ID]
	if havePrev && prev.Status == model.StatusMaintenance {
		s.mu.Unlock()
		return
	}
	ns := nextState(prev, havePrev, model.StatusMaintenance, time.Now(), m.Retries)
	ns.MonitorID = m.ID
	tr := s.storeStateLocked(prev, havePrev, ns, "paused")
	if tr != nil {
		tr.TeamID = m.TeamID
	}
	s.emit(tr)
	s.mu.Unlock()
}

// storeStateLocked does the flap bookkeeping for the change prev → ns,
// stores ns as dirty and returns the Transition to emit, or nil when
// neither the state nor the flap state changed. Callers hold s.mu.
func (s *Scheduler) storeStateLocked(prev model.MonitorState, havePrev bool, ns model.MonitorState, msg string) *model.Transition {
	id, at := ns.MonitorID, ns.LastCheckAt
	from := model.StatusUp
	if havePrev {
		from = prev.Status
	}

	flaps := s.flaps[id]
	if (ns.Status == model.StatusDown && from != model.StatusDown) || (from == model.StatusDown && ns.Status == model.StatusUp) {
		flaps = append(flaps, at)
	}
	cutoff := at.Add(-flapWindow)
	i := 0
	for i < len(flaps) && flaps[i].Before(cutoff) {
		i++
	}
	flaps = flaps[i:]
	if len(flaps) == 0 {
		delete(s.flaps, id)
	} else {
		s.flaps[id] = flaps
	}
	ns.FlapCount = len(flaps)
	nowFlapping := ns.FlapCount >= flapThreshold
	flapChanged := nowFlapping != s.flapping[id]
	s.flapping[id] = nowFlapping

	s.states[id] = ns
	s.dirty[id] = struct{}{}

	if ns.Status == from && !flapChanged {
		return nil
	}
	return &model.Transition{
		MonitorID:   id,
		From:        from,
		To:          ns.Status,
		At:          at,
		Since:       ns.Since,
		PrevSince:   prev.Since,
		Message:     msg,
		Flapping:    nowFlapping,
		FlapChanged: flapChanged,
	}
}

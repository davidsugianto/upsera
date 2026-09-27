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
		if ns.Status != model.StatusUp && ns.ConsecutiveFailures > 0 && ns.ConsecutiveFailures <= m.Retries {
			next = time.Duration(m.RetryIntervalS) * s.unit
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(next):
		}
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
			msg := fmt.Sprintf("no push received within %ds", m.IntervalS)
			s.recordHeartbeat(m.ID, time.Now(), model.StatusDown, 0, model.TruncateMessage(msg), nil)
			timer.Reset(interval)
		}
	}
}

// recordHeartbeat buffers hb for the flusher and updates (and dirties) the
// monitor's in-memory state, deriving Since and ConsecutiveFailures from
// the previous cached state so restarts and Push reports stay consistent
// with the ongoing check loop. It returns the new state. If id is no
// longer in s.monitors (Remove dropped it, racing this call from an
// in-flight check or Push), it records nothing at all: writing the buffer
// or states/dirty here would resurrect an id the scheduler has already
// forgotten.
func (s *Scheduler) recordHeartbeat(id int64, start time.Time, status model.Status, latencyMs int32, msg string, tlsAt *time.Time) model.MonitorState {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.monitors[id]; !ok {
		return model.MonitorState{}
	}

	s.buf.Add(model.Heartbeat{
		MonitorID: id,
		ProbeID:   s.opts.ProbeID,
		Time:      start,
		Status:    status,
		LatencyMs: latencyMs,
		Message:   msg,
	})

	prev, ok := s.states[id]
	since := start
	if ok && prev.Status == status {
		since = prev.Since
	}
	failures := 0
	if status != model.StatusUp {
		if ok {
			failures = prev.ConsecutiveFailures + 1
		} else {
			failures = 1
		}
	}
	if tlsAt == nil && ok {
		tlsAt = prev.TLSExpiresAt
	}

	ns := model.MonitorState{
		MonitorID:           id,
		Status:              status,
		Since:               since,
		LastCheckAt:         start,
		ConsecutiveFailures: failures,
		TLSExpiresAt:        tlsAt,
	}
	s.states[id] = ns
	s.dirty[id] = struct{}{}
	return ns
}

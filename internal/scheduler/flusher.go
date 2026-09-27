package scheduler

import (
	"context"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

const flushBatchSize = 5000

// flushTimeout bounds one periodic flush so an unreachable database cannot
// stall the flusher; buffered data simply waits for the next tick.
const flushTimeout = 15 * time.Second

// flushLoop drains the heartbeat buffer and dirty monitor states into the
// store every FlushInterval, until ctx is done.
func (s *Scheduler) flushLoop(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(s.opts.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fctx, cancel := context.WithTimeout(ctx, flushTimeout)
			s.flushOnce(fctx)
			cancel()
		}
	}
}

// flushOnce inserts up to flushBatchSize buffered heartbeats per call
// until the buffer is empty, then upserts dirty monitor states. A failed
// batch is put back on the buffer (still subject to its capacity) and the
// state upsert is retried on the next cycle; nothing buffered is lost by a
// database outage. When there is nothing to flush, it pings the store at
// most once per pingInterval so Health().DBReachable stays accurate.
func (s *Scheduler) flushOnce(ctx context.Context) error {
	s.buf.resetWarnFlag()

	didWork := false
	for {
		batch := s.buf.Take(flushBatchSize)
		if len(batch) == 0 {
			break
		}
		didWork = true
		if err := s.st.InsertHeartbeats(ctx, batch); err != nil {
			s.buf.Requeue(batch)
			s.markUnreachable(err)
			return err
		}
	}

	states := s.collectDirtyStates()
	if len(states) > 0 {
		didWork = true
		if err := s.st.UpsertMonitorStates(ctx, states); err != nil {
			s.requeueDirty(states)
			s.markUnreachable(err)
			return err
		}
	}

	if didWork {
		s.markReachable()
		s.dbMu.Lock()
		s.lastFlushAt = time.Now()
		s.dbMu.Unlock()
		return nil
	}

	return s.maybePing(ctx)
}

// maybePing checks the store's reachability at most once per pingInterval
// when there is nothing buffered to flush.
func (s *Scheduler) maybePing(ctx context.Context) error {
	s.dbMu.Lock()
	due := s.lastPingAt.IsZero() || time.Since(s.lastPingAt) >= s.pingInterval
	if due {
		s.lastPingAt = time.Now()
	}
	s.dbMu.Unlock()
	if !due {
		return nil
	}
	if err := s.st.Ping(ctx); err != nil {
		s.markUnreachable(err)
		return err
	}
	s.markReachable()
	return nil
}

func (s *Scheduler) markUnreachable(err error) {
	s.dbMu.Lock()
	wasReachable := s.dbReachable
	s.dbReachable = false
	s.dbMu.Unlock()
	if wasReachable {
		s.log.Warn("scheduler: database unreachable, buffering", "error", err)
	}
}

func (s *Scheduler) markReachable() {
	s.dbMu.Lock()
	wasReachable := s.dbReachable
	s.dbReachable = true
	s.dbMu.Unlock()
	if !wasReachable {
		s.log.Info("scheduler: database reachable again")
	}
}

// collectDirtyStates drains and returns the current value of every dirty
// monitor state.
func (s *Scheduler) collectDirtyStates() []model.MonitorState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dirty) == 0 {
		return nil
	}
	out := make([]model.MonitorState, 0, len(s.dirty))
	for id := range s.dirty {
		if st, ok := s.states[id]; ok {
			out = append(out, st)
		}
	}
	s.dirty = make(map[int64]struct{})
	return out
}

// requeueDirty marks every state's id dirty again after a failed upsert.
// The next flush reads the (possibly newer) current value from s.states,
// so this never resurrects stale data.
func (s *Scheduler) requeueDirty(states []model.MonitorState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range states {
		s.dirty[st.MonitorID] = struct{}{}
	}
}

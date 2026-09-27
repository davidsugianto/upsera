// Package scheduler runs one goroutine per active monitor, buffers their
// heartbeats in memory and flushes them to the store on a timer. A cached
// copy of every monitor and its live state lives in memory so checks keep
// running (and Push keeps accepting reports) through a database outage;
// see buffer.go and flusher.go.
package scheduler

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/davidsugianto/upsera/internal/checker"
	"github.com/davidsugianto/upsera/internal/model"
)

// Checker runs one check of a monitor. *checker.Checker satisfies this.
type Checker interface {
	Check(ctx context.Context, m model.Monitor) checker.Result
}

// Store is the persistence the scheduler needs. *store.Store satisfies
// this.
type Store interface {
	InsertHeartbeats(context.Context, []model.Heartbeat) error
	UpsertMonitorStates(context.Context, []model.MonitorState) error
	Ping(context.Context) error
}

// Maintenance reports whether a monitor is inside a maintenance window.
// *maintenance.Registry satisfies this.
type Maintenance interface {
	Active(monitorID int64, t time.Time) (name string, ok bool)
}

// Options configures a Scheduler.
type Options struct {
	ProbeID       int64
	MaxConcurrent int
	BufferSize    int
	FlushInterval time.Duration
	Logger        *slog.Logger
	// Maintenance puts monitors in MAINTENANCE instead of checking them
	// (nil = never).
	Maintenance Maintenance
	// OnTransition receives every state change (and flap start/end), in
	// order. It is called with the scheduler's lock held, so it must not
	// block or call back into the scheduler (nil = drop).
	OnTransition func(model.Transition)
}

// monitorSlot tracks the currently-running worker goroutine for one
// monitor, if any. Its mutex serializes start/stop of that one monitor so
// Upsert/Remove never leave two loops running for the same id.
type monitorSlot struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	resetCh chan struct{} // push monitors only
	// removed is set true by Remove, under mu, once it has dropped this
	// slot's id from every scheduler map and stopped its loop. A racing
	// Upsert that is still holding a reference to this same slot object
	// must see removed and bail instead of starting a new loop the
	// scheduler no longer tracks; a later Upsert(id) finds no slot in the
	// map and allocates a fresh one.
	removed bool
}

// Scheduler owns the in-memory monitor cache, the per-monitor check
// goroutines and the heartbeat buffer. All exported methods are safe for
// concurrent use.
type Scheduler struct {
	st   Store
	ck   Checker
	opts Options
	log  *slog.Logger

	buf *heartbeatBuffer
	sem chan struct{}

	mu       sync.Mutex
	monitors map[int64]model.Monitor
	pushIdx  map[string]int64
	states   map[int64]model.MonitorState
	dirty    map[int64]struct{}
	slots    map[int64]*monitorSlot
	flaps    map[int64][]time.Time // DOWN/UP change times within flapWindow
	flapping map[int64]bool
	stopped  bool

	runCtx context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	dbMu        sync.Mutex
	dbReachable bool
	lastFlushAt time.Time
	lastPingAt  time.Time

	shutdownOnce sync.Once
	shutdownErr  error

	// Test hooks. unit scales monitor IntervalS/RetryIntervalS (which are
	// whole seconds by model contract) into a real wait duration, so tests
	// can run monitors on a millisecond clock without lowering
	// model.MinIntervalS. jitter and pingInterval are likewise overridable
	// for deterministic/fast tests; production uses the defaults set in
	// New.
	unit         time.Duration
	jitter       func(max time.Duration) time.Duration
	pingInterval time.Duration
}

// New builds a Scheduler. Call Start to begin running checks.
func New(st Store, ck Checker, opts Options) *Scheduler {
	if opts.MaxConcurrent < 1 {
		opts.MaxConcurrent = 1
	}
	if opts.BufferSize < 1 {
		opts.BufferSize = 1
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = time.Second
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{
		st:   st,
		ck:   ck,
		opts: opts,
		log:  log,

		buf: newHeartbeatBuffer(opts.BufferSize, log),
		sem: make(chan struct{}, opts.MaxConcurrent),

		monitors: make(map[int64]model.Monitor),
		pushIdx:  make(map[string]int64),
		states:   make(map[int64]model.MonitorState),
		dirty:    make(map[int64]struct{}),
		slots:    make(map[int64]*monitorSlot),
		flaps:    make(map[int64][]time.Time),
		flapping: make(map[int64]bool),

		dbReachable: true,

		unit:         time.Second,
		jitter:       defaultJitter,
		pingInterval: 10 * time.Second,
	}
}

func defaultJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return rand.N(max)
}

// Start seeds the in-memory cache from monitors and states and begins one
// goroutine per active (non-paused) monitor, plus the flusher. Each
// monitor's first check runs after a random jitter in
// [0, min(interval, 30s)) so a restart does not stampede every target at
// once.
func (s *Scheduler) Start(ctx context.Context, monitors []model.Monitor, states []model.MonitorState) {
	s.runCtx, s.cancel = context.WithCancel(ctx)

	s.mu.Lock()
	for _, st := range states {
		s.states[st.MonitorID] = st
		// Only the count is persisted, not the change times: approximate
		// them with the last check so flapping survives a restart and
		// decays within flapWindow.
		if st.FlapCount > 0 {
			s.flaps[st.MonitorID] = slices.Repeat([]time.Time{st.LastCheckAt}, st.FlapCount)
		}
		s.flapping[st.MonitorID] = st.FlapCount >= flapThreshold
	}
	s.mu.Unlock()

	s.wg.Add(1)
	go s.flushLoop(s.runCtx)

	for _, m := range monitors {
		s.upsert(m, true)
	}
}

// Upsert caches m and (re)starts its check loop, stopping any previous
// loop for the same id first so a type or config change never leaves two
// loops running. A paused monitor's loop is stopped and not restarted. The
// first check after an Upsert runs immediately, with no jitter, so a
// newly-created or just-edited monitor reports right away.
func (s *Scheduler) Upsert(m model.Monitor) { s.upsert(m, false) }

func (s *Scheduler) upsert(m model.Monitor, jittered bool) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	if old, ok := s.monitors[m.ID]; ok && old.PushToken != "" && old.PushToken != m.PushToken {
		delete(s.pushIdx, old.PushToken)
	}
	s.monitors[m.ID] = m
	if m.Type == model.TypePush && m.PushToken != "" {
		s.pushIdx[m.PushToken] = m.ID
	}
	slot, ok := s.slots[m.ID]
	if !ok {
		slot = &monitorSlot{}
		s.slots[m.ID] = slot
	}
	s.mu.Unlock()

	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.removed {
		return
	}
	s.stopSlotLocked(slot)
	if m.Paused {
		s.enterPausedMaintenance(m)
		return
	}

	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	if stopped {
		return
	}

	ctx, cancel := context.WithCancel(s.runCtx)
	done := make(chan struct{})
	slot.cancel = cancel
	slot.done = done
	s.wg.Add(1)
	if m.Type == model.TypePush {
		resetCh := make(chan struct{}, 1)
		slot.resetCh = resetCh
		go s.runPushLoop(ctx, m, resetCh, done)
	} else {
		go s.runCheckLoop(ctx, m, jittered, done)
	}
}

// stopSlotLocked cancels slot's running goroutine, if any, and waits for
// it to exit. Callers must hold slot.mu.
func (s *Scheduler) stopSlotLocked(slot *monitorSlot) {
	if slot.cancel == nil {
		return
	}
	slot.cancel()
	<-slot.done
	slot.cancel = nil
	slot.done = nil
	slot.resetCh = nil
}

// Remove stops id's check loop and drops it from the cache entirely. The
// map reads/deletes happen in one s.mu critical section so a concurrent
// Upsert(id) either lands entirely before this section (and gets deleted
// along with everything else) or entirely after it (and finds no slot in
// the map, so it starts a fresh one instead of racing this one). The
// slot's removed flag then stops a racing Upsert that already grabbed a
// reference to this exact slot object before the delete: see monitorSlot.
func (s *Scheduler) Remove(id int64) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	slot := s.slots[id]
	m, hadMonitor := s.monitors[id]
	delete(s.slots, id)
	delete(s.monitors, id)
	delete(s.states, id)
	delete(s.dirty, id)
	delete(s.flaps, id)
	delete(s.flapping, id)
	if hadMonitor && m.PushToken != "" {
		delete(s.pushIdx, m.PushToken)
	}
	s.mu.Unlock()

	if slot != nil {
		slot.mu.Lock()
		slot.removed = true
		s.stopSlotLocked(slot)
		slot.mu.Unlock()
	}
}

// Push records a report from a push monitor and resets its deadline. It
// returns ErrUnknownPushToken for a token that matches no monitor and
// ErrPaused if the monitor is paused.
func (s *Scheduler) Push(token string, status model.Status, msg string, latencyMs int32) error {
	s.mu.Lock()
	id, ok := s.pushIdx[token]
	if !ok {
		s.mu.Unlock()
		return ErrUnknownPushToken
	}
	m := s.monitors[id]
	if m.Paused {
		s.mu.Unlock()
		return ErrPaused
	}
	slot := s.slots[id]
	s.mu.Unlock()

	if name, ok := s.maintenanceActive(id, time.Now()); ok {
		// The report is ignored during maintenance, but it still proves
		// the job is alive, so the deadline below is reset.
		s.recordHeartbeat(id, time.Now(), model.StatusMaintenance, 0, model.TruncateMessage("maintenance: "+name), nil)
	} else {
		if msg == "" {
			msg = "push received"
		}
		s.recordHeartbeat(id, time.Now(), status, latencyMs, model.TruncateMessage(msg), nil)
	}

	if slot != nil {
		slot.mu.Lock()
		if slot.resetCh != nil {
			select {
			case slot.resetCh <- struct{}{}:
			default:
			}
		}
		slot.mu.Unlock()
	}
	return nil
}

// State returns the live in-memory state of monitor id, if known.
func (s *Scheduler) State(id int64) (model.MonitorState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[id]
	return st, ok
}

// Health reports the scheduler's current view of the database and its
// heartbeat buffer.
func (s *Scheduler) Health() Health {
	s.dbMu.Lock()
	h := Health{DBReachable: s.dbReachable, LastFlushAt: s.lastFlushAt}
	s.dbMu.Unlock()
	h.BufferedHeartbeats = s.buf.Len()
	h.DroppedHeartbeats = s.buf.Dropped()
	return h
}

// Shutdown stops every check loop and the flusher, then performs one final
// flush bounded by ctx. It is safe to call once; a second call returns the
// first call's result immediately. Upsert and Remove are no-ops after
// Shutdown begins.
func (s *Scheduler) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		s.mu.Unlock()

		if s.cancel != nil {
			s.cancel()
		}

		done := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
		}

		s.shutdownErr = s.flushOnce(ctx)
	})
	return s.shutdownErr
}

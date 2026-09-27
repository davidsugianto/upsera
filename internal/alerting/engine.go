// Package alerting turns monitor state transitions into notifications:
// it opens and resolves alerts, escalates unacknowledged alerts through
// escalation policies, suppresses alerts of monitors whose parent is DOWN,
// damps flapping monitors and sends certificate-expiry warnings. All of
// its state lives in memory and is written to the store asynchronously,
// so alerts keep firing through a database outage.
package alerting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/notify"
)

var (
	// ErrAlertNotFound is returned by Acknowledge for an unknown or
	// other-team alert (resolved alerts are forgotten once written).
	ErrAlertNotFound = errors.New("alert not found")
	// ErrAlertResolved is returned by Acknowledge for a resolved alert that
	// is still in memory.
	ErrAlertResolved = errors.New("alert is resolved")
)

const (
	tickInterval      = time.Second
	certFirstScan     = time.Minute
	certScanInterval  = 10 * time.Minute
	maxDependencyHops = 10
)

// Store is the persistence the engine needs. *store.Store satisfies this.
type Store interface {
	UpsertAlerts(context.Context, []model.Alert) error
	InsertNotificationLog(context.Context, []model.NotificationLogEntry) error
}

// StateSource returns a monitor's live state. *scheduler.Scheduler
// satisfies this.
type StateSource interface {
	State(id int64) (model.MonitorState, bool)
}

// Snapshot is what the engine loads at startup.
type Snapshot struct {
	Monitors   []model.Monitor
	Channels   []model.Channel
	Policies   []model.EscalationPolicy
	OpenAlerts []model.Alert
	CertKeys   []string // dedupe keys of cert warnings already sent
}

// Options configures an Engine.
type Options struct {
	Store  Store
	Notify notify.Options
	// Notifiers overrides notify.New(Notify); tests inject fakes.
	Notifiers     map[model.ChannelType]notify.Notifier
	Logger        *slog.Logger
	FlushInterval time.Duration // default 1s
	// OnAlert receives a copy of every alert after each change (called with
	// the engine lock held; must not block; nil = drop).
	OnAlert func(model.Alert)
}

// Engine is the alerting engine. All exported methods are safe for
// concurrent use.
type Engine struct {
	st   Store
	log  *slog.Logger
	opts Options
	disp *notify.Dispatcher
	acks *notify.AckListeners

	busMu   sync.Mutex
	pending []model.Transition
	signal  chan struct{}

	mu       sync.Mutex
	states   StateSource
	monitors map[int64]model.Monitor
	children map[int64][]int64
	channels map[int64]model.Channel
	policies map[int64]model.EscalationPolicy
	alerts   map[int64]*model.Alert  // open alert by monitor id
	byID     map[string]*model.Alert // open alerts, plus resolved ones until flushed
	dirty    map[string]struct{}     // alert ids to write
	certSent map[string]struct{}
	warned   map[string]bool // alert ids already warned about having no channels
	now      func() time.Time

	syncMu sync.Mutex // serializes ack listener syncs; guards runCtx writes

	outMu        sync.Mutex
	logs         []model.NotificationLogEntry
	logDropWarn  bool
	dbReachable  bool
	runCtx       context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	shutdownOnce sync.Once
	shutdownErr  error
}

// New builds an Engine. Call Start to begin processing.
func New(opts Options) *Engine {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = time.Second
	}
	notifiers := opts.Notifiers
	if notifiers == nil {
		notifiers = notify.New(opts.Notify)
	}
	e := &Engine{
		st:          opts.Store,
		log:         opts.Logger,
		opts:        opts,
		signal:      make(chan struct{}, 1),
		monitors:    make(map[int64]model.Monitor),
		children:    make(map[int64][]int64),
		channels:    make(map[int64]model.Channel),
		policies:    make(map[int64]model.EscalationPolicy),
		alerts:      make(map[int64]*model.Alert),
		byID:        make(map[string]*model.Alert),
		dirty:       make(map[string]struct{}),
		certSent:    make(map[string]struct{}),
		warned:      make(map[string]bool),
		now:         time.Now,
		dbReachable: true,
	}
	e.disp = notify.NewDispatcher(notifiers, e.recordAttempt, opts.Logger)
	e.acks = notify.NewAckListeners(opts.Notify, func(id string, src model.AckSource, name string) error {
		_, err := e.Acknowledge(0, id, model.AckBy{Source: src, Name: name})
		return err
	}, opts.Logger)
	return e
}

// Start loads snap, reconciles its open alerts against the live states
// (alerts whose monitor is UP again are resolved and their recovery sent;
// those in MAINTENANCE are resolved silently; the rest resume escalating
// from their persisted step) and starts the event loop, the outbox flusher
// and the acknowledgement listeners.
func (e *Engine) Start(ctx context.Context, snap Snapshot, states StateSource) {
	e.load(snap, states)
	e.syncMu.Lock()
	e.runCtx, e.cancel = context.WithCancel(ctx)
	e.syncMu.Unlock()
	e.wg.Add(2)
	go e.loop(e.runCtx)
	go e.flushLoop(e.runCtx)
	e.syncAcks()
}

// load seeds the caches from snap and reconciles its open alerts with the
// live states: resolved when UP or in MAINTENANCE, and suppression matched
// to the ancestors' states.
func (e *Engine) load(snap Snapshot, states StateSource) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.states = states
	for _, m := range snap.Monitors {
		e.monitors[m.ID] = m
	}
	e.rebuildChildrenLocked()
	for _, c := range snap.Channels {
		e.channels[c.ID] = c
	}
	for _, p := range snap.Policies {
		e.policies[p.ID] = p
	}
	for _, k := range snap.CertKeys {
		e.certSent[k] = struct{}{}
	}
	now := e.now()
	for _, a := range snap.OpenAlerts {
		if _, ok := e.monitors[a.MonitorID]; !ok || a.ResolvedAt != nil {
			continue
		}
		a.NotifiedChannelIDs = slices.Clone(a.NotifiedChannelIDs)
		p := &a
		e.alerts[a.MonitorID] = p
		e.byID[a.ID] = p
	}
	for _, p := range e.alerts {
		st, ok := e.stateLocked(p.MonitorID)
		if !ok {
			continue
		}
		at := now
		if st.Since.After(p.IncidentStart) && !st.Since.After(now) {
			at = st.Since
		}
		switch st.Status {
		case model.StatusUp:
			e.resolveRecoveredLocked(p, at)
		case model.StatusMaintenance:
			e.resolveLocked(p, at, model.ResolutionMaintenance)
		}
	}
	for _, p := range e.alerts {
		e.reconcileLocked(p)
	}
}

// Publish queues a transition for the event loop. It never blocks and is
// safe to call before Start.
func (e *Engine) Publish(t model.Transition) {
	e.busMu.Lock()
	e.pending = append(e.pending, t)
	e.busMu.Unlock()
	select {
	case e.signal <- struct{}{}:
	default:
	}
}

func (e *Engine) loop(ctx context.Context) {
	defer e.wg.Done()
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	cert := time.NewTimer(certFirstScan)
	defer cert.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.signal:
			e.drain()
		case <-ticker.C:
			e.drain()
			e.tick(e.now())
		case <-cert.C:
			e.certScan(e.now())
			cert.Reset(certScanInterval)
		}
	}
}

// drain applies every queued transition in order.
func (e *Engine) drain() {
	e.busMu.Lock()
	ts := e.pending
	e.pending = nil
	e.busMu.Unlock()
	if len(ts) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, t := range ts {
		e.handleLocked(t)
	}
}

// handleLocked applies one transition. Callers hold e.mu.
func (e *Engine) handleLocked(t model.Transition) {
	m, ok := e.monitors[t.MonitorID]
	if !ok {
		return
	}
	a := e.alerts[m.ID]

	if t.To == model.StatusMaintenance {
		if a != nil {
			e.resolveLocked(a, t.At, model.ResolutionMaintenance)
		}
		// A parent in maintenance is no longer DOWN: release its children.
		e.reconcileDescendantsLocked(m.ID)
		return
	}
	if t.FlapChanged && t.Flapping {
		if a != nil {
			a.Flapping = true
			a.NextEscalationAt = nil
			e.markDirtyLocked(a)
		}
		if e.ancestorDownLocked(m) {
			return // a DOWN parent silences its children, flapping included
		}
		flapCount := 0
		if st, ok := e.stateLocked(m.ID); ok {
			flapCount = st.FlapCount
		}
		ev := e.eventLocked(model.EventFlapping, m)
		ev.At, ev.FlapCount, ev.Message = t.At, flapCount, t.Message
		e.sendStepLocked(m, 0, ev, fmt.Sprintf("flapping:%d:%d", m.ID, t.At.Unix()))
		return
	}
	if t.FlapChanged {
		switch t.To {
		case model.StatusDown:
			if a != nil {
				e.stopFlappingLocked(a)
			} else {
				e.openLocked(m, t)
			}
		case model.StatusUp:
			if a != nil {
				e.resolveRecoveredLocked(a, t.At)
			}
			e.reconcileDescendantsLocked(m.ID)
		}
		// Flapping that ends in PENDING leaves the alert marked flapping;
		// the next DOWN resumes it (below) and UP resolves it.
		return
	}
	if t.Flapping {
		return
	}
	switch {
	case t.To == model.StatusDown:
		if a == nil {
			e.openLocked(m, t)
		} else if a.Flapping {
			e.stopFlappingLocked(a)
		}
	case t.To == model.StatusUp && (t.From == model.StatusDown || a != nil):
		// An open alert is also resolved when it recovers from PENDING:
		// after a restart an alert can be open while the state is PENDING.
		if a != nil {
			e.resolveRecoveredLocked(a, t.At)
		}
		e.reconcileDescendantsLocked(m.ID)
	}
}

// stopFlappingLocked resumes an alert whose monitor stopped flapping while
// DOWN: step 0 is notified again unless the alert is acknowledged or
// suppressed.
func (e *Engine) stopFlappingLocked(a *model.Alert) {
	a.Flapping = false
	e.markDirtyLocked(a)
	if a.AckedAt == nil && !a.Suppressed {
		e.fireLocked(a, 0)
	}
}

// openLocked opens an alert for m's DOWN transition t, fires its first
// step unless an ancestor is DOWN, and suppresses descendants' alerts.
func (e *Engine) openLocked(m model.Monitor, t model.Transition) {
	since := t.Since
	if since.IsZero() {
		since = t.At
	}
	a := &model.Alert{
		ID:                 uuid.Must(uuid.NewV7()).String(),
		TeamID:             m.TeamID,
		MonitorID:          m.ID,
		IncidentStart:      since,
		OpenedAt:           t.At,
		Message:            t.Message,
		Step:               -1,
		NotifiedChannelIDs: []int64{},
	}
	e.alerts[m.ID] = a
	e.byID[a.ID] = a
	if e.ancestorDownLocked(m) {
		a.Suppressed = true
		e.markDirtyLocked(a)
	} else {
		e.fireLocked(a, 0)
	}
	e.reconcileDescendantsLocked(m.ID)
}

// reconcileDescendantsLocked re-evaluates suppression of every open alert
// below id (see reconcileLocked).
func (e *Engine) reconcileDescendantsLocked(id int64) {
	e.forEachDescendantLocked(id, e.reconcileLocked)
}

// reconcileLocked makes a's suppression match its monitor's ancestors: an
// alert under a DOWN ancestor is suppressed (its escalation stops); a
// suppressed alert whose ancestors are no longer DOWN is released and
// fires its first step, or its next step if it had already fired.
func (e *Engine) reconcileLocked(a *model.Alert) {
	down := e.ancestorDownLocked(e.monitors[a.MonitorID])
	switch {
	case down && !a.Suppressed:
		a.Suppressed = true
		a.NextEscalationAt = nil
		e.markDirtyLocked(a)
	case !down && a.Suppressed:
		a.Suppressed = false
		e.markDirtyLocked(a)
		if a.AckedAt != nil || a.Flapping {
			return
		}
		if a.Step == -1 {
			e.fireLocked(a, 0)
		} else if next := a.Step + 1; next < len(e.stepsLocked(e.monitors[a.MonitorID])) {
			e.fireLocked(a, next)
		}
	}
}

// resolveLocked closes a without notifying anyone.
func (e *Engine) resolveLocked(a *model.Alert, at time.Time, resolution string) {
	a.ResolvedAt = new(at)
	a.Resolution = resolution
	a.NextEscalationAt = nil
	if e.alerts[a.MonitorID] == a {
		delete(e.alerts, a.MonitorID)
	}
	e.markDirtyLocked(a)
}

// resolveRecoveredLocked closes a as recovered and tells every channel that
// was told about the outage (unless the alert was suppressed).
func (e *Engine) resolveRecoveredLocked(a *model.Alert, at time.Time) {
	e.resolveLocked(a, at, model.ResolutionRecovered)
	if a.Suppressed || len(a.NotifiedChannelIDs) == 0 {
		return
	}
	m := e.monitors[a.MonitorID]
	ev := e.eventLocked(model.EventRecovered, m)
	ev.AlertID, ev.At, ev.Downtime = a.ID, at, at.Sub(a.IncidentStart)
	key := "recovered:" + a.ID
	for _, c := range e.resolveChannelsLocked(a.NotifiedChannelIDs) {
		ev.ChannelName = c.Name
		e.disp.Enqueue(notify.Job{Channel: c, Event: ev, DedupeKey: key})
	}
}

// fireLocked notifies escalation step idx of a's monitor and schedules the
// next step.
func (e *Engine) fireLocked(a *model.Alert, idx int) {
	m := e.monitors[a.MonitorID]
	steps := e.stepsLocked(m)
	if idx >= len(steps) {
		a.NextEscalationAt = nil
		e.markDirtyLocked(a)
		return
	}
	chans := e.resolveChannelsLocked(steps[idx].ChannelIDs)
	if len(chans) == 0 && !e.warned[a.ID] {
		e.warned[a.ID] = true
		e.log.Warn("alerting: no channels for monitor", "monitor_id", m.ID)
	}
	ev := e.eventLocked(model.EventDown, m)
	ev.AlertID, ev.Message, ev.At, ev.Ackable = a.ID, a.Message, a.IncidentStart, true
	key := fmt.Sprintf("down:%s:%d", a.ID, idx)
	for _, c := range chans {
		ev.ChannelName = c.Name
		e.disp.Enqueue(notify.Job{Channel: c, Event: ev, DedupeKey: key})
		if !slices.Contains(a.NotifiedChannelIDs, c.ID) {
			a.NotifiedChannelIDs = append(a.NotifiedChannelIDs, c.ID)
		}
	}
	a.Step = idx
	a.NextEscalationAt = nil
	if idx < len(steps)-1 {
		a.NextEscalationAt = new(e.now().Add(time.Duration(steps[idx].DelayS) * time.Second))
	}
	e.markDirtyLocked(a)
}

// tick fires the next step of every open, unacknowledged, unsuppressed,
// non-flapping alert whose escalation is due.
func (e *Engine) tick(now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range e.alerts {
		if a.AckedAt != nil || a.Suppressed || a.Flapping || a.NextEscalationAt == nil || now.Before(*a.NextEscalationAt) {
			continue
		}
		e.fireLocked(a, a.Step+1)
	}
}

// Acknowledge stops a's escalation. teamID 0 matches any team (chat
// buttons carry only the alert id). Acknowledging twice returns the first
// acknowledgement unchanged. A resolved alert still in memory returns
// ErrAlertResolved; one no longer in memory, ErrAlertNotFound.
func (e *Engine) Acknowledge(teamID int64, alertID string, by model.AckBy) (model.Alert, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.byID[alertID]
	if !ok || (teamID != 0 && a.TeamID != teamID) {
		return model.Alert{}, ErrAlertNotFound
	}
	if a.ResolvedAt != nil {
		return model.Alert{}, ErrAlertResolved
	}
	if a.AckedAt == nil {
		a.AckedAt = new(e.now())
		a.AckedByUserID = by.UserID
		a.AckSource = by.Source
		a.AckedByName = by.Name
		a.NextEscalationAt = nil
		e.markDirtyLocked(a)
	}
	return copyAlert(a), nil
}

// TestSend sends a test notification to ch right away.
func (e *Engine) TestSend(ctx context.Context, ch model.Channel) error {
	now := e.nowSafe()
	return e.disp.SendNow(ctx, notify.Job{
		Channel:   ch,
		Event:     notify.Event{Kind: model.EventTest, TeamID: ch.TeamID, ChannelName: ch.Name, At: now},
		DedupeKey: fmt.Sprintf("test:%d:%d", ch.ID, now.Unix()),
	})
}

func (e *Engine) nowSafe() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now()
}

// UpsertMonitor caches m. A changed parent re-evaluates the suppression of
// m's (and its descendants') open alerts.
func (e *Engine) UpsertMonitor(m model.Monitor) {
	e.mu.Lock()
	defer e.mu.Unlock()
	old, existed := e.monitors[m.ID]
	e.monitors[m.ID] = m
	e.rebuildChildrenLocked()
	if existed && !equalParent(old.ParentID, m.ParentID) {
		if a := e.alerts[m.ID]; a != nil {
			e.reconcileLocked(a)
		}
		e.reconcileDescendantsLocked(m.ID)
	}
}

func equalParent(a, b *int64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// RemoveMonitor forgets monitor id and its alerts (the database cascade
// deletes their rows). Its children lose their parent, as parent_id is set
// to NULL in the database, and their suppressed alerts are released.
func (e *Engine) RemoveMonitor(id int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.monitors, id)
	for aid, a := range e.byID {
		if a.MonitorID == id {
			delete(e.byID, aid)
			delete(e.dirty, aid)
			delete(e.warned, aid)
		}
	}
	delete(e.alerts, id)
	children := e.children[id]
	for _, c := range children {
		if m, ok := e.monitors[c]; ok {
			m.ParentID = nil
			e.monitors[c] = m
		}
	}
	e.rebuildChildrenLocked()
	for _, c := range children {
		if a := e.alerts[c]; a != nil {
			e.reconcileLocked(a)
		}
		e.reconcileDescendantsLocked(c)
	}
}

// UpsertChannel caches c and re-syncs the acknowledgement listeners.
func (e *Engine) UpsertChannel(c model.Channel) {
	e.mu.Lock()
	e.channels[c.ID] = c
	e.mu.Unlock()
	e.syncAcks()
}

// RemoveChannel forgets channel id, drops it from the cached monitors'
// channel lists (the database cascade removes the links, so a monitor left
// without channels falls back to the team's defaults) and re-syncs the
// acknowledgement listeners.
func (e *Engine) RemoveChannel(id int64) {
	e.mu.Lock()
	delete(e.channels, id)
	for mid, m := range e.monitors {
		if slices.Contains(m.ChannelIDs, id) {
			m.ChannelIDs = slices.DeleteFunc(slices.Clone(m.ChannelIDs), func(c int64) bool { return c == id })
			e.monitors[mid] = m
		}
	}
	e.mu.Unlock()
	e.syncAcks()
}

// syncAcks applies the current channel set to the acknowledgement
// listeners. syncMu spans snapshot and Sync so concurrent channel writes
// can never apply an older snapshot last.
func (e *Engine) syncAcks() {
	e.syncMu.Lock()
	defer e.syncMu.Unlock()
	if e.runCtx == nil || e.runCtx.Err() != nil {
		return // not started (Start syncs) or shutting down
	}
	e.mu.Lock()
	chans := e.channelListLocked()
	e.mu.Unlock()
	e.acks.Sync(e.runCtx, chans)
}

// UpsertPolicy caches p.
func (e *Engine) UpsertPolicy(p model.EscalationPolicy) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.policies[p.ID] = p
}

// RemovePolicy forgets policy id.
func (e *Engine) RemovePolicy(id int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.policies, id)
}

// Shutdown stops the event loop (after applying queued transitions), the
// acknowledgement listeners and the dispatcher (waiting for queued sends
// until ctx is done), then writes everything pending to the store. The
// final write gets its own flushTimeout, so slow channels that used up ctx
// cannot make it lose the alerts' final state.
func (e *Engine) Shutdown(ctx context.Context) error {
	e.shutdownOnce.Do(func() {
		if e.cancel != nil {
			e.cancel()
		}
		e.wg.Wait()
		e.drain()
		e.acks.Close()
		e.disp.Close(ctx)
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flushTimeout)
		defer cancel()
		e.shutdownErr = e.flush(fctx)
	})
	return e.shutdownErr
}

func (e *Engine) stateLocked(id int64) (model.MonitorState, bool) {
	if e.states == nil {
		return model.MonitorState{}, false
	}
	return e.states.State(id)
}

// ancestorDownLocked reports whether any ancestor of m (up to
// maxDependencyHops) is DOWN.
func (e *Engine) ancestorDownLocked(m model.Monitor) bool {
	cur := m
	for range maxDependencyHops {
		if cur.ParentID == nil {
			return false
		}
		p, ok := e.monitors[*cur.ParentID]
		if !ok {
			return false
		}
		if st, ok := e.stateLocked(p.ID); ok && st.Status == model.StatusDown {
			return true
		}
		cur = p
	}
	return false
}

// forEachDescendantLocked calls fn for the open alert of every descendant
// of id, breadth first, at most maxDependencyHops levels deep.
func (e *Engine) forEachDescendantLocked(id int64, fn func(*model.Alert)) {
	seen := map[int64]bool{id: true}
	level := []int64{id}
	for range maxDependencyHops {
		var next []int64
		for _, p := range level {
			for _, c := range e.children[p] {
				if seen[c] {
					continue
				}
				seen[c] = true
				next = append(next, c)
				if a := e.alerts[c]; a != nil {
					fn(a)
				}
			}
		}
		if len(next) == 0 {
			return
		}
		level = next
	}
}

func (e *Engine) rebuildChildrenLocked() {
	e.children = make(map[int64][]int64)
	for _, m := range e.monitors {
		if m.ParentID != nil {
			e.children[*m.ParentID] = append(e.children[*m.ParentID], m.ID)
		}
	}
}

func (e *Engine) channelListLocked() []model.Channel {
	out := make([]model.Channel, 0, len(e.channels))
	for _, c := range e.channels {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b model.Channel) int { return int(a.ID - b.ID) })
	return out
}

func (e *Engine) eventLocked(kind model.AlertEvent, m model.Monitor) notify.Event {
	return notify.Event{Kind: kind, TeamID: m.TeamID, MonitorID: m.ID, MonitorName: m.Name}
}

// sendStepLocked sends ev to the channels of m's escalation step idx.
func (e *Engine) sendStepLocked(m model.Monitor, idx int, ev notify.Event, key string) {
	steps := e.stepsLocked(m)
	if idx >= len(steps) {
		return
	}
	for _, c := range e.resolveChannelsLocked(steps[idx].ChannelIDs) {
		ev.ChannelName = c.Name
		e.disp.Enqueue(notify.Job{Channel: c, Event: ev, DedupeKey: key})
	}
}

func (e *Engine) markDirtyLocked(a *model.Alert) {
	e.dirty[a.ID] = struct{}{}
	if e.opts.OnAlert != nil {
		e.opts.OnAlert(copyAlert(a))
	}
}

func copyAlert(a *model.Alert) model.Alert {
	c := *a
	c.NotifiedChannelIDs = slices.Clone(a.NotifiedChannelIDs)
	return c
}

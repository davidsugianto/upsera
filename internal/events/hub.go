// Package events fans live dashboard events out to per-team subscribers.
// Publishers are the scheduler and alerting engine (under their own locks)
// and the API; subscribers are the dashboard's SSE streams. Publish never
// blocks: a subscriber whose buffer is full is dropped and its channel
// closed, so the client reconnects and refetches instead of stalling a
// publisher.
package events

import "sync"

// subscriberBuffer is how many events a subscriber may fall behind before it
// is dropped.
const subscriberBuffer = 64

// MonitorsChanged tells dashboards to refetch a team's monitor list.
type MonitorsChanged struct {
	MonitorID int64
	Deleted   bool
}

// Hub routes published values to the subscriptions of their team. The zero
// value is not usable; use NewHub.
type Hub struct {
	mu     sync.Mutex
	subs   map[int64]map[*Subscription]struct{}
	closed bool
}

// Subscription receives a team's events on C.
type Subscription struct {
	// C is closed when the hub closes, the subscriber lags, or Close is
	// called.
	C <-chan any

	c      chan any
	hub    *Hub
	teamID int64
	once   sync.Once
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[int64]map[*Subscription]struct{})}
}

// Subscribe registers a subscriber for teamID. After Close it returns a
// subscription whose C is already closed.
func (h *Hub) Subscribe(teamID int64) *Subscription {
	c := make(chan any, subscriberBuffer)
	s := &Subscription{C: c, c: c, hub: h, teamID: teamID}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		s.once.Do(func() { close(c) })
		return s
	}
	set := h.subs[teamID]
	if set == nil {
		set = make(map[*Subscription]struct{})
		h.subs[teamID] = set
	}
	set[s] = struct{}{}
	return s
}

// Publish delivers v to every subscriber of teamID without blocking. v is a
// model.Transition, model.Alert or MonitorsChanged. The sends are
// non-blocking and happen under h.mu so they cannot race with a channel
// being closed; a subscriber whose buffer is full is dropped.
func (h *Hub) Publish(teamID int64, v any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs[teamID] {
		select {
		case s.c <- v:
		default:
			h.removeLocked(s)
		}
	}
}

// removeLocked unregisters s and closes its channel. h.mu must be held.
func (h *Hub) removeLocked(s *Subscription) {
	if set := h.subs[s.teamID]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(h.subs, s.teamID)
		}
	}
	s.once.Do(func() { close(s.c) })
}

// Close unsubscribes s and closes its channel. It is idempotent.
func (s *Subscription) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.hub.removeLocked(s)
}

// Close closes every subscription; later Publish calls are no-ops and later
// Subscribe calls return closed subscriptions.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, set := range h.subs {
		for s := range set {
			s.once.Do(func() { close(s.c) })
		}
	}
	h.subs = make(map[int64]map[*Subscription]struct{})
}

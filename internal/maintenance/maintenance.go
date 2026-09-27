// Package maintenance decides whether a monitor is inside a maintenance
// window. The Registry is an in-memory cache the scheduler consults on
// every check, so the check path never touches the database.
package maintenance

import (
	"slices"
	"sync"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// ActiveAt reports whether window w covers t. Recurring windows repeat at
// the same local wall-clock time in loc, so they stay put across DST
// changes.
func ActiveAt(w model.MaintenanceWindow, loc *time.Location, t time.Time) bool {
	if t.Before(w.StartsAt) {
		return false
	}
	var stepDays int
	switch w.Recurrence {
	case model.RecurDaily:
		stepDays = 1
	case model.RecurWeekly:
		stepDays = 7
	default:
		return t.Before(w.EndsAt)
	}
	dur := w.EndsAt.Sub(w.StartsAt)
	k := int(t.Sub(w.StartsAt) / (time.Duration(stepDays) * 24 * time.Hour))
	start := w.StartsAt.In(loc)
	// A DST shift can move the k-th occurrence up to an hour either way
	// from k*step, so check the neighbours too.
	for i := k - 1; i <= k+1; i++ {
		if i < 0 {
			continue
		}
		s := start.AddDate(0, 0, i*stepDays)
		if !t.Before(s) && t.Before(s.Add(dur)) {
			return true
		}
	}
	return false
}

// Registry caches every maintenance window. It is safe for concurrent use.
type Registry struct {
	mu      sync.RWMutex
	loc     *time.Location
	windows map[int64]model.MaintenanceWindow
}

// NewRegistry returns an empty registry evaluating recurrences in loc.
func NewRegistry(loc *time.Location) *Registry {
	if loc == nil {
		loc = time.UTC
	}
	return &Registry{loc: loc, windows: make(map[int64]model.MaintenanceWindow)}
}

// Set replaces every cached window.
func (r *Registry) Set(ws []model.MaintenanceWindow) {
	m := make(map[int64]model.MaintenanceWindow, len(ws))
	for _, w := range ws {
		m[w.ID] = w
	}
	r.mu.Lock()
	r.windows = m
	r.mu.Unlock()
}

// Upsert caches w, replacing any window with the same id.
func (r *Registry) Upsert(w model.MaintenanceWindow) {
	r.mu.Lock()
	r.windows[w.ID] = w
	r.mu.Unlock()
}

// Remove drops window id.
func (r *Registry) Remove(id int64) {
	r.mu.Lock()
	delete(r.windows, id)
	r.mu.Unlock()
}

// Active returns the name of the active window (the lowest id, if several)
// that contains monitorID at t.
func (r *Registry) Active(monitorID int64, t time.Time) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var best *model.MaintenanceWindow
	for id, w := range r.windows {
		if best != nil && id > best.ID {
			continue
		}
		if !slices.Contains(w.MonitorIDs, monitorID) || !ActiveAt(w, r.loc, t) {
			continue
		}
		best = &w
	}
	if best == nil {
		return "", false
	}
	return best.Name, true
}

// Package probes keeps the server's view of its remote probes in memory:
// credential lookup for every probe request (no database access on that
// path), liveness, and "probe silent" / "probe reporting again" detection.
// Last-contact times are written to the store in the background.
package probes

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"sync"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// Defaults for Options.
const (
	DefaultSilentAfter = 2 * time.Minute
	DefaultCheckEvery  = 5 * time.Second
	DefaultFlushEvery  = 30 * time.Second
	flushTimeout       = 15 * time.Second
)

// Store persists last-contact times. *store.Store satisfies this.
type Store interface {
	TouchProbes(context.Context, []model.ProbeSeen) error
}

// Options configures a Registry.
type Options struct {
	Store       Store
	SilentAfter time.Duration // default 2m
	CheckEvery  time.Duration // default 5s
	FlushEvery  time.Duration // default 30s
	Now         func() time.Time
	Logger      *slog.Logger
	// OnSilent fires once when a registered probe has not been in contact for
	// SilentAfter; since is its last contact, or when it was loaded or added if
	// it has not been seen since (the startup grace). OnRecovered fires once
	// when a probe reported silent is seen again. Both run without registry
	// locks held, so they may call back into the Registry.
	OnSilent    func(p model.ProbeAuth, since time.Time)
	OnRecovered func(p model.ProbeAuth, at time.Time)
}

type entry struct {
	auth     model.ProbeAuth
	lastSeen time.Time // zero = never seen
	baseline time.Time // registry start, or when the credential was added
	version  string
	dirty    bool
	silent   bool // OnSilent fired and no contact since
}

// reference is the instant silence is measured from.
func (e *entry) reference() time.Time {
	if e.lastSeen.After(e.baseline) {
		return e.lastSeen
	}
	return e.baseline
}

// Registry is the in-memory probe registry. All methods are safe for
// concurrent use.
type Registry struct {
	o Options

	mu     sync.RWMutex
	byID   map[int64]*entry
	byHash map[[sha256.Size]byte]*entry

	lifeMu  sync.Mutex
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewRegistry builds a Registry; call Start to begin the background work.
func NewRegistry(o Options) *Registry {
	if o.SilentAfter <= 0 {
		o.SilentAfter = DefaultSilentAfter
	}
	if o.CheckEvery <= 0 {
		o.CheckEvery = DefaultCheckEvery
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = DefaultFlushEvery
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Registry{
		o:      o,
		byID:   make(map[int64]*entry),
		byHash: make(map[[sha256.Size]byte]*entry),
	}
}

func hashOf(b []byte) ([sha256.Size]byte, bool) {
	var h [sha256.Size]byte
	if len(b) != sha256.Size {
		return h, false
	}
	copy(h[:], b)
	return h, true
}

// Set replaces every probe. Probes already known keep their in-memory
// liveness (so a refresh never repeats a silent notification).
func (r *Registry) Set(ps []model.ProbeAuth) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.byID
	r.byID = make(map[int64]*entry, len(ps))
	r.byHash = make(map[[sha256.Size]byte]*entry, len(ps))
	now := r.o.Now()
	for _, p := range ps {
		r.putLocked(p, old[p.ID], now)
	}
}

// Add adds or replaces one probe (after registration or a new token). A
// changed credential starts a fresh startup grace.
func (r *Registry) Add(p model.ProbeAuth) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.putLocked(p, r.byID[p.ID], r.o.Now())
}

// putLocked stores p, carrying liveness over from prev when the credential is
// unchanged. A probe without a credential is not stored: it can neither
// authenticate nor go silent.
func (r *Registry) putLocked(p model.ProbeAuth, prev *entry, now time.Time) {
	if prev != nil {
		delete(r.byHash, hashKey(prev.auth.TokenHash))
		delete(r.byID, prev.auth.ID)
	}
	h, ok := hashOf(p.TokenHash)
	if !ok {
		return
	}
	e := &entry{auth: p, baseline: now}
	if prev != nil {
		e.lastSeen, e.version = prev.lastSeen, prev.version
		if string(prev.auth.TokenHash) == string(p.TokenHash) {
			e.baseline, e.dirty, e.silent = prev.baseline, prev.dirty, prev.silent
		}
	}
	if p.LastSeenAt != nil && p.LastSeenAt.After(e.lastSeen) {
		e.lastSeen = *p.LastSeenAt
	}
	r.byID[p.ID] = e
	r.byHash[h] = e
}

func hashKey(b []byte) [sha256.Size]byte {
	h, _ := hashOf(b)
	return h
}

// Remove forgets probe id (deleted, or its credential revoked).
func (r *Registry) Remove(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.byID[id]; ok {
		delete(r.byHash, hashKey(e.auth.TokenHash))
		delete(r.byID, id)
	}
}

// Authenticate resolves a probe credential. It never touches the database.
func (r *Registry) Authenticate(credential string) (model.ProbeAuth, bool) {
	if credential == "" {
		return model.ProbeAuth{}, false
	}
	h := sha256.Sum256([]byte(credential))
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.byHash[h]
	if !ok {
		return model.ProbeAuth{}, false
	}
	return e.auth, true
}

// Seen records contact from probe id (in memory; flushed in the background).
// A probe that had been reported silent is reported recovered.
func (r *Registry) Seen(id int64, version string) {
	r.mu.Lock()
	e, ok := r.byID[id]
	if !ok {
		r.mu.Unlock()
		return
	}
	now := r.o.Now()
	if now.After(e.lastSeen) {
		e.lastSeen = now
	}
	if version != "" {
		e.version = version
	}
	e.dirty = true
	recovered := e.silent
	e.silent = false
	p := e.auth
	r.mu.Unlock()
	if recovered && r.o.OnRecovered != nil {
		r.o.OnRecovered(p, now)
	}
}

// Online reports whether region is reporting: "local" always, any other
// region when one of its probes was seen within SilentAfter.
func (r *Registry) Online(region string) bool {
	if region == model.LocalRegion {
		return true
	}
	now := r.o.Now()
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.byID {
		if e.auth.Region == region && !e.lastSeen.IsZero() && now.Sub(e.lastSeen) <= r.o.SilentAfter {
			return true
		}
	}
	return false
}

// check reports every registered probe that just went silent.
func (r *Registry) check() {
	type silentEvent struct {
		p     model.ProbeAuth
		since time.Time
	}
	var fire []silentEvent
	now := r.o.Now()
	r.mu.Lock()
	for _, e := range r.byID {
		if e.silent {
			continue
		}
		if ref := e.reference(); now.Sub(ref) > r.o.SilentAfter {
			e.silent = true
			fire = append(fire, silentEvent{e.auth, ref})
		}
	}
	r.mu.Unlock()
	if r.o.OnSilent == nil {
		return
	}
	for _, f := range fire {
		r.o.OnSilent(f.p, f.since)
	}
}

// flush writes the probes seen since the last flush. On failure they stay
// dirty for the next attempt.
func (r *Registry) flush(ctx context.Context) error {
	r.mu.Lock()
	var seen []model.ProbeSeen
	for _, e := range r.byID {
		if e.dirty {
			e.dirty = false
			seen = append(seen, model.ProbeSeen{ID: e.auth.ID, At: e.lastSeen, Version: e.version})
		}
	}
	r.mu.Unlock()
	if len(seen) == 0 || r.o.Store == nil {
		return nil
	}
	if err := r.o.Store.TouchProbes(ctx, seen); err != nil {
		r.mu.Lock()
		for _, s := range seen {
			if e, ok := r.byID[s.ID]; ok {
				e.dirty = true
			}
		}
		r.mu.Unlock()
		return err
	}
	return nil
}

// Start runs the silence check and the last_seen flush until ctx is done or
// Shutdown is called. Calling it twice is a no-op.
func (r *Registry) Start(ctx context.Context) {
	r.lifeMu.Lock()
	defer r.lifeMu.Unlock()
	if r.started {
		return
	}
	r.started = true
	ctx, r.cancel = context.WithCancel(ctx)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		check := time.NewTicker(r.o.CheckEvery)
		defer check.Stop()
		flush := time.NewTicker(r.o.FlushEvery)
		defer flush.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-check.C:
				r.check()
			case <-flush.C:
				fctx, cancel := context.WithTimeout(ctx, flushTimeout)
				if err := r.flush(fctx); err != nil {
					r.o.Logger.Warn("probes: writing last_seen failed", "error", err)
				}
				cancel()
			}
		}
	}()
}

// Shutdown stops the background work and writes last_seen one final time.
func (r *Registry) Shutdown(ctx context.Context) error {
	r.lifeMu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.lifeMu.Unlock()
	r.wg.Wait()
	return r.flush(ctx)
}

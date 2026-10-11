package statuspage

import (
	"errors"
	"sync"
	"time"
)

// maxCacheEntries bounds the cache: a page has four variants, so this holds
// a few hundred pages.
const maxCacheEntries = 1000

// Cache variants of one page.
const (
	variantPage       = "page"
	variantPageCustom = "page-custom"
	variantFeed       = "feed"
	variantFeedCustom = "feed-custom"
)

// errRenderAborted is what waiters see when the render they waited for
// panicked.
var errRenderAborted = errors.New("statuspage: render aborted")

// cachedResponse is a rendered page or feed.
type cachedResponse struct {
	body        []byte
	contentType string
}

type cacheKey struct{ slug, variant string }

type cacheEntry struct {
	resp    cachedResponse
	expires time.Time
}

// flight is one render in progress; concurrent misses wait for it.
type flight struct {
	done chan struct{}
	resp cachedResponse
	err  error
}

// pageCache is the short-lived cache of rendered pages and feeds, keyed by
// slug and variant. Failed renders (404s, store errors) are never cached.
// It is safe for concurrent use.
type pageCache struct {
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	gen     uint64 // bumped by every invalidation
	entries map[cacheKey]cacheEntry
	flights map[cacheKey]*flight
}

func newPageCache(ttl time.Duration, now func() time.Time) *pageCache {
	return &pageCache{
		ttl: ttl, now: now,
		entries: make(map[cacheKey]cacheEntry),
		flights: make(map[cacheKey]*flight),
	}
}

// get returns the cached response for slug and variant, calling render on a
// miss. Concurrent misses for one key share a single render call. A render
// that raced with an invalidation is returned but not stored.
func (c *pageCache) get(slug, variant string, render func() (cachedResponse, error)) (cachedResponse, error) {
	key := cacheKey{slug, variant}
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && c.now().Before(e.expires) {
		c.mu.Unlock()
		return e.resp, nil
	}
	if f, ok := c.flights[key]; ok {
		c.mu.Unlock()
		<-f.done
		return f.resp, f.err
	}
	f := &flight{done: make(chan struct{})}
	c.flights[key] = f
	gen := c.gen
	c.mu.Unlock()

	// Finish the flight even if render panics, so waiters are not stuck.
	f.err = errRenderAborted
	defer func() {
		c.mu.Lock()
		delete(c.flights, key)
		if f.err == nil && gen == c.gen {
			c.store(key, f.resp)
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.resp, f.err = render()
	return f.resp, f.err
}

// store adds an entry, dropping expired ones first and, at the hard cap, the
// entry closest to expiry. c.mu must be held.
func (c *pageCache) store(key cacheKey, resp cachedResponse) {
	now := c.now()
	var oldest cacheKey
	var oldestAt time.Time
	for k, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, k)
		} else if oldestAt.IsZero() || e.expires.Before(oldestAt) {
			oldest, oldestAt = k, e.expires
		}
	}
	if _, replace := c.entries[key]; !replace && len(c.entries) >= maxCacheEntries {
		delete(c.entries, oldest)
	}
	c.entries[key] = cacheEntry{resp: resp, expires: now.Add(c.ttl)}
}

// invalidate drops every variant of slug.
func (c *pageCache) invalidate(slug string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	for k := range c.entries {
		if k.slug == slug {
			delete(c.entries, k)
		}
	}
}

// clear drops everything.
func (c *pageCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	clear(c.entries)
}

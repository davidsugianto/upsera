package statuspage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// cacheTestClock is a settable clock safe for concurrent use.
type cacheTestClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *cacheTestClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *cacheTestClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// cacheTestService serves the test page with a counting store and a clock
// the test advances.
func cacheTestService(t *testing.T) (*Service, *cacheTestClock, *atomic.Int32) {
	t.Helper()
	clock := &cacheTestClock{t: testNow}
	var calls atomic.Int32
	st := renderTestStore(renderTestPage())
	inner := st.GetPublishedPageBySlugFn
	st.GetPublishedPageBySlugFn = func(ctx context.Context, slug string) (model.StatusPage, error) {
		calls.Add(1)
		return inner(ctx, slug)
	}
	svc := newTestService(t, st, fakeStates{}, func(o *Options) { o.Now = clock.now })
	return svc, clock, &calls
}

func TestCachePageWithinTTL(t *testing.T) {
	svc, clock, calls := cacheTestService(t)
	h := svc.Handler()
	for i := range 3 {
		if rec := renderTestGet(h, http.MethodGet, "/status/acme"); rec.Code != http.StatusOK {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
		clock.advance(9 * time.Second)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("store calls within TTL = %d, want 1", n)
	}
	clock.advance(10 * time.Second) // 37s since the render
	renderTestGet(h, http.MethodGet, "/status/acme")
	if n := calls.Load(); n != 2 {
		t.Errorf("store calls after TTL = %d, want 2", n)
	}
}

func TestCacheVariantsAreSeparate(t *testing.T) {
	svc, _, calls := cacheTestService(t)
	h := svc.Handler()
	renderTestGet(h, http.MethodGet, "/status/acme")
	renderTestGet(h, http.MethodGet, "/status/acme/feed.xml")
	renderTestGet(h, http.MethodGet, "/status/acme")
	renderTestGet(h, http.MethodGet, "/status/acme/feed.xml")
	if n := calls.Load(); n != 2 {
		t.Errorf("store calls = %d, want 2 (one per variant)", n)
	}
}

func TestCacheInvalidatePage(t *testing.T) {
	svc, _, calls := cacheTestService(t)
	h := svc.Handler()
	renderTestGet(h, http.MethodGet, "/status/acme")
	renderTestGet(h, http.MethodGet, "/status/acme/feed.xml")
	svc.InvalidatePage("other") // other slugs are untouched
	renderTestGet(h, http.MethodGet, "/status/acme")
	if n := calls.Load(); n != 2 {
		t.Fatalf("store calls after unrelated invalidation = %d, want 2", n)
	}
	svc.InvalidatePage("acme")
	renderTestGet(h, http.MethodGet, "/status/acme")
	renderTestGet(h, http.MethodGet, "/status/acme/feed.xml")
	if n := calls.Load(); n != 4 {
		t.Errorf("store calls after InvalidatePage = %d, want 4 (both variants re-rendered)", n)
	}
}

func TestCacheInvalidateAll(t *testing.T) {
	svc, _, calls := cacheTestService(t)
	h := svc.Handler()
	renderTestGet(h, http.MethodGet, "/status/acme")
	svc.InvalidateAll()
	renderTestGet(h, http.MethodGet, "/status/acme")
	if n := calls.Load(); n != 2 {
		t.Errorf("store calls = %d, want 2", n)
	}
}

func TestCacheCustomDomainVariant(t *testing.T) {
	svc, _, calls := cacheTestService(t)
	svc.domains.replace(map[string]string{"status.acme.test": "acme"})
	h := svc.HostMiddleware(http.NotFoundHandler())
	for _, host := range []string{"status.acme.test", "status.acme.test"} {
		req := newDomainTestRequest(http.MethodGet, host, "/")
		rec := serveDomainTest(h, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	rec := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme")
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("store calls = %d, want 2 (custom-domain and normal pages are cached apart)", n)
	}
}

func TestCacheNothingCachedOnError(t *testing.T) {
	c := newPageCache(time.Minute, func() time.Time { return testNow })
	var calls int
	boom := errors.New("boom")
	for range 3 {
		if _, err := c.get("a", variantPage, func() (cachedResponse, error) { calls++; return cachedResponse{}, boom }); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	}
	if calls != 3 {
		t.Errorf("render calls = %d, want 3", calls)
	}
}

func TestCacheConcurrentMissesShareOneRender(t *testing.T) {
	c := newPageCache(time.Minute, func() time.Time { return testNow })
	var renders atomic.Int32
	release := make(chan struct{})
	render := func() (cachedResponse, error) {
		renders.Add(1)
		<-release
		return cachedResponse{body: []byte("x")}, nil
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			resp, err := c.get("a", variantPage, render)
			if err != nil || string(resp.body) != "x" {
				t.Errorf("get = %q, %v", resp.body, err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond) // let the waiters pile up behind the leader
	close(release)
	wg.Wait()
	if n := renders.Load(); n != 1 {
		t.Errorf("renders = %d, want 1", n)
	}
}

func TestCacheInvalidateDuringRenderIsNotStored(t *testing.T) {
	c := newPageCache(time.Minute, func() time.Time { return testNow })
	var calls int
	_, _ = c.get("a", variantPage, func() (cachedResponse, error) {
		calls++
		c.invalidate("a") // the page changed while it was rendering
		return cachedResponse{body: []byte("stale")}, nil
	})
	resp, _ := c.get("a", variantPage, func() (cachedResponse, error) {
		calls++
		return cachedResponse{body: []byte("fresh")}, nil
	})
	if calls != 2 || string(resp.body) != "fresh" {
		t.Errorf("calls = %d, body = %q, want 2 and fresh", calls, resp.body)
	}
}

func TestCacheIsBounded(t *testing.T) {
	clock := &cacheTestClock{t: testNow}
	c := newPageCache(time.Minute, clock.now)
	render := func() (cachedResponse, error) { return cachedResponse{body: []byte("x")}, nil }
	for i := range maxCacheEntries + 50 {
		_, _ = c.get(fmt.Sprintf("slug-%d", i), variantPage, render)
		clock.advance(time.Millisecond)
	}
	if n := len(c.entries); n != maxCacheEntries {
		t.Fatalf("entries = %d, want the cap %d", n, maxCacheEntries)
	}
	if _, ok := c.entries[cacheKey{"slug-0", variantPage}]; ok {
		t.Error("the entry closest to expiry should have been evicted")
	}
	clock.advance(2 * time.Minute) // everything expired: the next write sweeps it
	_, _ = c.get("fresh", variantPage, render)
	if n := len(c.entries); n != 1 {
		t.Errorf("entries after sweep = %d, want 1", n)
	}
}

func TestCacheConcurrentRace(t *testing.T) {
	svc, _, _ := cacheTestService(t)
	h := svc.Handler()
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() {
			switch i % 4 {
			case 0:
				svc.InvalidatePage("acme")
			case 1:
				svc.InvalidateAll()
			case 2:
				renderTestGet(h, http.MethodGet, "/status/acme/feed.xml")
			default:
				renderTestGet(h, http.MethodGet, "/status/acme")
			}
		})
	}
	wg.Wait()
}

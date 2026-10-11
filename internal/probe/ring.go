package probe

import (
	"sync"

	"github.com/davidsugianto/upsera/internal/probeproto"
)

// ring is a bounded FIFO of results. When full, Push drops the oldest result
// and counts it, so a long outage never grows memory and the newest results
// survive.
type ring struct {
	mu      sync.Mutex
	buf     []probeproto.Result
	head    int
	n       int
	dropped uint64
}

func newRing(capacity int) *ring {
	if capacity < 1 {
		capacity = 1
	}
	return &ring{buf: make([]probeproto.Result, capacity)}
}

// Push appends r, dropping the oldest result if the ring is full.
func (r *ring) Push(res probeproto.Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n == len(r.buf) {
		r.buf[r.head] = probeproto.Result{}
		r.head = (r.head + 1) % len(r.buf)
		r.n--
		r.dropped++
	}
	r.buf[(r.head+r.n)%len(r.buf)] = res
	r.n++
}

// Take removes and returns up to max of the oldest results, oldest first.
func (r *ring) Take(max int) []probeproto.Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	if max > r.n {
		max = r.n
	}
	if max <= 0 {
		return nil
	}
	out := make([]probeproto.Result, max)
	for i := range out {
		idx := (r.head + i) % len(r.buf)
		out[i] = r.buf[idx]
		r.buf[idx] = probeproto.Result{}
	}
	r.head = (r.head + max) % len(r.buf)
	r.n -= max
	return out
}

// PutFront returns a previously taken batch to the front of the ring. If the
// ring has since filled up, the oldest results of the batch are dropped (and
// counted) so that the ring stays within its capacity.
func (r *ring) PutFront(items []probeproto.Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if space := len(r.buf) - r.n; len(items) > space {
		drop := len(items) - space
		r.dropped += uint64(drop)
		items = items[drop:]
	}
	for i := len(items) - 1; i >= 0; i-- {
		r.head = (r.head - 1 + len(r.buf)) % len(r.buf)
		r.buf[r.head] = items[i]
		r.n++
	}
}

// countDropped records results discarded outside the ring (e.g. a batch the
// server permanently rejected).
func (r *ring) countDropped(n int) {
	r.mu.Lock()
	r.dropped += uint64(n)
	r.mu.Unlock()
}

// Len is the number of buffered results.
func (r *ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// Dropped is the total number of results discarded so far.
func (r *ring) Dropped() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

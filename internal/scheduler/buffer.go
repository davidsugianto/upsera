package scheduler

import (
	"log/slog"
	"sync"

	"github.com/davidsugianto/upsera/internal/model"
)

// heartbeatBuffer is a bounded in-memory FIFO of heartbeats awaiting a
// flush to the store. Add never blocks: once the buffer is at capacity it
// drops the oldest entry and counts the drop. Take and Requeue give the
// flusher at-least-once, order-preserving semantics: a batch that fails to
// insert goes back to the front, still subject to the capacity.
type heartbeatBuffer struct {
	mu      sync.Mutex
	cap     int
	items   []model.Heartbeat
	dropped int64
	warned  bool
	log     *slog.Logger
}

func newHeartbeatBuffer(capacity int, log *slog.Logger) *heartbeatBuffer {
	if capacity < 1 {
		capacity = 1
	}
	return &heartbeatBuffer{cap: capacity, items: make([]model.Heartbeat, 0, capacity), log: log}
}

// Add appends hb, dropping the oldest buffered heartbeat if the buffer is
// full.
func (b *heartbeatBuffer) Add(hb model.Heartbeat) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) >= b.cap {
		b.items = b.items[1:]
		b.dropped++
		b.warnDroppedLocked()
	}
	b.items = append(b.items, hb)
}

// warnDroppedLocked logs at most once between two resetWarnFlag calls.
func (b *heartbeatBuffer) warnDroppedLocked() {
	if b.warned {
		return
	}
	b.warned = true
	b.log.Warn("heartbeat buffer full, dropping oldest heartbeats", "dropped_total", b.dropped)
}

// resetWarnFlag allows the next drop to log again. The flusher calls this
// once per flush cycle so a sustained overflow logs at most once per cycle
// instead of once per dropped heartbeat.
func (b *heartbeatBuffer) resetWarnFlag() {
	b.mu.Lock()
	b.warned = false
	b.mu.Unlock()
}

// Take removes and returns up to max heartbeats from the front (oldest
// first). It returns nil if the buffer is empty.
func (b *heartbeatBuffer) Take(max int) []model.Heartbeat {
	b.mu.Lock()
	defer b.mu.Unlock()
	if max > len(b.items) {
		max = len(b.items)
	}
	if max == 0 {
		return nil
	}
	out := make([]model.Heartbeat, max)
	copy(out, b.items[:max])
	b.items = b.items[max:]
	return out
}

// Requeue puts a previously-Taken batch back at the front, ahead of
// whatever was added meanwhile, so order is preserved. If the combined
// size exceeds capacity the oldest entries (the front of batch) are
// dropped, same as Add would.
func (b *heartbeatBuffer) Requeue(batch []model.Heartbeat) {
	if len(batch) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	combined := make([]model.Heartbeat, 0, len(batch)+len(b.items))
	combined = append(combined, batch...)
	combined = append(combined, b.items...)
	if len(combined) > b.cap {
		overflow := len(combined) - b.cap
		combined = combined[overflow:]
		b.dropped += int64(overflow)
		b.warnDroppedLocked()
	}
	b.items = combined
}

// Len reports how many heartbeats are currently buffered.
func (b *heartbeatBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.items)
}

// Dropped reports the total number of heartbeats dropped since start.
func (b *heartbeatBuffer) Dropped() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}

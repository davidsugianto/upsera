package api

import (
	"fmt"
	"testing"
	"time"
)

// TestLoginLimiterPrunesStaleKeys confirms that failures from many distinct
// (IP, email) keys that are never retried do not accumulate in the map
// forever: once their failures have all aged out of loginWindow, a sweep
// (piggy-backed on a later allow/recordFailure call) removes them.
func TestLoginLimiterPrunesStaleKeys(t *testing.T) {
	l := newLoginLimiter()

	const staleKeys = 500
	for i := 0; i < staleKeys; i++ {
		l.recordFailure(fmt.Sprintf("10.0.0.%d", i%256), fmt.Sprintf("user%d@example.com", i))
	}
	if got := len(l.failures); got != staleKeys {
		t.Fatalf("failures map has %d keys after recording, want %d", got, staleKeys)
	}

	// Age every recorded failure out of the window, and force the sweep
	// throttle to allow a sweep on the next call.
	l.mu.Lock()
	for k, ts := range l.failures {
		aged := make([]time.Time, len(ts))
		for i, tt := range ts {
			aged[i] = tt.Add(-2 * loginWindow)
		}
		l.failures[k] = aged
	}
	l.lastSweep = time.Now().Add(-2 * loginWindow)
	l.mu.Unlock()

	// Any allow/recordFailure call should sweep every stale key away, not
	// just the one it's called for.
	if !l.allow("9.9.9.9", "trigger@example.com") {
		t.Fatal("allow() for a fresh key should not be blocked")
	}

	if got := len(l.failures); got != 0 {
		t.Fatalf("failures map has %d keys after sweep, want 0 (unbounded growth from one-off attempts)", got)
	}
}

package statuspage

import (
	"fmt"
	"testing"
	"time"
)

func limiterTestClock() (func() time.Time, func(time.Duration)) {
	now := testNow
	return func() time.Time { return now }, func(d time.Duration) { now = now.Add(d) }
}

func TestLimiterIP(t *testing.T) {
	clock, advance := limiterTestClock()
	l := newSubscribeLimiter(clock)
	for i := 0; i < subscribeIPMax; i++ {
		if _, ok := l.allowIP("1.2.3.4"); !ok {
			t.Fatalf("request %d refused", i+1)
		}
		advance(time.Minute)
	}
	wait, ok := l.allowIP("1.2.3.4")
	if ok {
		t.Fatal("over-limit request allowed")
	}
	if want := time.Hour - subscribeIPMax*time.Minute; wait != want {
		t.Fatalf("wait = %v, want %v", wait, want)
	}
	if _, ok := l.allowIP("5.6.7.8"); !ok {
		t.Fatal("other IP refused")
	}
	advance(wait)
	if _, ok := l.allowIP("1.2.3.4"); !ok {
		t.Fatal("still refused after the oldest attempt expired")
	}
}

func TestLimiterEmailCaseInsensitivePerPage(t *testing.T) {
	clock, _ := limiterTestClock()
	l := newSubscribeLimiter(clock)
	for i, e := range []string{"A@x.test", "a@X.test", "a@x.TEST"} {
		if _, ok := l.allowEmail(1, e); !ok {
			t.Fatalf("request %d refused", i+1)
		}
	}
	if _, ok := l.allowEmail(1, "A@x.test"); ok {
		t.Fatal("4th request allowed")
	}
	if _, ok := l.allowEmail(2, "a@x.test"); !ok {
		t.Fatal("same email on another page refused")
	}
}

func TestLimiterMemoryBounded(t *testing.T) {
	clock, advance := limiterTestClock()
	l := newSubscribeLimiter(clock)

	// Expired keys are swept.
	for i := 0; i < 1000; i++ {
		l.allowIP(fmt.Sprintf("10.0.%d.%d", i/256, i%256))
		l.allowEmail(1, fmt.Sprintf("u%d@x.test", i))
	}
	if n := l.size(); n != 2000 {
		t.Fatalf("size = %d, want 2000", n)
	}
	advance(subscribeWindow + 2*limiterSweepEvery)
	l.allowIP("9.9.9.9")
	if n := l.size(); n != 1 {
		t.Fatalf("size after expiry = %d, want 1", n)
	}

	// A flood of distinct keys within one window stays at the cap by evicting
	// old keys: new clients are never locked out (and a flood of fake
	// addresses cannot make every real visitor get refused).
	l = newSubscribeLimiter(clock)
	for i := 0; i < limiterMaxKeys+100; i++ {
		l.allowIP(fmt.Sprintf("k%d", i))
	}
	if n := len(l.ips); n > limiterMaxKeys {
		t.Fatalf("ip map grew to %d (cap %d)", n, limiterMaxKeys)
	}
	if _, ok := l.allowIP("brand-new"); !ok {
		t.Fatal("a new client was refused because the map is full")
	}
	if n := len(l.ips); n > limiterMaxKeys {
		t.Fatalf("ip map grew to %d (cap %d)", n, limiterMaxKeys)
	}
	// ...and a client that was just admitted is still limited.
	for i := 1; i < subscribeIPMax; i++ {
		l.allowIP("brand-new")
	}
	if _, ok := l.allowIP("brand-new"); ok {
		t.Fatal("the new client escaped its limit")
	}
}

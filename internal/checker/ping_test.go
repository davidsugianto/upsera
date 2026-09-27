package checker

import (
	"strings"
	"testing"

	"github.com/davidsugianto/upsera/internal/model"
)

func TestPingLoopback(t *testing.T) {
	t.Parallel()
	c := newTestChecker()
	m := mustMonitor(t, model.TypePing, PingConfig{Host: "127.0.0.1", Count: 2}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusUp {
		if strings.Contains(res.Message, "ping not permitted") {
			t.Skipf("unprivileged ICMP not permitted in this sandbox: %s", res.Message)
		}
		t.Fatalf("Status = %v, Message = %q, want Up", res.Status, res.Message)
	}
	if res.Latency <= 0 {
		t.Error("Latency (avg RTT) not measured")
	}
}

func TestPingCountTimesIntervalExceedsTimeout(t *testing.T) {
	t.Parallel()
	c := newTestChecker()
	// count=10 with pro-bing's default 1s Interval would take ~9s to send
	// all packets, well past this 3s timeout; the check must still come
	// up Up on loopback instead of a false Down.
	m := mustMonitor(t, model.TypePing, PingConfig{Host: "127.0.0.1", Count: 10}, 3)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusUp {
		if strings.Contains(res.Message, "ping not permitted") {
			t.Skipf("unprivileged ICMP not permitted in this sandbox: %s", res.Message)
		}
		t.Fatalf("Status = %v, Message = %q, want Up (count*interval > timeout should still succeed on loopback)", res.Status, res.Message)
	}
}

func TestPingBlockedTarget(t *testing.T) {
	t.Parallel()
	c := newTestChecker()
	m := mustMonitor(t, model.TypePing, PingConfig{Host: "169.254.169.254", Count: 1}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusDown || !strings.Contains(res.Message, "blocked") {
		t.Fatalf("got status=%v message=%q, want Down mentioning blocked", res.Status, res.Message)
	}
}

package checker

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"syscall"
	"time"

	probing "github.com/prometheus-community/pro-bing"

	"github.com/davidsugianto/upsera/internal/model"
)

func (c *Checker) checkPing(ctx context.Context, m model.Monitor, timeoutS int) Result {
	var cfg PingConfig
	if err := json.Unmarshal(m.Config, &cfg); err != nil {
		return Result{Status: model.StatusDown, Message: "invalid monitor config"}
	}
	count := cfg.Count
	if count <= 0 {
		count = 3
	}

	start := time.Now()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, cfg.Host)
	if err != nil || len(addrs) == 0 {
		if err == nil {
			err = errors.New("no such host")
		}
		return Result{Status: model.StatusDown, Latency: time.Since(start), Message: describeErr(err, timeoutS)}
	}
	ipAddr := addrs[0]

	ip, ok := netip.AddrFromSlice(ipAddr.IP)
	if !ok {
		return Result{Status: model.StatusDown, Latency: time.Since(start), Message: "invalid resolved address"}
	}
	ip = ip.Unmap()
	if err := c.policy.Check(ip); err != nil {
		return Result{Status: model.StatusDown, Latency: time.Since(start), Message: describeErr(err, timeoutS)}
	}

	pinger := probing.New(ip.String())
	pinger.SetIPAddr(&ipAddr)
	pinger.SetPrivileged(false)
	pinger.Count = count
	// pro-bing defaults to a 1s Interval between sends and only stops on
	// count or context cancellation; if count*Interval would outlast the
	// overall check deadline, the context expires mid-run and every
	// packet received so far would otherwise be thrown away below.
	// Tighten the interval to fit (never widen it past the 1s default).
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			if interval := remaining / time.Duration(count+1); interval < pinger.Interval {
				pinger.Interval = interval
			}
		}
	}

	runErr := pinger.RunWithContext(ctx)
	stats := pinger.Statistics()
	if runErr != nil {
		if errors.Is(runErr, syscall.EACCES) {
			return Result{Status: model.StatusDown, Latency: time.Since(start), Message: "ping not permitted: enable net.ipv4.ping_group_range"}
		}
		if errors.Is(runErr, context.DeadlineExceeded) && stats.PacketsRecv > 0 {
			// The overall check deadline cut the run short, but the
			// host answered at least once: it's reachable, just
			// slower than count*interval allowed for.
			return Result{Status: model.StatusUp, Latency: stats.AvgRtt}
		}
		return Result{Status: model.StatusDown, Latency: time.Since(start), Message: describeErr(runErr, timeoutS)}
	}
	if stats.PacketsRecv == 0 {
		return Result{Status: model.StatusDown, Latency: time.Since(start), Message: "no ping reply received"}
	}
	return Result{Status: model.StatusUp, Latency: stats.AvgRtt}
}

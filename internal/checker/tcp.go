package checker

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

func (c *Checker) checkTCP(ctx context.Context, m model.Monitor, timeoutS int) Result {
	var cfg TCPConfig
	if err := json.Unmarshal(m.Config, &cfg); err != nil {
		return Result{Status: model.StatusDown, Message: "invalid monitor config"}
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	start := time.Now()
	conn, err := c.policy.DialContext(ctx, "tcp", addr)
	latency := time.Since(start)
	if err != nil {
		return Result{Status: model.StatusDown, Latency: latency, Message: describeErr(err, timeoutS)}
	}
	conn.Close()
	return Result{Status: model.StatusUp, Latency: latency}
}

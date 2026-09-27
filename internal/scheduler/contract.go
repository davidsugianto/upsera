package scheduler

import (
	"errors"
	"time"
)

var (
	// ErrUnknownPushToken is returned by Push for a token that matches no
	// push monitor.
	ErrUnknownPushToken = errors.New("unknown push token")
	// ErrPaused is returned by Push when the monitor is paused.
	ErrPaused = errors.New("monitor is paused")
)

// Health describes the scheduler's view of the database and its heartbeat
// buffer.
type Health struct {
	DBReachable        bool
	BufferedHeartbeats int
	DroppedHeartbeats  int64     // total since start
	LastFlushAt        time.Time // zero until the first successful flush
}

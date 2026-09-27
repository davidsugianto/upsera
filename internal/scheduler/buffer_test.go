package scheduler

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

func hb(n int) model.Heartbeat {
	return model.Heartbeat{MonitorID: int64(n), Time: time.Unix(int64(n), 0)}
}

func TestBufferDropsOldestWhenFull(t *testing.T) {
	buf := newHeartbeatBuffer(3, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	for i := 1; i <= 3; i++ {
		buf.Add(hb(i))
	}
	if buf.Dropped() != 0 {
		t.Fatalf("dropped = %d, want 0 before overflow", buf.Dropped())
	}
	buf.Add(hb(4)) // overflow: drops #1
	buf.Add(hb(5)) // overflow: drops #2
	if got := buf.Dropped(); got != 2 {
		t.Fatalf("dropped = %d, want 2", got)
	}
	if got := buf.Len(); got != 3 {
		t.Fatalf("len = %d, want 3", got)
	}
	got := buf.Take(10)
	want := []int64{3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("Take = %v, want monitor ids %v", got, want)
	}
	for i, w := range want {
		if got[i].MonitorID != w {
			t.Fatalf("Take[%d].MonitorID = %d, want %d", i, got[i].MonitorID, w)
		}
	}
}

func TestBufferTakeRequeuePreservesOrder(t *testing.T) {
	buf := newHeartbeatBuffer(10, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	for i := 1; i <= 3; i++ {
		buf.Add(hb(i))
	}
	batch := buf.Take(2) // takes 1, 2
	buf.Add(hb(4))       // buffer now has [3, 4]
	buf.Requeue(batch)   // failed flush: put 1, 2 back at the front
	got := buf.Take(10)
	want := []int64{1, 2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("Take after requeue = %v, want ids %v", got, want)
	}
	for i, w := range want {
		if got[i].MonitorID != w {
			t.Fatalf("Take[%d].MonitorID = %d, want %d", i, got[i].MonitorID, w)
		}
	}
}

func TestBufferRequeueDropsOldestOnOverflow(t *testing.T) {
	buf := newHeartbeatBuffer(3, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	batch := []model.Heartbeat{hb(1), hb(2)}
	buf.Add(hb(3))
	buf.Add(hb(4))
	buf.Requeue(batch) // combined [1,2,3,4] over cap 3: drops 1
	if got := buf.Dropped(); got != 1 {
		t.Fatalf("dropped = %d, want 1", got)
	}
	got := buf.Take(10)
	want := []int64{2, 3, 4}
	for i, w := range want {
		if got[i].MonitorID != w {
			t.Fatalf("Take[%d].MonitorID = %d, want %d", i, got[i].MonitorID, w)
		}
	}
}

func TestBufferWarnsOnceUntilReset(t *testing.T) {
	var out bytes.Buffer
	buf := newHeartbeatBuffer(1, slog.New(slog.NewTextHandler(&out, nil)))
	buf.Add(hb(1))
	buf.Add(hb(2)) // drop #1: warns
	buf.Add(hb(3)) // drop #2: same cycle, no warn
	buf.Add(hb(4)) // drop #3: same cycle, no warn
	n := bytes.Count(out.Bytes(), []byte("heartbeat buffer full"))
	if n != 1 {
		t.Fatalf("warn log count = %d, want 1 before reset", n)
	}
	buf.resetWarnFlag()
	buf.Add(hb(5)) // drop #4: new cycle, warns again
	n = bytes.Count(out.Bytes(), []byte("heartbeat buffer full"))
	if n != 2 {
		t.Fatalf("warn log count = %d, want 2 after reset", n)
	}
}

func TestBufferTakeEmpty(t *testing.T) {
	buf := newHeartbeatBuffer(3, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if got := buf.Take(5); got != nil {
		t.Fatalf("Take on empty buffer = %v, want nil", got)
	}
}

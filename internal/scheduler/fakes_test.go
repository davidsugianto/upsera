package scheduler

import (
	"context"
	"sync"
	"time"

	"github.com/davidsugianto/upsera/internal/checker"
	"github.com/davidsugianto/upsera/internal/model"
)

// fakeStore is an in-memory scheduler.Store double with switchable
// failures.
type fakeStore struct {
	mu sync.Mutex

	failInsert bool
	failUpsert bool
	failPing   bool

	heartbeats  []model.Heartbeat
	states      map[int64]model.MonitorState
	insertCalls int
	upsertCalls int
	pingCalls   int
}

func newFakeStore() *fakeStore {
	return &fakeStore{states: make(map[int64]model.MonitorState)}
}

func (f *fakeStore) InsertHeartbeats(_ context.Context, hbs []model.Heartbeat) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.insertCalls++
	if f.failInsert {
		return errFake
	}
	f.heartbeats = append(f.heartbeats, hbs...)
	return nil
}

func (f *fakeStore) UpsertMonitorStates(_ context.Context, states []model.MonitorState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertCalls++
	if f.failUpsert {
		return errFake
	}
	for _, st := range states {
		f.states[st.MonitorID] = st
	}
	return nil
}

func (f *fakeStore) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pingCalls++
	if f.failPing {
		return errFake
	}
	return nil
}

func (f *fakeStore) setFail(insert, upsert, ping bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failInsert, f.failUpsert, f.failPing = insert, upsert, ping
}

func (f *fakeStore) heartbeatCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.heartbeats)
}

func (f *fakeStore) heartbeatsSnapshot() []model.Heartbeat {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]model.Heartbeat, len(f.heartbeats))
	copy(out, f.heartbeats)
	return out
}

func (f *fakeStore) counts() (insert, upsert, ping int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.insertCalls, f.upsertCalls, f.pingCalls
}

type fakeErr struct{ s string }

func (e *fakeErr) Error() string { return e.s }

var errFake = &fakeErr{"fake store error"}

// fakeChecker is a scheduler.Checker double. fn, if set, computes the
// result per call; otherwise it returns result. It tracks concurrency and
// call count/timestamps.
type fakeChecker struct {
	mu            sync.Mutex
	fn            func(m model.Monitor, call int) checker.Result
	result        checker.Result
	delay         time.Duration
	concurrent    int
	maxConcurrent int
	calls         int
	callTimes     []time.Time
}

func (f *fakeChecker) Check(ctx context.Context, m model.Monitor) checker.Result {
	f.mu.Lock()
	f.concurrent++
	if f.concurrent > f.maxConcurrent {
		f.maxConcurrent = f.concurrent
	}
	f.calls++
	call := f.calls
	f.callTimes = append(f.callTimes, time.Now())
	delay := f.delay
	f.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
		}
	}

	f.mu.Lock()
	var res checker.Result
	if f.fn != nil {
		res = f.fn(m, call)
	} else {
		res = f.result
	}
	f.concurrent--
	f.mu.Unlock()
	return res
}

func (f *fakeChecker) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeChecker) maxSeenConcurrent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxConcurrent
}

func (f *fakeChecker) times() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Time, len(f.callTimes))
	copy(out, f.callTimes)
	return out
}

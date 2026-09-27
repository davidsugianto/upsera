package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// fakeNotifier records every Send call and, if fail is set, fails the
// first failN calls.
type fakeNotifier struct {
	mu      sync.Mutex
	events  []Event
	failN   int
	delay   time.Duration
	sendErr error
}

func (f *fakeNotifier) Type() string                   { return "fake" }
func (f *fakeNotifier) Validate(json.RawMessage) error { return nil }
func (f *fakeNotifier) Send(ctx context.Context, _ json.RawMessage, ev Event) error {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	if len(f.events) <= f.failN {
		if f.sendErr != nil {
			return f.sendErr
		}
		return errors.New("boom")
	}
	return nil
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func TestDispatcherRetriesThreeTimes(t *testing.T) {
	t.Parallel()
	fn := &fakeNotifier{failN: 3} // always fails (only 3 attempts happen anyway)
	var mu sync.Mutex
	var attempts []Attempt
	d := NewDispatcher(map[model.ChannelType]Notifier{model.ChannelWebhook: fn}, func(a Attempt) {
		mu.Lock()
		attempts = append(attempts, a)
		mu.Unlock()
	}, testLogger())
	d.backoff = []time.Duration{0, 0}
	defer d.Close(context.Background())

	d.Enqueue(Job{Channel: model.Channel{ID: 1, Type: model.ChannelWebhook, Config: json.RawMessage(`{}`)}, DedupeKey: "k1"})

	waitUntil(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(attempts) == 3
	})

	mu.Lock()
	defer mu.Unlock()
	for i, a := range attempts {
		if a.N != i+1 {
			t.Errorf("attempts[%d].N = %d, want %d", i, a.N, i+1)
		}
		if a.Err == nil {
			t.Errorf("attempts[%d].Err = nil, want non-nil", i)
		}
	}
}

func TestDispatcherChannelUnavailableNotRetried(t *testing.T) {
	t.Parallel()
	fn := &fakeNotifier{}
	var mu sync.Mutex
	var attempts []Attempt
	d := NewDispatcher(map[model.ChannelType]Notifier{model.ChannelWebhook: fn}, func(a Attempt) {
		mu.Lock()
		attempts = append(attempts, a)
		mu.Unlock()
	}, testLogger())
	d.backoff = []time.Duration{0, 0}
	defer d.Close(context.Background())

	d.Enqueue(Job{Channel: model.Channel{ID: 2, Type: model.ChannelWebhook, DecryptErr: "cannot decrypt"}, DedupeKey: "k2"})

	waitUntil(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(attempts) >= 1
	})
	time.Sleep(100 * time.Millisecond) // make sure no further attempts arrive

	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 1 {
		t.Fatalf("len(attempts) = %d, want 1", len(attempts))
	}
	if !errors.Is(attempts[0].Err, ErrChannelUnavailable) {
		t.Errorf("Err = %v, want ErrChannelUnavailable", attempts[0].Err)
	}
	if fn.count() != 0 {
		t.Errorf("notifier was called %d times, want 0", fn.count())
	}
}

func TestDispatcherOrdersJobsPerChannel(t *testing.T) {
	t.Parallel()
	fn := &fakeNotifier{}
	d := NewDispatcher(map[model.ChannelType]Notifier{model.ChannelWebhook: fn}, nil, testLogger())
	ch := model.Channel{ID: 3, Type: model.ChannelWebhook, Config: json.RawMessage(`{}`)}
	for i := range 20 {
		d.Enqueue(Job{Channel: ch, Event: Event{Message: string(rune('a' + i))}, DedupeKey: string(rune('a' + i))})
	}
	d.Close(context.Background())

	fn.mu.Lock()
	defer fn.mu.Unlock()
	if len(fn.events) != 20 {
		t.Fatalf("len(events) = %d, want 20", len(fn.events))
	}
	for i, ev := range fn.events {
		want := string(rune('a' + i))
		if ev.Message != want {
			t.Errorf("events[%d].Message = %q, want %q", i, ev.Message, want)
		}
	}
}

func TestDispatcherCloseWaitsForQueuedJobs(t *testing.T) {
	t.Parallel()
	fn := &fakeNotifier{delay: 20 * time.Millisecond}
	d := NewDispatcher(map[model.ChannelType]Notifier{model.ChannelWebhook: fn}, nil, testLogger())
	ch := model.Channel{ID: 4, Type: model.ChannelWebhook, Config: json.RawMessage(`{}`)}
	for i := range 5 {
		d.Enqueue(Job{Channel: ch, DedupeKey: string(rune('a' + i))})
	}
	d.Close(context.Background())
	if fn.count() != 5 {
		t.Fatalf("count() = %d, want 5 (Close must wait for the queue to drain)", fn.count())
	}
}

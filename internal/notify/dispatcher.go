package notify

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

const (
	maxQueueLen    = 1000
	maxAttempts    = 3
	attemptTimeout = 10 * time.Second
)

// ErrChannelUnavailable is the error of a job whose channel config could
// not be decrypted (or whose type has no notifier). It is not retried.
var ErrChannelUnavailable = errors.New("channel config unavailable")

// Job is one event to deliver to one channel. DedupeKey identifies the
// logical notification across attempts (and in the notification log).
type Job struct {
	Channel   model.Channel
	Event     Event
	DedupeKey string
}

// Attempt reports one send attempt; N counts from 1 and Err is nil on
// success.
type Attempt struct {
	Job Job
	N   int
	Err error
	At  time.Time
}

// channelQueue holds one channel's pending jobs. Its worker goroutine
// exists only while the queue is non-empty: it retires (removing the
// queue) once drained, so deleted channels leave nothing behind.
type channelQueue struct {
	channelID int64
	jobs      []Job
}

// Dispatcher delivers jobs through one worker goroutine per channel, so a
// slow or failing channel never delays another, and jobs on one channel
// are sent in order. Each job gets up to 3 attempts.
type Dispatcher struct {
	notifiers map[model.ChannelType]Notifier
	onAttempt func(Attempt)
	log       *slog.Logger
	backoff   []time.Duration // wait before attempt 2, 3, …; overridable in tests

	ctx    context.Context // cancelled when Close gives up waiting
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	queues map[int64]*channelQueue
	closed bool
}

// NewDispatcher returns a running dispatcher. onAttempt (may be nil) is
// called after every attempt, from the channel's worker goroutine.
func NewDispatcher(notifiers map[model.ChannelType]Notifier, onAttempt func(Attempt), log *slog.Logger) *Dispatcher {
	if log == nil {
		log = slog.Default()
	}
	if onAttempt == nil {
		onAttempt = func(Attempt) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{
		notifiers: notifiers,
		onAttempt: onAttempt,
		log:       log,
		backoff:   []time.Duration{time.Second, 5 * time.Second},
		ctx:       ctx,
		cancel:    cancel,
		queues:    make(map[int64]*channelQueue),
	}
}

// Enqueue queues j on its channel's worker. It never blocks: when the
// channel's queue is full, its oldest job is dropped.
func (d *Dispatcher) Enqueue(j Job) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		d.log.Warn("notify: dispatcher closed, dropping job", "channel_id", j.Channel.ID, "dedupe_key", j.DedupeKey)
		return
	}
	q, ok := d.queues[j.Channel.ID]
	if !ok {
		q = &channelQueue{channelID: j.Channel.ID}
		d.queues[j.Channel.ID] = q
		d.wg.Add(1)
		go d.worker(q)
	}
	if len(q.jobs) >= maxQueueLen {
		d.log.Warn("notify: channel queue full, dropping oldest job",
			"channel_id", j.Channel.ID, "dropped_dedupe_key", q.jobs[0].DedupeKey)
		q.jobs[0] = Job{}
		q.jobs = q.jobs[1:]
	}
	q.jobs = append(q.jobs, j)
}

// SendNow makes one synchronous attempt (used for test sends), reported
// through onAttempt like any other.
func (d *Dispatcher) SendNow(ctx context.Context, j Job) error {
	err := d.attempt(ctx, j)
	d.onAttempt(Attempt{Job: j, N: 1, Err: err, At: time.Now()})
	return err
}

// Close stops accepting jobs and waits until every queued job has been
// delivered or ctx is done, in which case in-flight sends are cancelled.
func (d *Dispatcher) Close(ctx context.Context) {
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()

	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		d.cancel()
		<-done
	}
	d.cancel()
}

func (d *Dispatcher) worker(q *channelQueue) {
	defer d.wg.Done()
	for {
		d.mu.Lock()
		if len(q.jobs) == 0 {
			delete(d.queues, q.channelID)
			d.mu.Unlock()
			return
		}
		j := q.jobs[0]
		q.jobs[0] = Job{}
		q.jobs = q.jobs[1:]
		d.mu.Unlock()

		if d.ctx.Err() != nil {
			return
		}
		d.deliver(j)
	}
}

// deliver runs up to maxAttempts attempts of j with backoff in between.
func (d *Dispatcher) deliver(j Job) {
	for n := 1; ; n++ {
		err := d.attempt(d.ctx, j)
		d.onAttempt(Attempt{Job: j, N: n, Err: err, At: time.Now()})
		if err == nil || n >= maxAttempts || errors.Is(err, ErrChannelUnavailable) {
			if err != nil {
				d.log.Warn("notify: giving up", "channel_id", j.Channel.ID, "dedupe_key", j.DedupeKey,
					"attempts", n, "err", err)
			}
			return
		}
		var wait time.Duration
		if n-1 < len(d.backoff) {
			wait = d.backoff[n-1]
		}
		if wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-t.C:
			case <-d.ctx.Done():
				t.Stop()
				return
			}
		}
		if d.ctx.Err() != nil {
			return
		}
	}
}

func (d *Dispatcher) attempt(ctx context.Context, j Job) error {
	n := d.notifiers[j.Channel.Type]
	if n == nil || j.Channel.DecryptErr != "" || j.Channel.Config == nil {
		return ErrChannelUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	return n.Send(ctx, j.Channel.Config, j.Event)
}

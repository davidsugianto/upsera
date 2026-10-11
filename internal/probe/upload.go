package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/davidsugianto/upsera/internal/probeproto"
)

// uploadLoop sends buffered results: every FlushInterval, right after a check
// finishes (when no retry backoff is pending), and once more when stop is
// closed. After a failed upload it waits with exponential backoff (up to
// backoffMax) and keeps the batch in the buffer. It returns when ctx is done
// or after the final flush.
func (a *Agent) uploadLoop(ctx context.Context, stop <-chan struct{}) {
	ticker := time.NewTicker(a.cfg.FlushInterval)
	defer ticker.Stop()
	var retry <-chan time.Time
	var backoff time.Duration
	var lastDropped uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			a.finalFlush(ctx)
			return
		case <-retry:
			retry = nil
		case <-a.wake:
			if retry != nil {
				continue
			}
		case <-ticker.C:
			if retry != nil {
				continue
			}
		}

		err := a.flush(ctx)
		if d := a.buf.Dropped(); d > lastDropped {
			a.log.Warn("result buffer overflowed; oldest results dropped", "dropped_total", d, "buffered", a.buf.Len())
			lastDropped = d
		}
		var fe *FatalError
		switch {
		case err == nil:
			backoff = 0
		case errors.As(err, &fe):
			a.log.Error("probe stopped", "reason", fe.Msg)
			a.setFatal(fe)
			return
		case ctx.Err() != nil:
			return
		default:
			if backoff == 0 {
				backoff = a.backoffBase
			} else {
				backoff = min(backoff*2, a.backoffMax)
			}
			a.log.Warn("uploading results failed; keeping them buffered", "err", err, "buffered", a.buf.Len(), "retry_in", backoff)
			retry = time.After(backoff)
		}
	}
}

// finalFlush makes one bounded attempt to upload whatever is buffered.
func (a *Agent) finalFlush(ctx context.Context) {
	if a.buf.Len() == 0 {
		return
	}
	fctx, cancel := context.WithTimeout(ctx, shutdownFlushTimeout)
	defer cancel()
	if err := a.flush(fctx); err != nil {
		a.log.Warn("final upload failed; buffered results lost", "err", err, "buffered", a.buf.Len())
		return
	}
	a.log.Info("final upload done")
}

// flush uploads batches until the buffer is empty or an upload fails. A
// failed batch goes back to the front of the buffer. A batch the server
// rejects for good (400/413/422) is dropped so it cannot block newer results.
func (a *Agent) flush(ctx context.Context) error {
	for ctx.Err() == nil {
		batch := a.buf.Take(probeproto.MaxResultsPerBatch)
		if len(batch) == 0 {
			return nil
		}
		body, used := encodeBatch(a.cfg.Version, batch)
		if used == 0 {
			a.log.Error("cannot encode results batch; dropping it", "results", len(batch))
			a.buf.countDropped(len(batch))
			continue
		}
		if used < len(batch) {
			a.buf.PutFront(batch[used:])
			batch = batch[:used]
		}
		err := a.post(ctx, body)
		switch {
		case err == nil:
		case errors.Is(err, errRejected):
			a.log.Warn("server rejected a results batch; dropping it", "err", err, "results", len(batch))
			a.buf.countDropped(len(batch))
		default:
			a.buf.PutFront(batch)
			return err
		}
	}
	return ctx.Err()
}

// encodeBatch marshals as many leading results as fit in MaxBatchBytes.
func encodeBatch(version string, batch []probeproto.Result) (body []byte, used int) {
	for n := len(batch); n >= 1; n = n / 2 {
		b, err := json.Marshal(probeproto.ResultsRequest{SentAt: time.Now().UTC(), Version: version, Results: batch[:n]})
		if err == nil && (len(b) <= probeproto.MaxBatchBytes || n == 1) {
			return b, n
		}
	}
	return nil, 0
}

func (a *Agent) post(ctx context.Context, body []byte) error {
	_, status, err := a.postJSON(ctx, probeproto.PathResults, a.credential, body)
	if err != nil {
		return err
	}
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusUnauthorized:
		return unauthorized("results")
	case status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge || status == http.StatusUnprocessableEntity:
		return fmt.Errorf("%w (HTTP %d)", errRejected, status)
	default:
		return fmt.Errorf("results: unexpected HTTP %d", status)
	}
}

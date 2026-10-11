package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/probeproto"
)

// worker is the goroutine checking one assigned monitor.
type worker struct {
	as     probeproto.Assignment
	cancel context.CancelFunc
}

// pollLoop polls the assignments immediately and then every PollInterval
// until ctx is done.
func (a *Agent) pollLoop(ctx context.Context) {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := a.poll(ctx); err != nil && ctx.Err() == nil {
			var fe *FatalError
			if errors.As(err, &fe) {
				a.log.Error("probe stopped", "reason", fe.Msg)
				a.setFatal(fe)
				return
			}
			a.log.Warn("fetching assignments failed; keeping current monitors", "err", err)
		}
		t.Reset(a.cfg.PollInterval)
	}
}

// poll fetches the assignments once (conditionally) and reconciles the
// running workers with the answer.
func (a *Agent) poll(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+probeproto.PathAssignments, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.credential)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", a.userAgent())
	if a.etag != "" {
		req.Header.Set("If-None-Match", a.etag)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil
	case http.StatusUnauthorized:
		return unauthorized("assignments")
	case http.StatusOK:
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("assignments: unexpected HTTP %d", resp.StatusCode)
	}

	var ar probeproto.AssignmentsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(&ar); err != nil {
		return fmt.Errorf("assignments: decode: %w", err)
	}
	a.policy.SetBlockPrivate(ar.BlockPrivateTargets)
	a.reconcile(ctx, ar.Monitors)
	a.etag = resp.Header.Get("ETag")
	if a.etag == "" && ar.Version != "" {
		a.etag = `"` + ar.Version + `"`
	}
	return nil
}

// reconcile starts, restarts and stops workers so exactly the given
// assignments run. Only the Run goroutine calls it.
func (a *Agent) reconcile(ctx context.Context, as []probeproto.Assignment) {
	want := make(map[int64]probeproto.Assignment, len(as))
	for _, x := range as {
		if !model.MonitorType(x.Type).RemoteCapable() {
			a.log.Warn("ignoring assignment of a type probes cannot run", "monitor_id", x.MonitorID, "type", x.Type)
			continue
		}
		want[x.MonitorID] = x
	}
	for id, w := range a.workers {
		if nx, ok := want[id]; !ok || !sameAssignment(w.as, nx) {
			w.cancel()
			delete(a.workers, id)
			if !ok {
				a.log.Info("monitor unassigned", "monitor_id", id)
			}
		}
	}
	for id, x := range want {
		if _, ok := a.workers[id]; ok {
			continue
		}
		wctx, cancel := context.WithCancel(ctx)
		a.workers[id] = &worker{as: x, cancel: cancel}
		a.log.Info("monitor assigned", "monitor_id", id, "type", x.Type, "interval_s", x.IntervalS)
		a.wg.Go(func() { a.runMonitor(wctx, x) })
	}
}

func sameAssignment(a, b probeproto.Assignment) bool {
	return a.MonitorID == b.MonitorID && a.Name == b.Name && a.Type == b.Type &&
		a.IntervalS == b.IntervalS && a.RetryIntervalS == b.RetryIntervalS &&
		a.Retries == b.Retries && a.TimeoutS == b.TimeoutS && bytes.Equal(a.Config, b.Config)
}

func monitorFromAssignment(x probeproto.Assignment) model.Monitor {
	return model.Monitor{
		ID:             x.MonitorID,
		Name:           x.Name,
		Type:           model.MonitorType(x.Type),
		Config:         x.Config,
		IntervalS:      x.IntervalS,
		RetryIntervalS: x.RetryIntervalS,
		Retries:        x.Retries,
		TimeoutS:       x.TimeoutS,
	}
}

// nextDelayS is the seconds to wait after a check: the retry interval while
// the consecutive failures so far are within the monitor's Retries, else the
// regular interval.
func nextDelayS(x probeproto.Assignment, consecutiveFailures int) int {
	interval := x.IntervalS
	if interval < 1 {
		interval = model.DefaultIntervalS
	}
	if consecutiveFailures > 0 && consecutiveFailures <= x.Retries && x.RetryIntervalS > 0 {
		return x.RetryIntervalS
	}
	return interval
}

// runMonitor checks one monitor until ctx is cancelled.
func (a *Agent) runMonitor(ctx context.Context, x probeproto.Assignment) {
	m := monitorFromAssignment(x)
	jitterS := min(max(x.IntervalS, 1), maxFirstCheckJitterS)
	timer := time.NewTimer(rand.N(time.Duration(jitterS) * a.unit))
	defer timer.Stop()
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		select {
		case a.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		started := time.Now()
		res := a.chk.Check(ctx, m)
		<-a.sem
		if ctx.Err() != nil {
			return // aborted by shutdown or reassignment: not a real result
		}

		ok := res.Status == model.StatusUp
		if ok {
			fails = 0
		} else {
			fails++
		}
		a.buf.Push(probeproto.Result{
			MonitorID:    x.MonitorID,
			Time:         started.UTC(),
			OK:           ok,
			LatencyMs:    int32(min(res.Latency.Milliseconds(), 1<<31-1)),
			Message:      res.Message,
			TLSExpiresAt: res.TLSExpiresAt,
		})
		select {
		case a.wake <- struct{}{}:
		default:
		}
		a.log.Debug("check done", "monitor_id", x.MonitorID, "ok", ok, "latency_ms", res.Latency.Milliseconds())

		timer.Reset(max(time.Duration(nextDelayS(x, fails))*a.unit-time.Since(started), 0))
	}
}

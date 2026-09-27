package alerting

import (
	"context"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/notify"
)

const (
	maxOutboxLogs = 10000
	flushTimeout  = 15 * time.Second
	maxErrorLen   = 500
)

// recordAttempt queues a notification log entry for one send attempt. It
// is called from dispatcher workers.
func (e *Engine) recordAttempt(at notify.Attempt) {
	j := at.Job
	entry := model.NotificationLogEntry{
		TeamID:    j.Channel.TeamID,
		ChannelID: new(j.Channel.ID),
		Event:     j.Event.Kind,
		DedupeKey: j.DedupeKey,
		Attempt:   at.N,
		OK:        at.Err == nil,
		At:        at.At,
	}
	if j.Event.MonitorID != 0 {
		entry.MonitorID = new(j.Event.MonitorID)
	}
	if j.Event.AlertID != "" {
		entry.AlertID = new(j.Event.AlertID)
	}
	if at.Err != nil {
		msg := []rune(at.Err.Error())
		if len(msg) > maxErrorLen {
			msg = msg[:maxErrorLen]
		}
		entry.Error = string(msg)
	}
	e.outMu.Lock()
	e.appendLogsLocked([]model.NotificationLogEntry{entry})
	e.outMu.Unlock()
}

// appendLogsLocked appends entries, dropping the oldest beyond capacity.
// Callers hold e.outMu.
func (e *Engine) appendLogsLocked(entries []model.NotificationLogEntry) {
	e.logs = append(e.logs, entries...)
	if over := len(e.logs) - maxOutboxLogs; over > 0 {
		if !e.logDropWarn {
			e.logDropWarn = true
			e.log.Warn("alerting: notification log outbox full, dropping oldest entries")
		}
		clear(e.logs[:over])
		e.logs = e.logs[over:]
	}
}

func (e *Engine) flushLoop(ctx context.Context) {
	defer e.wg.Done()
	ticker := time.NewTicker(e.opts.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fctx, cancel := context.WithTimeout(ctx, flushTimeout)
			_ = e.flush(fctx)
			cancel()
		}
	}
}

// flush writes dirty alerts, then queued log entries (in that order, so
// log rows can reference the alerts). On failure everything is requeued
// for the next flush; nothing is lost to a database outage. Resolved
// alerts are forgotten once written.
func (e *Engine) flush(ctx context.Context) error {
	e.mu.Lock()
	alerts := make([]model.Alert, 0, len(e.dirty))
	for id := range e.dirty {
		if a, ok := e.byID[id]; ok {
			alerts = append(alerts, copyAlert(a))
		}
	}
	e.dirty = make(map[string]struct{})
	e.mu.Unlock()

	e.outMu.Lock()
	logs := e.logs
	e.logs = nil
	e.outMu.Unlock()

	if len(alerts) == 0 && len(logs) == 0 {
		return nil
	}
	if len(alerts) > 0 {
		if err := e.st.UpsertAlerts(ctx, alerts); err != nil {
			e.requeue(alerts, logs)
			e.markUnreachable(err)
			return err
		}
		e.forgetResolved(alerts)
	}
	if len(logs) > 0 {
		if err := e.st.InsertNotificationLog(ctx, logs); err != nil {
			e.requeue(nil, logs)
			e.markUnreachable(err)
			return err
		}
	}
	e.markReachable()
	e.outMu.Lock()
	e.logDropWarn = false
	e.outMu.Unlock()
	return nil
}

// forgetResolved drops written resolved alerts from memory, unless they
// changed again since.
func (e *Engine) forgetResolved(written []model.Alert) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range written {
		if a.ResolvedAt == nil {
			continue
		}
		if _, again := e.dirty[a.ID]; !again {
			delete(e.byID, a.ID)
			delete(e.warned, a.ID)
		}
	}
}

// requeue marks alerts dirty again (the next flush re-reads their current
// value) and puts logs back in front of entries queued since.
func (e *Engine) requeue(alerts []model.Alert, logs []model.NotificationLogEntry) {
	if len(alerts) > 0 {
		e.mu.Lock()
		for _, a := range alerts {
			if _, ok := e.byID[a.ID]; ok {
				e.dirty[a.ID] = struct{}{}
			}
		}
		e.mu.Unlock()
	}
	if len(logs) > 0 {
		e.outMu.Lock()
		newer := e.logs
		e.logs = logs
		e.appendLogsLocked(newer)
		e.outMu.Unlock()
	}
}

func (e *Engine) markUnreachable(err error) {
	e.outMu.Lock()
	was := e.dbReachable
	e.dbReachable = false
	e.outMu.Unlock()
	if was {
		e.log.Warn("alerting: database unreachable, keeping alerts in memory", "error", err)
	}
}

func (e *Engine) markReachable() {
	e.outMu.Lock()
	was := e.dbReachable
	e.dbReachable = true
	e.outMu.Unlock()
	if !was {
		e.log.Info("alerting: database reachable again")
	}
}

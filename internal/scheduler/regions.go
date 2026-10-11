package scheduler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// Multi-region monitors.
//
// A monitor that is not LocalOnly (and whose type is RemoteCapable) is
// checked from several regions. Each region's raw results (from a remote
// probe via Report, or from the built-in checker as region "local") run
// through the same nextState retry logic, per region. The per-region states
// are then combined into the monitor's one MonitorState:
//
//  1. fresh regions in DOWN >= quorum            → DOWN
//  2. fewer regions reporting than the quorum    → PENDING "probe silent: …"
//     (a silent probe never opens a down alert)
//  3. fresh DOWN + fresh PENDING >= quorum       → PENDING
//  4. otherwise                                  → UP
//
// A region is fresh when its last result is at most 2*IntervalS old. A region
// that has not reported yet is unknown until the grace period (2*IntervalS
// after the scheduler started, the monitor was (re)configured or its
// maintenance ended) is over; unknown regions are neither down nor silent.
// During the grace period, with nothing fresh to go on, the state is left
// alone.
//
// Time passing changes the outcome (a probe going silent), so a sweep
// re-evaluates every multi-region monitor every sweepInterval.

// defaultSweepInterval is how often staleness is re-evaluated.
const defaultSweepInterval = 5 * time.Second

// freshWindows is how many check intervals a region's last result stays fresh.
const freshWindows = 2

// isMultiRegion reports whether m uses the multi-region path. Everything
// else (local-only, push, docker) keeps the original single-region path.
func isMultiRegion(m model.Monitor) bool {
	return !m.LocalOnly() && m.Type.RemoteCapable()
}

// aggregate is the combined verdict of a monitor's regions.
type aggregate struct {
	decided  bool // false: nothing fresh to go on yet (startup grace)
	status   model.Status
	silent   bool // PENDING because too few regions are reporting
	failures int
	msg      string
}

// graceEnd is when regions that have not reported (or only have old data)
// start counting as silent for m.
func (s *Scheduler) graceEnd(m model.Monitor) time.Time {
	anchor, ok := s.graceFrom[m.ID]
	if !ok {
		anchor = s.startedAt
	}
	return anchor.Add(freshWindows * time.Duration(m.IntervalS) * s.unit)
}

// aggregateLocked combines regs into one verdict at time now. Callers hold
// s.mu.
func (s *Scheduler) aggregateLocked(m model.Monitor, regs map[string]model.RegionState, now time.Time) aggregate {
	names := m.EffectiveRegions()
	quorum := min(m.EffectiveQuorum(), len(names))
	window := freshWindows * time.Duration(m.IntervalS) * s.unit
	inGrace := now.Before(s.graceEnd(m))

	var nFresh, nUnknown, nDown, nPending, downFail, pendFail int
	var downMsg, pendMsg string
	var notReporting []string
	for _, name := range names {
		rs, ok := regs[name]
		switch {
		case ok && now.Sub(rs.LastResultAt) <= window:
			nFresh++
			switch rs.Status {
			case model.StatusDown:
				if nDown == 0 {
					downMsg = rs.Message
				}
				nDown++
				downFail = max(downFail, rs.ConsecutiveFailures)
			case model.StatusPending:
				if nPending == 0 {
					pendMsg = rs.Message
				}
				nPending++
				pendFail = max(pendFail, rs.ConsecutiveFailures)
			}
		case inGrace:
			nUnknown++
			notReporting = append(notReporting, name)
		default:
			notReporting = append(notReporting, name)
		}
	}

	switch {
	case nDown >= quorum:
		return aggregate{decided: true, status: model.StatusDown, failures: downFail, msg: downMsg}
	case nFresh+nUnknown < quorum:
		msg := fmt.Sprintf("probe silent: %d of %d regions reporting (%s silent)",
			nFresh, len(names), strings.Join(notReporting, ", "))
		return aggregate{decided: true, status: model.StatusPending, silent: true, msg: model.TruncateMessage(msg)}
	case nFresh == 0:
		return aggregate{} // only possible during the grace period
	case nDown+nPending >= quorum:
		msg, fails := pendMsg, pendFail
		if nDown > 0 {
			msg, fails = downMsg, max(downFail, pendFail)
		}
		return aggregate{decided: true, status: model.StatusPending, failures: fails, msg: msg}
	}
	return aggregate{decided: true, status: model.StatusUp}
}

// commitLocked stores the monitor state implied by agg (and regs) at time at
// and emits the resulting transition; hb, if set, is buffered with the new
// status. Undecided aggregates leave the status alone and only refresh the
// stored region states (hb is the caller's to buffer then). Callers hold
// s.mu.
func (s *Scheduler) commitLocked(m model.Monitor, regs map[string]model.RegionState, agg aggregate,
	at time.Time, tlsAt *time.Time, msg string, hb *model.Heartbeat) {

	id := m.ID
	prev, havePrev := s.states[id]
	if !agg.decided {
		if havePrev {
			prev.Regions = nilIfEmpty(regs)
			s.states[id] = prev
			s.dirty[id] = struct{}{}
		}
		return
	}
	if havePrev && at.Before(prev.LastCheckAt) {
		at = prev.LastCheckAt
	}
	ns := model.MonitorState{
		MonitorID:           id,
		Status:              agg.status,
		LastCheckAt:         at,
		ConsecutiveFailures: agg.failures,
		TLSExpiresAt:        prev.TLSExpiresAt,
		FlapCount:           prev.FlapCount,
		Regions:             nilIfEmpty(regs),
	}
	if tlsAt != nil {
		ns.TLSExpiresAt = tlsAt
	}
	ns.Since = at
	switch {
	case havePrev && prev.Status == ns.Status:
		ns.Since = prev.Since
	case havePrev && prev.Status == model.StatusPending && prev.ConsecutiveFailures > 0 && ns.Status == model.StatusDown:
		// Like nextState: an outage starts at its first failed check.
		ns.Since = prev.Since
	}
	if hb != nil {
		hb.Status = ns.Status
		s.buf.Add(*hb)
	}
	// Emitted under s.mu so transitions reach OnTransition in the order they
	// were applied.
	tr := s.storeStateLocked(prev, havePrev, ns, msg)
	if tr != nil {
		tr.TeamID = m.TeamID
	}
	s.emit(tr)
}

func nilIfEmpty(regs map[string]model.RegionState) map[string]model.RegionState {
	if len(regs) == 0 {
		return nil
	}
	return regs
}

// enterMaintenanceLocked moves m into MAINTENANCE (the state machine's
// normal maintenance handling) and remembers that, so the grace period starts
// when the window ends. Callers hold s.mu.
func (s *Scheduler) enterMaintenanceLocked(m model.Monitor, name string, at time.Time) model.MonitorState {
	s.maintSeen[m.ID] = true
	prev, havePrev := s.states[m.ID]
	ns := nextState(prev, havePrev, model.StatusMaintenance, at, m.Retries)
	ns.MonitorID = m.ID
	ns.Regions = nilIfEmpty(s.regions[m.ID])
	tr := s.storeStateLocked(prev, havePrev, ns, model.TruncateMessage("maintenance: "+name))
	if tr != nil {
		tr.TeamID = m.TeamID
	}
	s.emit(tr)
	return ns
}

// leaveMaintenanceLocked starts a fresh grace period for m if it was in
// maintenance: results were ignored meanwhile, so every region looks old.
func (s *Scheduler) leaveMaintenanceLocked(id int64, now time.Time) {
	if s.maintSeen[id] {
		delete(s.maintSeen, id)
		s.graceFrom[id] = now
	}
}

// Report feeds raw check results of the probe probeID, which serves region,
// into the monitors' per-region state machines. Results for unknown
// monitors, monitors that are not checked from region, paused monitors and
// push/docker/local-only monitors are ignored, as is region "local" (the
// built-in checker feeds that itself). A result older than the region's last
// accepted one is out of order: it only becomes a heartbeat (history), the
// state is not touched. During a maintenance window results are ignored and
// a MAINTENANCE heartbeat is written. It does not block on the database.
func (s *Scheduler) Report(probeID int64, region string, rs []model.RegionResult) {
	if region == model.LocalRegion || len(rs) == 0 {
		return
	}
	rs = slices.Clone(rs)
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Time.Before(rs[j].Time) })
	for _, r := range rs {
		name, inMaint := s.maintenanceActive(r.MonitorID, s.now())
		s.ingest(probeID, region, r, name, inMaint)
	}
}

// ingest applies one region result (or, with inMaint, a maintenance
// observation) to its monitor. It returns the region's status afterwards and
// whether the result was applied (as opposed to ignored).
func (s *Scheduler) ingest(probeID int64, region string, r model.RegionResult, maintName string, inMaint bool) (model.Status, bool) {
	if !inMaint && r.Status != model.StatusUp && r.Status != model.StatusDown {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.monitors[r.MonitorID]
	if !ok || m.Paused || !isMultiRegion(m) || !m.InRegion(region) {
		return 0, false
	}
	id := m.ID
	now := s.now()
	prev, havePrev := s.states[id]
	regs := s.regions[id]
	last, hasLast := regs[region]

	if inMaint {
		msg := model.TruncateMessage("maintenance: " + maintName)
		ns := s.enterMaintenanceLocked(m, maintName, now)
		s.buf.Add(model.Heartbeat{MonitorID: id, ProbeID: probeID, Time: now, Status: ns.Status, Message: msg})
		return last.Status, true
	}

	msg := model.TruncateMessage(r.Message)
	if r.Time.After(now) {
		r.Time = now
	}
	hb := model.Heartbeat{MonitorID: id, ProbeID: probeID, Time: r.Time, LatencyMs: r.LatencyMs, Message: msg}

	if hasLast && r.Time.Before(last.LastResultAt) {
		// Out of order: history only.
		hb.Status = r.Status
		if havePrev {
			hb.Status = prev.Status
		}
		s.buf.Add(hb)
		return last.Status, true
	}

	s.leaveMaintenanceLocked(id, now)
	rns := nextState(model.MonitorState{Status: last.Status, ConsecutiveFailures: last.ConsecutiveFailures},
		hasLast, r.Status, r.Time, m.Retries)
	rs := model.RegionState{
		Status:              rns.Status,
		ConsecutiveFailures: rns.ConsecutiveFailures,
		LastResultAt:        r.Time,
		LatencyMs:           r.LatencyMs,
		Message:             msg,
	}
	next := make(map[string]model.RegionState, len(regs)+1)
	maps.Copy(next, regs)
	next[region] = rs
	s.regions[id] = next

	agg := s.aggregateLocked(m, next, now)
	tmsg := agg.msg
	if agg.status == model.StatusUp || (!agg.silent && rs.Status != model.StatusUp) {
		tmsg = msg
	}
	s.commitLocked(m, next, agg, r.Time, r.TLSExpiresAt, tmsg, &hb)
	if !agg.decided {
		// No verdict yet (startup grace): history only.
		hb.Status = r.Status
		if havePrev {
			hb.Status = prev.Status
		}
		s.buf.Add(hb)
	}
	return rs.Status, true
}

// sweepLoop re-evaluates every multi-region monitor on a timer until ctx is
// done, so a probe going silent flips its monitors without any new result.
func (s *Scheduler) sweepLoop(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(s.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweep()
		}
	}
}

// sweep re-evaluates staleness of every multi-region monitor. Time alone
// never moves a monitor into DOWN.
func (s *Scheduler) sweep() {
	s.mu.Lock()
	ids := make([]int64, 0, len(s.monitors))
	for id, m := range s.monitors {
		if isMultiRegion(m) && !m.Paused {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.reevaluate(id, false)
	}
}

// reevaluate recomputes monitor id's state from its region states at the
// current time. A new DOWN is only possible when allowNewDown is set (a
// config change such as a lower quorum); the periodic sweep never opens one.
func (s *Scheduler) reevaluate(id int64, allowNewDown bool) {
	now := s.now()
	maintName, inMaint := s.maintenanceActive(id, now) // outside s.mu, like the check loops

	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.monitors[id]
	if !ok || s.stopped || m.Paused || !isMultiRegion(m) {
		return
	}
	prev, havePrev := s.states[id]
	if inMaint {
		s.maintSeen[id] = true
		if !havePrev || prev.Status != model.StatusMaintenance {
			s.enterMaintenanceLocked(m, maintName, now)
		}
		return
	}
	s.leaveMaintenanceLocked(id, now)
	agg := s.aggregateLocked(m, s.regions[id], now)
	if !agg.decided || (havePrev && prev.Status == agg.status) {
		return
	}
	if agg.status == model.StatusDown && !allowNewDown {
		return
	}
	s.commitLocked(m, s.regions[id], agg, now, nil, agg.msg, nil)
}

// syncRegionsLocked reconciles the region bookkeeping of m after an Upsert:
// region states of regions m no longer lists are dropped, and a monitor that
// is no longer multi-region loses them all. Callers hold s.mu.
func (s *Scheduler) syncRegionsLocked(m model.Monitor) {
	id := m.ID
	setStateRegions := func(regs map[string]model.RegionState) {
		if st, ok := s.states[id]; ok && len(st.Regions) != len(regs) {
			st.Regions = nilIfEmpty(regs)
			s.states[id] = st
			s.dirty[id] = struct{}{}
		}
	}
	if !isMultiRegion(m) {
		delete(s.graceFrom, id)
		delete(s.maintSeen, id)
		delete(s.regions, id)
		setStateRegions(nil)
		return
	}
	s.graceFrom[id] = s.now()
	regs := s.regions[id]
	keep := make(map[string]model.RegionState, len(regs))
	for name, rs := range regs {
		if m.InRegion(name) {
			keep[name] = rs
		}
	}
	if len(keep) != len(regs) {
		s.regions[id] = keep
	}
	setStateRegions(keep)
}

// runLocalRegionLoop runs the built-in checker for a multi-region monitor
// that lists "local": like runCheckLoop, but the result feeds region "local"
// of the aggregation instead of the monitor's state directly.
func (s *Scheduler) runLocalRegionLoop(ctx context.Context, m model.Monitor, jittered bool, done chan struct{}) {
	defer close(done)
	defer s.wg.Done()

	interval := time.Duration(m.IntervalS) * s.unit
	if jittered {
		jitterCap := min(interval, maxStartupJitter)
		if wait := s.jitter(jitterCap); wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}

	for {
		if name, ok := s.maintenanceActive(m.ID, s.now()); ok {
			s.ingest(s.opts.ProbeID, model.LocalRegion, model.RegionResult{MonitorID: m.ID}, name, true)
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
			continue
		}
		select {
		case s.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		start := s.now()
		res := s.ck.Check(ctx, m)
		<-s.sem
		if ctx.Err() != nil {
			return
		}
		status, _ := s.ingest(s.opts.ProbeID, model.LocalRegion, model.RegionResult{
			MonitorID:    m.ID,
			Time:         start,
			Status:       res.Status,
			LatencyMs:    int32(res.Latency.Milliseconds()),
			Message:      res.Message,
			TLSExpiresAt: res.TLSExpiresAt,
		}, "", false)

		next := interval
		if status == model.StatusPending {
			next = time.Duration(m.RetryIntervalS) * s.unit
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(next):
		}
	}
}

// assignedTo reports whether a probe of region must check m.
func assignedTo(m model.Monitor, region string) bool {
	return !m.Paused && m.Type.RemoteCapable() && m.InRegion(region)
}

// Assigned reports whether monitor id is one of Assignments(region): a cheap
// membership test that does not copy or hash anything.
func (s *Scheduler) Assigned(region string, id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.monitors[id]
	return ok && assignedTo(m, region)
}

// Assignments returns the unpaused, remote-capable monitors that are checked
// from region (sorted by id, deep-enough copies) and a version string that
// changes iff that set, or the check-relevant configuration of one of its
// monitors (id, type, config, intervals, retries, timeout, name), changes.
func (s *Scheduler) Assignments(region string) (string, []model.Monitor) {
	s.mu.Lock()
	ms := make([]model.Monitor, 0, 8)
	for _, m := range s.monitors {
		if !assignedTo(m, region) {
			continue
		}
		c := m
		c.Config = bytes.Clone(m.Config)
		c.Regions = slices.Clone(m.Regions)
		c.Tags = slices.Clone(m.Tags)
		c.ChannelIDs = slices.Clone(m.ChannelIDs)
		ms = append(ms, c)
	}
	s.mu.Unlock()
	slices.SortFunc(ms, func(a, b model.Monitor) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})

	h := sha256.New()
	for _, m := range ms {
		cfg := m.Config
		var buf bytes.Buffer
		if json.Compact(&buf, cfg) == nil {
			cfg = buf.Bytes()
		}
		fmt.Fprintf(h, "%d|%s|%q|%d|%d|%d|%d|%q\n", m.ID, m.Type, cfg, m.IntervalS, m.RetryIntervalS, m.Retries, m.TimeoutS, m.Name)
	}
	return hex.EncodeToString(h.Sum(nil)[:16]), ms
}

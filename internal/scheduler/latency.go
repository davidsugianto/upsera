package scheduler

import (
	"context"
	"math"
	"slices"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// DEGRADED detection.
//
// A remote-capable monitor (http, keyword, tcp, ping, dns) is DEGRADED in a
// region when it is UP there but the p95 latency of its UP checks over the
// last latencyWindow is far above that region's own baseline: the p95 over
// the last baselineSpan, loaded from the database every baselineRefresh in
// its own goroutine (never on the check path). A region enters DEGRADED when
// the baseline was built from at least minBaselineSamples heartbeats, the
// window holds at least minWindowSamples checks, the window p95 is more than
// degradedFactor × the baseline and at least degradedFloorMs above it. It
// leaves when the window p95 is at most recoverFactor × the baseline.
// The values are deliberately not configurable.
const (
	latencyWindow      = 15 * time.Minute
	baselineSpan       = 7 * 24 * time.Hour
	baselineRefresh    = time.Hour
	minBaselineSamples = 100
	minWindowSamples   = 3
	degradedFactor     = 3
	recoverFactor      = 2
	degradedFloorMs    = 100
	// maxWindowSamples bounds one region's window (the oldest are dropped).
	maxWindowSamples = 512
)

// Baselines loads the latency baselines: the p95 latency (and sample count)
// of every (monitor, probe) since the given time. *store.Store satisfies
// this.
type Baselines interface {
	LatencyBaselines(ctx context.Context, since time.Time) (map[model.BaselineKey]model.LatencyBaseline, error)
}

type latencySample struct {
	at time.Time
	ms int32
}

// latencyTrack is one region's latency window of one monitor, plus the
// numbers of the last verdict (for the transition).
type latencyTrack struct {
	samples   []latencySample
	p95, base int32
}

// p95 is the 95th percentile of v with linear interpolation (Postgres's
// percentile_cont), rounded to the millisecond. v must not be empty.
func p95(v []int32) int32 {
	s := slices.Clone(v)
	slices.Sort(s)
	pos := 0.95 * float64(len(s)-1)
	lo := int(math.Floor(pos))
	hi := min(lo+1, len(s)-1)
	f := float64(s[lo]) + (pos-float64(lo))*float64(s[hi]-s[lo])
	return int32(math.Round(f))
}

// degradedVerdict decides whether a region is DEGRADED given whether it was
// before (prev), its baseline (haveBase false: none) and the latencies of
// its window. It also returns the window p95 (0 for an empty window).
func degradedVerdict(prev bool, base model.LatencyBaseline, haveBase bool, window []int32) (bool, int32) {
	var p int32
	if len(window) > 0 {
		p = p95(window)
	}
	if !haveBase || base.Samples < minBaselineSamples {
		return false, p
	}
	if len(window) < minWindowSamples {
		return prev, p // too little to go on: keep the current verdict
	}
	b := int64(base.P95Ms)
	if prev {
		return int64(p) > recoverFactor*b, p
	}
	return int64(p) > degradedFactor*b && int64(p)-b >= degradedFloorMs, p
}

// latencyObsLocked records the UP check latency ms of monitor m in region
// (served by probeID) at time at and returns the observation to feed the
// state machine: StatusDegraded or StatusUp. prevDegraded is whether the
// region (or single-region monitor) is DEGRADED now. Callers hold s.mu.
func (s *Scheduler) latencyObsLocked(m model.Monitor, region string, probeID int64, at time.Time, ms int32, prevDegraded bool) model.Status {
	if s.opts.Baselines == nil || !m.Type.RemoteCapable() {
		return model.StatusUp
	}
	regs := s.lat[m.ID]
	if regs == nil {
		regs = make(map[string]*latencyTrack)
		s.lat[m.ID] = regs
	}
	tr := regs[region]
	if tr == nil {
		tr = &latencyTrack{}
		regs[region] = tr
	}
	tr.samples = append(tr.samples, latencySample{at, ms})
	i := 0
	for i < len(tr.samples) && at.Sub(tr.samples[i].at) >= s.latWindow {
		i++
	}
	i = max(i, len(tr.samples)-maxWindowSamples)
	tr.samples = slices.Delete(tr.samples, 0, i)

	all := s.baselines.Load()
	if all == nil {
		// Not loaded yet (just started): keep the persisted verdict rather
		// than announce a recovery that did not happen.
		if prevDegraded {
			return model.StatusDegraded
		}
		return model.StatusUp
	}
	base, ok := (*all)[model.BaselineKey{MonitorID: m.ID, ProbeID: probeID}]
	window := make([]int32, len(tr.samples))
	for j, smp := range tr.samples {
		window[j] = smp.ms
	}
	deg, p := degradedVerdict(prevDegraded, base, ok, window)
	tr.p95, tr.base = p, base.P95Ms
	if deg {
		return model.StatusDegraded
	}
	return model.StatusUp
}

// latencyInfoLocked fills tr's latency fields from region's last verdict
// when tr enters or leaves DEGRADED. Callers hold s.mu.
func (s *Scheduler) latencyInfoLocked(tr *model.Transition, region string) {
	if tr == nil || (tr.To != model.StatusDegraded && tr.From != model.StatusDegraded) {
		return
	}
	tr.Region = region
	if t := s.lat[tr.MonitorID][region]; t != nil {
		tr.LatencyP95Ms, tr.BaselineMs = t.p95, t.base
	}
}

// syncLatencyLocked drops the latency windows of regions m is no longer
// checked from. Callers hold s.mu.
func (s *Scheduler) syncLatencyLocked(m model.Monitor) {
	for region := range s.lat[m.ID] {
		if !m.InRegion(region) || !m.Type.RemoteCapable() {
			delete(s.lat[m.ID], region)
		}
	}
}

// baseline returns the loaded baseline of (monitorID, probeID).
func (s *Scheduler) baseline(monitorID, probeID int64) (model.LatencyBaseline, bool) {
	all := s.baselines.Load()
	if all == nil {
		return model.LatencyBaseline{}, false
	}
	b, ok := (*all)[model.BaselineKey{MonitorID: monitorID, ProbeID: probeID}]
	return b, ok
}

// refreshBaselines loads the baselines once. A failed load keeps the
// previous ones.
func (s *Scheduler) refreshBaselines(ctx context.Context) {
	if s.opts.Baselines == nil {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	m, err := s.opts.Baselines.LatencyBaselines(lctx, time.Now().Add(-baselineSpan))
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("scheduler: loading latency baselines failed; keeping the previous ones", "error", err)
		}
		return
	}
	if m == nil {
		m = map[model.BaselineKey]model.LatencyBaseline{}
	}
	s.baselines.Store(&m)
}

// baselineLoop loads the baselines right away and then every
// s.baselineEvery until ctx is done.
func (s *Scheduler) baselineLoop(ctx context.Context) {
	defer s.wg.Done()
	s.refreshBaselines(ctx)
	ticker := time.NewTicker(s.baselineEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshBaselines(ctx)
		}
	}
}

package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/testutil"
)

func TestDegradedStore(t *testing.T) {
	st, _ := testutil.Store(t)
	ctx := context.Background()
	_, team, err := st.Setup(ctx, "admin@example.com", "Admin", "hash", "Ops")
	if err != nil {
		t.Fatal(err)
	}
	local, err := st.LocalProbeID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	remote, _, err := st.CreateProbe(ctx, "sg", "sg-1")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name string) model.Monitor {
		m, err := st.CreateMonitor(ctx, model.Monitor{TeamID: team.ID, Name: name, Type: model.TypeTCP,
			Config: json.RawMessage(`{"host":"db","port":5432}`), IntervalS: 60, RetryIntervalS: 20, Retries: 1,
			TimeoutS: 10})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	insert := func(hbs []model.Heartbeat) {
		t.Helper()
		if err := st.InsertHeartbeats(ctx, hbs); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("latency baselines per monitor and probe", func(t *testing.T) {
		a, b := mk("a"), mk("b")
		now := time.Now()
		var hbs []model.Heartbeat
		// a/local: 1..100 ms over the last 6 days, up and degraded.
		for i := range 100 {
			s := model.StatusUp
			if i%2 == 0 {
				s = model.StatusDegraded
			}
			hbs = append(hbs, model.Heartbeat{MonitorID: a.ID, ProbeID: local, Time: now.Add(-time.Duration(i) * time.Hour),
				Status: s, LatencyMs: int32(i + 1)})
		}
		// Not counted: down, pending, maintenance, and anything older than since.
		for _, s := range []model.Status{model.StatusDown, model.StatusPending, model.StatusMaintenance} {
			hbs = append(hbs, model.Heartbeat{MonitorID: a.ID, ProbeID: local, Time: now, Status: s, LatencyMs: 99999})
		}
		hbs = append(hbs, model.Heartbeat{MonitorID: a.ID, ProbeID: local, Time: now.Add(-8 * 24 * time.Hour),
			Status: model.StatusUp, LatencyMs: 99999})
		// a/remote: 10 samples of 200 ms.
		for i := range 10 {
			hbs = append(hbs, model.Heartbeat{MonitorID: a.ID, ProbeID: remote.ID, Time: now.Add(-time.Duration(i) * time.Minute),
				Status: model.StatusUp, LatencyMs: 200})
		}
		// b/local: one sample.
		hbs = append(hbs, model.Heartbeat{MonitorID: b.ID, ProbeID: local, Time: now, Status: model.StatusUp, LatencyMs: 7})
		insert(hbs)

		got, err := st.LatencyBaselines(ctx, now.Add(-7*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		want := map[model.BaselineKey]model.LatencyBaseline{
			{MonitorID: a.ID, ProbeID: local}:     {P95Ms: 95, Samples: 100}, // percentile_cont over 1..100 = 95.05
			{MonitorID: a.ID, ProbeID: remote.ID}: {P95Ms: 200, Samples: 10},
			{MonitorID: b.ID, ProbeID: local}:     {P95Ms: 7, Samples: 1},
		}
		for k, w := range want {
			if got[k] != w {
				t.Errorf("baseline %+v = %+v, want %+v", k, got[k], w)
			}
		}
		if len(got) != len(want) {
			t.Errorf("got %d baselines, want %d: %+v", len(got), len(want), got)
		}
	})

	t.Run("degraded counts as up in rollup and uptime", func(t *testing.T) {
		m := mk("rollup")
		day := time.Now().UTC().AddDate(0, 0, -1).Truncate(24 * time.Hour).Add(6 * time.Hour)
		insert([]model.Heartbeat{
			{MonitorID: m.ID, ProbeID: local, Time: day, Status: model.StatusUp, LatencyMs: 100},
			{MonitorID: m.ID, ProbeID: local, Time: day.Add(time.Minute), Status: model.StatusDegraded, LatencyMs: 700},
			{MonitorID: m.ID, ProbeID: local, Time: day.Add(2 * time.Minute), Status: model.StatusPending, LatencyMs: 5000},
			{MonitorID: m.ID, ProbeID: local, Time: day.Add(3 * time.Minute), Status: model.StatusDown, LatencyMs: 5000},
			{MonitorID: m.ID, ProbeID: local, Time: day.Add(4 * time.Minute), Status: model.StatusMaintenance},
		})
		if _, err := st.Rollup(ctx, "UTC"); err != nil {
			t.Fatal(err)
		}
		days, err := st.ListUptimeDaily(ctx, m.ID, day, day)
		if err != nil {
			t.Fatal(err)
		}
		// p95 over {100, 700} = 100 + 0.95*600 = 670.
		if len(days) != 1 || days[0].Checks != 4 || days[0].Up != 3 || days[0].AvgLatencyMs != 400 || days[0].P95LatencyMs != 670 {
			t.Fatalf("rollup = %+v, want 4 checks, 3 up, avg 400, p95 670", days)
		}
		counts, err := st.CountUptimeSince(ctx, team.ID, m.ID, day.Add(-time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if want := (model.UptimeCount{Checks: 4, Up: 3}); counts[m.ID] != want {
			t.Fatalf("CountUptimeSince = %v, want %v", counts[m.ID], want)
		}
		todayStart := day.Add(-6 * time.Hour)
		page, err := st.StatusPageUptime(ctx, []int64{m.ID}, todayStart, todayStart)
		if err != nil {
			t.Fatal(err)
		}
		if d := page[m.ID]; len(d) != 1 || d[0].Checks != 4 || d[0].Up != 3 {
			t.Fatalf("StatusPageUptime (raw today) = %+v, want 4 checks, 3 up", d)
		}
	})
}

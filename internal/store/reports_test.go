package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/testutil"
)

func TestReportStore(t *testing.T) {
	st, _ := testutil.Store(t)
	ctx := context.Background()
	_, team, err := st.Setup(ctx, "admin@example.com", "Admin", "hash", "Ops")
	if err != nil {
		t.Fatal(err)
	}
	probe, err := st.LocalProbeID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	jkt, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name string) model.Monitor {
		m, err := st.CreateMonitor(ctx, model.Monitor{TeamID: team.ID, Name: name, Type: model.TypeTCP,
			Config: json.RawMessage(`{"host":"db","port":5432}`), IntervalS: 60, RetryIntervalS: 20, Retries: 1, TimeoutS: 10})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	insert := func(hbs ...model.Heartbeat) {
		t.Helper()
		if err := st.InsertHeartbeats(ctx, hbs); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("daily counts split rollup and raw days without overlap or gap", func(t *testing.T) {
		m := mk("daily")
		now := time.Now()
		rawStart := model.RawDaysStart(now, jkt) // 00:00 yesterday in Jakarta
		hb := func(at time.Time, s model.Status, ms int32) model.Heartbeat {
			return model.Heartbeat{MonitorID: m.ID, ProbeID: probe, Time: at, Status: s, LatencyMs: ms}
		}
		// Three days ago and the last second before rawStart: rollup days.
		insert(
			hb(rawStart.Add(-48*time.Hour+time.Hour), model.StatusUp, 10),
			hb(rawStart.Add(-48*time.Hour+2*time.Hour), model.StatusDown, 10),
			hb(rawStart.Add(-time.Second), model.StatusUp, 30),
			hb(rawStart.Add(-time.Second+time.Millisecond), model.StatusMaintenance, 0),
			// rawStart itself and later: raw days (yesterday, today).
			hb(rawStart, model.StatusDegraded, 70),
			hb(rawStart.Add(time.Hour), model.StatusUp, 50),
		)
		if _, err := st.Rollup(ctx, "Asia/Jakarta"); err != nil {
			t.Fatal(err)
		}
		// The rollup lags: these land after it and must still count.
		insert(hb(rawStart.Add(2*time.Hour), model.StatusDown, 0), hb(now.Add(-time.Second), model.StatusPending, 0))

		today := model.DateIn(now, jkt)
		from := today.AddDate(0, 0, -3)
		got, err := st.DailyCounts(ctx, []int64{m.ID}, from, today, now, jkt)
		if err != nil {
			t.Fatal(err)
		}
		byDay := map[string]model.DayCount{}
		for _, d := range got[m.ID] {
			if _, dup := byDay[d.Day.Format(time.DateOnly)]; dup {
				t.Fatalf("day %s returned twice", d.Day.Format(time.DateOnly))
			}
			byDay[d.Day.Format(time.DateOnly)] = d
		}
		day := func(back int) string { return today.AddDate(0, 0, -back).Format(time.DateOnly) }
		want := map[string]model.SLICount{
			day(3): {Good: 1, Valid: 2},
			day(2): {Good: 1, Valid: 1}, // maintenance not counted
			day(1): {Good: 2, Valid: 3}, // raw: degraded, up, late down
		}
		if pendingToday := now.Add(-time.Second); model.DateIn(pendingToday, jkt).Equal(today) {
			want[day(0)] = model.SLICount{Good: 1, Valid: 1}
		} else {
			want[day(1)] = model.SLICount{Good: 3, Valid: 4}
		}
		for k, w := range want {
			if byDay[k].Count != w {
				t.Errorf("day %s = %+v, want %+v (all %+v)", k, byDay[k].Count, w, got[m.ID])
			}
		}
		if byDay[day(2)].P95Ms != 30 || byDay[day(1)].P95Ms != 69 {
			t.Errorf("p95 = %d (rollup) / %d (raw), want 30 / 69", byDay[day(2)].P95Ms, byDay[day(1)].P95Ms)
		}
		// A range that ends before the raw days uses the rollup only.
		old, err := st.DailyCounts(ctx, []int64{m.ID}, from, from, now, jkt)
		if err != nil || len(old[m.ID]) != 1 || old[m.ID][0].Count != want[day(3)] {
			t.Fatalf("rollup-only range = %+v, %v", old, err)
		}
	})

	t.Run("alert downtime is clipped to the month", func(t *testing.T) {
		a, b, c := mk("alerts-a"), mk("alerts-b"), mk("alerts-c")
		start := time.Date(2026, 4, 1, 0, 0, 0, 0, jkt)
		end := start.AddDate(0, 1, 0)
		now := end.Add(10 * 24 * time.Hour)
		alert := func(m model.Monitor, from time.Time, to *time.Time) model.Alert {
			return model.Alert{ID: uuid.Must(uuid.NewV7()).String(), TeamID: team.ID, MonitorID: m.ID, IncidentStart: from,
				OpenedAt: from, Step: 0, NotifiedChannelIDs: []int64{}, ResolvedAt: to, UpdatedAt: from}
		}
		alerts := []model.Alert{
			// Started in March, resolved 1h into April: 1h counts, the alert does not.
			alert(a, start.Add(-5*time.Hour), new(start.Add(time.Hour))),
			// Fully inside: 30 min, counted.
			alert(a, start.Add(48*time.Hour), new(start.Add(48*time.Hour+30*time.Minute))),
			// Starts 2h before the end, still open: clipped at the month end.
			alert(b, end.Add(-2*time.Hour), nil),
			// Entirely in May.
			alert(c, end.Add(time.Hour), new(end.Add(2*time.Hour))),
		}
		if err := st.UpsertAlerts(ctx, alerts); err != nil {
			t.Fatal(err)
		}
		got, err := st.SLAAlerts(ctx, team.ID, start, end, now)
		if err != nil {
			t.Fatal(err)
		}
		if g := got[a.ID]; g.DowntimeS != 3600+1800 || g.Alerts != 1 {
			t.Errorf("a = %+v, want 5400s, 1 alert", g)
		}
		if g := got[b.ID]; g.DowntimeS != 7200 || g.Alerts != 1 {
			t.Errorf("b = %+v, want 7200s, 1 alert", g)
		}
		if _, ok := got[c.ID]; ok {
			t.Errorf("c (May only) = %+v, want absent", got[c.ID])
		}
		// The open alert in the current month counts up to now.
		cur, err := st.SLAAlerts(ctx, team.ID, start, end, end.Add(-time.Hour))
		if err != nil || cur[b.ID].DowntimeS != 3600 {
			t.Fatalf("open alert as of an hour before the end = %+v, %v", cur[b.ID], err)
		}
	})

	t.Run("a month without data", func(t *testing.T) {
		m := mk("empty")
		from := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		got, err := st.DailyCounts(ctx, []int64{m.ID}, from, from.AddDate(0, 1, -1), time.Now(), jkt)
		if err != nil || len(got[m.ID]) != 0 {
			t.Fatalf("DailyCounts = %+v, %v", got, err)
		}
		al, err := st.SLAAlerts(ctx, team.ID, from, from.AddDate(0, 1, 0), time.Now())
		if err != nil || len(al) != 0 {
			t.Fatalf("SLAAlerts = %+v, %v", al, err)
		}
	})
}

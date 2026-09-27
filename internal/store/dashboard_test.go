package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/testutil"
)

func TestDashboardStore(t *testing.T) {
	st, _ := testutil.Store(t)
	ctx := context.Background()
	user, team, err := st.Setup(ctx, "admin@example.com", "Admin", "hash", "Ops")
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateTeam(ctx, "Other", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := st.LocalProbeID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mkMonitor := func(teamID int64, name string) model.Monitor {
		m, err := st.CreateMonitor(ctx, model.Monitor{TeamID: teamID, Name: name, Type: model.TypeTCP,
			Config: json.RawMessage(`{"host":"db","port":5432}`), IntervalS: 60, RetryIntervalS: 20, Retries: 1,
			TimeoutS: 10})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	insert := func(id int64, statuses ...model.Status) {
		hbs := make([]model.Heartbeat, len(statuses))
		for i, s := range statuses {
			hbs[i] = model.Heartbeat{MonitorID: id, ProbeID: probe, Time: base.Add(time.Duration(i) * time.Minute),
				Status: s, LatencyMs: int32(10 + i), Message: s.String()}
		}
		if err := st.InsertHeartbeats(ctx, hbs); err != nil {
			t.Fatal(err)
		}
	}
	up, down, pend, maint := model.StatusUp, model.StatusDown, model.StatusPending, model.StatusMaintenance

	t.Run("recent heartbeats are capped per monitor and team scoped", func(t *testing.T) {
		a, b, foreign := mkMonitor(team.ID, "a"), mkMonitor(team.ID, "b"), mkMonitor(other.ID, "foreign")
		insert(a.ID, up, up, down, up, up)
		insert(b.ID, down)
		insert(foreign.ID, up)
		got, err := st.ListRecentHeartbeats(ctx, team.ID, 3)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got[foreign.ID]; ok {
			t.Fatal("another team's monitor was included")
		}
		if n := len(got[a.ID]); n != 3 {
			t.Fatalf("monitor a: %d heartbeats, want 3", n)
		}
		if h := got[a.ID][0]; !h.Time.Equal(base.Add(4 * time.Minute)) {
			t.Fatalf("monitor a: first heartbeat at %v, want the newest", h.Time)
		}
		if n := len(got[b.ID]); n != 1 {
			t.Fatalf("monitor b: %d heartbeats, want 1", n)
		}
	})

	t.Run("uptime counts exclude maintenance and count pending as up", func(t *testing.T) {
		m := mkMonitor(team.ID, "uptime")
		insert(m.ID, up, pend, down, maint)
		got, err := st.CountUptimeSince(ctx, team.ID, m.ID, base.Add(-time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if want := (model.UptimeCount{Checks: 3, Up: 2}); len(got) != 1 || got[m.ID] != want {
			t.Fatalf("CountUptimeSince = %v, want only {%d: %v}", got, m.ID, want)
		}
		if got, err := st.CountUptimeSince(ctx, other.ID, m.ID, base.Add(-time.Minute)); err != nil || len(got) != 0 {
			t.Fatalf("other team's CountUptimeSince = %v, %v; want empty", got, err)
		}

		if _, err := st.Rollup(ctx, "UTC"); err != nil {
			t.Fatal(err)
		}
		daily, err := st.SumUptimeDaily(ctx, team.ID, m.ID, base.AddDate(0, 0, -1))
		if err != nil {
			t.Fatal(err)
		}
		if want := (model.UptimeCount{Checks: 3, Up: 2}); daily[m.ID] != want {
			t.Fatalf("SumUptimeDaily = %v, want %v", daily[m.ID], want)
		}
	})

	t.Run("status changes are newest first with nil previous for the oldest", func(t *testing.T) {
		m := mkMonitor(team.ID, "changes")
		insert(m.ID, up, up, down, down, up)
		got, err := st.ListStatusChanges(ctx, m.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d changes, want 3: %+v", len(got), got)
		}
		want := []struct {
			status model.Status
			prev   *model.Status
		}{{up, &down}, {down, &up}, {up, nil}}
		for i, w := range want {
			g := got[i]
			if g.Status != w.status || (g.Previous == nil) != (w.prev == nil) || (g.Previous != nil && *g.Previous != *w.prev) {
				t.Fatalf("change %d = %v (prev %v), want %v (prev %v)", i, g.Status, g.Previous, w.status, w.prev)
			}
		}
		if !got[0].Time.After(got[1].Time) {
			t.Fatal("changes not newest first")
		}
	})
}

package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/store"
	"github.com/davidsugianto/upsera/internal/testutil"
)

func TestSLOStore(t *testing.T) {
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
	mk := func(teamID int64, name string, paused bool) model.Monitor {
		m, err := st.CreateMonitor(ctx, model.Monitor{TeamID: teamID, Name: name, Type: model.TypeTCP,
			Config: json.RawMessage(`{"host":"db","port":5432}`), IntervalS: 60, RetryIntervalS: 20, Retries: 1,
			TimeoutS: 10, Paused: paused})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	slo := func(m model.Monitor, target float64) model.SLO {
		return model.SLO{TeamID: m.TeamID, MonitorID: m.ID, Target: target, WindowDays: 30, FastBurn: 14.4, SlowBurn: 6}
	}

	t.Run("crud", func(t *testing.T) {
		m := mk(team.ID, "crud", false)
		if _, err := st.GetSLO(ctx, team.ID, m.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetSLO before create: %v", err)
		}
		created, err := st.UpsertSLO(ctx, slo(m, 99.95))
		if err != nil {
			t.Fatal(err)
		}
		if created.ID == 0 || created.Target != 99.95 || created.WindowDays != 30 || created.FastBurn != 14.4 ||
			created.SlowBurn != 6 || created.MonitorName != "crud" || created.Firing() != model.SLOFiringNone {
			t.Fatalf("created = %+v", created)
		}
		got, err := st.GetSLO(ctx, team.ID, m.ID)
		if err != nil || got.ID != created.ID || !got.UpdatedAt.Equal(created.UpdatedAt) {
			t.Fatalf("GetSLO = %+v, %v", got, err)
		}
		if _, err := st.GetSLO(ctx, other.ID, m.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("other team's GetSLO: %v", err)
		}

		// Firing state persists, guarded by the version it was computed for.
		at := time.Now().Truncate(time.Microsecond)
		if err := st.SetSLOFiring(ctx, m.ID, got.UpdatedAt, &at, nil); err != nil {
			t.Fatal(err)
		}
		if err := st.SetSLOFiring(ctx, m.ID, got.UpdatedAt.Add(-time.Second), nil, &at); err != nil {
			t.Fatal(err)
		}
		got, _ = st.GetSLO(ctx, team.ID, m.ID)
		if got.FastFiringSince == nil || !got.FastFiringSince.Equal(at) || got.SlowFiringSince != nil {
			t.Fatalf("after SetSLOFiring: %+v", got)
		}

		// Re-PUT with the same values keeps the firing state and version...
		same, err := st.UpsertSLO(ctx, slo(m, 99.95))
		if err != nil || same.FastFiringSince == nil || !same.UpdatedAt.Equal(got.UpdatedAt) {
			t.Fatalf("unchanged upsert = %+v, %v", same, err)
		}
		// ...changing it clears the state.
		changed := slo(m, 99.9)
		changed.WindowDays = 7
		upd, err := st.UpsertSLO(ctx, changed)
		if err != nil || upd.ID != created.ID || upd.Target != 99.9 || upd.WindowDays != 7 || upd.FastFiringSince != nil ||
			!upd.UpdatedAt.After(got.UpdatedAt) {
			t.Fatalf("changed upsert = %+v, %v", upd, err)
		}

		if err := st.DeleteSLO(ctx, other.ID, m.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("other team's DeleteSLO: %v", err)
		}
		if err := st.DeleteSLO(ctx, team.ID, m.ID); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteSLO(ctx, team.ID, m.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("second DeleteSLO: %v", err)
		}
	})

	t.Run("constraints", func(t *testing.T) {
		m := mk(team.ID, "constraints", false)
		bad := []func(*model.SLO){
			func(s *model.SLO) { s.Target = 0 },
			func(s *model.SLO) { s.Target = 100 },
			func(s *model.SLO) { s.WindowDays = 6 },
			func(s *model.SLO) { s.WindowDays = 91 },
			func(s *model.SLO) { s.SlowBurn, s.FastBurn = 10, 5 },
			func(s *model.SLO) { s.SlowBurn = 0.5 },
			func(s *model.SLO) { s.FastBurn = 1001 },
		}
		for i, f := range bad {
			s := slo(m, 99.9)
			f(&s)
			if _, err := st.UpsertSLO(ctx, s); err == nil {
				t.Errorf("case %d (%+v) accepted", i, s)
			}
		}
		s := slo(m, 99.999)
		s.WindowDays, s.FastBurn, s.SlowBurn = 90, 1000, 1
		if _, err := st.UpsertSLO(ctx, s); err != nil {
			t.Fatalf("edge values rejected: %v", err)
		}
	})

	t.Run("list, cascade and burn counts", func(t *testing.T) {
		b, a, p := mk(team.ID, "b-list", false), mk(team.ID, "a-list", false), mk(team.ID, "paused-list", true)
		foreign := mk(other.ID, "foreign", false)
		for _, m := range []model.Monitor{a, b, p, foreign} {
			if _, err := st.UpsertSLO(ctx, slo(m, 99)); err != nil {
				t.Fatal(err)
			}
		}
		list, err := st.ListSLOs(ctx, team.ID)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, s := range list {
			if s.TeamID != team.ID {
				t.Fatalf("other team's SLO listed: %+v", s)
			}
			names = append(names, s.MonitorName)
		}
		if len(names) < 3 || names[0] != "a-list" {
			t.Fatalf("names = %v, want sorted by monitor name", names)
		}
		all, err := st.ListAllSLOs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var sawPaused, sawForeign bool
		for _, s := range all {
			sawPaused = sawPaused || (s.MonitorID == p.ID && s.MonitorPaused)
			sawForeign = sawForeign || s.MonitorID == foreign.ID
		}
		if !sawPaused || !sawForeign {
			t.Fatalf("ListAllSLOs = %+v", all)
		}

		now := time.Now()
		var hbs []model.Heartbeat
		add := func(m model.Monitor, ago time.Duration, s model.Status) {
			hbs = append(hbs, model.Heartbeat{MonitorID: m.ID, ProbeID: probe, Time: now.Add(-ago), Status: s})
		}
		add(a, time.Minute, model.StatusDown)
		add(a, 2*time.Minute, model.StatusUp)
		add(a, 3*time.Minute, model.StatusMaintenance)
		add(a, 20*time.Minute, model.StatusDegraded)
		add(a, 2*time.Hour, model.StatusPending)
		add(a, 7*time.Hour, model.StatusDown) // older than every window
		add(b, 10*time.Second, model.StatusUp)
		add(b, -time.Minute, model.StatusDown) // in the future: not counted
		if err := st.InsertHeartbeats(ctx, hbs); err != nil {
			t.Fatal(err)
		}
		counts, err := st.BurnCounts(ctx, []int64{a.ID, b.ID, p.ID}, now,
			[]time.Time{now.Add(-5 * time.Minute), now.Add(-time.Hour), now.Add(-6 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		wantA := []model.SLICount{{Good: 1, Valid: 2}, {Good: 2, Valid: 3}, {Good: 3, Valid: 4}}
		wantB := []model.SLICount{{Good: 1, Valid: 1}, {Good: 1, Valid: 1}, {Good: 1, Valid: 1}}
		if !equalCounts(counts[a.ID], wantA) || !equalCounts(counts[b.ID], wantB) {
			t.Fatalf("counts = %+v, want a %+v b %+v", counts, wantA, wantB)
		}
		if _, ok := counts[p.ID]; ok {
			t.Fatalf("monitor without heartbeats has counts: %+v", counts[p.ID])
		}

		if err := st.DeleteMonitor(ctx, team.ID, a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetSLO(ctx, team.ID, a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("SLO survived its monitor: %v", err)
		}
	})

	t.Run("notification log accepts the phase 7 events", func(t *testing.T) {
		var entries []model.NotificationLogEntry
		for _, ev := range []model.AlertEvent{model.EventDegraded, model.EventLatencyRecovered, model.EventSLOBurn, model.EventSLOBurnRecovered} {
			entries = append(entries, model.NotificationLogEntry{TeamID: team.ID, Event: ev, DedupeKey: string(ev), Attempt: 1, OK: true, At: time.Now()})
		}
		if err := st.InsertNotificationLog(ctx, entries); err != nil {
			t.Fatalf("InsertNotificationLog: %v", err)
		}
	})
}

func equalCounts(a, b []model.SLICount) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

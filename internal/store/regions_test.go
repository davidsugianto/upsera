package store_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/store"
	"github.com/davidsugianto/upsera/internal/testutil"
)

func regionMonitor(teamID int64, name string, regions []string, quorum int) model.Monitor {
	return model.Monitor{TeamID: teamID, Name: name, Type: model.TypeHTTP, Config: json.RawMessage(`{}`),
		IntervalS: 60, RetryIntervalS: 20, Retries: 1, TimeoutS: 10, Regions: regions, Quorum: quorum}
}

func TestMonitorRegions(t *testing.T) {
	st, _ := testutil.Store(t)
	ctx := context.Background()
	_, team, err := st.Setup(ctx, "a@example.com", "A", "hash", "Ops")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"sg-1", "fra-1"} {
		if _, _, err := st.CreateProbe(ctx, r, r); err != nil {
			t.Fatal(err)
		}
	}

	def, err := st.CreateMonitor(ctx, regionMonitor(team.ID, "default", nil, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(def.Regions, []string{"local"}) || def.Quorum != 1 {
		t.Fatalf("empty regions must default to {local}/1: %+v", def)
	}

	multi, err := st.CreateMonitor(ctx, regionMonitor(team.ID, "multi", []string{"sg-1", "local", "fra-1"}, 2))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(multi.Regions, []string{"sg-1", "local", "fra-1"}) || multi.Quorum != 2 {
		t.Fatalf("create: %+v", multi)
	}
	got, err := st.GetMonitor(ctx, team.ID, multi.ID)
	if err != nil || !slices.Equal(got.Regions, multi.Regions) || got.Quorum != 2 {
		t.Fatalf("get: %+v %v", got, err)
	}
	list, err := st.ListMonitors(ctx, team.ID)
	if err != nil || len(list) != 2 || !slices.Equal(list[1].Regions, multi.Regions) || list[1].Quorum != 2 {
		t.Fatalf("list: %+v %v", list, err)
	}
	all, err := st.ListAllMonitors(ctx)
	if err != nil || len(all) != 2 || !slices.Equal(all[1].Regions, multi.Regions) || all[1].Quorum != 2 {
		t.Fatalf("list all: %+v %v", all, err)
	}

	upd := multi
	upd.Regions, upd.Quorum = []string{"fra-1"}, 1
	out, err := st.UpdateMonitor(ctx, upd)
	if err != nil || !slices.Equal(out.Regions, []string{"fra-1"}) || out.Quorum != 1 {
		t.Fatalf("update: %+v %v", out, err)
	}
	upd.Regions, upd.Quorum = nil, 0
	out, err = st.UpdateMonitor(ctx, upd)
	if err != nil || !slices.Equal(out.Regions, []string{"local"}) || out.Quorum != 1 {
		t.Fatalf("update to empty: %+v %v", out, err)
	}

	t.Run("table checks reject bad quorum", func(t *testing.T) {
		if _, err := st.CreateMonitor(ctx, regionMonitor(team.ID, "bad", []string{"local"}, 2)); err == nil {
			t.Fatal("quorum > regions must be rejected by the CHECK")
		}
	})

	t.Run("monitor state regions round trip", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Millisecond)
		regs := map[string]model.RegionState{
			"sg-1":  {Status: model.StatusDown, ConsecutiveFailures: 3, LastResultAt: now, LatencyMs: 120, Message: "timeout"},
			"local": {Status: model.StatusUp, LastResultAt: now.Add(-time.Second), LatencyMs: 8},
		}
		err := st.UpsertMonitorStates(ctx, []model.MonitorState{
			{MonitorID: multi.ID, Status: model.StatusDown, Since: now, LastCheckAt: now, ConsecutiveFailures: 3, Regions: regs},
			{MonitorID: def.ID, Status: model.StatusUp, Since: now, LastCheckAt: now},
		})
		if err != nil {
			t.Fatal(err)
		}
		states, err := st.ListMonitorStates(ctx)
		if err != nil || len(states) != 2 {
			t.Fatalf("states: %+v %v", states, err)
		}
		for _, s := range states {
			switch s.MonitorID {
			case multi.ID:
				if len(s.Regions) != 2 || s.Regions["sg-1"].Message != "timeout" || s.Regions["sg-1"].ConsecutiveFailures != 3 ||
					!s.Regions["sg-1"].LastResultAt.Equal(now) || s.Regions["local"].Status != model.StatusUp {
					t.Fatalf("regions: %+v", s.Regions)
				}
			case def.ID:
				if len(s.Regions) != 0 {
					t.Fatalf("local-only monitor should have no regions: %+v", s.Regions)
				}
			}
		}
		// Update replaces the regions map, clearing it with nil.
		if err := st.UpsertMonitorStates(ctx, []model.MonitorState{
			{MonitorID: multi.ID, Status: model.StatusUp, Since: now, LastCheckAt: now}}); err != nil {
			t.Fatal(err)
		}
		states, _ = st.ListMonitorStates(ctx)
		for _, s := range states {
			if s.MonitorID == multi.ID && len(s.Regions) != 0 {
				t.Fatalf("regions should be cleared: %+v", s.Regions)
			}
		}
	})
}

func TestMigrationUpgrade4To5(t *testing.T) {
	url := testutil.PostgresURL(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(ctx, url, 5)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.MigrateTo(ctx, log, 4); err != nil {
		t.Fatal(err)
	}

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var teamID, monID int64
	if err := conn.QueryRow(ctx, "INSERT INTO upsera.teams (name) VALUES ('Ops') RETURNING id").Scan(&teamID); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `
		INSERT INTO upsera.monitors (team_id, name, type, config, interval_s, retry_interval_s, retries, timeout_s)
		VALUES ($1, 'old', 'http', '{}', 60, 20, 1, 10) RETURNING id`, teamID).Scan(&monID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO upsera.monitor_state (monitor_id, status, since, last_check_at) VALUES ($1, 1, now(), now())`, monID); err != nil {
		t.Fatal(err)
	}

	if err := st.Migrate(ctx, log); err != nil {
		t.Fatal(err)
	}
	m, err := st.GetMonitor(ctx, teamID, monID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m.Regions, []string{"local"}) || m.Quorum != 1 {
		t.Fatalf("existing monitor after upgrade: regions=%v quorum=%d", m.Regions, m.Quorum)
	}
	states, err := st.ListMonitorStates(ctx)
	if err != nil || len(states) != 1 || len(states[0].Regions) != 0 {
		t.Fatalf("existing state after upgrade: %+v %v", states, err)
	}
	ps, err := st.ListProbes(ctx)
	if err != nil || len(ps) != 1 || ps[0].Region != "local" {
		t.Fatalf("probes after upgrade: %+v %v", ps, err)
	}
}

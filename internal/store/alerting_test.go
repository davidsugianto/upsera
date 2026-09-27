package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/store"
	"github.com/davidsugianto/upsera/internal/testutil"
)

func TestAlertingStore(t *testing.T) {
	st, url := testutil.Store(t)
	ctx := context.Background()
	_, team, err := st.Setup(ctx, "admin@example.com", "Admin", "hash", "Ops")
	if err != nil {
		t.Fatal(err)
	}
	const token = "123456:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	mkChannel := func(name string) model.Channel {
		c, err := st.CreateChannel(ctx, model.Channel{TeamID: team.ID, Type: model.ChannelTelegram, Name: name,
			Config: json.RawMessage(`{"bot_token":"` + token + `","chat_id":"42"}`)})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	mkMonitor := func(name string, channels []int64, policy *int64) model.Monitor {
		m, err := st.CreateMonitor(ctx, model.Monitor{TeamID: team.ID, Name: name, Type: model.TypeTCP,
			Config: json.RawMessage(`{"host":"db","port":5432}`), IntervalS: 60, RetryIntervalS: 20, Retries: 1,
			TimeoutS: 10, ChannelIDs: channels, EscalationPolicyID: policy})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}

	t.Run("channel config is encrypted at rest", func(t *testing.T) {
		c := mkChannel("tg")
		conn, err := pgx.Connect(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		var raw string
		if err := conn.QueryRow(ctx, "SELECT config FROM upsera.notification_channels WHERE id = $1", c.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(raw, "v1:") || strings.Contains(raw, token) {
			t.Fatalf("stored config is not ciphertext: %q", raw)
		}
		got, err := st.GetChannel(ctx, team.ID, c.ID)
		if err != nil || !strings.Contains(string(got.Config), token) || got.DecryptErr != "" {
			t.Fatalf("decrypted channel = %+v, %v", got, err)
		}
	})

	t.Run("in-use channel and policy cannot be deleted", func(t *testing.T) {
		c := mkChannel("used")
		p, err := st.CreatePolicy(ctx, model.EscalationPolicy{TeamID: team.ID, Name: "p",
			Steps: []model.EscalationStep{{ChannelIDs: []int64{c.ID}, DelayS: 30}}})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Steps) != 1 || p.Steps[0].DelayS != 30 || !slices.Equal(p.Steps[0].ChannelIDs, []int64{c.ID}) {
			t.Fatalf("policy steps = %+v", p.Steps)
		}
		if err := st.DeleteChannel(ctx, team.ID, c.ID); !errors.Is(err, store.ErrInUse) {
			t.Fatalf("delete channel used by policy: want ErrInUse, got %v", err)
		}
		m := mkMonitor("uses-policy", nil, &p.ID)
		if err := st.DeletePolicy(ctx, team.ID, p.ID); !errors.Is(err, store.ErrInUse) {
			t.Fatalf("delete policy used by monitor: want ErrInUse, got %v", err)
		}
		if err := st.DeleteMonitor(ctx, team.ID, m.ID); err != nil {
			t.Fatal(err)
		}
		if err := st.DeletePolicy(ctx, team.ID, p.ID); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteChannel(ctx, team.ID, c.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("policy steps round-trip in order", func(t *testing.T) {
		a, b, c := mkChannel("a"), mkChannel("b"), mkChannel("c")
		p, err := st.CreatePolicy(ctx, model.EscalationPolicy{TeamID: team.ID, Name: "two", Steps: []model.EscalationStep{
			{ChannelIDs: []int64{b.ID, a.ID}, DelayS: 10}, {ChannelIDs: []int64{c.ID}, DelayS: 600},
		}})
		if err != nil {
			t.Fatal(err)
		}
		p.Steps = []model.EscalationStep{{ChannelIDs: []int64{c.ID}, DelayS: 20}}
		p, err = st.UpdatePolicy(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := st.GetPolicy(ctx, team.ID, p.ID)
		if err != nil || len(got.Steps) != 1 || got.Steps[0].DelayS != 20 || !slices.Equal(got.Steps[0].ChannelIDs, []int64{c.ID}) {
			t.Fatalf("updated policy = %+v, %v", got, err)
		}
	})

	t.Run("monitor channel_ids round-trip", func(t *testing.T) {
		a, b := mkChannel("m-a"), mkChannel("m-b")
		m := mkMonitor("linked", []int64{b.ID, a.ID}, nil)
		if !slices.Equal(m.ChannelIDs, []int64{a.ID, b.ID}) {
			t.Fatalf("created channel_ids = %v", m.ChannelIDs)
		}
		m.ChannelIDs = []int64{b.ID}
		m, err := st.UpdateMonitor(ctx, m)
		if err != nil {
			t.Fatal(err)
		}
		got, err := st.GetMonitor(ctx, team.ID, m.ID)
		if err != nil || !slices.Equal(got.ChannelIDs, []int64{b.ID}) {
			t.Fatalf("updated channel_ids = %v, %v", got.ChannelIDs, err)
		}
	})

	t.Run("resolved alert is written before a newer open alert", func(t *testing.T) {
		m := mkMonitor("flappy", nil, nil)
		now := time.Now().UTC().Truncate(time.Microsecond)
		a1 := model.Alert{ID: uuid.Must(uuid.NewV7()).String(), TeamID: team.ID, MonitorID: m.ID,
			IncidentStart: now, OpenedAt: now, Step: -1}
		if err := st.UpsertAlerts(ctx, []model.Alert{a1}); err != nil {
			t.Fatal(err)
		}
		a1.ResolvedAt, a1.Resolution = new(now.Add(time.Minute)), model.ResolutionRecovered
		a2 := model.Alert{ID: uuid.Must(uuid.NewV7()).String(), TeamID: team.ID, MonitorID: m.ID,
			IncidentStart: now.Add(2 * time.Minute), OpenedAt: now.Add(2 * time.Minute), Step: 0,
			NotifiedChannelIDs: []int64{1}}
		if err := st.UpsertAlerts(ctx, []model.Alert{a2, a1}); err != nil {
			t.Fatalf("upsert [open A2, resolved A1]: %v", err)
		}
		open, err := st.ListAlerts(ctx, team.ID, "open", m.ID, time.Time{}, 10)
		if err != nil || len(open) != 1 || open[0].ID != a2.ID {
			t.Fatalf("open alerts = %+v, %v", open, err)
		}
		got, err := st.GetAlert(ctx, team.ID, a1.ID)
		if err != nil || got.Resolution != model.ResolutionRecovered || got.ResolvedAt == nil {
			t.Fatalf("resolved alert = %+v, %v", got, err)
		}
	})

	t.Run("log row of a deleted channel inserts with NULL channel", func(t *testing.T) {
		c := mkChannel("gone")
		if err := st.DeleteChannel(ctx, team.ID, c.ID); err != nil {
			t.Fatal(err)
		}
		err := st.InsertNotificationLog(ctx, []model.NotificationLogEntry{{TeamID: team.ID, ChannelID: &c.ID,
			Event: model.EventTest, DedupeKey: "test:x", Attempt: 1, OK: true, At: time.Now()}})
		if err != nil {
			t.Fatal(err)
		}
		log, err := st.ListNotificationLog(ctx, team.ID, 0, 1)
		if err != nil || len(log) != 1 || log[0].ChannelID != nil || log[0].DedupeKey != "test:x" {
			t.Fatalf("log = %+v, %v", log, err)
		}
	})

	t.Run("maintenance window links round-trip", func(t *testing.T) {
		m := mkMonitor("maint", nil, nil)
		start := time.Now().UTC().Truncate(time.Second)
		w, err := st.CreateMaintenanceWindow(ctx, model.MaintenanceWindow{TeamID: team.ID, Name: "nightly",
			StartsAt: start, EndsAt: start.Add(time.Hour), Recurrence: model.RecurDaily, MonitorIDs: []int64{m.ID}})
		if err != nil || !slices.Equal(w.MonitorIDs, []int64{m.ID}) {
			t.Fatalf("window = %+v, %v", w, err)
		}
		all, err := st.ListAllMaintenanceWindows(ctx)
		if err != nil || len(all) != 1 || all[0].Recurrence != model.RecurDaily {
			t.Fatalf("windows = %+v, %v", all, err)
		}
	})
}

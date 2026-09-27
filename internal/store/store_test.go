package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/store"
	"github.com/davidsugianto/upsera/internal/testutil"
)

func TestStore(t *testing.T) {
	st, _ := testutil.Store(t)
	ctx := context.Background()

	t.Run("migrate is idempotent", func(t *testing.T) {
		if err := st.Migrate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
			t.Fatal(err)
		}
	})

	admin, team, err := st.Setup(ctx, "Admin@Example.com", "Admin", "hash", "Ops")
	if err != nil {
		t.Fatal(err)
	}
	if !admin.IsAdmin {
		t.Fatal("setup user must be instance admin")
	}

	t.Run("setup runs once", func(t *testing.T) {
		_, _, err := st.Setup(ctx, "other@example.com", "x", "hash", "")
		if !errors.Is(err, store.ErrSetupDone) {
			t.Fatalf("want ErrSetupDone, got %v", err)
		}
	})

	t.Run("email is unique case-insensitively", func(t *testing.T) {
		_, err := st.CreateUser(ctx, "admin@example.COM", "dup", "hash", false)
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("want ErrConflict, got %v", err)
		}
		u, err := st.GetUserByEmail(ctx, "ADMIN@example.com")
		if err != nil || u.ID != admin.ID {
			t.Fatalf("lookup by email: %v %+v", err, u)
		}
	})

	t.Run("team keeps an owner", func(t *testing.T) {
		if err := st.RemoveMember(ctx, team.ID, admin.ID); !errors.Is(err, store.ErrLastOwner) {
			t.Fatalf("remove last owner: want ErrLastOwner, got %v", err)
		}
		if err := st.SetMember(ctx, team.ID, admin.ID, model.RoleEditor); !errors.Is(err, store.ErrLastOwner) {
			t.Fatalf("demote last owner: want ErrLastOwner, got %v", err)
		}
		bob, err := st.CreateUser(ctx, "bob@example.com", "Bob", "hash", false)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetMember(ctx, team.ID, bob.ID, model.RoleOwner); err != nil {
			t.Fatal(err)
		}
		if err := st.SetMember(ctx, team.ID, admin.ID, model.RoleEditor); err != nil {
			t.Fatalf("demote with another owner present: %v", err)
		}
		if r, _ := st.GetRole(ctx, team.ID, admin.ID); r != model.RoleEditor {
			t.Fatalf("role = %q, want editor", r)
		}
		if err := st.SetMember(ctx, team.ID, admin.ID, model.RoleOwner); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("push token required exactly for push monitors", func(t *testing.T) {
		m := model.Monitor{TeamID: team.ID, Name: "cron", Type: model.TypePush, Config: json.RawMessage(`{}`),
			IntervalS: 60, RetryIntervalS: 20, Retries: 1, TimeoutS: 10}
		if _, err := st.CreateMonitor(ctx, m); err == nil {
			t.Fatal("push monitor without token must be rejected")
		}
		m.Type, m.PushToken = model.TypeTCP, "tok"
		if _, err := st.CreateMonitor(ctx, m); err == nil {
			t.Fatal("tcp monitor with push token must be rejected")
		}
	})

	t.Run("monitors are team scoped", func(t *testing.T) {
		other, err := st.CreateTeam(ctx, "Other", admin.ID)
		if err != nil {
			t.Fatal(err)
		}
		m, err := st.CreateMonitor(ctx, model.Monitor{TeamID: team.ID, Name: "web", Type: model.TypeHTTP,
			Config: json.RawMessage(`{"url":"https://example.com"}`), IntervalS: 60, RetryIntervalS: 20, Retries: 1, TimeoutS: 10})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetMonitor(ctx, other.ID, m.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("cross-team get: want ErrNotFound, got %v", err)
		}
		if err := st.DeleteMonitor(ctx, other.ID, m.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("cross-team delete: want ErrNotFound, got %v", err)
		}
	})

	t.Run("rollup and prune", func(t *testing.T) {
		probe, err := st.LocalProbeID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		m, err := st.CreateMonitor(ctx, model.Monitor{TeamID: team.ID, Name: "api", Type: model.TypeTCP,
			Config: json.RawMessage(`{"host":"db","port":5432}`), IntervalS: 60, RetryIntervalS: 20, Retries: 1, TimeoutS: 10})
		if err != nil {
			t.Fatal(err)
		}
		jkt, _ := time.LoadLocation("Asia/Jakarta")
		// Two days ago at 23:30 Jakarta, and one hour later (00:30 the next
		// Jakarta day) — both fall on the same UTC day, so bucketing must
		// use the configured time zone.
		d := time.Now().In(jkt).AddDate(0, 0, -2)
		day1 := time.Date(d.Year(), d.Month(), d.Day(), 23, 30, 0, 0, jkt)
		day2 := day1.Add(time.Hour)
		old := time.Now().AddDate(0, 0, -30)
		hbs := []model.Heartbeat{
			{MonitorID: m.ID, ProbeID: probe, Time: day1, Status: model.StatusUp, LatencyMs: 100},
			{MonitorID: m.ID, ProbeID: probe, Time: day1.Add(time.Minute), Status: model.StatusDown, LatencyMs: 5000},
			{MonitorID: m.ID, ProbeID: probe, Time: day1.Add(2 * time.Minute), Status: model.StatusMaintenance, LatencyMs: 0},
			{MonitorID: m.ID, ProbeID: probe, Time: day2, Status: model.StatusUp, LatencyMs: 300},
			{MonitorID: m.ID, ProbeID: probe, Time: old, Status: model.StatusUp, LatencyMs: 1},
		}
		if err := st.InsertHeartbeats(ctx, hbs); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Rollup(ctx, "Asia/Jakarta"); err != nil {
			t.Fatal(err)
		}
		days, err := st.ListUptimeDaily(ctx, m.ID, day1, day2)
		if err != nil {
			t.Fatal(err)
		}
		if len(days) != 2 {
			t.Fatalf("want 2 Jakarta days, got %+v", days)
		}
		if d := days[0]; d.Checks != 2 || d.Up != 1 || d.AvgLatencyMs != 100 {
			t.Fatalf("day1 = %+v, want 2 checks (maintenance excluded), 1 up, avg 100", d)
		}
		if d := days[1]; d.Checks != 1 || d.Up != 1 || d.P95LatencyMs != 300 {
			t.Fatalf("day2 = %+v", d)
		}

		// Batch size 1 exercises the batching loop; only the 30-day-old row
		// is past the 14-day cutoff.
		n, err := st.PruneHeartbeats(ctx, time.Now().AddDate(0, 0, -14), 1, "Asia/Jakarta")
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("pruned %d rows, want 1", n)
		}
		left, err := st.ListHeartbeats(ctx, m.ID, time.Time{}, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(left) != len(hbs)-1 {
			t.Fatalf("left %d heartbeats, want %d", len(left), len(hbs)-1)
		}
		// Rollup survives pruning.
		days, _ = st.ListUptimeDaily(ctx, m.ID, day1, day2)
		if len(days) != 2 {
			t.Fatalf("rollup rows lost after prune: %+v", days)
		}
	})

	t.Run("heartbeats for a deleted monitor are skipped, not fatal", func(t *testing.T) {
		probe, _ := st.LocalProbeID(ctx)
		mk := func(name string) model.Monitor {
			m, err := st.CreateMonitor(ctx, model.Monitor{TeamID: team.ID, Name: name, Type: model.TypeTCP,
				Config: json.RawMessage(`{"host":"db","port":5432}`), IntervalS: 60, RetryIntervalS: 20, Retries: 1, TimeoutS: 10})
			if err != nil {
				t.Fatal(err)
			}
			return m
		}
		gone, keep := mk("gone"), mk("kept")
		if err := st.DeleteMonitor(ctx, team.ID, gone.ID); err != nil {
			t.Fatal(err)
		}
		// A batch buffered before the delete mixes both monitors.
		now := time.Now()
		err := st.InsertHeartbeats(ctx, []model.Heartbeat{
			{MonitorID: gone.ID, ProbeID: probe, Time: now, Status: model.StatusUp, LatencyMs: 1},
			{MonitorID: keep.ID, ProbeID: probe, Time: now, Status: model.StatusUp, LatencyMs: 2, Message: "ok"},
		})
		if err != nil {
			t.Fatalf("batch with a deleted monitor must not fail (it would be requeued forever): %v", err)
		}
		hbs, err := st.ListHeartbeats(ctx, keep.ID, time.Time{}, 10)
		if err != nil || len(hbs) != 1 || hbs[0].Message != "ok" {
			t.Fatalf("surviving monitor's heartbeat lost: %v %+v", err, hbs)
		}
	})

	t.Run("prune rolls up heartbeats the watermark skipped", func(t *testing.T) {
		// Reproduces a long DB outage: the scheduler buffers heartbeats
		// while Postgres is down, then flushes them all after Rollup has
		// already advanced its watermark past their day. PruneHeartbeats
		// must roll those days up itself before deleting, or the data is
		// lost forever.
		probe, err := st.LocalProbeID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		m, err := st.CreateMonitor(ctx, model.Monitor{TeamID: team.ID, Name: "gap", Type: model.TypeTCP,
			Config: json.RawMessage(`{"host":"db","port":5432}`), IntervalS: 60, RetryIntervalS: 20, Retries: 1, TimeoutS: 10})
		if err != nil {
			t.Fatal(err)
		}
		const tz = "Asia/Jakarta"
		jkt, _ := time.LoadLocation(tz)
		now := time.Now().In(jkt)
		d2, d10 := now.AddDate(0, 0, -2), now.AddDate(0, 0, -10)
		recent := time.Date(d2.Year(), d2.Month(), d2.Day(), 10, 0, 0, 0, jkt)
		old := time.Date(d10.Year(), d10.Month(), d10.Day(), 10, 0, 0, 0, jkt)

		// Advance the watermark to D-2, as if the server had been running
		// normally until then.
		if err := st.InsertHeartbeats(ctx, []model.Heartbeat{
			{MonitorID: m.ID, ProbeID: probe, Time: recent, Status: model.StatusUp, LatencyMs: 10},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Rollup(ctx, tz); err != nil {
			t.Fatal(err)
		}

		// The outage's buffered heartbeats for D-10 land only now.
		if err := st.InsertHeartbeats(ctx, []model.Heartbeat{
			{MonitorID: m.ID, ProbeID: probe, Time: old, Status: model.StatusUp, LatencyMs: 20},
			{MonitorID: m.ID, ProbeID: probe, Time: old.Add(time.Minute), Status: model.StatusDown, LatencyMs: 30},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Rollup(ctx, tz); err != nil {
			t.Fatal(err)
		}
		if days, _ := st.ListUptimeDaily(ctx, m.ID, old, old); len(days) != 0 {
			t.Fatalf("setup invariant broken: Rollup already covered D-10: %+v", days)
		}

		// Pruning past D-10 must roll it up first.
		if _, err := st.PruneHeartbeats(ctx, old.Add(24*time.Hour), 100, tz); err != nil {
			t.Fatal(err)
		}
		days, err := st.ListUptimeDaily(ctx, m.ID, old, old)
		if err != nil {
			t.Fatal(err)
		}
		if len(days) != 1 || days[0].Checks != 2 || days[0].Up != 1 {
			t.Fatalf("D-10 not rolled up before pruning: %+v", days)
		}
		left, err := st.ListHeartbeats(ctx, m.ID, time.Time{}, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(left) != 1 {
			t.Fatalf("left %d heartbeats after prune, want 1 (only the D-2 one)", len(left))
		}
	})

	t.Run("heartbeat with invalid encoding is sanitized, not rejected", func(t *testing.T) {
		probe, err := st.LocalProbeID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		m, err := st.CreateMonitor(ctx, model.Monitor{TeamID: team.ID, Name: "badenc", Type: model.TypeTCP,
			Config: json.RawMessage(`{"host":"db","port":5432}`), IntervalS: 60, RetryIntervalS: 20, Retries: 1, TimeoutS: 10})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		err = st.InsertHeartbeats(ctx, []model.Heartbeat{
			{MonitorID: m.ID, ProbeID: probe, Time: now, Status: model.StatusDown, LatencyMs: 1, Message: "a\x00b\xff"},
		})
		if err != nil {
			t.Fatalf("insert with bad encoding must not fail (it would be requeued forever): %v", err)
		}
		hbs, err := st.ListHeartbeats(ctx, m.ID, time.Time{}, 1)
		if err != nil || len(hbs) != 1 {
			t.Fatalf("list: %v %+v", err, hbs)
		}
		if hbs[0].Message != "ab\uFFFD" {
			t.Fatalf("message = %q, want sanitized %q", hbs[0].Message, "ab\uFFFD")
		}
	})

	t.Run("audit entry keeps its actor after the token is deleted", func(t *testing.T) {
		tok, err := st.CreateAPIToken(ctx, team.ID, "ci", model.ScopeRead, []byte("hash-for-audit-test"), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.InsertAudit(ctx, model.AuditEntry{
			TeamID: &team.ID, ActorTokenID: &tok.ID, Action: "token.create", TargetType: "api_token", TargetID: &tok.ID,
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteAPIToken(ctx, team.ID, tok.ID); err != nil {
			t.Fatal(err)
		}
		// Deleting again (or a token that never existed) is ErrNotFound.
		if err := st.DeleteAPIToken(ctx, team.ID, tok.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("delete already-revoked token: want ErrNotFound, got %v", err)
		}
		if _, err := st.UseAPIToken(ctx, []byte("hash-for-audit-test")); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("revoked token must be unusable: want ErrNotFound, got %v", err)
		}
		toks, err := st.ListAPITokens(ctx, team.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, tk := range toks {
			if tk.ID == tok.ID {
				t.Fatalf("revoked token still listed: %+v", tk)
			}
		}
		entries, err := st.ListAudit(ctx, team.ID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range entries {
			if e.Action == "token.create" {
				if e.ActorTokenID == nil || *e.ActorTokenID != tok.ID {
					t.Fatalf("audit entry lost its actor_token_id after the token was deleted: %+v", e)
				}
				found = true
			}
		}
		if !found {
			t.Fatal("token.create audit entry not found")
		}
	})
}

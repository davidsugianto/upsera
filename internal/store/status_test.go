package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/secret"
	"github.com/davidsugianto/upsera/internal/store"
	"github.com/davidsugianto/upsera/internal/testutil"
)

// sx is the shared fixture of the status store tests: a store, a raw SQL
// connection and two teams.
type sx struct {
	t     *testing.T
	st    *store.Store
	url   string
	ctx   context.Context
	conn  *pgx.Conn
	team  model.Team
	other model.Team
	seq   int
}

func newSX(t *testing.T) *sx {
	t.Helper()
	st, url := testutil.Store(t)
	ctx := context.Background()
	admin, team, err := st.Setup(ctx, "admin@example.com", "Admin", "hash", "Ops")
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateTeam(ctx, "Other", admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(ctx) })
	return &sx{t: t, st: st, url: url, ctx: ctx, conn: conn, team: team, other: other}
}

func (x *sx) monitor(teamID int64, name string) model.Monitor {
	x.t.Helper()
	m, err := x.st.CreateMonitor(x.ctx, model.Monitor{TeamID: teamID, Name: name, Type: model.TypeTCP,
		Config: json.RawMessage(`{"host":"db","port":5432}`), IntervalS: 60, RetryIntervalS: 20, Retries: 1, TimeoutS: 10})
	if err != nil {
		x.t.Fatal(err)
	}
	return m
}

func items(ms ...model.Monitor) []model.StatusPageItem {
	out := make([]model.StatusPageItem, len(ms))
	for i, m := range ms {
		out[i] = model.StatusPageItem{Section: "Core", MonitorID: m.ID, DisplayName: "Public " + m.Name, Position: i}
	}
	return out
}

// page creates a page of x.team with a unique title.
func (x *sx) page(slug string, published bool, ms ...model.Monitor) model.StatusPage {
	x.t.Helper()
	p, err := x.st.CreateStatusPage(x.ctx, model.StatusPage{TeamID: x.team.ID, Slug: slug, Title: "Status " + slug,
		Theme: model.ThemeAuto, Published: published, Items: items(ms...)})
	if err != nil {
		x.t.Fatal(err)
	}
	return p
}

func (x *sx) exec(sql string, args ...any) {
	x.t.Helper()
	if _, err := x.conn.Exec(x.ctx, sql, args...); err != nil {
		x.t.Fatalf("%s: %v", sql, err)
	}
}

func (x *sx) count(sql string, args ...any) int {
	x.t.Helper()
	var n int
	if err := x.conn.QueryRow(x.ctx, sql, args...).Scan(&n); err != nil {
		x.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (x *sx) subscriber(pageID int64, kind model.SubscriberKind, target string, confirmed bool) model.Subscriber {
	x.t.Helper()
	s, created, err := x.st.CreateSubscriber(x.ctx, model.Subscriber{PageID: pageID, Kind: kind, Target: target, Confirmed: confirmed})
	if err != nil || !created {
		x.t.Fatalf("create subscriber %s: created=%v err=%v", target, created, err)
	}
	return s
}

func (x *sx) incident(pageID int64, title string, draft bool, body string) model.Incident {
	x.t.Helper()
	in, err := x.st.CreateIncident(x.ctx, model.Incident{PageID: pageID, Title: title, Severity: model.SeverityMinor,
		Status: model.IncidentInvestigating, Draft: draft}, body)
	if err != nil {
		x.t.Fatal(err)
	}
	return in
}

// deliveries counts the incident deliveries of an incident per subscriber.
func (x *sx) incidentDeliveries(incidentID int64) map[int64]int {
	x.t.Helper()
	rows, err := x.conn.Query(x.ctx,
		"SELECT subscriber_id FROM upsera.subscriber_deliveries WHERE kind = 'incident' AND incident_id = $1", incidentID)
	if err != nil {
		x.t.Fatal(err)
	}
	subs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		x.t.Fatal(err)
	}
	out := map[int64]int{}
	for _, s := range subs {
		out[s]++
	}
	return out
}

// claimNow is the "now" handed to ClaimDeliveries. Deliveries are enqueued
// with the database clock, which can run slightly ahead of the test host's
// (a container VM), so a few seconds are added to make fresh rows due.
func claimNow() time.Time { return time.Now().Add(5 * time.Second) }

func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestStatusPagesStore(t *testing.T) {
	x := newSX(t)
	ctx, st := x.ctx, x.st
	m1, m2, m3 := x.monitor(x.team.ID, "web"), x.monitor(x.team.ID, "api"), x.monitor(x.team.ID, "db")
	foreign := x.monitor(x.other.ID, "foreign")

	t.Run("create orders items by position and normalizes the domain", func(t *testing.T) {
		p, err := st.CreateStatusPage(ctx, model.StatusPage{TeamID: x.team.ID, Slug: "acme", Title: "Acme", Description: "d",
			LogoURL: "https://example.com/l.png", Theme: model.ThemeDark, Published: true,
			CustomDomain: new("  Status.Acme.COM "),
			Items: []model.StatusPageItem{
				{Section: "B", MonitorID: m1.ID, DisplayName: "Web", Position: 2},
				{Section: "A", MonitorID: m2.ID, DisplayName: "API", Position: 0},
				{MonitorID: m3.ID, DisplayName: "DB", Position: 1},
			}})
		if err != nil {
			t.Fatal(err)
		}
		if p.ID == 0 || p.TeamID != x.team.ID || p.Slug != "acme" || p.Theme != model.ThemeDark || !p.Published || p.LogoURL != "https://example.com/l.png" {
			t.Fatalf("page = %+v", p)
		}
		if p.CustomDomain == nil || *p.CustomDomain != "status.acme.com" {
			t.Fatalf("custom domain = %v", p.CustomDomain)
		}
		var got []string
		for _, it := range p.Items {
			got = append(got, it.DisplayName)
		}
		if !slices.Equal(got, []string{"API", "DB", "Web"}) || p.Items[1].Section != "" || p.Items[0].Section != "A" {
			t.Fatalf("items = %+v", p.Items)
		}
		g, err := st.GetStatusPage(ctx, x.team.ID, p.ID)
		if err != nil || len(g.Items) != 3 || g.Items[0].MonitorID != m2.ID {
			t.Fatalf("get = %+v, %v", g, err)
		}
	})

	t.Run("empty custom domain is NULL and items are an empty slice", func(t *testing.T) {
		p, err := st.CreateStatusPage(ctx, model.StatusPage{TeamID: x.team.ID, Slug: "nodomain", Title: "T", Theme: model.ThemeAuto,
			CustomDomain: new("")})
		if err != nil {
			t.Fatal(err)
		}
		if p.CustomDomain != nil || p.Items == nil || len(p.Items) != 0 || p.Published {
			t.Fatalf("page = %+v", p)
		}
		var isNull bool
		if err := x.conn.QueryRow(ctx, "SELECT custom_domain IS NULL FROM upsera.status_pages WHERE id = $1", p.ID).Scan(&isNull); err != nil || !isNull {
			t.Fatalf("custom_domain must be NULL: %v %v", isNull, err)
		}
		// A second page without a domain must not collide.
		if _, err := st.CreateStatusPage(ctx, model.StatusPage{TeamID: x.team.ID, Slug: "nodomain2", Title: "T", Theme: model.ThemeAuto,
			CustomDomain: new("  ")}); err != nil {
			t.Fatalf("second page without domain: %v", err)
		}
	})

	t.Run("slug and custom domain are unique across teams", func(t *testing.T) {
		_, err := st.CreateStatusPage(ctx, model.StatusPage{TeamID: x.other.ID, Slug: "acme", Title: "T", Theme: model.ThemeAuto})
		wantErr(t, err, store.ErrConflict)
		_, err = st.CreateStatusPage(ctx, model.StatusPage{TeamID: x.other.ID, Slug: "other-acme", Title: "T", Theme: model.ThemeAuto,
			CustomDomain: new("STATUS.acme.com")})
		wantErr(t, err, store.ErrConflict)
	})

	t.Run("monitor of another team is not found and nothing is created", func(t *testing.T) {
		before, _ := st.ListStatusPages(ctx, x.team.ID)
		_, err := st.CreateStatusPage(ctx, model.StatusPage{TeamID: x.team.ID, Slug: "leaky", Title: "T", Theme: model.ThemeAuto,
			Items: items(m1, foreign)})
		wantErr(t, err, store.ErrNotFound)
		after, _ := st.ListStatusPages(ctx, x.team.ID)
		if len(after) != len(before) {
			t.Fatalf("failed create left a page behind: %d -> %d", len(before), len(after))
		}
		_, err = st.CreateStatusPage(ctx, model.StatusPage{TeamID: x.team.ID, Slug: "ghost", Title: "T", Theme: model.ThemeAuto,
			Items: []model.StatusPageItem{{MonitorID: 999999, DisplayName: "x"}}})
		wantErr(t, err, store.ErrNotFound)
	})

	t.Run("the same monitor twice is a conflict", func(t *testing.T) {
		before, _ := st.ListStatusPages(ctx, x.team.ID)
		_, err := st.CreateStatusPage(ctx, model.StatusPage{TeamID: x.team.ID, Slug: "dupe", Title: "T", Theme: model.ThemeAuto,
			Items: []model.StatusPageItem{{MonitorID: m1.ID, DisplayName: "a", Position: 0}, {MonitorID: m1.ID, DisplayName: "b", Position: 1}}})
		wantErr(t, err, store.ErrConflict)
		after, _ := st.ListStatusPages(ctx, x.team.ID)
		if len(after) != len(before) {
			t.Fatal("failed create left a page behind")
		}
	})

	t.Run("update replaces settings and items", func(t *testing.T) {
		p := x.page("upd", false, m1, m2)
		p.Slug, p.Title, p.Description, p.Theme, p.Published = "upd2", "New", "desc", model.ThemeLight, true
		p.CustomDomain = new("Upd.Example.com")
		p.Items = []model.StatusPageItem{{Section: "S", MonitorID: m3.ID, DisplayName: "Only DB", Position: 0}}
		u, err := st.UpdateStatusPage(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if u.Slug != "upd2" || u.Title != "New" || u.Theme != model.ThemeLight || !u.Published || *u.CustomDomain != "upd.example.com" ||
			len(u.Items) != 1 || u.Items[0].DisplayName != "Only DB" || !u.UpdatedAt.After(p.UpdatedAt) {
			t.Fatalf("updated = %+v", u)
		}
		if n := x.count("SELECT count(*) FROM upsera.status_page_items WHERE page_id = $1", p.ID); n != 1 {
			t.Fatalf("old items must be gone, have %d rows", n)
		}
		u.CustomDomain = nil
		u.Items = nil
		u, err = st.UpdateStatusPage(ctx, u)
		if err != nil || u.CustomDomain != nil || len(u.Items) != 0 {
			t.Fatalf("clear domain and items: %+v %v", u, err)
		}
	})

	t.Run("a failed update changes nothing", func(t *testing.T) {
		p := x.page("keep", false, m1, m2)
		bad := p
		bad.Title = "changed"
		bad.Items = items(m3, foreign)
		_, err := st.UpdateStatusPage(ctx, bad)
		wantErr(t, err, store.ErrNotFound)
		bad = p
		bad.Slug = "acme"
		_, err = st.UpdateStatusPage(ctx, bad)
		wantErr(t, err, store.ErrConflict)
		bad = p
		bad.Items = []model.StatusPageItem{{MonitorID: m3.ID, DisplayName: "a"}, {MonitorID: m3.ID, DisplayName: "b", Position: 1}}
		_, err = st.UpdateStatusPage(ctx, bad)
		wantErr(t, err, store.ErrConflict)
		got, err := st.GetStatusPage(ctx, x.team.ID, p.ID)
		if err != nil || got.Title != p.Title || got.Slug != "keep" || len(got.Items) != 2 {
			t.Fatalf("page after failed updates = %+v, %v", got, err)
		}
	})

	t.Run("get update and delete are team scoped", func(t *testing.T) {
		p := x.page("scoped", true, m1)
		_, err := st.GetStatusPage(ctx, x.other.ID, p.ID)
		wantErr(t, err, store.ErrNotFound)
		hijack := p
		hijack.TeamID = x.other.ID
		hijack.Title = "hijacked"
		_, err = st.UpdateStatusPage(ctx, hijack)
		wantErr(t, err, store.ErrNotFound)
		wantErr(t, st.DeleteStatusPage(ctx, x.other.ID, p.ID), store.ErrNotFound)
		if got, err := st.GetStatusPage(ctx, x.team.ID, p.ID); err != nil || got.Title != p.Title || len(got.Items) != 1 {
			t.Fatalf("page was modified: %+v %v", got, err)
		}
		_, err = st.GetStatusPage(ctx, x.team.ID, 999999)
		wantErr(t, err, store.ErrNotFound)
	})

	t.Run("list is per team and ordered by id", func(t *testing.T) {
		theirs, err := st.CreateStatusPage(ctx, model.StatusPage{TeamID: x.other.ID, Slug: "theirs", Title: "T", Theme: model.ThemeAuto,
			Items: items(foreign)})
		if err != nil {
			t.Fatal(err)
		}
		mine, err := st.ListStatusPages(ctx, x.team.ID)
		if err != nil || len(mine) < 3 {
			t.Fatalf("list = %d, %v", len(mine), err)
		}
		for i, p := range mine {
			if p.TeamID != x.team.ID || p.Items == nil || (i > 0 && mine[i-1].ID >= p.ID) {
				t.Fatalf("list[%d] = %+v", i, p)
			}
		}
		others, err := st.ListStatusPages(ctx, x.other.ID)
		if err != nil || len(others) != 1 || others[0].ID != theirs.ID || len(others[0].Items) != 1 {
			t.Fatalf("other team list = %+v, %v", others, err)
		}
	})

	t.Run("published lookups", func(t *testing.T) {
		pub := x.page("pubpage", true, m1)
		x.page("hidden", false, m1)
		got, err := st.GetPublishedPageBySlug(ctx, "pubpage")
		if err != nil || got.ID != pub.ID || len(got.Items) != 1 {
			t.Fatalf("published = %+v, %v", got, err)
		}
		_, err = st.GetPublishedPageBySlug(ctx, "hidden")
		wantErr(t, err, store.ErrNotFound)
		_, err = st.GetPublishedPageBySlug(ctx, "nope")
		wantErr(t, err, store.ErrNotFound)

		// Domains: acme is published with status.acme.com; add a draft with a domain and a published one without.
		draft := x.page("draftdom", false)
		draft.CustomDomain = new("draft.example.com")
		if _, err := st.UpdateStatusPage(ctx, draft); err != nil {
			t.Fatal(err)
		}
		doms, err := st.ListPublishedCustomDomains(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(doms, []model.PageDomain{{Domain: "status.acme.com", Slug: "acme"}}) {
			t.Fatalf("domains = %+v", doms)
		}
	})

	t.Run("deleting a monitor removes its page items", func(t *testing.T) {
		mon := x.monitor(x.team.ID, "short-lived")
		p := x.page("cascade-mon", true, mon, m1)
		if err := st.DeleteMonitor(ctx, x.team.ID, mon.ID); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetStatusPage(ctx, x.team.ID, p.ID)
		if err != nil || len(got.Items) != 1 || got.Items[0].MonitorID != m1.ID {
			t.Fatalf("items after monitor delete = %+v, %v", got.Items, err)
		}
	})

	t.Run("deleting a page cascades", func(t *testing.T) {
		p := x.page("cascade-page", true, m1)
		sub := x.subscriber(p.ID, model.SubscriberEmail, "a@example.com", true)
		in := x.incident(p.ID, "Outage", false, "looking")
		if n := x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE subscriber_id = $1", sub.ID); n != 1 {
			t.Fatalf("setup deliveries = %d", n)
		}
		if err := st.DeleteStatusPage(ctx, x.team.ID, p.ID); err != nil {
			t.Fatal(err)
		}
		for name, q := range map[string]string{
			"items":       "SELECT count(*) FROM upsera.status_page_items WHERE page_id = $1",
			"incidents":   "SELECT count(*) FROM upsera.incidents WHERE page_id = $1",
			"subscribers": "SELECT count(*) FROM upsera.subscribers WHERE page_id = $1",
		} {
			if n := x.count(q, p.ID); n != 0 {
				t.Errorf("%s left behind: %d", name, n)
			}
		}
		if n := x.count("SELECT count(*) FROM upsera.incident_updates WHERE incident_id = $1", in.ID); n != 0 {
			t.Errorf("incident updates left behind: %d", n)
		}
		if n := x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE subscriber_id = $1", sub.ID); n != 0 {
			t.Errorf("deliveries left behind: %d", n)
		}
		wantErr(t, st.DeleteStatusPage(ctx, x.team.ID, p.ID), store.ErrNotFound)
	})
}

func TestStatusPageUptime(t *testing.T) {
	x := newSX(t)
	ctx, st := x.ctx, x.st
	mA, mB, mC, mD := x.monitor(x.team.ID, "a"), x.monitor(x.team.ID, "b"), x.monitor(x.team.ID, "c"), x.monitor(x.team.ID, "d")
	var probe int64
	if err := x.conn.QueryRow(ctx, "SELECT id FROM upsera.probes WHERE name = 'local'").Scan(&probe); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	day := func(back int) time.Time { return today.AddDate(0, 0, -back) }
	daily := func(m model.Monitor, back, checks, up int) {
		x.exec("INSERT INTO upsera.uptime_daily (monitor_id, day, checks, up, avg_latency_ms, p95_latency_ms) VALUES ($1, $2, $3, $4, 1, 2)",
			m.ID, day(back), checks, up)
	}
	beat := func(m model.Monitor, at time.Time, status model.Status) {
		x.exec("INSERT INTO upsera.heartbeats (monitor_id, probe_id, time, status, latency_ms) VALUES ($1, $2, $3, $4, 5)", m.ID, probe, at, int16(status))
	}

	// Monitor A: three rollup days (one before the window, one today that
	// must be ignored in favour of the raw heartbeats) plus raw beats today.
	daily(mA, 10, 100, 100) // before fromDay
	daily(mA, 3, 100, 90)
	daily(mA, 1, 50, 50)
	daily(mA, 0, 999, 999) // today's rollup is not used
	beat(mA, day(1).Add(23*time.Hour), model.StatusDown)
	for i, s := range []model.Status{model.StatusUp, model.StatusUp, model.StatusPending, model.StatusDown, model.StatusMaintenance} {
		beat(mA, today.Add(time.Duration(i+1)*time.Second), s)
	}
	// Monitor B: only maintenance beats today, no rollup => absent.
	beat(mB, today.Add(time.Second), model.StatusMaintenance)
	// Monitor C: a rollup day only (no beats today).
	daily(mC, 2, 10, 5)
	// Monitor D: nothing at all.

	got, err := st.StatusPageUptime(ctx, []int64{mA.ID, mB.ID, mC.ID, mD.ID}, day(5), today)
	if err != nil {
		t.Fatal(err)
	}
	a := got[mA.ID]
	if len(a) != 3 {
		t.Fatalf("A days = %+v", a)
	}
	if !a[0].Day.Equal(day(3)) || a[0].Checks != 100 || a[0].Up != 90 ||
		!a[1].Day.Equal(day(1)) || a[1].Checks != 50 || a[1].Up != 50 ||
		!a[2].Day.Equal(today) || a[2].Checks != 4 || a[2].Up != 3 || a[2].MonitorID != mA.ID {
		t.Fatalf("A days = %+v", a)
	}
	if _, ok := got[mB.ID]; ok {
		t.Fatalf("maintenance-only monitor must be absent: %+v", got[mB.ID])
	}
	if c := got[mC.ID]; len(c) != 1 || c[0].Checks != 10 || c[0].Up != 5 {
		t.Fatalf("C days = %+v", c)
	}
	if _, ok := got[mD.ID]; ok {
		t.Fatal("monitor without data must be absent")
	}

	t.Run("only requested monitors", func(t *testing.T) {
		got, err := st.StatusPageUptime(ctx, []int64{mC.ID}, day(5), today)
		if err != nil || len(got) != 1 {
			t.Fatalf("got %+v %v", got, err)
		}
		got, err = st.StatusPageUptime(ctx, nil, day(5), today)
		if err != nil || len(got) != 0 {
			t.Fatalf("no monitors: %+v %v", got, err)
		}
	})

	t.Run("window start is inclusive", func(t *testing.T) {
		got, err := st.StatusPageUptime(ctx, []int64{mA.ID}, day(3), today)
		if err != nil || len(got[mA.ID]) != 3 || !got[mA.ID][0].Day.Equal(day(3)) {
			t.Fatalf("got %+v %v", got, err)
		}
		got, err = st.StatusPageUptime(ctx, []int64{mA.ID}, day(2), today)
		if err != nil || len(got[mA.ID]) != 2 || !got[mA.ID][0].Day.Equal(day(1)) {
			t.Fatalf("got %+v %v", got, err)
		}
	})
}

func TestStatusIncidentsStore(t *testing.T) {
	x := newSX(t)
	ctx, st := x.ctx, x.st
	m := x.monitor(x.team.ID, "web")
	pub := x.page("pub", true, m)
	hidden := x.page("hidden", false, m)
	other := x.page("other", true, m)
	subA := x.subscriber(pub.ID, model.SubscriberEmail, "a@example.com", true)
	subW := x.subscriber(pub.ID, model.SubscriberWebhook, "https://hooks.example.com/x", true)
	x.subscriber(pub.ID, model.SubscriberEmail, "unconfirmed@example.com", false)
	x.subscriber(hidden.ID, model.SubscriberEmail, "h@example.com", true)
	x.subscriber(other.ID, model.SubscriberEmail, "o@example.com", true)

	t.Run("draft incident", func(t *testing.T) {
		in := x.incident(pub.ID, "Draft", true, "")
		if !in.Draft || in.PublishedAt != nil || in.ResolvedAt != nil || in.AutoAlertID != nil || len(in.Updates) != 0 || in.Updates == nil ||
			in.Status != model.IncidentInvestigating || in.Severity != model.SeverityMinor || in.PageID != pub.ID {
			t.Fatalf("draft = %+v", in)
		}
		if time.Since(in.StartedAt) > time.Minute || in.StartedAt.After(time.Now().Add(time.Second)) {
			t.Fatalf("started_at must default to now: %v", in.StartedAt)
		}
		if n := len(x.incidentDeliveries(in.ID)); n != 0 {
			t.Fatalf("draft enqueued %d deliveries", n)
		}
	})

	t.Run("draft with first update enqueues nothing", func(t *testing.T) {
		in := x.incident(pub.ID, "Draft body", true, "we are looking")
		if len(in.Updates) != 1 || in.Updates[0].Body != "we are looking" || in.Updates[0].Status != model.IncidentInvestigating {
			t.Fatalf("updates = %+v", in.Updates)
		}
		if n := len(x.incidentDeliveries(in.ID)); n != 0 {
			t.Fatalf("enqueued %d", n)
		}
	})

	t.Run("published incident notifies confirmed subscribers of a published page", func(t *testing.T) {
		in := x.incident(pub.ID, "Live", false, "first")
		if in.Draft || in.PublishedAt == nil || len(in.Updates) != 1 {
			t.Fatalf("incident = %+v", in)
		}
		d := x.incidentDeliveries(in.ID)
		if len(d) != 2 || d[subA.ID] != 1 || d[subW.ID] != 1 {
			t.Fatalf("deliveries = %v, want one each for %d and %d", d, subA.ID, subW.ID)
		}
		if n := x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE incident_id = $1 AND update_id = $2 AND state = 'pending'",
			in.ID, in.Updates[0].ID); n != 2 {
			t.Fatalf("deliveries must reference the first update, got %d", n)
		}
	})

	t.Run("published incident without a body announces the incident as a whole", func(t *testing.T) {
		in := x.incident(pub.ID, "No body", false, "")
		if n := x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE incident_id = $1 AND update_id IS NULL", in.ID); n != 2 {
			t.Fatalf("deliveries with NULL update = %d", n)
		}
	})

	t.Run("unpublished page enqueues nothing", func(t *testing.T) {
		in := x.incident(hidden.ID, "Hidden", false, "x")
		if n := len(x.incidentDeliveries(in.ID)); n != 0 {
			t.Fatalf("enqueued %d for an unpublished page", n)
		}
	})

	t.Run("explicit start and resolved status", func(t *testing.T) {
		start := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Microsecond)
		in, err := st.CreateIncident(ctx, model.Incident{PageID: pub.ID, Title: "Past", Severity: model.SeverityCritical,
			Status: model.IncidentResolved, Draft: true, StartedAt: start}, "all good")
		if err != nil {
			t.Fatal(err)
		}
		if !in.StartedAt.Equal(start) || in.ResolvedAt == nil || !in.ResolvedAt.Equal(start) || in.Status != model.IncidentResolved {
			t.Fatalf("resolved incident = %+v", in)
		}
		end := start.Add(time.Hour)
		in, err = st.CreateIncident(ctx, model.Incident{PageID: pub.ID, Title: "Past2", Severity: model.SeverityMajor,
			Status: model.IncidentResolved, Draft: true, StartedAt: start, ResolvedAt: &end}, "")
		if err != nil || !in.ResolvedAt.Equal(end) {
			t.Fatalf("explicit resolved_at: %+v %v", in, err)
		}
	})

	t.Run("get is page scoped", func(t *testing.T) {
		in := x.incident(pub.ID, "Scoped", true, "b")
		got, err := st.GetIncident(ctx, pub.ID, in.ID)
		if err != nil || got.ID != in.ID || len(got.Updates) != 1 {
			t.Fatalf("get = %+v %v", got, err)
		}
		_, err = st.GetIncident(ctx, other.ID, in.ID)
		wantErr(t, err, store.ErrNotFound)
	})

	t.Run("update changes title and severity only", func(t *testing.T) {
		in := x.incident(pub.ID, "Old", true, "")
		u, err := st.UpdateIncident(ctx, pub.ID, in.ID, "New", model.SeverityCritical)
		if err != nil || u.Title != "New" || u.Severity != model.SeverityCritical || u.Status != in.Status || !u.Draft {
			t.Fatalf("update = %+v %v", u, err)
		}
		_, err = st.UpdateIncident(ctx, other.ID, in.ID, "Hijack", model.SeverityMinor)
		wantErr(t, err, store.ErrNotFound)
		if n := len(x.incidentDeliveries(in.ID)); n != 0 {
			t.Fatalf("update enqueued %d", n)
		}
	})

	t.Run("add update sets and clears resolved_at", func(t *testing.T) {
		in := x.incident(pub.ID, "Lifecycle", false, "")
		base := x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE incident_id = $1", in.ID)
		in, err := st.AddIncidentUpdate(ctx, pub.ID, in.ID, model.IncidentResolved, "fixed")
		if err != nil || in.Status != model.IncidentResolved || in.ResolvedAt == nil || len(in.Updates) != 1 || in.Updates[0].Body != "fixed" {
			t.Fatalf("resolve = %+v %v", in, err)
		}
		firstResolved := *in.ResolvedAt
		if n := x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE incident_id = $1 AND update_id = $2", in.ID, in.Updates[0].ID); n != 2 {
			t.Fatalf("update deliveries = %d, want 2 (base %d)", n, base)
		}
		in, err = st.AddIncidentUpdate(ctx, pub.ID, in.ID, model.IncidentResolved, "still fixed")
		if err != nil || in.ResolvedAt == nil || !in.ResolvedAt.Equal(firstResolved) {
			t.Fatalf("resolving twice must keep the first resolved_at: %+v %v", in, err)
		}
		in, err = st.AddIncidentUpdate(ctx, pub.ID, in.ID, model.IncidentMonitoring, "back again")
		if err != nil || in.Status != model.IncidentMonitoring || in.ResolvedAt != nil || len(in.Updates) != 3 {
			t.Fatalf("reopen = %+v %v", in, err)
		}
		if in.Updates[0].Body != "fixed" || in.Updates[2].Body != "back again" {
			t.Fatalf("updates must be oldest first: %+v", in.Updates)
		}
		_, err = st.AddIncidentUpdate(ctx, other.ID, in.ID, model.IncidentResolved, "x")
		wantErr(t, err, store.ErrNotFound)
		if n := x.count("SELECT count(*) FROM upsera.incident_updates WHERE incident_id = $1", in.ID); n != 3 {
			t.Fatalf("failed add left an update: %d", n)
		}
	})

	t.Run("update on a draft enqueues nothing", func(t *testing.T) {
		in := x.incident(pub.ID, "Draft upd", true, "")
		in, err := st.AddIncidentUpdate(ctx, pub.ID, in.ID, model.IncidentIdentified, "found it")
		if err != nil || !in.Draft || in.Status != model.IncidentIdentified {
			t.Fatalf("update = %+v %v", in, err)
		}
		if n := len(x.incidentDeliveries(in.ID)); n != 0 {
			t.Fatalf("enqueued %d", n)
		}
	})

	t.Run("publish is idempotent", func(t *testing.T) {
		in := x.incident(pub.ID, "To publish", true, "")
		in, err := st.PublishIncident(ctx, pub.ID, in.ID)
		if err != nil || in.Draft || in.PublishedAt == nil {
			t.Fatalf("publish = %+v %v", in, err)
		}
		published := *in.PublishedAt
		d := x.incidentDeliveries(in.ID)
		if len(d) != 2 || d[subA.ID] != 1 || d[subW.ID] != 1 {
			t.Fatalf("deliveries = %v", d)
		}
		if n := x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE incident_id = $1 AND update_id IS NULL", in.ID); n != 2 {
			t.Fatalf("publish must announce the incident as a whole, got %d", n)
		}
		in, err = st.PublishIncident(ctx, pub.ID, in.ID)
		if err != nil || !in.PublishedAt.Equal(published) {
			t.Fatalf("second publish = %+v %v", in, err)
		}
		if n := x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE incident_id = $1", in.ID); n != 2 {
			t.Fatalf("second publish enqueued again: %d", n)
		}
		_, err = st.PublishIncident(ctx, other.ID, in.ID)
		wantErr(t, err, store.ErrNotFound)
		_, err = st.PublishIncident(ctx, pub.ID, 999999)
		wantErr(t, err, store.ErrNotFound)
	})

	t.Run("publishing on an unpublished page enqueues nothing", func(t *testing.T) {
		in := x.incident(hidden.ID, "Hidden draft", true, "")
		in, err := st.PublishIncident(ctx, hidden.ID, in.ID)
		if err != nil || in.Draft {
			t.Fatal(err)
		}
		if n := len(x.incidentDeliveries(in.ID)); n != 0 {
			t.Fatalf("enqueued %d", n)
		}
	})

	t.Run("a duplicate auto alert on a page is a conflict", func(t *testing.T) {
		id := uuid.Must(uuid.NewV7()).String()
		x.exec("INSERT INTO upsera.alerts (id, team_id, monitor_id, incident_start, opened_at) VALUES ($1, $2, $3, now(), now())", id, x.team.ID, m.ID)
		mk := func(pageID int64) error {
			_, err := st.CreateIncident(ctx, model.Incident{PageID: pageID, Title: "auto", Severity: model.SeverityMajor,
				Status: model.IncidentInvestigating, Draft: true, AutoAlertID: &id}, "")
			return err
		}
		if err := mk(pub.ID); err != nil {
			t.Fatal(err)
		}
		wantErr(t, mk(pub.ID), store.ErrConflict)
		if err := mk(other.ID); err != nil {
			t.Fatalf("same alert on another page: %v", err)
		}
	})

	t.Run("list filters drafts, orders newest first and limits", func(t *testing.T) {
		p := x.page("listing", true, m)
		base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
		mk := func(title string, ageH int, draft bool) {
			t.Helper()
			if _, err := st.CreateIncident(ctx, model.Incident{PageID: p.ID, Title: title, Severity: model.SeverityMinor,
				Status: model.IncidentInvestigating, Draft: draft, StartedAt: base.Add(time.Duration(ageH) * time.Hour)}, title+" body"); err != nil {
				t.Fatal(err)
			}
		}
		mk("oldest", 0, false)
		mk("draft", 5, true)
		mk("newest", 10, false)
		mk("middle", 2, false)
		titles := func(includeDrafts bool, limit int) []string {
			l, err := st.ListIncidents(ctx, p.ID, includeDrafts, limit)
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, in := range l {
				out = append(out, in.Title)
				if len(in.Updates) != 1 || in.Updates[0].Body != in.Title+" body" {
					t.Fatalf("updates of %q = %+v", in.Title, in.Updates)
				}
			}
			return out
		}
		if got := titles(false, 10); !slices.Equal(got, []string{"newest", "middle", "oldest"}) {
			t.Fatalf("public list = %v", got)
		}
		if got := titles(true, 10); !slices.Equal(got, []string{"newest", "draft", "middle", "oldest"}) {
			t.Fatalf("dashboard list = %v", got)
		}
		if got := titles(true, 2); !slices.Equal(got, []string{"newest", "draft"}) {
			t.Fatalf("limited list = %v", got)
		}
		l, err := st.ListIncidents(ctx, 999999, true, 10)
		if err != nil || len(l) != 0 {
			t.Fatalf("empty list = %+v %v", l, err)
		}
	})

	t.Run("delete cascades", func(t *testing.T) {
		in := x.incident(pub.ID, "Doomed", false, "x")
		in, _ = st.AddIncidentUpdate(ctx, pub.ID, in.ID, model.IncidentMonitoring, "y")
		wantErr(t, st.DeleteIncident(ctx, other.ID, in.ID), store.ErrNotFound)
		if err := st.DeleteIncident(ctx, pub.ID, in.ID); err != nil {
			t.Fatal(err)
		}
		if n := x.count("SELECT count(*) FROM upsera.incident_updates WHERE incident_id = $1", in.ID); n != 0 {
			t.Fatalf("updates left: %d", n)
		}
		if n := x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE incident_id = $1", in.ID); n != 0 {
			t.Fatalf("deliveries left: %d", n)
		}
		wantErr(t, st.DeleteIncident(ctx, pub.ID, in.ID), store.ErrNotFound)
	})
}

func TestStatusDeliveries(t *testing.T) {
	x := newSX(t)
	ctx, st := x.ctx, x.st
	m := x.monitor(x.team.ID, "web")
	p := x.page("deliv", true, m)
	subA := x.subscriber(p.ID, model.SubscriberEmail, "a@example.com", true)
	subW := x.subscriber(p.ID, model.SubscriberWebhook, "https://hooks.example.com/x", true)
	subU := x.subscriber(p.ID, model.SubscriberEmail, "u@example.com", false)

	// An incident with two timeline entries, announcing the second.
	in := x.incident(p.ID, "Outage", false, "first")
	in, err := st.AddIncidentUpdate(ctx, p.ID, in.ID, model.IncidentMonitoring, "second")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueConfirm(ctx, subU.ID); err != nil {
		t.Fatal(err)
	}
	// 2 (create) + 2 (update) + 1 (confirm).
	if n := x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE state = 'pending'"); n != 5 {
		t.Fatalf("pending = %d, want 5", n)
	}

	now := claimNow()
	t.Run("claim returns complete joined data and leases", func(t *testing.T) {
		got, err := st.ClaimDeliveries(ctx, now, 100, 5*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 5 {
			t.Fatalf("claimed %d, want 5", len(got))
		}
		kinds := map[model.DeliveryKind]int{}
		for _, d := range got {
			kinds[d.Kind]++
			if d.Attempts != 0 || d.Page.ID != p.ID || d.Page.Slug != "deliv" || d.Page.Title != "Status deliv" || !d.Page.Published ||
				d.Subscriber.PageID != p.ID || d.Subscriber.Target == "" {
				t.Fatalf("delivery = %+v", d)
			}
			switch d.Kind {
			case model.DeliveryConfirm:
				if d.Incident != nil || d.UpdateID != nil || d.Subscriber.ID != subU.ID || d.Subscriber.Confirmed {
					t.Fatalf("confirm delivery = %+v", d)
				}
			case model.DeliveryIncident:
				if d.Incident == nil || d.Incident.ID != in.ID || d.Incident.Title != "Outage" || len(d.Incident.Updates) != 2 ||
					d.Incident.Updates[0].Body != "first" || d.Incident.Updates[1].Body != "second" {
					t.Fatalf("incident delivery = %+v", d)
				}
				if d.Subscriber.ID != subA.ID && d.Subscriber.ID != subW.ID {
					t.Fatalf("incident delivery to %d", d.Subscriber.ID)
				}
				if d.UpdateID == nil {
					t.Fatalf("incident delivery without update: %+v", d)
				}
			}
		}
		if kinds[model.DeliveryConfirm] != 1 || kinds[model.DeliveryIncident] != 4 {
			t.Fatalf("kinds = %v", kinds)
		}
		ids := make([]int64, len(got))
		for i, d := range got {
			ids[i] = d.ID
		}
		if !slices.IsSorted(ids) {
			t.Errorf("claim order = %v, want ascending ids", ids)
		}
		again, err := st.ClaimDeliveries(ctx, now, 100, 5*time.Minute)
		if err != nil || len(again) != 0 {
			t.Fatalf("leased deliveries were claimed again: %d %v", len(again), err)
		}
	})

	t.Run("a lease expires and the limit applies", func(t *testing.T) {
		later := now.Add(10 * time.Minute)
		got, err := st.ClaimDeliveries(ctx, later, 2, time.Minute)
		if err != nil || len(got) != 2 {
			t.Fatalf("claim limit 2: %d %v", len(got), err)
		}
		rest, err := st.ClaimDeliveries(ctx, later, 100, time.Minute)
		if err != nil || len(rest) != 3 {
			t.Fatalf("rest = %d %v", len(rest), err)
		}
	})

	t.Run("sent deliveries are never claimed again", func(t *testing.T) {
		got, err := st.ClaimDeliveries(ctx, now.Add(time.Hour), 1, time.Minute)
		if err != nil || len(got) != 1 {
			t.Fatalf("claim = %d %v", len(got), err)
		}
		if err := st.MarkDeliverySent(ctx, got[0].ID); err != nil {
			t.Fatal(err)
		}
		var state string
		var attempts int
		var sentAt *time.Time
		if err := x.conn.QueryRow(ctx, "SELECT state, attempts, sent_at FROM upsera.subscriber_deliveries WHERE id = $1", got[0].ID).
			Scan(&state, &attempts, &sentAt); err != nil || state != "sent" || attempts != 1 || sentAt == nil {
			t.Fatalf("sent row: %s %d %v %v", state, attempts, sentAt, err)
		}
		// Only the 4 other deliveries remain claimable.
		rest, err := st.ClaimDeliveries(ctx, now.Add(24*time.Hour), 100, time.Minute)
		if err != nil || len(rest) != 4 {
			t.Fatalf("rest = %d %v", len(rest), err)
		}
		for _, d := range rest {
			if d.ID == got[0].ID {
				t.Fatal("sent delivery was claimed again")
			}
		}
	})

	t.Run("failed deliveries retry then give up", func(t *testing.T) {
		far := now.Add(48 * time.Hour)
		got, err := st.ClaimDeliveries(ctx, far, 1, time.Minute)
		if err != nil || len(got) != 1 {
			t.Fatalf("claim = %d %v", len(got), err)
		}
		id := got[0].ID
		long := strings.Repeat("é", 700)
		state := func() (string, int, string, bool) {
			var s, e string
			var a int
			var future bool
			if err := x.conn.QueryRow(ctx,
				"SELECT state, attempts, last_error, next_attempt_at > now() + interval '30 minutes' FROM upsera.subscriber_deliveries WHERE id = $1", id).
				Scan(&s, &a, &e, &future); err != nil {
				t.Fatal(err)
			}
			return s, a, e, future
		}
		if err := st.MarkDeliveryFailed(ctx, id, long, 3, time.Hour); err != nil {
			t.Fatal(err)
		}
		s, a, e, future := state()
		if s != "pending" || a != 1 || len([]rune(e)) != 500 || !future {
			t.Fatalf("after 1st failure: %s %d %d future=%v", s, a, len([]rune(e)), future)
		}
		// Not due yet: a claim at the real current time must not return it.
		due, err := st.ClaimDeliveries(ctx, time.Now(), 100, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range due {
			if d.ID == id {
				t.Fatal("a delivery waiting for retry was claimed early")
			}
		}
		if err := st.MarkDeliveryFailed(ctx, id, "boom", 3, time.Hour); err != nil {
			t.Fatal(err)
		}
		if s, a, e, _ = state(); s != "pending" || a != 2 || e != "boom" {
			t.Fatalf("after 2nd failure: %s %d %q", s, a, e)
		}
		if err := st.MarkDeliveryFailed(ctx, id, "boom again", 3, time.Hour); err != nil {
			t.Fatal(err)
		}
		if s, a, e, _ = state(); s != "failed" || a != 3 || e != "boom again" {
			t.Fatalf("after 3rd failure: %s %d %q", s, a, e)
		}
		later, err := st.ClaimDeliveries(ctx, far.Add(24*time.Hour), 100, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range later {
			if d.ID == id {
				t.Fatal("failed delivery was claimed again")
			}
		}
	})

	t.Run("purge removes finished deliveries older than the cutoff", func(t *testing.T) {
		x.exec("UPDATE upsera.subscriber_deliveries SET created_at = now() - interval '10 days' WHERE state <> 'pending'")
		x.exec("UPDATE upsera.subscriber_deliveries SET created_at = now() - interval '10 days' WHERE id = (SELECT min(id) FROM upsera.subscriber_deliveries WHERE state = 'pending')")
		finished := x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE state <> 'pending'")
		pending := x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE state = 'pending'")
		if finished != 2 {
			t.Fatalf("finished = %d, want 2", finished)
		}
		// A recent finished delivery stays.
		x.exec("UPDATE upsera.subscriber_deliveries SET state = 'sent' WHERE id = (SELECT max(id) FROM upsera.subscriber_deliveries WHERE state = 'pending')")
		n, err := st.PurgeDeliveries(ctx, time.Now().Add(-24*time.Hour))
		if err != nil || n != 2 {
			t.Fatalf("purged %d, %v", n, err)
		}
		if got := x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE state = 'pending'"); got != pending-1 {
			t.Fatalf("pending after purge = %d, want %d", got, pending-1)
		}
		if got := x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE state = 'sent'"); got != 1 {
			t.Fatalf("recent finished delivery was purged: %d left", got)
		}
	})
}

func TestStatusClaimIsExclusive(t *testing.T) {
	x := newSX(t)
	ctx, st := x.ctx, x.st
	m := x.monitor(x.team.ID, "web")
	p := x.page("race", true, m)
	sub := x.subscriber(p.ID, model.SubscriberEmail, "u@example.com", false)
	const total = 60
	for range total {
		if err := st.EnqueueConfirm(ctx, sub.ID); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	now := claimNow()
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				got, err := st.ClaimDeliveries(ctx, now, 7, time.Hour)
				if err != nil {
					errs <- err
					return
				}
				if len(got) == 0 {
					return
				}
				mu.Lock()
				for _, d := range got {
					seen[d.ID]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if len(seen) != total {
		t.Fatalf("claimed %d distinct deliveries, want %d", len(seen), total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("delivery %d was claimed %d times", id, n)
		}
	}
}

func TestStatusAutoIncidents(t *testing.T) {
	x := newSX(t)
	ctx, st := x.ctx, x.st
	mk := func(name string) model.Monitor { return x.monitor(x.team.ID, name) }
	m1, mUnpub, mResolved, mSuppressed, mFresh, mNoPage, m7 := mk("m1"), mk("unpub"), mk("resolved"), mk("suppressed"), mk("fresh"), mk("nopage"), mk("m7")

	p1 := x.page("auto-one", true)
	p2 := x.page("auto-two", true)
	pHidden := x.page("auto-hidden", false)
	for _, e := range []struct {
		page model.StatusPage
		m    model.Monitor
		name string
	}{
		{p1, m1, "One M1"}, {p2, m1, "Two M1"}, {pHidden, mUnpub, "Hidden"}, {p1, mResolved, "Res"},
		{p1, mSuppressed, "Supp"}, {p1, mFresh, "Fresh"}, {p1, m7, "Seven"},
	} {
		p, err := st.GetStatusPage(ctx, x.team.ID, e.page.ID)
		if err != nil {
			t.Fatal(err)
		}
		p.Items = append(p.Items, model.StatusPageItem{MonitorID: e.m.ID, DisplayName: e.name, Position: len(p.Items)})
		if _, err := st.UpdateStatusPage(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	subOne := x.subscriber(p1.ID, model.SubscriberEmail, "one@example.com", true)
	subTwo := x.subscriber(p2.ID, model.SubscriberEmail, "two@example.com", true)

	now := time.Now().UTC().Truncate(time.Microsecond)
	cutoff := now.Add(-5 * time.Minute)
	alert := func(m model.Monitor, start time.Time, mod func(*model.Alert)) model.Alert {
		a := model.Alert{ID: uuid.Must(uuid.NewV7()).String(), TeamID: x.team.ID, MonitorID: m.ID, IncidentStart: start, OpenedAt: start, Step: -1}
		if mod != nil {
			mod(&a)
		}
		if err := st.UpsertAlerts(ctx, []model.Alert{a}); err != nil {
			t.Fatal(err)
		}
		return a
	}
	old := now.Add(-10 * time.Minute)
	a1 := alert(m1, old, nil)
	alert(mUnpub, old, nil)
	alert(mResolved, old, func(a *model.Alert) {
		a.ResolvedAt, a.Resolution = new(now.Add(-time.Minute)), model.ResolutionRecovered
	})
	alert(mSuppressed, old, func(a *model.Alert) { a.Suppressed = true })
	alert(mFresh, now.Add(-time.Minute), nil)
	alert(mNoPage, old, nil)

	t.Run("candidates are open, unsuppressed, old and on published pages", func(t *testing.T) {
		got, err := st.AutoDraftCandidates(ctx, cutoff)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("candidates = %+v", got)
		}
		byPage := map[int64]model.DraftCandidate{}
		for _, c := range got {
			byPage[c.PageID] = c
		}
		c1, c2 := byPage[p1.ID], byPage[p2.ID]
		if c1.MonitorID != m1.ID || c1.AlertID != a1.ID || c1.DisplayName != "One M1" || !c1.StartedAt.Equal(old) ||
			c2.MonitorID != m1.ID || c2.AlertID != a1.ID || c2.DisplayName != "Two M1" {
			t.Fatalf("candidates = %+v", got)
		}
		// Not yet old enough.
		if got, err := st.AutoDraftCandidates(ctx, old.Add(-time.Minute)); err != nil || len(got) != 0 {
			t.Fatalf("too early: %+v %v", got, err)
		}
	})

	t.Run("an incident for a page removes only that page's candidate", func(t *testing.T) {
		_, err := st.CreateIncident(ctx, model.Incident{PageID: p1.ID, Title: "One M1 is down", Severity: model.SeverityMajor,
			Status: model.IncidentInvestigating, Draft: true, AutoAlertID: &a1.ID, StartedAt: old}, "")
		if err != nil {
			t.Fatal(err)
		}
		got, err := st.AutoDraftCandidates(ctx, cutoff)
		if err != nil || len(got) != 1 || got[0].PageID != p2.ID {
			t.Fatalf("candidates = %+v %v", got, err)
		}
	})

	var a1Incident2 model.Incident
	t.Run("resolve does nothing while alerts are open", func(t *testing.T) {
		var err error
		a1Incident2, err = st.CreateIncident(ctx, model.Incident{PageID: p2.ID, Title: "Two M1 is down", Severity: model.SeverityMajor,
			Status: model.IncidentInvestigating, Draft: false, AutoAlertID: &a1.ID, StartedAt: old}, "auto")
		if err != nil {
			t.Fatal(err)
		}
		if n, err := st.ResolveAutoIncidents(ctx, "recovered"); err != nil || n != 0 {
			t.Fatalf("resolved %d, %v", n, err)
		}
		if got, _ := st.AutoDraftCandidates(ctx, cutoff); len(got) != 0 {
			t.Fatalf("candidates = %+v", got)
		}
	})

	var incP1 model.Incident
	t.Run("resolve closes incidents of resolved alerts only", func(t *testing.T) {
		incs, err := st.ListIncidents(ctx, p1.ID, true, 10)
		if err != nil || len(incs) != 1 {
			t.Fatalf("p1 incidents = %+v %v", incs, err)
		}
		incP1 = incs[0]
		// A manual incident that must stay untouched.
		manual := x.incident(p1.ID, "manual", false, "")
		deliveriesBefore := x.count("SELECT count(*) FROM upsera.subscriber_deliveries")

		resolvedAt := now.Add(-2 * time.Minute)
		a1.ResolvedAt, a1.Resolution = &resolvedAt, model.ResolutionRecovered
		if err := st.UpsertAlerts(ctx, []model.Alert{a1}); err != nil {
			t.Fatal(err)
		}
		n, err := st.ResolveAutoIncidents(ctx, "The monitor recovered.")
		if err != nil || n != 2 {
			t.Fatalf("resolved %d, %v", n, err)
		}
		for _, id := range []struct{ page, inc int64 }{{p1.ID, incP1.ID}, {p2.ID, a1Incident2.ID}} {
			got, err := st.GetIncident(ctx, id.page, id.inc)
			if err != nil {
				t.Fatal(err)
			}
			last := got.Updates[len(got.Updates)-1]
			if got.Status != model.IncidentResolved || got.ResolvedAt == nil || !got.ResolvedAt.Equal(resolvedAt) ||
				last.Status != model.IncidentResolved || last.Body != "The monitor recovered." {
				t.Fatalf("incident after resolve = %+v", got)
			}
		}
		if got, _ := st.GetIncident(ctx, p1.ID, manual.ID); got.Status != model.IncidentInvestigating || got.ResolvedAt != nil || len(got.Updates) != 0 {
			t.Fatalf("manual incident touched: %+v", got)
		}
		// Only the published incident (page two) was announced, to its subscriber.
		if after := x.count("SELECT count(*) FROM upsera.subscriber_deliveries"); after != deliveriesBefore+1 {
			t.Fatalf("deliveries %d -> %d, want +1", deliveriesBefore, after)
		}
		got, _ := st.GetIncident(ctx, p2.ID, a1Incident2.ID)
		if d := x.incidentDeliveries(a1Incident2.ID); d[subTwo.ID] != 2 || len(d) != 1 {
			t.Fatalf("page two deliveries = %v", d)
		}
		if x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE incident_id = $1 AND update_id = $2",
			a1Incident2.ID, got.Updates[len(got.Updates)-1].ID) != 1 {
			t.Fatal("the resolve delivery must reference the new update")
		}
		if d := x.incidentDeliveries(incP1.ID); len(d) != 0 || subOne.ID == 0 {
			t.Fatalf("the draft was announced: %v", d)
		}

		if n, err := st.ResolveAutoIncidents(ctx, "again"); err != nil || n != 0 {
			t.Fatalf("second run resolved %d, %v", n, err)
		}
		if got, _ := st.GetIncident(ctx, p2.ID, a1Incident2.ID); len(got.Updates) != 2 {
			t.Fatalf("second run added updates: %+v", got.Updates)
		}
	})

	t.Run("a manually resolved auto incident is left alone", func(t *testing.T) {
		a7 := alert(m7, old, nil)
		in, err := st.CreateIncident(ctx, model.Incident{PageID: p1.ID, Title: "Seven", Severity: model.SeverityMinor,
			Status: model.IncidentInvestigating, Draft: false, AutoAlertID: &a7.ID, StartedAt: old}, "auto")
		if err != nil {
			t.Fatal(err)
		}
		in, err = st.AddIncidentUpdate(ctx, p1.ID, in.ID, model.IncidentResolved, "manual fix")
		if err != nil {
			t.Fatal(err)
		}
		manualAt := *in.ResolvedAt
		a7.ResolvedAt, a7.Resolution = new(now.Add(-time.Hour)), model.ResolutionRecovered
		if err := st.UpsertAlerts(ctx, []model.Alert{a7}); err != nil {
			t.Fatal(err)
		}
		if n, err := st.ResolveAutoIncidents(ctx, "auto"); err != nil || n != 0 {
			t.Fatalf("resolved %d, %v", n, err)
		}
		got, _ := st.GetIncident(ctx, p1.ID, in.ID)
		if len(got.Updates) != 2 || !got.ResolvedAt.Equal(manualAt) {
			t.Fatalf("incident = %+v", got)
		}
	})

	t.Run("a resolved alert is no longer a candidate", func(t *testing.T) {
		if got, err := st.AutoDraftCandidates(ctx, cutoff); err != nil || len(got) != 0 {
			t.Fatalf("candidates = %+v %v", got, err)
		}
	})
}

// Deleting an auto-drafted incident while its alert is still open must dismiss
// it for good: the worker must not draft it again on the next pass.
func TestStatusDismissAutoIncident(t *testing.T) {
	x := newSX(t)
	ctx, st := x.ctx, x.st
	m := x.monitor(x.team.ID, "dismiss-me")
	p := x.page("dismiss", true, m)
	sub := x.subscriber(p.ID, model.SubscriberEmail, "d@example.com", true)

	now := time.Now().UTC().Truncate(time.Microsecond)
	old := now.Add(-10 * time.Minute)
	cutoff := now.Add(-5 * time.Minute)
	a := model.Alert{ID: uuid.Must(uuid.NewV7()).String(), TeamID: x.team.ID, MonitorID: m.ID, IncidentStart: old, OpenedAt: old, Step: -1}
	if err := st.UpsertAlerts(ctx, []model.Alert{a}); err != nil {
		t.Fatal(err)
	}
	in, err := st.CreateIncident(ctx, model.Incident{PageID: p.ID, Title: "down", Severity: model.SeverityMajor,
		Status: model.IncidentInvestigating, Draft: false, AutoAlertID: &a.ID, StartedAt: old}, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if d := x.incidentDeliveries(in.ID); d[sub.ID] != 1 {
		t.Fatalf("setup: the published incident should be queued for the subscriber: %v", d)
	}

	if err := st.DeleteIncident(ctx, p.ID, in.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetIncident(ctx, p.ID, in.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a dismissed incident must be gone for readers: %v", err)
	}
	if list, err := st.ListIncidents(ctx, p.ID, true, 10); err != nil || len(list) != 0 {
		t.Fatalf("list after dismiss = %+v %v", list, err)
	}
	if got, err := st.AutoDraftCandidates(ctx, cutoff); err != nil || len(got) != 0 {
		t.Fatalf("the open alert must not be drafted again: %+v %v", got, err)
	}
	if d := x.incidentDeliveries(in.ID); len(d) != 0 {
		t.Fatalf("pending notices of a dismissed incident must be dropped: %v", d)
	}
	if err := st.DeleteIncident(ctx, p.ID, in.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleting a dismissed incident again: %v", err)
	}

	// Recovery of the alert neither resurrects nor announces it.
	a.ResolvedAt, a.Resolution = new(now), model.ResolutionRecovered
	if err := st.UpsertAlerts(ctx, []model.Alert{a}); err != nil {
		t.Fatal(err)
	}
	before := x.count("SELECT count(*) FROM upsera.subscriber_deliveries")
	if n, err := st.ResolveAutoIncidents(ctx, "recovered"); err != nil || n != 0 {
		t.Fatalf("resolved %d, %v", n, err)
	}
	if after := x.count("SELECT count(*) FROM upsera.subscriber_deliveries"); after != before {
		t.Fatalf("a dismissed incident was announced: %d -> %d", before, after)
	}

	t.Run("with a resolved alert, or a manual incident, delete really deletes", func(t *testing.T) {
		m2 := x.monitor(x.team.ID, "dismiss-resolved")
		a2 := model.Alert{ID: uuid.Must(uuid.NewV7()).String(), TeamID: x.team.ID, MonitorID: m2.ID, IncidentStart: old, OpenedAt: old, Step: -1,
			ResolvedAt: new(now), Resolution: model.ResolutionRecovered}
		if err := st.UpsertAlerts(ctx, []model.Alert{a2}); err != nil {
			t.Fatal(err)
		}
		auto, err := st.CreateIncident(ctx, model.Incident{PageID: p.ID, Title: "old", Severity: model.SeverityMinor,
			Status: model.IncidentInvestigating, Draft: true, AutoAlertID: &a2.ID, StartedAt: old}, "")
		if err != nil {
			t.Fatal(err)
		}
		manual := x.incident(p.ID, "manual", true, "")
		for _, id := range []int64{auto.ID, manual.ID} {
			if err := st.DeleteIncident(ctx, p.ID, id); err != nil {
				t.Fatal(err)
			}
			if n := x.count("SELECT count(*) FROM upsera.incidents WHERE id = $1", id); n != 0 {
				t.Fatalf("incident %d should be deleted, rows = %d", id, n)
			}
		}
	})
}

func TestStatusCountConfirmDeliveries(t *testing.T) {
	x := newSX(t)
	ctx, st := x.ctx, x.st
	p1, p2 := x.page("count-one", true), x.page("count-two", true)
	a := x.subscriber(p1.ID, model.SubscriberEmail, "a@example.com", false)
	b := x.subscriber(p1.ID, model.SubscriberEmail, "b@example.com", false)
	c := x.subscriber(p2.ID, model.SubscriberEmail, "c@example.com", false)
	for _, sub := range []model.Subscriber{a, b, c} {
		if err := st.EnqueueConfirm(ctx, sub.ID); err != nil {
			t.Fatal(err)
		}
	}
	// A notice of another kind and an old confirmation must not count.
	x.exec("INSERT INTO upsera.subscriber_deliveries (subscriber_id, kind, created_at) VALUES ($1, 'confirm', now() - interval '3 hours')", a.ID)
	since := time.Now().Add(-time.Hour)
	for _, tc := range []struct {
		page int64
		want int
	}{{p1.ID, 2}, {p2.ID, 1}, {0, 3}, {999999, 0}} {
		got, err := st.CountConfirmDeliveries(ctx, since, tc.page)
		if err != nil || got != tc.want {
			t.Errorf("CountConfirmDeliveries(page %d) = %d, %v; want %d", tc.page, got, err, tc.want)
		}
	}
	if got, _ := st.CountConfirmDeliveries(ctx, time.Now().Add(-6*time.Hour), 0); got != 4 {
		t.Errorf("with a longer window = %d, want 4", got)
	}
}

// Cascading deletes (unsubscribing, deleting an incident or a page, the
// hourly purge) look deliveries up by these columns: each needs an index.
func TestStatusMigrationIndexes(t *testing.T) {
	x := newSX(t)
	for _, col := range []string{"subscriber_id", "incident_id", "update_id"} {
		n := x.count(`SELECT count(*) FROM pg_indexes WHERE schemaname = 'upsera' AND tablename = 'subscriber_deliveries'
			AND indexdef LIKE '%' || $1 || '%' AND indexname <> 'subscriber_deliveries_pkey'`, "("+col+")")
		if n == 0 {
			t.Errorf("no index on subscriber_deliveries(%s)", col)
		}
	}
	if x.count(`SELECT count(*) FROM pg_indexes WHERE schemaname = 'upsera' AND tablename = 'subscriber_deliveries' AND indexdef LIKE '%created_at%'`) == 0 {
		t.Error("no index to count recent confirmation mails")
	}
}

// Deferring a delivery (no SMTP yet, mail server down) reschedules it
// without using up one of its attempts: after any number of deferrals a real
// failure is still a first failure, not the last straw.
func TestStatusDeferDelivery(t *testing.T) {
	x := newSX(t)
	ctx, st := x.ctx, x.st
	p := x.page("defer", true)
	sub := x.subscriber(p.ID, model.SubscriberEmail, "defer@example.com", true)
	in := x.incident(p.ID, "defer", false, "body")
	if d := x.incidentDeliveries(in.ID); d[sub.ID] != 1 {
		t.Fatalf("setup: deliveries %v", d)
	}
	id := int64(x.count("SELECT id FROM upsera.subscriber_deliveries WHERE incident_id = $1", in.ID))

	for range 25 {
		if err := st.DeferDelivery(ctx, id, "SMTP is not configured on this instance", 10*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if n := x.count("SELECT attempts FROM upsera.subscriber_deliveries WHERE id = $1", id); n != 0 {
		t.Fatalf("attempts after 25 deferrals = %d, want 0", n)
	}
	if n := x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE id = $1 AND state = 'pending' AND last_error <> '' AND next_attempt_at > now() + interval '9 minutes'", id); n != 1 {
		t.Fatalf("a deferred delivery must stay pending, keep its reason and wait ~10 minutes (rows %d)", n)
	}

	// A genuine failure afterwards is attempt number one of ten.
	if err := st.MarkDeliveryFailed(ctx, id, "authentication failed", 10, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if n := x.count("SELECT attempts FROM upsera.subscriber_deliveries WHERE id = $1 AND state = 'pending'", id); n != 1 {
		t.Fatalf("after one real failure: attempts = %d (state must still be pending)", n)
	}

	// Only a pending delivery can be deferred.
	x.exec("UPDATE upsera.subscriber_deliveries SET state = 'sent' WHERE id = $1", id)
	if err := st.DeferDelivery(ctx, id, "late", time.Minute); err != nil {
		t.Fatal(err)
	}
	if n := x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE id = $1 AND state = 'sent'", id); n != 1 {
		t.Fatal("a sent delivery was put back in the queue")
	}
}

// A notice that could not be delivered for a day is dropped, not sent late:
// "investigating" mails for an incident that ended days ago are noise.
func TestStatusExpireDeliveries(t *testing.T) {
	x := newSX(t)
	ctx, st := x.ctx, x.st
	p := x.page("expire", true)
	for _, email := range []string{"old@example.com", "deferred@example.com", "sent@example.com", "fresh@example.com", "silent@example.com"} {
		x.subscriber(p.ID, model.SubscriberEmail, email, true)
	}
	subs, err := st.ListSubscribers(ctx, p.ID)
	if err != nil || len(subs) != 5 {
		t.Fatalf("subscribers = %+v %v", subs, err)
	}
	for _, sub := range subs {
		if err := st.EnqueueConfirm(ctx, sub.ID); err != nil {
			t.Fatal(err)
		}
	}
	id := func(sub model.Subscriber) int64 {
		return int64(x.count("SELECT id FROM upsera.subscriber_deliveries WHERE subscriber_id = $1", sub.ID))
	}
	old, deferred, sent, fresh, silent := id(subs[0]), id(subs[1]), id(subs[2]), id(subs[3]), id(subs[4])
	x.exec("UPDATE upsera.subscriber_deliveries SET created_at = now() - interval '25 hours' WHERE id = ANY($1)", []int64{old, deferred, sent, silent})
	x.exec("UPDATE upsera.subscriber_deliveries SET state = 'sent' WHERE id = $1", sent)
	if err := st.DeferDelivery(ctx, deferred, "SMTP is not configured on this instance", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkDeliveryFailed(ctx, old, "authentication failed", 10, time.Minute); err != nil {
		t.Fatal(err)
	}

	n, err := st.ExpireDeliveries(ctx, time.Now().Add(-24*time.Hour))
	if err != nil || n != 3 {
		t.Fatalf("expired %d, %v; want the three old pending deliveries", n, err)
	}
	for _, expired := range []int64{old, deferred, silent} {
		if x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE id = $1 AND state = 'failed' AND last_error LIKE '%expired%'", expired) != 1 {
			t.Errorf("delivery %d should be failed with an expiry reason", expired)
		}
	}
	if x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE id = $1 AND state = 'sent'", sent) != 1 {
		t.Error("a delivery that was already sent was touched")
	}
	if x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE id = $1 AND state = 'pending'", fresh) != 1 {
		t.Error("a recent delivery was expired")
	}
	// The reason a delivery kept failing is kept, after the expiry note: it
	// is the only diagnostic stored for it.
	for id, cause := range map[int64]string{old: "authentication failed", deferred: "SMTP is not configured on this instance"} {
		if x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE id = $1 AND last_error = $2", id, "expired: "+cause) != 1 {
			t.Errorf("delivery %d lost its failure reason %q on expiry", id, cause)
		}
	}
	// One that never failed has no reason to keep: no dangling "expired: ".
	if x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE id = $1 AND last_error = 'expired: not delivered in time'", silent) != 1 {
		t.Error("a delivery that never failed should say it was simply not delivered in time")
	}
	// Expired deliveries are no longer claimed.
	claimed, err := st.ClaimDeliveries(ctx, time.Now().Add(time.Hour), 10, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].ID != fresh {
		t.Fatalf("claimed %+v %v; want only the fresh delivery", claimed, err)
	}
	if n, err := st.ExpireDeliveries(ctx, time.Now().Add(-24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("second run expired %d, %v", n, err)
	}
}

func TestStatusSubscribersStore(t *testing.T) {
	x := newSX(t)
	ctx, st := x.ctx, x.st
	m := x.monitor(x.team.ID, "web")
	p1, p2 := x.page("subs-one", true, m), x.page("subs-two", true, m)

	t.Run("create is idempotent case-insensitively", func(t *testing.T) {
		s, created, err := st.CreateSubscriber(ctx, model.Subscriber{PageID: p1.ID, Kind: model.SubscriberEmail, Target: "Alice@Example.com"})
		if err != nil || !created || s.ID == 0 || s.Confirmed || s.Target != "Alice@Example.com" || s.CreatedAt.IsZero() {
			t.Fatalf("create = %+v %v %v", s, created, err)
		}
		again, created, err := st.CreateSubscriber(ctx, model.Subscriber{PageID: p1.ID, Kind: model.SubscriberEmail, Target: "alice@example.COM", Confirmed: true})
		if err != nil || created || again.ID != s.ID || again.Target != "Alice@Example.com" || again.Confirmed {
			t.Fatalf("second create = %+v %v %v", again, created, err)
		}
		// Different page and different kind are distinct subscriptions.
		o1, created1, err1 := st.CreateSubscriber(ctx, model.Subscriber{PageID: p2.ID, Kind: model.SubscriberEmail, Target: "alice@example.com"})
		o2, created2, err2 := st.CreateSubscriber(ctx, model.Subscriber{PageID: p1.ID, Kind: model.SubscriberWebhook, Target: "alice@example.com", Confirmed: true})
		if err1 != nil || err2 != nil || !created1 || !created2 || o1.ID == s.ID || o2.ID == s.ID || !o2.Confirmed {
			t.Fatalf("distinct: %+v %v %v / %+v %v %v", o1, created1, err1, o2, created2, err2)
		}
		if _, _, err := st.CreateSubscriber(ctx, model.Subscriber{PageID: 999999, Kind: model.SubscriberEmail, Target: "x@example.com"}); err == nil {
			t.Fatal("subscriber on a missing page must fail")
		}
	})

	t.Run("get and confirm", func(t *testing.T) {
		s := x.subscriber(p1.ID, model.SubscriberEmail, "bob@example.com", false)
		got, err := st.GetSubscriber(ctx, s.ID)
		if err != nil || got.ID != s.ID || got.PageID != p1.ID || got.Kind != model.SubscriberEmail || got.Confirmed {
			t.Fatalf("get = %+v %v", got, err)
		}
		if err := st.ConfirmSubscriber(ctx, s.ID); err != nil {
			t.Fatal(err)
		}
		if got, _ := st.GetSubscriber(ctx, s.ID); !got.Confirmed {
			t.Fatal("not confirmed")
		}
		if err := st.ConfirmSubscriber(ctx, s.ID); err != nil {
			t.Fatalf("confirming twice: %v", err)
		}
		_, err = st.GetSubscriber(ctx, 999999)
		wantErr(t, err, store.ErrNotFound)
		wantErr(t, st.ConfirmSubscriber(ctx, 999999), store.ErrNotFound)
	})

	t.Run("delete is page scoped unless the page id is 0", func(t *testing.T) {
		s := x.subscriber(p1.ID, model.SubscriberEmail, "carol@example.com", true)
		wantErr(t, st.DeleteSubscriber(ctx, p2.ID, s.ID), store.ErrNotFound)
		if _, err := st.GetSubscriber(ctx, s.ID); err != nil {
			t.Fatalf("subscriber deleted through the wrong page: %v", err)
		}
		if err := st.DeleteSubscriber(ctx, p1.ID, s.ID); err != nil {
			t.Fatal(err)
		}
		wantErr(t, st.DeleteSubscriber(ctx, p1.ID, s.ID), store.ErrNotFound)
		s = x.subscriber(p2.ID, model.SubscriberEmail, "dave@example.com", true)
		if err := st.DeleteSubscriber(ctx, 0, s.ID); err != nil {
			t.Fatalf("page 0 delete: %v", err)
		}
		wantErr(t, st.DeleteSubscriber(ctx, 0, s.ID), store.ErrNotFound)
	})

	t.Run("deleting a subscriber drops its deliveries", func(t *testing.T) {
		s := x.subscriber(p1.ID, model.SubscriberEmail, "erin@example.com", false)
		if err := st.EnqueueConfirm(ctx, s.ID); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteSubscriber(ctx, 0, s.ID); err != nil {
			t.Fatal(err)
		}
		if n := x.count("SELECT count(*) FROM upsera.subscriber_deliveries WHERE subscriber_id = $1", s.ID); n != 0 {
			t.Fatalf("deliveries left: %d", n)
		}
	})

	t.Run("list is per page, oldest first", func(t *testing.T) {
		p3 := x.page("subs-three", true, m)
		first := x.subscriber(p3.ID, model.SubscriberEmail, "z@example.com", true)
		second := x.subscriber(p3.ID, model.SubscriberWebhook, "https://hooks.example.com/a", true)
		got, err := st.ListSubscribers(ctx, p3.ID)
		if err != nil || len(got) != 2 || got[0].ID != first.ID || got[1].ID != second.ID {
			t.Fatalf("list = %+v %v", got, err)
		}
		none, err := st.ListSubscribers(ctx, 999999)
		if err != nil || len(none) != 0 {
			t.Fatalf("empty list = %+v %v", none, err)
		}
	})

	t.Run("purge removes only old unconfirmed subscribers", func(t *testing.T) {
		p := x.page("subs-purge", true, m)
		oldUnconfirmed := x.subscriber(p.ID, model.SubscriberEmail, "old@example.com", false)
		oldConfirmed := x.subscriber(p.ID, model.SubscriberEmail, "oldok@example.com", true)
		fresh := x.subscriber(p.ID, model.SubscriberEmail, "fresh@example.com", false)
		x.exec("UPDATE upsera.subscribers SET created_at = now() - interval '3 days' WHERE id = ANY($1)", []int64{oldUnconfirmed.ID, oldConfirmed.ID})
		n, err := st.PurgeUnconfirmedSubscribers(ctx, time.Now().Add(-24*time.Hour))
		if err != nil || n != 1 {
			t.Fatalf("purged %d, %v", n, err)
		}
		_, err = st.GetSubscriber(ctx, oldUnconfirmed.ID)
		wantErr(t, err, store.ErrNotFound)
		for _, id := range []int64{oldConfirmed.ID, fresh.ID} {
			if _, err := st.GetSubscriber(ctx, id); err != nil {
				t.Fatalf("subscriber %d was purged: %v", id, err)
			}
		}
	})
}

func TestStatusSMTPSettings(t *testing.T) {
	x := newSX(t)
	ctx, st := x.ctx, x.st
	raw := func() *string {
		var s *string
		if err := x.conn.QueryRow(ctx, "SELECT smtp FROM upsera.instance_settings WHERE id = 1").Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	if got, err := st.GetSMTPSettings(ctx); err != nil || got != nil {
		t.Fatalf("unset = %+v, %v", got, err)
	}
	cfg := model.SMTPSettings{Host: "smtp.example.com", Port: 587, Username: "mailer", Password: "s3cret-pass", From: "status@example.com", TLS: "starttls"}
	if err := st.SetSMTPSettings(ctx, &cfg); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSMTPSettings(ctx)
	if err != nil || got == nil || *got != cfg {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	stored := raw()
	if stored == nil || !strings.HasPrefix(*stored, "v1:") || strings.Contains(*stored, "s3cret-pass") || strings.Contains(*stored, "smtp.example.com") {
		t.Fatalf("smtp must be stored as ciphertext, got %v", stored)
	}

	t.Run("an undecryptable value is an error", func(t *testing.T) {
		st2, err := store.Open(ctx, x.url, 2)
		if err != nil {
			t.Fatal(err)
		}
		defer st2.Close()
		box, err := secret.New("a-completely-different-secret-0123456789")
		if err != nil {
			t.Fatal(err)
		}
		st2.SetSecretBox(box)
		if got, err := st2.GetSMTPSettings(ctx); err == nil || got != nil {
			t.Fatalf("different box: got %+v, err %v", got, err)
		}
		st3, err := store.Open(ctx, x.url, 2)
		if err != nil {
			t.Fatal(err)
		}
		defer st3.Close()
		if got, err := st3.GetSMTPSettings(ctx); err == nil || got != nil {
			t.Fatalf("no box: got %+v, err %v", got, err)
		}
		if err := st3.SetSMTPSettings(ctx, &cfg); err == nil {
			t.Fatal("storing without a secret box must fail")
		}
	})

	t.Run("nil clears", func(t *testing.T) {
		if err := st.SetSMTPSettings(ctx, nil); err != nil {
			t.Fatal(err)
		}
		if got, err := st.GetSMTPSettings(ctx); err != nil || got != nil {
			t.Fatalf("after clear = %+v, %v", got, err)
		}
		if raw() != nil {
			t.Fatal("column must be NULL after clearing")
		}
		// Clearing something that is not set is fine.
		if err := st.SetSMTPSettings(ctx, nil); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("update overwrites", func(t *testing.T) {
		first, second := cfg, cfg
		second.Host, second.Password = "smtp2.example.com", ""
		for _, c := range []model.SMTPSettings{first, second} {
			if err := st.SetSMTPSettings(ctx, &c); err != nil {
				t.Fatal(err)
			}
			if got, err := st.GetSMTPSettings(ctx); err != nil || got == nil || *got != c {
				t.Fatalf("got %+v, want %+v (%v)", got, c, err)
			}
		}
	})
}

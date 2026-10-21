package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davidsugianto/upsera/internal/model"
)

var errNoBox = errors.New("store: secret box not configured")

// ---- Status pages ----

const pageCols = `id, team_id, slug, title, description, logo_url, theme, custom_domain, published, created_at, updated_at`

func scanPage(row pgx.Row) (model.StatusPage, error) {
	var p model.StatusPage
	err := row.Scan(&p.ID, &p.TeamID, &p.Slug, &p.Title, &p.Description, &p.LogoURL, &p.Theme,
		&p.CustomDomain, &p.Published, &p.CreatedAt, &p.UpdatedAt)
	return p, mapErr(err)
}

// normalizeDomain lower-cases a custom domain; "" means none.
func normalizeDomain(d *string) *string {
	if d == nil {
		return nil
	}
	v := strings.ToLower(strings.TrimSpace(*d))
	if v == "" {
		return nil
	}
	return &v
}

func loadItems(ctx context.Context, q querier, pageIDs []int64) (map[int64][]model.StatusPageItem, error) {
	rows, err := q.Query(ctx, `
		SELECT id, page_id, section, monitor_id, display_name, position FROM upsera.status_page_items
		WHERE page_id = ANY($1) ORDER BY page_id, position, id`, pageIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][]model.StatusPageItem)
	for rows.Next() {
		var it model.StatusPageItem
		var pageID int64
		if err := rows.Scan(&it.ID, &pageID, &it.Section, &it.MonitorID, &it.DisplayName, &it.Position); err != nil {
			return nil, err
		}
		out[pageID] = append(out[pageID], it)
	}
	return out, rows.Err()
}

func (s *Store) pagesWithItems(ctx context.Context, rows pgx.Rows, err error) ([]model.StatusPage, error) {
	if err != nil {
		return nil, err
	}
	pages, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.StatusPage, error) { return scanPage(r) })
	if err != nil || len(pages) == 0 {
		return pages, err
	}
	ids := make([]int64, len(pages))
	for i, p := range pages {
		ids[i] = p.ID
	}
	items, err := loadItems(ctx, s.pool, ids)
	if err != nil {
		return nil, err
	}
	for i := range pages {
		pages[i].Items = items[pages[i].ID]
		if pages[i].Items == nil {
			pages[i].Items = []model.StatusPageItem{}
		}
	}
	return pages, nil
}

// insertItems adds items to pageID. Every monitor must belong to teamID,
// otherwise ErrNotFound is returned; the same monitor twice is ErrConflict.
func insertItems(ctx context.Context, tx pgx.Tx, teamID, pageID int64, items []model.StatusPageItem) error {
	for _, it := range items {
		tag, err := tx.Exec(ctx, `
			INSERT INTO upsera.status_page_items (page_id, section, monitor_id, display_name, position)
			SELECT $1, $2, m.id, $4, $5 FROM upsera.monitors m WHERE m.id = $3 AND m.team_id = $6`,
			pageID, it.Section, it.MonitorID, it.DisplayName, it.Position, teamID)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
	}
	return nil
}

// CreateStatusPage inserts p and its items. A duplicate slug or custom
// domain, or a monitor listed twice, is ErrConflict; a monitor of another
// team is ErrNotFound.
func (s *Store) CreateStatusPage(ctx context.Context, p model.StatusPage) (model.StatusPage, error) {
	var out model.StatusPage
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = scanPage(tx.QueryRow(ctx, `
			INSERT INTO upsera.status_pages (team_id, slug, title, description, logo_url, theme, custom_domain, published)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+pageCols,
			p.TeamID, p.Slug, p.Title, p.Description, p.LogoURL, p.Theme, normalizeDomain(p.CustomDomain), p.Published))
		if err != nil {
			return err
		}
		return insertItems(ctx, tx, p.TeamID, out.ID, p.Items)
	})
	if err != nil {
		return model.StatusPage{}, err
	}
	return s.GetStatusPage(ctx, out.TeamID, out.ID)
}

// UpdateStatusPage replaces the settings and the items of page p.ID in
// p.TeamID. Errors as for CreateStatusPage.
func (s *Store) UpdateStatusPage(ctx context.Context, p model.StatusPage) (model.StatusPage, error) {
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		_, err := scanPage(tx.QueryRow(ctx, `
			UPDATE upsera.status_pages SET slug = $3, title = $4, description = $5, logo_url = $6, theme = $7,
				custom_domain = $8, published = $9, updated_at = now()
			WHERE team_id = $1 AND id = $2 RETURNING `+pageCols,
			p.TeamID, p.ID, p.Slug, p.Title, p.Description, p.LogoURL, p.Theme, normalizeDomain(p.CustomDomain), p.Published))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM upsera.status_page_items WHERE page_id = $1", p.ID); err != nil {
			return err
		}
		return insertItems(ctx, tx, p.TeamID, p.ID, p.Items)
	})
	if err != nil {
		return model.StatusPage{}, err
	}
	return s.GetStatusPage(ctx, p.TeamID, p.ID)
}

// GetStatusPage loads one page of teamID with its items.
func (s *Store) GetStatusPage(ctx context.Context, teamID, id int64) (model.StatusPage, error) {
	rows, qerr := s.pool.Query(ctx, "SELECT "+pageCols+" FROM upsera.status_pages WHERE team_id = $1 AND id = $2", teamID, id)
	list, err := s.pagesWithItems(ctx, rows, qerr)
	if err != nil {
		return model.StatusPage{}, err
	}
	if len(list) == 0 {
		return model.StatusPage{}, ErrNotFound
	}
	return list[0], nil
}

// ListStatusPages returns teamID's pages with items, by id.
func (s *Store) ListStatusPages(ctx context.Context, teamID int64) ([]model.StatusPage, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+pageCols+" FROM upsera.status_pages WHERE team_id = $1 ORDER BY id", teamID)
	return s.pagesWithItems(ctx, rows, err)
}

// DeleteStatusPage removes a page of teamID with its items, incidents and
// subscribers.
func (s *Store) DeleteStatusPage(ctx context.Context, teamID, id int64) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM upsera.status_pages WHERE team_id = $1 AND id = $2", teamID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetPublishedPageBySlug loads a published page (with items) by slug; an
// unpublished or unknown slug is ErrNotFound.
func (s *Store) GetPublishedPageBySlug(ctx context.Context, slug string) (model.StatusPage, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+pageCols+" FROM upsera.status_pages WHERE slug = $1 AND published", slug)
	list, err := s.pagesWithItems(ctx, rows, err)
	if err != nil {
		return model.StatusPage{}, err
	}
	if len(list) == 0 {
		return model.StatusPage{}, ErrNotFound
	}
	return list[0], nil
}

// ListPublishedCustomDomains maps every published page's custom domain to
// its slug.
func (s *Store) ListPublishedCustomDomains(ctx context.Context) ([]model.PageDomain, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT custom_domain, slug FROM upsera.status_pages WHERE published AND custom_domain IS NOT NULL ORDER BY id")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.PageDomain, error) {
		var d model.PageDomain
		err := r.Scan(&d.Domain, &d.Slug)
		return d, err
	})
}

// StatusPageUptime returns, per monitor, the per-day uptime for calendar
// days fromDay through today (ascending). Days before today come from the
// uptime_daily rollup; today is counted from raw heartbeats since
// todayStart (the start of today in the server's time zone). Monitors
// without data are absent; maintenance checks are not counted and PENDING
// and DEGRADED count as up, as in the dashboard.
func (s *Store) StatusPageUptime(ctx context.Context, monitorIDs []int64, fromDay, todayStart time.Time) (map[int64][]model.UptimeDay, error) {
	out := make(map[int64][]model.UptimeDay)
	rows, err := s.pool.Query(ctx, `
		SELECT monitor_id, day, checks, up FROM upsera.uptime_daily
		WHERE monitor_id = ANY($1) AND day >= $2::date AND day < $3::date ORDER BY monitor_id, day`,
		monitorIDs, fromDay.Format(time.DateOnly), todayStart.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d model.UptimeDay
		if err := rows.Scan(&d.MonitorID, &d.Day, &d.Checks, &d.Up); err != nil {
			rows.Close()
			return nil, err
		}
		out[d.MonitorID] = append(out[d.MonitorID], d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	today, err := s.pool.Query(ctx, `
		SELECT monitor_id, count(*) FILTER (WHERE status <> 3), count(*) FILTER (WHERE status IN (1, 2, 4))
		FROM upsera.heartbeats WHERE monitor_id = ANY($1) AND time >= $2 GROUP BY monitor_id`, monitorIDs, todayStart)
	if err != nil {
		return nil, err
	}
	defer today.Close()
	for today.Next() {
		d := model.UptimeDay{Day: todayStart}
		if err := today.Scan(&d.MonitorID, &d.Checks, &d.Up); err != nil {
			return nil, err
		}
		if d.Checks > 0 {
			out[d.MonitorID] = append(out[d.MonitorID], d)
		}
	}
	return out, today.Err()
}

// ---- Incidents ----

const incidentCols = `id, page_id, title, severity, status, draft, auto_alert_id::text, started_at, resolved_at, published_at, created_at, updated_at`

func scanIncident(row pgx.Row) (model.Incident, error) {
	var i model.Incident
	err := row.Scan(&i.ID, &i.PageID, &i.Title, &i.Severity, &i.Status, &i.Draft, &i.AutoAlertID,
		&i.StartedAt, &i.ResolvedAt, &i.PublishedAt, &i.CreatedAt, &i.UpdatedAt)
	return i, mapErr(err)
}

func withUpdates(ctx context.Context, q querier, list []model.Incident) ([]model.Incident, error) {
	if len(list) == 0 {
		return list, nil
	}
	ids := make([]int64, len(list))
	idx := make(map[int64]int, len(list))
	for n, i := range list {
		ids[n] = i.ID
		idx[i.ID] = n
		list[n].Updates = []model.IncidentUpdate{}
	}
	rows, err := q.Query(ctx, `
		SELECT id, incident_id, status, body, created_at FROM upsera.incident_updates
		WHERE incident_id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u model.IncidentUpdate
		if err := rows.Scan(&u.ID, &u.IncidentID, &u.Status, &u.Body, &u.CreatedAt); err != nil {
			return nil, err
		}
		n := idx[u.IncidentID]
		list[n].Updates = append(list[n].Updates, u)
	}
	return list, rows.Err()
}

func collectIncidents(ctx context.Context, q querier, rows pgx.Rows, err error) ([]model.Incident, error) {
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Incident, error) { return scanIncident(r) })
	if err != nil {
		return nil, err
	}
	return withUpdates(ctx, q, list)
}

func getIncident(ctx context.Context, q querier, where string, args ...any) (model.Incident, error) {
	rows, err := q.Query(ctx, "SELECT "+incidentCols+" FROM upsera.incidents WHERE NOT dismissed AND ("+where+")", args...)
	list, err := collectIncidents(ctx, q, rows, err)
	if err != nil {
		return model.Incident{}, err
	}
	if len(list) == 0 {
		return model.Incident{}, ErrNotFound
	}
	return list[0], nil
}

// enqueueIncident queues an incident notice for every confirmed subscriber
// of a published page. updateID nil announces the incident as a whole.
func enqueueIncident(ctx context.Context, tx pgx.Tx, pageID, incidentID int64, updateID *int64) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO upsera.subscriber_deliveries (subscriber_id, kind, incident_id, update_id)
		SELECT s.id, 'incident', $2, $3 FROM upsera.subscribers s
		WHERE s.page_id = $1 AND s.confirmed
		  AND EXISTS (SELECT 1 FROM upsera.status_pages p WHERE p.id = $1 AND p.published)`,
		pageID, incidentID, updateID)
	return err
}

// CreateIncident inserts in on page in.PageID with an optional first
// timeline entry (firstBody). A published incident (Draft false) is queued
// for the page's subscribers. StartedAt defaults to now. A second incident
// for the same page and AutoAlertID is ErrConflict.
func (s *Store) CreateIncident(ctx context.Context, in model.Incident, firstBody string) (model.Incident, error) {
	var out model.Incident
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		started := in.StartedAt
		if started.IsZero() {
			started = time.Now()
		}
		var resolved *time.Time
		if in.Status == model.IncidentResolved {
			resolved = &started
			if in.ResolvedAt != nil {
				resolved = in.ResolvedAt
			}
		}
		var err error
		out, err = scanIncident(tx.QueryRow(ctx, `
			INSERT INTO upsera.incidents (page_id, title, severity, status, draft, auto_alert_id, started_at, resolved_at, published_at)
			VALUES ($1, $2, $3, $4, $5, $6::uuid, $7, $8, CASE WHEN $5 THEN NULL ELSE now() END)
			RETURNING `+incidentCols,
			in.PageID, in.Title, in.Severity, in.Status, in.Draft, in.AutoAlertID, started, resolved))
		if err != nil {
			return err
		}
		var updateID *int64
		if firstBody != "" {
			var id int64
			if err := tx.QueryRow(ctx, `
				INSERT INTO upsera.incident_updates (incident_id, status, body) VALUES ($1, $2, $3) RETURNING id`,
				out.ID, out.Status, firstBody).Scan(&id); err != nil {
				return err
			}
			updateID = &id
		}
		if !out.Draft {
			return enqueueIncident(ctx, tx, out.PageID, out.ID, updateID)
		}
		return nil
	})
	if err != nil {
		return model.Incident{}, err
	}
	return getIncident(ctx, s.pool, "id = $1", out.ID)
}

// GetIncident loads incident id of page pageID with its timeline.
func (s *Store) GetIncident(ctx context.Context, pageID, id int64) (model.Incident, error) {
	return getIncident(ctx, s.pool, "page_id = $1 AND id = $2", pageID, id)
}

// ListIncidents returns pageID's incidents, newest first, with timelines.
// Drafts are included only when includeDrafts is set.
func (s *Store) ListIncidents(ctx context.Context, pageID int64, includeDrafts bool, limit int) ([]model.Incident, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+incidentCols+` FROM upsera.incidents
		WHERE page_id = $1 AND NOT dismissed AND ($2 OR NOT draft) ORDER BY started_at DESC, id DESC LIMIT $3`, pageID, includeDrafts, limit)
	return collectIncidents(ctx, s.pool, rows, err)
}

// UpdateIncident changes the title and severity of an incident.
func (s *Store) UpdateIncident(ctx context.Context, pageID, id int64, title string, severity model.Severity) (model.Incident, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE upsera.incidents SET title = $3, severity = $4, updated_at = now() WHERE page_id = $1 AND id = $2 AND NOT dismissed`,
		pageID, id, title, severity)
	if err != nil {
		return model.Incident{}, err
	}
	if tag.RowsAffected() == 0 {
		return model.Incident{}, ErrNotFound
	}
	return s.GetIncident(ctx, pageID, id)
}

// AddIncidentUpdate appends a timeline entry and sets the incident's
// status; "resolved" stamps resolved_at, any other status clears it. On a
// published incident the update is queued for subscribers.
func (s *Store) AddIncidentUpdate(ctx context.Context, pageID, id int64, status model.IncidentStatus, body string) (model.Incident, error) {
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var draft bool
		err := tx.QueryRow(ctx, `
			UPDATE upsera.incidents SET status = $3,
				resolved_at = CASE WHEN $3 = 'resolved' THEN coalesce(resolved_at, now()) ELSE NULL END, updated_at = now()
			WHERE page_id = $1 AND id = $2 AND NOT dismissed RETURNING draft`, pageID, id, status).Scan(&draft)
		if err != nil {
			return mapErr(err)
		}
		var updateID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO upsera.incident_updates (incident_id, status, body) VALUES ($1, $2, $3) RETURNING id`,
			id, status, body).Scan(&updateID); err != nil {
			return err
		}
		if !draft {
			return enqueueIncident(ctx, tx, pageID, id, &updateID)
		}
		return nil
	})
	if err != nil {
		return model.Incident{}, err
	}
	return s.GetIncident(ctx, pageID, id)
}

// PublishIncident turns a draft into a public incident and queues it for
// the page's subscribers. Publishing an already published incident does
// nothing.
func (s *Store) PublishIncident(ctx context.Context, pageID, id int64) (model.Incident, error) {
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE upsera.incidents SET draft = false, published_at = now(), updated_at = now()
			WHERE page_id = $1 AND id = $2 AND draft AND NOT dismissed`, pageID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM upsera.incidents WHERE page_id = $1 AND id = $2 AND NOT dismissed)", pageID, id).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrNotFound
			}
			return nil
		}
		return enqueueIncident(ctx, tx, pageID, id, nil)
	})
	if err != nil {
		return model.Incident{}, err
	}
	return s.GetIncident(ctx, pageID, id)
}

// DeleteIncident removes an incident with its timeline. An auto-drafted
// incident whose alert is still open is only dismissed (hidden for good, its
// queued notices dropped), so the worker does not draft the same outage
// again on its next pass; once the alert has resolved there is nothing left
// to re-draft and the row is really deleted.
func (s *Store) DeleteIncident(ctx context.Context, pageID, id int64) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE upsera.incidents i SET dismissed = true, updated_at = now()
			WHERE i.page_id = $1 AND i.id = $2 AND NOT i.dismissed AND i.auto_alert_id IS NOT NULL
			  AND EXISTS (SELECT 1 FROM upsera.alerts a WHERE a.id = i.auto_alert_id AND a.resolved_at IS NULL)`, pageID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			_, err := tx.Exec(ctx, "DELETE FROM upsera.subscriber_deliveries WHERE incident_id = $1 AND state = 'pending'", id)
			return err
		}
		tag, err = tx.Exec(ctx, "DELETE FROM upsera.incidents WHERE page_id = $1 AND id = $2 AND NOT dismissed", pageID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// AutoDraftCandidates returns open, unsuppressed alerts on monitors listed
// on published pages that started at or before startedBefore and have no
// incident on that page yet.
func (s *Store) AutoDraftCandidates(ctx context.Context, startedBefore time.Time) ([]model.DraftCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, it.monitor_id, it.display_name, a.id::text, a.incident_start
		FROM upsera.alerts a
		JOIN upsera.status_page_items it ON it.monitor_id = a.monitor_id
		JOIN upsera.status_pages p ON p.id = it.page_id AND p.published
		WHERE a.resolved_at IS NULL AND NOT a.suppressed AND a.incident_start <= $1
		  AND NOT EXISTS (SELECT 1 FROM upsera.incidents i WHERE i.page_id = p.id AND i.auto_alert_id = a.id)
		ORDER BY a.incident_start, p.id`, startedBefore)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.DraftCandidate, error) {
		var c model.DraftCandidate
		err := r.Scan(&c.PageID, &c.MonitorID, &c.DisplayName, &c.AlertID, &c.StartedAt)
		return c, err
	})
}

// ResolveAutoIncidents resolves every unresolved auto-drafted incident
// whose alert has resolved: it appends a "resolved" timeline entry with
// body, stamps resolved_at from the alert, and queues the update for
// subscribers if the incident is published. It returns how many it
// resolved.
func (s *Store) ResolveAutoIncidents(ctx context.Context, body string) (int, error) {
	n := 0
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT i.id, i.page_id, i.draft, a.resolved_at FROM upsera.incidents i
			JOIN upsera.alerts a ON a.id = i.auto_alert_id
			WHERE i.resolved_at IS NULL AND NOT i.dismissed AND a.resolved_at IS NOT NULL FOR UPDATE OF i`)
		if err != nil {
			return err
		}
		type row struct {
			id, pageID int64
			draft      bool
			at         time.Time
		}
		var todo []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.pageID, &r.draft, &r.at); err != nil {
				rows.Close()
				return err
			}
			todo = append(todo, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, r := range todo {
			if _, err := tx.Exec(ctx, `
				UPDATE upsera.incidents SET status = 'resolved', resolved_at = $2, updated_at = now() WHERE id = $1`, r.id, r.at); err != nil {
				return err
			}
			var updateID int64
			if err := tx.QueryRow(ctx, `
				INSERT INTO upsera.incident_updates (incident_id, status, body) VALUES ($1, 'resolved', $2) RETURNING id`,
				r.id, body).Scan(&updateID); err != nil {
				return err
			}
			if !r.draft {
				if err := enqueueIncident(ctx, tx, r.pageID, r.id, &updateID); err != nil {
					return err
				}
			}
			n++
		}
		return nil
	})
	return n, err
}

// ---- Subscribers and the delivery outbox ----

const subscriberCols = `id, page_id, kind, target, confirmed, created_at`

func scanSubscriber(row pgx.Row) (model.Subscriber, error) {
	var sub model.Subscriber
	err := row.Scan(&sub.ID, &sub.PageID, &sub.Kind, &sub.Target, &sub.Confirmed, &sub.CreatedAt)
	return sub, mapErr(err)
}

// CreateSubscriber adds sub (Confirmed as given). If the same target is
// already subscribed to the page, the existing row is returned with
// created=false and nothing changes.
func (s *Store) CreateSubscriber(ctx context.Context, sub model.Subscriber) (model.Subscriber, bool, error) {
	out, err := scanSubscriber(s.pool.QueryRow(ctx, `
		INSERT INTO upsera.subscribers (page_id, kind, target, confirmed) VALUES ($1, $2, $3, $4)
		ON CONFLICT (page_id, kind, lower(target)) DO NOTHING RETURNING `+subscriberCols,
		sub.PageID, sub.Kind, sub.Target, sub.Confirmed))
	if err == nil {
		return out, true, nil
	}
	if err != ErrNotFound {
		return model.Subscriber{}, false, err
	}
	out, err = scanSubscriber(s.pool.QueryRow(ctx, `
		SELECT `+subscriberCols+` FROM upsera.subscribers WHERE page_id = $1 AND kind = $2 AND lower(target) = lower($3)`,
		sub.PageID, sub.Kind, sub.Target))
	return out, false, err
}

// GetSubscriber loads a subscriber by id (public confirm/unsubscribe flows).
func (s *Store) GetSubscriber(ctx context.Context, id int64) (model.Subscriber, error) {
	return scanSubscriber(s.pool.QueryRow(ctx, "SELECT "+subscriberCols+" FROM upsera.subscribers WHERE id = $1", id))
}

// ConfirmSubscriber marks a subscriber confirmed.
func (s *Store) ConfirmSubscriber(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, "UPDATE upsera.subscribers SET confirmed = true WHERE id = $1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSubscriber removes a subscriber by id. pageID 0 matches any page
// (the public unsubscribe link); otherwise the subscriber must be on pageID.
func (s *Store) DeleteSubscriber(ctx context.Context, pageID, id int64) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM upsera.subscribers WHERE id = $2 AND ($1::bigint = 0 OR page_id = $1)", pageID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListSubscribers returns pageID's subscribers, oldest first.
func (s *Store) ListSubscribers(ctx context.Context, pageID int64) ([]model.Subscriber, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+subscriberCols+" FROM upsera.subscribers WHERE page_id = $1 ORDER BY id", pageID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Subscriber, error) { return scanSubscriber(r) })
}

// PurgeUnconfirmedSubscribers deletes subscribers that never confirmed and
// were created before the cutoff.
func (s *Store) PurgeUnconfirmedSubscribers(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM upsera.subscribers WHERE NOT confirmed AND created_at < $1", before)
	return tag.RowsAffected(), err
}

// EnqueueConfirm queues a confirmation mail for an unconfirmed subscriber.
func (s *Store) EnqueueConfirm(ctx context.Context, subscriberID int64) error {
	_, err := s.pool.Exec(ctx,
		"INSERT INTO upsera.subscriber_deliveries (subscriber_id, kind) VALUES ($1, 'confirm')", subscriberID)
	return err
}

// CountConfirmDeliveries counts confirmation mails queued at or after since
// for pageID's subscribers (pageID 0 = every page), however they ended.
func (s *Store) CountConfirmDeliveries(ctx context.Context, since time.Time, pageID int64) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM upsera.subscriber_deliveries d JOIN upsera.subscribers s ON s.id = d.subscriber_id
		WHERE d.kind = 'confirm' AND d.created_at >= $1 AND ($2::bigint = 0 OR s.page_id = $2)`, since, pageID).Scan(&n)
	return n, err
}

// ClaimDeliveries returns up to limit pending deliveries that are due at
// now, and leases them (pushes next_attempt_at out by lease) so another
// worker or a restart does not send them twice at once. Report each with
// MarkDeliverySent or MarkDeliveryFailed.
func (s *Store) ClaimDeliveries(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]model.Delivery, error) {
	var ids []int64
	rows, err := s.pool.Query(ctx, `
		UPDATE upsera.subscriber_deliveries SET next_attempt_at = $2
		WHERE id IN (SELECT id FROM upsera.subscriber_deliveries
			WHERE state = 'pending' AND next_attempt_at <= $1 ORDER BY id LIMIT $3 FOR UPDATE SKIP LOCKED)
		RETURNING id`, now, now.Add(lease), limit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Sort(ids) // RETURNING order is arbitrary; send oldest first
	out := make([]model.Delivery, 0, len(ids))
	for _, id := range ids {
		var d model.Delivery
		var incidentID *int64
		err := s.pool.QueryRow(ctx, `
			SELECT d.id, d.kind, d.attempts, d.incident_id, d.update_id,
				s.id, s.page_id, s.kind, s.target, s.confirmed, s.created_at,
				p.id, p.team_id, p.slug, p.title, p.description, p.logo_url, p.theme, p.custom_domain, p.published, p.created_at, p.updated_at
			FROM upsera.subscriber_deliveries d
			JOIN upsera.subscribers s ON s.id = d.subscriber_id
			JOIN upsera.status_pages p ON p.id = s.page_id
			WHERE d.id = $1`, id).Scan(
			&d.ID, &d.Kind, &d.Attempts, &incidentID, &d.UpdateID,
			&d.Subscriber.ID, &d.Subscriber.PageID, &d.Subscriber.Kind, &d.Subscriber.Target, &d.Subscriber.Confirmed, &d.Subscriber.CreatedAt,
			&d.Page.ID, &d.Page.TeamID, &d.Page.Slug, &d.Page.Title, &d.Page.Description, &d.Page.LogoURL, &d.Page.Theme,
			&d.Page.CustomDomain, &d.Page.Published, &d.Page.CreatedAt, &d.Page.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if incidentID != nil {
			inc, err := getIncident(ctx, s.pool, "id = $1", *incidentID)
			if err != nil {
				return nil, err
			}
			d.Incident = &inc
		}
		out = append(out, d)
	}
	return out, nil
}

// MarkDeliverySent records a successful send.
func (s *Store) MarkDeliverySent(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		"UPDATE upsera.subscriber_deliveries SET state = 'sent', sent_at = now(), attempts = attempts + 1, last_error = '' WHERE id = $1", id)
	return err
}

// MarkDeliveryFailed records a failed attempt: the delivery is retried
// after retryAfter until maxAttempts attempts were made, then marked failed.
func (s *Store) MarkDeliveryFailed(ctx context.Context, id int64, errMsg string, maxAttempts int, retryAfter time.Duration) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE upsera.subscriber_deliveries SET attempts = attempts + 1, last_error = left($2, 500),
			state = CASE WHEN attempts + 1 >= $3 THEN 'failed' ELSE 'pending' END,
			next_attempt_at = now() + make_interval(secs => $4)
		WHERE id = $1`, id, errMsg, maxAttempts, retryAfter.Seconds())
	return err
}

// DeferDelivery reschedules a pending delivery after retryAfter without
// using up one of its attempts, recording why. It is for delays that are not
// the notice's fault (the instance has no SMTP settings yet, the mail server
// is down), so that a later real failure still gets all its retries. A
// delivery that is no longer pending is left alone.
func (s *Store) DeferDelivery(ctx context.Context, id int64, errMsg string, retryAfter time.Duration) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE upsera.subscriber_deliveries SET last_error = left($2, 500), next_attempt_at = now() + make_interval(secs => $3)
		WHERE id = $1 AND state = 'pending'`, id, errMsg, retryAfter.Seconds())
	return err
}

// ExpireDeliveries marks every still-pending delivery created before the
// cutoff as failed ("expired: <the last reason it kept failing>"), whatever
// kept it waiting, and returns how many it dropped. A notice that is a day late (an outage of the mail
// server, SMTP configured long after the incident) is noise, not news.
func (s *Store) ExpireDeliveries(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE upsera.subscriber_deliveries SET state = 'failed', last_error = left('expired: ' || coalesce(nullif(last_error, ''), 'not delivered in time'), 500)
		WHERE state = 'pending' AND created_at < $1`, before)
	return tag.RowsAffected(), err
}

// PurgeDeliveries deletes finished (sent or failed) deliveries created
// before the cutoff.
func (s *Store) PurgeDeliveries(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM upsera.subscriber_deliveries WHERE state <> 'pending' AND created_at < $1", before)
	return tag.RowsAffected(), err
}

// ---- Instance SMTP settings ----

// GetSMTPSettings returns the instance mail server, or nil if none is set.
// A stored value that cannot be decrypted is an error.
func (s *Store) GetSMTPSettings(ctx context.Context) (*model.SMTPSettings, error) {
	var enc *string
	if err := s.pool.QueryRow(ctx, "SELECT smtp FROM upsera.instance_settings WHERE id = 1").Scan(&enc); err != nil {
		return nil, mapErr(err)
	}
	if enc == nil || *enc == "" {
		return nil, nil
	}
	if s.box == nil {
		return nil, errNoBox
	}
	plain, err := s.box.Decrypt(*enc)
	if err != nil {
		return nil, err
	}
	var cfg model.SMTPSettings
	if err := json.Unmarshal(plain, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SetSMTPSettings stores cfg encrypted; nil clears the setting.
func (s *Store) SetSMTPSettings(ctx context.Context, cfg *model.SMTPSettings) error {
	var enc *string
	if cfg != nil {
		raw, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		v, err := s.encryptConfig(raw)
		if err != nil {
			return err
		}
		enc = &v
	}
	_, err := s.pool.Exec(ctx, "UPDATE upsera.instance_settings SET smtp = $1, updated_at = now() WHERE id = 1", enc)
	return err
}

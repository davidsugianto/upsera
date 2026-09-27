package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/davidsugianto/upsera/internal/model"
)

// querier is satisfied by both the pool and a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// deleteRow runs a DELETE of one team-scoped row, mapping "no row" to
// ErrNotFound and a foreign-key violation (something still references
// it) to ErrInUse.
func (s *Store) deleteRow(ctx context.Context, sql string, teamID, id int64) error {
	tag, err := s.pool.Exec(ctx, sql, teamID, id)
	if isForeignKeyViolation(err) {
		return ErrInUse
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- Notification channels ----

const channelDecryptErr = "cannot decrypt config (APP_SECRET changed?)"

const channelCols = `id, team_id, type, name, config, is_default, created_at, updated_at`

func (s *Store) scanChannel(row pgx.Row) (model.Channel, error) {
	var c model.Channel
	var enc string
	if err := row.Scan(&c.ID, &c.TeamID, &c.Type, &c.Name, &enc, &c.IsDefault, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return c, mapErr(err)
	}
	if s.box == nil {
		c.DecryptErr = channelDecryptErr
		return c, nil
	}
	plain, err := s.box.Decrypt(enc)
	if err != nil {
		c.DecryptErr = channelDecryptErr
		return c, nil
	}
	c.Config = json.RawMessage(plain)
	return c, nil
}

func (s *Store) collectChannels(rows pgx.Rows, err error) ([]model.Channel, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Channel, error) { return s.scanChannel(r) })
}

func (s *Store) encryptConfig(cfg json.RawMessage) (string, error) {
	if s.box == nil {
		return "", errors.New("store: secret box not configured")
	}
	return s.box.Encrypt(cfg)
}

// CreateChannel inserts c with its config encrypted.
func (s *Store) CreateChannel(ctx context.Context, c model.Channel) (model.Channel, error) {
	enc, err := s.encryptConfig(c.Config)
	if err != nil {
		return model.Channel{}, err
	}
	return s.scanChannel(s.pool.QueryRow(ctx, `
		INSERT INTO upsera.notification_channels (team_id, type, name, config, is_default)
		VALUES ($1, $2, $3, $4, $5) RETURNING `+channelCols,
		c.TeamID, c.Type, c.Name, enc, c.IsDefault))
}

// UpdateChannel replaces the name, config and default flag of channel
// c.ID in c.TeamID. The type is immutable.
func (s *Store) UpdateChannel(ctx context.Context, c model.Channel) (model.Channel, error) {
	enc, err := s.encryptConfig(c.Config)
	if err != nil {
		return model.Channel{}, err
	}
	return s.scanChannel(s.pool.QueryRow(ctx, `
		UPDATE upsera.notification_channels SET name = $3, config = $4, is_default = $5, updated_at = now()
		WHERE team_id = $1 AND id = $2 RETURNING `+channelCols,
		c.TeamID, c.ID, c.Name, enc, c.IsDefault))
}

// GetChannel loads one channel of teamID.
func (s *Store) GetChannel(ctx context.Context, teamID, id int64) (model.Channel, error) {
	return s.scanChannel(s.pool.QueryRow(ctx,
		"SELECT "+channelCols+" FROM upsera.notification_channels WHERE team_id = $1 AND id = $2", teamID, id))
}

// ListChannels returns teamID's channels ordered by id. A channel whose
// config cannot be decrypted is returned with DecryptErr set.
func (s *Store) ListChannels(ctx context.Context, teamID int64) ([]model.Channel, error) {
	return s.collectChannels(s.pool.Query(ctx,
		"SELECT "+channelCols+" FROM upsera.notification_channels WHERE team_id = $1 ORDER BY id", teamID))
}

// ListAllChannels returns every channel on the instance, for the alerting
// engine's cache.
func (s *Store) ListAllChannels(ctx context.Context) ([]model.Channel, error) {
	return s.collectChannels(s.pool.Query(ctx, "SELECT "+channelCols+" FROM upsera.notification_channels ORDER BY id"))
}

// DeleteChannel removes a channel of teamID. It returns ErrInUse while an
// escalation policy still uses it.
func (s *Store) DeleteChannel(ctx context.Context, teamID, id int64) error {
	return s.deleteRow(ctx, "DELETE FROM upsera.notification_channels WHERE team_id = $1 AND id = $2", teamID, id)
}

// ---- Escalation policies ----

func insertSteps(ctx context.Context, tx pgx.Tx, policyID int64, steps []model.EscalationStep) error {
	var pos []int16
	var delay []int32
	var chans []int64
	for i, st := range steps {
		for _, c := range st.ChannelIDs {
			pos = append(pos, int16(i))
			delay = append(delay, int32(st.DelayS))
			chans = append(chans, c)
		}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO upsera.escalation_steps (policy_id, position, delay_s, channel_id)
		SELECT $1, s.position, s.delay_s, s.channel_id
		FROM unnest($2::smallint[], $3::integer[], $4::bigint[]) AS s(position, delay_s, channel_id)
		ON CONFLICT DO NOTHING`, policyID, pos, delay, chans)
	return err
}

// loadPolicies returns the policies matching cond (a condition on alias
// "p", placeholders from $1), with their steps.
func loadPolicies(ctx context.Context, q querier, cond string, args ...any) ([]model.EscalationPolicy, error) {
	rows, err := q.Query(ctx, `
		SELECT p.id, p.team_id, p.name, p.created_at, p.updated_at
		FROM upsera.escalation_policies p WHERE `+cond+` ORDER BY p.id`, args...)
	if err != nil {
		return nil, err
	}
	ps, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.EscalationPolicy, error) {
		var p model.EscalationPolicy
		err := r.Scan(&p.ID, &p.TeamID, &p.Name, &p.CreatedAt, &p.UpdatedAt)
		p.Steps = []model.EscalationStep{}
		return p, err
	})
	if err != nil || len(ps) == 0 {
		return ps, err
	}
	idx := make(map[int64]int, len(ps))
	for i, p := range ps {
		idx[p.ID] = i
	}
	rows, err = q.Query(ctx, `
		SELECT s.policy_id, s.position, s.delay_s, s.channel_id
		FROM upsera.escalation_steps s JOIN upsera.escalation_policies p ON p.id = s.policy_id
		WHERE `+cond+` ORDER BY s.policy_id, s.position, s.channel_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var policyID, channelID int64
		var pos int16
		var delay int32
		if err := rows.Scan(&policyID, &pos, &delay, &channelID); err != nil {
			return nil, err
		}
		i, ok := idx[policyID]
		if !ok {
			continue
		}
		p := &ps[i]
		for len(p.Steps) <= int(pos) {
			p.Steps = append(p.Steps, model.EscalationStep{ChannelIDs: []int64{}})
		}
		p.Steps[pos].DelayS = int(delay)
		p.Steps[pos].ChannelIDs = append(p.Steps[pos].ChannelIDs, channelID)
	}
	return ps, rows.Err()
}

func getPolicy(ctx context.Context, q querier, teamID, id int64) (model.EscalationPolicy, error) {
	ps, err := loadPolicies(ctx, q, "p.team_id = $1 AND p.id = $2", teamID, id)
	if err != nil {
		return model.EscalationPolicy{}, err
	}
	if len(ps) == 0 {
		return model.EscalationPolicy{}, ErrNotFound
	}
	return ps[0], nil
}

// CreatePolicy inserts p with its steps.
func (s *Store) CreatePolicy(ctx context.Context, p model.EscalationPolicy) (model.EscalationPolicy, error) {
	var out model.EscalationPolicy
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var id int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO upsera.escalation_policies (team_id, name) VALUES ($1, $2) RETURNING id`,
			p.TeamID, p.Name).Scan(&id); err != nil {
			return err
		}
		if err := insertSteps(ctx, tx, id, p.Steps); err != nil {
			return err
		}
		var err error
		out, err = getPolicy(ctx, tx, p.TeamID, id)
		return err
	})
	return out, mapErr(err)
}

// UpdatePolicy replaces the name and steps of policy p.ID in p.TeamID.
func (s *Store) UpdatePolicy(ctx context.Context, p model.EscalationPolicy) (model.EscalationPolicy, error) {
	var out model.EscalationPolicy
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE upsera.escalation_policies SET name = $3, updated_at = now() WHERE team_id = $1 AND id = $2`,
			p.TeamID, p.ID, p.Name)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, "DELETE FROM upsera.escalation_steps WHERE policy_id = $1", p.ID); err != nil {
			return err
		}
		if err := insertSteps(ctx, tx, p.ID, p.Steps); err != nil {
			return err
		}
		out, err = getPolicy(ctx, tx, p.TeamID, p.ID)
		return err
	})
	return out, mapErr(err)
}

// GetPolicy loads one policy of teamID.
func (s *Store) GetPolicy(ctx context.Context, teamID, id int64) (model.EscalationPolicy, error) {
	return getPolicy(ctx, s.pool, teamID, id)
}

// ListPolicies returns teamID's policies ordered by id.
func (s *Store) ListPolicies(ctx context.Context, teamID int64) ([]model.EscalationPolicy, error) {
	return loadPolicies(ctx, s.pool, "p.team_id = $1", teamID)
}

// ListAllPolicies returns every policy on the instance.
func (s *Store) ListAllPolicies(ctx context.Context) ([]model.EscalationPolicy, error) {
	return loadPolicies(ctx, s.pool, "TRUE")
}

// DeletePolicy removes a policy of teamID. It returns ErrInUse while a
// monitor still uses it.
func (s *Store) DeletePolicy(ctx context.Context, teamID, id int64) error {
	return s.deleteRow(ctx, "DELETE FROM upsera.escalation_policies WHERE team_id = $1 AND id = $2", teamID, id)
}

// ---- Maintenance windows ----

const windowCols = `w.id, w.team_id, w.name, w.starts_at, w.ends_at, w.recurrence,
	ARRAY(SELECT wm.monitor_id FROM upsera.maintenance_window_monitors wm WHERE wm.window_id = w.id ORDER BY wm.monitor_id),
	w.created_at, w.updated_at`

func scanWindow(row pgx.Row) (model.MaintenanceWindow, error) {
	var w model.MaintenanceWindow
	err := row.Scan(&w.ID, &w.TeamID, &w.Name, &w.StartsAt, &w.EndsAt, &w.Recurrence, &w.MonitorIDs,
		&w.CreatedAt, &w.UpdatedAt)
	if w.MonitorIDs == nil {
		w.MonitorIDs = []int64{}
	}
	return w, mapErr(err)
}

func collectWindows(rows pgx.Rows, err error) ([]model.MaintenanceWindow, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.MaintenanceWindow, error) { return scanWindow(r) })
}

func finishWindowWrite(ctx context.Context, tx pgx.Tx, teamID, id int64, monitorIDs []int64, out *model.MaintenanceWindow) error {
	if _, err := tx.Exec(ctx, "DELETE FROM upsera.maintenance_window_monitors WHERE window_id = $1", id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO upsera.maintenance_window_monitors (window_id, monitor_id)
		SELECT $1, m FROM unnest($2::bigint[]) AS m ON CONFLICT DO NOTHING`, id, monitorIDs); err != nil {
		return err
	}
	w, err := scanWindow(tx.QueryRow(ctx,
		"SELECT "+windowCols+" FROM upsera.maintenance_windows w WHERE w.team_id = $1 AND w.id = $2", teamID, id))
	*out = w
	return err
}

// CreateMaintenanceWindow inserts w with its monitor links.
func (s *Store) CreateMaintenanceWindow(ctx context.Context, w model.MaintenanceWindow) (model.MaintenanceWindow, error) {
	var out model.MaintenanceWindow
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var id int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO upsera.maintenance_windows (team_id, name, starts_at, ends_at, recurrence)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			w.TeamID, w.Name, w.StartsAt, w.EndsAt, w.Recurrence).Scan(&id); err != nil {
			return err
		}
		return finishWindowWrite(ctx, tx, w.TeamID, id, w.MonitorIDs, &out)
	})
	return out, mapErr(err)
}

// UpdateMaintenanceWindow replaces window w.ID in w.TeamID.
func (s *Store) UpdateMaintenanceWindow(ctx context.Context, w model.MaintenanceWindow) (model.MaintenanceWindow, error) {
	var out model.MaintenanceWindow
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE upsera.maintenance_windows SET name = $3, starts_at = $4, ends_at = $5, recurrence = $6, updated_at = now()
			WHERE team_id = $1 AND id = $2`,
			w.TeamID, w.ID, w.Name, w.StartsAt, w.EndsAt, w.Recurrence)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return finishWindowWrite(ctx, tx, w.TeamID, w.ID, w.MonitorIDs, &out)
	})
	return out, mapErr(err)
}

// GetMaintenanceWindow loads one window of teamID.
func (s *Store) GetMaintenanceWindow(ctx context.Context, teamID, id int64) (model.MaintenanceWindow, error) {
	return scanWindow(s.pool.QueryRow(ctx,
		"SELECT "+windowCols+" FROM upsera.maintenance_windows w WHERE w.team_id = $1 AND w.id = $2", teamID, id))
}

// ListMaintenanceWindows returns teamID's windows ordered by id.
func (s *Store) ListMaintenanceWindows(ctx context.Context, teamID int64) ([]model.MaintenanceWindow, error) {
	return collectWindows(s.pool.Query(ctx,
		"SELECT "+windowCols+" FROM upsera.maintenance_windows w WHERE w.team_id = $1 ORDER BY w.id", teamID))
}

// ListAllMaintenanceWindows returns every window on the instance.
func (s *Store) ListAllMaintenanceWindows(ctx context.Context) ([]model.MaintenanceWindow, error) {
	return collectWindows(s.pool.Query(ctx, "SELECT "+windowCols+" FROM upsera.maintenance_windows w ORDER BY w.id"))
}

// DeleteMaintenanceWindow removes a window of teamID.
func (s *Store) DeleteMaintenanceWindow(ctx context.Context, teamID, id int64) error {
	return s.deleteRow(ctx, "DELETE FROM upsera.maintenance_windows WHERE team_id = $1 AND id = $2", teamID, id)
}

// ---- Alerts ----

const alertCols = `id::text, team_id, monitor_id, incident_start, opened_at, message, step, next_escalation_at,
	notified_channel_ids, suppressed, flapping, acked_at, acked_by_user_id, coalesce(ack_source, ''), acked_by_name,
	resolved_at, resolution, updated_at`

func scanAlert(row pgx.Row) (model.Alert, error) {
	var a model.Alert
	err := row.Scan(&a.ID, &a.TeamID, &a.MonitorID, &a.IncidentStart, &a.OpenedAt, &a.Message, &a.Step,
		&a.NextEscalationAt, &a.NotifiedChannelIDs, &a.Suppressed, &a.Flapping, &a.AckedAt, &a.AckedByUserID,
		&a.AckSource, &a.AckedByName, &a.ResolvedAt, &a.Resolution, &a.UpdatedAt)
	if a.NotifiedChannelIDs == nil {
		a.NotifiedChannelIDs = []int64{}
	}
	return a, mapErr(err)
}

func collectAlerts(rows pgx.Rows, err error) ([]model.Alert, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Alert, error) { return scanAlert(r) })
}

// UpsertAlerts writes alerts in one batch (one implicit transaction).
// Resolved alerts are written first so that a resolved alert frees the
// one-open-alert-per-monitor index before a newer open alert for the same
// monitor is inserted. Alerts of monitors deleted in the meantime are
// skipped, and an acknowledging user deleted since is written as NULL.
func (s *Store) UpsertAlerts(ctx context.Context, alerts []model.Alert) error {
	if len(alerts) == 0 {
		return nil
	}
	sorted := slices.Clone(alerts)
	slices.SortStableFunc(sorted, func(a, b model.Alert) int {
		switch ar, br := a.ResolvedAt != nil, b.ResolvedAt != nil; {
		case ar && !br:
			return -1
		case !ar && br:
			return 1
		}
		return 0
	})
	b := &pgx.Batch{}
	for _, a := range sorted {
		notified := a.NotifiedChannelIDs
		if notified == nil {
			notified = []int64{}
		}
		b.Queue(`
			INSERT INTO upsera.alerts (id, team_id, monitor_id, incident_start, opened_at, message, step,
				next_escalation_at, notified_channel_ids, suppressed, flapping, acked_at, acked_by_user_id,
				ack_source, acked_by_name, resolved_at, resolution, updated_at)
			SELECT $1::uuid, $2::bigint, $3::bigint, $4::timestamptz, $5::timestamptz, $6::text, $7::integer,
				$8::timestamptz, $9::bigint[], $10::boolean, $11::boolean, $12::timestamptz,
				(SELECT u.id FROM upsera.users u WHERE u.id = $13::bigint), $14::text, $15::text,
				$16::timestamptz, $17::text, now()
			WHERE EXISTS (SELECT 1 FROM upsera.monitors WHERE id = $3::bigint)
			ON CONFLICT (id) DO UPDATE SET message = EXCLUDED.message, step = EXCLUDED.step,
				next_escalation_at = EXCLUDED.next_escalation_at, notified_channel_ids = EXCLUDED.notified_channel_ids,
				suppressed = EXCLUDED.suppressed, flapping = EXCLUDED.flapping, acked_at = EXCLUDED.acked_at,
				acked_by_user_id = EXCLUDED.acked_by_user_id, ack_source = EXCLUDED.ack_source,
				acked_by_name = EXCLUDED.acked_by_name, resolved_at = EXCLUDED.resolved_at,
				resolution = EXCLUDED.resolution, updated_at = now()`,
			a.ID, a.TeamID, a.MonitorID, a.IncidentStart, a.OpenedAt, sanitizeMessage(a.Message), a.Step,
			a.NextEscalationAt, notified, a.Suppressed, a.Flapping, a.AckedAt, a.AckedByUserID,
			nullIfEmpty(string(a.AckSource)), sanitizeMessage(a.AckedByName), a.ResolvedAt, a.Resolution)
	}
	return s.pool.SendBatch(ctx, b).Close()
}

// ListOpenAlerts returns every unresolved alert on the instance.
func (s *Store) ListOpenAlerts(ctx context.Context) ([]model.Alert, error) {
	return collectAlerts(s.pool.Query(ctx,
		"SELECT "+alertCols+" FROM upsera.alerts WHERE resolved_at IS NULL ORDER BY opened_at"))
}

// GetAlert loads one alert of teamID. A malformed id is ErrNotFound.
func (s *Store) GetAlert(ctx context.Context, teamID int64, id string) (model.Alert, error) {
	if _, err := uuid.Parse(id); err != nil {
		return model.Alert{}, ErrNotFound
	}
	return scanAlert(s.pool.QueryRow(ctx,
		"SELECT "+alertCols+" FROM upsera.alerts WHERE team_id = $1 AND id = $2::uuid", teamID, id))
}

// ListAlerts returns teamID's alerts newest first. state is "open",
// "resolved" or "all"; monitorID 0 means any monitor; a zero before means
// no upper bound on opened_at.
func (s *Store) ListAlerts(ctx context.Context, teamID int64, state string, monitorID int64, before time.Time, limit int) ([]model.Alert, error) {
	var beforePtr *time.Time
	if !before.IsZero() {
		beforePtr = &before
	}
	return collectAlerts(s.pool.Query(ctx, "SELECT "+alertCols+` FROM upsera.alerts
		WHERE team_id = $1
		  AND ($2::text = 'all' OR ($2::text = 'open') = (resolved_at IS NULL))
		  AND ($3::bigint = 0 OR monitor_id = $3)
		  AND ($4::timestamptz IS NULL OR opened_at < $4)
		ORDER BY opened_at DESC, id DESC LIMIT $5`, teamID, state, monitorID, beforePtr, limit))
}

// ---- Notification log ----

// InsertNotificationLog appends entries. References to channels, monitors
// or alerts deleted in the meantime are written as NULL, and entries of
// deleted teams are skipped, so a stale outbox never blocks the log.
func (s *Store) InsertNotificationLog(ctx context.Context, entries []model.NotificationLogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	n := len(entries)
	team, attempt := make([]int64, n), make([]int16, n)
	channel, monitor := make([]*int64, n), make([]*int64, n)
	alert := make([]*string, n)
	event, key, errs := make([]string, n), make([]string, n), make([]string, n)
	ok, at := make([]bool, n), make([]time.Time, n)
	for i, e := range entries {
		team[i], channel[i], monitor[i], alert[i] = e.TeamID, e.ChannelID, e.MonitorID, e.AlertID
		event[i], key[i], attempt[i], ok[i] = string(e.Event), e.DedupeKey, int16(e.Attempt), e.OK
		errs[i], at[i] = sanitizeMessage(e.Error), e.At
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO upsera.notification_log (team_id, channel_id, monitor_id, alert_id, event, dedupe_key, attempt, ok, error, at)
		SELECT e.team_id,
			(SELECT c.id FROM upsera.notification_channels c WHERE c.id = e.channel_id),
			(SELECT m.id FROM upsera.monitors m WHERE m.id = e.monitor_id),
			(SELECT a.id FROM upsera.alerts a WHERE a.id = e.alert_id::uuid),
			e.event, e.dedupe_key, e.attempt, e.ok, e.error, e.at
		FROM unnest($1::bigint[], $2::bigint[], $3::bigint[], $4::text[], $5::text[], $6::text[], $7::smallint[],
			$8::boolean[], $9::text[], $10::timestamptz[])
			AS e(team_id, channel_id, monitor_id, alert_id, event, dedupe_key, attempt, ok, error, at)
		WHERE EXISTS (SELECT 1 FROM upsera.teams t WHERE t.id = e.team_id)`,
		team, channel, monitor, alert, event, key, attempt, ok, errs, at)
	return err
}

// ListNotificationLog returns teamID's log newest first, at most limit,
// only entries with id < beforeID when beforeID > 0.
func (s *Store) ListNotificationLog(ctx context.Context, teamID, beforeID int64, limit int) ([]model.NotificationLogEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, team_id, channel_id, monitor_id, alert_id::text, event, dedupe_key, attempt, ok, error, at
		FROM upsera.notification_log
		WHERE team_id = $1 AND ($2::bigint = 0 OR id < $2)
		ORDER BY id DESC LIMIT $3`, teamID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.NotificationLogEntry, error) {
		var e model.NotificationLogEntry
		var attempt int16
		err := r.Scan(&e.ID, &e.TeamID, &e.ChannelID, &e.MonitorID, &e.AlertID, &e.Event, &e.DedupeKey,
			&attempt, &e.OK, &e.Error, &e.At)
		e.Attempt = int(attempt)
		return e, err
	})
}

// ListCertDedupeKeys returns the dedupe keys of cert-expiry notifications
// sent successfully since since, so a restart does not resend them.
func (s *Store) ListCertDedupeKeys(ctx context.Context, since time.Time) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT dedupe_key FROM upsera.notification_log
		WHERE event = 'cert_expiry' AND ok AND at >= $1`, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// PruneNotificationLog deletes log entries older than before.
func (s *Store) PruneNotificationLog(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM upsera.notification_log WHERE at < $1", before)
	return tag.RowsAffected(), err
}

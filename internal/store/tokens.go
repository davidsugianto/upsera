package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davidsugianto/upsera/internal/model"
)

const tokenCols = "id, team_id, name, scope, created_by, created_at, last_used_at, expires_at"

func scanToken(row pgx.Row) (model.APIToken, error) {
	var t model.APIToken
	err := row.Scan(&t.ID, &t.TeamID, &t.Name, &t.Scope, &t.CreatedBy, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt)
	return t, mapErr(err)
}

// CreateAPIToken stores a token. tokenHash is SHA-256 of the secret.
func (s *Store) CreateAPIToken(ctx context.Context, teamID int64, name string, scope model.TokenScope, tokenHash []byte, createdBy *int64, expiresAt *time.Time) (model.APIToken, error) {
	return scanToken(s.pool.QueryRow(ctx, `
		INSERT INTO upsera.api_tokens (team_id, name, scope, token_hash, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+tokenCols,
		teamID, name, scope, tokenHash, createdBy, expiresAt))
}

// ListAPITokens returns teamID's non-revoked tokens, newest first.
func (s *Store) ListAPITokens(ctx context.Context, teamID int64) ([]model.APIToken, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT "+tokenCols+" FROM upsera.api_tokens WHERE team_id = $1 AND revoked_at IS NULL ORDER BY id DESC", teamID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.APIToken, error) { return scanToken(r) })
}

// DeleteAPIToken revokes a token of teamID. The row is kept (revoked_at
// set) rather than deleted so past audit_log entries keep a valid
// actor_token_id; a revoked or missing token both return ErrNotFound.
func (s *Store) DeleteAPIToken(ctx context.Context, teamID, id int64) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE upsera.api_tokens SET revoked_at = now() WHERE team_id = $1 AND id = $2 AND revoked_at IS NULL", teamID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UseAPIToken resolves an unexpired, non-revoked token by hash and records
// its use. The last_used_at write is throttled to once a minute per token.
func (s *Store) UseAPIToken(ctx context.Context, tokenHash []byte) (model.APIToken, error) {
	t, err := scanToken(s.pool.QueryRow(ctx,
		"SELECT "+tokenCols+" FROM upsera.api_tokens WHERE token_hash = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())", tokenHash))
	if err != nil {
		return t, err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE upsera.api_tokens SET last_used_at = now()
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`, t.ID)
	return t, err
}

// InsertAudit appends an audit entry. Details defaults to {}.
func (s *Store) InsertAudit(ctx context.Context, e model.AuditEntry) error {
	details := e.Details
	if len(details) == 0 {
		details = json.RawMessage("{}")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO upsera.audit_log (team_id, actor_user_id, actor_token_id, action, target_type, target_id, details)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.TeamID, e.ActorUserID, e.ActorTokenID, e.Action, e.TargetType, e.TargetID, details)
	return err
}

// ListAudit returns teamID's audit entries newest first. beforeID > 0 pages
// backwards from that id.
func (s *Store) ListAudit(ctx context.Context, teamID int64, beforeID int64, limit int) ([]model.AuditEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, team_id, actor_user_id, actor_token_id, action, target_type, target_id, details, at
		FROM upsera.audit_log
		WHERE team_id = $1 AND ($2 = 0 OR id < $2)
		ORDER BY id DESC LIMIT $3`, teamID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.AuditEntry, error) {
		var e model.AuditEntry
		err := r.Scan(&e.ID, &e.TeamID, &e.ActorUserID, &e.ActorTokenID, &e.Action, &e.TargetType, &e.TargetID, &e.Details, &e.At)
		return e, err
	})
}

// GetInstanceSettings loads the singleton settings row.
func (s *Store) GetInstanceSettings(ctx context.Context) (model.InstanceSettings, error) {
	var st model.InstanceSettings
	err := s.pool.QueryRow(ctx, "SELECT block_private_targets, retention_days, updated_at FROM upsera.instance_settings WHERE id = 1").
		Scan(&st.BlockPrivateTargets, &st.RetentionDays, &st.UpdatedAt)
	return st, mapErr(err)
}

// UpdateInstanceSettings replaces the singleton settings row.
func (s *Store) UpdateInstanceSettings(ctx context.Context, st model.InstanceSettings) (model.InstanceSettings, error) {
	err := s.pool.QueryRow(ctx, `
		UPDATE upsera.instance_settings SET block_private_targets = $1, retention_days = $2, updated_at = now()
		WHERE id = 1 RETURNING block_private_targets, retention_days, updated_at`,
		st.BlockPrivateTargets, st.RetentionDays).
		Scan(&st.BlockPrivateTargets, &st.RetentionDays, &st.UpdatedAt)
	return st, mapErr(err)
}

// LocalProbeID returns the id of the built-in "local" probe.
func (s *Store) LocalProbeID(ctx context.Context) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, "SELECT id FROM upsera.probes WHERE name = 'local'").Scan(&id)
	return id, mapErr(err)
}

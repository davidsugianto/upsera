package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davidsugianto/upsera/internal/model"
)

// ErrSetupDone is returned by Setup when any user already exists.
var ErrSetupDone = errors.New("setup already completed")

// ErrLastOwner is returned when a change would leave a team without an owner.
var ErrLastOwner = errors.New("team must keep at least one owner")

const userCols = "id, email, name, password_hash, is_admin, created_at"

func scanUser(row pgx.Row) (model.User, error) {
	var u model.User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.IsAdmin, &u.CreatedAt)
	return u, mapErr(err)
}

// CountUsers returns the number of accounts; zero means first-run setup is pending.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM upsera.users").Scan(&n)
	return n, err
}

// Setup creates the instance admin and their first team in one transaction.
// It fails with ErrSetupDone if any user exists.
func (s *Store) Setup(ctx context.Context, email, name, passwordHash, teamName string) (model.User, model.Team, error) {
	var u model.User
	var t model.Team
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "LOCK TABLE upsera.users IN EXCLUSIVE MODE"); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM upsera.users)").Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrSetupDone
		}
		var err error
		if u, err = insertUser(ctx, tx, email, name, passwordHash, true); err != nil {
			return err
		}
		t, err = insertTeam(ctx, tx, teamNameOrDefault(teamName), u.ID)
		return err
	})
	return u, t, err
}

func teamNameOrDefault(teamName string) string {
	if teamName == "" {
		return "Default"
	}
	return teamName
}

func insertUser(ctx context.Context, q pgx.Tx, email, name, passwordHash string, admin bool) (model.User, error) {
	return scanUser(q.QueryRow(ctx,
		"INSERT INTO upsera.users (email, name, password_hash, is_admin) VALUES ($1, $2, $3, $4) RETURNING "+userCols,
		email, name, passwordHash, admin))
}

func insertTeam(ctx context.Context, tx pgx.Tx, name string, ownerID int64) (model.Team, error) {
	var t model.Team
	err := tx.QueryRow(ctx, "INSERT INTO upsera.teams (name) VALUES ($1) RETURNING id, name, created_at", name).
		Scan(&t.ID, &t.Name, &t.CreatedAt)
	if err != nil {
		return t, mapErr(err)
	}
	_, err = tx.Exec(ctx, "INSERT INTO upsera.memberships (team_id, user_id, role) VALUES ($1, $2, 'owner')", t.ID, ownerID)
	return t, mapErr(err)
}

// CreateUser adds an account. Duplicate emails (case-insensitive) return ErrConflict.
func (s *Store) CreateUser(ctx context.Context, email, name, passwordHash string, admin bool) (model.User, error) {
	var u model.User
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		u, err = insertUser(ctx, tx, email, name, passwordHash, admin)
		return err
	})
	return u, err
}

// GetUser loads an account by id.
func (s *Store) GetUser(ctx context.Context, id int64) (model.User, error) {
	return scanUser(s.pool.QueryRow(ctx, "SELECT "+userCols+" FROM upsera.users WHERE id = $1", id))
}

// GetUserByEmail loads an account by email, case-insensitively.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (model.User, error) {
	return scanUser(s.pool.QueryRow(ctx, "SELECT "+userCols+" FROM upsera.users WHERE lower(email) = lower($1)", email))
}

// ListUsers returns every account, oldest first.
func (s *Store) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+userCols+" FROM upsera.users ORDER BY id")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.User, error) { return scanUser(r) })
}

// CreateTeam creates a team with ownerID as its first owner.
func (s *Store) CreateTeam(ctx context.Context, name string, ownerID int64) (model.Team, error) {
	var t model.Team
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		t, err = insertTeam(ctx, tx, name, ownerID)
		return err
	})
	return t, err
}

// ListTeamsForUser returns the teams userID belongs to, with their role.
func (s *Store) ListTeamsForUser(ctx context.Context, userID int64) ([]model.TeamMembership, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, t.name, t.created_at, m.role
		FROM upsera.memberships m JOIN upsera.teams t ON t.id = m.team_id
		WHERE m.user_id = $1 ORDER BY t.id`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.TeamMembership, error) {
		var tm model.TeamMembership
		err := r.Scan(&tm.Team.ID, &tm.Team.Name, &tm.Team.CreatedAt, &tm.Role)
		return tm, err
	})
}

// GetRole returns userID's role in teamID, or ErrNotFound if not a member.
func (s *Store) GetRole(ctx context.Context, teamID, userID int64) (model.Role, error) {
	var r model.Role
	err := s.pool.QueryRow(ctx, "SELECT role FROM upsera.memberships WHERE team_id = $1 AND user_id = $2", teamID, userID).Scan(&r)
	return r, mapErr(err)
}

// ListMembers returns the members of teamID.
func (s *Store) ListMembers(ctx context.Context, teamID int64) ([]model.Member, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.email, u.name, m.role
		FROM upsera.memberships m JOIN upsera.users u ON u.id = m.user_id
		WHERE m.team_id = $1 ORDER BY u.id`, teamID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Member, error) {
		var m model.Member
		err := r.Scan(&m.UserID, &m.Email, &m.Name, &m.Role)
		return m, err
	})
}

// SetMember adds userID to teamID or changes their role. Demoting the last
// owner returns ErrLastOwner.
func (s *Store) SetMember(ctx context.Context, teamID, userID int64, role model.Role) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if role != model.RoleOwner {
			if err := ensureOtherOwner(ctx, tx, teamID, userID); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO upsera.memberships (team_id, user_id, role) VALUES ($1, $2, $3)
			ON CONFLICT (team_id, user_id) DO UPDATE SET role = EXCLUDED.role`, teamID, userID, role)
		if err != nil {
			return fmt.Errorf("set member: %w", mapErr(err))
		}
		return nil
	})
}

// RemoveMember removes userID from teamID. Removing the last owner returns
// ErrLastOwner; a non-member returns ErrNotFound.
func (s *Store) RemoveMember(ctx context.Context, teamID, userID int64) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if err := ensureOtherOwner(ctx, tx, teamID, userID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "DELETE FROM upsera.memberships WHERE team_id = $1 AND user_id = $2", teamID, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ensureOtherOwner fails with ErrLastOwner if userID is currently the only
// owner of teamID. Owner rows are locked so concurrent demotions serialize.
func ensureOtherOwner(ctx context.Context, tx pgx.Tx, teamID, userID int64) error {
	rows, err := tx.Query(ctx, "SELECT user_id FROM upsera.memberships WHERE team_id = $1 AND role = 'owner' FOR UPDATE", teamID)
	if err != nil {
		return err
	}
	owners, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range owners {
		if id != userID {
			return nil
		}
	}
	if len(owners) == 0 {
		return nil // userID is not an owner; nothing to protect
	}
	return ErrLastOwner
}

// CreateSession stores a new session. tokenHash is SHA-256 of the cookie value.
func (s *Store) CreateSession(ctx context.Context, userID int64, tokenHash []byte, csrf string, expires time.Time) error {
	_, err := s.pool.Exec(ctx,
		"INSERT INTO upsera.sessions (token_hash, user_id, csrf_token, expires_at) VALUES ($1, $2, $3, $4)",
		tokenHash, userID, csrf, expires)
	return err
}

// GetSession returns an unexpired session and its user.
func (s *Store) GetSession(ctx context.Context, tokenHash []byte) (model.Session, model.User, error) {
	var sess model.Session
	var u model.User
	err := s.pool.QueryRow(ctx, `
		SELECT s.id, s.user_id, s.csrf_token, s.expires_at,
		       u.id, u.email, u.name, u.password_hash, u.is_admin, u.created_at
		FROM upsera.sessions s JOIN upsera.users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, tokenHash).
		Scan(&sess.ID, &sess.UserID, &sess.CSRFToken, &sess.ExpiresAt,
			&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.IsAdmin, &u.CreatedAt)
	return sess, u, mapErr(err)
}

// DeleteSession removes a session (logout).
func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM upsera.sessions WHERE token_hash = $1", tokenHash)
	return err
}

// DeleteExpiredSessions prunes expired sessions and returns how many were removed.
func (s *Store) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM upsera.sessions WHERE expires_at <= now()")
	return tag.RowsAffected(), err
}

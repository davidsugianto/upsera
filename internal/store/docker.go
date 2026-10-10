package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/davidsugianto/upsera/internal/model"
)

const dockerHostCols = `id, team_id, name, endpoint, tls, created_at, updated_at`

func (s *Store) scanDockerHost(row pgx.Row) (model.DockerHost, error) {
	var h model.DockerHost
	var enc string
	if err := row.Scan(&h.ID, &h.TeamID, &h.Name, &h.Endpoint, &enc, &h.CreatedAt, &h.UpdatedAt); err != nil {
		return h, mapErr(err)
	}
	if s.box == nil {
		h.DecryptErr = channelDecryptErr
		return h, nil
	}
	plain, err := s.box.Decrypt(enc)
	if err != nil {
		h.DecryptErr = channelDecryptErr
		return h, nil
	}
	if err := json.Unmarshal(plain, &h.TLS); err != nil {
		h.DecryptErr = channelDecryptErr
	}
	return h, nil
}

func (s *Store) collectDockerHosts(rows pgx.Rows, err error) ([]model.DockerHost, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.DockerHost, error) { return s.scanDockerHost(r) })
}

func (s *Store) encryptTLS(t model.DockerTLS) (string, error) {
	raw, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	return s.encryptConfig(raw)
}

// CreateDockerHost inserts h with its TLS material encrypted. A duplicate
// name within the team returns ErrConflict.
func (s *Store) CreateDockerHost(ctx context.Context, h model.DockerHost) (model.DockerHost, error) {
	enc, err := s.encryptTLS(h.TLS)
	if err != nil {
		return model.DockerHost{}, err
	}
	return s.scanDockerHost(s.pool.QueryRow(ctx, `
		INSERT INTO upsera.docker_hosts (team_id, name, endpoint, tls)
		VALUES ($1, $2, $3, $4) RETURNING `+dockerHostCols,
		h.TeamID, h.Name, h.Endpoint, enc))
}

// UpdateDockerHost replaces the name, endpoint and TLS material of host
// h.ID in h.TeamID.
func (s *Store) UpdateDockerHost(ctx context.Context, h model.DockerHost) (model.DockerHost, error) {
	enc, err := s.encryptTLS(h.TLS)
	if err != nil {
		return model.DockerHost{}, err
	}
	return s.scanDockerHost(s.pool.QueryRow(ctx, `
		UPDATE upsera.docker_hosts SET name = $3, endpoint = $4, tls = $5, updated_at = now()
		WHERE team_id = $1 AND id = $2 RETURNING `+dockerHostCols,
		h.TeamID, h.ID, h.Name, h.Endpoint, enc))
}

// GetDockerHost loads one Docker host of teamID.
func (s *Store) GetDockerHost(ctx context.Context, teamID, id int64) (model.DockerHost, error) {
	return s.scanDockerHost(s.pool.QueryRow(ctx,
		"SELECT "+dockerHostCols+" FROM upsera.docker_hosts WHERE team_id = $1 AND id = $2", teamID, id))
}

// ListDockerHosts returns teamID's Docker hosts ordered by id.
func (s *Store) ListDockerHosts(ctx context.Context, teamID int64) ([]model.DockerHost, error) {
	return s.collectDockerHosts(s.pool.Query(ctx,
		"SELECT "+dockerHostCols+" FROM upsera.docker_hosts WHERE team_id = $1 ORDER BY id", teamID))
}

// ListAllDockerHosts returns every Docker host on the instance, for the
// checker's registry.
func (s *Store) ListAllDockerHosts(ctx context.Context) ([]model.DockerHost, error) {
	return s.collectDockerHosts(s.pool.Query(ctx,
		"SELECT "+dockerHostCols+" FROM upsera.docker_hosts ORDER BY id"))
}

// DeleteDockerHost removes a Docker host of teamID. It returns ErrInUse
// while a docker monitor still points at it.
func (s *Store) DeleteDockerHost(ctx context.Context, teamID, id int64) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var inUse bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM upsera.monitors
			WHERE team_id = $1 AND type = 'docker' AND config @> jsonb_build_object('docker_host_id', $2::bigint))`,
			teamID, id).Scan(&inUse); err != nil {
			return err
		}
		if inUse {
			return ErrInUse
		}
		tag, err := tx.Exec(ctx, "DELETE FROM upsera.docker_hosts WHERE team_id = $1 AND id = $2", teamID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// EnsureLocalDockerHost creates a host named "local" with the given
// endpoint on the first team (lowest id) if that team has no Docker hosts
// yet. It returns the created host, or ok=false if nothing was created
// (no team yet, or the team already has hosts).
func (s *Store) EnsureLocalDockerHost(ctx context.Context, endpoint string) (h model.DockerHost, ok bool, err error) {
	enc, err := s.encryptTLS(model.DockerTLS{})
	if err != nil {
		return h, false, err
	}
	h, err = s.scanDockerHost(s.pool.QueryRow(ctx, `
		INSERT INTO upsera.docker_hosts (team_id, name, endpoint, tls)
		SELECT t.id, 'local', $1, $2 FROM (SELECT id FROM upsera.teams ORDER BY id LIMIT 1) t
		WHERE NOT EXISTS (SELECT 1 FROM upsera.docker_hosts d WHERE d.team_id = t.id)
		ON CONFLICT DO NOTHING
		RETURNING `+dockerHostCols, endpoint, enc))
	if err == ErrNotFound {
		return model.DockerHost{}, false, nil
	}
	return h, err == nil, err
}

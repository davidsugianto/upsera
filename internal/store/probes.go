package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davidsugianto/upsera/internal/model"
)

// RegistrationTTL is how long a registration token from CreateProbe stays
// redeemable.
const RegistrationTTL = 24 * time.Hour

// probeCols: a probe is registered once it holds a credential; the built-in
// local probe never has one and always counts as registered.
const probeCols = `id, name, region, (token_hash IS NOT NULL OR region = 'local'), registration_expires_at,
	last_seen_at, version, created_at`

func scanProbe(row pgx.Row) (model.Probe, error) {
	var p model.Probe
	err := row.Scan(&p.ID, &p.Name, &p.Region, &p.Registered, &p.RegistrationExpiresAt, &p.LastSeenAt, &p.Version, &p.CreatedAt)
	return p, mapErr(err)
}

// newProbeSecret returns prefix plus 32 random bytes as unpadded base64url,
// and the SHA-256 of the whole string, the only form that is stored.
func newProbeSecret(prefix string) (secret string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	secret = prefix + base64.RawURLEncoding.EncodeToString(b)
	return secret, hashSecret(secret), nil
}

func hashSecret(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// CreateProbe adds an unregistered probe for region and returns it with its
// one-time registration token ("upr_..."), valid for RegistrationTTL. A
// duplicate name or region is ErrConflict.
func (s *Store) CreateProbe(ctx context.Context, name, region string) (model.Probe, string, error) {
	token, hash, err := newProbeSecret(model.RegistrationPrefix)
	if err != nil {
		return model.Probe{}, "", err
	}
	p, err := scanProbe(s.pool.QueryRow(ctx, `
		INSERT INTO upsera.probes (name, region, registration_token_hash, registration_expires_at)
		VALUES ($1, $2, $3, now() + make_interval(secs => $4))
		RETURNING `+probeCols, name, region, hash, RegistrationTTL.Seconds()))
	if err != nil {
		return model.Probe{}, "", err
	}
	return p, token, nil
}

// ListProbes returns every probe, including the built-in local one, by id.
func (s *Store) ListProbes(ctx context.Context) ([]model.Probe, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+probeCols+" FROM upsera.probes ORDER BY id")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Probe, error) { return scanProbe(r) })
}

// ListProbeAuth returns the registered, non-local probes with their
// credential hashes, to seed the in-memory probe registry.
func (s *Store) ListProbeAuth(ctx context.Context) ([]model.ProbeAuth, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, region, token_hash, last_seen_at FROM upsera.probes
		WHERE token_hash IS NOT NULL AND region <> 'local' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.ProbeAuth, error) {
		var a model.ProbeAuth
		err := r.Scan(&a.ID, &a.Name, &a.Region, &a.TokenHash, &a.LastSeenAt)
		return a, err
	})
}

// IssueRegistrationToken replaces probe id's credential and registration
// token with a new registration token valid for ttl: any current credential
// is revoked. It returns ErrNotFound for an unknown probe and ErrLocalProbe
// for the built-in one.
func (s *Store) IssueRegistrationToken(ctx context.Context, id int64, ttl time.Duration) (string, time.Time, error) {
	token, hash, err := newProbeSecret(model.RegistrationPrefix)
	if err != nil {
		return "", time.Time{}, err
	}
	var expires time.Time
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		var region string
		if err := tx.QueryRow(ctx, "SELECT region FROM upsera.probes WHERE id = $1 FOR UPDATE", id).Scan(&region); err != nil {
			return err
		}
		if region == model.LocalRegion {
			return ErrLocalProbe
		}
		return tx.QueryRow(ctx, `
			UPDATE upsera.probes SET token_hash = NULL, registration_token_hash = $2,
				registration_expires_at = now() + make_interval(secs => $3)
			WHERE id = $1 RETURNING registration_expires_at`, id, hash, ttl.Seconds()).Scan(&expires)
	})
	if err != nil {
		return "", time.Time{}, mapErr(err)
	}
	return token, expires, nil
}

// RedeemRegistration exchanges a registration token for a new credential
// ("upp_..."), recording the probe's version. The token is single use: an
// unknown, expired or already redeemed token is ErrNotFound. The returned
// ProbeAuth is what the registry needs to authenticate the new credential.
func (s *Store) RedeemRegistration(ctx context.Context, token, version string) (model.Probe, string, model.ProbeAuth, error) {
	cred, credHash, err := newProbeSecret(model.CredentialPrefix)
	if err != nil {
		return model.Probe{}, "", model.ProbeAuth{}, err
	}
	// One UPDATE is the whole exchange, so two racing redemptions cannot
	// both succeed.
	p, err := scanProbe(s.pool.QueryRow(ctx, `
		UPDATE upsera.probes SET token_hash = $2, registration_token_hash = NULL, registration_expires_at = NULL,
			version = $3
		WHERE registration_token_hash = $1 AND registration_expires_at > now() AND region <> 'local'
		RETURNING `+probeCols, hashSecret(token), credHash, version))
	if err != nil {
		return model.Probe{}, "", model.ProbeAuth{}, err
	}
	auth := model.ProbeAuth{ID: p.ID, Name: p.Name, Region: p.Region, TokenHash: credHash, LastSeenAt: p.LastSeenAt}
	return p, cred, auth, nil
}

// DeleteProbe removes probe id and, by cascade, the heartbeats it reported.
// It returns ErrNotFound for an unknown probe, ErrLocalProbe for the
// built-in one and ErrInUse while any monitor lists the probe's region.
func (s *Store) DeleteProbe(ctx context.Context, id int64) error {
	return mapErr(s.inTx(ctx, func(tx pgx.Tx) error {
		var region string
		if err := tx.QueryRow(ctx, "SELECT region FROM upsera.probes WHERE id = $1 FOR UPDATE", id).Scan(&region); err != nil {
			return err
		}
		if region == model.LocalRegion {
			return ErrLocalProbe
		}
		var used bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM upsera.monitors WHERE $1 = ANY (regions))", region).Scan(&used); err != nil {
			return err
		}
		if used {
			return ErrInUse
		}
		_, err := tx.Exec(ctx, "DELETE FROM upsera.probes WHERE id = $1", id)
		return err
	}))
}

// TouchProbes records probe contact. last_seen_at only moves forward, and
// the version is taken from the newest observation. Unknown probe ids are
// ignored.
func (s *Store) TouchProbes(ctx context.Context, seen []model.ProbeSeen) error {
	if len(seen) == 0 {
		return nil
	}
	newest := make(map[int64]model.ProbeSeen, len(seen))
	for _, x := range seen {
		if cur, ok := newest[x.ID]; !ok || x.At.After(cur.At) {
			newest[x.ID] = x
		}
	}
	ids, ats, vers := make([]int64, 0, len(newest)), make([]time.Time, 0, len(newest)), make([]string, 0, len(newest))
	for _, x := range newest {
		ids, ats, vers = append(ids, x.ID), append(ats, x.At), append(vers, x.Version)
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE upsera.probes p SET
			version = CASE WHEN s.version <> '' AND (p.last_seen_at IS NULL OR s.at >= p.last_seen_at) THEN s.version ELSE p.version END,
			last_seen_at = GREATEST(p.last_seen_at, s.at)
		FROM unnest($1::bigint[], $2::timestamptz[], $3::text[]) AS s(id, at, version)
		WHERE p.id = s.id`, ids, ats, vers)
	return err
}

// ProbeRegions maps probe id to region, for labelling heartbeats.
func (s *Store) ProbeRegions(ctx context.Context) (map[int64]string, error) {
	rows, err := s.pool.Query(ctx, "SELECT id, region FROM upsera.probes")
	if err != nil {
		return nil, err
	}
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var region string
		if err := rows.Scan(&id, &region); err != nil {
			return nil, err
		}
		out[id] = region
	}
	return out, rows.Err()
}

package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/store"
)

// registrationTTL is how long a probe registration token stays valid.
const registrationTTL = 24 * time.Hour

// probeNameRe is the shape of probe names and region names.
var probeNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

const probeNamePattern = `^[a-z0-9][a-z0-9-]*$`

// probeRegionCache maps probe ids to region names for heartbeat bodies. Probe
// ids are never reused and a probe's region never changes, so entries never
// go stale; the cache only reloads (at most every few seconds) when it meets
// an id it does not know.
type probeRegionCache struct {
	st *store.Store

	mu        sync.Mutex
	regions   map[int64]string
	refreshed time.Time
}

const probeRegionRefresh = 5 * time.Second

func newProbeRegionCache(st *store.Store) *probeRegionCache {
	return &probeRegionCache{st: st, regions: map[int64]string{}}
}

// region returns the region of probe id, or "" when unknown.
func (c *probeRegionCache) region(ctx context.Context, id int64) string {
	if c == nil || c.st == nil || id <= 0 {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.regions[id]; ok {
		return r
	}
	if time.Since(c.refreshed) < probeRegionRefresh {
		return ""
	}
	m, err := c.st.ProbeRegions(ctx)
	if err != nil {
		return ""
	}
	c.refreshed = time.Now()
	for k, v := range m {
		c.regions[k] = v
	}
	return c.regions[id]
}

type probeBody struct {
	ID                    int64      `json:"id"`
	Name                  string     `json:"name"`
	Region                string     `json:"region"`
	Registered            bool       `json:"registered" doc:"True once the registration token was exchanged for a credential."`
	RegistrationExpiresAt *time.Time `json:"registration_expires_at,omitempty" doc:"When the pending registration token expires; absent once redeemed."`
	LastSeenAt            *time.Time `json:"last_seen_at,omitempty"`
	Version               string     `json:"version"`
	Online                bool       `json:"online"`
	IsLocal               bool       `json:"is_local" doc:"The built-in checker; it cannot be deleted or re-registered."`
	CreatedAt             time.Time  `json:"created_at"`
}

func toProbeBody(d Deps, p model.Probe) probeBody {
	return probeBody{
		ID: p.ID, Name: p.Name, Region: p.Region, Registered: p.Registered,
		RegistrationExpiresAt: p.RegistrationExpiresAt, LastSeenAt: p.LastSeenAt, Version: p.Version,
		Online: probeOnline(d, p), IsLocal: p.Region == model.LocalRegion, CreatedAt: p.CreatedAt,
	}
}

// probeOnline: the local checker is always online; a remote probe is online
// when it is registered and the registry has heard from it recently.
func probeOnline(d Deps, p model.Probe) bool {
	if p.Region == model.LocalRegion {
		return true
	}
	return p.Registered && d.Probes != nil && d.Probes.Online(p.Region)
}

type listProbesOutput struct {
	Body struct {
		Probes []probeBody `json:"probes"`
	}
}

type createProbeInput struct {
	Body struct {
		Name   string `json:"name" minLength:"1" maxLength:"40" pattern:"^[a-z0-9][a-z0-9-]*$" doc:"Lowercase letters, digits and dashes."`
		Region string `json:"region" minLength:"1" maxLength:"40" pattern:"^[a-z0-9][a-z0-9-]*$" doc:"Region label monitors select; one probe per region. \"local\" is reserved."`
	}
}

type probeTokenBody struct {
	RegistrationToken     string    `json:"registration_token" doc:"One-time token (upr_...) the probe exchanges for its credential. Shown once."`
	RegistrationExpiresAt time.Time `json:"registration_expires_at"`
}

type createProbeOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         struct {
		Probe                 probeBody `json:"probe"`
		RegistrationToken     string    `json:"registration_token"`
		RegistrationExpiresAt time.Time `json:"registration_expires_at"`
	}
}

type probePathInput struct {
	ID int64 `path:"id"`
}

type probeTokenOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         probeTokenBody
}

type regionBody struct {
	Name   string `json:"name"`
	Online bool   `json:"online"`
}

type listRegionsOutput struct {
	Body struct {
		Regions []regionBody `json:"regions"`
	}
}

func registerProbeAdminRoutes(api huma.API, d Deps) {
	huma.Get(api, "/api/regions", func(ctx context.Context, _ *noInput) (*listRegionsOutput, error) {
		if p := principalFrom(ctx); p == nil || (p.user == nil && p.token == nil) {
			return nil, huma.Error401Unauthorized("authentication required")
		}
		ps, err := d.Store.ListProbes(ctx)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listRegionsOutput{}
		out.Body.Regions = make([]regionBody, len(ps))
		for i, p := range ps {
			out.Body.Regions[i] = regionBody{Name: p.Region, Online: probeOnline(d, p)}
		}
		return out, nil
	})

	huma.Get(api, "/api/admin/probes", func(ctx context.Context, _ *noInput) (*listProbesOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		ps, err := d.Store.ListProbes(ctx)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listProbesOutput{}
		out.Body.Probes = make([]probeBody, len(ps))
		for i, p := range ps {
			out.Body.Probes[i] = toProbeBody(d, p)
		}
		return out, nil
	})

	huma.Post(api, "/api/admin/probes", func(ctx context.Context, in *createProbeInput) (*createProbeOutput, error) {
		admin, err := requireAdmin(ctx)
		if err != nil {
			return nil, err
		}
		name, region := in.Body.Name, in.Body.Region
		if len(name) < 1 || len(name) > 40 || !probeNameRe.MatchString(name) {
			return nil, huma.Error422UnprocessableEntity("name must be 1-40 characters: lowercase letters, digits and dashes")
		}
		if len(region) < 1 || len(region) > 40 || !probeNameRe.MatchString(region) {
			return nil, huma.Error422UnprocessableEntity("region must be 1-40 characters: lowercase letters, digits and dashes")
		}
		if name == model.LocalRegion || region == model.LocalRegion {
			return nil, huma.Error422UnprocessableEntity(`"local" is reserved for the built-in checker`)
		}
		p, token, err := d.Store.CreateProbe(ctx, name, region)
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil, huma.Error409Conflict("a probe with this name or region already exists")
			}
			return nil, mapStoreErr(d, err)
		}
		writeAudit(ctx, d, nil, actor{userID: &admin.ID}, "probe.create", "probe", &p.ID,
			map[string]any{"name": p.Name, "region": p.Region})
		out := &createProbeOutput{CacheControl: "no-store"}
		out.Body.Probe = toProbeBody(d, p)
		out.Body.RegistrationToken = token
		out.Body.RegistrationExpiresAt = registrationExpiry(p)
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated })

	huma.Post(api, "/api/admin/probes/{id}/registration-token", func(ctx context.Context, in *probePathInput) (*probeTokenOutput, error) {
		admin, err := requireAdmin(ctx)
		if err != nil {
			return nil, err
		}
		token, expires, err := d.Store.IssueRegistrationToken(ctx, in.ID, registrationTTL)
		if err != nil {
			if errors.Is(err, store.ErrLocalProbe) {
				return nil, huma.Error422UnprocessableEntity("the local probe is built in and has no registration token")
			}
			return nil, mapStoreErr(d, err)
		}
		// The store revoked the old credential; drop it from memory too.
		if d.Probes != nil {
			d.Probes.Remove(in.ID)
		}
		writeAudit(ctx, d, nil, actor{userID: &admin.ID}, "probe.token_reset", "probe", &in.ID, nil)
		return &probeTokenOutput{
			CacheControl: "no-store",
			Body:         probeTokenBody{RegistrationToken: token, RegistrationExpiresAt: expires},
		}, nil
	})

	huma.Delete(api, "/api/admin/probes/{id}", func(ctx context.Context, in *probePathInput) (*emptyOutput, error) {
		admin, err := requireAdmin(ctx)
		if err != nil {
			return nil, err
		}
		var details map[string]any
		if ps, err := d.Store.ListProbes(ctx); err == nil {
			for _, p := range ps {
				if p.ID == in.ID {
					details = map[string]any{"name": p.Name, "region": p.Region}
				}
			}
		}
		if err := d.Store.DeleteProbe(ctx, in.ID); err != nil {
			switch {
			case errors.Is(err, store.ErrLocalProbe):
				return nil, huma.Error422UnprocessableEntity("the local probe is built in and cannot be deleted")
			case errors.Is(err, store.ErrInUse):
				return nil, huma.Error409Conflict("monitors still use this probe's region")
			}
			return nil, mapStoreErr(d, err)
		}
		if d.Probes != nil {
			d.Probes.Remove(in.ID)
		}
		writeAudit(ctx, d, nil, actor{userID: &admin.ID}, "probe.delete", "probe", &in.ID, details)
		return nil, nil
	})
}

// registrationExpiry is when p's pending registration token expires; the
// store sets it, the fallback only guards a store that did not.
func registrationExpiry(p model.Probe) time.Time {
	if p.RegistrationExpiresAt != nil {
		return *p.RegistrationExpiresAt
	}
	return time.Now().Add(registrationTTL)
}

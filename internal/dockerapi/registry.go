package dockerapi

import (
	"errors"
	"sync"

	"github.com/davidsugianto/upsera/internal/model"
)

type entry struct {
	teamID int64
	client *Client
	err    error
}

// Registry caches a Client per Docker host so the check path never touches
// the database. It is safe for concurrent use.
type Registry struct {
	mu    sync.RWMutex
	hosts map[int64]entry
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{hosts: make(map[int64]entry)} }

func build(h model.DockerHost) entry {
	if h.DecryptErr != "" {
		return entry{teamID: h.TeamID, err: errors.New("docker host config unavailable")}
	}
	c, err := New(h)
	if err != nil {
		return entry{teamID: h.TeamID, err: errors.New("docker host config invalid")}
	}
	return entry{teamID: h.TeamID, client: c}
}

// Set replaces every cached host.
func (r *Registry) Set(hs []model.DockerHost) {
	m := make(map[int64]entry, len(hs))
	for _, h := range hs {
		m[h.ID] = build(h)
	}
	r.mu.Lock()
	r.hosts = m
	r.mu.Unlock()
}

// Upsert caches h, replacing any host with the same id.
func (r *Registry) Upsert(h model.DockerHost) {
	e := build(h)
	r.mu.Lock()
	r.hosts[h.ID] = e
	r.mu.Unlock()
}

// Remove drops host id.
func (r *Registry) Remove(id int64) {
	r.mu.Lock()
	delete(r.hosts, id)
	r.mu.Unlock()
}

// Client returns the client for host id, which must belong to teamID.
func (r *Registry) Client(teamID, id int64) (*Client, error) {
	r.mu.RLock()
	e, ok := r.hosts[id]
	r.mu.RUnlock()
	if !ok || e.teamID != teamID {
		return nil, errors.New("docker host not found")
	}
	return e.client, e.err
}

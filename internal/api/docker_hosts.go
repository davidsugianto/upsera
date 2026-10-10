package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/davidsugianto/upsera/internal/checker"
	"github.com/davidsugianto/upsera/internal/dockerapi"
	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/notify"
	"github.com/davidsugianto/upsera/internal/store"
)

const dockerCallTimeout = 10 * time.Second

type dockerHostPathInput struct {
	TeamID int64 `path:"teamID"`
	HostID int64 `path:"hostID"`
}

// dockerTLSRequest holds PEM material. A field that is omitted or equal to
// the redaction placeholder keeps the stored value on update; an empty
// string clears it.
type dockerTLSRequest struct {
	CA   *string `json:"ca,omitempty" maxLength:"20000"`
	Cert *string `json:"cert,omitempty" maxLength:"20000"`
	Key  *string `json:"key,omitempty" maxLength:"20000"`
}

type dockerHostRequestBody struct {
	Name     string           `json:"name" minLength:"1" maxLength:"100"`
	Endpoint string           `json:"endpoint" minLength:"1" maxLength:"500"`
	TLS      dockerTLSRequest `json:"tls,omitempty"`
}

type createDockerHostInput struct {
	TeamID int64 `path:"teamID"`
	Body   dockerHostRequestBody
}

type updateDockerHostInput struct {
	TeamID int64 `path:"teamID"`
	HostID int64 `path:"hostID"`
	Body   dockerHostRequestBody
}

// dockerTLSBody reports which TLS fields are set; a set field is shown as
// the redaction placeholder, never its value.
type dockerTLSBody struct {
	CA   string `json:"ca"`
	Cert string `json:"cert"`
	Key  string `json:"key"`
}

type dockerHostBody struct {
	ID          int64         `json:"id"`
	TeamID      int64         `json:"team_id"`
	Name        string        `json:"name"`
	Endpoint    string        `json:"endpoint"`
	TLS         dockerTLSBody `json:"tls"`
	ConfigError string        `json:"config_error,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

func redactIf(v string) string {
	if v == "" {
		return ""
	}
	return notify.Redacted
}

func toDockerHostBody(h model.DockerHost) dockerHostBody {
	return dockerHostBody{
		ID: h.ID, TeamID: h.TeamID, Name: h.Name, Endpoint: h.Endpoint,
		TLS:         dockerTLSBody{CA: redactIf(h.TLS.CA), Cert: redactIf(h.TLS.Cert), Key: redactIf(h.TLS.Key)},
		ConfigError: h.DecryptErr, CreatedAt: h.CreatedAt, UpdatedAt: h.UpdatedAt,
	}
}

// mergeDockerTLS resolves a request against the stored TLS material.
func mergeDockerTLS(stored model.DockerTLS, req dockerTLSRequest) model.DockerTLS {
	pick := func(r *string, old string) string {
		if r == nil || *r == notify.Redacted {
			return old
		}
		return strings.TrimSpace(*r)
	}
	return model.DockerTLS{CA: pick(req.CA, stored.CA), Cert: pick(req.Cert, stored.Cert), Key: pick(req.Key, stored.Key)}
}

// validateDockerHostBody normalizes and validates name, endpoint and TLS.
// A unix socket endpoint is only accepted when it is the operator's own
// DOCKER_HOST: team editors must not be able to point the server at
// arbitrary local sockets.
func validateDockerHostBody(d Deps, name, endpoint string, tls model.DockerTLS) (string, string, error) {
	name = strings.TrimSpace(name)
	endpoint = strings.TrimSpace(endpoint)
	if name == "" {
		return "", "", huma.Error422UnprocessableEntity("name is required")
	}
	ep, err := dockerapi.ParseEndpoint(endpoint, !tls.Empty())
	if err != nil {
		return "", "", huma.Error422UnprocessableEntity(err.Error())
	}
	if ep.Unix != "" && endpoint != d.Config.DockerHost {
		return "", "", huma.Error422UnprocessableEntity("unix socket endpoints are only allowed for the server's DOCKER_HOST")
	}
	if err := dockerapi.ValidateTLS(tls); err != nil {
		return "", "", huma.Error422UnprocessableEntity(err.Error())
	}
	return name, endpoint, nil
}

type dockerHostOutput struct {
	Body dockerHostBody
}

type listDockerHostsOutput struct {
	Body struct {
		Hosts []dockerHostBody `json:"hosts"`
	}
}

type testDockerHostOutput struct {
	Body struct {
		OK bool `json:"ok"`
	}
}

type listContainersOutput struct {
	Body struct {
		Containers []dockerapi.Container `json:"containers"`
	}
}

func registerDockerHostRoutes(api huma.API, d Deps) {
	huma.Get(api, "/api/teams/{teamID}/docker-hosts", func(ctx context.Context, in *teamPathInput) (*listDockerHostsOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		hs, err := d.Store.ListDockerHosts(ctx, in.TeamID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listDockerHostsOutput{}
		out.Body.Hosts = make([]dockerHostBody, len(hs))
		for i, h := range hs {
			out.Body.Hosts[i] = toDockerHostBody(h)
		}
		return out, nil
	})

	huma.Get(api, "/api/teams/{teamID}/docker-hosts/{hostID}", func(ctx context.Context, in *dockerHostPathInput) (*dockerHostOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		h, err := d.Store.GetDockerHost(ctx, in.TeamID, in.HostID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		return &dockerHostOutput{Body: toDockerHostBody(h)}, nil
	})

	huma.Post(api, "/api/teams/{teamID}/docker-hosts", func(ctx context.Context, in *createDockerHostInput) (*dockerHostOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		tls := mergeDockerTLS(model.DockerTLS{}, in.Body.TLS)
		name, endpoint, err := validateDockerHostBody(d, in.Body.Name, in.Body.Endpoint, tls)
		if err != nil {
			return nil, err
		}
		created, err := d.Store.CreateDockerHost(ctx, model.DockerHost{TeamID: in.TeamID, Name: name, Endpoint: endpoint, TLS: tls})
		if err != nil {
			return nil, mapDockerHostErr(d, err)
		}
		d.Docker.Upsert(created)
		writeAudit(ctx, d, &in.TeamID, act, "docker_host.create", "docker_host", &created.ID,
			map[string]any{"name": created.Name, "endpoint": created.Endpoint})
		return &dockerHostOutput{Body: toDockerHostBody(created)}, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated })

	huma.Put(api, "/api/teams/{teamID}/docker-hosts/{hostID}", func(ctx context.Context, in *updateDockerHostInput) (*dockerHostOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		existing, err := d.Store.GetDockerHost(ctx, in.TeamID, in.HostID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		if existing.DecryptErr != "" && (in.Body.TLS.CA == nil || in.Body.TLS.Cert == nil || in.Body.TLS.Key == nil ||
			*in.Body.TLS.CA == notify.Redacted || *in.Body.TLS.Cert == notify.Redacted || *in.Body.TLS.Key == notify.Redacted) {
			return nil, huma.Error422UnprocessableEntity(
				"cannot keep existing TLS material: it was not decryptable, please re-enter or clear all three fields")
		}
		tls := mergeDockerTLS(existing.TLS, in.Body.TLS)
		name, endpoint, err := validateDockerHostBody(d, in.Body.Name, in.Body.Endpoint, tls)
		if err != nil {
			return nil, err
		}
		updated, err := d.Store.UpdateDockerHost(ctx, model.DockerHost{
			ID: in.HostID, TeamID: in.TeamID, Name: name, Endpoint: endpoint, TLS: tls,
		})
		if err != nil {
			return nil, mapDockerHostErr(d, err)
		}
		d.Docker.Upsert(updated)
		writeAudit(ctx, d, &in.TeamID, act, "docker_host.update", "docker_host", &updated.ID,
			map[string]any{"name": updated.Name, "endpoint": updated.Endpoint})
		return &dockerHostOutput{Body: toDockerHostBody(updated)}, nil
	})

	huma.Delete(api, "/api/teams/{teamID}/docker-hosts/{hostID}", func(ctx context.Context, in *dockerHostPathInput) (*emptyOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		existing, err := d.Store.GetDockerHost(ctx, in.TeamID, in.HostID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		if err := d.Store.DeleteDockerHost(ctx, in.TeamID, in.HostID); err != nil {
			if errors.Is(err, store.ErrInUse) {
				return nil, huma.Error409Conflict("docker host is used by a monitor")
			}
			return nil, mapStoreErr(d, err)
		}
		d.Docker.Remove(in.HostID)
		writeAudit(ctx, d, &in.TeamID, act, "docker_host.delete", "docker_host", &in.HostID,
			map[string]any{"name": existing.Name, "endpoint": existing.Endpoint})
		return nil, nil
	})

	huma.Post(api, "/api/teams/{teamID}/docker-hosts/{hostID}/test", func(ctx context.Context, in *dockerHostPathInput) (*testDockerHostOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		h, err := d.Store.GetDockerHost(ctx, in.TeamID, in.HostID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		writeAudit(ctx, d, &in.TeamID, act, "docker_host.test", "docker_host", &h.ID,
			map[string]any{"name": h.Name, "endpoint": h.Endpoint})
		client, err := dockerClient(h)
		if err != nil {
			return nil, huma.NewError(http.StatusBadGateway, "ping failed: "+err.Error())
		}
		cctx, cancel := context.WithTimeout(ctx, dockerCallTimeout)
		defer cancel()
		if err := client.Ping(cctx); err != nil {
			return nil, huma.NewError(http.StatusBadGateway, "ping failed: "+dockerErrMessage(err))
		}
		out := &testDockerHostOutput{}
		out.Body.OK = true
		return out, nil
	})

	huma.Get(api, "/api/teams/{teamID}/docker-hosts/{hostID}/containers", func(ctx context.Context, in *dockerHostPathInput) (*listContainersOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		h, err := d.Store.GetDockerHost(ctx, in.TeamID, in.HostID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		client, err := dockerClient(h)
		if err != nil {
			return nil, huma.NewError(http.StatusBadGateway, "cannot list containers: "+err.Error())
		}
		cctx, cancel := context.WithTimeout(ctx, dockerCallTimeout)
		defer cancel()
		cs, err := client.ListContainers(cctx)
		if err != nil {
			return nil, huma.NewError(http.StatusBadGateway, "cannot list containers: "+dockerErrMessage(err))
		}
		out := &listContainersOutput{}
		out.Body.Containers = cs
		return out, nil
	})
}

// dockerClient builds a throwaway client from the stored host, so the test
// and the picker reflect what is saved rather than the registry's cache.
func dockerClient(h model.DockerHost) (*dockerapi.Client, error) {
	if h.DecryptErr != "" {
		return nil, errors.New(h.DecryptErr)
	}
	return dockerapi.New(h)
}

func dockerErrMessage(err error) string {
	var se *dockerapi.StatusError
	if errors.As(err, &se) {
		return se.Error()
	}
	return checker.DescribeError(err, int(dockerCallTimeout/time.Second))
}

func mapDockerHostErr(d Deps, err error) error {
	if errors.Is(err, store.ErrConflict) {
		return huma.Error409Conflict("a docker host with that name already exists")
	}
	return mapStoreErr(d, err)
}

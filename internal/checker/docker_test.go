package checker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/davidsugianto/upsera/internal/dockerapi"
	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
)

// fakeEngine serves /containers/{name}/json from a name → state-JSON map.
func fakeEngine(t *testing.T, states map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/json")
		if name == "broken" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		st, ok := states[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"State":%s}`, st)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dockerChecker(t *testing.T, srv *httptest.Server, teamID int64) (*Checker, int64) {
	t.Helper()
	reg := dockerapi.NewRegistry()
	reg.Upsert(model.DockerHost{ID: 5, TeamID: teamID, Endpoint: "tcp://" + srv.Listener.Addr().String()})
	c := newTestChecker()
	c.SetDockerHosts(reg)
	return c, 5
}

func TestCheckDocker(t *testing.T) {
	srv := fakeEngine(t, map[string]string{
		"ok":        `{"Status":"running"}`,
		"healthy":   `{"Status":"running","Health":{"Status":"healthy"}}`,
		"starting":  `{"Status":"running","Health":{"Status":"starting"}}`,
		"unhealthy": `{"Status":"running","Health":{"Status":"unhealthy"}}`,
		"killed":    `{"Status":"exited","ExitCode":137}`,
		"oom":       `{"Status":"exited","ExitCode":137,"OOMKilled":true}`,
		"loop":      `{"Status":"restarting","ExitCode":1}`,
		"paused":    `{"Status":"paused"}`,
		"created":   `{"Status":"created"}`,
	})
	c, hostID := dockerChecker(t, srv, 1)
	cases := []struct {
		container string
		want      model.Status
		msg       string
	}{
		{"ok", model.StatusUp, ""},
		{"healthy", model.StatusUp, ""},
		{"starting", model.StatusDown, "health check starting"},
		{"unhealthy", model.StatusDown, "unhealthy"},
		{"killed", model.StatusDown, "exited (137)"},
		{"oom", model.StatusDown, "exited (137, OOM killed)"},
		{"loop", model.StatusDown, "restarting (last exit 1)"},
		{"paused", model.StatusDown, "paused"},
		{"created", model.StatusDown, "created"},
		{"gone", model.StatusDown, "container not found"},
		{"broken", model.StatusDown, "docker API returned HTTP 500"},
	}
	for _, tc := range cases {
		t.Run(tc.container, func(t *testing.T) {
			m := mustMonitor(t, model.TypeDocker, DockerConfig{DockerHostID: hostID, Container: tc.container}, 5)
			m.TeamID = 1
			r := c.Check(context.Background(), m)
			if r.Status != tc.want || r.Message != tc.msg {
				t.Fatalf("got %v %q, want %v %q", r.Status, r.Message, tc.want, tc.msg)
			}
		})
	}
}

func TestCheckDockerHostProblems(t *testing.T) {
	srv := fakeEngine(t, nil)
	c, hostID := dockerChecker(t, srv, 1)
	m := mustMonitor(t, model.TypeDocker, DockerConfig{DockerHostID: hostID, Container: "x"}, 5)

	// A host belonging to another team must not be usable.
	m.TeamID = 2
	if r := c.Check(context.Background(), m); r.Status != model.StatusDown || r.Message != "docker host not found" {
		t.Fatalf("cross-team host: %+v", r)
	}
	// Unknown host.
	m.TeamID = 1
	m.Config = []byte(`{"docker_host_id":99,"container":"x"}`)
	if r := c.Check(context.Background(), m); r.Message != "docker host not found" {
		t.Fatalf("unknown host: %+v", r)
	}
	// Unreachable host.
	srv.Close()
	m.Config = []byte(fmt.Sprintf(`{"docker_host_id":%d,"container":"x"}`, hostID))
	if r := c.Check(context.Background(), m); r.Status != model.StatusDown || !strings.HasPrefix(r.Message, "docker host unreachable") {
		t.Fatalf("unreachable host: %+v", r)
	}
	// No registry wired.
	if r := newTestChecker().Check(context.Background(), m); r.Status != model.StatusDown {
		t.Fatalf("no registry: %+v", r)
	}
}

// The docker checker may reach the configured Docker host, but ordinary
// monitors still must not (netpolicy refuses the DOCKER_HOST address).
func TestDockerHostStaysBlockedForOtherMonitors(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"State":{"Status":"running"}}`))
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	c := New(netpolicy.New("tcp://" + addr))
	reg := dockerapi.NewRegistry()
	reg.Upsert(model.DockerHost{ID: 1, TeamID: 1, Endpoint: "tcp://" + addr})
	c.SetDockerHosts(reg)

	dm := mustMonitor(t, model.TypeDocker, DockerConfig{DockerHostID: 1, Container: "x"}, 5)
	dm.TeamID = 1
	if r := c.Check(context.Background(), dm); r.Status != model.StatusUp {
		t.Fatalf("docker monitor must reach the docker host: %+v", r)
	}
	hm := mustMonitor(t, model.TypeHTTP, HTTPConfig{URL: "http://" + addr + "/"}, 5)
	before := hits.Load()
	r := c.Check(context.Background(), hm)
	if r.Status != model.StatusDown || !strings.Contains(r.Message, "blocked by outbound policy") {
		t.Fatalf("http monitor must be blocked from the docker host: %+v", r)
	}
	if hits.Load() != before {
		t.Fatal("the blocked request still reached the server")
	}
}

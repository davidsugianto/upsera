package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/store"
	"github.com/davidsugianto/upsera/internal/testutil"
)

func TestDockerHosts(t *testing.T) {
	st, url := testutil.Store(t)
	ctx := context.Background()

	if _, ok, err := st.EnsureLocalDockerHost(ctx, "tcp://docker-proxy:2375"); err != nil || ok {
		t.Fatalf("no team yet: ok=%v err=%v", ok, err)
	}
	admin, team, err := st.Setup(ctx, "a@example.com", "A", "hash", "Ops")
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateTeam(ctx, "Other", admin.ID)
	if err != nil {
		t.Fatal(err)
	}

	tlsIn := model.DockerTLS{CA: "ca-pem", Cert: "cert-pem", Key: "key-pem"}
	h, err := st.CreateDockerHost(ctx, model.DockerHost{TeamID: team.ID, Name: "remote", Endpoint: "tcp://10.0.0.5:2376", TLS: tlsIn})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetDockerHost(ctx, team.ID, h.ID)
	if err != nil || got.TLS != tlsIn || got.DecryptErr != "" || got.Endpoint != "tcp://10.0.0.5:2376" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var stored string
	if err := conn.QueryRow(ctx, "SELECT tls FROM upsera.docker_hosts WHERE id = $1", h.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "v1:") || strings.Contains(stored, "key-pem") {
		t.Fatalf("tls must be stored encrypted, got %q", stored)
	}

	t.Run("unique name per team", func(t *testing.T) {
		_, err := st.CreateDockerHost(ctx, model.DockerHost{TeamID: team.ID, Name: "remote", Endpoint: "tcp://x:1"})
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("want ErrConflict, got %v", err)
		}
		if _, err := st.CreateDockerHost(ctx, model.DockerHost{TeamID: other.ID, Name: "remote", Endpoint: "tcp://x:1"}); err != nil {
			t.Fatalf("same name in another team: %v", err)
		}
	})

	t.Run("team scoping", func(t *testing.T) {
		if _, err := st.GetDockerHost(ctx, other.ID, h.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("other team's get: %v", err)
		}
		if _, err := st.UpdateDockerHost(ctx, model.DockerHost{ID: h.ID, TeamID: other.ID, Name: "x", Endpoint: "tcp://x:1"}); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("other team's update: %v", err)
		}
		if err := st.DeleteDockerHost(ctx, other.ID, h.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("other team's delete: %v", err)
		}
		mine, _ := st.ListDockerHosts(ctx, team.ID)
		if len(mine) != 1 {
			t.Fatalf("list: %+v", mine)
		}
		all, _ := st.ListAllDockerHosts(ctx)
		if len(all) != 2 {
			t.Fatalf("list all: %+v", all)
		}
	})

	t.Run("update", func(t *testing.T) {
		u, err := st.UpdateDockerHost(ctx, model.DockerHost{ID: h.ID, TeamID: team.ID, Name: "renamed", Endpoint: "tcp://10.0.0.6:2375"})
		if err != nil || u.Name != "renamed" || !u.TLS.Empty() {
			t.Fatalf("update: %+v %v", u, err)
		}
	})

	t.Run("delete refused while a monitor uses it", func(t *testing.T) {
		m, err := st.CreateMonitor(ctx, model.Monitor{
			TeamID: team.ID, Name: "web", Type: model.TypeDocker,
			Config:    []byte(fmt.Sprintf(`{"docker_host_id":%d,"container":"web"}`, h.ID)),
			IntervalS: 60, RetryIntervalS: 60, Retries: 1, TimeoutS: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteDockerHost(ctx, team.ID, h.ID); !errors.Is(err, store.ErrInUse) {
			t.Fatalf("want ErrInUse, got %v", err)
		}
		if err := st.DeleteMonitor(ctx, team.ID, m.ID); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteDockerHost(ctx, team.ID, h.ID); err != nil {
			t.Fatalf("delete after monitor removed: %v", err)
		}
	})

	t.Run("ensure local host", func(t *testing.T) {
		// first team has no hosts now
		l, ok, err := st.EnsureLocalDockerHost(ctx, "tcp://docker-proxy:2375")
		if err != nil || !ok || l.Name != "local" || l.TeamID != team.ID {
			t.Fatalf("ensure: %+v %v %v", l, ok, err)
		}
		if _, ok, err := st.EnsureLocalDockerHost(ctx, "tcp://docker-proxy:2375"); err != nil || ok {
			t.Fatalf("second ensure must be a no-op: ok=%v err=%v", ok, err)
		}
	})
}

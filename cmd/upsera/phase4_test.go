package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/config"
	"github.com/davidsugianto/upsera/internal/testutil"
)

// TestPhase4DoneWhen: "docker stop on a watched container triggers an
// alert". The real server watches a container on the seeded "local" host (a
// fake Engine API); when the container exits, the Telegram alert says
// "exited (137)", and when it comes back the alert resolves.
func TestPhase4DoneWhen(t *testing.T) {
	dbURL := testutil.PostgresURL(t)
	tg, tgSrv := newFakeTelegram(t)

	var exited atomic.Bool
	engine := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/demo/json":
			if exited.Load() {
				io.WriteString(w, `{"State":{"Status":"exited","ExitCode":137}}`)
				return
			}
			io.WriteString(w, `{"State":{"Status":"running","Health":{"Status":"healthy"}}}`)
		case "/containers/json":
			io.WriteString(w, `[{"Id":"0123456789abcdef","Names":["/demo"],"Image":"nginx","State":"running","Status":"Up"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	// The fake Engine API listens on a unix socket: netpolicy refuses the
	// DOCKER_HOST IP for notifiers too, and the fake Telegram shares
	// 127.0.0.1 with any TCP listener.
	dir, err := os.MkdirTemp("", "ups")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "d.sock")
	engine.Listener, err = net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	engine.Start()
	defer engine.Close()

	base, _ := startServer(t, dbURL, func(c *config.Config) {
		c.TelegramAPIURL = tgSrv.URL
		c.DockerHost = "unix://" + sock
	})
	teamURL, bearer := setupAdmin(t, base)

	// The DOCKER_HOST endpoint was seeded as the team's "local" host.
	var hosts struct {
		Hosts []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"hosts"`
	}
	call(t, http.DefaultClient, "GET", teamURL+"/docker-hosts", bearer, nil, http.StatusOK, &hosts)
	if len(hosts.Hosts) != 1 || hosts.Hosts[0].Name != "local" {
		t.Fatalf("seeded hosts = %+v", hosts)
	}
	hostID := hosts.Hosts[0].ID
	var picker struct {
		Containers []struct {
			Name string `json:"name"`
		} `json:"containers"`
	}
	call(t, http.DefaultClient, "GET", fmt.Sprintf("%s/docker-hosts/%d/containers", teamURL, hostID), bearer, nil, http.StatusOK, &picker)
	if len(picker.Containers) != 1 || picker.Containers[0].Name != "demo" {
		t.Fatalf("picker = %+v", picker)
	}

	call(t, http.DefaultClient, "POST", teamURL+"/channels", bearer, map[string]any{
		"type": "telegram", "name": "tg", "is_default": true,
		"config": map[string]any{"bot_token": "123456:" + strings.Repeat("A", 35), "chat_id": "4242"},
	}, http.StatusCreated, nil)

	var mon struct {
		ID int64 `json:"id"`
	}
	call(t, http.DefaultClient, "POST", teamURL+"/monitors", bearer, map[string]any{
		"name": "demo", "type": "docker", "interval_s": 20, "retry_interval_s": 20, "timeout_s": 5, "retries": 0,
		"config": map[string]any{"docker_host_id": hostID, "container": "demo"},
	}, http.StatusCreated, &mon)

	state := func() string {
		var m struct {
			State *struct {
				Status string `json:"status"`
			} `json:"state"`
		}
		call(t, http.DefaultClient, "GET", fmt.Sprintf("%s/monitors/%d", teamURL, mon.ID), bearer, nil, http.StatusOK, &m)
		if m.State == nil {
			return ""
		}
		return m.State.Status
	}
	waitFor(t, 40*time.Second, func() bool { return state() == "up" })

	exited.Store(true) // docker stop
	waitFor(t, 45*time.Second, func() bool { return len(tg.withPrefix("[DOWN] demo")) == 1 })
	if text := tg.withPrefix("[DOWN] demo")[0].Text; !strings.Contains(text, "exited (137)") {
		t.Fatalf("alert text does not carry the reason: %q", text)
	}

	exited.Store(false) // docker start
	waitFor(t, 45*time.Second, func() bool { return len(tg.withPrefix("[UP] demo")) == 1 })
}

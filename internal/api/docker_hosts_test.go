package api_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/api"
	"github.com/davidsugianto/upsera/internal/config"
	"github.com/davidsugianto/upsera/internal/dockerapi"
	"github.com/davidsugianto/upsera/internal/events"
	"github.com/davidsugianto/upsera/internal/netpolicy"
	"github.com/davidsugianto/upsera/internal/testutil"
)

func selfSignedPair(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "upsera-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
}

func TestDockerHostsAPI(t *testing.T) {
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_ping":
			io.WriteString(w, "OK")
		case "/containers/json":
			io.WriteString(w, `[{"Id":"abcdef0123456789","Names":["/web"],"Image":"nginx","State":"running","Status":"Up 1 hour"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer engine.Close()
	engineURL := "tcp://" + engine.Listener.Addr().String()

	st, _ := testutil.Store(t)
	h := api.NewRouter(api.Deps{
		Store: st, Runner: newFakeRunner(), Alerting: newFakeAlerting(), Maintenance: newFakeMaintenance(),
		Docker: dockerapi.NewRegistry(), Policy: netpolicy.New(""),
		Config: config.Config{BaseURL: "http://upsera.test", Port: 3080, DockerHost: engineURL},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test", Events: events.NewHub(),
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	base := srv.URL

	admin := newClient(t)
	resp, body := do(t, admin, http.MethodPost, base+"/api/setup", "", "", map[string]any{
		"email": "admin@example.com", "name": "Admin", "password": "correct horse battery", "team_name": "Ops",
	})
	requireStatus(t, resp, http.StatusCreated, body)
	var me meResponse
	decodeInto(t, body, &me)
	csrf, teamID := me.CSRFToken, me.Teams[0].ID
	hostsURL := fmt.Sprintf("%s/api/teams/%d/docker-hosts", base, teamID)

	// A viewer for the permission checks.
	resp, body = do(t, admin, http.MethodPost, base+"/api/admin/users", csrf, "", map[string]any{
		"email": "viewer@example.com", "name": "Viewer", "password": "viewer password", "is_admin": false,
	})
	requireStatus(t, resp, http.StatusCreated, body)
	resp, body = do(t, admin, http.MethodPost, fmt.Sprintf("%s/api/teams/%d/members", base, teamID), csrf, "", map[string]any{
		"email": "viewer@example.com", "role": "viewer",
	})
	requireStatus(t, resp, http.StatusCreated, body)
	viewer := newClient(t)
	resp, body = do(t, viewer, http.MethodPost, base+"/api/auth/login", "", "", map[string]any{
		"email": "viewer@example.com", "password": "viewer password",
	})
	requireStatus(t, resp, http.StatusOK, body)
	var vme meResponse
	decodeInto(t, body, &vme)

	var localID float64
	t.Run("setup seeds the local host", func(t *testing.T) {
		resp, body := do(t, admin, http.MethodGet, hostsURL, "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		hosts := body["hosts"].([]any)
		if len(hosts) != 1 {
			t.Fatalf("hosts: %v", hosts)
		}
		h := hosts[0].(map[string]any)
		if h["name"] != "local" || h["endpoint"] != engineURL {
			t.Fatalf("seeded host: %v", h)
		}
		localID = h["id"].(float64)
	})

	localURL := func() string { return fmt.Sprintf("%s/%d", hostsURL, int64(localID)) }

	t.Run("test and container picker", func(t *testing.T) {
		resp, body := do(t, admin, http.MethodPost, localURL()+"/test", csrf, "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		if body["ok"] != true {
			t.Fatalf("test: %v", body)
		}
		resp, body = do(t, viewer, http.MethodGet, localURL()+"/containers", "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		cs := body["containers"].([]any)
		if len(cs) != 1 || cs[0].(map[string]any)["name"] != "web" || cs[0].(map[string]any)["id"] != "abcdef012345" {
			t.Fatalf("containers: %v", cs)
		}
	})

	t.Run("unreachable host reports 502 without leaking the address", func(t *testing.T) {
		resp, body := do(t, admin, http.MethodPost, hostsURL, csrf, "", map[string]any{"name": "dead", "endpoint": "tcp://127.0.0.1:1"})
		requireStatus(t, resp, http.StatusCreated, body)
		id := int64(body["id"].(float64))
		resp, body = do(t, admin, http.MethodPost, fmt.Sprintf("%s/%d/test", hostsURL, id), csrf, "", nil)
		requireStatus(t, resp, http.StatusBadGateway, body)
		resp, body = do(t, admin, http.MethodGet, fmt.Sprintf("%s/%d/containers", hostsURL, id), "", "", nil)
		requireStatus(t, resp, http.StatusBadGateway, body)
		resp, body = do(t, admin, http.MethodDelete, fmt.Sprintf("%s/%d", hostsURL, id), csrf, "", nil)
		requireStatus(t, resp, http.StatusNoContent, body)
	})

	t.Run("validation and duplicates", func(t *testing.T) {
		for _, bad := range []map[string]any{
			{"name": "x", "endpoint": "docker-proxy:2375"},
			{"name": "x", "endpoint": "tcp://h/path"},
			{"name": "x", "endpoint": "tcp://h", "tls": map[string]any{"cert": "nope", "key": "nope"}},
			{"name": "x", "endpoint": "tcp://h", "tls": map[string]any{"cert": "only-cert"}},
			{"name": "  ", "endpoint": "tcp://h"},
			{"name": "x", "endpoint": "unix:///var/run/docker.sock"},
		} {
			resp, body := do(t, admin, http.MethodPost, hostsURL, csrf, "", bad)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("%v: status %d body %v", bad, resp.StatusCode, body)
			}
		}
		resp, body := do(t, admin, http.MethodPost, hostsURL, csrf, "", map[string]any{"name": "local", "endpoint": "tcp://h:1"})
		requireStatus(t, resp, http.StatusConflict, body)
	})

	t.Run("viewer can read but not write", func(t *testing.T) {
		resp, body := do(t, viewer, http.MethodPost, hostsURL, vme.CSRFToken, "", map[string]any{"name": "v", "endpoint": "tcp://h:1"})
		requireStatus(t, resp, http.StatusForbidden, body)
		resp, body = do(t, viewer, http.MethodPost, localURL()+"/test", vme.CSRFToken, "", nil)
		requireStatus(t, resp, http.StatusForbidden, body)
		resp, body = do(t, viewer, http.MethodDelete, localURL(), vme.CSRFToken, "", nil)
		requireStatus(t, resp, http.StatusForbidden, body)
	})

	t.Run("tls is redacted, kept on omit and cleared on empty", func(t *testing.T) {
		cert, key := selfSignedPair(t)
		resp, body := do(t, admin, http.MethodPost, hostsURL, csrf, "", map[string]any{
			"name": "remote", "endpoint": "tcp://10.0.0.5", "tls": map[string]any{"ca": cert, "cert": cert, "key": key},
		})
		requireStatus(t, resp, http.StatusCreated, body)
		tls := body["tls"].(map[string]any)
		if tls["ca"] != "********" || tls["cert"] != "********" || tls["key"] != "********" {
			t.Fatalf("tls must be redacted: %v", tls)
		}
		id := int64(body["id"].(float64))
		url := fmt.Sprintf("%s/%d", hostsURL, id)

		// Omitted tls and placeholder values keep the secrets.
		resp, body = do(t, admin, http.MethodPut, url, csrf, "", map[string]any{
			"name": "remote2", "endpoint": "tcp://10.0.0.6", "tls": map[string]any{"key": "********"},
		})
		requireStatus(t, resp, http.StatusOK, body)
		tls = body["tls"].(map[string]any)
		if body["name"] != "remote2" || tls["ca"] != "********" || tls["key"] != "********" {
			t.Fatalf("secrets must be kept: %v", body)
		}
		// Empty strings clear them.
		resp, body = do(t, admin, http.MethodPut, url, csrf, "", map[string]any{
			"name": "remote2", "endpoint": "tcp://10.0.0.6", "tls": map[string]any{"ca": "", "cert": "", "key": ""},
		})
		requireStatus(t, resp, http.StatusOK, body)
		tls = body["tls"].(map[string]any)
		if tls["ca"] != "" || tls["cert"] != "" || tls["key"] != "" {
			t.Fatalf("tls must be cleared: %v", tls)
		}
	})

	t.Run("monitors must use a host of their team and pin it", func(t *testing.T) {
		monitorsURL := fmt.Sprintf("%s/api/teams/%d/monitors", base, teamID)
		mon := func(hostID int64) map[string]any {
			return map[string]any{
				"name": "web container", "type": "docker", "interval_s": 60, "retry_interval_s": 30, "timeout_s": 10,
				"config": map[string]any{"docker_host_id": hostID, "container": "web"},
			}
		}
		resp, body := do(t, admin, http.MethodPost, monitorsURL, csrf, "", mon(99999))
		requireStatus(t, resp, http.StatusUnprocessableEntity, body)
		resp, body = do(t, admin, http.MethodPost, monitorsURL, csrf, "", mon(int64(localID)))
		requireStatus(t, resp, http.StatusCreated, body)
		monID := int64(body["id"].(float64))

		resp, body = do(t, admin, http.MethodDelete, localURL(), csrf, "", nil)
		requireStatus(t, resp, http.StatusConflict, body)

		resp, body = do(t, admin, http.MethodDelete, fmt.Sprintf("%s/%d", monitorsURL, monID), csrf, "", nil)
		requireStatus(t, resp, http.StatusNoContent, body)
		resp, body = do(t, admin, http.MethodDelete, localURL(), csrf, "", nil)
		requireStatus(t, resp, http.StatusNoContent, body)
		resp, body = do(t, admin, http.MethodGet, localURL(), "", "", nil)
		requireStatus(t, resp, http.StatusNotFound, body)
	})
}

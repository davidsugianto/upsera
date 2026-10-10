package dockerapi

import (
	"context"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

func engine(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("OK")) })
	mux.HandleFunc("/containers/json", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("all") != "1" {
			t.Errorf("list must ask for all containers: %s", r.URL.RawQuery)
		}
		w.Write([]byte(`[{"Id":"0123456789abcdef","Names":["/web"],"Image":"nginx","State":"running","Status":"Up 2 hours"}]`))
	})
	mux.HandleFunc("/containers/web/json", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"State":{"Status":"running","ExitCode":0,"Health":{"Status":"healthy"}}}`))
	})
	mux.HandleFunc("/containers/dead/json", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"State":{"Status":"exited","ExitCode":137,"OOMKilled":true}}`))
	})
	mux.HandleFunc("/containers/boom/json", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "secret internal detail", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestClient(t *testing.T) {
	srv := engine(t)
	c, err := New(model.DockerHost{Endpoint: "tcp://" + srv.Listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	st, err := c.Inspect(ctx, "web")
	if err != nil || st.Status != "running" || st.Health != "healthy" {
		t.Fatalf("inspect web: %+v %v", st, err)
	}
	st, err = c.Inspect(ctx, "dead")
	if err != nil || st.Status != "exited" || st.ExitCode != 137 || !st.OOMKilled || st.Health != "" {
		t.Fatalf("inspect dead: %+v %v", st, err)
	}
	if _, err := c.Inspect(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown container: %v", err)
	}
	_, err = c.Inspect(ctx, "boom")
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 500 || err.Error() != "docker API returned HTTP 500" {
		t.Fatalf("5xx must not leak the body: %v", err)
	}
	if _, err := c.Inspect(ctx, "../etc"); err == nil {
		t.Fatal("path traversal in container name must be rejected")
	}
	list, err := c.ListContainers(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "web" || list[0].ID != "0123456789ab" || list[0].State != "running" {
		t.Fatalf("list: %+v %v", list, err)
	}
}

func TestClientUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	srv := &http.Server{Handler: engine(t).Config.Handler}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	c, err := New(model.DockerHost{Endpoint: "unix://" + sock})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping over unix socket: %v", err)
	}
}

func TestClientRefusesMetadata(t *testing.T) {
	c, err := New(model.DockerHost{Endpoint: "tcp://169.254.169.254:2375"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err == nil {
		t.Fatal("link-local metadata address must be refused even for docker hosts")
	}
}

func TestParseEndpoint(t *testing.T) {
	ok := []struct {
		in     string
		tls    bool
		host   string
		secure bool
		unix   string
	}{
		{"tcp://docker-proxy:2375", false, "docker-proxy:2375", false, ""},
		{"tcp://10.0.0.5", false, "10.0.0.5:2375", false, ""},
		{"tcp://10.0.0.5", true, "10.0.0.5:2376", true, ""},
		{"https://h:9999", false, "h:9999", true, ""},
		{"http://h", false, "h:2375", false, ""},
		{"unix:///var/run/docker.sock", false, "", false, "/var/run/docker.sock"},
	}
	for _, c := range ok {
		ep, err := ParseEndpoint(c.in, c.tls)
		if err != nil || ep.Host != c.host || ep.Secure != c.secure || ep.Unix != c.unix {
			t.Errorf("ParseEndpoint(%q,%v) = %+v, %v", c.in, c.tls, ep, err)
		}
	}
	for _, in := range []string{"", "ftp://h", "tcp://", "tcp://h/path", "tcp://u:p@h", "unix://host/sock", "docker-proxy:2375", "tcp://h?x=1"} {
		if _, err := ParseEndpoint(in, false); err == nil {
			t.Errorf("ParseEndpoint(%q) should fail", in)
		}
	}
	if _, err := ParseEndpoint("unix:///s", true); err == nil {
		t.Error("TLS with unix socket should fail")
	}
	if _, err := ParseEndpoint("http://h", true); err == nil {
		t.Error("TLS with http:// should fail")
	}
}

func TestValidateTLS(t *testing.T) {
	if err := ValidateTLS(model.DockerTLS{}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTLS(model.DockerTLS{Cert: "x"}); err == nil {
		t.Error("cert without key")
	}
	if err := ValidateTLS(model.DockerTLS{Cert: "x", Key: "y"}); err == nil {
		t.Error("garbage pair")
	}
	if err := ValidateTLS(model.DockerTLS{CA: "garbage"}); err == nil {
		t.Error("garbage CA")
	}
}

func TestTLSClient(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("OK")) }))
	defer srv.Close()
	ca := string(pemCert(srv.Certificate().Raw))
	c, err := New(model.DockerHost{Endpoint: "tcp://" + srv.Listener.Addr().String(), TLS: model.DockerTLS{CA: ca}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("ping with trusted CA: %v", err)
	}
	// Without the CA the self-signed server certificate must be rejected.
	c2, err := New(model.DockerHost{Endpoint: "https://" + srv.Listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	if err := c2.Ping(context.Background()); err == nil {
		t.Fatal("untrusted certificate must fail")
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	r.Upsert(model.DockerHost{ID: 1, TeamID: 7, Endpoint: "tcp://h:2375"})
	r.Upsert(model.DockerHost{ID: 2, TeamID: 7, Endpoint: "bogus"})
	r.Upsert(model.DockerHost{ID: 3, TeamID: 7, Endpoint: "tcp://h", DecryptErr: "x"})
	if c, err := r.Client(7, 1); err != nil || c == nil {
		t.Fatalf("host 1: %v", err)
	}
	if _, err := r.Client(8, 1); err == nil {
		t.Error("other team's host must not resolve")
	}
	if _, err := r.Client(7, 2); err == nil {
		t.Error("invalid endpoint must error")
	}
	if _, err := r.Client(7, 3); err == nil {
		t.Error("undecryptable host must error")
	}
	r.Remove(1)
	if _, err := r.Client(7, 1); err == nil {
		t.Error("removed host must not resolve")
	}
}

func pemCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

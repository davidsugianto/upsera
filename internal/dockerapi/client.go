// Package dockerapi is a minimal Docker Engine API client (container list
// and inspect, ping) over plain net/http, plus the registry of configured
// Docker hosts the docker checker reads from. It deliberately has no
// dependency on the Docker SDK.
package dockerapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
)

// ErrNotFound is returned by Inspect for an unknown container.
var ErrNotFound = errors.New("container not found")

// maxBody caps how much of an Engine API response is read.
const maxBody = 8 << 20

var containerRef = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// ValidContainer reports whether s is a plausible container name or id.
func ValidContainer(s string) bool { return len(s) <= 128 && containerRef.MatchString(s) }

// Endpoint is a parsed Docker host endpoint.
type Endpoint struct {
	Unix   string // socket path; empty for TCP
	Host   string // host:port for TCP
	Secure bool   // https
}

// ParseEndpoint validates endpoint. Accepted forms: tcp://host:port (https
// when TLS material is set), http://host[:port], https://host[:port] and
// unix:///path/to/socket. hasTLS says whether TLS material is configured.
func ParseEndpoint(endpoint string, hasTLS bool) (Endpoint, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return Endpoint{}, errors.New("invalid endpoint")
	}
	switch u.Scheme {
	case "unix":
		if u.Path == "" || u.Host != "" {
			return Endpoint{}, errors.New("unix endpoint must look like unix:///var/run/docker.sock")
		}
		if hasTLS {
			return Endpoint{}, errors.New("TLS cannot be used with a unix socket")
		}
		return Endpoint{Unix: u.Path}, nil
	case "tcp", "http", "https":
		if u.Hostname() == "" {
			return Endpoint{}, errors.New("endpoint needs a host")
		}
		if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
			return Endpoint{}, errors.New("endpoint must not contain a path, query or credentials")
		}
		secure := u.Scheme == "https" || (u.Scheme == "tcp" && hasTLS)
		if u.Scheme == "http" && hasTLS {
			return Endpoint{}, errors.New("TLS material needs a tcp:// or https:// endpoint")
		}
		port := u.Port()
		if port == "" {
			port = "2375"
			if secure {
				port = "2376"
			}
		}
		return Endpoint{Host: net.JoinHostPort(u.Hostname(), port), Secure: secure}, nil
	}
	return Endpoint{}, errors.New("endpoint must start with tcp://, http://, https:// or unix://")
}

// ValidateTLS checks that PEM material parses and that cert and key come
// as a pair.
func ValidateTLS(t model.DockerTLS) error {
	if (t.Cert == "") != (t.Key == "") {
		return errors.New("client certificate and key must be set together")
	}
	if t.Cert != "" {
		if _, err := tls.X509KeyPair([]byte(t.Cert), []byte(t.Key)); err != nil {
			return errors.New("client certificate/key is not a valid PEM pair")
		}
	}
	if t.CA != "" && !x509.NewCertPool().AppendCertsFromPEM([]byte(t.CA)) {
		return errors.New("CA is not valid PEM")
	}
	return nil
}

// Client talks to one Docker host. It is safe for concurrent use.
type Client struct {
	base string
	hc   *http.Client
}

// New builds a client for h. Connections bypass the outbound target policy
// except for its always-blocked ranges (link-local, metadata).
func New(h model.DockerHost) (*Client, error) {
	ep, err := ParseEndpoint(h.Endpoint, !h.TLS.Empty())
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		Proxy:                 nil,
		MaxIdleConns:          2,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	c := &Client{hc: &http.Client{
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	if ep.Unix != "" {
		path := ep.Unix
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}
		c.base = "http://docker"
		return c, nil
	}
	tr.DialContext = netpolicy.DialDockerHost
	c.base = "http://" + ep.Host
	if ep.Secure {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if h.TLS.CA != "" {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM([]byte(h.TLS.CA)) {
				return nil, errors.New("CA is not valid PEM")
			}
			cfg.RootCAs = pool
		}
		if h.TLS.Cert != "" {
			pair, err := tls.X509KeyPair([]byte(h.TLS.Cert), []byte(h.TLS.Key))
			if err != nil {
				return nil, errors.New("client certificate/key is not a valid PEM pair")
			}
			cfg.Certificates = []tls.Certificate{pair}
		}
		tr.TLSClientConfig = cfg
		c.base = "https://" + ep.Host
	}
	return c, nil
}

func (c *Client) get(ctx context.Context, path string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return resp.StatusCode, body, err
}

// StatusError is a non-success Engine API response. The body is never kept.
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("docker API returned HTTP %d", e.Code) }

// Ping calls /_ping.
func (c *Client) Ping(ctx context.Context) error {
	code, _, err := c.get(ctx, "/_ping")
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return &StatusError{Code: code}
	}
	return nil
}

// ContainerState is the part of a container's inspect output Upsera uses.
type ContainerState struct {
	Status    string // created, running, paused, restarting, removing, exited, dead
	ExitCode  int
	OOMKilled bool
	Health    string // "", starting, healthy, unhealthy
}

// Inspect returns the state of a container by name or id. An unknown
// container returns ErrNotFound.
func (c *Client) Inspect(ctx context.Context, container string) (ContainerState, error) {
	if !ValidContainer(container) {
		return ContainerState{}, errors.New("invalid container name")
	}
	code, body, err := c.get(ctx, "/containers/"+url.PathEscape(container)+"/json")
	if err != nil {
		return ContainerState{}, err
	}
	switch code {
	case http.StatusOK:
	case http.StatusNotFound:
		return ContainerState{}, ErrNotFound
	default:
		return ContainerState{}, &StatusError{Code: code}
	}
	var raw struct {
		State struct {
			Status    string
			ExitCode  int
			OOMKilled bool
			Health    *struct{ Status string }
		}
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return ContainerState{}, errors.New("invalid response from docker API")
	}
	st := ContainerState{Status: raw.State.Status, ExitCode: raw.State.ExitCode, OOMKilled: raw.State.OOMKilled}
	if raw.State.Health != nil {
		st.Health = raw.State.Health.Status
	}
	return st, nil
}

// Container is one row of the container picker.
type Container struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Image  string `json:"image"`
	State  string `json:"state"`
	Status string `json:"status"`
}

// ListContainers returns all containers (running or not), sorted as the
// Engine returns them, with leading slashes stripped from names.
func (c *Client) ListContainers(ctx context.Context) ([]Container, error) {
	code, body, err := c.get(ctx, "/containers/json?all=1")
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, &StatusError{Code: code}
	}
	var raw []struct {
		ID, Image, State, Status string
		Names                    []string
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("invalid response from docker API")
	}
	out := make([]Container, 0, len(raw))
	for _, r := range raw {
		name := ""
		if len(r.Names) > 0 {
			name = strings.TrimPrefix(r.Names[0], "/")
		}
		id := r.ID
		if len(id) > 12 {
			id = id[:12]
		}
		out = append(out, Container{ID: id, Name: name, Image: r.Image, State: r.State, Status: r.Status})
	}
	return out, nil
}

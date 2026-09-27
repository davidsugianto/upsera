// Package checker runs one check of a monitor and reports the result. The
// same code runs in the server and (later) in upsera-probe.
package checker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// Result is the outcome of one check.
type Result struct {
	Status  model.Status // StatusUp or StatusDown
	Latency time.Duration
	// Message is a short human-readable reason, capped at
	// model.MaxMessageLen. It never contains response bodies.
	Message string
	// TLSExpiresAt is the leaf certificate's NotAfter, for HTTPS targets.
	TLSExpiresAt *time.Time
}

// HTTPConfig configures http and keyword monitors.
type HTTPConfig struct {
	URL             string            `json:"url"`
	Method          string            `json:"method"`
	Headers         map[string]string `json:"headers,omitempty"`
	Body            string            `json:"body,omitempty"`
	AcceptedStatus  []string          `json:"accepted_status"` // "200-299", "301", ...
	MaxRedirects    *int              `json:"max_redirects"`   // default 10; 0 disables following redirects
	IgnoreTLSErrors bool              `json:"ignore_tls_errors"`
	// Keyword monitors only.
	Keyword       string `json:"keyword,omitempty"`
	InvertKeyword bool   `json:"invert_keyword,omitempty"`
	CaseSensitive bool   `json:"case_sensitive,omitempty"`
}

// TCPConfig configures tcp monitors.
type TCPConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// PingConfig configures ping monitors.
type PingConfig struct {
	Host  string `json:"host"`
	Count int    `json:"count"`
}

// DNSConfig configures dns monitors. The check is up when the lookup
// returns at least one record and, if Expected is set, one record contains
// Expected (case-insensitive).
type DNSConfig struct {
	Hostname   string `json:"hostname"`
	RecordType string `json:"record_type"`        // A, AAAA, CNAME, MX, TXT, NS
	Resolver   string `json:"resolver,omitempty"` // "host:port"; empty uses the system resolver
	Expected   string `json:"expected,omitempty"`
}

// PushConfig configures push monitors; it has no settings. The monitor goes
// down when no push arrives within its interval.
type PushConfig struct{}

// DefaultMaxRedirects is how many redirects an HTTP check follows when
// max_redirects is omitted.
const DefaultMaxRedirects = 10

// maxRedirects returns the configured redirect limit, defaulting when unset.
func (c HTTPConfig) maxRedirects() int {
	if c.MaxRedirects == nil {
		return DefaultMaxRedirects
	}
	return *c.MaxRedirects
}

var dnsTypes = []string{"A", "AAAA", "CNAME", "MX", "TXT", "NS"}

var httpMethods = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions}

// ValidateConfig checks the type-specific config, fills defaults and
// returns the normalized JSON to store. Errors are safe to show to users.
func ValidateConfig(t model.MonitorType, raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		raw = json.RawMessage("{}")
	}
	switch t {
	case model.TypeHTTP, model.TypeKeyword:
		var c HTTPConfig
		if err := decodeStrict(raw, &c); err != nil {
			return nil, err
		}
		if err := c.normalize(t == model.TypeKeyword); err != nil {
			return nil, err
		}
		return json.Marshal(c)
	case model.TypeTCP:
		var c TCPConfig
		if err := decodeStrict(raw, &c); err != nil {
			return nil, err
		}
		if err := validHost(c.Host); err != nil {
			return nil, err
		}
		if c.Port < 1 || c.Port > 65535 {
			return nil, errors.New("port must be between 1 and 65535")
		}
		return json.Marshal(c)
	case model.TypePing:
		var c PingConfig
		if err := decodeStrict(raw, &c); err != nil {
			return nil, err
		}
		if err := validHost(c.Host); err != nil {
			return nil, err
		}
		if c.Count == 0 {
			c.Count = 3
		}
		if c.Count < 1 || c.Count > 10 {
			return nil, errors.New("count must be between 1 and 10")
		}
		return json.Marshal(c)
	case model.TypeDNS:
		var c DNSConfig
		if err := decodeStrict(raw, &c); err != nil {
			return nil, err
		}
		if err := validHost(c.Hostname); err != nil {
			return nil, fmt.Errorf("hostname: %w", err)
		}
		c.RecordType = strings.ToUpper(strings.TrimSpace(c.RecordType))
		if c.RecordType == "" {
			c.RecordType = "A"
		}
		if !contains(dnsTypes, c.RecordType) {
			return nil, fmt.Errorf("record_type must be one of %s", strings.Join(dnsTypes, ", "))
		}
		if c.Resolver != "" {
			host, port, err := net.SplitHostPort(c.Resolver)
			if err != nil {
				host, port = strings.Trim(c.Resolver, "[]"), "53"
			}
			if err := validHost(host); err != nil {
				return nil, fmt.Errorf("resolver: %w", err)
			}
			if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
				return nil, errors.New("resolver port must be between 1 and 65535")
			}
			c.Resolver = net.JoinHostPort(host, port)
		}
		return json.Marshal(c)
	case model.TypePush:
		var c PushConfig
		if err := decodeStrict(raw, &c); err != nil {
			return nil, err
		}
		return json.Marshal(c)
	}
	return nil, fmt.Errorf("unknown monitor type %q", t)
}

func (c *HTTPConfig) normalize(keyword bool) error {
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("url must be an absolute http:// or https:// URL")
	}
	c.Method = strings.ToUpper(strings.TrimSpace(c.Method))
	if c.Method == "" {
		c.Method = http.MethodGet
	}
	if !contains(httpMethods, c.Method) {
		return fmt.Errorf("method must be one of %s", strings.Join(httpMethods, ", "))
	}
	if len(c.AcceptedStatus) == 0 {
		c.AcceptedStatus = []string{"200-299"}
	}
	for _, s := range c.AcceptedStatus {
		if _, _, err := parseStatusRange(s); err != nil {
			return err
		}
	}
	if c.MaxRedirects == nil {
		c.MaxRedirects = new(DefaultMaxRedirects)
	}
	if *c.MaxRedirects < 0 || *c.MaxRedirects > 20 {
		return errors.New("max_redirects must be between 0 and 20")
	}
	for k := range c.Headers {
		if k == "" || strings.ContainsAny(k, " \r\n:") {
			return fmt.Errorf("invalid header name %q", k)
		}
	}
	if keyword {
		if c.Keyword == "" {
			return errors.New("keyword is required for keyword monitors")
		}
	} else if c.Keyword != "" || c.InvertKeyword || c.CaseSensitive {
		return errors.New("keyword fields are only allowed on keyword monitors")
	}
	return nil
}

// parseStatusRange parses "200" or "200-299".
func parseStatusRange(s string) (lo, hi int, err error) {
	a, b, isRange := strings.Cut(strings.TrimSpace(s), "-")
	lo, err1 := strconv.Atoi(a)
	hi = lo
	var err2 error
	if isRange {
		hi, err2 = strconv.Atoi(b)
	}
	if err1 != nil || err2 != nil || lo < 100 || hi > 599 || lo > hi {
		return 0, 0, fmt.Errorf("invalid accepted_status %q: use a code like \"200\" or a range like \"200-299\"", s)
	}
	return lo, hi, nil
}

// statusAccepted reports whether code matches any accepted range.
func statusAccepted(accepted []string, code int) bool {
	for _, s := range accepted {
		lo, hi, err := parseStatusRange(s)
		if err == nil && code >= lo && code <= hi {
			return true
		}
	}
	return false
}

func validHost(h string) error {
	h = strings.TrimSpace(h)
	if h == "" {
		return errors.New("host is required")
	}
	if len(h) > 253 || strings.ContainsAny(h, " /\\?#@") {
		return fmt.Errorf("invalid host %q", h)
	}
	return nil
}

func decodeStrict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

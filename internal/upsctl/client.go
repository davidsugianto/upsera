// Package upsctl manages a team's monitors, notification channels,
// escalation policies and status pages as YAML ("monitors as code") through
// the Upsera REST API: export the live setup, diff a spec against it, apply
// it. It talks HTTP only and never imports server packages, so the upsctl
// binary stays small.
package upsctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// API is what upsctl needs from an Upsera server. *Client implements it
// over HTTP; tests use a fake.
type API interface {
	TokenInfo(ctx context.Context) (TokenInfo, error)
	Regions(ctx context.Context) ([]string, error)
	DockerHosts(ctx context.Context) ([]DockerHost, error)

	Channels(ctx context.Context) ([]Channel, error)
	CreateChannel(ctx context.Context, req ChannelRequest) (Channel, error)
	UpdateChannel(ctx context.Context, id int64, req ChannelRequest) (Channel, error)
	DeleteChannel(ctx context.Context, id int64) error

	Policies(ctx context.Context) ([]Policy, error)
	CreatePolicy(ctx context.Context, req PolicyRequest) (Policy, error)
	UpdatePolicy(ctx context.Context, id int64, req PolicyRequest) (Policy, error)
	DeletePolicy(ctx context.Context, id int64) error

	Monitors(ctx context.Context) ([]Monitor, error)
	CreateMonitor(ctx context.Context, req MonitorRequest) (Monitor, error)
	UpdateMonitor(ctx context.Context, id int64, req MonitorRequest) (Monitor, error)
	DeleteMonitor(ctx context.Context, id int64) error

	StatusPages(ctx context.Context) ([]StatusPage, error)
	CreateStatusPage(ctx context.Context, req StatusPageRequest) (StatusPage, error)
	UpdateStatusPage(ctx context.Context, id int64, req StatusPageRequest) (StatusPage, error)
	DeleteStatusPage(ctx context.Context, id int64) error

	ListSLOs(ctx context.Context) ([]SLOListItem, error)
	PutSLO(ctx context.Context, monitorID int64, in SLOInput) (SLO, error)
	// DeleteSLO removes a monitor's SLO; one that is already gone is not
	// an error.
	DeleteSLO(ctx context.Context, monitorID int64) error
}

// TokenInfo is GET /api/token: the team the token belongs to.
type TokenInfo struct {
	TeamID   int64  `json:"team_id"`
	TeamName string `json:"team_name"`
	Scope    string `json:"scope"`
	Name     string `json:"name"`
}

// DockerHost is a team's Docker host (only what upsctl needs).
type DockerHost struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Channel is a notification channel as the API returns it (secret config
// fields redacted).
type Channel struct {
	ID          int64           `json:"id"`
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Config      json.RawMessage `json:"config"`
	IsDefault   bool            `json:"is_default"`
	ConfigError string          `json:"config_error,omitempty"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// ChannelRequest creates or updates a channel.
type ChannelRequest struct {
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Config    json.RawMessage `json:"config"`
	IsDefault bool            `json:"is_default"`
}

// PolicyStep is one escalation step: notify ChannelIDs, then wait DelayS.
type PolicyStep struct {
	ChannelIDs []int64 `json:"channel_ids"`
	DelayS     int     `json:"delay_s"`
}

// Policy is an escalation policy.
type Policy struct {
	ID    int64        `json:"id"`
	Name  string       `json:"name"`
	Steps []PolicyStep `json:"steps"`
}

// PolicyRequest creates or updates an escalation policy.
type PolicyRequest struct {
	Name  string       `json:"name"`
	Steps []PolicyStep `json:"steps"`
}

// Monitor is a monitor as the API returns it.
type Monitor struct {
	ID                 int64           `json:"id"`
	Name               string          `json:"name"`
	Type               string          `json:"type"`
	Config             json.RawMessage `json:"config"`
	IntervalS          int             `json:"interval_s"`
	RetryIntervalS     int             `json:"retry_interval_s"`
	Retries            int             `json:"retries"`
	TimeoutS           int             `json:"timeout_s"`
	Paused             bool            `json:"paused"`
	GroupName          string          `json:"group_name"`
	Tags               []string        `json:"tags"`
	ParentID           *int64          `json:"parent_id"`
	EscalationPolicyID *int64          `json:"escalation_policy_id"`
	ChannelIDs         []int64         `json:"channel_ids"`
	Regions            []string        `json:"regions"`
	Quorum             int             `json:"quorum"`
}

// MonitorRequest creates or updates a monitor. Every field is sent, so an
// update replaces the whole monitor.
type MonitorRequest struct {
	Name               string          `json:"name"`
	Type               string          `json:"type"`
	Config             json.RawMessage `json:"config"`
	IntervalS          int             `json:"interval_s"`
	RetryIntervalS     int             `json:"retry_interval_s"`
	Retries            *int            `json:"retries"`
	TimeoutS           int             `json:"timeout_s"`
	Paused             bool            `json:"paused"`
	GroupName          string          `json:"group_name"`
	Tags               []string        `json:"tags,omitempty"`
	ParentID           *int64          `json:"parent_id,omitempty"`
	EscalationPolicyID *int64          `json:"escalation_policy_id,omitempty"`
	ChannelIDs         []int64         `json:"channel_ids,omitempty"`
	Regions            []string        `json:"regions"`
	Quorum             *int            `json:"quorum"`
}

// StatusPageItem is one monitor on a status page.
type StatusPageItem struct {
	Section     string `json:"section"`
	MonitorID   int64  `json:"monitor_id"`
	DisplayName string `json:"display_name"`
}

// StatusPage is a status page with its items in display order.
type StatusPage struct {
	ID           int64            `json:"id"`
	Slug         string           `json:"slug"`
	Title        string           `json:"title"`
	Description  string           `json:"description"`
	LogoURL      string           `json:"logo_url"`
	Theme        string           `json:"theme"`
	CustomDomain *string          `json:"custom_domain"`
	Published    bool             `json:"published"`
	Items        []StatusPageItem `json:"items"`
}

// StatusPageRequest creates or updates a status page. CustomDomain is left
// out (nil) when the caller may not change it, which keeps the current one.
type StatusPageRequest struct {
	Slug         string           `json:"slug"`
	Title        string           `json:"title"`
	Description  string           `json:"description"`
	LogoURL      string           `json:"logo_url"`
	Theme        string           `json:"theme,omitempty"`
	CustomDomain *string          `json:"custom_domain,omitempty"`
	Published    bool             `json:"published"`
	Items        []StatusPageItem `json:"items"`
}

// SLO is a monitor's service level objective (target in percent).
type SLO struct {
	MonitorID  int64   `json:"monitor_id"`
	Target     float64 `json:"target"`
	WindowDays int     `json:"window_days"`
	FastBurn   float64 `json:"fast_burn"`
	SlowBurn   float64 `json:"slow_burn"`
}

// SLOInput creates or replaces a monitor's SLO; omitted fields take the
// server defaults (30 days, fast burn 14.4, slow burn 6).
type SLOInput struct {
	Target     float64  `json:"target"`
	WindowDays *int     `json:"window_days,omitempty"`
	FastBurn   *float64 `json:"fast_burn,omitempty"`
	SlowBurn   *float64 `json:"slow_burn,omitempty"`
}

// SLOListItem is one entry of GET .../slos. Its live status is ignored.
type SLOListItem struct {
	MonitorID   int64   `json:"monitor_id"`
	MonitorName string  `json:"monitor_name"`
	Target      float64 `json:"target"`
	WindowDays  int     `json:"window_days"`
	FastBurn    float64 `json:"fast_burn"`
	SlowBurn    float64 `json:"slow_burn"`
}

// APIError is a non-2xx answer from the server. It carries the server's
// problem detail, never the request (which may hold secrets).
type APIError struct {
	Method string
	Path   string
	Status int
	Detail string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("%s %s: %d %s", e.Method, e.Path, e.Status, http.StatusText(e.Status))
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Client is the HTTP implementation of API, bound to the team of its token.
type Client struct {
	base  string
	token string
	http  *http.Client

	once    sync.Once
	info    TokenInfo
	infoErr error
}

// NewClient returns a client for the server at baseURL authenticating with
// the team API token. The token is only ever sent as a bearer header.
func NewClient(baseURL, token string) *Client {
	return &Client{
		base:  strings.TrimRight(baseURL, "/"),
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

// TokenInfo returns (and caches) the token's team, scope and name.
func (c *Client) TokenInfo(ctx context.Context) (TokenInfo, error) {
	c.once.Do(func() {
		c.infoErr = c.do(ctx, http.MethodGet, "/api/token", nil, &c.info)
	})
	return c.info, c.infoErr
}

func (c *Client) teamPath(ctx context.Context, format string, args ...any) (string, error) {
	info, err := c.TokenInfo(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/api/teams/%d", info.TeamID) + fmt.Sprintf(format, args...), nil
}

// teamGet GETs a team-scoped path into out.
func (c *Client) teamGet(ctx context.Context, path string, out any) error {
	p, err := c.teamPath(ctx, "%s", path)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodGet, p, nil, out)
}

// teamSend sends body with method to a team-scoped path, decoding into out.
func (c *Client) teamSend(ctx context.Context, method, path string, body, out any) error {
	p, err := c.teamPath(ctx, "%s", path)
	if err != nil {
		return err
	}
	return c.do(ctx, method, p, body, out)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "upsctl")
	resp, err := c.http.Do(req)
	if err != nil {
		// url.Error holds the URL only (no headers), so the token never
		// appears here.
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Method: method, Path: path, Status: resp.StatusCode, Detail: problemDetail(raw)}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, path, err)
	}
	return nil
}

// problemDetail extracts a readable message from a huma problem body.
func problemDetail(raw []byte) string {
	var p struct {
		Detail string `json:"detail"`
		Errors []struct {
			Message  string `json:"message"`
			Location string `json:"location"`
		} `json:"errors"`
	}
	if json.Unmarshal(raw, &p) != nil {
		s := strings.TrimSpace(string(raw))
		if len(s) > 200 {
			s = s[:200]
		}
		return s
	}
	parts := []string{}
	if p.Detail != "" {
		parts = append(parts, p.Detail)
	}
	for _, e := range p.Errors {
		if e.Location != "" {
			parts = append(parts, e.Location+": "+e.Message)
		} else {
			parts = append(parts, e.Message)
		}
	}
	return strings.Join(parts, "; ")
}

// Regions lists the probe regions of the instance ("local" included).
func (c *Client) Regions(ctx context.Context) ([]string, error) {
	var out struct {
		Regions []struct {
			Name string `json:"name"`
		} `json:"regions"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/regions", nil, &out); err != nil {
		return nil, err
	}
	names := make([]string, len(out.Regions))
	for i, r := range out.Regions {
		names[i] = r.Name
	}
	return names, nil
}

// DockerHosts lists the team's Docker hosts.
func (c *Client) DockerHosts(ctx context.Context) ([]DockerHost, error) {
	var out struct {
		Hosts []DockerHost `json:"hosts"`
	}
	err := c.teamGet(ctx, "/docker-hosts", &out)
	return out.Hosts, err
}

// Channels lists the team's notification channels.
func (c *Client) Channels(ctx context.Context) ([]Channel, error) {
	var out struct {
		Channels []Channel `json:"channels"`
	}
	err := c.teamGet(ctx, "/channels", &out)
	return out.Channels, err
}

// CreateChannel creates a channel.
func (c *Client) CreateChannel(ctx context.Context, req ChannelRequest) (Channel, error) {
	var out Channel
	err := c.teamSend(ctx, http.MethodPost, "/channels", req, &out)
	return out, err
}

// UpdateChannel replaces a channel's settings.
func (c *Client) UpdateChannel(ctx context.Context, id int64, req ChannelRequest) (Channel, error) {
	var out Channel
	err := c.teamSend(ctx, http.MethodPut, fmt.Sprintf("/channels/%d", id), req, &out)
	return out, err
}

// DeleteChannel deletes a channel.
func (c *Client) DeleteChannel(ctx context.Context, id int64) error {
	return c.teamSend(ctx, http.MethodDelete, fmt.Sprintf("/channels/%d", id), nil, nil)
}

// Policies lists the team's escalation policies.
func (c *Client) Policies(ctx context.Context) ([]Policy, error) {
	var out struct {
		Policies []Policy `json:"escalation_policies"`
	}
	err := c.teamGet(ctx, "/escalation-policies", &out)
	return out.Policies, err
}

// CreatePolicy creates an escalation policy.
func (c *Client) CreatePolicy(ctx context.Context, req PolicyRequest) (Policy, error) {
	var out Policy
	err := c.teamSend(ctx, http.MethodPost, "/escalation-policies", req, &out)
	return out, err
}

// UpdatePolicy replaces an escalation policy.
func (c *Client) UpdatePolicy(ctx context.Context, id int64, req PolicyRequest) (Policy, error) {
	var out Policy
	err := c.teamSend(ctx, http.MethodPut, fmt.Sprintf("/escalation-policies/%d", id), req, &out)
	return out, err
}

// DeletePolicy deletes an escalation policy.
func (c *Client) DeletePolicy(ctx context.Context, id int64) error {
	return c.teamSend(ctx, http.MethodDelete, fmt.Sprintf("/escalation-policies/%d", id), nil, nil)
}

// Monitors lists the team's monitors.
func (c *Client) Monitors(ctx context.Context) ([]Monitor, error) {
	var out struct {
		Monitors []Monitor `json:"monitors"`
	}
	err := c.teamGet(ctx, "/monitors", &out)
	return out.Monitors, err
}

// CreateMonitor creates a monitor.
func (c *Client) CreateMonitor(ctx context.Context, req MonitorRequest) (Monitor, error) {
	var out Monitor
	err := c.teamSend(ctx, http.MethodPost, "/monitors", req, &out)
	return out, err
}

// UpdateMonitor replaces a monitor.
func (c *Client) UpdateMonitor(ctx context.Context, id int64, req MonitorRequest) (Monitor, error) {
	var out Monitor
	err := c.teamSend(ctx, http.MethodPut, fmt.Sprintf("/monitors/%d", id), req, &out)
	return out, err
}

// DeleteMonitor deletes a monitor.
func (c *Client) DeleteMonitor(ctx context.Context, id int64) error {
	return c.teamSend(ctx, http.MethodDelete, fmt.Sprintf("/monitors/%d", id), nil, nil)
}

// StatusPages lists the team's status pages with their items.
func (c *Client) StatusPages(ctx context.Context) ([]StatusPage, error) {
	var out struct {
		Pages []StatusPage `json:"pages"`
	}
	err := c.teamGet(ctx, "/status-pages", &out)
	return out.Pages, err
}

// CreateStatusPage creates a status page.
func (c *Client) CreateStatusPage(ctx context.Context, req StatusPageRequest) (StatusPage, error) {
	var out StatusPage
	err := c.teamSend(ctx, http.MethodPost, "/status-pages", req, &out)
	return out, err
}

// UpdateStatusPage replaces a status page's settings and items.
func (c *Client) UpdateStatusPage(ctx context.Context, id int64, req StatusPageRequest) (StatusPage, error) {
	var out StatusPage
	err := c.teamSend(ctx, http.MethodPut, fmt.Sprintf("/status-pages/%d", id), req, &out)
	return out, err
}

// DeleteStatusPage deletes a status page (with its incidents and
// subscribers).
func (c *Client) DeleteStatusPage(ctx context.Context, id int64) error {
	return c.teamSend(ctx, http.MethodDelete, fmt.Sprintf("/status-pages/%d", id), nil, nil)
}

// ListSLOs lists the SLOs of the team's monitors.
func (c *Client) ListSLOs(ctx context.Context) ([]SLOListItem, error) {
	var out struct {
		SLOs []SLOListItem `json:"slos"`
	}
	err := c.teamGet(ctx, "/slos", &out)
	return out.SLOs, err
}

// PutSLO creates or replaces a monitor's SLO.
func (c *Client) PutSLO(ctx context.Context, monitorID int64, in SLOInput) (SLO, error) {
	var out struct {
		SLO SLO `json:"slo"`
	}
	err := c.teamSend(ctx, http.MethodPut, fmt.Sprintf("/monitors/%d/slo", monitorID), in, &out)
	return out.SLO, err
}

// DeleteSLO removes a monitor's SLO (404, already gone, is success).
func (c *Client) DeleteSLO(ctx context.Context, monitorID int64) error {
	err := c.teamSend(ctx, http.MethodDelete, fmt.Sprintf("/monitors/%d/slo", monitorID), nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// IsNotFound reports whether err is a 404 from the server.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

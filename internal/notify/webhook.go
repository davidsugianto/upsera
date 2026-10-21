package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

type webhookConfig struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

func parseWebhook(cfg json.RawMessage) (webhookConfig, error) {
	var c webhookConfig
	if err := decodeConfig(cfg, &c); err != nil {
		return c, err
	}
	if err := validateHTTPURL("url", c.URL); err != nil {
		return c, err
	}
	if len(c.Headers) > 20 {
		return c, errors.New("headers: at most 20 allowed")
	}
	for k := range c.Headers {
		if k == "" {
			return c, errors.New("headers: empty header name")
		}
	}
	return c, nil
}

type webhook struct{ client *http.Client }

func (*webhook) Type() string { return "webhook" }

func (*webhook) Validate(cfg json.RawMessage) error {
	_, err := parseWebhook(cfg)
	return err
}

type webhookPayload struct {
	Event   string `json:"event"`
	AlertID string `json:"alert_id"`
	Monitor struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"monitor"`
	Message       string  `json:"message"`
	At            string  `json:"at"`
	DowntimeS     int64   `json:"downtime_s"`
	CertExpiresAt *string `json:"cert_expires_at"`
	CertDaysLeft  int     `json:"cert_days_left"`
	Region        string  `json:"region,omitempty"`
	LatencyP95Ms  int32   `json:"latency_p95_ms,omitempty"`
	BaselineMs    int32   `json:"baseline_ms,omitempty"`
	// SLO fields, set for slo_burn / slo_burn_recovered only.
	SLOLevel       string   `json:"slo_level,omitempty"`
	SLOTarget      *float64 `json:"slo_target,omitempty"`
	BurnRate       *float64 `json:"burn_rate,omitempty"`
	BudgetConsumed *float64 `json:"budget_consumed,omitempty"`
}

func (w *webhook) Send(ctx context.Context, cfg json.RawMessage, ev Event) error {
	c, err := parseWebhook(cfg)
	if err != nil {
		return err
	}
	var p webhookPayload
	p.Event = string(ev.Kind)
	p.AlertID = ev.AlertID
	p.Monitor.ID = ev.MonitorID
	p.Monitor.Name = ev.MonitorName
	p.Message = ev.Message
	p.At = ev.At.UTC().Format(time.RFC3339)
	p.DowntimeS = int64(ev.Downtime / time.Second)
	if !ev.CertExpiresAt.IsZero() {
		p.CertExpiresAt = new(ev.CertExpiresAt.UTC().Format(time.RFC3339))
	}
	p.CertDaysLeft = ev.CertDaysLeft
	p.Region = ev.Region
	p.LatencyP95Ms, p.BaselineMs = ev.LatencyP95Ms, ev.BaselineMs
	if ev.Kind == model.EventSLOBurn || ev.Kind == model.EventSLOBurnRecovered {
		p.SLOLevel, p.SLOTarget, p.BurnRate = ev.SLOLevel, new(ev.SLOTarget), new(ev.BurnRate)
		if ev.BudgetConsumed >= 0 {
			p.BudgetConsumed = new(ev.BudgetConsumed)
		}
	}
	_, err = postJSON(ctx, w.client, c.URL, c.Headers, p)
	return err
}

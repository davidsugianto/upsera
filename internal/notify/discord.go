package notify

import (
	"context"
	"encoding/json"
	"net/http"
)

type webhookURLConfig struct {
	WebhookURL string `json:"webhook_url"`
}

func parseWebhookURL(cfg json.RawMessage) (webhookURLConfig, error) {
	var c webhookURLConfig
	if err := decodeConfig(cfg, &c); err != nil {
		return c, err
	}
	return c, validateHTTPURL("webhook_url", c.WebhookURL)
}

type discord struct{ client *http.Client }

func (*discord) Type() string { return "discord" }

func (*discord) Validate(cfg json.RawMessage) error {
	_, err := parseWebhookURL(cfg)
	return err
}

func (d *discord) Send(ctx context.Context, cfg json.RawMessage, ev Event) error {
	c, err := parseWebhookURL(cfg)
	if err != nil {
		return err
	}
	_, err = postJSON(ctx, d.client, c.WebhookURL, nil, map[string]string{"content": Title(ev) + "\n" + Body(ev)})
	return err
}

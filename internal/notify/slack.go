package notify

import (
	"context"
	"encoding/json"
	"net/http"
)

// slackWebhook posts to a Slack incoming webhook (no buttons).
type slackWebhook struct{ client *http.Client }

func (*slackWebhook) Type() string { return "slack" }

func (*slackWebhook) Validate(cfg json.RawMessage) error {
	_, err := parseWebhookURL(cfg)
	return err
}

func (s *slackWebhook) Send(ctx context.Context, cfg json.RawMessage, ev Event) error {
	c, err := parseWebhookURL(cfg)
	if err != nil {
		return err
	}
	_, err = postJSON(ctx, s.client, c.WebhookURL, nil, map[string]string{"text": "*" + Title(ev) + "*\n" + Body(ev)})
	return err
}

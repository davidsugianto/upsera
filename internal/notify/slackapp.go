package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// slackAckActionID identifies the Acknowledge button in block_actions
// payloads.
const slackAckActionID = "upsera_ack"

type slackAppConfig struct {
	BotToken string `json:"bot_token"`
	AppToken string `json:"app_token"`
	Channel  string `json:"channel"`
}

func parseSlackApp(cfg json.RawMessage) (slackAppConfig, error) {
	var c slackAppConfig
	if err := decodeConfig(cfg, &c); err != nil {
		return c, err
	}
	if !strings.HasPrefix(c.BotToken, "xoxb-") {
		return c, errors.New("bot_token must be a bot token (xoxb-…)")
	}
	if !strings.HasPrefix(c.AppToken, "xapp-") {
		return c, errors.New("app_token must be an app-level token (xapp-…)")
	}
	if strings.TrimSpace(c.Channel) == "" {
		return c, errors.New("channel is required")
	}
	return c, nil
}

// slackApp posts with a bot token; its Acknowledge button is answered over
// Socket Mode (see AckListeners).
type slackApp struct {
	api    string
	client *http.Client
}

func (*slackApp) Type() string { return "slack_app" }

func (*slackApp) Validate(cfg json.RawMessage) error {
	_, err := parseSlackApp(cfg)
	return err
}

func (s *slackApp) Send(ctx context.Context, cfg json.RawMessage, ev Event) error {
	c, err := parseSlackApp(cfg)
	if err != nil {
		return err
	}
	blocks := []any{map[string]any{
		"type": "section",
		"text": map[string]string{"type": "mrkdwn", "text": "*" + Title(ev) + "*\n" + Body(ev)},
	}}
	if ev.Ackable && ev.AlertID != "" {
		blocks = append(blocks, map[string]any{
			"type": "actions",
			"elements": []any{map[string]any{
				"type":      "button",
				"action_id": slackAckActionID,
				"value":     ev.AlertID,
				"text":      map[string]string{"type": "plain_text", "text": "Acknowledge"},
			}},
		})
	}
	return slackCall(ctx, s.client, s.api+"/chat.postMessage", c.BotToken,
		map[string]any{"channel": c.Channel, "text": Title(ev), "blocks": blocks}, nil)
}

// slackCall POSTs a Slack Web API method and checks its {"ok":…} envelope.
// out, if non-nil, receives the decoded response.
func slackCall(ctx context.Context, c *http.Client, u, token string, body, out any) error {
	data, err := postJSON(ctx, c, u, map[string]string{"Authorization": "Bearer " + token}, body)
	if err != nil {
		return err
	}
	var env struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return errors.New("slack: invalid response")
	}
	if !env.OK {
		return errors.New("slack: " + env.Error)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

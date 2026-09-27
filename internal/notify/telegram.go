package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
)

var telegramTokenRe = regexp.MustCompile(`^\d+:[A-Za-z0-9_-]{30,}$`)

type telegramConfig struct {
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
}

func parseTelegram(cfg json.RawMessage) (telegramConfig, error) {
	var c telegramConfig
	if err := decodeConfig(cfg, &c); err != nil {
		return c, err
	}
	if !telegramTokenRe.MatchString(c.BotToken) {
		return c, errors.New("bot_token must look like 123456:ABC… (from @BotFather)")
	}
	if strings.TrimSpace(c.ChatID) == "" {
		return c, errors.New("chat_id is required")
	}
	return c, nil
}

type telegram struct {
	api    string
	client *http.Client
}

func (*telegram) Type() string { return "telegram" }

func (*telegram) Validate(cfg json.RawMessage) error {
	_, err := parseTelegram(cfg)
	return err
}

func (t *telegram) Send(ctx context.Context, cfg json.RawMessage, ev Event) error {
	c, err := parseTelegram(cfg)
	if err != nil {
		return err
	}
	body := map[string]any{
		"chat_id": c.ChatID,
		"text":    Title(ev) + "\n" + Body(ev),
	}
	if ev.Ackable && ev.AlertID != "" {
		body["reply_markup"] = map[string]any{
			"inline_keyboard": [][]map[string]string{{{"text": "Acknowledge", "callback_data": "ack:" + ev.AlertID}}},
		}
	}
	_, err = postJSON(ctx, t.client, t.api+"/bot"+c.BotToken+"/sendMessage", nil, body)
	return err
}

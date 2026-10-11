package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
)

type smtpConfig struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Username string   `json:"username"`
	Password string   `json:"password"`
	From     string   `json:"from"`
	To       []string `json:"to"`
	TLS      string   `json:"tls"` // "starttls", "tls" or "none"
}

func parseSMTP(cfg json.RawMessage) (smtpConfig, error) {
	var c smtpConfig
	if err := decodeConfig(cfg, &c); err != nil {
		return c, err
	}
	if strings.TrimSpace(c.Host) == "" {
		return c, errors.New("host is required")
	}
	if c.Port < 1 || c.Port > 65535 {
		return c, errors.New("port must be 1-65535")
	}
	if _, err := mail.ParseAddress(c.From); err != nil {
		return c, errors.New("from: invalid address")
	}
	if len(c.To) < 1 || len(c.To) > 20 {
		return c, errors.New("to: 1-20 recipients required")
	}
	for _, to := range c.To {
		if _, err := mail.ParseAddress(to); err != nil {
			return c, fmt.Errorf("to: invalid address %q", to)
		}
	}
	switch c.TLS {
	case "starttls", "tls", "none":
	default:
		return c, errors.New(`tls must be "starttls", "tls" or "none"`)
	}
	if c.TLS == "none" && c.Username != "" {
		return c, errors.New("authentication requires tls or starttls")
	}
	return c, nil
}

type smtpNotifier struct{ policy *netpolicy.Policy }

func (*smtpNotifier) Type() string { return "smtp" }

func (*smtpNotifier) Validate(cfg json.RawMessage) error {
	_, err := parseSMTP(cfg)
	return err
}

func (n *smtpNotifier) Send(ctx context.Context, cfg json.RawMessage, ev Event) error {
	c, err := parseSMTP(cfg)
	if err != nil {
		return err
	}
	return SendMail(ctx, n.policy, model.SMTPSettings{
		Host: c.Host, Port: c.Port, Username: c.Username, Password: c.Password, From: c.From, TLS: c.TLS,
	}, c.To, Title(ev), Body(ev))
}

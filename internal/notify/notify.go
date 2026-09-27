// Package notify sends alert notifications to channels (Telegram, Discord,
// Slack, SMTP, generic webhooks), queues and retries them per channel
// (Dispatcher) and listens for acknowledgement buttons (AckListeners).
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
)

// Event is one notification.
type Event struct {
	Kind          model.AlertEvent
	AlertID       string
	TeamID        int64
	MonitorID     int64
	MonitorName   string
	ChannelName   string
	Message       string
	At            time.Time
	Downtime      time.Duration
	CertExpiresAt time.Time
	CertDaysLeft  int
	FlapCount     int
	// Ackable adds an Acknowledge button where the channel supports one.
	Ackable bool
}

// Notifier delivers events to one channel type. cfg is the channel's
// plaintext JSON config.
type Notifier interface {
	Type() string
	Validate(cfg json.RawMessage) error
	Send(ctx context.Context, cfg json.RawMessage, ev Event) error
}

// Options configures the notifiers. A nil Policy dials without an
// outbound policy (tests).
type Options struct {
	Policy         *netpolicy.Policy
	TelegramAPIURL string
	SlackAPIURL    string
}

// Default API endpoints, used when Options leaves them empty.
const (
	DefaultTelegramAPIURL = "https://api.telegram.org"
	DefaultSlackAPIURL    = "https://slack.com/api"
)

// Redacted replaces secret config values in API responses.
const Redacted = "********"

func (o Options) withDefaults() Options {
	if o.TelegramAPIURL == "" {
		o.TelegramAPIURL = DefaultTelegramAPIURL
	}
	if o.SlackAPIURL == "" {
		o.SlackAPIURL = DefaultSlackAPIURL
	}
	o.TelegramAPIURL = strings.TrimSuffix(o.TelegramAPIURL, "/")
	o.SlackAPIURL = strings.TrimSuffix(o.SlackAPIURL, "/")
	return o
}

// New returns one notifier per channel type.
func New(opts Options) map[model.ChannelType]Notifier {
	opts = opts.withDefaults()
	c := newHTTPClient(opts.Policy)
	return map[model.ChannelType]Notifier{
		model.ChannelTelegram: &telegram{api: opts.TelegramAPIURL, client: c},
		model.ChannelDiscord:  &discord{client: c},
		model.ChannelSlack:    &slackWebhook{client: c},
		model.ChannelSlackApp: &slackApp{api: opts.SlackAPIURL, client: c},
		model.ChannelSMTP:     &smtpNotifier{policy: opts.Policy},
		model.ChannelWebhook:  &webhook{client: c},
	}
}

var validators = New(Options{})

// Validate checks cfg against channel type t.
func Validate(t model.ChannelType, cfg json.RawMessage) error {
	n, ok := validators[t]
	if !ok {
		return fmt.Errorf("unknown channel type %q", t)
	}
	return n.Validate(cfg)
}

// SecretFields lists the config fields of channel type t that must never
// be shown back to users.
func SecretFields(t model.ChannelType) []string {
	switch t {
	case model.ChannelTelegram:
		return []string{"bot_token"}
	case model.ChannelDiscord, model.ChannelSlack:
		return []string{"webhook_url"}
	case model.ChannelSlackApp:
		return []string{"bot_token", "app_token"}
	case model.ChannelSMTP:
		return []string{"password"}
	case model.ChannelWebhook:
		return []string{"url", "headers"}
	}
	return nil
}

// decodeConfig strictly decodes cfg into v, rejecting unknown fields and
// trailing data.
func decodeConfig(cfg json.RawMessage, v any) error {
	if len(bytes.TrimSpace(cfg)) == 0 {
		return errors.New("config is required")
	}
	dec := json.NewDecoder(bytes.NewReader(cfg))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid config: %s", strings.TrimPrefix(err.Error(), "json: "))
	}
	if dec.More() {
		return errors.New("invalid config: trailing data")
	}
	return nil
}

// validateHTTPURL checks that s is an absolute http(s) URL. field names it
// in the error, which never echoes the URL (it is a secret).
func validateHTTPURL(field, s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s must be an absolute http(s) URL", field)
	}
	return nil
}

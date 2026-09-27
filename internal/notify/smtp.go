package notify

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/davidsugianto/upsera/internal/netpolicy"
)

const smtpTimeout = 10 * time.Second

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
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	var conn net.Conn
	if n.policy != nil {
		conn, err = n.policy.DialContext(ctx, "tcp", addr)
	} else {
		var d net.Dialer
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	// net/smtp ignores ctx: closing the connection on cancellation
	// interrupts the exchange (e.g. when the dispatcher is shutting down).
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline := time.Now().Add(smtpTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if c.TLS == "tls" {
		conn = tls.Client(conn, &tls.Config{ServerName: c.Host})
	}

	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		return err
	}
	defer cl.Close()
	if c.TLS == "starttls" {
		if err := cl.StartTLS(&tls.Config{ServerName: c.Host}); err != nil {
			return err
		}
	}
	if c.Username != "" {
		if err := cl.Auth(smtp.PlainAuth("", c.Username, c.Password, c.Host)); err != nil {
			return err
		}
	}
	from, _ := mail.ParseAddress(c.From)
	if err := cl.Mail(from.Address); err != nil {
		return err
	}
	for _, to := range c.To {
		a, _ := mail.ParseAddress(to)
		if err := cl.Rcpt(a.Address); err != nil {
			return err
		}
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(buildMessage(c, ev)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}

// buildMessage renders the RFC 5322 message with CRLF line endings.
// Header values come from validated addresses and the title; CR/LF are
// stripped from the subject so a monitor name cannot inject headers.
func buildMessage(c smtpConfig, ev Event) []byte {
	subject := strings.NewReplacer("\r", " ", "\n", " ").Replace(Title(ev))
	var b strings.Builder
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", c.From)
	h("To", strings.Join(c.To, ", "))
	h("Subject", mimeHeader(subject))
	h("Date", time.Now().Format(time.RFC1123Z))
	h("Message-ID", "<"+uuid.NewString()+"@upsera>")
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=utf-8")
	b.WriteString("\r\n")
	body := strings.ReplaceAll(Body(ev), "\r\n", "\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	b.WriteString("\r\n")
	return []byte(b.String())
}

// mimeHeader Q-encodes s only when it is not plain ASCII.
func mimeHeader(s string) string {
	for _, r := range s {
		if r >= 0x80 {
			return mime.QEncoding.Encode("utf-8", s)
		}
	}
	return s
}

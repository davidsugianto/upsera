package notify

import (
	"context"
	"crypto/tls"
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

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
)

const (
	smtpTimeout   = 10 * time.Second
	maxRecipients = 20
)

// ValidateSMTPSettings checks a mail server definition: a host, a port in
// 1-65535, a valid From address and tls of "starttls", "tls" or "none".
func ValidateSMTPSettings(cfg model.SMTPSettings) error {
	if strings.TrimSpace(cfg.Host) == "" {
		return errors.New("host is required")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return errors.New("port must be 1-65535")
	}
	if hasCRLF(cfg.From) {
		return errors.New("from: invalid address")
	}
	if _, err := mail.ParseAddress(cfg.From); err != nil {
		return errors.New("from: invalid address")
	}
	switch cfg.TLS {
	case "starttls", "tls", "none":
	default:
		return errors.New(`tls must be "starttls", "tls" or "none"`)
	}
	return nil
}

// SendMail sends one plain-text UTF-8 message through the SMTP server cfg
// to every address in to, dialing through policy (nil dials directly).
// ctx cancels the exchange. CR/LF in the subject are replaced by spaces so
// it cannot inject headers; recipients containing them are rejected.
func SendMail(ctx context.Context, policy *netpolicy.Policy, cfg model.SMTPSettings, to []string, subject, body string) error {
	if err := ValidateSMTPSettings(cfg); err != nil {
		return err
	}
	if len(to) < 1 || len(to) > maxRecipients {
		return fmt.Errorf("to: 1-%d recipients required", maxRecipients)
	}
	rcpts := make([]string, len(to))
	for i, t := range to {
		if hasCRLF(t) {
			return fmt.Errorf("to: invalid address %q", t)
		}
		a, err := mail.ParseAddress(t)
		if err != nil {
			return fmt.Errorf("to: invalid address %q", t)
		}
		rcpts[i] = a.Address
	}
	from, _ := mail.ParseAddress(cfg.From)

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	var conn net.Conn
	var err error
	if policy != nil {
		conn, err = policy.DialContext(ctx, "tcp", addr)
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
	if cfg.TLS == "tls" {
		conn = tls.Client(conn, &tls.Config{ServerName: cfg.Host})
	}

	cl, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return err
	}
	defer cl.Close()
	if cfg.TLS == "starttls" {
		if err := cl.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
			return err
		}
	}
	if cfg.Username != "" {
		if err := cl.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return err
		}
	}
	if err := cl.Mail(from.Address); err != nil {
		return err
	}
	for _, r := range rcpts {
		if err := cl.Rcpt(r); err != nil {
			return err
		}
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(buildMessage(cfg.From, to, subject, body)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}

// buildMessage renders the RFC 5322 message with CRLF line endings.
// from and to are validated addresses; CR/LF are stripped from the subject
// so a monitor name or page title cannot inject headers.
func buildMessage(from string, to []string, subject, body string) []byte {
	subject = strings.NewReplacer("\r", " ", "\n", " ").Replace(subject)
	var b strings.Builder
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from)
	h("To", strings.Join(to, ", "))
	h("Subject", mimeHeader(subject))
	h("Date", time.Now().Format(time.RFC1123Z))
	h("Message-ID", "<"+uuid.NewString()+"@upsera>")
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=utf-8")
	b.WriteString("\r\n")
	body = strings.ReplaceAll(body, "\r\n", "\n")
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

func hasCRLF(s string) bool { return strings.ContainsAny(s, "\r\n") }

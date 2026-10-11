package notify

import (
	"bufio"
	"context"
	"encoding/base64"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mime"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
	"github.com/davidsugianto/upsera/internal/testutil"
)

func mailTestSettings(t *testing.T, addr string) model.SMTPSettings {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return model.SMTPSettings{Host: host, Port: p, From: "Upsera <status@upsera.dev>", TLS: "none"}
}

func mailTestCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// mailTestAuthServer is a fake SMTP server that advertises AUTH PLAIN and
// records the credentials it was given. A wrong password gets a 535.
type mailTestAuthServer struct {
	mu       sync.Mutex
	user     string
	pass     string
	authed   bool
	messages int
}

func mailTestStartAuthServer(t *testing.T, wantUser, wantPass string) (string, *mailTestAuthServer) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := &mailTestAuthServer{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn, wantUser, wantPass)
		}
	}()
	return ln.Addr().String(), s
}

func (s *mailTestAuthServer) serve(conn net.Conn, wantUser, wantPass string) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := func(l string) { _, _ = conn.Write([]byte(l + "\r\n")) }
	w("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "EHLO"):
			w("250-fake")
			w("250 AUTH PLAIN")
		case strings.HasPrefix(up, "AUTH PLAIN "):
			raw, _ := base64.StdEncoding.DecodeString(line[len("AUTH PLAIN "):])
			parts := strings.Split(string(raw), "\x00")
			if len(parts) != 3 {
				w("501 bad")
				continue
			}
			s.mu.Lock()
			s.user, s.pass = parts[1], parts[2]
			s.authed = parts[1] == wantUser && parts[2] == wantPass
			ok := s.authed
			s.mu.Unlock()
			if ok {
				w("235 ok")
			} else {
				w("535 5.7.8 authentication failed")
			}
		case strings.HasPrefix(up, "MAIL FROM:"), strings.HasPrefix(up, "RCPT TO:"):
			s.mu.Lock()
			ok := s.authed
			s.mu.Unlock()
			if !ok {
				w("530 auth required")
				continue
			}
			w("250 ok")
		case up == "DATA":
			w("354 go")
			for {
				dl, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if dl == ".\r\n" {
					break
				}
			}
			s.mu.Lock()
			s.messages++
			s.mu.Unlock()
			w("250 ok")
		case up == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

func TestSendMail(t *testing.T) {
	t.Parallel()

	t.Run("plain", func(t *testing.T) {
		t.Parallel()
		addr, messages := testutil.FakeSMTP(t)
		cfg := mailTestSettings(t, addr)
		to := []string{"a@example.com", "Bee <b@example.com>"}
		if err := SendMail(mailTestCtx(t), nil, cfg, to, "Hello", "line one\nline two\n.dot line"); err != nil {
			t.Fatalf("SendMail: %v", err)
		}
		msgs := messages()
		if len(msgs) != 1 {
			t.Fatalf("len(messages) = %d, want 1", len(msgs))
		}
		m := msgs[0]
		if m.From != "status@upsera.dev" {
			t.Errorf("envelope From = %q", m.From)
		}
		if len(m.To) != 2 || m.To[0] != "a@example.com" || m.To[1] != "b@example.com" {
			t.Errorf("envelope To = %v", m.To)
		}
		parsed, err := mail.ReadMessage(strings.NewReader(m.Data))
		if err != nil {
			t.Fatalf("parse message: %v", err)
		}
		for h, want := range map[string]string{
			"From":         "Upsera <status@upsera.dev>",
			"To":           "a@example.com, Bee <b@example.com>",
			"Subject":      "Hello",
			"Content-Type": "text/plain; charset=utf-8",
			"Mime-Version": "1.0",
		} {
			if got := parsed.Header.Get(h); got != want {
				t.Errorf("header %s = %q, want %q", h, got, want)
			}
		}
		if _, err := parsed.Header.Date(); err != nil {
			t.Errorf("Date header: %v", err)
		}
		if id := parsed.Header.Get("Message-Id"); !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, ">") {
			t.Errorf("Message-ID = %q", id)
		}
		if !strings.Contains(m.Data, "\r\n\r\nline one\r\nline two\r\n") {
			t.Errorf("body not CRLF-normalised: %q", m.Data)
		}
	})

	t.Run("non-ascii subject is encoded", func(t *testing.T) {
		t.Parallel()
		addr, messages := testutil.FakeSMTP(t)
		const subject = "Statusupdate: Störung behoben ✓"
		if err := SendMail(mailTestCtx(t), nil, mailTestSettings(t, addr), []string{"a@example.com"}, subject, "ok"); err != nil {
			t.Fatalf("SendMail: %v", err)
		}
		parsed, err := mail.ReadMessage(strings.NewReader(messages()[0].Data))
		if err != nil {
			t.Fatal(err)
		}
		raw := parsed.Header.Get("Subject")
		if !strings.HasPrefix(raw, "=?utf-8?q?") {
			t.Errorf("raw Subject = %q, want a Q-encoded word", raw)
		}
		dec, err := new(mime.WordDecoder).DecodeHeader(raw)
		if err != nil || dec != subject {
			t.Errorf("decoded Subject = %q (%v), want %q", dec, err, subject)
		}
	})

	t.Run("auth", func(t *testing.T) {
		t.Parallel()
		addr, srv := mailTestStartAuthServer(t, "mailer", "s3cret")
		cfg := mailTestSettings(t, addr)
		cfg.Username, cfg.Password = "mailer", "s3cret"
		if err := SendMail(mailTestCtx(t), nil, cfg, []string{"a@example.com"}, "hi", "body"); err != nil {
			t.Fatalf("SendMail: %v", err)
		}
		srv.mu.Lock()
		defer srv.mu.Unlock()
		if srv.user != "mailer" || srv.pass != "s3cret" || srv.messages != 1 {
			t.Errorf("server saw user=%q pass=%q messages=%d", srv.user, srv.pass, srv.messages)
		}
	})

	t.Run("wrong password fails without leaking it", func(t *testing.T) {
		t.Parallel()
		addr, srv := mailTestStartAuthServer(t, "mailer", "s3cret")
		cfg := mailTestSettings(t, addr)
		cfg.Username, cfg.Password = "mailer", "hunter2-wrong"
		err := SendMail(mailTestCtx(t), nil, cfg, []string{"a@example.com"}, "hi", "body")
		if err == nil {
			t.Fatal("SendMail with a wrong password succeeded")
		}
		if strings.Contains(err.Error(), "hunter2-wrong") {
			t.Errorf("error leaks the password: %v", err)
		}
		srv.mu.Lock()
		defer srv.mu.Unlock()
		if srv.messages != 0 {
			t.Errorf("messages = %d, want 0", srv.messages)
		}
	})

	t.Run("CR/LF injection", func(t *testing.T) {
		t.Parallel()
		addr, messages := testutil.FakeSMTP(t)
		cfg := mailTestSettings(t, addr)

		// A subject cannot start a new header.
		if err := SendMail(mailTestCtx(t), nil, cfg, []string{"a@example.com"},
			"hi\r\nBcc: evil@example.com", "body"); err != nil {
			t.Fatalf("SendMail: %v", err)
		}
		m := messages()[0]
		parsed, err := mail.ReadMessage(strings.NewReader(m.Data))
		if err != nil {
			t.Fatal(err)
		}
		if bcc := parsed.Header.Get("Bcc"); bcc != "" {
			t.Errorf("injected Bcc header survived: %q", bcc)
		}
		if got := parsed.Header.Get("Subject"); !strings.HasPrefix(got, "hi") {
			t.Errorf("Subject = %q", got)
		}
		if len(m.To) != 1 {
			t.Errorf("envelope To = %v", m.To)
		}

		// A recipient or From cannot smuggle SMTP commands or headers.
		for _, to := range []string{
			"a@example.com\r\nRCPT TO:<evil@example.com>",
			"a@example.com\nBcc: evil@example.com",
		} {
			if err := SendMail(mailTestCtx(t), nil, cfg, []string{to}, "hi", "body"); err == nil {
				t.Errorf("recipient %q accepted", to)
			}
		}
		bad := cfg
		bad.From = "a@example.com\r\nBcc: evil@example.com"
		if err := SendMail(mailTestCtx(t), nil, bad, []string{"a@example.com"}, "hi", "body"); err == nil {
			t.Error("From with CR/LF accepted")
		}
		if n := len(messages()); n != 1 {
			t.Errorf("messages after rejected sends = %d, want 1", n)
		}
	})

	t.Run("recipient count", func(t *testing.T) {
		t.Parallel()
		cfg := model.SMTPSettings{Host: "127.0.0.1", Port: 1, From: "a@example.com", TLS: "none"}
		if err := SendMail(mailTestCtx(t), nil, cfg, nil, "s", "b"); err == nil {
			t.Error("no recipients accepted")
		}
		many := make([]string, maxRecipients+1)
		for i := range many {
			many[i] = "a@example.com"
		}
		if err := SendMail(mailTestCtx(t), nil, cfg, many, "s", "b"); err == nil {
			t.Error("too many recipients accepted")
		}
	})

	t.Run("context cancel interrupts a hung server", func(t *testing.T) {
		t.Parallel()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		go func() { // accept, never greet
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				t.Cleanup(func() { _ = c.Close() })
			}
		}()
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(100*time.Millisecond, cancel)
		start := time.Now()
		err = SendMail(ctx, nil, mailTestSettings(t, ln.Addr().String()), []string{"a@example.com"}, "s", "b")
		if err == nil {
			t.Fatal("SendMail succeeded against a silent server")
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("SendMail took %s after cancel", d)
		}
	})

	t.Run("policy refuses link-local", func(t *testing.T) {
		t.Parallel()
		cfg := model.SMTPSettings{Host: "169.254.169.254", Port: 25, From: "a@example.com", TLS: "none"}
		start := time.Now()
		err := SendMail(mailTestCtx(t), netpolicy.New(""), cfg, []string{"a@example.com"}, "s", "b")
		if err == nil {
			t.Fatal("link-local address was dialed")
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("refusal took %s; the address was probably dialed", d)
		}
	})
}

func TestValidateSMTPSettings(t *testing.T) {
	t.Parallel()
	ok := model.SMTPSettings{Host: "smtp.example.com", Port: 587, From: "status@example.com", TLS: "starttls"}
	if err := ValidateSMTPSettings(ok); err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
	for _, tls := range []string{"starttls", "tls", "none"} {
		c := ok
		c.TLS = tls
		if err := ValidateSMTPSettings(c); err != nil {
			t.Errorf("tls %q rejected: %v", tls, err)
		}
	}
	tests := []struct {
		name string
		mut  func(*model.SMTPSettings)
	}{
		{"empty host", func(c *model.SMTPSettings) { c.Host = "" }},
		{"blank host", func(c *model.SMTPSettings) { c.Host = "  " }},
		{"port zero", func(c *model.SMTPSettings) { c.Port = 0 }},
		{"port too high", func(c *model.SMTPSettings) { c.Port = 65536 }},
		{"negative port", func(c *model.SMTPSettings) { c.Port = -1 }},
		{"bad tls", func(c *model.SMTPSettings) { c.TLS = "ssl" }},
		{"empty tls", func(c *model.SMTPSettings) { c.TLS = "" }},
		{"empty from", func(c *model.SMTPSettings) { c.From = "" }},
		{"bad from", func(c *model.SMTPSettings) { c.From = "not an address" }},
		{"from with newline", func(c *model.SMTPSettings) { c.From = "a@example.com\nBcc: x@example.com" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := ok
			tt.mut(&c)
			if err := ValidateSMTPSettings(c); err == nil {
				t.Errorf("%+v accepted", c)
			}
			if err := SendMail(mailTestCtx(t), nil, c, []string{"a@example.com"}, "s", "b"); err == nil {
				t.Errorf("SendMail accepted %+v", c)
			}
		})
	}
}

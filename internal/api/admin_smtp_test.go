package api_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/davidsugianto/upsera/internal/api"
	"github.com/davidsugianto/upsera/internal/config"
	"github.com/davidsugianto/upsera/internal/events"
	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/netpolicy"
	"github.com/davidsugianto/upsera/internal/store"
	"github.com/davidsugianto/upsera/internal/testutil"
)

const smtpTestPassword = "pw-very-secret-9137"

type smtpTestEnv struct {
	base      string
	dbURL     string
	st        *store.Store
	admin     *http.Client
	adminCSRF string
	viewer    *http.Client
	viewerCSR string
	anon      *http.Client
}

// smtpTestSetup starts a server over a real store (kept so tests can read
// what was persisted) with an admin and a non-admin user logged in.
func smtpTestSetup(t *testing.T) *smtpTestEnv {
	t.Helper()
	st, dbURL := testutil.Store(t)
	h := api.NewRouter(api.Deps{
		Store: st, Runner: newFakeRunner(), Alerting: newFakeAlerting(), Maintenance: newFakeMaintenance(),
		Policy: netpolicy.New(""), Config: config.Config{BaseURL: "http://upsera.test", Port: 3080},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test", Events: events.NewHub(),
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	e := &smtpTestEnv{base: srv.URL, dbURL: dbURL, st: st, admin: newClient(t), viewer: newClient(t), anon: newClient(t)}
	resp, body := do(t, e.admin, http.MethodPost, e.base+"/api/setup", "", "", map[string]any{
		"email": "admin@example.com", "name": "Admin", "password": "correct horse battery", "team_name": "Ops",
	})
	requireStatus(t, resp, http.StatusCreated, body)
	var me meResponse
	decodeInto(t, body, &me)
	e.adminCSRF = me.CSRFToken

	resp, body = do(t, e.admin, http.MethodPost, e.base+"/api/admin/users", e.adminCSRF, "", map[string]any{
		"email": "viewer@example.com", "name": "Viewer", "password": "viewer password", "is_admin": false,
	})
	requireStatus(t, resp, http.StatusCreated, body)
	resp, body = do(t, e.viewer, http.MethodPost, e.base+"/api/auth/login", "", "", map[string]any{
		"email": "viewer@example.com", "password": "viewer password",
	})
	requireStatus(t, resp, http.StatusOK, body)
	var vme meResponse
	decodeInto(t, body, &vme)
	e.viewerCSR = vme.CSRFToken
	return e
}

func (e *smtpTestEnv) settings(t *testing.T) map[string]any {
	t.Helper()
	resp, body := do(t, e.admin, http.MethodGet, e.base+"/api/admin/settings", "", "", nil)
	requireStatus(t, resp, http.StatusOK, body)
	return body
}

// put sends PUT /api/admin/settings with a raw map body.
func (e *smtpTestEnv) put(t *testing.T, body map[string]any) (*http.Response, map[string]any) {
	t.Helper()
	return do(t, e.admin, http.MethodPut, e.base+"/api/admin/settings", e.adminCSRF, "", body)
}

func smtpTestObject(host string, port int, password any) map[string]any {
	o := map[string]any{
		"host": host, "port": port, "username": "mailer", "from": "Upsera <status@example.com>", "tls": "starttls",
	}
	if password != nil {
		o["password"] = password
	}
	return o
}

func smtpTestSplit(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	var p int
	if _, err := fmt.Sscanf(port, "%d", &p); err != nil {
		t.Fatal(err)
	}
	return host, p
}

func TestAdminSMTPSettings(t *testing.T) {
	e := smtpTestSetup(t)
	ctx := context.Background()

	t.Run("unset is null", func(t *testing.T) {
		body := e.settings(t)
		if v, ok := body["smtp"]; !ok || v != nil {
			t.Fatalf("smtp = %v (present=%v), want null", v, ok)
		}
		if body["block_private_targets"] != false {
			t.Errorf("existing fields lost: %v", body)
		}
	})

	t.Run("set redacts the password", func(t *testing.T) {
		resp, body := e.put(t, map[string]any{
			"block_private_targets": false, "retention_days": 30,
			"smtp": smtpTestObject("smtp.example.com", 587, smtpTestPassword),
		})
		requireStatus(t, resp, http.StatusOK, body)
		for _, b := range []map[string]any{body, e.settings(t)} {
			s, _ := b["smtp"].(map[string]any)
			if s == nil {
				t.Fatalf("smtp missing: %v", b)
			}
			if s["host"] != "smtp.example.com" || s["port"] != float64(587) || s["username"] != "mailer" ||
				s["from"] != "Upsera <status@example.com>" || s["tls"] != "starttls" || s["password"] != "********" {
				t.Errorf("smtp = %v", s)
			}
			if raw := fmt.Sprint(b); strings.Contains(raw, smtpTestPassword) {
				t.Errorf("password leaked: %v", b)
			}
		}
		stored, err := e.st.GetSMTPSettings(ctx)
		if err != nil || stored == nil || stored.Password != smtpTestPassword {
			t.Fatalf("stored = %+v, %v", stored, err)
		}
	})

	t.Run("omitted keeps", func(t *testing.T) {
		resp, body := e.put(t, map[string]any{"block_private_targets": false, "retention_days": 45})
		requireStatus(t, resp, http.StatusOK, body)
		s, _ := body["smtp"].(map[string]any)
		if s == nil || s["host"] != "smtp.example.com" || s["password"] != "********" {
			t.Fatalf("smtp changed by an update that omitted it: %v", body["smtp"])
		}
		if body["retention_days"] != float64(45) {
			t.Errorf("retention_days = %v", body["retention_days"])
		}
	})

	t.Run("mask keeps the stored password", func(t *testing.T) {
		resp, body := e.put(t, map[string]any{
			"block_private_targets": false, "smtp": smtpTestObject("smtp2.example.com", 465, "********"),
		})
		requireStatus(t, resp, http.StatusOK, body)
		stored, err := e.st.GetSMTPSettings(ctx)
		if err != nil || stored == nil {
			t.Fatalf("stored = %+v, %v", stored, err)
		}
		if stored.Host != "smtp2.example.com" || stored.Port != 465 || stored.Password != smtpTestPassword {
			t.Errorf("stored = %+v, want new host and the old password", stored)
		}
	})

	t.Run("omitted password keeps the stored password", func(t *testing.T) {
		resp, body := e.put(t, map[string]any{
			"block_private_targets": false, "smtp": smtpTestObject("smtp3.example.com", 587, nil),
		})
		requireStatus(t, resp, http.StatusOK, body)
		stored, _ := e.st.GetSMTPSettings(ctx)
		if stored == nil || stored.Host != "smtp3.example.com" || stored.Password != smtpTestPassword {
			t.Errorf("stored = %+v", stored)
		}
	})

	t.Run("new password replaces", func(t *testing.T) {
		resp, body := e.put(t, map[string]any{
			"block_private_targets": false, "smtp": smtpTestObject("smtp3.example.com", 587, "another-pw"),
		})
		requireStatus(t, resp, http.StatusOK, body)
		if stored, _ := e.st.GetSMTPSettings(ctx); stored == nil || stored.Password != "another-pw" {
			t.Errorf("stored = %+v", stored)
		}
	})

	t.Run("empty password clears it", func(t *testing.T) {
		resp, body := e.put(t, map[string]any{
			"block_private_targets": false, "smtp": smtpTestObject("smtp3.example.com", 587, ""),
		})
		requireStatus(t, resp, http.StatusOK, body)
		if s, _ := body["smtp"].(map[string]any); s == nil || s["password"] != "" {
			t.Errorf("smtp = %v, want an empty password", body["smtp"])
		}
		if stored, _ := e.st.GetSMTPSettings(ctx); stored == nil || stored.Password != "" {
			t.Errorf("stored = %+v", stored)
		}
	})

	t.Run("invalid settings are 422 and change nothing", func(t *testing.T) {
		before, _ := e.st.GetSMTPSettings(ctx)
		valid := func() map[string]any { return smtpTestObject("smtp.example.com", 587, "pw") }
		tests := []struct {
			name string
			mut  func(map[string]any)
		}{
			{"no host", func(o map[string]any) { o["host"] = "" }},
			{"port 0", func(o map[string]any) { o["port"] = 0 }},
			{"port too high", func(o map[string]any) { o["port"] = 70000 }},
			{"bad tls", func(o map[string]any) { o["tls"] = "ssl" }},
			{"bad from", func(o map[string]any) { o["from"] = "nope" }},
			{"auth without tls", func(o map[string]any) { o["tls"] = "none" }},
			{"unknown field", func(o map[string]any) { o["bogus"] = 1 }},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				o := valid()
				tt.mut(o)
				resp, body := e.put(t, map[string]any{"block_private_targets": true, "retention_days": 99, "smtp": o})
				requireStatus(t, resp, http.StatusUnprocessableEntity, body)
			})
		}
		resp, body := e.put(t, map[string]any{"block_private_targets": true, "smtp": "a string"})
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("non-object smtp: status = %d, body = %v", resp.StatusCode, body)
		}
		after, _ := e.st.GetSMTPSettings(ctx)
		if before == nil || after == nil || *before != *after {
			t.Errorf("settings changed by invalid updates: %+v -> %+v", before, after)
		}
		if got := e.settings(t); got["retention_days"] == float64(99) || got["block_private_targets"] == true {
			t.Errorf("other settings applied by a rejected update: %v", got)
		}
	})

	t.Run("null clears", func(t *testing.T) {
		resp, body := e.put(t, map[string]any{"block_private_targets": false, "smtp": nil})
		requireStatus(t, resp, http.StatusOK, body)
		if v, ok := body["smtp"]; !ok || v != nil {
			t.Errorf("smtp = %v, want null", v)
		}
		if stored, err := e.st.GetSMTPSettings(ctx); err != nil || stored != nil {
			t.Errorf("stored = %+v, %v; want nil", stored, err)
		}
		if got := e.settings(t); got["smtp"] != nil {
			t.Errorf("GET smtp = %v", got["smtp"])
		}
	})

	t.Run("undecryptable stored value", func(t *testing.T) {
		// A value written under another APP_SECRET.
		conn, err := pgx.Connect(ctx, e.dbURL)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, "UPDATE upsera.instance_settings SET smtp = 'v1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' WHERE id = 1"); err != nil {
			t.Fatal(err)
		}
		body := e.settings(t)
		if v, ok := body["smtp"]; !ok || v != nil {
			t.Errorf("smtp = %v, want null", v)
		}
		if body["smtp_error"] != "cannot decrypt SMTP settings (APP_SECRET changed?)" {
			t.Errorf("smtp_error = %v", body["smtp_error"])
		}
		// Replacing it with a fresh password works; keeping the old one cannot.
		resp, body := e.put(t, map[string]any{"block_private_targets": false, "smtp": smtpTestObject("smtp.example.com", 587, nil)})
		requireStatus(t, resp, http.StatusConflict, body)
		resp, body = e.put(t, map[string]any{"block_private_targets": false, "smtp": smtpTestObject("smtp.example.com", 587, "fresh")})
		requireStatus(t, resp, http.StatusOK, body)
	})
}

func TestAdminSMTPAuthz(t *testing.T) {
	e := smtpTestSetup(t)
	settings := map[string]any{"block_private_targets": false, "smtp": nil}
	test := map[string]any{"to": "me@example.com"}
	tests := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"get", http.MethodGet, "/api/admin/settings", nil},
		{"put", http.MethodPut, "/api/admin/settings", settings},
		{"test", http.MethodPost, "/api/admin/settings/smtp/test", test},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := do(t, e.viewer, tt.method, e.base+tt.path, e.viewerCSR, "", tt.body)
			requireStatus(t, resp, http.StatusForbidden, body)
			resp, body = do(t, e.anon, tt.method, e.base+tt.path, "", "", tt.body)
			requireStatus(t, resp, http.StatusUnauthorized, body)
		})
	}
	if stored, _ := e.st.GetSMTPSettings(context.Background()); stored != nil {
		t.Errorf("a refused request stored settings: %+v", stored)
	}
}

// smtpTestRejectingServer is an SMTP server that refuses AUTH with a 535
// that echoes the credentials back, as a careless server might.
func smtpTestRejectingServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				w := func(l string) { _, _ = conn.Write([]byte(l + "\r\n")) }
				w("220 fake")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					up := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(up, "EHLO"):
						w("250-fake")
						w("250 AUTH PLAIN")
					case strings.HasPrefix(up, "AUTH PLAIN "):
						raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line)[len("AUTH PLAIN "):])
						w("535 5.7.8 rejected credentials " + strings.ReplaceAll(string(raw), "\x00", "/"))
					case up == "QUIT":
						w("221 bye")
						return
					default:
						w("250 ok")
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func TestAdminSMTPTest(t *testing.T) {
	e := smtpTestSetup(t)
	ctx := context.Background()
	testURL := e.base + "/api/admin/settings/smtp/test"

	t.Run("not configured is 409", func(t *testing.T) {
		resp, body := do(t, e.admin, http.MethodPost, testURL, e.adminCSRF, "", map[string]any{"to": "me@example.com"})
		requireStatus(t, resp, http.StatusConflict, body)
		if !strings.Contains(fmt.Sprint(body), "SMTP is not configured") {
			t.Errorf("body = %v", body)
		}
	})

	t.Run("invalid recipient is 422", func(t *testing.T) {
		resp, body := do(t, e.admin, http.MethodPost, testURL, e.adminCSRF, "", map[string]any{"to": "not an email"})
		requireStatus(t, resp, http.StatusUnprocessableEntity, body)
	})

	t.Run("success", func(t *testing.T) {
		addr, messages := testutil.FakeSMTP(t)
		host, port := smtpTestSplit(t, addr)
		o := map[string]any{"host": host, "port": port, "from": "Upsera <status@example.com>", "tls": "none"}
		resp, body := e.put(t, map[string]any{"block_private_targets": false, "smtp": o})
		requireStatus(t, resp, http.StatusOK, body)

		resp, body = do(t, e.admin, http.MethodPost, testURL, e.adminCSRF, "", map[string]any{"to": "me@example.com"})
		requireStatus(t, resp, http.StatusOK, body)
		if body["ok"] != true {
			t.Errorf("body = %v", body)
		}
		msgs := messages()
		if len(msgs) != 1 || len(msgs[0].To) != 1 || msgs[0].To[0] != "me@example.com" || msgs[0].From != "status@example.com" {
			t.Fatalf("messages = %+v", msgs)
		}
		if !strings.Contains(msgs[0].Data, "Subject: Upsera SMTP test\r\n") {
			t.Errorf("data = %q", msgs[0].Data)
		}
	})

	t.Run("failure is 502 without the password", func(t *testing.T) {
		host, port := smtpTestSplit(t, smtpTestRejectingServer(t))
		// Sending credentials needs TLS, so use the loopback exception of
		// net/smtp's PlainAuth via tls "none" at the store level.
		if err := e.st.SetSMTPSettings(ctx, &model.SMTPSettings{
			Host: host, Port: port, Username: "mailer", Password: smtpTestPassword,
			From: "status@example.com", TLS: "none",
		}); err != nil {
			t.Fatal(err)
		}
		resp, body := do(t, e.admin, http.MethodPost, testURL, e.adminCSRF, "", map[string]any{"to": "me@example.com"})
		requireStatus(t, resp, http.StatusBadGateway, body)
		raw := fmt.Sprint(body)
		if !strings.Contains(raw, "send failed: ") {
			t.Errorf("body = %v", body)
		}
		if strings.Contains(raw, smtpTestPassword) {
			t.Errorf("password leaked in the error: %v", body)
		}

		// Unreachable server.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		host, port = smtpTestSplit(t, ln.Addr().String())
		_ = ln.Close()
		if err := e.st.SetSMTPSettings(ctx, &model.SMTPSettings{
			Host: host, Port: port, From: "status@example.com", TLS: "none",
		}); err != nil {
			t.Fatal(err)
		}
		resp, body = do(t, e.admin, http.MethodPost, testURL, e.adminCSRF, "", map[string]any{"to": "me@example.com"})
		requireStatus(t, resp, http.StatusBadGateway, body)
	})

	t.Run("audit has no password", func(t *testing.T) {
		resp, body := e.put(t, map[string]any{
			"block_private_targets": false, "smtp": smtpTestObject("audit.example.com", 587, smtpTestPassword),
		})
		requireStatus(t, resp, http.StatusOK, body)

		conn, err := pgx.Connect(ctx, e.dbURL)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		rows, err := conn.Query(ctx, "SELECT action, details::text FROM upsera.audit_log WHERE team_id IS NULL ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		seen := map[string]int{}
		var lastUpdate string
		for rows.Next() {
			var action, details string
			if err := rows.Scan(&action, &details); err != nil {
				t.Fatal(err)
			}
			seen[action]++
			if strings.Contains(details, smtpTestPassword) || strings.Contains(details, "mailer") {
				t.Errorf("audit %s leaks credentials: %s", action, details)
			}
			switch action {
			case "settings.update":
				lastUpdate = details
			case "settings.smtp_test":
				if !strings.Contains(details, "me@example.com") {
					t.Errorf("smtp_test audit = %s", details)
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if seen["settings.update"] == 0 || seen["settings.smtp_test"] == 0 {
			t.Errorf("audit actions = %v, want settings.update and settings.smtp_test", seen)
		}
		if !strings.Contains(lastUpdate, "audit.example.com") {
			t.Errorf("settings.update audit = %s, want the SMTP host", lastUpdate)
		}
	})
}

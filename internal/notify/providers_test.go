package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return body
}

func TestTelegramSend(t *testing.T) {
	t.Parallel()
	const token = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"
	for _, ackable := range []bool{true, false} {
		var gotPath string
		var gotBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotBody = decodeBody(t, r)
			w.Write([]byte(`{"ok":true}`))
		}))

		tg := &telegram{api: srv.URL, client: srv.Client()}
		cfg, _ := json.Marshal(map[string]string{"bot_token": token, "chat_id": "42"})
		ev := Event{Kind: model.EventDown, MonitorName: "api", Message: "boom", AlertID: "alert-1", Ackable: ackable}
		if err := tg.Send(context.Background(), cfg, ev); err != nil {
			t.Fatalf("Send: %v", err)
		}
		srv.Close()

		if gotPath != "/bot"+token+"/sendMessage" {
			t.Errorf("path = %q, want /bot%s/sendMessage", gotPath, token)
		}
		if gotBody["chat_id"] != "42" {
			t.Errorf("chat_id = %v, want 42", gotBody["chat_id"])
		}
		if gotBody["text"] != Title(ev)+"\n"+Body(ev) {
			t.Errorf("text = %v, want %q", gotBody["text"], Title(ev)+"\n"+Body(ev))
		}
		rm, hasRM := gotBody["reply_markup"]
		if ackable != hasRM {
			t.Fatalf("ackable=%v: reply_markup present=%v", ackable, hasRM)
		}
		if ackable {
			kb := rm.(map[string]any)["inline_keyboard"].([]any)[0].([]any)[0].(map[string]any)
			if kb["callback_data"] != "ack:alert-1" {
				t.Errorf("callback_data = %v, want ack:alert-1", kb["callback_data"])
			}
		}
	}
}

func TestDiscordSend(t *testing.T) {
	t.Parallel()
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
	}))
	defer srv.Close()

	d := &discord{client: srv.Client()}
	cfg, _ := json.Marshal(map[string]string{"webhook_url": srv.URL})
	ev := Event{Kind: model.EventDown, MonitorName: "api", Message: "boom", At: time.Now()}
	if err := d.Send(context.Background(), cfg, ev); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotBody["content"] != Title(ev)+"\n"+Body(ev) {
		t.Errorf("content = %v", gotBody["content"])
	}
}

func TestSlackWebhookSend(t *testing.T) {
	t.Parallel()
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = decodeBody(t, r)
	}))
	defer srv.Close()

	s := &slackWebhook{client: srv.Client()}
	cfg, _ := json.Marshal(map[string]string{"webhook_url": srv.URL})
	ev := Event{Kind: model.EventRecovered, MonitorName: "api", Downtime: 2 * time.Minute}
	if err := s.Send(context.Background(), cfg, ev); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotBody["text"] != "*"+Title(ev)+"*\n"+Body(ev) {
		t.Errorf("text = %v", gotBody["text"])
	}
}

func TestSlackAppSend(t *testing.T) {
	t.Parallel()
	for _, ackable := range []bool{true, false} {
		var gotAuth string
		var gotBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/chat.postMessage" {
				t.Errorf("path = %q, want /chat.postMessage", r.URL.Path)
			}
			gotAuth = r.Header.Get("Authorization")
			gotBody = decodeBody(t, r)
			w.Write([]byte(`{"ok":true}`))
		}))

		s := &slackApp{api: srv.URL, client: srv.Client()}
		cfg, _ := json.Marshal(map[string]string{
			"bot_token": "xoxb-secret", "app_token": "xapp-secret", "channel": "C0123",
		})
		ev := Event{Kind: model.EventDown, MonitorName: "api", Message: "boom", AlertID: "alert-9", Ackable: ackable}
		if err := s.Send(context.Background(), cfg, ev); err != nil {
			t.Fatalf("Send: %v", err)
		}
		srv.Close()

		if gotAuth != "Bearer xoxb-secret" {
			t.Errorf("Authorization = %q, want Bearer xoxb-secret", gotAuth)
		}
		blocks := gotBody["blocks"].([]any)
		wantLen := 1
		if ackable {
			wantLen = 2
		}
		if len(blocks) != wantLen {
			t.Fatalf("ackable=%v: len(blocks) = %d, want %d", ackable, len(blocks), wantLen)
		}
		if ackable {
			actions := blocks[1].(map[string]any)["elements"].([]any)[0].(map[string]any)
			if actions["action_id"] != slackAckActionID {
				t.Errorf("action_id = %v, want %s", actions["action_id"], slackAckActionID)
			}
			if actions["value"] != "alert-9" {
				t.Errorf("value = %v, want alert-9", actions["value"])
			}
		}
	}
}

func TestSlackAppSendError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
	}))
	defer srv.Close()

	s := &slackApp{api: srv.URL, client: srv.Client()}
	cfg, _ := json.Marshal(map[string]string{
		"bot_token": "xoxb-secret", "app_token": "xapp-secret", "channel": "C0123",
	})
	err := s.Send(context.Background(), cfg, Event{Kind: model.EventTest})
	if err == nil || err.Error() != "slack: channel_not_found" {
		t.Fatalf("err = %v, want %q", err, "slack: channel_not_found")
	}
}

func TestWebhookSend(t *testing.T) {
	t.Parallel()
	certAt := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	cases := []struct {
		name string
		ev   Event
		want string // expected cert_expires_at JSON fragment
	}{
		{name: "no cert", ev: Event{Kind: model.EventDown, MonitorID: 5, MonitorName: "api"}, want: "null"},
		{name: "with cert", ev: Event{Kind: model.EventCertExpiry, CertExpiresAt: certAt, CertDaysLeft: 3}, want: `"2024-01-02T03:04:05Z"`},
	}
	for _, c := range cases {
		var gotHeader string
		var gotBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotHeader = r.Header.Get("X-Custom")
			gotBody = decodeBody(t, r)
		}))

		wh := &webhook{client: srv.Client()}
		cfg, _ := json.Marshal(map[string]any{"url": srv.URL, "headers": map[string]string{"X-Custom": "yes"}})
		if err := wh.Send(context.Background(), cfg, c.ev); err != nil {
			t.Fatalf("%s: Send: %v", c.name, err)
		}
		srv.Close()

		if gotHeader != "yes" {
			t.Errorf("%s: X-Custom header = %q, want yes", c.name, gotHeader)
		}
		if gotBody["event"] != string(c.ev.Kind) {
			t.Errorf("%s: event = %v", c.name, gotBody["event"])
		}
		raw, _ := json.Marshal(gotBody["cert_expires_at"])
		if string(raw) != c.want {
			t.Errorf("%s: cert_expires_at = %s, want %s", c.name, raw, c.want)
		}
	}
}

func TestSendNon2xxDoesNotLeakURL(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := &discord{client: srv.Client()}
	cfg, _ := json.Marshal(map[string]string{"webhook_url": srv.URL})
	err := d.Send(context.Background(), cfg, Event{Kind: model.EventTest})
	if err == nil || err.Error() != "HTTP 500" {
		t.Fatalf("err = %v, want %q", err, "HTTP 500")
	}
	if strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("err %q leaks the server URL", err.Error())
	}
}

func TestValidateUnknownFieldRejected(t *testing.T) {
	t.Parallel()
	cfg, _ := json.Marshal(map[string]string{"webhook_url": "https://example.com/x", "extra": "nope"})
	if err := Validate(model.ChannelDiscord, cfg); err == nil {
		t.Fatal("Validate with unknown field = nil, want error")
	}
}

func TestValidateTelegram(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		cfg     map[string]string
		wantErr bool
	}{
		{name: "valid", cfg: map[string]string{"bot_token": "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ012345", "chat_id": "1"}, wantErr: false},
		{name: "bad token", cfg: map[string]string{"bot_token": "not-a-token", "chat_id": "1"}, wantErr: true},
		{name: "short token", cfg: map[string]string{"bot_token": "123:short", "chat_id": "1"}, wantErr: true},
		{name: "missing chat_id", cfg: map[string]string{"bot_token": "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"}, wantErr: true},
	}
	for _, c := range cases {
		cfg, _ := json.Marshal(c.cfg)
		err := Validate(model.ChannelTelegram, cfg)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: Validate() err = %v, wantErr %v", c.name, err, c.wantErr)
		}
	}
}

func TestValidateSMTPAuthRequiresTLS(t *testing.T) {
	t.Parallel()
	cfg, _ := json.Marshal(map[string]any{
		"host": "mail.example.com", "port": 25, "username": "u", "password": "p",
		"from": "a@example.com", "to": []string{"b@example.com"}, "tls": "none",
	})
	err := Validate(model.ChannelSMTP, cfg)
	if err == nil || err.Error() != "authentication requires tls or starttls" {
		t.Fatalf("err = %v, want %q", err, "authentication requires tls or starttls")
	}
}

func TestValidateUnknownChannelType(t *testing.T) {
	t.Parallel()
	if err := Validate(model.ChannelType("carrier_pigeon"), []byte(`{}`)); err == nil {
		t.Fatal("Validate(unknown type) = nil, want error")
	}
}

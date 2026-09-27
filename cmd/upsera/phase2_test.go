package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/config"
	"github.com/davidsugianto/upsera/internal/testutil"
)

// startServer runs the real server against a fresh Postgres until the test
// ends (or stop is called) and returns its base URL. stop returns run's
// error.
func startServer(t *testing.T, dbURL string, tweak func(*config.Config)) (base string, stop func() error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base = "http://" + ln.Addr().String()
	cfg := config.Config{
		DatabaseURL: dbURL, AppSecret: strings.Repeat("s", 32), BaseURL: base,
		Port: ln.Addr().(*net.TCPAddr).Port, LogFormat: "text",
		DBMaxConns: 5, HeartbeatBufferSize: 1000, RetentionDays: 14, MaxConcurrentChecks: 10,
		FlushInterval: 200 * time.Millisecond, RollupEvery: time.Hour, TimeZone: "UTC",
		TelegramAPIURL: "http://127.0.0.1:1", SlackAPIURL: "http://127.0.0.1:1",
	}
	if tweak != nil {
		tweak(&cfg)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, cfg, log, ln) }()
	var once sync.Once
	var stopErr error
	stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case stopErr = <-runErr:
			case <-time.After(30 * time.Second):
				stopErr = fmt.Errorf("server did not shut down")
			}
		})
		return stopErr
	}
	t.Cleanup(func() { _ = stop() })

	waitFor(t, 60*time.Second, func() bool {
		resp, err := http.Get(base + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	return base, stop
}

// setupAdmin runs first-run setup and returns the team URL and a write
// token's Authorization header.
func setupAdmin(t *testing.T, base string) (teamURL, bearer string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}
	var me struct {
		Teams []struct {
			ID int64 `json:"id"`
		} `json:"teams"`
		CSRFToken string `json:"csrf_token"`
	}
	call(t, browser, "POST", base+"/api/setup", "", map[string]any{
		"email": "admin@example.com", "name": "Admin", "password": "correct horse battery", "team_name": "Ops",
	}, http.StatusCreated, &me)
	teamURL = fmt.Sprintf("%s/api/teams/%d", base, me.Teams[0].ID)
	var tok struct {
		Token string `json:"token"`
	}
	callCSRF(t, browser, "POST", teamURL+"/tokens", me.CSRFToken, map[string]any{"name": "ci", "scope": "write"}, &tok)
	return teamURL, "Bearer " + tok.Token
}

// fakeTelegram records sendMessage calls and serves getUpdates from a
// queue, long-polling up to 1s like the real Bot API.
type fakeTelegram struct {
	mu       sync.Mutex
	messages []tgMessage
	updates  []map[string]any
	nextID   int
	answers  []string
	wake     chan struct{}
}

type tgMessage struct {
	ChatID      string `json:"chat_id"`
	Text        string `json:"text"`
	ReplyMarkup struct {
		InlineKeyboard [][]struct {
			Text         string `json:"text"`
			CallbackData string `json:"callback_data"`
		} `json:"inline_keyboard"`
	} `json:"reply_markup"`
}

func newFakeTelegram(t *testing.T) (*fakeTelegram, *httptest.Server) {
	f := &fakeTelegram{wake: make(chan struct{}, 1)}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeTelegram) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/sendMessage"):
		var m tgMessage
		_ = json.NewDecoder(r.Body).Decode(&m)
		f.mu.Lock()
		f.messages = append(f.messages, m)
		f.mu.Unlock()
		io.WriteString(w, `{"ok":true,"result":{}}`)
	case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
		var a struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&a)
		f.mu.Lock()
		f.answers = append(f.answers, a.Text)
		f.mu.Unlock()
		io.WriteString(w, `{"ok":true,"result":true}`)
	case strings.HasSuffix(r.URL.Path, "/getUpdates"):
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		deadline := time.After(time.Second)
		for {
			f.mu.Lock()
			var out []map[string]any
			for _, u := range f.updates {
				if u["update_id"].(int) >= offset {
					out = append(out, u)
				}
			}
			f.mu.Unlock()
			if len(out) > 0 {
				json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": out})
				return
			}
			select {
			case <-f.wake:
			case <-deadline:
				io.WriteString(w, `{"ok":true,"result":[]}`)
				return
			case <-r.Context().Done():
				return
			}
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeTelegram) pressAck(chatID int64, data string) {
	f.mu.Lock()
	f.nextID++
	f.updates = append(f.updates, map[string]any{
		"update_id": f.nextID,
		"callback_query": map[string]any{
			"id":      fmt.Sprint("cb", f.nextID),
			"from":    map[string]any{"id": 1, "username": "oncall"},
			"message": map[string]any{"message_id": 1, "chat": map[string]any{"id": chatID}},
			"data":    data,
		},
	})
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// withPrefix returns the messages whose text starts with prefix.
func (f *fakeTelegram) withPrefix(prefix string) []tgMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tgMessage
	for _, m := range f.messages {
		if strings.HasPrefix(m.Text, prefix) {
			out = append(out, m)
		}
	}
	return out
}

func mailsWithSubject(msgs []testutil.SMTPMessage, subject string) int {
	n := 0
	for _, m := range msgs {
		if strings.Contains(m.Data, "\r\nSubject: "+subject+"\r\n") {
			n++
		}
	}
	return n
}

// TestPhase2DoneWhen exercises the phase 2 acceptance criterion end to end
// through the real server: an unacknowledged outage escalates from
// Telegram to email, a DOWN parent silences its children, and pressing
// Acknowledge in Telegram stops the escalation.
func TestPhase2DoneWhen(t *testing.T) {
	dbURL := testutil.PostgresURL(t)
	tg, tgSrv := newFakeTelegram(t)
	smtpAddr, mails := testutil.FakeSMTP(t)
	smtpHost, smtpPortStr, _ := net.SplitHostPort(smtpAddr)
	smtpPort, _ := strconv.Atoi(smtpPortStr)

	base, _ := startServer(t, dbURL, func(c *config.Config) { c.TelegramAPIURL = tgSrv.URL })
	teamURL, bearer := setupAdmin(t, base)

	type idResp struct {
		ID      int64  `json:"id"`
		PushURL string `json:"push_url"`
	}
	const chatID = 4242
	var tgCh, mailCh, pol idResp
	call(t, http.DefaultClient, "POST", teamURL+"/channels", bearer, map[string]any{
		"type": "telegram", "name": "tg",
		"config": map[string]any{"bot_token": "123456:" + strings.Repeat("A", 35), "chat_id": fmt.Sprint(chatID)},
	}, http.StatusCreated, &tgCh)
	call(t, http.DefaultClient, "POST", teamURL+"/channels", bearer, map[string]any{
		"type": "smtp", "name": "mail",
		"config": map[string]any{"host": smtpHost, "port": smtpPort, "from": "upsera@example.com",
			"to": []string{"oncall@example.com"}, "tls": "none"},
	}, http.StatusCreated, &mailCh)
	call(t, http.DefaultClient, "POST", teamURL+"/escalation-policies", bearer, map[string]any{
		"name": "P1", "steps": []map[string]any{
			{"channel_ids": []int64{tgCh.ID}, "delay_s": 10},
			{"channel_ids": []int64{mailCh.ID}},
		},
	}, http.StatusCreated, &pol)

	pushMonitor := func(name string, parent *int64) idResp {
		body := map[string]any{"name": name, "type": "push", "interval_s": 60, "retries": 0,
			"config": map[string]any{}, "escalation_policy_id": pol.ID}
		if parent != nil {
			body["parent_id"] = *parent
		}
		var m idResp
		call(t, http.DefaultClient, "POST", teamURL+"/monitors", bearer, body, http.StatusCreated, &m)
		return m
	}
	parent := pushMonitor("parent", nil)
	child := pushMonitor("child", &parent.ID)
	solo := pushMonitor("solo", nil)
	push := func(m idResp, status string) {
		call(t, http.DefaultClient, "GET", m.PushURL+"?status="+status, "", nil, http.StatusOK, nil)
	}

	// Scenario 1: Telegram first, email after the 10s step delay, then
	// recovery on both.
	push(solo, "down")
	waitFor(t, 5*time.Second, func() bool { return len(tg.withPrefix("[DOWN] solo")) == 1 })
	if kb := tg.withPrefix("[DOWN] solo")[0].ReplyMarkup.InlineKeyboard; len(kb) != 1 || kb[0][0].Text != "Acknowledge" {
		t.Fatalf("telegram DOWN message has no Acknowledge button: %+v", kb)
	}
	waitFor(t, 20*time.Second, func() bool { return mailsWithSubject(mails(), "[DOWN] solo") == 1 })
	msgs := mails()
	if len(msgs) != 1 || len(msgs[0].To) != 1 || msgs[0].To[0] != "oncall@example.com" {
		t.Fatalf("mail = %+v", msgs)
	}
	push(solo, "up")
	waitFor(t, 5*time.Second, func() bool { return len(tg.withPrefix("[UP] solo")) == 1 })
	waitFor(t, 5*time.Second, func() bool { return mailsWithSubject(mails(), "[UP] solo") == 1 })

	// Scenario 2: a DOWN parent silences its child.
	push(parent, "down")
	waitFor(t, 5*time.Second, func() bool { return len(tg.withPrefix("[DOWN] parent")) == 1 })
	push(child, "down")

	// Ack path (runs while scenario 2's quiet period elapses): a new
	// outage of solo, acknowledged from Telegram before it escalates.
	push(solo, "down")
	waitFor(t, 5*time.Second, func() bool { return len(tg.withPrefix("[DOWN] solo")) == 2 })
	data := tg.withPrefix("[DOWN] solo")[1].ReplyMarkup.InlineKeyboard[0][0].CallbackData
	alertID, ok := strings.CutPrefix(data, "ack:")
	if !ok || alertID == "" {
		t.Fatalf("callback_data = %q", data)
	}
	tg.pressAck(chatID, data)

	type alertsResp struct {
		Alerts []struct {
			ID         string `json:"id"`
			MonitorID  int64  `json:"monitor_id"`
			Suppressed bool   `json:"suppressed"`
			Acked      bool   `json:"acked"`
			AckSource  string `json:"ack_source"`
		} `json:"alerts"`
	}
	waitFor(t, 5*time.Second, func() bool {
		var ar alertsResp
		call(t, http.DefaultClient, "GET", teamURL+"/alerts?state=open", bearer, nil, http.StatusOK, &ar)
		for _, a := range ar.Alerts {
			if a.ID == alertID {
				return a.Acked && a.AckSource == "telegram"
			}
		}
		return false
	})
	ackedAt := time.Now()
	tg.mu.Lock()
	answers := slices.Clone(tg.answers)
	tg.mu.Unlock()
	if !slices.Contains(answers, "Acknowledged") {
		t.Fatalf("answerCallbackQuery texts = %q, want Acknowledged", answers)
	}

	// 15s quiet period: covers the child (scenario 2) and the acked solo
	// alert's 10s escalation delay.
	time.Sleep(15*time.Second - time.Since(ackedAt))
	for _, m := range tg.withPrefix("[DOWN] child") {
		t.Fatalf("child notified on Telegram while its parent is down: %+v", m)
	}
	if n := mailsWithSubject(mails(), "[DOWN] child"); n != 0 {
		t.Fatalf("child emailed while its parent is down (%d mails)", n)
	}
	if n := mailsWithSubject(mails(), "[DOWN] solo"); n != 1 {
		t.Fatalf("acknowledged solo alert escalated to email (%d DOWN mails, want only scenario 1's)", n)
	}
	var ar alertsResp
	call(t, http.DefaultClient, "GET", teamURL+"/alerts?state=open", bearer, nil, http.StatusOK, &ar)
	found := false
	for _, a := range ar.Alerts {
		if a.MonitorID == child.ID {
			found = true
			if !a.Suppressed {
				t.Fatalf("child alert = %+v, want suppressed", a)
			}
		}
	}
	if !found {
		t.Fatalf("no open alert for child: %+v", ar.Alerts)
	}
}

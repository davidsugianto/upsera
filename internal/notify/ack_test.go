package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/davidsugianto/upsera/internal/model"
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestAckListenersTelegram(t *testing.T) {
	t.Parallel()
	const token = "555555:ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"

	var mu sync.Mutex
	var acked []string
	onAck := func(id string, src model.AckSource, name string) error {
		mu.Lock()
		defer mu.Unlock()
		acked = append(acked, id+"|"+string(src)+"|"+name)
		return nil
	}

	updates := make(chan map[string]any)
	var answerMu sync.Mutex
	var answerBodies []map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("/bot"+token+"/getUpdates", func(w http.ResponseWriter, r *http.Request) {
		select {
		case u := <-updates:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{u}})
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("/bot"+token+"/answerCallbackQuery", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		answerMu.Lock()
		answerBodies = append(answerBodies, body)
		answerMu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	l := NewAckListeners(Options{TelegramAPIURL: srv.URL}, onAck, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer l.Close()
	l.Sync(context.Background(), []model.Channel{
		{ID: 1, Type: model.ChannelTelegram, Config: mustJSON(t, map[string]string{"bot_token": token, "chat_id": "555"})},
		{ID: 2, Type: model.ChannelTelegram, Config: mustJSON(t, map[string]string{"bot_token": token, "chat_id": "@ops"})},
	})

	updates <- map[string]any{
		"update_id": 1,
		"callback_query": map[string]any{
			"id":      "cbq-1",
			"from":    map[string]any{"username": "alice"},
			"message": map[string]any{"chat": map[string]any{"id": 555}},
			"data":    "ack:alert-1",
		},
	}
	waitUntil(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(acked) == 1
	})
	mu.Lock()
	got := acked[0]
	mu.Unlock()
	if got != "alert-1|telegram|alice" {
		t.Fatalf("acked = %q, want alert-1|telegram|alice", got)
	}
	waitUntil(t, 2*time.Second, func() bool {
		answerMu.Lock()
		defer answerMu.Unlock()
		return len(answerBodies) == 1
	})
	answerMu.Lock()
	body := answerBodies[0]
	answerMu.Unlock()
	if body["callback_query_id"] != "cbq-1" || body["text"] != "Acknowledged" {
		t.Fatalf("answerCallbackQuery body = %v", body)
	}

	// A callback from a chat outside the configured set is not acked, but
	// answered, so the button does not spin forever.
	updates <- map[string]any{
		"update_id": 2,
		"callback_query": map[string]any{
			"id":      "cbq-2",
			"from":    map[string]any{"username": "bob"},
			"message": map[string]any{"chat": map[string]any{"id": 999}},
			"data":    "ack:alert-2",
		},
	}
	waitUntil(t, 2*time.Second, func() bool {
		answerMu.Lock()
		defer answerMu.Unlock()
		return len(answerBodies) == 2
	})
	answerMu.Lock()
	body = answerBodies[1]
	answerMu.Unlock()
	if body["callback_query_id"] != "cbq-2" || body["text"] != errAckNotConfigured {
		t.Fatalf("answer for unconfigured chat = %v", body)
	}
	mu.Lock()
	n := len(acked)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("acked count after unrelated chat update = %d, want 1", n)
	}

	// chat_id configured as @username matches the chat's username.
	updates <- map[string]any{
		"update_id": 3,
		"callback_query": map[string]any{
			"id":      "cbq-3",
			"from":    map[string]any{"first_name": "Carol"},
			"message": map[string]any{"chat": map[string]any{"id": -100777, "username": "ops"}},
			"data":    "ack:alert-3",
		},
	}
	waitUntil(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(acked) == 2
	})
	mu.Lock()
	got = acked[1]
	mu.Unlock()
	if got != "alert-3|telegram|Carol" {
		t.Fatalf("acked = %q, want alert-3|telegram|Carol", got)
	}
}

func TestAckListenersSlackSocket(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var acked []string
	onAck := func(id string, src model.AckSource, name string) error {
		mu.Lock()
		defer mu.Unlock()
		acked = append(acked, id+"|"+string(src)+"|"+name)
		return nil
	}

	var responseMu sync.Mutex
	var responseBodies []map[string]any
	envelopeAcked := make(chan map[string]any, 1)

	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/apps.connections.open", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": srv.URL + "/link"})
	})
	mux.HandleFunc("/response", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		responseMu.Lock()
		responseBodies = append(responseBodies, body)
		responseMu.Unlock()
	})
	mux.HandleFunc("/link", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		frame := map[string]any{
			"envelope_id": "env-1",
			"type":        "interactive",
			"payload": map[string]any{
				"type":         "block_actions",
				"channel":      map[string]any{"id": "C123"},
				"user":         map[string]any{"username": "carol"},
				"actions":      []any{map[string]any{"action_id": slackAckActionID, "value": "alert-42"}},
				"response_url": srv.URL + "/response",
			},
		}
		b, _ := json.Marshal(frame)
		if err := c.Write(r.Context(), websocket.MessageText, b); err != nil {
			return
		}
		_, data, err := c.Read(r.Context())
		if err != nil {
			return
		}
		var ack map[string]any
		_ = json.Unmarshal(data, &ack)
		envelopeAcked <- ack
		<-r.Context().Done()
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	l := NewAckListeners(Options{SlackAPIURL: srv.URL}, onAck, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer l.Close()
	l.Sync(context.Background(), []model.Channel{{
		ID: 2, Type: model.ChannelSlackApp,
		Config: mustJSON(t, map[string]string{"bot_token": "xoxb-x", "app_token": "xapp-y", "channel": "C123"}),
	}})

	select {
	case ack := <-envelopeAcked:
		if ack["envelope_id"] != "env-1" {
			t.Fatalf("envelope ack = %v, want envelope_id=env-1", ack)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for envelope ack")
	}

	waitUntil(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(acked) == 1
	})
	mu.Lock()
	got := acked[0]
	mu.Unlock()
	if got != "alert-42|slack|carol" {
		t.Fatalf("acked = %q, want alert-42|slack|carol", got)
	}

	waitUntil(t, 2*time.Second, func() bool {
		responseMu.Lock()
		defer responseMu.Unlock()
		return len(responseBodies) == 1
	})
	responseMu.Lock()
	body := responseBodies[0]
	responseMu.Unlock()
	if body["text"] != "Acknowledged by carol" {
		t.Fatalf("response_url body = %v", body)
	}
	if body["replace_original"] != false {
		t.Fatalf("replace_original = %v, want false", body["replace_original"])
	}
}

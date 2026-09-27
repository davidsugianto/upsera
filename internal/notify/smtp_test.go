package notify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/testutil"
)

func TestSMTPSend(t *testing.T) {
	t.Parallel()
	addr, messages := testutil.FakeSMTP(t)
	host, port, ok := strings.Cut(addr, ":")
	if !ok {
		t.Fatalf("fake smtp addr %q has no port", addr)
	}

	n := &smtpNotifier{}
	cfg, _ := json.Marshal(map[string]any{
		"host": host, "port": atoi(t, port), "from": "alerts@upsera.dev",
		"to": []string{"a@example.com", "b@example.com"}, "tls": "none",
	})
	ev := Event{Kind: model.EventDown, MonitorName: "api", Message: "boom", At: time.Now()}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := n.Send(ctx, cfg, ev); err != nil {
		t.Fatalf("Send: %v", err)
	}

	msgs := messages()
	if len(msgs) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(msgs))
	}
	m := msgs[0]
	if m.From != "alerts@upsera.dev" {
		t.Errorf("From = %q, want alerts@upsera.dev", m.From)
	}
	if len(m.To) != 2 || m.To[0] != "a@example.com" || m.To[1] != "b@example.com" {
		t.Errorf("To = %v, want [a@example.com b@example.com]", m.To)
	}
	if !strings.Contains(m.Data, "Subject: [DOWN] api\r\n") {
		t.Errorf("Data missing Subject header: %q", m.Data)
	}
	if !strings.Contains(m.Data, "Reason: boom") {
		t.Errorf("Data missing body: %q", m.Data)
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			t.Fatalf("not a number: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n
}

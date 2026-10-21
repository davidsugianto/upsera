package api_test

import (
	"fmt"
	"net/http"
	"testing"
)

func (f *fakeAlerting) channelUpserts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.upsertChannels)
}

// TestChannelUpdateNoop proves that a PUT leaving a channel unchanged (the
// secret re-sent, redacted or omitted) writes nothing: no store write, no
// engine re-sync, no audit entry. A real change still writes.
func TestChannelUpdateNoop(t *testing.T) {
	base, _, _, alerts, _ := newTestServer(t)
	admin := newClient(t)
	resp, body := do(t, admin, http.MethodPost, base+"/api/setup", "", "", map[string]any{
		"email": "admin@example.com", "name": "Admin", "password": "correct horse battery", "team_name": "Ops",
	})
	requireStatus(t, resp, http.StatusCreated, body)
	var me meResponse
	decodeInto(t, body, &me)
	csrf, teamID := me.CSRFToken, me.Teams[0].ID
	teamURL := fmt.Sprintf("%s/api/teams/%d", base, teamID)

	const botToken = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef0123"
	resp, body = do(t, admin, http.MethodPost, teamURL+"/channels", csrf, "", map[string]any{
		"type": "telegram", "name": "oncall", "is_default": true,
		"config": map[string]any{"chat_id": "42", "bot_token": botToken},
	})
	requireStatus(t, resp, http.StatusCreated, body)
	chURL := fmt.Sprintf("%s/channels/%d", teamURL, int64(body["id"].(float64)))
	updatedAt := body["updated_at"]

	auditCount := func() int {
		resp, body := do(t, admin, http.MethodGet, teamURL+"/audit?limit=200", "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		return len(body["entries"].([]any))
	}
	put := func(cfg map[string]any, isDefault bool, name string) map[string]any {
		t.Helper()
		resp, body := do(t, admin, http.MethodPut, chURL, csrf, "", map[string]any{
			"type": "telegram", "name": name, "is_default": isDefault, "config": cfg,
		})
		requireStatus(t, resp, http.StatusOK, body)
		return body
	}

	audits, upserts := auditCount(), alerts.channelUpserts()
	for _, tc := range []struct {
		name string
		cfg  map[string]any
	}{
		{"secret re-sent", map[string]any{"chat_id": "42", "bot_token": botToken}},
		{"secret redacted", map[string]any{"chat_id": "42", "bot_token": "********"}},
		{"secret omitted", map[string]any{"chat_id": "42"}},
	} {
		got := put(tc.cfg, true, "  oncall ")
		if got["updated_at"] != updatedAt {
			t.Fatalf("%s: updated_at changed: %v -> %v", tc.name, updatedAt, got["updated_at"])
		}
		cfg := got["config"].(map[string]any)
		if got["name"] != "oncall" || cfg["bot_token"] != "********" || cfg["chat_id"] != "42" {
			t.Fatalf("%s: response = %v", tc.name, got)
		}
	}
	if n := auditCount(); n != audits {
		t.Fatalf("no-op PUTs wrote %d audit entries", n-audits)
	}
	if n := alerts.channelUpserts(); n != upserts {
		t.Fatalf("no-op PUTs called UpsertChannel %d times", n-upserts)
	}

	// Real changes: the secret, a plain config field, is_default, the name.
	changes := []struct {
		name      string
		cfg       map[string]any
		isDefault bool
		chName    string
	}{
		{"new secret", map[string]any{"chat_id": "42", "bot_token": "123456:ZYXWVUTSRQPONMLKJIHGFEDCBAzyxwvu9876"}, true, "oncall"},
		{"chat id", map[string]any{"chat_id": "43"}, true, "oncall"},
		{"is_default", map[string]any{"chat_id": "43"}, false, "oncall"},
		{"name", map[string]any{"chat_id": "43"}, false, "oncall-2"},
	}
	for i, c := range changes {
		got := put(c.cfg, c.isDefault, c.chName)
		if got["updated_at"] == updatedAt {
			t.Fatalf("%s: updated_at unchanged", c.name)
		}
		updatedAt = got["updated_at"]
		if n := auditCount(); n != audits+i+1 {
			t.Fatalf("%s: audit entries = %d, want %d", c.name, n, audits+i+1)
		}
		if n := alerts.channelUpserts(); n != upserts+i+1 {
			t.Fatalf("%s: UpsertChannel calls = %d, want %d", c.name, n, upserts+i+1)
		}
	}
}

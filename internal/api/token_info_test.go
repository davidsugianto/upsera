package api_test

import (
	"fmt"
	"net/http"
	"testing"
)

// TestTokenInfo covers GET /api/token: a team token learns its own team,
// scope and name; sessions and anonymous callers are refused.
func TestTokenInfo(t *testing.T) {
	base, _, _, _, _ := newTestServer(t)
	admin := newClient(t)
	resp, body := do(t, admin, http.MethodPost, base+"/api/setup", "", "", map[string]any{
		"email": "admin@example.com", "name": "Admin", "password": "correct horse battery", "team_name": "Ops",
	})
	requireStatus(t, resp, http.StatusCreated, body)
	var me meResponse
	decodeInto(t, body, &me)
	csrf, teamID := me.CSRFToken, me.Teams[0].ID

	// A second team, so the answer is provably the token's own team.
	resp, body = do(t, admin, http.MethodPost, base+"/api/teams", csrf, "", map[string]any{"name": "Platform"})
	requireStatus(t, resp, http.StatusCreated, body)
	otherID := int64(body["id"].(float64))

	token := func(team int64, name, scope string) string {
		resp, body := do(t, admin, http.MethodPost, fmt.Sprintf("%s/api/teams/%d/tokens", base, team), csrf, "", map[string]any{
			"name": name, "scope": scope,
		})
		requireStatus(t, resp, http.StatusCreated, body)
		return body["token"].(string)
	}

	t.Run("write token", func(t *testing.T) {
		resp, body := do(t, newClient(t), http.MethodGet, base+"/api/token", "", token(teamID, "ci", "write"), nil)
		requireStatus(t, resp, http.StatusOK, body)
		if body["team_id"] != float64(teamID) || body["team_name"] != "Ops" || body["scope"] != "write" || body["name"] != "ci" {
			t.Fatalf("token info = %v", body)
		}
	})

	t.Run("read token of another team", func(t *testing.T) {
		resp, body := do(t, newClient(t), http.MethodGet, base+"/api/token", "", token(otherID, "viewer", "read"), nil)
		requireStatus(t, resp, http.StatusOK, body)
		if body["team_id"] != float64(otherID) || body["team_name"] != "Platform" || body["scope"] != "read" || body["name"] != "viewer" {
			t.Fatalf("token info = %v", body)
		}
	})

	t.Run("session is refused", func(t *testing.T) {
		resp, body := do(t, admin, http.MethodGet, base+"/api/token", "", "", nil)
		requireStatus(t, resp, http.StatusForbidden, body)
		if body["detail"] != "use a team token" {
			t.Fatalf("detail = %v", body["detail"])
		}
	})

	t.Run("unauthenticated", func(t *testing.T) {
		resp, body := do(t, newClient(t), http.MethodGet, base+"/api/token", "", "", nil)
		requireStatus(t, resp, http.StatusUnauthorized, body)
		resp, body = do(t, newClient(t), http.MethodGet, base+"/api/token", "", "ups_bm90LWEtcmVhbC10b2tlbg", nil)
		requireStatus(t, resp, http.StatusUnauthorized, body)
	})
}

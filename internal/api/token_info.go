package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/davidsugianto/upsera/internal/model"
)

type tokenInfoOutput struct {
	Body struct {
		TeamID   int64            `json:"team_id"`
		TeamName string           `json:"team_name"`
		Scope    model.TokenScope `json:"scope"`
		Name     string           `json:"name"`
	}
}

// requireToken returns the caller's team API token, or an error: 403 if the
// caller authenticated with a session instead, 401 if unauthenticated.
func requireToken(ctx context.Context) (model.APIToken, error) {
	p := principalFrom(ctx)
	if p != nil && p.user != nil {
		return model.APIToken{}, huma.Error403Forbidden("use a team token")
	}
	if p == nil || p.token == nil {
		return model.APIToken{}, huma.Error401Unauthorized("authentication required")
	}
	return *p.token, nil
}

// teamName looks up the name of teamID. The store has no direct team
// lookup, so it goes through a member's team list (every team keeps at
// least one owner).
func teamName(ctx context.Context, d Deps, teamID int64) (string, error) {
	members, err := d.Store.ListMembers(ctx, teamID)
	if err != nil {
		return "", mapStoreErr(d, err)
	}
	for _, m := range members {
		teams, err := d.Store.ListTeamsForUser(ctx, m.UserID)
		if err != nil {
			return "", mapStoreErr(d, err)
		}
		for _, t := range teams {
			if t.Team.ID == teamID {
				return t.Team.Name, nil
			}
		}
	}
	return "", huma.Error404NotFound("team not found")
}

// registerTokenInfoRoutes wires GET /api/token, which tells a team token
// (upsctl, CI jobs) which team it belongs to and what it may do.
func registerTokenInfoRoutes(api huma.API, d Deps) {
	huma.Get(api, "/api/token", func(ctx context.Context, _ *noInput) (*tokenInfoOutput, error) {
		tok, err := requireToken(ctx)
		if err != nil {
			return nil, err
		}
		name, err := teamName(ctx, d, tok.TeamID)
		if err != nil {
			return nil, err
		}
		out := &tokenInfoOutput{}
		out.Body.TeamID = tok.TeamID
		out.Body.TeamName = name
		out.Body.Scope = tok.Scope
		out.Body.Name = tok.Name
		return out, nil
	})
}

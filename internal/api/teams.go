package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/store"
)

// teamPathInput is shared by every endpoint whose only path parameter is
// the team id.
type teamPathInput struct {
	TeamID int64 `path:"teamID"`
}

type teamBody struct {
	ID   int64      `json:"id"`
	Name string     `json:"name"`
	Role model.Role `json:"role"`
}

type listTeamsOutput struct {
	Body struct {
		Teams []teamBody `json:"teams"`
	}
}

type createTeamInput struct {
	Body struct {
		Name string `json:"name"`
	}
}

type createTeamOutput struct {
	Body teamBody
}

type memberBody struct {
	UserID int64      `json:"user_id"`
	Email  string     `json:"email"`
	Name   string     `json:"name"`
	Role   model.Role `json:"role"`
}

type listMembersOutput struct {
	Body struct {
		Members []memberBody `json:"members"`
	}
}

type addMemberInput struct {
	TeamID int64 `path:"teamID"`
	Body   struct {
		Email string     `json:"email"`
		Role  model.Role `json:"role"`
	}
}

type updateMemberInput struct {
	TeamID int64 `path:"teamID"`
	UserID int64 `path:"userID"`
	Body   struct {
		Role model.Role `json:"role"`
	}
}

type deleteMemberInput struct {
	TeamID int64 `path:"teamID"`
	UserID int64 `path:"userID"`
}

type memberOutput struct {
	Body memberBody
}

type tokenBody struct {
	ID         int64            `json:"id"`
	Name       string           `json:"name"`
	Scope      model.TokenScope `json:"scope"`
	CreatedAt  time.Time        `json:"created_at"`
	LastUsedAt *time.Time       `json:"last_used_at"`
	ExpiresAt  *time.Time       `json:"expires_at"`
}

func toTokenBody(t model.APIToken) tokenBody {
	return tokenBody{
		ID: t.ID, Name: t.Name, Scope: t.Scope,
		CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, ExpiresAt: t.ExpiresAt,
	}
}

type listTokensOutput struct {
	Body struct {
		Tokens []tokenBody `json:"tokens"`
	}
}

type createTokenInput struct {
	TeamID int64 `path:"teamID"`
	Body   struct {
		Name      string           `json:"name"`
		Scope     model.TokenScope `json:"scope"`
		ExpiresAt *time.Time       `json:"expires_at,omitempty"`
	}
}

type createTokenOutput struct {
	Body struct {
		ID        int64            `json:"id"`
		Name      string           `json:"name"`
		Scope     model.TokenScope `json:"scope"`
		Token     string           `json:"token"`
		CreatedAt time.Time        `json:"created_at"`
		ExpiresAt *time.Time       `json:"expires_at"`
	}
}

type deleteTokenInput struct {
	TeamID  int64 `path:"teamID"`
	TokenID int64 `path:"tokenID"`
}

func registerTeamRoutes(api huma.API, d Deps) {
	huma.Get(api, "/api/teams", func(ctx context.Context, _ *noInput) (*listTeamsOutput, error) {
		u, err := requireSessionUser(ctx)
		if err != nil {
			return nil, err
		}
		memberships, err := d.Store.ListTeamsForUser(ctx, u.ID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listTeamsOutput{}
		out.Body.Teams = make([]teamBody, len(memberships))
		for i, m := range memberships {
			out.Body.Teams[i] = teamBody{ID: m.Team.ID, Name: m.Team.Name, Role: m.Role}
		}
		return out, nil
	})

	huma.Post(api, "/api/teams", func(ctx context.Context, in *createTeamInput) (*createTeamOutput, error) {
		u, err := requireSessionUser(ctx)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(in.Body.Name)
		if name == "" {
			return nil, huma.Error422UnprocessableEntity("name is required")
		}
		t, err := d.Store.CreateTeam(ctx, name, u.ID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		writeAudit(ctx, d, &t.ID, actor{userID: &u.ID}, "team.create", "team", &t.ID, nil)
		return &createTeamOutput{Body: teamBody{ID: t.ID, Name: t.Name, Role: model.RoleOwner}}, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated })

	huma.Get(api, "/api/teams/{teamID}/members", func(ctx context.Context, in *teamPathInput) (*listMembersOutput, error) {
		if _, err := authorizeTeamSession(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		members, err := d.Store.ListMembers(ctx, in.TeamID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listMembersOutput{}
		out.Body.Members = make([]memberBody, len(members))
		for i, m := range members {
			out.Body.Members[i] = memberBody{UserID: m.UserID, Email: m.Email, Name: m.Name, Role: m.Role}
		}
		return out, nil
	})

	huma.Post(api, "/api/teams/{teamID}/members", func(ctx context.Context, in *addMemberInput) (*memberOutput, error) {
		act, err := authorizeTeamSession(ctx, d, in.TeamID, model.RoleOwner)
		if err != nil {
			return nil, err
		}
		if !in.Body.Role.Valid() {
			return nil, huma.Error422UnprocessableEntity("role must be owner, editor or viewer")
		}
		u, err := d.Store.GetUserByEmail(ctx, in.Body.Email)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, huma.Error404NotFound("no such user")
			}
			return nil, mapStoreErr(d, err)
		}
		if err := d.Store.SetMember(ctx, in.TeamID, u.ID, in.Body.Role); err != nil {
			return nil, mapStoreErr(d, err)
		}
		writeAudit(ctx, d, &in.TeamID, act, "member.add", "user", &u.ID, map[string]any{"role": in.Body.Role})
		return &memberOutput{Body: memberBody{UserID: u.ID, Email: u.Email, Name: u.Name, Role: in.Body.Role}}, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated })

	huma.Put(api, "/api/teams/{teamID}/members/{userID}", func(ctx context.Context, in *updateMemberInput) (*memberOutput, error) {
		act, err := authorizeTeamSession(ctx, d, in.TeamID, model.RoleOwner)
		if err != nil {
			return nil, err
		}
		if !in.Body.Role.Valid() {
			return nil, huma.Error422UnprocessableEntity("role must be owner, editor or viewer")
		}
		if err := d.Store.SetMember(ctx, in.TeamID, in.UserID, in.Body.Role); err != nil {
			return nil, mapStoreErr(d, err)
		}
		u, err := d.Store.GetUser(ctx, in.UserID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		writeAudit(ctx, d, &in.TeamID, act, "member.update", "user", &in.UserID, map[string]any{"role": in.Body.Role})
		return &memberOutput{Body: memberBody{UserID: u.ID, Email: u.Email, Name: u.Name, Role: in.Body.Role}}, nil
	})

	huma.Delete(api, "/api/teams/{teamID}/members/{userID}", func(ctx context.Context, in *deleteMemberInput) (*emptyOutput, error) {
		act, err := authorizeTeamSession(ctx, d, in.TeamID, model.RoleOwner)
		if err != nil {
			return nil, err
		}
		if err := d.Store.RemoveMember(ctx, in.TeamID, in.UserID); err != nil {
			return nil, mapStoreErr(d, err)
		}
		writeAudit(ctx, d, &in.TeamID, act, "member.remove", "user", &in.UserID, nil)
		return nil, nil
	})

	huma.Get(api, "/api/teams/{teamID}/tokens", func(ctx context.Context, in *teamPathInput) (*listTokensOutput, error) {
		if _, err := authorizeTeamSession(ctx, d, in.TeamID, model.RoleOwner); err != nil {
			return nil, err
		}
		toks, err := d.Store.ListAPITokens(ctx, in.TeamID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listTokensOutput{}
		out.Body.Tokens = make([]tokenBody, len(toks))
		for i, t := range toks {
			out.Body.Tokens[i] = toTokenBody(t)
		}
		return out, nil
	})

	huma.Post(api, "/api/teams/{teamID}/tokens", func(ctx context.Context, in *createTokenInput) (*createTokenOutput, error) {
		act, err := authorizeTeamSession(ctx, d, in.TeamID, model.RoleOwner)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(in.Body.Name)
		if name == "" {
			return nil, huma.Error422UnprocessableEntity("name is required")
		}
		if !in.Body.Scope.Valid() {
			return nil, huma.Error422UnprocessableEntity("scope must be read or write")
		}
		secret, value, err := newSecret()
		if err != nil {
			d.Logger.Error("generate token secret", "error", err)
			return nil, huma.Error500InternalServerError("internal error")
		}
		tok, err := d.Store.CreateAPIToken(ctx, in.TeamID, name, in.Body.Scope, hashSecret(secret), act.userID, in.Body.ExpiresAt)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		writeAudit(ctx, d, &in.TeamID, act, "token.create", "api_token", &tok.ID,
			map[string]any{"name": tok.Name, "scope": tok.Scope})
		out := &createTokenOutput{}
		out.Body.ID = tok.ID
		out.Body.Name = tok.Name
		out.Body.Scope = tok.Scope
		out.Body.Token = tokenPrefix + value
		out.Body.CreatedAt = tok.CreatedAt
		out.Body.ExpiresAt = tok.ExpiresAt
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated })

	huma.Delete(api, "/api/teams/{teamID}/tokens/{tokenID}", func(ctx context.Context, in *deleteTokenInput) (*emptyOutput, error) {
		act, err := authorizeTeamSession(ctx, d, in.TeamID, model.RoleOwner)
		if err != nil {
			return nil, err
		}
		if err := d.Store.DeleteAPIToken(ctx, in.TeamID, in.TokenID); err != nil {
			return nil, mapStoreErr(d, err)
		}
		writeAudit(ctx, d, &in.TeamID, act, "token.delete", "api_token", &in.TokenID, nil)
		return nil, nil
	})
}

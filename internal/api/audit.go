package api

import (
	"context"
	"encoding/json"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/davidsugianto/upsera/internal/model"
)

type listAuditInput struct {
	TeamID int64 `path:"teamID"`
	Before int64 `query:"before" minimum:"0"`
	Limit  int   `query:"limit" default:"50" minimum:"1" maximum:"200"`
}

type auditEntryBody struct {
	ID           int64           `json:"id"`
	TeamID       *int64          `json:"team_id"`
	ActorUserID  *int64          `json:"actor_user_id"`
	ActorTokenID *int64          `json:"actor_token_id"`
	Action       string          `json:"action"`
	TargetType   string          `json:"target_type"`
	TargetID     *int64          `json:"target_id"`
	Details      json.RawMessage `json:"details"`
	At           time.Time       `json:"at"`
}

type listAuditOutput struct {
	Body struct {
		Entries []auditEntryBody `json:"entries"`
	}
}

func registerAuditRoutes(api huma.API, d Deps) {
	huma.Get(api, "/api/teams/{teamID}/audit", func(ctx context.Context, in *listAuditInput) (*listAuditOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleOwner); err != nil {
			return nil, err
		}
		entries, err := d.Store.ListAudit(ctx, in.TeamID, in.Before, in.Limit)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listAuditOutput{}
		out.Body.Entries = make([]auditEntryBody, len(entries))
		for i, e := range entries {
			out.Body.Entries[i] = auditEntryBody{
				ID: e.ID, TeamID: e.TeamID, ActorUserID: e.ActorUserID, ActorTokenID: e.ActorTokenID,
				Action: e.Action, TargetType: e.TargetType, TargetID: e.TargetID, Details: e.Details, At: e.At,
			}
		}
		return out, nil
	})
}

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/store"
)

type policyPathInput struct {
	TeamID   int64 `path:"teamID"`
	PolicyID int64 `path:"policyID"`
}

type escalationStepBody struct {
	ChannelIDs []int64 `json:"channel_ids" minItems:"1" maxItems:"10"`
	DelayS     int     `json:"delay_s,omitempty" default:"600" minimum:"10" maximum:"86400"`
}

type policyRequestBody struct {
	Name  string               `json:"name" minLength:"1" maxLength:"100"`
	Steps []escalationStepBody `json:"steps" minItems:"1" maxItems:"10"`
}

type createPolicyInput struct {
	TeamID int64 `path:"teamID"`
	Body   policyRequestBody
}

type updatePolicyInput struct {
	TeamID   int64 `path:"teamID"`
	PolicyID int64 `path:"policyID"`
	Body     policyRequestBody
}

type policyBody struct {
	ID        int64                `json:"id"`
	TeamID    int64                `json:"team_id"`
	Name      string               `json:"name"`
	Steps     []escalationStepBody `json:"steps"`
	CreatedAt time.Time            `json:"created_at"`
	UpdatedAt time.Time            `json:"updated_at"`
}

func toPolicyBody(p model.EscalationPolicy) policyBody {
	steps := make([]escalationStepBody, len(p.Steps))
	for i, s := range p.Steps {
		steps[i] = escalationStepBody{ChannelIDs: s.ChannelIDs, DelayS: s.DelayS}
	}
	return policyBody{ID: p.ID, TeamID: p.TeamID, Name: p.Name, Steps: steps, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

type policyOutput struct {
	Body policyBody
}

type listPoliciesOutput struct {
	Body struct {
		Policies []policyBody `json:"escalation_policies"`
	}
}

// validatePolicyBody normalizes the name and checks that every step's
// channels belong to teamID.
func validatePolicyBody(ctx context.Context, d Deps, teamID int64, in policyRequestBody) (name string, steps []model.EscalationStep, err error) {
	name = strings.TrimSpace(in.Name)
	if name == "" {
		return "", nil, huma.Error422UnprocessableEntity("name is required")
	}
	steps = make([]model.EscalationStep, len(in.Steps))
	for i, s := range in.Steps {
		for _, id := range s.ChannelIDs {
			if _, err := d.Store.GetChannel(ctx, teamID, id); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return "", nil, huma.Error422UnprocessableEntity(
						fmt.Sprintf("steps[%d].channel_ids: channel %d not found", i, id))
				}
				return "", nil, mapStoreErr(d, err)
			}
		}
		delayS := s.DelayS
		if delayS == 0 {
			delayS = 600
		}
		steps[i] = model.EscalationStep{ChannelIDs: s.ChannelIDs, DelayS: delayS}
	}
	return name, steps, nil
}

func registerPolicyRoutes(api huma.API, d Deps) {
	huma.Get(api, "/api/teams/{teamID}/escalation-policies", func(ctx context.Context, in *teamPathInput) (*listPoliciesOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		ps, err := d.Store.ListPolicies(ctx, in.TeamID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listPoliciesOutput{}
		out.Body.Policies = make([]policyBody, len(ps))
		for i, p := range ps {
			out.Body.Policies[i] = toPolicyBody(p)
		}
		return out, nil
	})

	huma.Get(api, "/api/teams/{teamID}/escalation-policies/{policyID}", func(ctx context.Context, in *policyPathInput) (*policyOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		p, err := d.Store.GetPolicy(ctx, in.TeamID, in.PolicyID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		return &policyOutput{Body: toPolicyBody(p)}, nil
	})

	huma.Post(api, "/api/teams/{teamID}/escalation-policies", func(ctx context.Context, in *createPolicyInput) (*policyOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		name, steps, err := validatePolicyBody(ctx, d, in.TeamID, in.Body)
		if err != nil {
			return nil, err
		}
		created, err := d.Store.CreatePolicy(ctx, model.EscalationPolicy{TeamID: in.TeamID, Name: name, Steps: steps})
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		d.Alerting.UpsertPolicy(created)
		writeAudit(ctx, d, &in.TeamID, act, "policy.create", "escalation_policy", &created.ID,
			map[string]any{"name": created.Name})
		return &policyOutput{Body: toPolicyBody(created)}, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated })

	huma.Put(api, "/api/teams/{teamID}/escalation-policies/{policyID}", func(ctx context.Context, in *updatePolicyInput) (*policyOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		if _, err := d.Store.GetPolicy(ctx, in.TeamID, in.PolicyID); err != nil {
			return nil, mapStoreErr(d, err)
		}
		name, steps, err := validatePolicyBody(ctx, d, in.TeamID, in.Body)
		if err != nil {
			return nil, err
		}
		updated, err := d.Store.UpdatePolicy(ctx, model.EscalationPolicy{ID: in.PolicyID, TeamID: in.TeamID, Name: name, Steps: steps})
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		d.Alerting.UpsertPolicy(updated)
		writeAudit(ctx, d, &in.TeamID, act, "policy.update", "escalation_policy", &updated.ID,
			map[string]any{"name": updated.Name})
		return &policyOutput{Body: toPolicyBody(updated)}, nil
	})

	huma.Delete(api, "/api/teams/{teamID}/escalation-policies/{policyID}", func(ctx context.Context, in *policyPathInput) (*emptyOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		existing, err := d.Store.GetPolicy(ctx, in.TeamID, in.PolicyID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		if err := d.Store.DeletePolicy(ctx, in.TeamID, in.PolicyID); err != nil {
			if errors.Is(err, store.ErrInUse) {
				return nil, huma.Error409Conflict("policy is used by a monitor")
			}
			return nil, mapStoreErr(d, err)
		}
		d.Alerting.RemovePolicy(in.PolicyID)
		writeAudit(ctx, d, &in.TeamID, act, "policy.delete", "escalation_policy", &in.PolicyID,
			map[string]any{"name": existing.Name})
		return nil, nil
	})
}

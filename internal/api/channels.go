package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/notify"
	"github.com/davidsugianto/upsera/internal/store"
)

type channelPathInput struct {
	TeamID    int64 `path:"teamID"`
	ChannelID int64 `path:"channelID"`
}

// channelRequestBody is shared by create and update: create leaves Type
// free, update rejects a Type different from the existing channel.
type channelRequestBody struct {
	Type      model.ChannelType `json:"type"`
	Name      string            `json:"name" minLength:"1" maxLength:"100"`
	Config    json.RawMessage   `json:"config"`
	IsDefault bool              `json:"is_default,omitempty"`
}

type createChannelInput struct {
	TeamID int64 `path:"teamID"`
	Body   channelRequestBody
}

type updateChannelInput struct {
	TeamID    int64 `path:"teamID"`
	ChannelID int64 `path:"channelID"`
	Body      channelRequestBody
}

type channelBody struct {
	ID          int64             `json:"id"`
	TeamID      int64             `json:"team_id"`
	Type        model.ChannelType `json:"type"`
	Name        string            `json:"name"`
	Config      json.RawMessage   `json:"config"`
	IsDefault   bool              `json:"is_default"`
	ConfigError string            `json:"config_error,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

func toChannelBody(c model.Channel) channelBody {
	cfg := c.Config
	if c.DecryptErr == "" {
		cfg = redactChannelConfig(c.Type, cfg)
	}
	return channelBody{
		ID: c.ID, TeamID: c.TeamID, Type: c.Type, Name: c.Name, Config: cfg,
		IsDefault: c.IsDefault, ConfigError: c.DecryptErr, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

// redactChannelConfig replaces every non-empty secret field of cfg (per
// notify.SecretFields) with notify.Redacted, so API responses never leak
// bot tokens, webhook URLs or passwords.
func redactChannelConfig(t model.ChannelType, cfg json.RawMessage) json.RawMessage {
	if len(cfg) == 0 {
		return cfg
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(cfg, &m); err != nil {
		return cfg
	}
	redacted, err := json.Marshal(notify.Redacted)
	if err != nil {
		return cfg
	}
	changed := false
	for _, f := range notify.SecretFields(t) {
		v, ok := m[f]
		if !ok || isEmptyJSONValue(v) {
			continue
		}
		m[f] = redacted
		changed = true
	}
	if !changed {
		return cfg
	}
	out, err := json.Marshal(m)
	if err != nil {
		return cfg
	}
	return out
}

// isEmptyJSONValue reports whether raw decodes to null, "", {} or [].
func isEmptyJSONValue(raw json.RawMessage) bool {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case map[string]any:
		return len(x) == 0
	case []any:
		return len(x) == 0
	}
	return false
}

// mergeChannelConfig fills in secret fields left redacted or omitted from
// requested with their value from stored, so a PUT that doesn't touch a
// secret field doesn't need to re-send it. It fails with a 422 if a secret
// field needs to come from stored but stored isn't decryptable.
func mergeChannelConfig(t model.ChannelType, stored model.Channel, requested json.RawMessage) (json.RawMessage, error) {
	var reqMap map[string]json.RawMessage
	if len(requested) > 0 {
		if err := json.Unmarshal(requested, &reqMap); err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid config")
		}
	}
	if reqMap == nil {
		reqMap = map[string]json.RawMessage{}
	}
	secretFields := notify.SecretFields(t)
	needsStored := false
	for _, f := range secretFields {
		if isRedactedOrMissing(reqMap, f) {
			needsStored = true
			break
		}
	}
	if !needsStored {
		return json.Marshal(reqMap)
	}
	if stored.DecryptErr != "" {
		return nil, huma.Error422UnprocessableEntity(
			"cannot keep existing secret fields: config was not decryptable, please re-enter them")
	}
	var storedMap map[string]json.RawMessage
	if err := json.Unmarshal(stored.Config, &storedMap); err != nil {
		return nil, huma.Error422UnprocessableEntity(
			"cannot keep existing secret fields: config was not decryptable, please re-enter them")
	}
	for _, f := range secretFields {
		if !isRedactedOrMissing(reqMap, f) {
			continue
		}
		if v, ok := storedMap[f]; ok {
			reqMap[f] = v
		}
	}
	return json.Marshal(reqMap)
}

func isRedactedOrMissing(m map[string]json.RawMessage, field string) bool {
	v, ok := m[field]
	if !ok {
		return true
	}
	var s string
	if err := json.Unmarshal(v, &s); err == nil && s == notify.Redacted {
		return true
	}
	return false
}

type channelOutput struct {
	Body channelBody
}

type listChannelsOutput struct {
	Body struct {
		Channels []channelBody `json:"channels"`
	}
}

type testSendChannelOutput struct {
	Body struct {
		OK bool `json:"ok"`
	}
}

func registerChannelRoutes(api huma.API, d Deps) {
	huma.Get(api, "/api/teams/{teamID}/channels", func(ctx context.Context, in *teamPathInput) (*listChannelsOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		chs, err := d.Store.ListChannels(ctx, in.TeamID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listChannelsOutput{}
		out.Body.Channels = make([]channelBody, len(chs))
		for i, c := range chs {
			out.Body.Channels[i] = toChannelBody(c)
		}
		return out, nil
	})

	huma.Get(api, "/api/teams/{teamID}/channels/{channelID}", func(ctx context.Context, in *channelPathInput) (*channelOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		c, err := d.Store.GetChannel(ctx, in.TeamID, in.ChannelID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		return &channelOutput{Body: toChannelBody(c)}, nil
	})

	huma.Post(api, "/api/teams/{teamID}/channels", func(ctx context.Context, in *createChannelInput) (*channelOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		if !in.Body.Type.Valid() {
			return nil, huma.Error422UnprocessableEntity("invalid channel type")
		}
		name := strings.TrimSpace(in.Body.Name)
		if name == "" {
			return nil, huma.Error422UnprocessableEntity("name is required")
		}
		if err := notify.Validate(in.Body.Type, in.Body.Config); err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		c := model.Channel{
			TeamID: in.TeamID, Type: in.Body.Type, Name: name, Config: in.Body.Config, IsDefault: in.Body.IsDefault,
		}
		created, err := d.Store.CreateChannel(ctx, c)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		d.Alerting.UpsertChannel(created)
		writeAudit(ctx, d, &in.TeamID, act, "channel.create", "channel", &created.ID,
			map[string]any{"name": created.Name, "type": created.Type})
		return &channelOutput{Body: toChannelBody(created)}, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated })

	huma.Put(api, "/api/teams/{teamID}/channels/{channelID}", func(ctx context.Context, in *updateChannelInput) (*channelOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		existing, err := d.Store.GetChannel(ctx, in.TeamID, in.ChannelID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		if !in.Body.Type.Valid() {
			return nil, huma.Error422UnprocessableEntity("invalid channel type")
		}
		if in.Body.Type != existing.Type {
			return nil, huma.Error422UnprocessableEntity("channel type cannot be changed")
		}
		name := strings.TrimSpace(in.Body.Name)
		if name == "" {
			return nil, huma.Error422UnprocessableEntity("name is required")
		}
		cfg, err := mergeChannelConfig(existing.Type, existing, in.Body.Config)
		if err != nil {
			return nil, err
		}
		if err := notify.Validate(existing.Type, cfg); err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		c := model.Channel{
			ID: in.ChannelID, TeamID: in.TeamID, Type: existing.Type, Name: name, Config: cfg, IsDefault: in.Body.IsDefault,
		}
		updated, err := d.Store.UpdateChannel(ctx, c)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		d.Alerting.UpsertChannel(updated)
		writeAudit(ctx, d, &in.TeamID, act, "channel.update", "channel", &updated.ID,
			map[string]any{"name": updated.Name, "type": updated.Type})
		return &channelOutput{Body: toChannelBody(updated)}, nil
	})

	huma.Delete(api, "/api/teams/{teamID}/channels/{channelID}", func(ctx context.Context, in *channelPathInput) (*emptyOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		existing, err := d.Store.GetChannel(ctx, in.TeamID, in.ChannelID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		if err := d.Store.DeleteChannel(ctx, in.TeamID, in.ChannelID); err != nil {
			if errors.Is(err, store.ErrInUse) {
				return nil, huma.Error409Conflict("channel is used by an escalation policy")
			}
			return nil, mapStoreErr(d, err)
		}
		d.Alerting.RemoveChannel(in.ChannelID)
		writeAudit(ctx, d, &in.TeamID, act, "channel.delete", "channel", &in.ChannelID,
			map[string]any{"name": existing.Name, "type": existing.Type})
		return nil, nil
	})

	huma.Post(api, "/api/teams/{teamID}/channels/{channelID}/test", func(ctx context.Context, in *channelPathInput) (*testSendChannelOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		ch, err := d.Store.GetChannel(ctx, in.TeamID, in.ChannelID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		writeAudit(ctx, d, &in.TeamID, act, "channel.test", "channel", &ch.ID,
			map[string]any{"name": ch.Name, "type": ch.Type})
		if err := d.Alerting.TestSend(ctx, ch); err != nil {
			return nil, huma.NewError(http.StatusBadGateway, "send failed: "+err.Error())
		}
		out := &testSendChannelOutput{}
		out.Body.OK = true
		return out, nil
	})
}

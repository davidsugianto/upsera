package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/davidsugianto/upsera/internal/checker"
	"github.com/davidsugianto/upsera/internal/events"
	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/scheduler"
	"github.com/davidsugianto/upsera/internal/store"
)

type monitorPathInput struct {
	TeamID    int64 `path:"teamID"`
	MonitorID int64 `path:"monitorID"`
}

// monitorRequestBody is shared by create and update: create leaves Type
// free, update rejects a Type different from the existing monitor.
type monitorRequestBody struct {
	Name               string            `json:"name" minLength:"1" maxLength:"100"`
	Type               model.MonitorType `json:"type"`
	Config             json.RawMessage   `json:"config"`
	IntervalS          int               `json:"interval_s,omitempty" default:"60" minimum:"20" maximum:"86400"`
	RetryIntervalS     int               `json:"retry_interval_s,omitempty" default:"20" minimum:"20" maximum:"86400"`
	Retries            *int              `json:"retries,omitempty" default:"1" minimum:"0" maximum:"10"`
	TimeoutS           int               `json:"timeout_s,omitempty" default:"10" minimum:"1"`
	Paused             bool              `json:"paused,omitempty"`
	GroupName          string            `json:"group_name,omitempty"`
	Tags               []string          `json:"tags,omitempty" maxItems:"20" minLength:"1" maxLength:"50"`
	ParentID           *int64            `json:"parent_id,omitempty"`
	EscalationPolicyID *int64            `json:"escalation_policy_id,omitempty"`
	ChannelIDs         []int64           `json:"channel_ids,omitempty" maxItems:"20"`
}

type createMonitorInput struct {
	TeamID int64 `path:"teamID"`
	Body   monitorRequestBody
}

type updateMonitorInput struct {
	TeamID    int64 `path:"teamID"`
	MonitorID int64 `path:"monitorID"`
	Body      monitorRequestBody
}

type monitorStateBody struct {
	Status              model.Status `json:"status"`
	Since               time.Time    `json:"since"`
	LastCheckAt         time.Time    `json:"last_check_at"`
	ConsecutiveFailures int          `json:"consecutive_failures"`
	TLSExpiresAt        *time.Time   `json:"tls_expires_at"`
	FlapCount           int          `json:"flap_count"`
}

type monitorBody struct {
	ID                 int64             `json:"id"`
	TeamID             int64             `json:"team_id"`
	Name               string            `json:"name"`
	Type               model.MonitorType `json:"type"`
	Config             json.RawMessage   `json:"config"`
	IntervalS          int               `json:"interval_s"`
	RetryIntervalS     int               `json:"retry_interval_s"`
	Retries            int               `json:"retries"`
	TimeoutS           int               `json:"timeout_s"`
	Paused             bool              `json:"paused"`
	GroupName          string            `json:"group_name"`
	Tags               []string          `json:"tags"`
	ParentID           *int64            `json:"parent_id"`
	EscalationPolicyID *int64            `json:"escalation_policy_id"`
	ChannelIDs         []int64           `json:"channel_ids"`
	PushURL            string            `json:"push_url,omitempty"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
	State              *monitorStateBody `json:"state"`
}

func toMonitorBody(d Deps, m model.Monitor) monitorBody {
	channelIDs := m.ChannelIDs
	if channelIDs == nil {
		channelIDs = []int64{}
	}
	b := monitorBody{
		ID: m.ID, TeamID: m.TeamID, Name: m.Name, Type: m.Type, Config: m.Config,
		IntervalS: m.IntervalS, RetryIntervalS: m.RetryIntervalS, Retries: m.Retries,
		TimeoutS: m.TimeoutS, Paused: m.Paused, GroupName: m.GroupName, Tags: m.Tags,
		ParentID: m.ParentID, EscalationPolicyID: m.EscalationPolicyID, ChannelIDs: channelIDs,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
	if m.Type == model.TypePush {
		b.PushURL = d.Config.BaseURL + "/api/push/" + m.PushToken
	}
	if st, ok := d.Runner.State(m.ID); ok {
		b.State = &monitorStateBody{
			Status: st.Status, Since: st.Since, LastCheckAt: st.LastCheckAt,
			ConsecutiveFailures: st.ConsecutiveFailures, TLSExpiresAt: st.TLSExpiresAt,
			FlapCount: st.FlapCount,
		}
	}
	return b
}

type monitorOutput struct {
	Body monitorBody
}

type listMonitorsOutput struct {
	Body struct {
		Monitors []monitorBody `json:"monitors"`
	}
}

type listHeartbeatsInput struct {
	TeamID    int64     `path:"teamID"`
	MonitorID int64     `path:"monitorID"`
	Limit     int       `query:"limit" default:"100" minimum:"1" maximum:"5000"`
	Since     time.Time `query:"since"`
}

type heartbeatBody struct {
	ProbeID   int64        `json:"probe_id"`
	Time      time.Time    `json:"time"`
	Status    model.Status `json:"status"`
	LatencyMs int32        `json:"latency_ms"`
	Message   string       `json:"message"`
}

type listHeartbeatsOutput struct {
	Body struct {
		Heartbeats []heartbeatBody `json:"heartbeats"`
	}
}

type uptimeInput struct {
	TeamID    int64 `path:"teamID"`
	MonitorID int64 `path:"monitorID"`
	Days      int   `query:"days" default:"30" minimum:"1" maximum:"90"`
}

type uptimeDayBody struct {
	Day          time.Time `json:"day"`
	Checks       int       `json:"checks"`
	Up           int       `json:"up"`
	AvgLatencyMs int       `json:"avg_latency_ms"`
	P95LatencyMs int       `json:"p95_latency_ms"`
}

type uptimeOutput struct {
	Body struct {
		Days       []uptimeDayBody `json:"days"`
		Checks     int             `json:"checks"`
		Up         int             `json:"up"`
		Percentage *float64        `json:"percentage"`
	}
}

type pushInput struct {
	Token  string `path:"token"`
	Status string `query:"status" default:"up" enum:"up,down"`
	Msg    string `query:"msg"`
	Ping   int32  `query:"ping" minimum:"0"`
}

type pushOutput struct {
	Body struct {
		OK bool `json:"ok"`
	}
}

// validateMonitorBody normalizes and checks the cross-field rules that JSON
// schema tags cannot express, and validates/normalizes the type-specific
// config via the checker package.
func validateMonitorBody(in monitorRequestBody) (name string, cfg json.RawMessage, retries int, err error) {
	if !in.Type.Valid() {
		return "", nil, 0, huma.Error422UnprocessableEntity("invalid monitor type")
	}
	name = strings.TrimSpace(in.Name)
	if name == "" {
		return "", nil, 0, huma.Error422UnprocessableEntity("name is required")
	}
	if in.TimeoutS < 1 || in.TimeoutS >= in.IntervalS {
		return "", nil, 0, huma.Error422UnprocessableEntity("timeout_s must be >= 1 and less than interval_s")
	}
	cfg, err = checker.ValidateConfig(in.Type, in.Config)
	if err != nil {
		return "", nil, 0, huma.Error422UnprocessableEntity(err.Error())
	}
	retries = model.DefaultRetries
	if in.Retries != nil {
		retries = *in.Retries
	}
	return name, cfg, retries, nil
}

// validateMonitorDeps checks parent_id, escalation_policy_id and
// channel_ids against teamID. selfID is the monitor being validated (0 for
// create, where a monitor cannot yet be its own ancestor).
func validateMonitorDeps(ctx context.Context, d Deps, teamID, selfID int64, parentID, policyID *int64, channelIDs []int64) error {
	if parentID != nil {
		id := *parentID
		reachedRoot := false
		for hops := range 10 {
			if id == selfID {
				return huma.Error422UnprocessableEntity("parent_id would create a cycle")
			}
			m, err := d.Store.GetMonitor(ctx, teamID, id)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					if hops == 0 {
						return huma.Error422UnprocessableEntity("parent_id: monitor not found")
					}
					reachedRoot = true
					break
				}
				return mapStoreErr(d, err)
			}
			if m.ParentID == nil {
				reachedRoot = true
				break
			}
			id = *m.ParentID
		}
		if !reachedRoot {
			return huma.Error422UnprocessableEntity("dependency chain too deep")
		}
	}
	if policyID != nil {
		if _, err := d.Store.GetPolicy(ctx, teamID, *policyID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return huma.Error422UnprocessableEntity("escalation_policy_id: policy not found")
			}
			return mapStoreErr(d, err)
		}
	}
	for _, id := range channelIDs {
		if _, err := d.Store.GetChannel(ctx, teamID, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return huma.Error422UnprocessableEntity(fmt.Sprintf("channel_ids: channel %d not found", id))
			}
			return mapStoreErr(d, err)
		}
	}
	return nil
}

func registerMonitorRoutes(api huma.API, d Deps) {
	huma.Get(api, "/api/teams/{teamID}/monitors", func(ctx context.Context, in *teamPathInput) (*listMonitorsOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		ms, err := d.Store.ListMonitors(ctx, in.TeamID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listMonitorsOutput{}
		out.Body.Monitors = make([]monitorBody, len(ms))
		for i, m := range ms {
			out.Body.Monitors[i] = toMonitorBody(d, m)
		}
		return out, nil
	})

	huma.Post(api, "/api/teams/{teamID}/monitors", func(ctx context.Context, in *createMonitorInput) (*monitorOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		name, cfg, retries, err := validateMonitorBody(in.Body)
		if err != nil {
			return nil, err
		}
		if err := validateMonitorDeps(ctx, d, in.TeamID, 0, in.Body.ParentID, in.Body.EscalationPolicyID, in.Body.ChannelIDs); err != nil {
			return nil, err
		}
		m := model.Monitor{
			TeamID: in.TeamID, Name: name, Type: in.Body.Type, Config: cfg,
			IntervalS: in.Body.IntervalS, RetryIntervalS: in.Body.RetryIntervalS, Retries: retries,
			TimeoutS: in.Body.TimeoutS, Paused: in.Body.Paused, GroupName: in.Body.GroupName, Tags: in.Body.Tags,
			ParentID: in.Body.ParentID, EscalationPolicyID: in.Body.EscalationPolicyID, ChannelIDs: in.Body.ChannelIDs,
		}
		if in.Body.Type == model.TypePush {
			token, err := newPushToken()
			if err != nil {
				d.Logger.Error("generate push token", "error", err)
				return nil, huma.Error500InternalServerError("internal error")
			}
			m.PushToken = token
		}
		created, err := d.Store.CreateMonitor(ctx, m)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		d.Runner.Upsert(created)
		d.Alerting.UpsertMonitor(created)
		d.Events.Publish(in.TeamID, events.MonitorsChanged{MonitorID: created.ID})
		writeAudit(ctx, d, &in.TeamID, act, "monitor.create", "monitor", &created.ID,
			map[string]any{"name": created.Name, "type": created.Type})
		return &monitorOutput{Body: toMonitorBody(d, created)}, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated })

	huma.Get(api, "/api/teams/{teamID}/monitors/{monitorID}", func(ctx context.Context, in *monitorPathInput) (*monitorOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		m, err := d.Store.GetMonitor(ctx, in.TeamID, in.MonitorID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		return &monitorOutput{Body: toMonitorBody(d, m)}, nil
	})

	huma.Put(api, "/api/teams/{teamID}/monitors/{monitorID}", func(ctx context.Context, in *updateMonitorInput) (*monitorOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		existing, err := d.Store.GetMonitor(ctx, in.TeamID, in.MonitorID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		if !in.Body.Type.Valid() {
			return nil, huma.Error422UnprocessableEntity("invalid monitor type")
		}
		if in.Body.Type != existing.Type {
			return nil, huma.Error422UnprocessableEntity("monitor type cannot be changed")
		}
		name, cfg, retries, err := validateMonitorBody(in.Body)
		if err != nil {
			return nil, err
		}
		if err := validateMonitorDeps(ctx, d, in.TeamID, in.MonitorID, in.Body.ParentID, in.Body.EscalationPolicyID, in.Body.ChannelIDs); err != nil {
			return nil, err
		}
		m := model.Monitor{
			ID: in.MonitorID, TeamID: in.TeamID, Name: name, Type: existing.Type, Config: cfg,
			IntervalS: in.Body.IntervalS, RetryIntervalS: in.Body.RetryIntervalS, Retries: retries,
			TimeoutS: in.Body.TimeoutS, Paused: in.Body.Paused, GroupName: in.Body.GroupName, Tags: in.Body.Tags,
			ParentID: in.Body.ParentID, EscalationPolicyID: in.Body.EscalationPolicyID, ChannelIDs: in.Body.ChannelIDs,
		}
		updated, err := d.Store.UpdateMonitor(ctx, m)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		d.Runner.Upsert(updated)
		d.Alerting.UpsertMonitor(updated)
		d.Events.Publish(in.TeamID, events.MonitorsChanged{MonitorID: updated.ID})
		writeAudit(ctx, d, &in.TeamID, act, "monitor.update", "monitor", &updated.ID, map[string]any{"name": updated.Name})
		return &monitorOutput{Body: toMonitorBody(d, updated)}, nil
	})

	huma.Delete(api, "/api/teams/{teamID}/monitors/{monitorID}", func(ctx context.Context, in *monitorPathInput) (*emptyOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		if err := d.Store.DeleteMonitor(ctx, in.TeamID, in.MonitorID); err != nil {
			return nil, mapStoreErr(d, err)
		}
		d.Runner.Remove(in.MonitorID)
		d.Alerting.RemoveMonitor(in.MonitorID)
		d.Events.Publish(in.TeamID, events.MonitorsChanged{MonitorID: in.MonitorID, Deleted: true})
		writeAudit(ctx, d, &in.TeamID, act, "monitor.delete", "monitor", &in.MonitorID, nil)
		return nil, nil
	})

	huma.Get(api, "/api/teams/{teamID}/monitors/{monitorID}/heartbeats", func(ctx context.Context, in *listHeartbeatsInput) (*listHeartbeatsOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		if _, err := d.Store.GetMonitor(ctx, in.TeamID, in.MonitorID); err != nil {
			return nil, mapStoreErr(d, err)
		}
		hbs, err := d.Store.ListHeartbeats(ctx, in.MonitorID, in.Since, in.Limit)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listHeartbeatsOutput{}
		out.Body.Heartbeats = make([]heartbeatBody, len(hbs))
		for i, h := range hbs {
			out.Body.Heartbeats[i] = heartbeatBody{
				ProbeID: h.ProbeID, Time: h.Time, Status: h.Status, LatencyMs: h.LatencyMs, Message: h.Message,
			}
		}
		return out, nil
	})

	huma.Get(api, "/api/teams/{teamID}/monitors/{monitorID}/uptime", func(ctx context.Context, in *uptimeInput) (*uptimeOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		if _, err := d.Store.GetMonitor(ctx, in.TeamID, in.MonitorID); err != nil {
			return nil, mapStoreErr(d, err)
		}
		to := time.Now().UTC()
		from := to.AddDate(0, 0, -(in.Days - 1))
		rows, err := d.Store.ListUptimeDaily(ctx, in.MonitorID, from, to)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &uptimeOutput{}
		out.Body.Days = make([]uptimeDayBody, len(rows))
		var totalChecks, totalUp int
		for i, r := range rows {
			out.Body.Days[i] = uptimeDayBody{
				Day: r.Day, Checks: r.Checks, Up: r.Up, AvgLatencyMs: r.AvgLatencyMs, P95LatencyMs: r.P95LatencyMs,
			}
			totalChecks += r.Checks
			totalUp += r.Up
		}
		out.Body.Checks = totalChecks
		out.Body.Up = totalUp
		if totalChecks > 0 {
			pct := float64(totalUp) / float64(totalChecks) * 100
			out.Body.Percentage = &pct
		}
		return out, nil
	})
}

// registerPushRoutes wires the unauthenticated push endpoint. It only ever
// talks to the Runner, so it keeps working while the database is down.
func registerPushRoutes(api huma.API, d Deps) {
	handler := func(ctx context.Context, in *pushInput) (*pushOutput, error) {
		var status model.Status
		switch in.Status {
		case "up":
			status = model.StatusUp
		case "down":
			status = model.StatusDown
		default:
			return nil, huma.Error422UnprocessableEntity("status must be up or down")
		}
		msg := model.TruncateMessage(in.Msg)
		err := d.Runner.Push(in.Token, status, msg, in.Ping)
		switch {
		case err == nil:
			out := &pushOutput{}
			out.Body.OK = true
			return out, nil
		case errors.Is(err, scheduler.ErrUnknownPushToken):
			return nil, huma.Error404NotFound("unknown push token")
		case errors.Is(err, scheduler.ErrPaused):
			return nil, huma.Error409Conflict("monitor is paused")
		default:
			d.Logger.Error("push failed", "error", err)
			return nil, huma.Error500InternalServerError("internal error")
		}
	}
	huma.Get(api, "/api/push/{token}", handler)
	huma.Post(api, "/api/push/{token}", handler)
}

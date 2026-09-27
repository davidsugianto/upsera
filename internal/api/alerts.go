package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/davidsugianto/upsera/internal/alerting"
	"github.com/davidsugianto/upsera/internal/model"
)

type listAlertsInput struct {
	TeamID    int64     `path:"teamID"`
	State     string    `query:"state" default:"open" enum:"open,resolved,all"`
	MonitorID int64     `query:"monitor_id" minimum:"0"`
	Before    time.Time `query:"before"`
	Limit     int       `query:"limit" default:"50" minimum:"1" maximum:"200"`
}

type alertBody struct {
	ID                 string          `json:"id"`
	TeamID             int64           `json:"team_id"`
	MonitorID          int64           `json:"monitor_id"`
	IncidentStart      time.Time       `json:"incident_start"`
	OpenedAt           time.Time       `json:"opened_at"`
	Message            string          `json:"message"`
	Step               int             `json:"step"`
	NextEscalationAt   *time.Time      `json:"next_escalation_at"`
	NotifiedChannelIDs []int64         `json:"notified_channel_ids"`
	Suppressed         bool            `json:"suppressed"`
	Flapping           bool            `json:"flapping"`
	AckedAt            *time.Time      `json:"acked_at"`
	AckedByUserID      *int64          `json:"acked_by_user_id"`
	AckSource          model.AckSource `json:"ack_source"`
	AckedByName        string          `json:"acked_by_name"`
	ResolvedAt         *time.Time      `json:"resolved_at"`
	Resolution         string          `json:"resolution"`
	UpdatedAt          time.Time       `json:"updated_at"`
	Acked              bool            `json:"acked"`
}

func toAlertBody(a model.Alert) alertBody {
	channelIDs := a.NotifiedChannelIDs
	if channelIDs == nil {
		channelIDs = []int64{}
	}
	return alertBody{
		ID: a.ID, TeamID: a.TeamID, MonitorID: a.MonitorID, IncidentStart: a.IncidentStart, OpenedAt: a.OpenedAt,
		Message: a.Message, Step: a.Step, NextEscalationAt: a.NextEscalationAt, NotifiedChannelIDs: channelIDs,
		Suppressed: a.Suppressed, Flapping: a.Flapping, AckedAt: a.AckedAt, AckedByUserID: a.AckedByUserID,
		AckSource: a.AckSource, AckedByName: a.AckedByName, ResolvedAt: a.ResolvedAt, Resolution: a.Resolution,
		UpdatedAt: a.UpdatedAt, Acked: a.AckedAt != nil,
	}
}

type listAlertsOutput struct {
	Body struct {
		Alerts []alertBody `json:"alerts"`
	}
}

type alertPathInput struct {
	TeamID  int64  `path:"teamID"`
	AlertID string `path:"alertID" format:"uuid"`
}

type alertOutput struct {
	Body alertBody
}

type listNotificationLogInput struct {
	TeamID   int64 `path:"teamID"`
	BeforeID int64 `query:"before_id" minimum:"0"`
	Limit    int   `query:"limit" default:"50" minimum:"1" maximum:"200"`
}

type notificationLogEntryBody struct {
	ID        int64            `json:"id"`
	TeamID    int64            `json:"team_id"`
	ChannelID *int64           `json:"channel_id"`
	MonitorID *int64           `json:"monitor_id"`
	AlertID   *string          `json:"alert_id"`
	Event     model.AlertEvent `json:"event"`
	DedupeKey string           `json:"dedupe_key"`
	Attempt   int              `json:"attempt"`
	OK        bool             `json:"ok"`
	Error     string           `json:"error"`
	At        time.Time        `json:"at"`
}

type listNotificationLogOutput struct {
	Body struct {
		Entries []notificationLogEntryBody `json:"entries"`
	}
}

func registerAlertRoutes(api huma.API, d Deps) {
	huma.Get(api, "/api/teams/{teamID}/alerts", func(ctx context.Context, in *listAlertsInput) (*listAlertsOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		switch in.State {
		case "open", "resolved", "all":
		default:
			return nil, huma.Error422UnprocessableEntity("state must be open, resolved or all")
		}
		alerts, err := d.Store.ListAlerts(ctx, in.TeamID, in.State, in.MonitorID, in.Before, in.Limit)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listAlertsOutput{}
		out.Body.Alerts = make([]alertBody, len(alerts))
		for i, a := range alerts {
			out.Body.Alerts[i] = toAlertBody(a)
		}
		return out, nil
	})

	huma.Post(api, "/api/teams/{teamID}/alerts/{alertID}/acknowledge", func(ctx context.Context, in *alertPathInput) (*alertOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		by := model.AckBy{Source: model.AckWeb}
		if act.userID != nil {
			u, err := requireSessionUser(ctx)
			if err != nil {
				return nil, err
			}
			by.UserID = act.userID
			by.Name = u.Name
		} else {
			by.Name = fmt.Sprintf("token:%d", *act.tokenID)
		}
		a, err := d.Alerting.Acknowledge(in.TeamID, in.AlertID, by)
		if err != nil {
			if errors.Is(err, alerting.ErrAlertResolved) {
				return nil, huma.Error409Conflict("alert is resolved")
			}
			if errors.Is(err, alerting.ErrAlertNotFound) {
				existing, gerr := d.Store.GetAlert(ctx, in.TeamID, in.AlertID)
				if gerr == nil && existing.ResolvedAt != nil {
					return nil, huma.Error409Conflict("alert is resolved")
				}
				return nil, huma.Error404NotFound("alert not found")
			}
			return nil, mapStoreErr(d, err)
		}
		writeAudit(ctx, d, &in.TeamID, act, "alert.acknowledge", "alert", nil, map[string]any{"alert_id": in.AlertID})
		return &alertOutput{Body: toAlertBody(a)}, nil
	})

	huma.Get(api, "/api/teams/{teamID}/notification-log", func(ctx context.Context, in *listNotificationLogInput) (*listNotificationLogOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		entries, err := d.Store.ListNotificationLog(ctx, in.TeamID, in.BeforeID, in.Limit)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listNotificationLogOutput{}
		out.Body.Entries = make([]notificationLogEntryBody, len(entries))
		for i, e := range entries {
			out.Body.Entries[i] = notificationLogEntryBody{
				ID: e.ID, TeamID: e.TeamID, ChannelID: e.ChannelID, MonitorID: e.MonitorID, AlertID: e.AlertID,
				Event: e.Event, DedupeKey: e.DedupeKey, Attempt: e.Attempt, OK: e.OK, Error: e.Error, At: e.At,
			}
		}
		return out, nil
	})
}

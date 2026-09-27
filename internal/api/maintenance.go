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

type maintenanceWindowPathInput struct {
	TeamID   int64 `path:"teamID"`
	WindowID int64 `path:"windowID"`
}

type maintenanceWindowRequestBody struct {
	Name       string           `json:"name" minLength:"1" maxLength:"100"`
	StartsAt   time.Time        `json:"starts_at"`
	EndsAt     time.Time        `json:"ends_at"`
	Recurrence model.Recurrence `json:"recurrence,omitempty" default:"none"`
	MonitorIDs []int64          `json:"monitor_ids" minItems:"1" maxItems:"100"`
}

type createMaintenanceWindowInput struct {
	TeamID int64 `path:"teamID"`
	Body   maintenanceWindowRequestBody
}

type updateMaintenanceWindowInput struct {
	TeamID   int64 `path:"teamID"`
	WindowID int64 `path:"windowID"`
	Body     maintenanceWindowRequestBody
}

type maintenanceWindowBody struct {
	ID         int64            `json:"id"`
	TeamID     int64            `json:"team_id"`
	Name       string           `json:"name"`
	StartsAt   time.Time        `json:"starts_at"`
	EndsAt     time.Time        `json:"ends_at"`
	Recurrence model.Recurrence `json:"recurrence"`
	MonitorIDs []int64          `json:"monitor_ids"`
	CreatedAt  time.Time        `json:"created_at"`
	UpdatedAt  time.Time        `json:"updated_at"`
}

func toMaintenanceWindowBody(w model.MaintenanceWindow) maintenanceWindowBody {
	ids := w.MonitorIDs
	if ids == nil {
		ids = []int64{}
	}
	return maintenanceWindowBody{
		ID: w.ID, TeamID: w.TeamID, Name: w.Name, StartsAt: w.StartsAt, EndsAt: w.EndsAt,
		Recurrence: w.Recurrence, MonitorIDs: ids, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
}

type maintenanceWindowOutput struct {
	Body maintenanceWindowBody
}

type listMaintenanceWindowsOutput struct {
	Body struct {
		Windows []maintenanceWindowBody `json:"maintenance_windows"`
	}
}

// validateMaintenanceWindowBody normalizes the name and checks the
// cross-field rules that JSON schema tags cannot express: the window's
// bounds, its recurrence duration, and that every monitor belongs to
// teamID.
func validateMaintenanceWindowBody(ctx context.Context, d Deps, teamID int64, in maintenanceWindowRequestBody) (name string, err error) {
	name = strings.TrimSpace(in.Name)
	if name == "" {
		return "", huma.Error422UnprocessableEntity("name is required")
	}
	if !in.Recurrence.Valid() {
		return "", huma.Error422UnprocessableEntity("invalid recurrence")
	}
	if !in.EndsAt.After(in.StartsAt) {
		return "", huma.Error422UnprocessableEntity("ends_at must be after starts_at")
	}
	dur := in.EndsAt.Sub(in.StartsAt)
	switch in.Recurrence {
	case model.RecurDaily:
		if dur > 24*time.Hour {
			return "", huma.Error422UnprocessableEntity("window is longer than its recurrence period")
		}
	case model.RecurWeekly:
		if dur > 7*24*time.Hour {
			return "", huma.Error422UnprocessableEntity("window is longer than its recurrence period")
		}
	}
	for _, id := range in.MonitorIDs {
		if _, err := d.Store.GetMonitor(ctx, teamID, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return "", huma.Error422UnprocessableEntity(fmt.Sprintf("monitor_ids: monitor %d not found", id))
			}
			return "", mapStoreErr(d, err)
		}
	}
	return name, nil
}

func registerMaintenanceRoutes(api huma.API, d Deps) {
	huma.Get(api, "/api/teams/{teamID}/maintenance-windows", func(ctx context.Context, in *teamPathInput) (*listMaintenanceWindowsOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		ws, err := d.Store.ListMaintenanceWindows(ctx, in.TeamID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listMaintenanceWindowsOutput{}
		out.Body.Windows = make([]maintenanceWindowBody, len(ws))
		for i, w := range ws {
			out.Body.Windows[i] = toMaintenanceWindowBody(w)
		}
		return out, nil
	})

	huma.Get(api, "/api/teams/{teamID}/maintenance-windows/{windowID}", func(ctx context.Context, in *maintenanceWindowPathInput) (*maintenanceWindowOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		w, err := d.Store.GetMaintenanceWindow(ctx, in.TeamID, in.WindowID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		return &maintenanceWindowOutput{Body: toMaintenanceWindowBody(w)}, nil
	})

	huma.Post(api, "/api/teams/{teamID}/maintenance-windows", func(ctx context.Context, in *createMaintenanceWindowInput) (*maintenanceWindowOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		name, err := validateMaintenanceWindowBody(ctx, d, in.TeamID, in.Body)
		if err != nil {
			return nil, err
		}
		recurrence := in.Body.Recurrence
		if recurrence == "" {
			recurrence = model.RecurNone
		}
		w := model.MaintenanceWindow{
			TeamID: in.TeamID, Name: name, StartsAt: in.Body.StartsAt, EndsAt: in.Body.EndsAt,
			Recurrence: recurrence, MonitorIDs: in.Body.MonitorIDs,
		}
		created, err := d.Store.CreateMaintenanceWindow(ctx, w)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		d.Maintenance.Upsert(created)
		writeAudit(ctx, d, &in.TeamID, act, "maintenance.create", "maintenance_window", &created.ID,
			map[string]any{"name": created.Name})
		return &maintenanceWindowOutput{Body: toMaintenanceWindowBody(created)}, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated })

	huma.Put(api, "/api/teams/{teamID}/maintenance-windows/{windowID}", func(ctx context.Context, in *updateMaintenanceWindowInput) (*maintenanceWindowOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		if _, err := d.Store.GetMaintenanceWindow(ctx, in.TeamID, in.WindowID); err != nil {
			return nil, mapStoreErr(d, err)
		}
		name, err := validateMaintenanceWindowBody(ctx, d, in.TeamID, in.Body)
		if err != nil {
			return nil, err
		}
		recurrence := in.Body.Recurrence
		if recurrence == "" {
			recurrence = model.RecurNone
		}
		w := model.MaintenanceWindow{
			ID: in.WindowID, TeamID: in.TeamID, Name: name, StartsAt: in.Body.StartsAt, EndsAt: in.Body.EndsAt,
			Recurrence: recurrence, MonitorIDs: in.Body.MonitorIDs,
		}
		updated, err := d.Store.UpdateMaintenanceWindow(ctx, w)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		d.Maintenance.Upsert(updated)
		writeAudit(ctx, d, &in.TeamID, act, "maintenance.update", "maintenance_window", &updated.ID,
			map[string]any{"name": updated.Name})
		return &maintenanceWindowOutput{Body: toMaintenanceWindowBody(updated)}, nil
	})

	huma.Delete(api, "/api/teams/{teamID}/maintenance-windows/{windowID}", func(ctx context.Context, in *maintenanceWindowPathInput) (*emptyOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		existing, err := d.Store.GetMaintenanceWindow(ctx, in.TeamID, in.WindowID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		if err := d.Store.DeleteMaintenanceWindow(ctx, in.TeamID, in.WindowID); err != nil {
			return nil, mapStoreErr(d, err)
		}
		d.Maintenance.Remove(in.WindowID)
		writeAudit(ctx, d, &in.TeamID, act, "maintenance.delete", "maintenance_window", &in.WindowID,
			map[string]any{"name": existing.Name})
		return nil, nil
	})
}

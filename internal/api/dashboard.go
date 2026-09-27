package api

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/davidsugianto/upsera/internal/model"
)

type monitorOverviewInput struct {
	TeamID int64 `path:"teamID"`
	Beats  int   `query:"beats" default:"50" minimum:"1" maximum:"100"`
}

type monitorOverviewItem struct {
	MonitorID  int64           `json:"monitor_id"`
	Uptime24h  *float64        `json:"uptime_24h" doc:"Percent of non-maintenance checks up (or pending) in the last 24 hours; null without data."`
	Uptime30d  *float64        `json:"uptime_30d" doc:"Percent from the daily rollups of the last 30 calendar days; null without data."`
	Heartbeats []heartbeatBody `json:"heartbeats" doc:"Most recent heartbeats, oldest first."`
}

type monitorOverviewOutput struct {
	Body struct {
		Monitors []monitorOverviewItem `json:"monitors"`
	}
}

type uptimeSummaryOutput struct {
	Body struct {
		Uptime24h *float64 `json:"uptime_24h" doc:"From raw heartbeats; exact."`
		Uptime7d  *float64 `json:"uptime_7d" doc:"From daily rollups (lags by up to one rollup interval)."`
		Uptime30d *float64 `json:"uptime_30d" doc:"From daily rollups (lags by up to one rollup interval)."`
	}
}

type statusEventsInput struct {
	TeamID    int64 `path:"teamID"`
	MonitorID int64 `path:"monitorID"`
	Limit     int   `query:"limit" default:"50" minimum:"1" maximum:"200"`
}

type statusEventBody struct {
	Time           time.Time     `json:"time"`
	Status         model.Status  `json:"status"`
	PreviousStatus *model.Status `json:"previous_status" nullable:"true" doc:"Null for the oldest retained heartbeat."`
	Message        string        `json:"message"`
}

type statusEventsOutput struct {
	Body struct {
		Events []statusEventBody `json:"events"`
	}
}

// pct returns c as an uptime percentage, or nil without checks.
func pct(c model.UptimeCount) *float64 {
	if c.Checks == 0 {
		return nil
	}
	return new(float64(c.Up) / float64(c.Checks) * 100)
}

// uptimeLocation is the time zone uptime_daily days are bucketed in.
func uptimeLocation(tz string) *time.Location {
	if tz == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

// daysAgo returns the calendar date n days before today in loc.
func daysAgo(loc *time.Location, n int) time.Time {
	return time.Now().In(loc).AddDate(0, 0, -n)
}

func registerDashboardRoutes(api huma.API, d Deps) {
	loc := uptimeLocation(d.Config.TimeZone)

	huma.Get(api, "/api/teams/{teamID}/monitor-overview", func(ctx context.Context, in *monitorOverviewInput) (*monitorOverviewOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		monitors, err := d.Store.ListMonitors(ctx, in.TeamID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		beats, err := d.Store.ListRecentHeartbeats(ctx, in.TeamID, in.Beats)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		day, err := d.Store.CountUptimeSince(ctx, in.TeamID, 0, time.Now().Add(-24*time.Hour))
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		month, err := d.Store.SumUptimeDaily(ctx, in.TeamID, 0, daysAgo(loc, 29))
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &monitorOverviewOutput{}
		out.Body.Monitors = make([]monitorOverviewItem, len(monitors))
		for i, m := range monitors {
			hbs := beats[m.ID]
			item := monitorOverviewItem{
				MonitorID:  m.ID,
				Uptime24h:  pct(day[m.ID]),
				Uptime30d:  pct(month[m.ID]),
				Heartbeats: make([]heartbeatBody, len(hbs)),
			}
			for j, h := range hbs { // newest first -> oldest first
				item.Heartbeats[len(hbs)-1-j] = heartbeatBody{
					ProbeID: h.ProbeID, Time: h.Time, Status: h.Status, LatencyMs: h.LatencyMs, Message: h.Message,
				}
			}
			out.Body.Monitors[i] = item
		}
		return out, nil
	})

	huma.Get(api, "/api/teams/{teamID}/monitors/{monitorID}/uptime-summary", func(ctx context.Context, in *monitorPathInput) (*uptimeSummaryOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		if _, err := d.Store.GetMonitor(ctx, in.TeamID, in.MonitorID); err != nil {
			return nil, mapStoreErr(d, err)
		}
		day, err := d.Store.CountUptimeSince(ctx, in.TeamID, in.MonitorID, time.Now().Add(-24*time.Hour))
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		week, err := d.Store.SumUptimeDaily(ctx, in.TeamID, in.MonitorID, daysAgo(loc, 6))
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		month, err := d.Store.SumUptimeDaily(ctx, in.TeamID, in.MonitorID, daysAgo(loc, 29))
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &uptimeSummaryOutput{}
		out.Body.Uptime24h = pct(day[in.MonitorID])
		out.Body.Uptime7d = pct(week[in.MonitorID])
		out.Body.Uptime30d = pct(month[in.MonitorID])
		return out, nil
	})

	huma.Get(api, "/api/teams/{teamID}/monitors/{monitorID}/events", func(ctx context.Context, in *statusEventsInput) (*statusEventsOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		if _, err := d.Store.GetMonitor(ctx, in.TeamID, in.MonitorID); err != nil {
			return nil, mapStoreErr(d, err)
		}
		changes, err := d.Store.ListStatusChanges(ctx, in.MonitorID, in.Limit)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &statusEventsOutput{}
		out.Body.Events = make([]statusEventBody, len(changes))
		for i, c := range changes {
			out.Body.Events[i] = statusEventBody{Time: c.Time, Status: c.Status, PreviousStatus: c.Previous, Message: c.Message}
		}
		return out, nil
	})
}

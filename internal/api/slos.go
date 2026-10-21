package api

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/slo"
)

type sloInputBody struct {
	Target     float64 `json:"target" exclusiveMinimum:"0" exclusiveMaximum:"100" doc:"Percent of valid checks that must be good, at most 3 decimals (e.g. 99.9)."`
	WindowDays int     `json:"window_days,omitempty" default:"30" minimum:"7" maximum:"90" doc:"Rolling window in calendar days."`
	FastBurn   float64 `json:"fast_burn,omitempty" default:"14.4" minimum:"1" maximum:"1000" doc:"Burn-rate threshold of the fast (1h/5m) alert."`
	SlowBurn   float64 `json:"slow_burn,omitempty" default:"6" minimum:"1" maximum:"1000" doc:"Burn-rate threshold of the slow (6h/30m) alert; at most fast_burn."`
}

type putSLOInput struct {
	TeamID    int64 `path:"teamID"`
	MonitorID int64 `path:"monitorID"`
	Body      sloInputBody
}

type sloBody struct {
	MonitorID  int64   `json:"monitor_id"`
	Target     float64 `json:"target"`
	WindowDays int     `json:"window_days"`
	FastBurn   float64 `json:"fast_burn"`
	SlowBurn   float64 `json:"slow_burn"`
}

type sloStatusBody struct {
	SLI            *float64        `json:"sli,omitempty" minimum:"0" maximum:"1" doc:"Good / valid checks in the window; absent without valid checks."`
	Good           int             `json:"good" doc:"Up, pending and degraded checks in the window."`
	Valid          int             `json:"valid" doc:"Checks in the window, maintenance excluded."`
	BudgetConsumed *float64        `json:"budget_consumed,omitempty" doc:"(1 - sli) / (1 - target/100): 1 = budget exhausted, can exceed 1; absent without data."`
	BurnRate1h     *float64        `json:"burn_rate_1h,omitempty" doc:"Error rate of the last hour / (1 - target/100); absent without valid checks in that hour."`
	BurnRate6h     *float64        `json:"burn_rate_6h,omitempty" doc:"The same over the last 6 hours."`
	Firing         model.SLOFiring `json:"firing" doc:"Which burn-rate alert is firing; fast wins when both are."`
}

type sloPointBody struct {
	Day            string   `json:"day" format:"date" doc:"Calendar day in the server time zone."`
	SLI            *float64 `json:"sli,omitempty" minimum:"0" maximum:"1" doc:"That day's SLI; absent without valid checks."`
	BudgetConsumed float64  `json:"budget_consumed" doc:"Budget consumed over the window up to and including this day (0 before any valid check)."`
}

type sloOutputBody struct {
	SLO    sloBody        `json:"slo"`
	Status sloStatusBody  `json:"status"`
	Series []sloPointBody `json:"series"`
}

type sloOutput struct {
	Body sloOutputBody
}

type sloListItem struct {
	MonitorID   int64         `json:"monitor_id"`
	MonitorName string        `json:"monitor_name"`
	Target      float64       `json:"target"`
	WindowDays  int           `json:"window_days"`
	FastBurn    float64       `json:"fast_burn"`
	SlowBurn    float64       `json:"slow_burn"`
	Status      sloStatusBody `json:"status"`
}

type listSLOsOutput struct {
	Body struct {
		SLOs []sloListItem `json:"slos"`
	}
}

func toSLOBody(s model.SLO) sloBody {
	return sloBody{MonitorID: s.MonitorID, Target: s.Target, WindowDays: s.WindowDays, FastBurn: s.FastBurn, SlowBurn: s.SlowBurn}
}

// sloData is what an SLO's status and series are computed from.
type sloData struct {
	days  map[int64][]model.DayCount
	burns map[int64][]model.SLICount // last 1h, last 6h
	today time.Time
}

// loadSLOData fetches the daily tallies (for the widest window) and the 1h /
// 6h burn tallies of slos: two queries however many SLOs there are.
func loadSLOData(ctx context.Context, d Deps, slos []model.SLO) (sloData, error) {
	now := time.Now()
	loc := uptimeLocation(d.Config.TimeZone)
	data := sloData{today: model.DateIn(now, loc)}
	if len(slos) == 0 {
		return data, nil
	}
	ids := make([]int64, len(slos))
	widest := 0
	for i, s := range slos {
		ids[i] = s.MonitorID
		widest = max(widest, s.WindowDays)
	}
	var err error
	if data.days, err = d.Store.DailyCounts(ctx, ids, data.today.AddDate(0, 0, 1-widest), data.today, now, loc); err != nil {
		return data, err
	}
	data.burns, err = d.Store.BurnCounts(ctx, ids, now, []time.Time{now.Add(-time.Hour), now.Add(-6 * time.Hour)})
	return data, err
}

// windowDays is s's days within its own window.
func (data sloData) windowDays(s model.SLO) (from time.Time, days []model.DayCount) {
	from = data.today.AddDate(0, 0, 1-s.WindowDays)
	for _, dc := range data.days[s.MonitorID] {
		if !dc.Day.Before(from) {
			days = append(days, dc)
		}
	}
	return from, days
}

func optional(v float64, ok bool) *float64 {
	if !ok {
		return nil
	}
	return new(v)
}

func (data sloData) status(s model.SLO) sloStatusBody {
	_, days := data.windowDays(s)
	c := slo.Sum(days)
	st := sloStatusBody{Good: c.Good, Valid: c.Valid, Firing: s.Firing()}
	st.SLI = optional(slo.SLI(c))
	st.BudgetConsumed = optional(slo.Consumed(c, s.Target))
	if b := data.burns[s.MonitorID]; len(b) == 2 {
		st.BurnRate1h = optional(slo.BurnRate(b[0], s.Target))
		st.BurnRate6h = optional(slo.BurnRate(b[1], s.Target))
	}
	return st
}

func (data sloData) output(s model.SLO) *sloOutput {
	from, days := data.windowDays(s)
	pts := slo.Series(days, from, s.WindowDays, s.Target)
	out := &sloOutput{Body: sloOutputBody{SLO: toSLOBody(s), Status: data.status(s), Series: make([]sloPointBody, len(pts))}}
	for i, p := range pts {
		out.Body.Series[i] = sloPointBody{Day: p.Day.Format(time.DateOnly), SLI: p.SLI, BudgetConsumed: p.Consumed}
	}
	return out
}

// validateSLOInput applies the defaults and the checks the schema cannot
// express (decimals, slow <= fast).
func validateSLOInput(in sloInputBody) (model.SLO, error) {
	s := model.SLO{Target: in.Target, WindowDays: in.WindowDays, FastBurn: in.FastBurn, SlowBurn: in.SlowBurn}
	if s.WindowDays == 0 {
		s.WindowDays = model.DefaultSLOWindowDays
	}
	if s.FastBurn == 0 {
		s.FastBurn = model.DefaultFastBurn
	}
	if s.SlowBurn == 0 {
		s.SlowBurn = model.DefaultSlowBurn
	}
	switch {
	case !(s.Target > 0 && s.Target < 100):
		return s, huma.Error422UnprocessableEntity("target must be between 0 and 100 (exclusive)")
	case math.Abs(s.Target*1000-math.Round(s.Target*1000)) > 1e-6:
		return s, huma.Error422UnprocessableEntity("target has at most 3 decimals")
	case s.WindowDays < model.MinSLOWindowDays || s.WindowDays > model.MaxSLOWindowDays:
		return s, huma.Error422UnprocessableEntity("window_days must be between 7 and 90")
	case s.SlowBurn < 1 || s.FastBurn > model.MaxBurn:
		return s, huma.Error422UnprocessableEntity("burn thresholds must be between 1 and 1000")
	case s.SlowBurn > s.FastBurn:
		return s, huma.Error422UnprocessableEntity("slow_burn must not exceed fast_burn")
	}
	s.Target = math.Round(s.Target*1000) / 1000
	return s, nil
}

func registerSLORoutes(api huma.API, d Deps) {
	huma.Get(api, "/api/teams/{teamID}/monitors/{monitorID}/slo", func(ctx context.Context, in *monitorPathInput) (*sloOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		s, err := d.Store.GetSLO(ctx, in.TeamID, in.MonitorID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		data, err := loadSLOData(ctx, d, []model.SLO{s})
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		return data.output(s), nil
	})

	huma.Put(api, "/api/teams/{teamID}/monitors/{monitorID}/slo", func(ctx context.Context, in *putSLOInput) (*sloOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		if _, err := d.Store.GetMonitor(ctx, in.TeamID, in.MonitorID); err != nil {
			return nil, mapStoreErr(d, err)
		}
		s, err := validateSLOInput(in.Body)
		if err != nil {
			return nil, err
		}
		s.TeamID, s.MonitorID = in.TeamID, in.MonitorID
		saved, err := d.Store.UpsertSLO(ctx, s)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		writeAudit(ctx, d, &in.TeamID, act, "slo.set", "monitor", &in.MonitorID, map[string]any{
			"target": saved.Target, "window_days": saved.WindowDays, "fast_burn": saved.FastBurn, "slow_burn": saved.SlowBurn,
		})
		data, err := loadSLOData(ctx, d, []model.SLO{saved})
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		return data.output(saved), nil
	})

	huma.Delete(api, "/api/teams/{teamID}/monitors/{monitorID}/slo", func(ctx context.Context, in *monitorPathInput) (*emptyOutput, error) {
		act, err := authorizeTeam(ctx, d, in.TeamID, model.RoleEditor)
		if err != nil {
			return nil, err
		}
		if err := d.Store.DeleteSLO(ctx, in.TeamID, in.MonitorID); err != nil {
			return nil, mapStoreErr(d, err)
		}
		writeAudit(ctx, d, &in.TeamID, act, "slo.delete", "monitor", &in.MonitorID, nil)
		return nil, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusNoContent })

	huma.Get(api, "/api/teams/{teamID}/slos", func(ctx context.Context, in *teamPathInput) (*listSLOsOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		slos, err := d.Store.ListSLOs(ctx, in.TeamID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		data, err := loadSLOData(ctx, d, slos)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		out := &listSLOsOutput{}
		out.Body.SLOs = make([]sloListItem, len(slos))
		for i, s := range slos {
			out.Body.SLOs[i] = sloListItem{MonitorID: s.MonitorID, MonitorName: s.MonitorName, Target: s.Target,
				WindowDays: s.WindowDays, FastBurn: s.FastBurn, SlowBurn: s.SlowBurn, Status: data.status(s)}
		}
		return out, nil
	})
}

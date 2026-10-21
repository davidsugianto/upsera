package api

import (
	"context"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/slo"
)

type slaReportInput struct {
	TeamID int64  `path:"teamID"`
	Month  string `query:"month" doc:"YYYY-MM in the server time zone; default the current month."`
}

type slaRowBody struct {
	MonitorID    int64    `json:"monitor_id"`
	MonitorName  string   `json:"monitor_name"`
	Checks       int      `json:"checks" doc:"Checks in the month, maintenance excluded."`
	UptimePct    *float64 `json:"uptime_pct,omitempty" doc:"Up, pending and degraded checks / checks × 100; absent without checks."`
	SLOTarget    *float64 `json:"slo_target,omitempty" doc:"The monitor's SLO target; absent without an SLO."`
	SLOMet       *bool    `json:"slo_met,omitempty" doc:"uptime_pct >= slo_target; absent without an SLO or without checks."`
	DowntimeS    int64    `json:"downtime_s" doc:"Seconds of the month covered by the monitor's alerts (an open alert counts up to now)."`
	Alerts       int      `json:"alerts" doc:"Alerts whose incident started in the month."`
	P95LatencyMs int      `json:"p95_latency_ms" doc:"Approximation: the checks-weighted average of the daily p95 latencies over the days with checks (0 without any)."`
}

type slaReportOutput struct {
	Body struct {
		Month   string       `json:"month"`
		From    string       `json:"from" format:"date"`
		To      string       `json:"to" format:"date"`
		Partial bool         `json:"partial" doc:"True for the current month (it stops at today)."`
		Rows    []slaRowBody `json:"rows"`
	}
}

var monthRE = regexp.MustCompile(`^\d{4}-\d{2}$`)

func registerReportRoutes(api huma.API, d Deps) {
	huma.Get(api, "/api/teams/{teamID}/reports/sla", func(ctx context.Context, in *slaReportInput) (*slaReportOutput, error) {
		if _, err := authorizeTeam(ctx, d, in.TeamID, model.RoleViewer); err != nil {
			return nil, err
		}
		loc := uptimeLocation(d.Config.TimeZone)
		now := time.Now()
		today := model.DateIn(now, loc)
		month := today.Format("2006-01")
		if in.Month != "" {
			month = in.Month
		}
		first, err := time.Parse("2006-01", month)
		if err != nil || !monthRE.MatchString(month) {
			return nil, huma.Error422UnprocessableEntity("month must be YYYY-MM")
		}
		last := first.AddDate(0, 1, -1)
		partial := month == today.Format("2006-01")
		if partial {
			last = today
		}
		start, end := time.Date(first.Year(), first.Month(), 1, 0, 0, 0, 0, loc), time.Date(first.Year(), first.Month()+1, 1, 0, 0, 0, 0, loc)

		monitors, err := d.Store.ListMonitors(ctx, in.TeamID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		slices.SortStableFunc(monitors, func(a, b model.Monitor) int {
			if c := strings.Compare(a.Name, b.Name); c != 0 {
				return c
			}
			return int(a.ID - b.ID)
		})
		ids := make([]int64, len(monitors))
		for i, m := range monitors {
			ids[i] = m.ID
		}
		days, err := d.Store.DailyCounts(ctx, ids, first, last, now, loc)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		alerts, err := d.Store.SLAAlerts(ctx, in.TeamID, start, end, now)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		slos, err := d.Store.ListSLOs(ctx, in.TeamID)
		if err != nil {
			return nil, mapStoreErr(d, err)
		}
		targets := make(map[int64]float64, len(slos))
		for _, s := range slos {
			targets[s.MonitorID] = s.Target
		}

		out := &slaReportOutput{}
		out.Body.Month, out.Body.Partial = month, partial
		out.Body.From, out.Body.To = first.Format(time.DateOnly), last.Format(time.DateOnly)
		out.Body.Rows = make([]slaRowBody, len(monitors))
		for i, m := range monitors {
			c := slo.Sum(days[m.ID])
			row := slaRowBody{MonitorID: m.ID, MonitorName: m.Name, Checks: c.Valid,
				DowntimeS: alerts[m.ID].DowntimeS, Alerts: alerts[m.ID].Alerts, P95LatencyMs: weightedP95(days[m.ID])}
			if sli, ok := slo.SLI(c); ok {
				row.UptimePct = new(sli * 100)
			}
			if t, ok := targets[m.ID]; ok {
				row.SLOTarget = new(t)
				if row.UptimePct != nil {
					row.SLOMet = new(*row.UptimePct >= t)
				}
			}
			out.Body.Rows[i] = row
		}
		return out, nil
	})
}

// weightedP95 is the checks-weighted average of the daily p95 latencies over
// the days with checks: an approximation of the month's p95.
func weightedP95(days []model.DayCount) int {
	var sum, n float64
	for _, d := range days {
		if d.Count.Valid > 0 {
			sum += float64(d.P95Ms) * float64(d.Count.Valid)
			n += float64(d.Count.Valid)
		}
	}
	if n == 0 {
		return 0
	}
	return int(math.Round(sum / n))
}

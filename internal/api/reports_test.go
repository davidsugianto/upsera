package api_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/davidsugianto/upsera/internal/model"
)

func TestSLAReportAPI(t *testing.T) {
	e := newSLOEnv(t)
	slow := e.monitor(e.teamID, "b-slo", false)
	plain := e.monitor(e.teamID, "a-plain", false)
	paused := e.monitor(e.teamID, "c-paused", true)
	e.monitor(e.otherID, "foreign", false)
	resp, body := do(t, e.admin, http.MethodPut, e.sloURL(e.teamID, slow), e.csrf, "", map[string]any{"target": 99.9})
	requireStatus(t, resp, http.StatusOK, body)
	up, down := model.StatusUp, model.StatusDown
	e.beats(slow, up, up, up, down) // 75%: misses 99.9
	e.beats(plain, up, up)

	// One alert still open for slow, started 10 minutes ago.
	start := time.Now().Add(-10 * time.Minute)
	if err := e.st.UpsertAlerts(context.Background(), []model.Alert{{ID: uuid.Must(uuid.NewV7()).String(), TeamID: e.teamID,
		MonitorID: slow, IncidentStart: start, OpenedAt: start, Step: 0, NotifiedChannelIDs: []int64{}, UpdatedAt: start}}); err != nil {
		t.Fatal(err)
	}

	reportURL := fmt.Sprintf("%s/api/teams/%d/reports/sla", e.base, e.teamID)
	t.Run("current month", func(t *testing.T) {
		resp, body := do(t, e.viewer, http.MethodGet, reportURL, "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		requireKeys(t, "report", body, "month", "from", "to", "partial", "rows")
		now := time.Now().In(e.loc)
		if body["month"] != now.Format("2006-01") || body["from"] != now.Format("2006-01")+"-01" ||
			body["to"] != now.Format(time.DateOnly) || body["partial"] != true {
			t.Fatalf("report header = %v", body)
		}
		rows := body["rows"].([]any)
		if len(rows) != 3 {
			t.Fatalf("rows = %v, want the team's 3 monitors (paused included)", rows)
		}
		byName := map[string]map[string]any{}
		var order []string
		for _, r := range rows {
			m := r.(map[string]any)
			byName[m["monitor_name"].(string)] = m
			order = append(order, m["monitor_name"].(string))
		}
		if order[0] != "a-plain" || order[1] != "b-slo" || order[2] != "c-paused" {
			t.Fatalf("order = %v", order)
		}
		s := byName["b-slo"]
		requireKeys(t, "slo row", s, "monitor_id", "monitor_name", "checks", "uptime_pct", "slo_target", "slo_met",
			"downtime_s", "alerts", "p95_latency_ms")
		if s["checks"] != float64(4) || s["uptime_pct"] != float64(75) || s["slo_target"] != 99.9 || s["slo_met"] != false ||
			s["alerts"] != float64(1) || s["p95_latency_ms"] == float64(0) {
			t.Fatalf("slo row = %v", s)
		}
		if d := s["downtime_s"].(float64); d < 590 || d > 700 {
			t.Fatalf("downtime_s = %v, want about 600 (open alert counts up to now)", d)
		}
		requireKeys(t, "plain row", byName["a-plain"], "monitor_id", "monitor_name", "checks", "uptime_pct",
			"downtime_s", "alerts", "p95_latency_ms")
		requireKeys(t, "paused row", byName["c-paused"], "monitor_id", "monitor_name", "checks", "downtime_s", "alerts",
			"p95_latency_ms")
		if byName["c-paused"]["monitor_id"] != float64(paused) {
			t.Fatalf("paused row = %v", byName["c-paused"])
		}

		// Read tokens may read it.
		resp, body = do(t, http.DefaultClient, http.MethodGet, reportURL, "", e.readTok, nil)
		requireStatus(t, resp, http.StatusOK, body)
	})

	t.Run("past month", func(t *testing.T) {
		resp, body := do(t, e.admin, http.MethodGet, reportURL+"?month=2024-02", "", "", nil)
		requireStatus(t, resp, http.StatusOK, body)
		if body["month"] != "2024-02" || body["from"] != "2024-02-01" || body["to"] != "2024-02-29" || body["partial"] != false {
			t.Fatalf("report header = %v", body)
		}
		for _, r := range body["rows"].([]any) {
			m := r.(map[string]any)
			if m["checks"] != float64(0) || m["downtime_s"] != float64(0) || m["alerts"] != float64(0) {
				t.Fatalf("row with data in an empty month: %v", m)
			}
			if _, ok := m["uptime_pct"]; ok {
				t.Fatalf("uptime_pct present without checks: %v", m)
			}
			if _, ok := m["slo_met"]; ok {
				t.Fatalf("slo_met present without checks: %v", m)
			}
		}
	})

	t.Run("bad month", func(t *testing.T) {
		for _, m := range []string{"2026-13", "2026-1", "abc", "2026-00", "26-01"} {
			resp, body := do(t, e.admin, http.MethodGet, reportURL+"?month="+m, "", "", nil)
			requireStatus(t, resp, http.StatusUnprocessableEntity, body)
		}
	})

	t.Run("other team", func(t *testing.T) {
		resp, body := do(t, e.viewer, http.MethodGet, fmt.Sprintf("%s/api/teams/%d/reports/sla", e.base, e.otherID), "", "", nil)
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusForbidden {
			t.Fatalf("other team's report: %d %v", resp.StatusCode, body)
		}
	})
}

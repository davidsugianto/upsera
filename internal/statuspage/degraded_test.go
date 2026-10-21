package statuspage

import (
	"net/http"
	"strings"
	"testing"

	"github.com/davidsugianto/upsera/internal/model"
)

func TestRenderDegraded(t *testing.T) {
	up, down, deg, maint := model.StatusUp, model.StatusDown, model.StatusDegraded, model.StatusMaintenance
	tests := []struct {
		name   string
		states fakeStates
		want   string
		class  string
	}{
		{"one degraded", renderTestStates(up, deg, up), "Degraded performance", "b-degraded"},
		{"down beats degraded", renderTestStates(deg, down, up), "Partial outage", "b-warn"},
		{"degraded beats maintenance", renderTestStates(maint, deg, up), "Degraded performance", "b-degraded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(t, renderTestStore(renderTestPage()), tt.states, nil)
			rec := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme")
			body := rec.Body.String()
			if rec.Code != http.StatusOK || !strings.Contains(body, `class="banner `+tt.class+`" role="status"`) ||
				!strings.Contains(body, ">"+tt.want+"<") {
				t.Fatalf("banner %s %q missing (status %d)", tt.class, tt.want, rec.Code)
			}
		})
	}

	// The item says so too, and nothing about latency is ever public.
	states := renderTestStates(up, deg, up)
	st := states[102]
	st.Regions = map[string]model.RegionState{"sg-1": {Status: deg, LatencyMs: 4321, Message: "slow"}}
	states[102] = st
	svc := newTestService(t, renderTestStore(renderTestPage()), states, nil)
	body := renderTestGet(svc.Handler(), http.MethodGet, "/status/acme").Body.String()
	if !strings.Contains(body, `class="state s-degraded"`) || strings.Count(body, ">Degraded performance<") != 2 {
		t.Fatalf("degraded item state missing:\n%s", body)
	}
	for _, leak := range []string{"4321", " ms", "p95", "latency", "sg-1"} {
		if strings.Contains(strings.ToLower(body), leak) {
			t.Errorf("public page exposes %q", leak)
		}
	}
}

package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/testutil"
)

func TestSLOEventText(t *testing.T) {
	t.Parallel()
	burn := Event{Kind: model.EventSLOBurn, MonitorName: "api", SLOLevel: "fast", SLOTarget: 99.9, BurnRate: 1000, BudgetConsumed: 1.25}
	if got := Title(burn); got != "[SLO BURN] api (fast)" {
		t.Errorf("title = %q", got)
	}
	body := Body(burn)
	for _, want := range []string{"Fast burn", "99.9%", "1000.0×", "125%"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q missing %q", body, want)
		}
	}
	rec := Event{Kind: model.EventSLOBurnRecovered, MonitorName: "api", SLOLevel: "slow", SLOTarget: 99.95, BurnRate: 0.5, BudgetConsumed: -1}
	if got := Title(rec); got != "[SLO RECOVERED] api (slow)" {
		t.Errorf("title = %q", got)
	}
	body = Body(rec)
	for _, want := range []string{"Slow burn", "99.95%", "0.5×", "unknown"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q missing %q", body, want)
		}
	}
}

func TestSLOEventsReachEveryProvider(t *testing.T) {
	t.Parallel()
	ev := Event{Kind: model.EventSLOBurn, TeamID: 1, MonitorID: 9, MonitorName: "api", SLOLevel: "fast", SLOTarget: 99.9,
		BurnRate: 1000, BudgetConsumed: 1.25, At: time.Now()}
	wantTitle := Title(ev)
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := json.Marshal(decodeBody(t, r))
		got = append(got, string(raw))
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	hc := srv.Client()
	check := func(name string) {
		t.Helper()
		if len(got) != 1 || !strings.Contains(got[0], "SLO BURN") || !strings.Contains(got[0], "1000.0×") ||
			strings.Contains(got[0], "Acknowledge") {
			t.Errorf("%s: payloads %v", name, got)
		}
		got = nil
	}
	cfg, _ := json.Marshal(map[string]string{"bot_token": "123456:" + strings.Repeat("A", 35), "chat_id": "1"})
	if err := (&telegram{api: srv.URL, client: hc}).Send(context.Background(), cfg, ev); err != nil {
		t.Fatal(err)
	}
	check("telegram")
	wcfg, _ := json.Marshal(map[string]string{"webhook_url": srv.URL})
	if err := (&discord{client: hc}).Send(context.Background(), wcfg, ev); err != nil {
		t.Fatal(err)
	}
	check("discord")
	if err := (&slackWebhook{client: hc}).Send(context.Background(), wcfg, ev); err != nil {
		t.Fatal(err)
	}
	check("slack")
	acfg, _ := json.Marshal(map[string]string{"bot_token": "xoxb-1", "app_token": "xapp-1", "channel": "C1"})
	if err := (&slackApp{api: srv.URL, client: hc}).Send(context.Background(), acfg, ev); err != nil {
		t.Fatal(err)
	}
	check("slackapp")

	var body map[string]any
	whSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { body = decodeBody(t, r) }))
	defer whSrv.Close()
	ucfg, _ := json.Marshal(map[string]any{"url": whSrv.URL})
	if err := (&webhook{client: whSrv.Client()}).Send(context.Background(), ucfg, ev); err != nil {
		t.Fatal(err)
	}
	if body["event"] != "slo_burn" || body["slo_level"] != "fast" || body["slo_target"] != 99.9 ||
		body["burn_rate"] != float64(1000) || body["budget_consumed"] != 1.25 {
		t.Errorf("webhook payload = %v", body)
	}
	// A recovery with burn 0 still carries it; unknown consumption is left out.
	rec := ev
	rec.Kind, rec.BurnRate, rec.BudgetConsumed = model.EventSLOBurnRecovered, 0, -1
	if err := (&webhook{client: whSrv.Client()}).Send(context.Background(), ucfg, rec); err != nil {
		t.Fatal(err)
	}
	if v, ok := body["burn_rate"]; !ok || v != float64(0) {
		t.Errorf("recovered payload burn_rate = %v (present %v)", v, ok)
	}
	if _, ok := body["budget_consumed"]; ok {
		t.Errorf("unknown budget_consumed present: %v", body)
	}
	// Other events carry no SLO fields.
	if err := (&webhook{client: whSrv.Client()}).Send(context.Background(), ucfg, Event{Kind: model.EventDown}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"slo_level", "slo_target", "burn_rate", "budget_consumed"} {
		if _, ok := body[k]; ok {
			t.Errorf("down payload has %s: %v", k, body)
		}
	}

	addr, messages := testutil.FakeSMTP(t)
	host, port, _ := strings.Cut(addr, ":")
	scfg, _ := json.Marshal(map[string]any{"host": host, "port": atoi(t, port), "from": "a@upsera.dev", "to": []string{"b@example.com"}, "tls": "none"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := (&smtpNotifier{}).Send(ctx, scfg, ev); err != nil {
		t.Fatal(err)
	}
	if msgs := messages(); len(msgs) != 1 || !strings.Contains(msgs[0].Data, "Subject: "+wantTitle+"\r\n") {
		t.Errorf("smtp messages = %+v", msgs)
	}
}

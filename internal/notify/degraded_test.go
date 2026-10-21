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

func TestDegradedEventText(t *testing.T) {
	t.Parallel()
	deg := Event{Kind: model.EventDegraded, MonitorName: "api", Region: "sg-1", LatencyP95Ms: 640, BaselineMs: 52}
	if got := Title(deg); got != "[DEGRADED] api" {
		t.Errorf("degraded title = %q", got)
	}
	body := Body(deg)
	for _, want := range []string{`"sg-1"`, "640 ms", "52 ms", "still up"} {
		if !strings.Contains(body, want) {
			t.Errorf("degraded body %q missing %q", body, want)
		}
	}
	rec := Event{Kind: model.EventLatencyRecovered, MonitorName: "api", Region: "local", LatencyP95Ms: 48, BaselineMs: 52}
	if got := Title(rec); got != "[LATENCY OK] api" {
		t.Errorf("latency_recovered title = %q", got)
	}
	body = Body(rec)
	for _, want := range []string{`"local"`, "48 ms", "52 ms", "back to normal"} {
		if !strings.Contains(body, want) {
			t.Errorf("latency_recovered body %q missing %q", body, want)
		}
	}
	// No region (a region went quiet): still readable.
	if body := Body(Event{Kind: model.EventLatencyRecovered, MonitorName: "api"}); strings.Contains(body, `""`) {
		t.Errorf("body without region = %q", body)
	}
}

func TestDegradedEventsReachEveryProvider(t *testing.T) {
	t.Parallel()
	ev := Event{Kind: model.EventDegraded, TeamID: 1, MonitorID: 9, MonitorName: "api", Region: "sg-1",
		LatencyP95Ms: 640, BaselineMs: 52, At: time.Now()}
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
		if len(got) != 1 || !strings.Contains(got[0], "DEGRADED") || !strings.Contains(got[0], "640 ms") ||
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
	if body["event"] != "degraded" || body["region"] != "sg-1" || body["latency_p95_ms"] != float64(640) ||
		body["baseline_ms"] != float64(52) {
		t.Errorf("webhook payload = %v", body)
	}

	addr, messages := testutil.FakeSMTP(t)
	host, port, _ := strings.Cut(addr, ":")
	scfg, _ := json.Marshal(map[string]any{"host": host, "port": atoi(t, port), "from": "a@upsera.dev", "to": []string{"b@example.com"}, "tls": "none"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := (&smtpNotifier{}).Send(ctx, scfg, ev); err != nil {
		t.Fatal(err)
	}
	if msgs := messages(); len(msgs) != 1 || !strings.Contains(msgs[0].Data, "Subject: "+wantTitle+"\r\n") ||
		!strings.Contains(msgs[0].Data, "640 ms") {
		t.Errorf("smtp messages = %+v", msgs)
	}
}

func TestWebhookOmitsLatencyForOtherEvents(t *testing.T) {
	t.Parallel()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { body = decodeBody(t, r) }))
	defer srv.Close()
	cfg, _ := json.Marshal(map[string]any{"url": srv.URL})
	if err := (&webhook{client: srv.Client()}).Send(context.Background(), cfg, Event{Kind: model.EventDown, MonitorName: "api"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["latency_p95_ms"]; ok {
		t.Errorf("down payload carries latency: %v", body)
	}
}

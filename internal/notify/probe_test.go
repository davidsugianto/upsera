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

func TestProbeEventText(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 3, 10, 7, 30, 0, 0, time.UTC)
	silent := Event{Kind: model.EventProbeSilent, Region: "sg-1", At: at}
	if got := Title(silent); got != "[PROBE SILENT] sg-1" {
		t.Errorf("silent title = %q", got)
	}
	body := Body(silent)
	for _, want := range []string{`"sg-1"`, at.Format(time.RFC1123), "not DOWN"} {
		if !strings.Contains(body, want) {
			t.Errorf("silent body %q missing %q", body, want)
		}
	}
	rec := Event{Kind: model.EventProbeRecovered, Region: "sg-1", At: at}
	if got := Title(rec); got != "[PROBE UP] sg-1" {
		t.Errorf("recovered title = %q", got)
	}
	if got := Body(rec); !strings.Contains(got, `"sg-1"`) || !strings.Contains(got, "reporting again") {
		t.Errorf("recovered body = %q", got)
	}
}

func TestProbeEventsReachEveryProvider(t *testing.T) {
	t.Parallel()
	ev := Event{Kind: model.EventProbeSilent, Region: "sg-1", At: time.Now(), TeamID: 1}
	wantTitle, wantBody := Title(ev), Body(ev)

	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := decodeBody(t, r)
		raw, _ := json.Marshal(b)
		got = append(got, string(raw))
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	hc := srv.Client()

	check := func(name string) {
		t.Helper()
		if len(got) != 1 {
			t.Fatalf("%s: %d requests", name, len(got))
		}
		// JSON escapes < > & and newlines; compare via decoding.
		var m map[string]any
		_ = json.Unmarshal([]byte(got[0]), &m)
		flat, _ := json.Marshal(m)
		if !strings.Contains(string(flat), "sg-1") || strings.Contains(string(flat), "Acknowledge") {
			t.Errorf("%s: payload %s", name, flat)
		}
		got = nil
	}

	tg := &telegram{api: srv.URL, client: hc}
	cfg, _ := json.Marshal(map[string]string{"bot_token": "123456:" + strings.Repeat("A", 35), "chat_id": "1"})
	if err := tg.Send(context.Background(), cfg, ev); err != nil {
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

	// webhook carries the region in a field of its own
	var body map[string]any
	whSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { body = decodeBody(t, r) }))
	defer whSrv.Close()
	ucfg, _ := json.Marshal(map[string]any{"url": whSrv.URL})
	if err := (&webhook{client: whSrv.Client()}).Send(context.Background(), ucfg, ev); err != nil {
		t.Fatal(err)
	}
	if body["event"] != "probe_silent" || body["region"] != "sg-1" {
		t.Errorf("webhook payload = %v", body)
	}

	// smtp
	addr, messages := testutil.FakeSMTP(t)
	host, port, _ := strings.Cut(addr, ":")
	scfg, _ := json.Marshal(map[string]any{"host": host, "port": atoi(t, port), "from": "a@upsera.dev", "to": []string{"b@example.com"}, "tls": "none"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := (&smtpNotifier{}).Send(ctx, scfg, ev); err != nil {
		t.Fatal(err)
	}
	msgs := messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0].Data, "Subject: "+wantTitle+"\r\n") || !strings.Contains(msgs[0].Data, strings.SplitN(wantBody, "\n", 2)[0][:20]) {
		t.Errorf("smtp messages = %+v", msgs)
	}
}

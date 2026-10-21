package upsctl_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/notify"
	"github.com/davidsugianto/upsera/internal/upsctl"
)

const botToken = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef0123"

// sampleSpec lists the child before its parent and the page before its
// monitors, so ordering is the planner's job.
const sampleSpec = `
version: 1
status_pages:
  - slug: public
    title: Public status
    published: true
    sections:
      - name: Core
        items:
          - {monitor: api, display_name: API}
          - {monitor: db}
monitors:
  - name: api
    type: http
    interval_s: 30
    parent: db
    escalation_policy: default
    config: {url: "https://api.example.com/health"}
  - name: db
    type: tcp
    channels: [oncall]
    config: {host: db.internal, port: 5432}
escalation_policies:
  - name: default
    steps:
      - {channels: [oncall], delay_s: 300}
channels:
  - name: oncall
    type: telegram
    is_default: true
    config: {chat_id: "42", bot_token: "${UPSERA_ONCALL_BOT_TOKEN}"}
`

var sampleEnv = env(map[string]string{"UPSERA_ONCALL_BOT_TOKEN": botToken})

func apply(t *testing.T, api upsctl.API, yaml string, prune bool) (upsctl.Result, string) {
	t.Helper()
	var out bytes.Buffer
	res, err := upsctl.Apply(context.Background(), api, mustParse(t, yaml, sampleEnv), upsctl.ApplyOptions{Prune: prune, Out: &out})
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out.String())
	}
	return res, out.String()
}

func diff(t *testing.T, api upsctl.API, yaml string, prune bool) (int, string) {
	t.Helper()
	var out bytes.Buffer
	s, err := upsctl.Parse("spec.yaml", []byte(yaml), sampleEnv)
	if err != nil {
		return upsctl.DiffExitCode(false, err), err.Error()
	}
	changed, err := upsctl.Diff(context.Background(), api, s, prune, &out)
	if err != nil {
		return upsctl.DiffExitCode(changed, err), out.String() + err.Error()
	}
	return upsctl.DiffExitCode(changed, err), out.String()
}

func TestApplyOrderAndIdempotence(t *testing.T) {
	f := newFakeAPI()
	res, _ := apply(t, f, sampleSpec, false)
	if res.Created != 5 || res.Updated != 0 || res.Deleted != 0 {
		t.Fatalf("result = %+v", res)
	}
	want := []string{
		"create channel oncall", "create escalation policy default",
		"create monitor db", "create monitor api", "create status page public",
	}
	if got := f.takeWrites(); !slices.Equal(got, want) {
		t.Fatalf("writes = %q, want %q", got, want)
	}
	// References were resolved to the created ids.
	api, db := f.monitors[1], f.monitors[0]
	if *api.ParentID != db.ID || *api.EscalationPolicyID != f.policies[0].ID || db.ChannelIDs[0] != f.channels[0].ID {
		t.Fatalf("references not resolved: api=%+v db=%+v", api, db)
	}
	if !strings.Contains(string(f.channels[0].Config), botToken) {
		t.Fatal("the bot token did not reach the server")
	}
	if p := f.pages[0]; len(p.Items) != 2 || p.Items[0].MonitorID != api.ID || p.Items[0].DisplayName != "API" || p.Items[1].DisplayName != "db" || p.Theme != "auto" {
		t.Fatalf("page = %+v", p)
	}

	// Second run: nothing changes (the channel secret PUT is a no-op).
	res, out := apply(t, f, sampleSpec, false)
	if res.Changed() {
		t.Fatalf("second apply changed %+v:\n%s", res, out)
	}
	if w := f.takeWrites(); len(w) != 0 {
		t.Fatalf("second apply wrote %q", w)
	}
	if !strings.Contains(out, "No changes.") || !strings.Contains(out, `channel "oncall": secrets not compared`) {
		t.Fatalf("output:\n%s", out)
	}
	if code, out := diff(t, f, sampleSpec, false); code != upsctl.ExitNoChanges {
		t.Fatalf("diff exit = %d:\n%s", code, out)
	}

	// A new secret value is sent and reported.
	rotated := strings.Replace(sampleSpec, "${UPSERA_ONCALL_BOT_TOKEN}", "999:ZYXWVUTSRQPONMLKJIHGFEDCBAzyxwvu9876", 1)
	res, out = apply(t, f, rotated, false)
	if res.Updated != 1 || !strings.Contains(out, `updated channel "oncall" (secrets)`) {
		t.Fatalf("rotate: %+v\n%s", res, out)
	}
}

func TestPruneOrder(t *testing.T) {
	f := newFakeAPI()
	apply(t, f, sampleSpec, false)
	apply(t, f, `
version: 1
channels: [{name: spare, type: slack, config: {webhook_url: "https://hooks.example.com/x"}}]
monitors:
  - {name: leaf, type: tcp, parent: mid, config: {host: a, port: 1}}
  - {name: mid, type: tcp, parent: top, config: {host: a, port: 1}}
  - {name: top, type: tcp, config: {host: a, port: 1}}
`, false)
	f.takeWrites()

	// Only the sample stays: without --prune the rest is listed and kept.
	code, out := diff(t, f, sampleSpec, false)
	if code != upsctl.ExitNoChanges {
		t.Fatalf("diff exit = %d:\n%s", code, out)
	}
	for _, w := range []string{`not managed, kept: monitor "leaf"`, `not managed, kept: channel "spare"`} {
		if !strings.Contains(out, w) {
			t.Fatalf("diff output lacks %q:\n%s", w, out)
		}
	}
	res, _ := apply(t, f, sampleSpec, false)
	if res.Changed() || len(f.takeWrites()) != 0 {
		t.Fatal("apply without --prune deleted something")
	}

	// With --prune: children before parents, then policies, then channels;
	// status pages first.
	apply(t, f, strings.Replace(sampleSpec, "status_pages:\n", "status_pages:\n  - {slug: extra, title: Extra}\n", 1), false)
	f.takeWrites()
	res, _ = apply(t, f, sampleSpec, true)
	want := []string{"delete status page extra", "delete monitor leaf", "delete monitor mid", "delete monitor top", "delete channel spare"}
	if got := f.takeWrites(); !slices.Equal(got, want) || res.Deleted != 5 {
		t.Fatalf("prune writes = %q (%+v), want %q", got, res, want)
	}
}

func TestApplyStopsOnFirstError(t *testing.T) {
	f := newFakeAPI()
	f.failOn = "create monitor api"
	var out bytes.Buffer
	res, err := upsctl.Apply(context.Background(), f, mustParse(t, sampleSpec, sampleEnv), upsctl.ApplyOptions{Out: &out})
	var ae *upsctl.APIError
	if err == nil || !errors.As(err, &ae) || !strings.Contains(err.Error(), `create monitor "api" after 3 change(s)`) {
		t.Fatalf("err = %v", err)
	}
	if res.Created != 3 {
		t.Fatalf("result = %+v", res)
	}
	o := out.String()
	for _, w := range []string{`created channel "oncall"`, `created escalation policy "default"`, `created monitor "db"`, `error: create monitor "api" failed`} {
		if !strings.Contains(o, w) {
			t.Fatalf("output lacks %q:\n%s", w, o)
		}
	}
	if strings.Contains(o, `created status page`) {
		t.Fatalf("apply continued after the error:\n%s", o)
	}
}

func TestPlanPrechecks(t *testing.T) {
	f := newFakeAPI()
	f.hosts = []upsctl.DockerHost{{ID: 7, Name: "local"}}
	apply(t, f, sampleSpec, false)
	f.takeWrites()

	_, out := diff(t, f, `
version: 1
channels: [{name: oncall, type: slack, config: {webhook_url: x}}]
monitors:
  - {name: db, type: http, config: {url: "https://x"}}
  - {name: far, type: http, regions: [local, mars], config: {url: "https://x"}}
  - {name: box, type: docker, config: {docker_host: nas, container: web}}
  - {name: ok, type: docker, config: {docker_host: local, container: web}}
`, false)
	for _, w := range []string{
		`channel "oncall": type cannot change from telegram to slack`,
		`monitor "db": type cannot change from tcp to http`,
		`monitor "far": unknown region "mars"`,
		`monitor "box": unknown Docker host "nas"`,
	} {
		if !strings.Contains(out, w) {
			t.Errorf("problems lack %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, `"ok"`) {
		t.Errorf("a known Docker host was reported:\n%s", out)
	}
	if len(f.takeWrites()) != 0 {
		t.Fatal("a failed pre-check wrote something")
	}

	// Duplicate names in the live team refuse to run.
	f.monitors = append(f.monitors, upsctl.Monitor{ID: 900, Name: "db", Type: "tcp"})
	code, out := diff(t, f, sampleSpec, false)
	if code != upsctl.ExitError || !strings.Contains(out, `the team has 2 monitors named "db"`) {
		t.Fatalf("exit %d:\n%s", code, out)
	}

	if _, _, err := upsctl.Export(context.Background(), f); err == nil || !strings.Contains(err.Error(), `the team has 2 monitors named "db"`) {
		t.Fatalf("export with duplicate names: %v", err)
	}

	// Webhook headers from the environment must be a JSON object.
	_, out = diff(t, newFakeAPI(), `
version: 1
channels: [{name: hook, type: webhook, config: {url: "https://h.example.com", headers: "not json"}}]
`, false)
	if !strings.Contains(out, `channel "hook": config.headers must be a mapping`) || strings.Contains(out, "not json") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestDiffGolden(t *testing.T) {
	f := newFakeAPI()
	apply(t, f, sampleSpec, false)
	apply(t, f, "version: 1\nmonitors: [{name: legacy, type: tcp, config: {host: a, port: 1}}]\n", false)

	edited := strings.NewReplacer(
		"interval_s: 30", "interval_s: 45",
		`config: {url: "https://api.example.com/health"}`, `config: {url: "https://api.example.com/v2/health"}`,
		"{monitor: db}", "{monitor: db, display_name: Database}",
		"is_default: true", "is_default: false",
	).Replace(sampleSpec) + `
  - name: pager
    type: slack
    config: {webhook_url: "${UPSERA_ONCALL_BOT_TOKEN}"}
`
	code, out := diff(t, f, edited, false)
	want := `~ update channel "oncall": is_default (secrets not compared)
+ create channel "pager"
~ update monitor "api": interval_s, config.url
~ update status page "public": sections
  not managed, kept: monitor "legacy" (use --prune to delete)
Plan: 1 to create, 3 to update, 0 to delete.
`
	if code != upsctl.ExitChanges || out != want {
		t.Fatalf("exit %d, output:\n%s\nwant:\n%s", code, out, want)
	}

	code, out = diff(t, f, sampleSpec, true)
	want = `  channel "oncall": secrets not compared
- delete monitor "legacy"
Plan: 0 to create, 0 to update, 1 to delete.
`
	if code != upsctl.ExitChanges || out != want {
		t.Fatalf("prune: exit %d, output:\n%s\nwant:\n%s", code, out, want)
	}

	code, out = diff(t, f, strings.Replace(sampleSpec, "    channels: [oncall]\n", "    channels: [oncall]\n    slo: {target: 100}\n", 1), false)
	if code != upsctl.ExitError || !strings.Contains(out, "monitors[1] (db): slo.target must be greater than 0 and less than 100") {
		t.Fatalf("slo: exit %d: %s", code, out)
	}
}

func TestPlanDefaultsAreNotChanges(t *testing.T) {
	f := newFakeAPI()
	apply(t, f, sampleSpec, false)
	// The server fills config defaults and normalizes; a spec that leaves
	// them out or writes them differently is unchanged.
	f.monitors[1].Config = json.RawMessage(`{"url":"https://api.example.com/health","method":"GET","accepted_status":["200-299"],"max_redirects":10,"ignore_tls_errors":false}`)
	f.monitors[1].Tags = []string{}
	if code, out := diff(t, f, sampleSpec, false); code != upsctl.ExitNoChanges {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if code, out := diff(t, f, strings.Replace(sampleSpec, `config: {url: "https://api.example.com/health"}`,
		`config: {url: "https://api.example.com/health", method: get, max_redirects: 10}`, 1), false); code != upsctl.ExitNoChanges {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// A non-default value the spec drops is a change.
	f.monitors[1].Config = json.RawMessage(`{"url":"https://api.example.com/health","method":"POST"}`)
	if code, out := diff(t, f, sampleSpec, false); code != upsctl.ExitChanges || !strings.Contains(out, "config.method") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestCustomDomainIsSkipped(t *testing.T) {
	f := newFakeAPI()
	withDomain := strings.Replace(sampleSpec, "    published: true\n", "    published: true\n    custom_domain: status.example.com\n", 1)
	res, out := apply(t, f, withDomain, false)
	if res.Created != 5 || !strings.Contains(out, `warning: status page "public": custom_domain can only be changed by an instance admin`) {
		t.Fatalf("%+v\n%s", res, out)
	}
	if f.pages[0].CustomDomain != nil {
		t.Fatal("custom_domain was sent")
	}
	// The warning does not count as a change.
	if code, out := diff(t, f, withDomain, false); code != upsctl.ExitNoChanges || !strings.Contains(out, "warning:") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// Export includes the live domain.
	f.pages[0].CustomDomain = new("status.example.com")
	y, _, err := upsctl.Export(context.Background(), f)
	if err != nil || !strings.Contains(string(y), "custom_domain: status.example.com") {
		t.Fatalf("export: %v\n%s", err, y)
	}
}

func TestExport(t *testing.T) {
	f := newFakeAPI()
	f.hosts = []upsctl.DockerHost{{ID: 7, Name: "nas"}}
	apply(t, f, sampleSpec+`
  - name: hook
    type: webhook
    config: {url: "https://hooks.example.com/secret-path", headers: '{"X-Key":"k3y-value"}'}
  - name: mail
    type: smtp
    config: {host: smtp.example.com, port: 587, username: bot, password: "", from: a@example.com, to: [b@example.com], tls: starttls}
`, false)
	apply(t, f, "version: 1\nmonitors: [{name: box, type: docker, config: {docker_host: nas, container: web}}]\n", false)

	y, warnings, err := upsctl.Export(context.Background(), f)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("export: %v %q", err, warnings)
	}
	out := string(y)
	if !strings.HasPrefix(out, "# generated by upsctl, secrets are ${ENV} placeholders\nversion: 1\n") {
		t.Fatalf("header:\n%s", out)
	}
	for _, w := range []string{
		"bot_token: ${UPSERA_ONCALL_BOT_TOKEN}", "url: ${UPSERA_HOOK_URL}", "headers: ${UPSERA_HOOK_HEADERS}",
		`chat_id: "42"`, "docker_host: nas", "parent: db", "escalation_policy: default", "display_name: API",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("export lacks %q:\n%s", w, out)
		}
	}
	for _, leak := range []string{botToken, "secret-path", "k3y-value", "********", "docker_host_id", "push_token", " id:", "updated_at"} {
		if strings.Contains(out, leak) {
			t.Errorf("export contains %q:\n%s", leak, out)
		}
	}
	// An empty secret is not a secret: no placeholder for the SMTP password.
	if strings.Contains(out, "UPSERA_MAIL_PASSWORD") {
		t.Errorf("empty secret got a placeholder:\n%s", out)
	}

	// Deterministic: the live order does not matter.
	for range 5 {
		rand.Shuffle(len(f.channels), func(i, j int) { f.channels[i], f.channels[j] = f.channels[j], f.channels[i] })
		rand.Shuffle(len(f.monitors), func(i, j int) { f.monitors[i], f.monitors[j] = f.monitors[j], f.monitors[i] })
		again, _, err := upsctl.Export(context.Background(), f)
		if err != nil || string(again) != out {
			t.Fatalf("export not deterministic: %v\n%s\n---\n%s", err, again, out)
		}
	}

	// The export is a valid spec that applies to a fresh team as the same
	// setup, with secrets from the environment.
	lookup := env(map[string]string{
		"UPSERA_ONCALL_BOT_TOKEN": botToken, "UPSERA_HOOK_URL": "https://hooks.example.com/secret-path",
		"UPSERA_HOOK_HEADERS": `{"X-Key":"k3y-value"}`,
	})
	s, err := upsctl.Parse("export.yaml", y, lookup)
	if err != nil {
		t.Fatalf("parse export: %v\n%s", err, out)
	}
	g := newFakeAPI()
	g.hosts = []upsctl.DockerHost{{ID: 3, Name: "nas"}}
	if _, err := upsctl.Apply(context.Background(), g, s, upsctl.ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	y2, _, err := upsctl.Export(context.Background(), g)
	if err != nil || string(y2) != out {
		t.Fatalf("round trip differs: %v\n%s\n---\n%s", err, y2, out)
	}
	var hook map[string]any
	_ = json.Unmarshal(g.channels[slices.IndexFunc(g.channels, func(c upsctl.Channel) bool { return c.Name == "hook" })].Config, &hook)
	if h, ok := hook["headers"].(map[string]any); !ok || h["X-Key"] != "k3y-value" {
		t.Fatalf("webhook headers = %v", hook["headers"])
	}
}

func TestPlaceholderVar(t *testing.T) {
	if got := upsctl.PlaceholderVar("oncall-telegram", "bot_token"); got != "UPSERA_ONCALL_TELEGRAM_BOT_TOKEN" {
		t.Fatalf("got %s", got)
	}
	if got := upsctl.PlaceholderVar("Ops pager ü", "url"); got != "UPSERA_OPS_PAGER___URL" {
		t.Fatalf("got %s", got)
	}
}

// TestSecretFieldsMatchNotify keeps upsctl's copy of the secret field list
// identical to the server's.
func TestSecretFieldsMatchNotify(t *testing.T) {
	var types []string
	for _, ct := range model.ChannelTypes {
		types = append(types, string(ct))
		got, want := upsctl.SecretFields(string(ct)), notify.SecretFields(ct)
		if !slices.Equal(got, want) {
			t.Errorf("%s: upsctl.SecretFields = %q, notify.SecretFields = %q", ct, got, want)
		}
	}
	if !slices.Equal(types, upsctl.ChannelTypes) {
		t.Errorf("upsctl.ChannelTypes = %q, model.ChannelTypes = %q", upsctl.ChannelTypes, types)
	}
	var mtypes []string
	for _, mt := range model.MonitorTypes {
		mtypes = append(mtypes, string(mt))
	}
	if !slices.Equal(mtypes, upsctl.MonitorTypes) {
		t.Errorf("upsctl.MonitorTypes = %q, model.MonitorTypes = %q", upsctl.MonitorTypes, mtypes)
	}
}

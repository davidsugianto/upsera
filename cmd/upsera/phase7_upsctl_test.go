package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/config"
	"github.com/davidsugianto/upsera/internal/testutil"
	"github.com/davidsugianto/upsera/internal/upsctl"
)

const ucBotToken = "424242:ucSecretBotTokenValueABCDEFGHIJKLMNOP"

// ucTelegram is a fake Bot API that records which bot token each
// sendMessage used (from the /bot<token>/ path).
type ucTelegram struct {
	mu     sync.Mutex
	tokens []string
}

func (f *ucTelegram) serve(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/bot")
	token, method, _ := strings.Cut(rest, "/")
	switch {
	case ok && method == "sendMessage":
		f.mu.Lock()
		f.tokens = append(f.tokens, token)
		f.mu.Unlock()
		io.WriteString(w, `{"ok":true,"result":{}}`)
	case ok && method == "getUpdates":
		select {
		case <-time.After(time.Second):
		case <-r.Context().Done():
		}
		io.WriteString(w, `{"ok":true,"result":[]}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *ucTelegram) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.tokens)
}

// ucInstance is one server with its admin session and a write token.
type ucInstance struct {
	base, teamURL, token, csrf string
	browser                    *http.Client
}

func ucSetup(t *testing.T, base string) ucInstance {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	in := ucInstance{base: base, browser: &http.Client{Jar: jar}}
	var me struct {
		Teams []struct {
			ID int64 `json:"id"`
		} `json:"teams"`
		CSRFToken string `json:"csrf_token"`
	}
	call(t, in.browser, "POST", base+"/api/setup", "", map[string]any{
		"email": "admin@example.com", "name": "Admin", "password": "correct horse battery", "team_name": "Ops",
	}, http.StatusCreated, &me)
	in.teamURL = fmt.Sprintf("%s/api/teams/%d", base, me.Teams[0].ID)
	in.csrf = me.CSRFToken
	var tok struct {
		Token string `json:"token"`
	}
	callCSRF(t, in.browser, "POST", in.teamURL+"/tokens", in.csrf, map[string]any{"name": "upsctl", "scope": "write"}, &tok)
	in.token = tok.Token
	return in
}

func (in ucInstance) client() *upsctl.Client { return upsctl.NewClient(in.base, in.token) }

func (in ucInstance) post(t *testing.T, path string, body any) int64 {
	t.Helper()
	var out struct {
		ID int64 `json:"id"`
	}
	call(t, http.DefaultClient, "POST", in.teamURL+path, "Bearer "+in.token, body, http.StatusCreated, &out)
	return out.ID
}

// auditCount counts the team's audit entries (owner only, so the session).
func (in ucInstance) auditCount(t *testing.T) int {
	t.Helper()
	return len(in.auditActions(t))
}

// auditActions lists the actions of the team's audit entries.
func (in ucInstance) auditActions(t *testing.T) []string {
	t.Helper()
	var out struct {
		Entries []struct {
			Action string `json:"action"`
		} `json:"entries"`
	}
	call(t, in.browser, "GET", in.teamURL+"/audit?limit=200", "", nil, http.StatusOK, &out)
	actions := make([]string, len(out.Entries))
	for i, e := range out.Entries {
		actions[i] = e.Action
	}
	return actions
}

func ucCount(list []string, v string) int {
	n := 0
	for _, e := range list {
		if e == v {
			n++
		}
	}
	return n
}

// ucSLO is GET .../monitors/{id}/slo on in: the SLO, its firing state and
// the HTTP status (404 when the monitor has none).
type ucSLO struct {
	SLO struct {
		Target     float64 `json:"target"`
		WindowDays int     `json:"window_days"`
		FastBurn   float64 `json:"fast_burn"`
		SlowBurn   float64 `json:"slow_burn"`
	} `json:"slo"`
	Status struct {
		Firing string `json:"firing"`
	} `json:"status"`
}

func (in ucInstance) getSLO(t *testing.T, monitorName string, want int) ucSLO {
	t.Helper()
	ms, err := in.client().Monitors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(ms, func(m upsctl.Monitor) bool { return m.Name == monitorName })
	if i < 0 {
		t.Fatalf("no monitor %q", monitorName)
	}
	var out ucSLO
	var dst any = &out
	if want != http.StatusOK {
		dst = nil
	}
	call(t, http.DefaultClient, "GET", fmt.Sprintf("%s/monitors/%d/slo", in.teamURL, ms[i].ID), "Bearer "+in.token, nil, want, dst)
	return out
}

func ucExport(t *testing.T, in ucInstance) string {
	t.Helper()
	y, warnings, err := upsctl.Export(context.Background(), in.client())
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(warnings) > 0 {
		t.Fatalf("export warnings: %q", warnings)
	}
	return string(y)
}

func ucParse(t *testing.T, y string) *upsctl.Spec {
	t.Helper()
	s, err := upsctl.Parse("upsera.yaml", []byte(y), os.LookupEnv)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := upsctl.Validate(s); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return s
}

func ucDiff(t *testing.T, in ucInstance, s *upsctl.Spec, prune bool) (int, string) {
	t.Helper()
	var out bytes.Buffer
	changed, err := upsctl.Diff(context.Background(), in.client(), s, prune, &out)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	return upsctl.DiffExitCode(changed, err), out.String()
}

func ucApply(t *testing.T, in ucInstance, s *upsctl.Spec, prune bool) (upsctl.Result, string) {
	t.Helper()
	var out bytes.Buffer
	res, err := upsctl.Apply(context.Background(), in.client(), s, upsctl.ApplyOptions{Prune: prune, Out: &out})
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out.String())
	}
	return res, out.String()
}

func (in ucInstance) monitorNames(t *testing.T) []string {
	t.Helper()
	ms, err := in.client().Monitors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range ms {
		names = append(names, m.Name)
	}
	slices.Sort(names)
	return names
}

// TestPhase7UpsctlDoneWhen: a setup exported from instance A, with secrets
// supplied through the environment, applied to a fresh instance B gives the
// same setup; re-applying is quiet, and edits and prunes behave.
func TestPhase7UpsctlDoneWhen(t *testing.T) {
	tg := &ucTelegram{}
	tgSrv := httptest.NewServer(http.HandlerFunc(tg.serve))
	t.Cleanup(tgSrv.Close)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	t.Cleanup(target.Close)
	host, portStr, _ := net.SplitHostPort(strings.TrimPrefix(target.URL, "http://"))
	port, _ := strconv.Atoi(portStr)
	tweak := func(c *config.Config) { c.TelegramAPIURL = tgSrv.URL }

	// (1) Instance A, set up through its API.
	baseA, _ := startServer(t, testutil.PostgresURL(t), tweak)
	a := ucSetup(t, baseA)
	chID := a.post(t, "/channels", map[string]any{
		"type": "telegram", "name": "oncall", "is_default": true,
		"config": map[string]any{"chat_id": "1001", "bot_token": ucBotToken},
	})
	polID := a.post(t, "/escalation-policies", map[string]any{
		"name": "default", "steps": []map[string]any{{"channel_ids": []int64{chID}, "delay_s": 300}},
	})
	dbID := a.post(t, "/monitors", map[string]any{
		"name": "db", "type": "tcp", "interval_s": 60, "config": map[string]any{"host": host, "port": port},
		"tags": []string{"core"},
	})
	apiID := a.post(t, "/monitors", map[string]any{
		"name": "api", "type": "http", "interval_s": 60, "parent_id": dbID, "escalation_policy_id": polID,
		"group_name": "edge", "config": map[string]any{"url": target.URL + "/health"},
	})
	// One SLO with a non-default window and default burn rates.
	call(t, http.DefaultClient, "PUT", fmt.Sprintf("%s/monitors/%d/slo", a.teamURL, apiID), "Bearer "+a.token,
		map[string]any{"target": 99.95, "window_days": 28}, http.StatusOK, nil)
	a.post(t, "/status-pages", map[string]any{
		"slug": "public", "title": "Public status", "published": true,
		"items": []map[string]any{
			{"section": "Core", "monitor_id": apiID, "display_name": "API"},
			{"section": "Core", "monitor_id": dbID},
		},
	})

	// (2) Export A: the secret is a placeholder, never the value.
	yamlA := ucExport(t, a)
	if !strings.Contains(yamlA, "bot_token: ${UPSERA_ONCALL_BOT_TOKEN}") {
		t.Fatalf("export lacks the bot token placeholder:\n%s", yamlA)
	}
	if strings.Contains(yamlA, ucBotToken) || strings.Contains(yamlA, "424242:") {
		t.Fatal("export contains the plaintext bot token")
	}
	// The SLO is exported with all four fields, for that monitor only.
	if n := strings.Count(yamlA, "slo:"); n != 1 {
		t.Fatalf("export has %d slo blocks:\n%s", n, yamlA)
	}
	if !strings.Contains(yamlA, "  slo:\n    fast_burn: 14.4\n    slow_burn: 6\n    target: 99.95\n    window_days: 28\n") {
		t.Fatalf("export lacks the explicit slo block:\n%s", yamlA)
	}
	if i, j := strings.Index(yamlA, "name: api"), strings.Index(yamlA, "name: db"); !(i < strings.Index(yamlA, "slo:") && strings.Index(yamlA, "slo:") < j) {
		t.Fatalf("the slo block is not on monitor api:\n%s", yamlA)
	}
	for _, w := range []string{"parent: db", "escalation_policy: default", "display_name: API", "- monitor: db", "slug: public"} {
		if !strings.Contains(yamlA, w) {
			t.Fatalf("export lacks %q:\n%s", w, yamlA)
		}
	}

	// (3) A fresh instance B, the secret from the environment.
	baseB, _ := startServer(t, testutil.PostgresURL(t), tweak)
	b := ucSetup(t, baseB)
	t.Setenv("UPSERA_ONCALL_BOT_TOKEN", ucBotToken)
	spec := ucParse(t, yamlA)
	res, out := ucApply(t, b, spec, false)
	if res.Created != 6 || res.Updated != 0 || res.Deleted != 0 {
		t.Fatalf("apply to B: %+v\n%s", res, out)
	}

	// (4) B exports the same spec (exports have no ids, timestamps or
	// instance URLs, so only trailing whitespace is normalised).
	if yamlB := ucExport(t, b); strings.TrimSpace(yamlB) != strings.TrimSpace(yamlA) {
		t.Fatalf("export of B differs from A\n--- A\n%s\n--- B\n%s", yamlA, yamlB)
	}

	// (5) Nothing left to do: diff exits 0, a second apply writes nothing.
	if code, out := ucDiff(t, b, spec, false); code != upsctl.ExitNoChanges || !strings.Contains(out, "No changes.") ||
		!strings.Contains(out, `channel "oncall": secrets not compared`) {
		t.Fatalf("diff after apply: exit %d\n%s", code, out)
	}
	if got := b.getSLO(t, "api", http.StatusOK).SLO; got.Target != 99.95 || got.WindowDays != 28 || got.FastBurn != 14.4 || got.SlowBurn != 6 {
		t.Fatalf("slo on B = %+v", got)
	}
	audits := b.auditCount(t)
	sloSets := ucCount(b.auditActions(t), "slo.set")
	if res, out := ucApply(t, b, spec, false); res.Changed() {
		t.Fatalf("second apply changed something: %+v\n%s", res, out)
	}
	if n := b.auditCount(t); n != audits {
		t.Fatalf("second apply wrote %d audit entries", n-audits)
	}
	if n := ucCount(b.auditActions(t), "slo.set"); n != sloSets {
		t.Fatalf("second apply set the slo %d time(s)", n-sloSets)
	}

	// (6) The secret reached B: a test send uses the right bot token.
	chs, err := b.client().Channels(context.Background())
	if err != nil || len(chs) != 1 {
		t.Fatalf("channels on B: %v %d", err, len(chs))
	}
	before := len(tg.sent())
	call(t, http.DefaultClient, "POST", fmt.Sprintf("%s/channels/%d/test", b.teamURL, chs[0].ID), "Bearer "+b.token, nil, http.StatusOK, nil)
	sent := tg.sent()[before:]
	if len(sent) != 1 || sent[0] != ucBotToken {
		t.Fatalf("test send: %d message(s), right bot token: %v", len(sent), len(sent) == 1 && sent[0] == ucBotToken)
	}

	// (7) Editing an interval shows exactly that update.
	edited := ucParse(t, yamlA)
	for i := range edited.Monitors {
		if edited.Monitors[i].Name == "api" {
			edited.Monitors[i].IntervalS = new(120)
		}
	}
	code, out := ucDiff(t, b, edited, false)
	want := "  channel \"oncall\": secrets not compared\n~ update monitor \"api\": interval_s\nPlan: 0 to create, 1 to update, 0 to delete.\n"
	if code != upsctl.ExitChanges || out != want {
		t.Fatalf("diff after edit: exit %d\n%s\nwant:\n%s", code, out, want)
	}

	// (7b) Changing the SLO target shows exactly that update and applies it.
	retarget := ucParse(t, yamlA)
	for i := range retarget.Monitors {
		if retarget.Monitors[i].Name == "api" {
			retarget.Monitors[i].SLO.Target = new(99.9)
		}
	}
	code, out = ucDiff(t, b, retarget, false)
	want = "  channel \"oncall\": secrets not compared\n~ update monitor \"api\": slo target 99.95 -> 99.9\nPlan: 0 to create, 1 to update, 0 to delete.\n"
	if code != upsctl.ExitChanges || out != want {
		t.Fatalf("diff after slo edit: exit %d\n%s\nwant:\n%s", code, out, want)
	}
	if res, out := ucApply(t, b, retarget, false); res.Updated != 1 || !strings.Contains(out, `updated monitor "api" slo (target 99.95 -> 99.9)`) {
		t.Fatalf("apply slo edit: %+v\n%s", res, out)
	}
	got := b.getSLO(t, "api", http.StatusOK)
	if got.SLO.Target != 99.9 || got.SLO.WindowDays != 28 || got.Status.Firing != "none" {
		t.Fatalf("slo after edit = %+v", got)
	}
	if code, out := ucDiff(t, b, retarget, false); code != upsctl.ExitNoChanges {
		t.Fatalf("diff after slo apply: exit %d\n%s", code, out)
	}

	// (7c) Removing the slo block: plain apply keeps the SLO, --prune
	// deletes it.
	noSLO := ucParse(t, yamlA)
	for i := range noSLO.Monitors {
		noSLO.Monitors[i].SLO = nil
	}
	res, out = ucApply(t, b, noSLO, false)
	if res.Changed() || !strings.Contains(out, `not managed, kept: monitor "api" slo`) {
		t.Fatalf("apply without slo: %+v\n%s", res, out)
	}
	b.getSLO(t, "api", http.StatusOK)
	res, out = ucApply(t, b, noSLO, true)
	if res.Deleted != 1 || !strings.Contains(out, `deleted monitor "api" slo`) {
		t.Fatalf("apply --prune without slo: %+v\n%s", res, out)
	}
	b.getSLO(t, "api", http.StatusNotFound)

	// (8) Removing a monitor: plain apply keeps it, --prune deletes it.
	removed := ucParse(t, yamlA)
	removed.Monitors = slices.DeleteFunc(removed.Monitors, func(m upsctl.MonitorSpec) bool { return m.Name == "api" })
	for i := range removed.StatusPages {
		for j := range removed.StatusPages[i].Sections {
			sec := &removed.StatusPages[i].Sections[j]
			sec.Items = slices.DeleteFunc(sec.Items, func(it upsctl.ItemSpec) bool { return it.Monitor == "api" })
		}
	}
	res, out = ucApply(t, b, removed, false)
	if res.Deleted != 0 || !strings.Contains(out, `not managed, kept: monitor "api"`) {
		t.Fatalf("apply without prune: %+v\n%s", res, out)
	}
	if got := b.monitorNames(t); !slices.Equal(got, []string{"api", "db"}) {
		t.Fatalf("monitors after plain apply = %q", got)
	}
	res, out = ucApply(t, b, removed, true)
	if res.Deleted != 1 || !strings.Contains(out, `deleted monitor "api"`) {
		t.Fatalf("apply --prune: %+v\n%s", res, out)
	}
	if got := b.monitorNames(t); !slices.Equal(got, []string{"db"}) {
		t.Fatalf("monitors after prune = %q", got)
	}
	// A is untouched by all of this.
	if got := a.monitorNames(t); !slices.Equal(got, []string{"api", "db"}) {
		t.Fatalf("monitors on A = %q", got)
	}
}

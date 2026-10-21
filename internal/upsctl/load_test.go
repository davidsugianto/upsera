package upsctl_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davidsugianto/upsera/internal/upsctl"
)

func env(vars map[string]string) upsctl.LookupFunc {
	return func(name string) (string, bool) {
		v, ok := vars[name]
		return v, ok
	}
}

func mustParse(t *testing.T, yaml string, lookup upsctl.LookupFunc) *upsctl.Spec {
	t.Helper()
	s, err := upsctl.Parse("spec.yaml", []byte(yaml), lookup)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return s
}

func parseErr(t *testing.T, yaml string, lookup upsctl.LookupFunc) string {
	t.Helper()
	_, err := upsctl.Parse("spec.yaml", []byte(yaml), lookup)
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestParseStrict(t *testing.T) {
	s := mustParse(t, `
version: 1
channels:
  - name: oncall
    type: telegram
    config: {chat_id: "42", anything_goes_in_config: {nested: true}}
monitors:
  - name: api
    type: http
    interval_s: 30
    config: {url: https://example.com}
`, nil)
	if len(s.Channels) != 1 || len(s.Monitors) != 1 || *s.Monitors[0].IntervalS != 30 {
		t.Fatalf("spec = %+v", s)
	}

	for _, tc := range []struct{ name, yaml, want string }{
		{"top-level key", "version: 1\nmonitor: []\n", "spec.yaml: monitor: unknown field"},
		{"nested key with path", "version: 1\nmonitors:\n  - name: a\n    type: tcp\n    intervl_s: 30\n", "spec.yaml: monitors[0].intervl_s: unknown field"},
		{"step key", "version: 1\nescalation_policies:\n  - name: p\n    steps:\n      - channels: [a]\n        delay: 5\n", "escalation_policies[0].steps[0].delay: unknown field"},
		{"page item key", "version: 1\nstatus_pages:\n  - slug: s\n    title: S\n    sections:\n      - items:\n          - monitor_id: 3\n", "status_pages[0].sections[0].items[0].monitor_id: unknown field"},
		{"slo key", "version: 1\nmonitors:\n  - name: a\n    type: tcp\n    slo: {target: 99.9, budget: 1}\n", "spec.yaml: monitors[0].slo.budget: unknown field"},
		{"missing version", "monitors: []\n", "version must be 1"},
		{"wrong version", "version: 2\n", "version must be 1"},
		{"duplicate yaml key", "version: 1\nversion: 1\n", "spec.yaml"},
		{"wrong type", "version: 1\nmonitors:\n  - name: a\n    interval_s: soon\n", "interval_s"},
		{"not a mapping", "- a\n", "mapping"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseErr(t, tc.yaml, nil)
			if got == "" || !strings.Contains(got, tc.want) {
				t.Fatalf("error = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestExpand(t *testing.T) {
	lookup := env(map[string]string{"UPSERA_SECRET": "s3cr3t-value", "UPSERA_HOST": "db.internal", "UPSERA_EMPTY": ""})
	for in, want := range map[string]string{
		"${UPSERA_SECRET}":                  "s3cr3t-value",
		"pre-${UPSERA_HOST}:5432":           "pre-db.internal:5432",
		"$$":                                "$",
		"$${UPSERA_SECRET}":                 "${UPSERA_SECRET}",
		"cost $5":                           "cost $5",
		"$":                                 "$",
		"a$b":                               "a$b",
		"${UPSERA_EMPTY}x":                  "x",
		"$$$${UPSERA_HOST}":                 "$${UPSERA_HOST}",
		"$$$$${UPSERA_HOST}":                "$$db.internal",
		"no dollars at all":                 "no dollars at all",
		"${UPSERA_HOST}${UPSERA_SECRET}end": "db.internals3cr3t-valueend",
	} {
		got, err := upsctl.Expand(in, lookup)
		if err != nil || got != want {
			t.Errorf("Expand(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"${MISSING}", "${unterminated", "${bad-name}", "${}"} {
		if _, err := upsctl.Expand(in, lookup); err == nil {
			t.Errorf("Expand(%q): want an error", in)
		}
	}

	// In a spec: values only, never keys; errors name the variable and the
	// YAML path and never print values.
	s := mustParse(t, `
version: 1
channels:
  - name: oncall
    type: telegram
    config:
      bot_token: ${UPSERA_SECRET}
      ${UPSERA_HOST}: literal-key
      price: "$$5"
`, lookup)
	cfg := string(s.Channels[0].Config)
	if !strings.Contains(cfg, `"bot_token":"s3cr3t-value"`) || !strings.Contains(cfg, `"${UPSERA_HOST}":"literal-key"`) || !strings.Contains(cfg, `"price":"$5"`) {
		t.Fatalf("expanded config = %s", cfg)
	}
	got := parseErr(t, `
version: 1
monitors:
  - name: api
    type: http
    config:
      url: https://${UPSERA_HOST}/health
      headers: {Authorization: "Bearer ${UPSERA_API_TOKEN}"}
`, lookup)
	if !strings.Contains(got, "monitors[0].config.headers.Authorization: environment variable UPSERA_API_TOKEN is not set") {
		t.Fatalf("error = %q", got)
	}
	if strings.Contains(got, "db.internal") || strings.Contains(got, "s3cr3t") {
		t.Fatalf("error leaks a value: %q", got)
	}
	// Without a lookup (export-side parsing) placeholders stay as written.
	s = mustParse(t, "version: 1\nchannels:\n  - {name: a, type: telegram, config: {bot_token: '${NOPE}'}}\n", nil)
	if !strings.Contains(string(s.Channels[0].Config), "${NOPE}") {
		t.Fatalf("config = %s", s.Channels[0].Config)
	}
}

func TestLoadFilesMerge(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := write("a.yaml", `
version: 1
channels:
  - {name: oncall, type: telegram, config: {chat_id: "1", bot_token: "${UPSERA_BOT}"}}
monitors:
  - {name: db, type: tcp, config: {host: db, port: 5432}}
`)
	b := write("b.yaml", `
version: 1
escalation_policies:
  - name: default
    steps: [{channels: [oncall]}]
monitors:
  - {name: api, type: tcp, parent: db, escalation_policy: default, config: {host: api, port: 443}}
`)
	s, err := upsctl.LoadFiles([]string{a, b}, env(map[string]string{"UPSERA_BOT": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Channels) != 1 || len(s.EscalationPolicies) != 1 || len(s.Monitors) != 2 || s.Monitors[1].Parent != "db" {
		t.Fatalf("merged = %+v", s)
	}

	// A reference across files resolves; a duplicate across files is an
	// error naming both files.
	c := write("c.yaml", "version: 1\nmonitors:\n  - {name: db, type: tcp, config: {host: x, port: 1}}\n")
	_, err = upsctl.LoadFiles([]string{a, b, c}, env(map[string]string{"UPSERA_BOT": "x"}))
	if err == nil || !strings.Contains(err.Error(), `monitor "db" is defined in both `+a+" and "+c) {
		t.Fatalf("err = %v", err)
	}
	// Unset variable in the second file.
	_, err = upsctl.LoadFiles([]string{a, b}, env(nil))
	if err == nil || !strings.Contains(err.Error(), a+": channels[0].config.bot_token: environment variable UPSERA_BOT is not set") {
		t.Fatalf("err = %v", err)
	}
	if _, err := upsctl.LoadFiles(nil, env(nil)); err == nil {
		t.Fatal("no files: want an error")
	}
}

func TestValidate(t *testing.T) {
	problems := func(yaml string) []string {
		t.Helper()
		s := mustParse(t, yaml, nil)
		err := upsctl.Validate(s)
		var p upsctl.Problems
		if err == nil {
			return nil
		}
		if !errors.As(err, &p) {
			t.Fatalf("err = %T %v", err, err)
		}
		return p
	}
	has := func(t *testing.T, got []string, want ...string) {
		t.Helper()
		for _, w := range want {
			found := false
			for _, g := range got {
				if strings.Contains(g, w) {
					found = true
				}
			}
			if !found {
				t.Errorf("problems %q: missing %q", got, w)
			}
		}
	}

	t.Run("valid", func(t *testing.T) {
		if p := problems(`
version: 1
channels: [{name: c, type: slack, config: {webhook_url: x}}]
escalation_policies: [{name: p, steps: [{channels: [c]}]}]
monitors:
  - {name: child, type: tcp, parent: root, channels: [c], escalation_policy: p, config: {host: a, port: 1}}
  - {name: root, type: tcp, config: {host: a, port: 1}}
  - {name: box, type: docker, config: {docker_host: local, container: web}}
status_pages: [{slug: s, title: S, sections: [{name: Core, items: [{monitor: root}, {monitor: child}]}]}]
`); p != nil {
			t.Fatalf("problems = %q", p)
		}
	})

	t.Run("duplicates", func(t *testing.T) {
		has(t, problems(`
version: 1
channels: [{name: c, type: slack}, {name: c, type: slack}]
escalation_policies: [{name: p, steps: [{channels: [c]}]}, {name: p, steps: [{channels: [c]}]}]
monitors: [{name: m1, type: tcp}, {name: m1, type: tcp}, {name: m2, type: tcp}, {name: m2, type: tcp}]
status_pages: [{slug: s, title: S}, {slug: s, title: S}]
`), `duplicate channel names in the spec: "c"`, `duplicate escalation policy names in the spec: "p"`,
			`duplicate monitor names in the spec: "m1", "m2"`, `duplicate status page slug names in the spec: "s"`)
	})

	t.Run("unknown references", func(t *testing.T) {
		has(t, problems(`
version: 1
escalation_policies: [{name: p, steps: [{channels: [ghost]}]}]
monitors:
  - {name: m, type: tcp, parent: nobody, escalation_policy: nopolicy, channels: [nochan]}
status_pages: [{slug: s, title: S, sections: [{items: [{monitor: missing}, {monitor: m}, {monitor: m}]}]}]
`), `escalation_policies[0].steps[0] (p): unknown channel "ghost"`, `monitors[0] (m): unknown parent monitor "nobody"`,
			`unknown escalation policy "nopolicy"`, `unknown channel "nochan"`, `unknown monitor "missing"`, `monitor "m" is listed twice`)
	})

	t.Run("parent cycle", func(t *testing.T) {
		has(t, problems(`
version: 1
monitors:
  - {name: a, type: tcp, parent: c}
  - {name: b, type: tcp, parent: a}
  - {name: c, type: tcp, parent: b}
  - {name: self, type: tcp, parent: self}
`), "monitor parent cycle: a -> c -> b -> a", "monitors[3] (self): a monitor cannot be its own parent")
	})

	t.Run("types and fields", func(t *testing.T) {
		has(t, problems(`
version: 1
channels: [{name: c, type: pager}, {name: " padded", type: slack}, {type: slack}]
monitors:
  - {name: m, type: carrier-pigeon}
  - {name: d, type: docker, config: {docker_host_id: 3, container: web}}
  - {name: h, type: http, config: {docker_host: local}}
`), "channels[0] (c): type must be one of", `name " padded" has leading or trailing spaces`, "channels[2]: name is required",
			"monitors[0] (m): type must be one of", "config.docker_host_id is not allowed", "config.docker_host (the Docker host's name) is required",
			"config.docker_host is only allowed on docker monitors")
	})
}

// TestExampleSpec keeps deploy/examples/upsera.yaml a valid spec.
func TestExampleSpec(t *testing.T) {
	s, err := upsctl.LoadFiles([]string{"../../deploy/examples/upsera.yaml"},
		env(map[string]string{"UPSERA_ONCALL_TELEGRAM_BOT_TOKEN": "123456:x"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Channels) != 1 || len(s.EscalationPolicies) != 1 || len(s.Monitors) != 2 || len(s.StatusPages) != 1 || s.Monitors[1].Parent != "database" || s.Monitors[1].SLO == nil {
		t.Fatalf("example = %+v", s)
	}
}

// A spec is code under review, but it still must not be able to read the
// CI job's other variables (CI_JOB_TOKEN, cloud credentials, the API token
// itself) into names that diff prints or into settings that apply sends.
func TestExpandOnlyAllowsUpseraVariables(t *testing.T) {
	lookup := env(map[string]string{
		"UPSERA_ONCALL_BOT_TOKEN": "bot-value", "UPSERA_TOKEN": "api-token-value", "UPSERA_URL": "https://x.example",
		"CI_JOB_TOKEN": "job-token-value", "HOME": "/root", "UPSERA": "bare", "UPSERA2_X": "other",
	})
	if got, err := upsctl.Expand("${UPSERA_ONCALL_BOT_TOKEN}", lookup); err != nil || got != "bot-value" {
		t.Fatalf("UPSERA_ variable: got %q, %v", got, err)
	}
	for _, name := range []string{"CI_JOB_TOKEN", "HOME", "UPSERA_TOKEN", "UPSERA_URL", "UPSERA", "UPSERA2_X", "upsera_x", "NOT_SET"} {
		got, err := upsctl.Expand("${"+name+"}", lookup)
		if err == nil {
			t.Errorf("Expand(${%s}) = %q, want an error", name, got)
			continue
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error for %s does not name the variable: %v", name, err)
		}
		for _, secret := range []string{"api-token-value", "job-token-value", "bot-value", "/root"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error for %s leaks a value: %v", name, err)
			}
		}
	}
	// A forbidden name is refused before it is looked up, so the message
	// does not reveal whether it is set.
	_, setErr := upsctl.Expand("${CI_JOB_TOKEN}", lookup)
	_, unsetErr := upsctl.Expand("${CI_NOT_SET}", lookup)
	if setErr == nil || unsetErr == nil || strings.ReplaceAll(setErr.Error(), "CI_JOB_TOKEN", "X") != strings.ReplaceAll(unsetErr.Error(), "CI_NOT_SET", "X") {
		t.Errorf("forbidden names must give the same message whether set or not: %v / %v", setErr, unsetErr)
	}
}

func TestParseRejectsForbiddenVariableWithPath(t *testing.T) {
	msg := parseErr(t, "version: 1\nmonitors:\n  - name: ${CI_JOB_TOKEN}\n    type: tcp\n    config: {host: a, port: 1}\n",
		env(map[string]string{"CI_JOB_TOKEN": "job-token-value"}))
	if !strings.Contains(msg, "monitors[0].name") || !strings.Contains(msg, "CI_JOB_TOKEN") || strings.Contains(msg, "job-token-value") {
		t.Fatalf("error = %q", msg)
	}
}

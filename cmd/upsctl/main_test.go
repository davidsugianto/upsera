package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	code := run(context.Background(), args, strings.NewReader(""), &out, &errb, lookup)
	return code, out.String(), errb.String()
}

func TestCLI(t *testing.T) {
	if code, out, _ := runCLI(t, nil, "version"); code != 0 || out != "upsctl dev\n" {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, out, _ := runCLI(t, nil, "--help"); code != 0 || !strings.Contains(out, "push URL") || !strings.Contains(out, "${UPSERA_<CHANNEL>_<FIELD>}") || !strings.Contains(out, "slo: {target: 99.9") {
		t.Fatalf("help: %d\n%s", code, out)
	}

	dir := t.TempDir()
	spec := filepath.Join(dir, "spec.yaml")
	if err := os.WriteFile(spec, []byte("version: 1\nmonitors:\n  - {name: a, type: tcp, slo: {target: 99.95, window_days: 3}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCLI(t, nil, "diff", "-f", spec)
	if code != 1 || !strings.Contains(errOut, "monitors[0] (a): slo.window_days must be between 7 and 90") {
		t.Fatalf("slo: %d %q", code, errOut)
	}

	if err := os.WriteFile(spec, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = runCLI(t, nil, "apply", "-f", spec)
	if code != 1 || !strings.Contains(errOut, "UPSERA_URL") {
		t.Fatalf("no url: %d %q", code, errOut)
	}
	const token = "ups_dGhpcy1pcy1hLXRlc3QtdG9rZW4tdmFsdWU"
	code, _, errOut = runCLI(t, map[string]string{"UPSERA_URL": "http://127.0.0.1:1", "UPSERA_TOKEN": token}, "diff", "-f", spec)
	if code != 1 || strings.Contains(errOut, token) {
		t.Fatalf("unreachable server: %d %q", code, errOut)
	}
	if code, _, _ := runCLI(t, nil, "diff"); code != 1 {
		t.Fatalf("diff without -f: %d", code)
	}
}

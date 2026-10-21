package upsctl_test

import (
	"os"
	"regexp"
	"testing"

	"github.com/davidsugianto/upsera/internal/upsctl"
)

// The GitLab example maps its CI variables onto UPSERA_TOKEN. Those source
// variables hold API tokens, so a spec must not be able to expand them.
func TestGitLabExampleTokenVariablesAreNotExpandable(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/examples/upsctl.gitlab-ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s+UPSERA_TOKEN:\s*\$(\w+)\s*$`).FindAllStringSubmatch(string(raw), -1)
	if len(m) != 2 {
		t.Fatalf("found %d `UPSERA_TOKEN: $VAR` mappings in the example, want 2 (the diff job's read token and the apply job's write token); "+
			"if you added or removed a job that runs upsctl, update this count and make sure its token variable is named outside UPSERA_*", len(m))
	}
	lookup := func(string) (string, bool) { return "token-value", true }
	for _, g := range m {
		if got, err := upsctl.Expand("${"+g[1]+"}", lookup); err == nil {
			t.Errorf("a spec could read the token variable %s (expands to %q); name it outside UPSERA_*", g[1], got)
		}
	}
}

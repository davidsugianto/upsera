package statuspage

import (
	"regexp"
	"strings"
	"testing"
)

// cssBlock returns the declarations of the first rule that starts with open, e.g. `:root{`.
func cssBlock(t *testing.T, css, open string) string {
	t.Helper()
	i := strings.Index(css, open)
	if i < 0 {
		t.Fatalf("no %q rule in stylesheet", open)
	}
	rest := css[i+len(open):]
	j := strings.Index(rest, "}")
	if j < 0 {
		t.Fatalf("unterminated %q rule", open)
	}
	return rest[:j]
}

func cssHex(t *testing.T, block, name string) string {
	t.Helper()
	m := regexp.MustCompile(`--` + regexp.QuoteMeta(name) + `:\s*(#[0-9A-Fa-f]{6})`).FindStringSubmatch(block)
	if m == nil {
		t.Fatalf("token --%s missing in %q", name, block)
	}
	return strings.ToUpper(m[1])
}

// The confirmation page is a separate, tiny stylesheet; it must keep using the status page's palette.
func TestResultPagePaletteMatchesStatusPage(t *testing.T) {
	e := newSubscribeTestEnv(t)
	page := e.get("/status/nope/confirm?t=x").Body.String()

	// result page token -> status page token
	pairs := []struct{ result, status string }{
		{"bg", "bg"}, {"surface", "surface"}, {"ink", "ink"}, {"muted", "muted"}, {"line", "line"},
		{"brand", "accent"}, {"link", "accent-ink"},
	}
	light := map[string]string{}
	for _, p := range pairs {
		light[p.result] = cssHex(t, cssBlock(t, statusCSS, ":root{"), p.status)
	}
	dark := cssBlock(t, statusCSS, `:root[data-theme="dark"]{`)
	darkPage := cssBlock(t, page, "@media (prefers-color-scheme:dark){:root{")
	lightPage := cssBlock(t, page, ":root{")

	for _, p := range pairs {
		if got := cssHex(t, lightPage, p.result); got != light[p.result] {
			t.Errorf("light --%s = %s, status page --%s = %s", p.result, got, p.status, light[p.result])
		}
	}
	// The dark rule only overrides what changes in dark; brand stays the same pink.
	for _, p := range pairs {
		if p.result == "brand" {
			continue
		}
		want := cssHex(t, dark, p.status)
		if got := cssHex(t, darkPage, p.result); got != want {
			t.Errorf("dark --%s = %s, status page --%s = %s", p.result, got, p.status, want)
		}
	}
}

func TestResultPageUsesStatusPageRadius(t *testing.T) {
	e := newSubscribeTestEnv(t)
	page := e.get("/status/nope/confirm?t=x").Body.String()
	if strings.Contains(page, "border-radius:12px") {
		t.Error("confirmation page still uses the old 12px card radius")
	}
}

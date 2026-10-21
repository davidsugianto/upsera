package upsctl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// LookupFunc resolves an environment variable (os.LookupEnv in the CLI).
type LookupFunc func(name string) (string, bool)

// Problems is a list of spec errors, reported together before anything is
// written.
type Problems []string

func (p Problems) Error() string {
	if len(p) == 1 {
		return p[0]
	}
	return fmt.Sprintf("%d problems:\n  - %s", len(p), strings.Join(p, "\n  - "))
}

// LoadFiles reads, parses, expands and merges the spec files in paths, then
// validates the result.
func LoadFiles(paths []string, lookup LookupFunc) (*Spec, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no spec file given (use -f)")
	}
	specs := make([]*Spec, len(paths))
	for i, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if specs[i], err = Parse(p, data, lookup); err != nil {
			return nil, err
		}
	}
	merged, err := Merge(paths, specs)
	if err != nil {
		return nil, err
	}
	if err := Validate(merged); err != nil {
		return nil, err
	}
	return merged, nil
}

// Parse decodes one YAML spec strictly: an unknown key is an error naming
// its path. When lookup is non-nil, ${VAR} placeholders in string values
// are expanded after parsing ($$ is a literal $).
func Parse(name string, data []byte, lookup LookupFunc) (*Spec, error) {
	j, err := yaml.YAMLToJSONStrict(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var tree any
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.UseNumber()
	if err := dec.Decode(&tree); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if tree == nil {
		return nil, fmt.Errorf("%s: empty spec", name)
	}
	if _, ok := tree.(map[string]any); !ok {
		return nil, fmt.Errorf("%s: a spec must be a mapping with a version key", name)
	}
	var probs Problems
	for _, e := range checkKeys(tree, reflect.TypeFor[Spec](), "") {
		probs = append(probs, name+": "+e)
	}
	if len(probs) > 0 {
		return nil, probs
	}
	if lookup != nil {
		var errs []string
		tree = expandTree(tree, "", lookup, &errs)
		for _, e := range errs {
			probs = append(probs, name+": "+e)
		}
		if len(probs) > 0 {
			return nil, probs
		}
	}
	j, err = json.Marshal(tree)
	if err != nil {
		return nil, err
	}
	var s Spec
	dec = json.NewDecoder(bytes.NewReader(j))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %s", name, strings.TrimPrefix(err.Error(), "json: "))
	}
	if s.Version != SpecVersion {
		return nil, fmt.Errorf("%s: version must be %d", name, SpecVersion)
	}
	return &s, nil
}

// expandTree replaces ${VAR} in every string value (never in keys).
func expandTree(v any, path string, lookup LookupFunc, errs *[]string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = expandTree(e, joinPath(path, k), lookup, errs)
		}
	case []any:
		for i, e := range x {
			x[i] = expandTree(e, fmt.Sprintf("%s[%d]", path, i), lookup, errs)
		}
	case string:
		s, err := Expand(x, lookup)
		if err != nil {
			*errs = append(*errs, path+": "+err.Error())
			return x
		}
		return s
	}
	return v
}

// Expand replaces ${NAME} in s with the variable's value. $$ is a literal
// $; a $ followed by anything else is kept as is. Only UPSERA_* variables
// may be referenced (every placeholder export writes is one), and never
// UPSERA_TOKEN or UPSERA_URL, so a spec cannot read the rest of a CI job's
// environment, or the API token, into names that diff prints or settings
// that apply sends. Errors name the variable, never a value.
func Expand(s string, lookup LookupFunc) (string, error) {
	if !strings.Contains(s, "$") {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '$' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		switch s[i+1] {
		case '$':
			b.WriteByte('$')
			i++
		case '{':
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				return "", fmt.Errorf("unterminated ${ (write $$ for a literal $)")
			}
			name := s[i+2 : i+2+end]
			if !validVarName(name) {
				return "", fmt.Errorf("invalid variable name %q in ${...}", name)
			}
			if !allowedVar(name) {
				return "", fmt.Errorf("variable %s cannot be used in a spec: only UPSERA_* variables other than %s and %s may be referenced",
					name, connTokenVar, connURLVar)
			}
			val, ok := lookup(name)
			if !ok {
				return "", fmt.Errorf("environment variable %s is not set", name)
			}
			b.WriteString(val)
			i += 2 + end
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), nil
}

// Connection settings of the CLI itself; never available to a spec.
const (
	connTokenVar = "UPSERA_TOKEN"
	connURLVar   = "UPSERA_URL"
)

// allowedVar reports whether a spec may reference environment variable name.
// It is checked before the lookup, so the message is the same whether the
// variable is set or not.
func allowedVar(name string) bool {
	return strings.HasPrefix(name, "UPSERA_") && name != connTokenVar && name != connURLVar
}

func validVarName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

// Merge concatenates specs read from files (same order). An entry defined
// in two files is an error naming both.
func Merge(files []string, specs []*Spec) (*Spec, error) {
	out := &Spec{Version: SpecVersion}
	var probs Problems
	seen := map[string]string{}
	claim := func(kind, name string, file string) {
		key := kind + "\x00" + name
		if prev, ok := seen[key]; ok && prev != file {
			probs = append(probs, fmt.Sprintf("%s %q is defined in both %s and %s", kind, name, prev, file))
			return
		}
		seen[key] = file
	}
	for i, s := range specs {
		f := files[i]
		for _, c := range s.Channels {
			claim("channel", c.Name, f)
		}
		for _, p := range s.EscalationPolicies {
			claim("escalation policy", p.Name, f)
		}
		for _, m := range s.Monitors {
			claim("monitor", m.Name, f)
		}
		for _, p := range s.StatusPages {
			claim("status page", p.Slug, f)
		}
		out.Channels = append(out.Channels, s.Channels...)
		out.EscalationPolicies = append(out.EscalationPolicies, s.EscalationPolicies...)
		out.Monitors = append(out.Monitors, s.Monitors...)
		out.StatusPages = append(out.StatusPages, s.StatusPages...)
	}
	if len(probs) > 0 {
		return nil, probs
	}
	return out, nil
}

// Validate checks a spec on its own: names present and unique, types
// known, every reference defined in the spec, no parent cycle. Checks that
// need the live instance (regions, Docker hosts) happen in Plan.
func Validate(s *Spec) error {
	var probs Problems
	add := func(format string, args ...any) { probs = append(probs, fmt.Sprintf(format, args...)) }

	if s.Version != SpecVersion {
		add("version must be %d", SpecVersion)
	}
	checkName := func(path, name string) {
		switch {
		case name == "":
			add("%s: name is required", path)
		case strings.TrimSpace(name) != name:
			add("%s: name %q has leading or trailing spaces", path, name)
		}
	}
	dups := func(kind string, names []string) {
		count := map[string]int{}
		for _, n := range names {
			count[n]++
		}
		var d []string
		for n, c := range count {
			if c > 1 && n != "" {
				d = append(d, fmt.Sprintf("%q", n))
			}
		}
		if len(d) > 0 {
			sort.Strings(d)
			add("duplicate %s names in the spec: %s", kind, strings.Join(d, ", "))
		}
	}

	channels := map[string]bool{}
	var names []string
	for i, c := range s.Channels {
		path := fmt.Sprintf("channels[%d]", i)
		checkName(path, c.Name)
		channels[c.Name] = true
		names = append(names, c.Name)
		if !slices.Contains(ChannelTypes, c.Type) {
			add("%s (%s): type must be one of %s", path, c.Name, strings.Join(ChannelTypes, ", "))
		}
		if !isObjectOrEmpty(c.Config) {
			add("%s (%s): config must be a mapping", path, c.Name)
		}
	}
	dups("channel", names)

	policies := map[string]bool{}
	names = nil
	for i, p := range s.EscalationPolicies {
		path := fmt.Sprintf("escalation_policies[%d]", i)
		checkName(path, p.Name)
		policies[p.Name] = true
		names = append(names, p.Name)
		if len(p.Steps) == 0 {
			add("%s (%s): at least one step is required", path, p.Name)
		}
		for j, st := range p.Steps {
			if len(st.Channels) == 0 {
				add("%s.steps[%d] (%s): at least one channel is required", path, j, p.Name)
			}
			for _, c := range st.Channels {
				if !channels[c] {
					add("%s.steps[%d] (%s): unknown channel %q", path, j, p.Name, c)
				}
			}
		}
	}
	dups("escalation policy", names)

	monitors := map[string]*MonitorSpec{}
	names = nil
	for i := range s.Monitors {
		m := &s.Monitors[i]
		path := fmt.Sprintf("monitors[%d]", i)
		checkName(path, m.Name)
		monitors[m.Name] = m
		names = append(names, m.Name)
	}
	dups("monitor", names)
	for i, m := range s.Monitors {
		path := fmt.Sprintf("monitors[%d] (%s)", i, m.Name)
		if !slices.Contains(MonitorTypes, m.Type) {
			add("%s: type must be one of %s", path, strings.Join(MonitorTypes, ", "))
		}
		if !isObjectOrEmpty(m.Config) {
			add("%s: config must be a mapping", path)
		} else {
			cfg := configMap(m.Config)
			_, hasHost := cfg["docker_host"]
			if _, ok := cfg["docker_host_id"]; ok {
				add("%s: config.docker_host_id is not allowed, name the host with config.docker_host", path)
			}
			if m.Type == "docker" {
				if h, ok := cfg["docker_host"].(string); !ok || h == "" {
					add("%s: config.docker_host (the Docker host's name) is required", path)
				}
			} else if hasHost {
				add("%s: config.docker_host is only allowed on docker monitors", path)
			}
		}
		if m.Parent != "" {
			if m.Parent == m.Name {
				add("%s: a monitor cannot be its own parent", path)
			} else if monitors[m.Parent] == nil {
				add("%s: unknown parent monitor %q", path, m.Parent)
			}
		}
		if m.EscalationPolicy != "" && !policies[m.EscalationPolicy] {
			add("%s: unknown escalation policy %q", path, m.EscalationPolicy)
		}
		for _, c := range m.Channels {
			if !channels[c] {
				add("%s: unknown channel %q", path, c)
			}
		}
		if m.SLO != nil {
			for _, p := range sloProblems(*m.SLO) {
				add("%s: slo.%s", path, p)
			}
		}
	}
	for _, cycle := range parentCycles(s.Monitors) {
		add("monitor parent cycle: %s", strings.Join(cycle, " -> "))
	}

	names = nil
	for i, p := range s.StatusPages {
		path := fmt.Sprintf("status_pages[%d]", i)
		if p.Slug == "" {
			add("%s: slug is required", path)
		}
		names = append(names, p.Slug)
		listed := map[string]bool{}
		for j, sec := range p.Sections {
			for k, it := range sec.Items {
				ipath := fmt.Sprintf("%s.sections[%d].items[%d] (%s)", path, j, k, p.Slug)
				switch {
				case monitors[it.Monitor] == nil:
					add("%s: unknown monitor %q", ipath, it.Monitor)
				case listed[it.Monitor]:
					add("%s: monitor %q is listed twice on the page", ipath, it.Monitor)
				}
				listed[it.Monitor] = true
			}
		}
	}
	dups("status page slug", names)

	if len(probs) > 0 {
		return probs
	}
	return nil
}

func isObjectOrEmpty(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return true
	}
	var m map[string]any
	return json.Unmarshal(t, &m) == nil
}

// configMap decodes a config object (nil or invalid -> empty map).
func configMap(raw json.RawMessage) map[string]any {
	m := map[string]any{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return m
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&m)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

// parentCycles returns each parent cycle among ms (by name), once.
func parentCycles(ms []MonitorSpec) [][]string {
	parent := map[string]string{}
	for _, m := range ms {
		if m.Parent != "" && m.Parent != m.Name {
			parent[m.Name] = m.Parent
		}
	}
	reported := map[string]bool{}
	var out [][]string
	for _, m := range ms {
		pos := map[string]int{}
		var chain []string
		for n := m.Name; n != ""; n = parent[n] {
			if i, ok := pos[n]; ok {
				cycle := chain[i:]
				key := minRotation(cycle)
				if !reported[key] {
					reported[key] = true
					out = append(out, append(slices.Clone(cycle), n))
				}
				break
			}
			pos[n] = len(chain)
			chain = append(chain, n)
		}
	}
	return out
}

func minRotation(cycle []string) string {
	s := slices.Clone(cycle)
	sort.Strings(s)
	return strings.Join(s, "\x00")
}

// monitorOrder returns the spec's monitors parents first; among monitors
// at the same depth, the spec order is kept. The spec must be valid.
func monitorOrder(ms []MonitorSpec) []MonitorSpec {
	byName := map[string]MonitorSpec{}
	for _, m := range ms {
		byName[m.Name] = m
	}
	done := map[string]bool{}
	out := make([]MonitorSpec, 0, len(ms))
	var visit func(m MonitorSpec)
	visit = func(m MonitorSpec) {
		if done[m.Name] {
			return
		}
		done[m.Name] = true
		if p, ok := byName[m.Parent]; ok && m.Parent != "" {
			visit(p)
		}
		out = append(out, m)
	}
	for _, m := range ms {
		visit(m)
	}
	return out
}

// effectiveSLO is an SLO spec with the server defaults filled in.
func effectiveSLO(s SLOSpec) SLO {
	e := SLO{WindowDays: defaultSLOWindowDays, FastBurn: defaultSLOFastBurn, SlowBurn: defaultSLOSlowBurn}
	if s.Target != nil {
		e.Target = *s.Target
	}
	if s.WindowDays != nil {
		e.WindowDays = *s.WindowDays
	}
	if s.FastBurn != nil {
		e.FastBurn = *s.FastBurn
	}
	if s.SlowBurn != nil {
		e.SlowBurn = *s.SlowBurn
	}
	return e
}

// sloProblems checks an SLO against the server's rules: 0 < target < 100
// with at most 3 decimals, window 7..90 days, 1 <= slow <= fast <= 1000.
func sloProblems(s SLOSpec) []string {
	var out []string
	e := effectiveSLO(s)
	switch {
	case s.Target == nil:
		out = append(out, "target is required")
	case e.Target <= 0 || e.Target >= 100:
		out = append(out, "target must be greater than 0 and less than 100")
	case math.Abs(e.Target*1000-math.Round(e.Target*1000)) > 1e-6:
		out = append(out, "target must have at most 3 decimals")
	}
	if e.WindowDays < 7 || e.WindowDays > 90 {
		out = append(out, "window_days must be between 7 and 90")
	}
	switch {
	case e.SlowBurn < 1:
		out = append(out, "slow_burn must be at least 1")
	case e.FastBurn < e.SlowBurn:
		out = append(out, "fast_burn must be at least slow_burn")
	case e.FastBurn > 1000:
		out = append(out, "fast_burn must be at most 1000")
	}
	return out
}

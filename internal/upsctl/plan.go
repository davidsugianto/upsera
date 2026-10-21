package upsctl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Live is a snapshot of the team's current setup, read through the API.
type Live struct {
	Team        TokenInfo
	Channels    []Channel
	Policies    []Policy
	Monitors    []Monitor
	StatusPages []StatusPage
	Regions     []string
	DockerHosts []DockerHost
	SLOs        []SLOListItem
}

// FetchLive reads everything a plan or an export needs.
func FetchLive(ctx context.Context, api API) (*Live, error) {
	l := &Live{}
	var err error
	if l.Team, err = api.TokenInfo(ctx); err != nil {
		return nil, err
	}
	if l.Regions, err = api.Regions(ctx); err != nil {
		return nil, err
	}
	if l.DockerHosts, err = api.DockerHosts(ctx); err != nil {
		return nil, err
	}
	if l.Channels, err = api.Channels(ctx); err != nil {
		return nil, err
	}
	if l.Policies, err = api.Policies(ctx); err != nil {
		return nil, err
	}
	if l.Monitors, err = api.Monitors(ctx); err != nil {
		return nil, err
	}
	if l.StatusPages, err = api.StatusPages(ctx); err != nil {
		return nil, err
	}
	if l.SLOs, err = api.ListSLOs(ctx); err != nil {
		return nil, err
	}
	return l, nil
}

// Resource kinds, in apply order.
const (
	KindChannel    = "channel"
	KindPolicy     = "escalation policy"
	KindMonitor    = "monitor"
	KindStatusPage = "status page"
	// KindSLO is a monitor's SLO; Name is the monitor's name and ID its id.
	KindSLO = "slo"
)

// Op is what an action does.
type Op string

const (
	OpCreate Op = "create"
	OpUpdate Op = "update"
	OpDelete Op = "delete"
	// OpSync re-sends a channel's secrets. They cannot be compared with
	// what the server holds, so it is not counted as a change; the server
	// ignores a PUT that changes nothing.
	OpSync Op = "sync"
)

// Action is one step of a plan.
type Action struct {
	Op   Op
	Kind string
	// Name is the entry's name (a status page's slug).
	Name string
	// Fields are the changed field names of an update.
	Fields []string
	// SecretsNotCompared marks a channel whose secret fields are sent
	// without being compared.
	SecretsNotCompared bool
	// ID is the live id (update, delete, sync).
	ID int64

	channel *ChannelSpec
	policy  *PolicySpec
	monitor *MonitorSpec
	page    *StatusPageSpec
	slo     *SLO
	// liveUpdatedAt lets apply tell whether a channel PUT changed anything.
	liveChannel *Channel
}

// Plan is the ordered list of actions that makes the live team match a
// spec: channels, policies, monitors (parents first) and status pages are
// created or updated in that order, then (with prune) whatever the spec no
// longer lists is deleted in reverse order.
type Plan struct {
	Actions []Action
	// Unmanaged lists live entries absent from the spec that are kept
	// because prune is off (e.g. `monitor "legacy"`).
	Unmanaged []string
	// Warnings are things apply will not do, such as setting a custom domain.
	Warnings []string
}

// Counts returns how many creates, updates and deletes the plan holds.
func (p *Plan) Counts() (create, update, del int) {
	for _, a := range p.Actions {
		switch a.Op {
		case OpCreate:
			create++
		case OpUpdate:
			update++
		case OpDelete:
			del++
		}
	}
	return create, update, del
}

// HasChanges reports whether applying the plan would change anything that
// can be compared (secret re-sends are not changes).
func (p *Plan) HasChanges() bool {
	c, u, d := p.Counts()
	return c+u+d > 0
}

// PlanOptions tune BuildPlan.
type PlanOptions struct {
	// Prune deletes live entries the spec does not list.
	Prune bool
}

// BuildPlan compares a validated spec with the live team. It is pure. It
// fails, listing every problem, when the plan cannot be applied safely:
// duplicate names in the live team, unknown regions or Docker hosts, a
// changed channel or monitor type, a malformed secret.
func BuildPlan(s *Spec, live *Live, opts PlanOptions) (*Plan, error) {
	if err := Validate(s); err != nil {
		return nil, err
	}
	var probs Problems
	add := func(format string, args ...any) { probs = append(probs, fmt.Sprintf(format, args...)) }

	liveCh, d := indexByName(live.Channels, func(c Channel) string { return c.Name }, func(c Channel) int64 { return c.ID })
	probs = append(probs, dupProblems(KindChannel, d)...)
	livePol, d := indexByName(live.Policies, func(p Policy) string { return p.Name }, func(p Policy) int64 { return p.ID })
	probs = append(probs, dupProblems(KindPolicy, d)...)
	liveMon, d := indexByName(live.Monitors, func(m Monitor) string { return m.Name }, func(m Monitor) int64 { return m.ID })
	probs = append(probs, dupProblems(KindMonitor, d)...)
	livePage, _ := indexByName(live.StatusPages, func(p StatusPage) string { return p.Slug }, func(p StatusPage) int64 { return p.ID })
	hosts, hostDups := indexByName(live.DockerHosts, func(h DockerHost) string { return h.Name }, func(h DockerHost) int64 { return h.ID })

	n := newLiveNames(live)
	plan := &Plan{}

	// Pre-checks against the instance.
	regions := map[string]bool{localRegion: true}
	for _, r := range live.Regions {
		regions[r] = true
	}
	for _, m := range s.Monitors {
		if c, ok := liveMon[m.Name]; ok && c.Type != m.Type {
			add("monitor %q: type cannot change from %s to %s (delete it or use a new name)", m.Name, c.Type, m.Type)
		}
		for _, r := range m.Regions {
			if !regions[r] {
				add("monitor %q: unknown region %q", m.Name, r)
			}
		}
		if m.Type == "docker" {
			h, _ := configMap(m.Config)["docker_host"].(string)
			if _, ok := hosts[h]; !ok {
				add("monitor %q: unknown Docker host %q", m.Name, h)
			} else if len(hostDups[h]) > 0 {
				add("monitor %q: the team has several Docker hosts named %q", m.Name, h)
			}
		}
	}
	for _, c := range s.Channels {
		if l, ok := liveCh[c.Name]; ok && l.Type != c.Type {
			add("channel %q: type cannot change from %s to %s (delete it or use a new name)", c.Name, l.Type, c.Type)
		}
		if _, err := channelConfig(c); err != nil {
			add("channel %q: %v", c.Name, err)
		}
	}
	if len(probs) > 0 {
		return nil, probs
	}

	// Creates and updates.
	for i := range s.Channels {
		c := &s.Channels[i]
		l, ok := liveCh[c.Name]
		if !ok {
			plan.Actions = append(plan.Actions, Action{Op: OpCreate, Kind: KindChannel, Name: c.Name, channel: c})
			continue
		}
		fields := channelDiff(*c, l)
		secret := hasSecrets(*c)
		a := Action{Kind: KindChannel, Name: c.Name, ID: l.ID, Fields: fields, SecretsNotCompared: secret, channel: c, liveChannel: &l}
		switch {
		case len(fields) > 0:
			a.Op = OpUpdate
		case secret:
			a.Op = OpSync
		default:
			continue
		}
		plan.Actions = append(plan.Actions, a)
	}
	for i := range s.EscalationPolicies {
		p := &s.EscalationPolicies[i]
		l, ok := livePol[p.Name]
		if !ok {
			plan.Actions = append(plan.Actions, Action{Op: OpCreate, Kind: KindPolicy, Name: p.Name, policy: p})
			continue
		}
		if fields := policyDiff(*p, l, n); len(fields) > 0 {
			plan.Actions = append(plan.Actions, Action{Op: OpUpdate, Kind: KindPolicy, Name: p.Name, ID: l.ID, Fields: fields, policy: p})
		}
	}
	liveSLOs := map[int64]SLO{}
	for _, x := range live.SLOs {
		liveSLOs[x.MonitorID] = SLO{MonitorID: x.MonitorID, Target: x.Target, WindowDays: x.WindowDays, FastBurn: x.FastBurn, SlowBurn: x.SlowBurn}
	}
	var sloDeletes []Action
	for _, m := range monitorOrder(s.Monitors) {
		l, ok := liveMon[m.Name]
		switch {
		case !ok:
			plan.Actions = append(plan.Actions, Action{Op: OpCreate, Kind: KindMonitor, Name: m.Name, monitor: &m})
		default:
			if fields := monitorDiff(m, l, n); len(fields) > 0 {
				plan.Actions = append(plan.Actions, Action{Op: OpUpdate, Kind: KindMonitor, Name: m.Name, ID: l.ID, Fields: fields, monitor: &m})
			}
		}
		// The SLO follows its monitor, so a new monitor exists first.
		have, hasSLO := liveSLOs[l.ID]
		hasSLO = hasSLO && ok
		switch {
		case m.SLO != nil && !hasSLO:
			want := effectiveSLO(*m.SLO)
			plan.Actions = append(plan.Actions, Action{Op: OpCreate, Kind: KindSLO, Name: m.Name, Fields: sloFields(want), slo: &want})
		case m.SLO != nil:
			want := effectiveSLO(*m.SLO)
			if fields := sloDiff(want, have); len(fields) > 0 {
				plan.Actions = append(plan.Actions, Action{Op: OpUpdate, Kind: KindSLO, Name: m.Name, ID: l.ID, Fields: fields, slo: &want})
			}
		case hasSLO && opts.Prune:
			sloDeletes = append(sloDeletes, Action{Op: OpDelete, Kind: KindSLO, Name: m.Name, ID: l.ID})
		case hasSLO:
			plan.Unmanaged = append(plan.Unmanaged, fmt.Sprintf("%s %q slo", KindMonitor, m.Name))
		}
	}
	for i := range s.StatusPages {
		p := &s.StatusPages[i]
		l, ok := livePage[p.Slug]
		var liveDomain *string
		if ok {
			liveDomain = l.CustomDomain
		}
		if p.CustomDomain != nil && derefLower(p.CustomDomain) != derefLower(liveDomain) {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"status page %q: custom_domain can only be changed by an instance admin in the dashboard; leaving it as %s (spec: %s)",
				p.Slug, showDomain(liveDomain), showDomain(p.CustomDomain)))
		}
		if !ok {
			plan.Actions = append(plan.Actions, Action{Op: OpCreate, Kind: KindStatusPage, Name: p.Slug, page: p})
			continue
		}
		if fields := pageDiff(*p, l, n); len(fields) > 0 {
			plan.Actions = append(plan.Actions, Action{Op: OpUpdate, Kind: KindStatusPage, Name: p.Slug, ID: l.ID, Fields: fields, page: p})
		}
	}

	// Deletes (prune) in reverse order, or the list of what is kept.
	inSpec := func(kind, name string) bool {
		switch kind {
		case KindChannel:
			return slices.ContainsFunc(s.Channels, func(c ChannelSpec) bool { return c.Name == name })
		case KindPolicy:
			return slices.ContainsFunc(s.EscalationPolicies, func(p PolicySpec) bool { return p.Name == name })
		case KindMonitor:
			return slices.ContainsFunc(s.Monitors, func(m MonitorSpec) bool { return m.Name == name })
		}
		return slices.ContainsFunc(s.StatusPages, func(p StatusPageSpec) bool { return p.Slug == name })
	}
	extra := func(kind, name string, id int64) {
		if inSpec(kind, name) {
			return
		}
		if opts.Prune {
			plan.Actions = append(plan.Actions, Action{Op: OpDelete, Kind: kind, Name: name, ID: id})
		} else {
			plan.Unmanaged = append(plan.Unmanaged, fmt.Sprintf("%s %q", kind, name))
		}
	}
	for _, p := range sortedBy(live.StatusPages, func(p StatusPage) string { return p.Slug }) {
		extra(KindStatusPage, p.Slug, p.ID)
	}
	// A pruned monitor's SLO goes with it on the server; only SLOs of kept
	// monitors are deleted here.
	plan.Actions = append(plan.Actions, sloDeletes...)
	for _, m := range childrenFirst(live.Monitors) {
		extra(KindMonitor, m.Name, m.ID)
	}
	for _, p := range sortedBy(live.Policies, func(p Policy) string { return p.Name }) {
		extra(KindPolicy, p.Name, p.ID)
	}
	for _, c := range sortedBy(live.Channels, func(c Channel) string { return c.Name }) {
		extra(KindChannel, c.Name, c.ID)
	}
	return plan, nil
}

// indexByName maps names to entries; dups lists the ids of names used more
// than once.
func indexByName[T any](list []T, name func(T) string, id func(T) int64) (map[string]T, map[string][]int64) {
	idx := make(map[string]T, len(list))
	ids := map[string][]int64{}
	for _, e := range list {
		idx[name(e)] = e
		ids[name(e)] = append(ids[name(e)], id(e))
	}
	dups := map[string][]int64{}
	for n, l := range ids {
		if len(l) > 1 {
			dups[n] = l
		}
	}
	return idx, dups
}

func dupProblems(kind string, dups map[string][]int64) Problems {
	if len(dups) == 0 {
		return nil
	}
	names := make([]string, 0, len(dups))
	for n := range dups {
		names = append(names, n)
	}
	sort.Strings(names)
	var out Problems
	for _, n := range names {
		out = append(out, fmt.Sprintf("the team has %d %ss named %q (ids %s): rename them in the dashboard so names are unique",
			len(dups[n]), kind, n, joinIDs(dups[n])))
	}
	return out
}

func joinIDs(ids []int64) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = fmt.Sprint(id)
	}
	return strings.Join(s, ", ")
}

func sortedBy[T any](list []T, key func(T) string) []T {
	out := slices.Clone(list)
	sort.SliceStable(out, func(i, j int) bool { return key(out[i]) < key(out[j]) })
	return out
}

// childrenFirst orders live monitors so that a monitor comes before its
// parent (deepest first, then by name).
func childrenFirst(ms []Monitor) []Monitor {
	byID := map[int64]Monitor{}
	for _, m := range ms {
		byID[m.ID] = m
	}
	depth := func(m Monitor) int {
		d := 0
		for seen := map[int64]bool{}; m.ParentID != nil && !seen[m.ID]; d++ {
			seen[m.ID] = true
			p, ok := byID[*m.ParentID]
			if !ok {
				break
			}
			m = p
		}
		return d
	}
	out := slices.Clone(ms)
	sort.SliceStable(out, func(i, j int) bool {
		di, dj := depth(out[i]), depth(out[j])
		if di != dj {
			return di > dj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// liveNames resolves live ids to names.
type liveNames struct {
	channels, policies, monitors, hosts map[int64]string
}

func newLiveNames(l *Live) liveNames {
	n := liveNames{channels: map[int64]string{}, policies: map[int64]string{}, monitors: map[int64]string{}, hosts: map[int64]string{}}
	for _, c := range l.Channels {
		n.channels[c.ID] = c.Name
	}
	for _, p := range l.Policies {
		n.policies[p.ID] = p.Name
	}
	for _, m := range l.Monitors {
		n.monitors[m.ID] = m.Name
	}
	for _, h := range l.DockerHosts {
		n.hosts[h.ID] = h.Name
	}
	return n
}

func nameOf(m map[int64]string, id int64) string {
	if n, ok := m[id]; ok {
		return n
	}
	return fmt.Sprintf("#%d", id)
}

func (n liveNames) channelNames(ids []int64) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = nameOf(n.channels, id)
	}
	return out
}

// ---- channels ----

// hasSecrets reports whether the spec gives any secret field of c a value.
func hasSecrets(c ChannelSpec) bool {
	cfg := configMap(c.Config)
	for _, f := range SecretFields(c.Type) {
		if !isZero(cfg[f]) {
			return true
		}
	}
	return false
}

// channelConfig is the config to send for c: object secrets (webhook
// headers) given as a JSON string from the environment are decoded.
func channelConfig(c ChannelSpec) (json.RawMessage, error) {
	cfg := configMap(c.Config)
	for _, f := range SecretFields(c.Type) {
		s, ok := cfg[f].(string)
		if !ok || !isObjectSecret(c.Type, f) || s == redacted {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(s), &obj); err != nil {
			return nil, fmt.Errorf("config.%s must be a mapping (or a JSON object in its ${ENV} variable)", f)
		}
		cfg[f] = obj
	}
	return json.Marshal(cfg)
}

// channelDiff lists the changed fields of a channel; secret fields are
// never compared.
func channelDiff(c ChannelSpec, l Channel) []string {
	var fields []string
	if c.IsDefault != l.IsDefault {
		fields = append(fields, "is_default")
	}
	secrets := SecretFields(c.Type)
	want, have := configMap(c.Config), configMap(l.Config)
	for _, f := range secrets {
		delete(want, f)
		delete(have, f)
	}
	return append(fields, configDiff(want, have, nil)...)
}

// configDiff lists "config.<key>" for every key whose value differs. A key
// missing on one side equals the other side's value when that is a zero
// value or the server default from defaults.
func configDiff(want, have map[string]any, defaults map[string]any) []string {
	keys := map[string]bool{}
	for k := range want {
		keys[k] = true
	}
	for k := range have {
		keys[k] = true
	}
	var out []string
	for _, k := range sortedKeys(keys) {
		w, wok := want[k]
		h, hok := have[k]
		def, hasDef := defaults[k]
		switch {
		case wok && hok:
			if !jsonEqual(w, h) {
				out = append(out, "config."+k)
			}
		case wok:
			if !isZero(w) && !(hasDef && jsonEqual(w, def)) {
				out = append(out, "config."+k)
			}
		default:
			if !isZero(h) && !(hasDef && jsonEqual(h, def)) {
				out = append(out, "config."+k)
			}
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// canon round-trips v through JSON so numbers compare as float64.
func canon(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(b, &out) != nil {
		return v
	}
	return out
}

func jsonEqual(a, b any) bool { return reflect.DeepEqual(canon(a), canon(b)) }

// isZero reports whether v is null, false, 0, "", {} or [].
func isZero(v any) bool {
	switch x := canon(v).(type) {
	case nil:
		return true
	case bool:
		return !x
	case float64:
		return x == 0
	case string:
		return x == ""
	case map[string]any:
		return len(x) == 0
	case []any:
		return len(x) == 0
	}
	return false
}

// ---- policies ----

func stepDelay(d int) int {
	if d == 0 {
		return defaultDelayS
	}
	return d
}

func policyDiff(p PolicySpec, l Policy, n liveNames) []string {
	if len(p.Steps) != len(l.Steps) {
		return []string{"steps"}
	}
	var fields []string
	for i, s := range p.Steps {
		want := slices.Sorted(slices.Values(s.Channels))
		have := slices.Sorted(slices.Values(n.channelNames(l.Steps[i].ChannelIDs)))
		if !slices.Equal(want, have) {
			fields = append(fields, fmt.Sprintf("steps[%d].channels", i))
		}
		if stepDelay(s.DelayS) != l.Steps[i].DelayS {
			fields = append(fields, fmt.Sprintf("steps[%d].delay_s", i))
		}
	}
	return fields
}

// ---- monitors ----

// monitorConfigDefaults are the values the server fills into a monitor
// config when they are left out, so leaving them out of the spec is not a
// change.
var monitorConfigDefaults = map[string]map[string]any{
	"http":    {"method": "GET", "accepted_status": []any{"200-299"}, "max_redirects": 10},
	"keyword": {"method": "GET", "accepted_status": []any{"200-299"}, "max_redirects": 10},
	"ping":    {"count": 3},
	"dns":     {"record_type": "A"},
}

// upperConfigKeys are normalized to upper case by the server.
var upperConfigKeys = []string{"method", "record_type"}

func or(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func defaultQuorum(regions int) int { return regions/2 + 1 }

// desiredRegions returns the spec's regions (default [local]) and quorum.
func desiredRegions(m MonitorSpec) ([]string, int) {
	regions := m.Regions
	if len(regions) == 0 {
		regions = []string{localRegion}
	}
	return regions, or(m.Quorum, defaultQuorum(len(regions)))
}

// specMonitorConfig is a spec monitor's config with server normalization
// (upper-cased method and record type) applied; docker_host stays a name.
func specMonitorConfig(m MonitorSpec) map[string]any {
	cfg := configMap(m.Config)
	for _, k := range upperConfigKeys {
		if s, ok := cfg[k].(string); ok {
			cfg[k] = strings.ToUpper(strings.TrimSpace(s))
		}
	}
	return cfg
}

// liveMonitorConfig is a live config with docker_host_id replaced by the
// host's name.
func liveMonitorConfig(m Monitor, n liveNames) map[string]any {
	cfg := configMap(m.Config)
	if m.Type == "docker" {
		if id, ok := cfg["docker_host_id"]; ok {
			delete(cfg, "docker_host_id")
			var hid int64
			if f, ok := canon(id).(float64); ok {
				hid = int64(f)
			}
			cfg["docker_host"] = nameOf(n.hosts, hid)
		}
	}
	return cfg
}

func monitorDiff(m MonitorSpec, l Monitor, n liveNames) []string {
	var fields []string
	chk := func(name string, changed bool) {
		if changed {
			fields = append(fields, name)
		}
	}
	chk("interval_s", or(m.IntervalS, defaultIntervalS) != l.IntervalS)
	chk("retry_interval_s", or(m.RetryIntervalS, defaultRetryIntervalS) != l.RetryIntervalS)
	chk("retries", or(m.Retries, defaultRetries) != l.Retries)
	chk("timeout_s", or(m.TimeoutS, defaultTimeoutS) != l.TimeoutS)
	chk("paused", m.Paused != l.Paused)
	chk("group_name", m.GroupName != l.GroupName)
	chk("tags", !slices.Equal(m.Tags, l.Tags))
	parent := ""
	if l.ParentID != nil {
		parent = nameOf(n.monitors, *l.ParentID)
	}
	chk("parent", m.Parent != parent)
	policy := ""
	if l.EscalationPolicyID != nil {
		policy = nameOf(n.policies, *l.EscalationPolicyID)
	}
	chk("escalation_policy", m.EscalationPolicy != policy)
	chk("channels", !slices.Equal(slices.Sorted(slices.Values(m.Channels)), slices.Sorted(slices.Values(n.channelNames(l.ChannelIDs)))))
	regions, quorum := desiredRegions(m)
	liveRegions := l.Regions
	if len(liveRegions) == 0 {
		liveRegions = []string{localRegion}
	}
	chk("regions", !slices.Equal(slices.Sorted(slices.Values(regions)), slices.Sorted(slices.Values(liveRegions))))
	chk("quorum", quorum != max(l.Quorum, 1))
	return append(fields, configDiff(specMonitorConfig(m), liveMonitorConfig(l, n), monitorConfigDefaults[m.Type])...)
}

// ---- SLOs ----

func fmtNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// sloFields describes a new SLO.
func sloFields(e SLO) []string {
	return []string{
		"target " + fmtNum(e.Target), fmt.Sprintf("window_days %d", e.WindowDays),
		"fast_burn " + fmtNum(e.FastBurn), "slow_burn " + fmtNum(e.SlowBurn),
	}
}

// sloDiff lists "field old -> new" for every SLO value that differs.
// Targets compare as decimals with 3 places (99.9 equals 99.900).
func sloDiff(want, have SLO) []string {
	var out []string
	if math.Round(want.Target*1000) != math.Round(have.Target*1000) {
		out = append(out, "target "+fmtNum(have.Target)+" -> "+fmtNum(want.Target))
	}
	if want.WindowDays != have.WindowDays {
		out = append(out, fmt.Sprintf("window_days %d -> %d", have.WindowDays, want.WindowDays))
	}
	if !sameFloat(want.FastBurn, have.FastBurn) {
		out = append(out, "fast_burn "+fmtNum(have.FastBurn)+" -> "+fmtNum(want.FastBurn))
	}
	if !sameFloat(want.SlowBurn, have.SlowBurn) {
		out = append(out, "slow_burn "+fmtNum(have.SlowBurn)+" -> "+fmtNum(want.SlowBurn))
	}
	return out
}

func sameFloat(a, b float64) bool { return math.Abs(a-b) <= 1e-9*max(1, math.Abs(a)) }

// ---- status pages ----

type pageItem struct {
	Section, Monitor, DisplayName string
}

// specItems flattens a spec page's sections; display names default to the
// monitor's name, cut to 100 characters like the server does.
func specItems(p StatusPageSpec) []pageItem {
	var out []pageItem
	for _, sec := range p.Sections {
		for _, it := range sec.Items {
			name := strings.TrimSpace(it.DisplayName)
			if name == "" {
				name = it.Monitor
				if utf8.RuneCountInString(name) > 100 {
					name = string([]rune(name)[:100])
				}
			}
			out = append(out, pageItem{Section: strings.TrimSpace(sec.Name), Monitor: it.Monitor, DisplayName: name})
		}
	}
	return out
}

func pageDiff(p StatusPageSpec, l StatusPage, n liveNames) []string {
	var fields []string
	chk := func(name string, changed bool) {
		if changed {
			fields = append(fields, name)
		}
	}
	theme := p.Theme
	if theme == "" {
		theme = defaultTheme
	}
	chk("title", strings.TrimSpace(p.Title) != l.Title)
	chk("description", strings.TrimSpace(p.Description) != l.Description)
	chk("logo_url", strings.TrimSpace(p.LogoURL) != l.LogoURL)
	chk("theme", theme != l.Theme)
	chk("published", p.Published != l.Published)
	have := make([]pageItem, len(l.Items))
	for i, it := range l.Items {
		have[i] = pageItem{Section: it.Section, Monitor: nameOf(n.monitors, it.MonitorID), DisplayName: it.DisplayName}
	}
	chk("sections", !slices.Equal(specItems(p), have))
	return fields
}

func derefLower(s *string) string {
	if s == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(*s))
}

func showDomain(s *string) string {
	if d := derefLower(s); d != "" {
		return fmt.Sprintf("%q", d)
	}
	return "none"
}

// ---- output ----

// label names an action's target: `monitor "x"`, or `monitor "x" slo`.
func (a Action) label() string {
	if a.Kind == KindSLO {
		return fmt.Sprintf("%s %q slo", KindMonitor, a.Name)
	}
	return fmt.Sprintf("%s %q", a.Kind, a.Name)
}

func writeSLOAction(w io.Writer, a Action) {
	switch a.Op {
	case OpCreate:
		fmt.Fprintf(w, "+ create %s: %s\n", a.label(), strings.Join(a.Fields, ", "))
	case OpUpdate:
		fields := make([]string, len(a.Fields))
		for i, f := range a.Fields {
			fields[i] = "slo " + f
		}
		fmt.Fprintf(w, "~ update %s %q: %s\n", KindMonitor, a.Name, strings.Join(fields, ", "))
	case OpDelete:
		fmt.Fprintf(w, "- delete %s\n", a.label())
	}
}

// WritePlan prints a plan the way `upsctl diff` shows it.
func WritePlan(w io.Writer, p *Plan) {
	for _, a := range p.Actions {
		if a.Kind == KindSLO {
			writeSLOAction(w, a)
			continue
		}
		switch a.Op {
		case OpCreate:
			fmt.Fprintf(w, "+ create %s %q\n", a.Kind, a.Name)
		case OpUpdate:
			line := fmt.Sprintf("~ update %s %q: %s", a.Kind, a.Name, strings.Join(a.Fields, ", "))
			if a.SecretsNotCompared {
				line += " (secrets not compared)"
			}
			fmt.Fprintln(w, line)
		case OpDelete:
			fmt.Fprintf(w, "- delete %s %q\n", a.Kind, a.Name)
		case OpSync:
			fmt.Fprintf(w, "  %s %q: secrets not compared\n", a.Kind, a.Name)
		}
	}
	for _, u := range p.Unmanaged {
		fmt.Fprintf(w, "  not managed, kept: %s (use --prune to delete)\n", u)
	}
	for _, warn := range p.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warn)
	}
	if c, u, d := p.Counts(); c+u+d == 0 {
		fmt.Fprintln(w, "No changes.")
	} else {
		fmt.Fprintf(w, "Plan: %d to create, %d to update, %d to delete.\n", c, u, d)
	}
}

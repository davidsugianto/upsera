package upsctl

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// SpecVersion is the only spec format version upsctl understands.
const SpecVersion = 1

// Spec is one team's setup as code. Entries reference each other by name
// (status pages by slug), never by id, so a spec applies to any instance.
//
// YAML field names are the API's JSON field names, except where an id is
// replaced by a name: parent, escalation_policy, channels, a policy step's
// channels, a status page item's monitor, and docker_host inside a docker
// monitor's config.
type Spec struct {
	Version            int              `json:"version"`
	Channels           []ChannelSpec    `json:"channels,omitempty"`
	EscalationPolicies []PolicySpec     `json:"escalation_policies,omitempty"`
	Monitors           []MonitorSpec    `json:"monitors,omitempty"`
	StatusPages        []StatusPageSpec `json:"status_pages,omitempty"`
}

// ChannelSpec is a notification channel, matched by name within the team.
// Secret config fields are written as ${ENV} placeholders.
type ChannelSpec struct {
	Name      string          `json:"name"`
	Type      string          `json:"type"`
	IsDefault bool            `json:"is_default,omitempty"`
	Config    json.RawMessage `json:"config,omitempty"`
}

// PolicySpec is an escalation policy, matched by name.
type PolicySpec struct {
	Name  string     `json:"name"`
	Steps []StepSpec `json:"steps"`
}

// StepSpec is one escalation step: notify Channels (names), then wait
// DelayS seconds (default 600) before the next step.
type StepSpec struct {
	Channels []string `json:"channels"`
	DelayS   int      `json:"delay_s,omitempty"`
}

// MonitorSpec is a monitor, matched by name. Omitted numbers take the
// server's defaults (interval 60s, retry interval 20s, 1 retry, timeout
// 10s, regions [local], quorum a majority of the regions).
type MonitorSpec struct {
	Name             string          `json:"name"`
	Type             string          `json:"type"`
	Config           json.RawMessage `json:"config,omitempty"`
	IntervalS        *int            `json:"interval_s,omitempty"`
	RetryIntervalS   *int            `json:"retry_interval_s,omitempty"`
	Retries          *int            `json:"retries,omitempty"`
	TimeoutS         *int            `json:"timeout_s,omitempty"`
	Paused           bool            `json:"paused,omitempty"`
	GroupName        string          `json:"group_name,omitempty"`
	Tags             []string        `json:"tags,omitempty"`
	Regions          []string        `json:"regions,omitempty"`
	Quorum           *int            `json:"quorum,omitempty"`
	Parent           string          `json:"parent,omitempty"`
	EscalationPolicy string          `json:"escalation_policy,omitempty"`
	Channels         []string        `json:"channels,omitempty"`
	SLO              *SLOSpec        `json:"slo,omitempty"`
}

// SLOSpec is a monitor's service level objective. Target (percent) is
// required; the others default to 30 days, fast burn 14.4 and slow burn 6.
// Export always writes all four.
type SLOSpec struct {
	Target     *float64 `json:"target,omitempty"`
	WindowDays *int     `json:"window_days,omitempty"`
	FastBurn   *float64 `json:"fast_burn,omitempty"`
	SlowBurn   *float64 `json:"slow_burn,omitempty"`
}

// StatusPageSpec is a status page, matched by slug. CustomDomain can only
// be changed by an instance admin session: apply with a token skips it.
type StatusPageSpec struct {
	Slug         string        `json:"slug"`
	Title        string        `json:"title"`
	Description  string        `json:"description,omitempty"`
	LogoURL      string        `json:"logo_url,omitempty"`
	Theme        string        `json:"theme,omitempty"`
	Published    bool          `json:"published,omitempty"`
	CustomDomain *string       `json:"custom_domain,omitempty"`
	Sections     []SectionSpec `json:"sections,omitempty"`
}

// SectionSpec is a heading on a status page and the monitors under it, in
// display order. An empty name means items shown without a heading.
type SectionSpec struct {
	Name  string     `json:"name,omitempty"`
	Items []ItemSpec `json:"items"`
}

// ItemSpec lists one monitor (by name) on a status page. DisplayName
// defaults to the monitor's name.
type ItemSpec struct {
	Monitor     string `json:"monitor"`
	DisplayName string `json:"display_name,omitempty"`
}

// Server-side defaults the spec may leave out.
const (
	defaultIntervalS      = 60
	defaultRetryIntervalS = 20
	defaultRetries        = 1
	defaultTimeoutS       = 10
	defaultDelayS         = 600
	defaultTheme          = "auto"
	defaultSLOWindowDays  = 30
	defaultSLOFastBurn    = 14.4
	defaultSLOSlowBurn    = 6
	localRegion           = "local"
	redacted              = "********"
)

// ChannelTypes and MonitorTypes are the types the server accepts.
var (
	ChannelTypes = []string{"telegram", "discord", "slack", "slack_app", "smtp", "webhook"}
	MonitorTypes = []string{"http", "keyword", "tcp", "ping", "dns", "push", "docker"}
)

// SecretFields lists the config fields of a channel type the server
// redacts. It must equal notify.SecretFields (a test enforces it).
func SecretFields(channelType string) []string {
	switch channelType {
	case "telegram":
		return []string{"bot_token"}
	case "discord", "slack":
		return []string{"webhook_url"}
	case "slack_app":
		return []string{"bot_token", "app_token"}
	case "smtp":
		return []string{"password"}
	case "webhook":
		return []string{"url", "headers"}
	}
	return nil
}

// objectSecretFields are secret fields whose value is a JSON object, not a
// string; their ${ENV} placeholder must expand to a JSON object.
var objectSecretFields = map[string][]string{"webhook": {"headers"}}

func isObjectSecret(channelType, field string) bool {
	return slices.Contains(objectSecretFields[channelType], field)
}

// PlaceholderVar is the environment variable an exported secret is read
// from: UPSERA_<CHANNEL>_<FIELD>, upper-cased, every other character "_".
func PlaceholderVar(owner, field string) string {
	return "UPSERA_" + envName(owner) + "_" + envName(field)
}

func envName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// checkKeys reports every key of the decoded YAML tree v that the spec
// type t does not define, with its path. Raw JSON fields (config) accept
// anything.
func checkKeys(v any, t reflect.Type, path string) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeFor[json.RawMessage]() {
		return nil
	}
	var errs []string
	switch t.Kind() {
	case reflect.Struct:
		m, ok := v.(map[string]any)
		if !ok {
			return nil // a type error the JSON decoder reports
		}
		fields := map[string]reflect.Type{}
		for i := range t.NumField() {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name != "" && name != "-" {
				fields[name] = f.Type
			}
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ft, ok := fields[k]
			switch {
			case ok:
				errs = append(errs, checkKeys(m[k], ft, joinPath(path, k))...)
			default:
				errs = append(errs, joinPath(path, k)+": unknown field")
			}
		}
	case reflect.Slice:
		list, ok := v.([]any)
		if !ok {
			return nil
		}
		for i, e := range list {
			errs = append(errs, checkKeys(e, t.Elem(), fmt.Sprintf("%s[%d]", path, i))...)
		}
	}
	return errs
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

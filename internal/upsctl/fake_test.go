package upsctl_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/davidsugianto/upsera/internal/upsctl"
)

// fakeAPI is an in-memory Upsera team implementing upsctl.API. It keeps
// channel secrets in plain text and redacts them on reads like the server,
// ignores channel PUTs that change nothing, and logs every write.
type fakeAPI struct {
	mu       sync.Mutex
	nextID   int64
	now      time.Time
	regions  []string
	hosts    []upsctl.DockerHost
	channels []upsctl.Channel
	policies []upsctl.Policy
	monitors []upsctl.Monitor
	pages    []upsctl.StatusPage
	slos     map[int64]upsctl.SLO
	writes   []string
	// sloGone makes DeleteSLO answer 404 (deleted elsewhere meanwhile).
	sloGone bool
	// failOn makes the write with this log line fail.
	failOn string
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		nextID: 100, now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), regions: []string{"local", "eu"},
		slos: map[int64]upsctl.SLO{},
	}
}

func (f *fakeAPI) id() int64 { f.nextID++; return f.nextID }

func (f *fakeAPI) tick() time.Time { f.now = f.now.Add(time.Second); return f.now }

func (f *fakeAPI) log(format string, args ...any) error {
	line := fmt.Sprintf(format, args...)
	if line == f.failOn {
		return &upsctl.APIError{Method: "POST", Path: "/x", Status: http.StatusUnprocessableEntity, Detail: "boom"}
	}
	f.writes = append(f.writes, line)
	return nil
}

func (f *fakeAPI) takeWrites() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := f.writes
	f.writes = nil
	return w
}

func (f *fakeAPI) TokenInfo(context.Context) (upsctl.TokenInfo, error) {
	return upsctl.TokenInfo{TeamID: 1, TeamName: "Ops", Scope: "write", Name: "ci"}, nil
}

func (f *fakeAPI) Regions(context.Context) ([]string, error) { return f.regions, nil }

func (f *fakeAPI) DockerHosts(context.Context) ([]upsctl.DockerHost, error) { return f.hosts, nil }

var secretFields = map[string][]string{
	"telegram": {"bot_token"}, "discord": {"webhook_url"}, "slack": {"webhook_url"},
	"slack_app": {"bot_token", "app_token"}, "smtp": {"password"}, "webhook": {"url", "headers"},
}

func redact(c upsctl.Channel) upsctl.Channel {
	var m map[string]any
	_ = json.Unmarshal(c.Config, &m)
	for _, k := range secretFields[c.Type] {
		if v, ok := m[k]; ok && v != "" && v != nil {
			m[k] = "********"
		}
	}
	c.Config, _ = json.Marshal(m)
	return c
}

func (f *fakeAPI) Channels(context.Context) ([]upsctl.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]upsctl.Channel, len(f.channels))
	for i, c := range f.channels {
		out[i] = redact(c)
	}
	return out, nil
}

func (f *fakeAPI) CreateChannel(_ context.Context, r upsctl.ChannelRequest) (upsctl.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.log("create channel %s", r.Name); err != nil {
		return upsctl.Channel{}, err
	}
	c := upsctl.Channel{ID: f.id(), Type: r.Type, Name: r.Name, Config: r.Config, IsDefault: r.IsDefault, UpdatedAt: f.tick()}
	f.channels = append(f.channels, c)
	return redact(c), nil
}

func (f *fakeAPI) UpdateChannel(_ context.Context, id int64, r upsctl.ChannelRequest) (upsctl.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.channels {
		if c.ID != id {
			continue
		}
		var stored, next map[string]any
		_ = json.Unmarshal(c.Config, &stored)
		_ = json.Unmarshal(r.Config, &next)
		for _, k := range secretFields[c.Type] {
			if v, ok := next[k]; !ok || v == "********" {
				if sv, ok := stored[k]; ok {
					next[k] = sv
				}
			}
		}
		if c.Name == r.Name && c.IsDefault == r.IsDefault && reflect.DeepEqual(stored, next) {
			return redact(c), nil // server-side no-op
		}
		if err := f.log("update channel %s", r.Name); err != nil {
			return upsctl.Channel{}, err
		}
		c.Name, c.IsDefault = r.Name, r.IsDefault
		c.Config, _ = json.Marshal(next)
		c.UpdatedAt = f.tick()
		f.channels[i] = c
		return redact(c), nil
	}
	return upsctl.Channel{}, &upsctl.APIError{Status: 404}
}

func (f *fakeAPI) DeleteChannel(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.channels {
		if c.ID == id {
			if err := f.log("delete channel %s", c.Name); err != nil {
				return err
			}
			f.channels = append(f.channels[:i], f.channels[i+1:]...)
			return nil
		}
	}
	return &upsctl.APIError{Status: 404}
}

func (f *fakeAPI) Policies(context.Context) ([]upsctl.Policy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]upsctl.Policy(nil), f.policies...), nil
}

func (f *fakeAPI) CreatePolicy(_ context.Context, r upsctl.PolicyRequest) (upsctl.Policy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.log("create escalation policy %s", r.Name); err != nil {
		return upsctl.Policy{}, err
	}
	p := upsctl.Policy{ID: f.id(), Name: r.Name, Steps: r.Steps}
	f.policies = append(f.policies, p)
	return p, nil
}

func (f *fakeAPI) UpdatePolicy(_ context.Context, id int64, r upsctl.PolicyRequest) (upsctl.Policy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.policies {
		if p.ID == id {
			if err := f.log("update escalation policy %s", r.Name); err != nil {
				return upsctl.Policy{}, err
			}
			f.policies[i] = upsctl.Policy{ID: id, Name: r.Name, Steps: r.Steps}
			return f.policies[i], nil
		}
	}
	return upsctl.Policy{}, &upsctl.APIError{Status: 404}
}

func (f *fakeAPI) DeletePolicy(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.policies {
		if p.ID == id {
			if err := f.log("delete escalation policy %s", p.Name); err != nil {
				return err
			}
			f.policies = append(f.policies[:i], f.policies[i+1:]...)
			return nil
		}
	}
	return &upsctl.APIError{Status: 404}
}

func (f *fakeAPI) Monitors(context.Context) ([]upsctl.Monitor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]upsctl.Monitor(nil), f.monitors...), nil
}

func monitorFrom(id int64, r upsctl.MonitorRequest) upsctl.Monitor {
	return upsctl.Monitor{
		ID: id, Name: r.Name, Type: r.Type, Config: r.Config, IntervalS: r.IntervalS, RetryIntervalS: r.RetryIntervalS,
		Retries: *r.Retries, TimeoutS: r.TimeoutS, Paused: r.Paused, GroupName: r.GroupName, Tags: r.Tags,
		ParentID: r.ParentID, EscalationPolicyID: r.EscalationPolicyID, ChannelIDs: r.ChannelIDs,
		Regions: r.Regions, Quorum: *r.Quorum,
	}
}

func (f *fakeAPI) CreateMonitor(_ context.Context, r upsctl.MonitorRequest) (upsctl.Monitor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.log("create monitor %s", r.Name); err != nil {
		return upsctl.Monitor{}, err
	}
	m := monitorFrom(f.id(), r)
	f.monitors = append(f.monitors, m)
	return m, nil
}

func (f *fakeAPI) UpdateMonitor(_ context.Context, id int64, r upsctl.MonitorRequest) (upsctl.Monitor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, m := range f.monitors {
		if m.ID == id {
			if err := f.log("update monitor %s", r.Name); err != nil {
				return upsctl.Monitor{}, err
			}
			f.monitors[i] = monitorFrom(id, r)
			return f.monitors[i], nil
		}
	}
	return upsctl.Monitor{}, &upsctl.APIError{Status: 404}
}

func (f *fakeAPI) DeleteMonitor(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, m := range f.monitors {
		if m.ID == id {
			if err := f.log("delete monitor %s", m.Name); err != nil {
				return err
			}
			f.monitors = append(f.monitors[:i], f.monitors[i+1:]...)
			delete(f.slos, id) // the server cascades
			return nil
		}
	}
	return &upsctl.APIError{Status: 404}
}

func (f *fakeAPI) StatusPages(context.Context) ([]upsctl.StatusPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]upsctl.StatusPage(nil), f.pages...), nil
}

func (f *fakeAPI) monitorName(id int64) string {
	for _, m := range f.monitors {
		if m.ID == id {
			return m.Name
		}
	}
	return ""
}

func (f *fakeAPI) pageFrom(id int64, r upsctl.StatusPageRequest, domain *string) upsctl.StatusPage {
	items := make([]upsctl.StatusPageItem, len(r.Items))
	for i, it := range r.Items {
		if it.DisplayName == "" {
			it.DisplayName = f.monitorName(it.MonitorID)
		}
		items[i] = it
	}
	return upsctl.StatusPage{
		ID: id, Slug: r.Slug, Title: r.Title, Description: r.Description, LogoURL: r.LogoURL, Theme: r.Theme,
		CustomDomain: domain, Published: r.Published, Items: items,
	}
}

func (f *fakeAPI) CreateStatusPage(_ context.Context, r upsctl.StatusPageRequest) (upsctl.StatusPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.log("create status page %s", r.Slug); err != nil {
		return upsctl.StatusPage{}, err
	}
	p := f.pageFrom(f.id(), r, nil)
	f.pages = append(f.pages, p)
	return p, nil
}

func (f *fakeAPI) UpdateStatusPage(_ context.Context, id int64, r upsctl.StatusPageRequest) (upsctl.StatusPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.pages {
		if p.ID == id {
			if err := f.log("update status page %s", r.Slug); err != nil {
				return upsctl.StatusPage{}, err
			}
			// A token cannot change the custom domain; omitted keeps it.
			f.pages[i] = f.pageFrom(id, r, p.CustomDomain)
			return f.pages[i], nil
		}
	}
	return upsctl.StatusPage{}, &upsctl.APIError{Status: 404}
}

func (f *fakeAPI) DeleteStatusPage(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.pages {
		if p.ID == id {
			if err := f.log("delete status page %s", p.Slug); err != nil {
				return err
			}
			f.pages = append(f.pages[:i], f.pages[i+1:]...)
			return nil
		}
	}
	return &upsctl.APIError{Status: 404}
}

func (f *fakeAPI) ListSLOs(context.Context) ([]upsctl.SLOListItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []upsctl.SLOListItem
	for id, s := range f.slos {
		out = append(out, upsctl.SLOListItem{
			MonitorID: id, MonitorName: f.monitorName(id),
			Target: s.Target, WindowDays: s.WindowDays, FastBurn: s.FastBurn, SlowBurn: s.SlowBurn,
		})
	}
	slices.SortFunc(out, func(a, b upsctl.SLOListItem) int { return strings.Compare(a.MonitorName, b.MonitorName) })
	return out, nil
}

func (f *fakeAPI) PutSLO(_ context.Context, monitorID int64, in upsctl.SLOInput) (upsctl.SLO, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := f.monitorName(monitorID)
	if name == "" {
		return upsctl.SLO{}, &upsctl.APIError{Status: 404}
	}
	if err := f.log("put slo %s", name); err != nil {
		return upsctl.SLO{}, err
	}
	s := upsctl.SLO{MonitorID: monitorID, Target: in.Target, WindowDays: 30, FastBurn: 14.4, SlowBurn: 6}
	if in.WindowDays != nil {
		s.WindowDays = *in.WindowDays
	}
	if in.FastBurn != nil {
		s.FastBurn = *in.FastBurn
	}
	if in.SlowBurn != nil {
		s.SlowBurn = *in.SlowBurn
	}
	f.slos[monitorID] = s
	return s, nil
}

func (f *fakeAPI) DeleteSLO(_ context.Context, monitorID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.slos[monitorID]; !ok || f.sloGone {
		return &upsctl.APIError{Method: "DELETE", Path: "/slo", Status: http.StatusNotFound}
	}
	if err := f.log("delete slo %s", f.monitorName(monitorID)); err != nil {
		return err
	}
	delete(f.slos, monitorID)
	return nil
}

// slo returns the SLO of the monitor called name.
func (f *fakeAPI) slo(name string) upsctl.SLO {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.monitors {
		if m.Name == name {
			return f.slos[m.ID]
		}
	}
	return upsctl.SLO{}
}

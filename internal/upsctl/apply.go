package upsctl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ApplyOptions tune Apply.
type ApplyOptions struct {
	Prune bool
	// Out receives the plan and one line per applied action.
	Out io.Writer
	// Confirm is asked before anything is written when the plan has
	// changes; nil means yes.
	Confirm func() (bool, error)
}

// Result counts what Apply changed.
type Result struct {
	Created, Updated, Deleted int
}

// Changed reports whether anything was written that changed the server.
func (r Result) Changed() bool { return r.Created+r.Updated+r.Deleted > 0 }

// Diff builds the plan for s against the live team and prints it. It
// returns whether applying would change anything.
func Diff(ctx context.Context, api API, s *Spec, prune bool, out io.Writer) (bool, error) {
	live, err := FetchLive(ctx, api)
	if err != nil {
		return false, err
	}
	plan, err := BuildPlan(s, live, PlanOptions{Prune: prune})
	if err != nil {
		return false, err
	}
	WritePlan(out, plan)
	return plan.HasChanges(), nil
}

// Apply makes the live team match s. Every pre-check runs before the first
// write. On an API error it stops at once; the lines already printed are
// what was applied.
func Apply(ctx context.Context, api API, s *Spec, opts ApplyOptions) (Result, error) {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	live, err := FetchLive(ctx, api)
	if err != nil {
		return Result{}, err
	}
	plan, err := BuildPlan(s, live, PlanOptions{Prune: opts.Prune})
	if err != nil {
		return Result{}, err
	}
	WritePlan(out, plan)
	if plan.HasChanges() && opts.Confirm != nil {
		ok, err := opts.Confirm()
		if err != nil {
			return Result{}, err
		}
		if !ok {
			return Result{}, fmt.Errorf("apply cancelled, nothing was changed")
		}
	}
	return execute(ctx, api, live, plan, out)
}

// ids resolves spec names to live ids while a plan runs; creates add to it.
type ids struct {
	channels, policies, monitors map[string]int64
	hosts                        map[string]int64
}

func newIDs(l *Live) *ids {
	x := &ids{channels: map[string]int64{}, policies: map[string]int64{}, monitors: map[string]int64{}, hosts: map[string]int64{}}
	for _, c := range l.Channels {
		x.channels[c.Name] = c.ID
	}
	for _, p := range l.Policies {
		x.policies[p.Name] = p.ID
	}
	for _, m := range l.Monitors {
		x.monitors[m.Name] = m.ID
	}
	for _, h := range l.DockerHosts {
		x.hosts[h.Name] = h.ID
	}
	return x
}

func lookupID(m map[string]int64, kind, name string) (int64, error) {
	id, ok := m[name]
	if !ok {
		return 0, fmt.Errorf("%s %q does not exist", kind, name)
	}
	return id, nil
}

func execute(ctx context.Context, api API, live *Live, plan *Plan, out io.Writer) (Result, error) {
	var res Result
	x := newIDs(live)
	for _, a := range plan.Actions {
		changed, err := run(ctx, api, x, a)
		if err != nil {
			fmt.Fprintf(out, "error: %s %s failed: %v\n", a.Op, a.label(), err)
			done := res.Created + res.Updated + res.Deleted
			return res, fmt.Errorf("apply stopped at %s %s after %d change(s) (listed above): %w", a.Op, a.label(), done, err)
		}
		switch {
		case a.Op == OpCreate:
			res.Created++
			fmt.Fprintf(out, "created %s\n", a.label())
		case a.Op == OpUpdate:
			res.Updated++
			fmt.Fprintf(out, "updated %s (%s)\n", a.label(), strings.Join(a.Fields, ", "))
		case a.Op == OpDelete:
			res.Deleted++
			fmt.Fprintf(out, "deleted %s\n", a.label())
		case a.Op == OpSync && changed:
			res.Updated++
			fmt.Fprintf(out, "updated %s (secrets)\n", a.label())
		}
	}
	if res.Changed() {
		fmt.Fprintf(out, "Applied: %d created, %d updated, %d deleted.\n", res.Created, res.Updated, res.Deleted)
	} else {
		fmt.Fprintln(out, "No changes applied.")
	}
	return res, nil
}

// run performs one action. For a channel secret sync, changed tells
// whether the server actually stored something new.
func run(ctx context.Context, api API, x *ids, a Action) (changed bool, err error) {
	switch a.Op {
	case OpDelete:
		switch a.Kind {
		case KindChannel:
			return true, api.DeleteChannel(ctx, a.ID)
		case KindPolicy:
			return true, api.DeletePolicy(ctx, a.ID)
		case KindMonitor:
			return true, api.DeleteMonitor(ctx, a.ID)
		case KindStatusPage:
			return true, api.DeleteStatusPage(ctx, a.ID)
		case KindSLO:
			// Already gone (deleted elsewhere meanwhile) is fine.
			if err := api.DeleteSLO(ctx, a.ID); err != nil && !IsNotFound(err) {
				return false, err
			}
			return true, nil
		}
		return false, fmt.Errorf("unknown kind %q", a.Kind)
	}

	switch a.Kind {
	case KindChannel:
		cfg, err := channelConfig(*a.channel)
		if err != nil {
			return false, err
		}
		req := ChannelRequest{Type: a.channel.Type, Name: a.channel.Name, Config: cfg, IsDefault: a.channel.IsDefault}
		if a.Op == OpCreate {
			c, err := api.CreateChannel(ctx, req)
			if err == nil {
				x.channels[c.Name] = c.ID
			}
			return true, err
		}
		c, err := api.UpdateChannel(ctx, a.ID, req)
		if err != nil {
			return false, err
		}
		return a.liveChannel == nil || !c.UpdatedAt.Equal(a.liveChannel.UpdatedAt), nil

	case KindPolicy:
		req := PolicyRequest{Name: a.policy.Name}
		for _, st := range a.policy.Steps {
			step := PolicyStep{DelayS: stepDelay(st.DelayS)}
			for _, c := range st.Channels {
				id, err := lookupID(x.channels, KindChannel, c)
				if err != nil {
					return false, err
				}
				step.ChannelIDs = append(step.ChannelIDs, id)
			}
			req.Steps = append(req.Steps, step)
		}
		if a.Op == OpCreate {
			p, err := api.CreatePolicy(ctx, req)
			if err == nil {
				x.policies[p.Name] = p.ID
			}
			return true, err
		}
		_, err := api.UpdatePolicy(ctx, a.ID, req)
		return true, err

	case KindMonitor:
		req, err := monitorRequest(*a.monitor, x)
		if err != nil {
			return false, err
		}
		if a.Op == OpCreate {
			m, err := api.CreateMonitor(ctx, req)
			if err == nil {
				x.monitors[m.Name] = m.ID
			}
			return true, err
		}
		_, err = api.UpdateMonitor(ctx, a.ID, req)
		return true, err

	case KindSLO:
		id, err := lookupID(x.monitors, KindMonitor, a.Name)
		if err != nil {
			return false, err
		}
		// All four values are sent, so the server's defaults never decide.
		e := a.slo
		_, err = api.PutSLO(ctx, id, SLOInput{Target: e.Target, WindowDays: new(e.WindowDays), FastBurn: new(e.FastBurn), SlowBurn: new(e.SlowBurn)})
		return true, err

	case KindStatusPage:
		p := a.page
		theme := p.Theme
		if theme == "" {
			theme = defaultTheme
		}
		// custom_domain is never sent: only an instance admin session may
		// change it, and leaving it out keeps the current value.
		req := StatusPageRequest{
			Slug: p.Slug, Title: p.Title, Description: p.Description, LogoURL: p.LogoURL,
			Theme: theme, Published: p.Published, Items: []StatusPageItem{},
		}
		for _, sec := range p.Sections {
			for _, it := range sec.Items {
				id, err := lookupID(x.monitors, KindMonitor, it.Monitor)
				if err != nil {
					return false, err
				}
				req.Items = append(req.Items, StatusPageItem{Section: sec.Name, MonitorID: id, DisplayName: it.DisplayName})
			}
		}
		if a.Op == OpCreate {
			_, err := api.CreateStatusPage(ctx, req)
			return true, err
		}
		_, err := api.UpdateStatusPage(ctx, a.ID, req)
		return true, err
	}
	return false, fmt.Errorf("unknown kind %q", a.Kind)
}

// monitorRequest resolves a spec monitor's names to ids and fills the
// server defaults, so an update replaces every field.
func monitorRequest(m MonitorSpec, x *ids) (MonitorRequest, error) {
	cfg := configMap(m.Config)
	if m.Type == "docker" {
		h, _ := cfg["docker_host"].(string)
		id, err := lookupID(x.hosts, "Docker host", h)
		if err != nil {
			return MonitorRequest{}, err
		}
		delete(cfg, "docker_host")
		cfg["docker_host_id"] = id
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return MonitorRequest{}, err
	}
	regions, quorum := desiredRegions(m)
	req := MonitorRequest{
		Name: m.Name, Type: m.Type, Config: raw,
		IntervalS: or(m.IntervalS, defaultIntervalS), RetryIntervalS: or(m.RetryIntervalS, defaultRetryIntervalS),
		Retries: new(or(m.Retries, defaultRetries)), TimeoutS: or(m.TimeoutS, defaultTimeoutS),
		Paused: m.Paused, GroupName: m.GroupName, Tags: m.Tags, Regions: regions, Quorum: new(quorum),
	}
	if m.Parent != "" {
		id, err := lookupID(x.monitors, KindMonitor, m.Parent)
		if err != nil {
			return MonitorRequest{}, err
		}
		req.ParentID = new(id)
	}
	if m.EscalationPolicy != "" {
		id, err := lookupID(x.policies, KindPolicy, m.EscalationPolicy)
		if err != nil {
			return MonitorRequest{}, err
		}
		req.EscalationPolicyID = new(id)
	}
	for _, c := range m.Channels {
		id, err := lookupID(x.channels, KindChannel, c)
		if err != nil {
			return MonitorRequest{}, err
		}
		req.ChannelIDs = append(req.ChannelIDs, id)
	}
	return req, nil
}

// Exit codes of `upsctl diff`.
const (
	ExitNoChanges = 0
	ExitError     = 1
	ExitChanges   = 2
)

// DiffExitCode maps a Diff result to the command's exit code.
func DiffExitCode(changed bool, err error) int {
	switch {
	case err != nil:
		return ExitError
	case changed:
		return ExitChanges
	}
	return ExitNoChanges
}

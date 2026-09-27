package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
	"github.com/davidsugianto/upsera/internal/notify"
)

// ---- fakes ----

type sent struct {
	Channel string
	Ev      notify.Event
}

type recorder struct {
	mu    sync.Mutex
	sends []sent
}

func (r *recorder) all() []sent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sends)
}

// matching returns the sends of kind to channel (any channel if "").
func (r *recorder) matching(kind model.AlertEvent, channel string) []sent {
	var out []sent
	for _, s := range r.all() {
		if s.Ev.Kind == kind && (channel == "" || s.Channel == channel) {
			out = append(out, s)
		}
	}
	return out
}

type fakeNotifier struct {
	typ string
	rec *recorder
}

func (f fakeNotifier) Type() string                   { return f.typ }
func (f fakeNotifier) Validate(json.RawMessage) error { return nil }
func (f fakeNotifier) Send(_ context.Context, _ json.RawMessage, ev notify.Event) error {
	f.rec.mu.Lock()
	f.rec.sends = append(f.rec.sends, sent{Channel: ev.ChannelName, Ev: ev})
	f.rec.mu.Unlock()
	return nil
}

type fakeStates struct {
	mu sync.Mutex
	m  map[int64]model.MonitorState
}

func (f *fakeStates) State(id int64) (model.MonitorState, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.m[id]
	return st, ok
}

func (f *fakeStates) set(id int64, st model.MonitorState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st.MonitorID = id
	f.m[id] = st
}

type fakeStore struct {
	mu       sync.Mutex
	fail     bool
	failLogs bool
	alerts   map[string]model.Alert
	logs     []model.NotificationLogEntry
}

func (f *fakeStore) UpsertAlerts(_ context.Context, as []model.Alert) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("db down")
	}
	for _, a := range as {
		f.alerts[a.ID] = a
	}
	return nil
}

func (f *fakeStore) InsertNotificationLog(_ context.Context, es []model.NotificationLogEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail || f.failLogs {
		return errors.New("db down")
	}
	f.logs = append(f.logs, es...)
	return nil
}

func (f *fakeStore) setFail(v bool) {
	f.mu.Lock()
	f.fail = v
	f.mu.Unlock()
}

// ---- harness ----

const (
	chTelegram int64 = 1
	chSMTP     int64 = 2
	policyID   int64 = 10
	teamID     int64 = 100
)

type harness struct {
	t      *testing.T
	e      *Engine
	rec    *recorder
	states *fakeStates
	store  *fakeStore
	clock  time.Time
}

func newHarness(t *testing.T, monitors []model.Monitor, open []model.Alert, seed func(*fakeStates)) *harness {
	t.Helper()
	h := &harness{
		t:      t,
		rec:    &recorder{},
		states: &fakeStates{m: make(map[int64]model.MonitorState)},
		store:  &fakeStore{alerts: make(map[string]model.Alert)},
		clock:  time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	h.e = New(Options{
		Store: h.store,
		Notifiers: map[model.ChannelType]notify.Notifier{
			model.ChannelTelegram: fakeNotifier{"telegram", h.rec},
			model.ChannelSMTP:     fakeNotifier{"smtp", h.rec},
		},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		FlushInterval: time.Hour,
	})
	h.e.now = func() time.Time { return h.clock }
	if seed != nil {
		seed(h.states)
	}
	// Configs that no ack listener accepts, so no poller ever starts.
	h.e.load(Snapshot{
		Monitors: monitors,
		Channels: []model.Channel{
			{ID: chTelegram, TeamID: teamID, Type: model.ChannelTelegram, Name: "telegram", Config: json.RawMessage(`{}`)},
			{ID: chSMTP, TeamID: teamID, Type: model.ChannelSMTP, Name: "smtp", Config: json.RawMessage(`{}`)},
		},
		Policies: []model.EscalationPolicy{{ID: policyID, TeamID: teamID, Name: "p", Steps: []model.EscalationStep{
			{ChannelIDs: []int64{chTelegram}, DelayS: 10},
			{ChannelIDs: []int64{chSMTP}, DelayS: 600},
		}}},
		OpenAlerts: open,
	}, h.states)
	t.Cleanup(func() { h.e.disp.Close(context.Background()) })
	return h
}

func monitor(id int64, name string, parent *int64) model.Monitor {
	return model.Monitor{ID: id, TeamID: teamID, Name: name, Type: model.TypeHTTP, ParentID: parent,
		EscalationPolicyID: new(policyID)}
}

// transition sets id's live state to `to` and applies the transition
// synchronously, as the event loop would.
func (h *harness) transition(id int64, from, to model.Status) {
	h.t.Helper()
	h.states.set(id, model.MonitorState{Status: to, Since: h.clock})
	h.e.Publish(model.Transition{MonitorID: id, From: from, To: to, At: h.clock, Since: h.clock, Message: "refused"})
	h.e.drain()
}

func (h *harness) advance(d time.Duration) {
	h.clock = h.clock.Add(d)
	h.e.tick(h.clock)
}

func (h *harness) alert(monitorID int64) *model.Alert {
	h.e.mu.Lock()
	defer h.e.mu.Unlock()
	a := h.e.alerts[monitorID]
	if a == nil {
		return nil
	}
	c := copyAlert(a)
	return &c
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

// quiet waits long enough for any stray delivery to land.
func quiet() { time.Sleep(100 * time.Millisecond) }

// ---- tests ----

func TestEscalatesFromTelegramToEmail(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.transition(1, model.StatusPending, model.StatusDown)
	eventually(t, func() bool { return len(h.rec.matching(model.EventDown, "telegram")) == 1 })
	d := h.rec.matching(model.EventDown, "telegram")[0].Ev
	if !d.Ackable || d.AlertID == "" || d.MonitorName != "api" || d.Message != "refused" {
		t.Fatalf("down event = %+v", d)
	}

	h.advance(9 * time.Second)
	quiet()
	if n := len(h.rec.matching(model.EventDown, "smtp")); n != 0 {
		t.Fatalf("smtp fired %d times before the 10s delay", n)
	}
	h.advance(time.Second)
	eventually(t, func() bool { return len(h.rec.matching(model.EventDown, "smtp")) == 1 })
	a := h.alert(1)
	if a.Step != 1 || a.NextEscalationAt != nil || !slices.Equal(a.NotifiedChannelIDs, []int64{chTelegram, chSMTP}) {
		t.Fatalf("alert after last step = %+v", a)
	}
}

func TestAcknowledgeStopsEscalation(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.transition(1, model.StatusUp, model.StatusDown)
	id := h.alert(1).ID

	h.advance(5 * time.Second)
	got, err := h.e.Acknowledge(teamID, id, model.AckBy{UserID: new(int64(7)), Source: model.AckWeb, Name: "ann"})
	if err != nil || got.AckedAt == nil || got.AckSource != model.AckWeb || got.AckedByName != "ann" {
		t.Fatalf("Acknowledge = %+v, %v", got, err)
	}
	if _, err := h.e.Acknowledge(teamID+1, id, model.AckBy{Source: model.AckWeb}); !errors.Is(err, ErrAlertNotFound) {
		t.Fatalf("other team ack: err = %v", err)
	}
	again, err := h.e.Acknowledge(0, id, model.AckBy{Source: model.AckTelegram, Name: "bob"})
	if err != nil || again.AckedByName != "ann" {
		t.Fatalf("second ack changed the first: %+v, %v", again, err)
	}
	h.advance(time.Hour)
	quiet()
	if n := len(h.rec.matching(model.EventDown, "smtp")); n != 0 {
		t.Fatalf("acknowledged alert escalated to smtp (%d sends)", n)
	}
}

func TestParentDownSuppressesChild(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "parent", nil), monitor(2, "child", new(int64(1)))}, nil, nil)
	h.transition(1, model.StatusUp, model.StatusDown)
	h.transition(2, model.StatusUp, model.StatusDown)
	quiet()
	for _, s := range h.rec.all() {
		if s.Ev.MonitorName == "child" {
			t.Fatalf("child notified while parent is down: %+v", s)
		}
	}
	if a := h.alert(2); a == nil || !a.Suppressed {
		t.Fatalf("child alert = %+v, want suppressed", a)
	}
	h.advance(time.Hour)
	quiet()
	for _, s := range h.rec.all() {
		if s.Ev.MonitorName == "child" {
			t.Fatalf("suppressed child escalated: %+v", s)
		}
	}
}

func TestParentDownStopsChildEscalation(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "parent", nil), monitor(2, "child", new(int64(1)))}, nil, nil)
	h.transition(2, model.StatusUp, model.StatusDown)
	eventually(t, func() bool { return len(h.rec.matching(model.EventDown, "telegram")) == 1 })
	h.transition(1, model.StatusUp, model.StatusDown)
	if a := h.alert(2); !a.Suppressed || a.NextEscalationAt != nil {
		t.Fatalf("child alert = %+v, want suppressed without escalation", a)
	}
	h.advance(time.Minute)
	quiet()
	for _, s := range h.rec.matching(model.EventDown, "smtp") {
		if s.Ev.MonitorName == "child" {
			t.Fatal("child escalated to smtp after its parent went down")
		}
	}
}

func TestParentRecoveryReleasesChild(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "parent", nil), monitor(2, "child", new(int64(1)))}, nil, nil)
	h.transition(1, model.StatusUp, model.StatusDown)
	h.transition(2, model.StatusUp, model.StatusDown)
	h.transition(1, model.StatusDown, model.StatusUp)
	eventually(t, func() bool {
		for _, s := range h.rec.matching(model.EventDown, "telegram") {
			if s.Ev.MonitorName == "child" {
				return true
			}
		}
		return false
	})
	if a := h.alert(2); a.Suppressed || a.Step != 0 {
		t.Fatalf("child alert = %+v, want released at step 0", a)
	}
}

func TestRecoveryNotifiesExactlyNotifiedChannels(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.transition(1, model.StatusUp, model.StatusDown)
	id := h.alert(1).ID
	h.advance(5 * time.Second) // before step 1 (smtp) is due
	h.transition(1, model.StatusDown, model.StatusUp)
	if h.alert(1) != nil {
		t.Fatal("alert still open after recovery")
	}
	eventually(t, func() bool { return len(h.rec.matching(model.EventRecovered, "")) == 1 })
	quiet()
	rec := h.rec.matching(model.EventRecovered, "")
	if rec[0].Channel != "telegram" || rec[0].Ev.AlertID != id || rec[0].Ev.Downtime != 5*time.Second {
		t.Fatalf("recovered sends = %+v, want one to telegram with 5s downtime", rec)
	}
}

func TestFlappingHoldsBackAlerts(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.states.set(1, model.MonitorState{Status: model.StatusDown, FlapCount: 5})
	h.e.Publish(model.Transition{MonitorID: 1, From: model.StatusUp, To: model.StatusDown, At: h.clock,
		Flapping: true, FlapChanged: true})
	h.e.drain()
	for _, to := range []model.Status{model.StatusUp, model.StatusDown, model.StatusUp} {
		h.states.set(1, model.MonitorState{Status: to})
		h.e.Publish(model.Transition{MonitorID: 1, To: to, At: h.clock, Flapping: true})
		h.e.drain()
	}
	eventually(t, func() bool { return len(h.rec.all()) >= 1 })
	quiet()
	all := h.rec.all()
	if len(all) != 1 || all[0].Ev.Kind != model.EventFlapping || all[0].Ev.FlapCount != 5 || all[0].Ev.Ackable {
		t.Fatalf("sends = %+v, want exactly one flapping event", all)
	}
	if h.alert(1) != nil {
		t.Fatal("alert opened while flapping")
	}
}

func TestMaintenanceResolvesSilently(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.transition(1, model.StatusUp, model.StatusDown)
	eventually(t, func() bool { return len(h.rec.all()) == 1 })
	id := h.alert(1).ID
	h.transition(1, model.StatusDown, model.StatusMaintenance)
	quiet()
	if len(h.rec.all()) != 1 {
		t.Fatalf("maintenance sent notifications: %+v", h.rec.all())
	}
	if h.alert(1) != nil {
		t.Fatal("alert still open in maintenance")
	}
	h.e.mu.Lock()
	a := h.e.byID[id]
	h.e.mu.Unlock()
	if a == nil || a.Resolution != model.ResolutionMaintenance {
		t.Fatalf("resolved alert = %+v", a)
	}
}

func TestStartResolvesAlertsOfRecoveredMonitors(t *testing.T) {
	start := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)
	open := model.Alert{ID: "0190aaaa-0000-7000-8000-000000000001", TeamID: teamID, MonitorID: 1,
		IncidentStart: start, OpenedAt: start, Step: 0, NotifiedChannelIDs: []int64{chTelegram}}
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, []model.Alert{open}, func(s *fakeStates) {
		s.set(1, model.MonitorState{Status: model.StatusUp, Since: start.Add(20 * time.Minute)})
	})
	eventually(t, func() bool { return len(h.rec.matching(model.EventRecovered, "telegram")) == 1 })
	if d := h.rec.matching(model.EventRecovered, "telegram")[0].Ev.Downtime; d != 20*time.Minute {
		t.Fatalf("downtime = %v, want 20m", d)
	}
	if h.alert(1) != nil {
		t.Fatal("alert still open")
	}
}

func TestCertExpiryWarnsOncePerThreshold(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	exp := h.clock.Add(6*24*time.Hour + 12*time.Hour)
	h.states.set(1, model.MonitorState{Status: model.StatusUp, TLSExpiresAt: &exp})
	h.e.certScan(h.clock)
	h.e.certScan(h.clock.Add(10 * time.Minute))
	eventually(t, func() bool { return len(h.rec.all()) >= 1 })
	quiet()
	all := h.rec.all()
	if len(all) != 1 || all[0].Ev.Kind != model.EventCertExpiry || all[0].Ev.CertDaysLeft != 6 || all[0].Channel != "telegram" {
		t.Fatalf("sends = %+v, want one cert event to step-0 channel", all)
	}
	h.e.mu.Lock()
	var keys []string
	for k := range h.e.certSent {
		keys = append(keys, k)
	}
	h.e.mu.Unlock()
	if len(keys) != 1 || !strings.HasSuffix(keys[0], ":7") {
		t.Fatalf("cert keys = %v, want one ending in :7", keys)
	}
}

func TestSendsSurviveStoreOutage(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.store.setFail(true)
	h.transition(1, model.StatusUp, model.StatusDown)
	eventually(t, func() bool { return len(h.rec.all()) == 1 })
	eventually(t, func() bool {
		h.e.outMu.Lock()
		defer h.e.outMu.Unlock()
		return len(h.e.logs) == 1
	})
	if err := h.e.flush(context.Background()); err == nil {
		t.Fatal("flush succeeded against a failing store")
	}
	h.transition(1, model.StatusDown, model.StatusUp)
	eventually(t, func() bool { return len(h.rec.all()) == 2 })
	eventually(t, func() bool {
		h.e.outMu.Lock()
		defer h.e.outMu.Unlock()
		return len(h.e.logs) == 2
	})

	h.store.setFail(false)
	if err := h.e.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if len(h.store.alerts) != 1 || len(h.store.logs) != 2 {
		t.Fatalf("store after recovery: %d alerts, %d logs; want 1, 2", len(h.store.alerts), len(h.store.logs))
	}
	for _, a := range h.store.alerts {
		if a.ResolvedAt == nil || a.Resolution != model.ResolutionRecovered {
			t.Fatalf("stored alert = %+v, want resolved", a)
		}
	}
	h.e.mu.Lock()
	defer h.e.mu.Unlock()
	if len(h.e.byID) != 0 {
		t.Fatalf("resolved alert kept in memory after flush: %d", len(h.e.byID))
	}
}

// childNotified reports whether the child monitor got a DOWN on telegram.
func (h *harness) childNotified() bool {
	for _, s := range h.rec.matching(model.EventDown, "telegram") {
		if s.Ev.MonitorName == "child" {
			return true
		}
	}
	return false
}

func TestParentMaintenanceReleasesChild(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "parent", nil), monitor(2, "child", new(int64(1)))}, nil, nil)
	h.transition(1, model.StatusUp, model.StatusDown)
	h.transition(2, model.StatusUp, model.StatusDown)
	h.transition(1, model.StatusDown, model.StatusMaintenance)
	eventually(t, h.childNotified)
	if a := h.alert(2); a.Suppressed || a.Step != 0 {
		t.Fatalf("child alert = %+v, want released at step 0", a)
	}
}

func TestRemovingOrDetachingParentReleasesChild(t *testing.T) {
	for _, tc := range []struct {
		name   string
		detach func(h *harness)
	}{
		{"parent deleted", func(h *harness) { h.e.RemoveMonitor(1) }},
		{"child re-parented", func(h *harness) { h.e.UpsertMonitor(monitor(2, "child", nil)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, []model.Monitor{monitor(1, "parent", nil), monitor(2, "child", new(int64(1)))}, nil, nil)
			h.transition(1, model.StatusUp, model.StatusDown)
			h.transition(2, model.StatusUp, model.StatusDown)
			if !h.alert(2).Suppressed {
				t.Fatal("child alert not suppressed")
			}
			tc.detach(h)
			eventually(t, h.childNotified)
			if a := h.alert(2); a.Suppressed {
				t.Fatalf("child alert = %+v, want released", a)
			}
		})
	}
}

func TestReparentingUnderDownParentSuppresses(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "parent", nil), monitor(2, "child", nil)}, nil, nil)
	h.transition(1, model.StatusUp, model.StatusDown)
	h.transition(2, model.StatusUp, model.StatusDown)
	h.e.UpsertMonitor(monitor(2, "child", new(int64(1))))
	if a := h.alert(2); !a.Suppressed || a.NextEscalationAt != nil {
		t.Fatalf("child alert = %+v, want suppressed", a)
	}
}

func TestFlappingEndingInPendingResumesOnDown(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.transition(1, model.StatusUp, model.StatusDown)
	eventually(t, func() bool { return len(h.rec.matching(model.EventDown, "telegram")) == 1 })
	pub := func(tr model.Transition) {
		h.states.set(1, model.MonitorState{Status: tr.To})
		tr.MonitorID, tr.At = 1, h.clock
		h.e.Publish(tr)
		h.e.drain()
	}
	pub(model.Transition{From: model.StatusDown, To: model.StatusUp, Flapping: true, FlapChanged: true})
	pub(model.Transition{From: model.StatusUp, To: model.StatusPending, FlapChanged: true})
	pub(model.Transition{From: model.StatusPending, To: model.StatusDown})
	eventually(t, func() bool { return len(h.rec.matching(model.EventDown, "telegram")) == 2 })
	if a := h.alert(1); a.Flapping || a.NextEscalationAt == nil {
		t.Fatalf("alert = %+v, want escalating again", a)
	}
}

func TestFlappingEndingDoesNotRenotifyAckedAlert(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.transition(1, model.StatusUp, model.StatusDown)
	if _, err := h.e.Acknowledge(teamID, h.alert(1).ID, model.AckBy{Source: model.AckWeb, Name: "ann"}); err != nil {
		t.Fatal(err)
	}
	for _, tr := range []model.Transition{
		{From: model.StatusDown, To: model.StatusUp, Flapping: true, FlapChanged: true},
		{From: model.StatusUp, To: model.StatusDown, FlapChanged: true},
	} {
		h.states.set(1, model.MonitorState{Status: tr.To})
		tr.MonitorID, tr.At = 1, h.clock
		h.e.Publish(tr)
		h.e.drain()
	}
	quiet()
	if n := len(h.rec.matching(model.EventDown, "")); n != 1 {
		t.Fatalf("DOWN sends = %d, want only the first (alert is acknowledged)", n)
	}
}

func TestDeletedChannelFallsBackToDefaults(t *testing.T) {
	m := model.Monitor{ID: 1, TeamID: teamID, Name: "api", Type: model.TypeHTTP, ChannelIDs: []int64{chSMTP}}
	h := newHarness(t, []model.Monitor{m}, nil, nil)
	h.e.UpsertChannel(model.Channel{ID: 3, TeamID: teamID, Type: model.ChannelTelegram, Name: "default",
		Config: json.RawMessage(`{}`), IsDefault: true})
	h.e.RemoveChannel(chSMTP)
	h.transition(1, model.StatusUp, model.StatusDown)
	eventually(t, func() bool { return len(h.rec.matching(model.EventDown, "default")) == 1 })
}

func TestAcknowledgeResolvedAlert(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.transition(1, model.StatusUp, model.StatusDown)
	id := h.alert(1).ID
	h.transition(1, model.StatusDown, model.StatusUp)
	if _, err := h.e.Acknowledge(teamID, id, model.AckBy{Source: model.AckWeb}); !errors.Is(err, ErrAlertResolved) {
		t.Fatalf("ack of resolved alert: err = %v, want ErrAlertResolved", err)
	}
}

func TestResolvedAlertForgottenWhenOnlyLogInsertFails(t *testing.T) {
	h := newHarness(t, []model.Monitor{monitor(1, "api", nil)}, nil, nil)
	h.transition(1, model.StatusUp, model.StatusDown)
	h.transition(1, model.StatusDown, model.StatusUp)
	eventually(t, func() bool { return len(h.rec.all()) == 2 })
	eventually(t, func() bool {
		h.e.outMu.Lock()
		defer h.e.outMu.Unlock()
		return len(h.e.logs) == 2
	})
	h.store.mu.Lock()
	h.store.failLogs = true
	h.store.mu.Unlock()
	if err := h.e.flush(context.Background()); err == nil {
		t.Fatal("flush succeeded with a failing log insert")
	}
	h.e.mu.Lock()
	n := len(h.e.byID)
	h.e.mu.Unlock()
	if n != 0 {
		t.Fatalf("resolved alert kept in memory after it was written: %d", n)
	}
}

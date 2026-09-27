package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// fakeStore is an in-memory jobs.Store double that records call order and
// lets each step's error be switched independently.
type fakeStore struct {
	mu sync.Mutex

	settings    model.InstanceSettings
	settingsErr error
	rollupErr   error
	pruneErr    error
	sessionsErr error

	order        []string
	pruneCalls   []time.Time
	rollupCalls  int
	pruneCalls_  int
	sessionCalls int
}

func (f *fakeStore) GetInstanceSettings(context.Context) (model.InstanceSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "settings")
	return f.settings, f.settingsErr
}

func (f *fakeStore) Rollup(context.Context, string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "rollup")
	f.rollupCalls++
	return 7, f.rollupErr
}

func (f *fakeStore) PruneHeartbeats(_ context.Context, before time.Time, batch int, tz string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "prune")
	f.pruneCalls_++
	f.pruneCalls = append(f.pruneCalls, before)
	return 3, f.pruneErr
}

func (f *fakeStore) DeleteExpiredSessions(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "sessions")
	f.sessionCalls++
	return 2, f.sessionsErr
}

func (f *fakeStore) PruneNotificationLog(_ context.Context, before time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "notification_log")
	return 1, nil
}

func (f *fakeStore) callOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.order))
	copy(out, f.order)
	return out
}

func runOnce(t *testing.T, st *fakeStore, opts Options) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	// Every is set far in the future so only the immediate run fires
	// before we cancel.
	opts.Every = time.Hour
	done := make(chan struct{})
	go func() {
		Run(ctx, st, opts)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Run did not return after ctx cancellation")
	}
}

func TestRunOrderRollupPruneSessions(t *testing.T) {
	st := &fakeStore{}
	runOnce(t, st, Options{DefaultRetentionDays: 14, TZ: "UTC"})

	order := st.callOrder()
	want := []string{"settings", "rollup", "prune", "sessions", "notification_log"}
	if len(order) != len(want) {
		t.Fatalf("call order = %v, want %v", order, want)
	}
	for i, w := range want {
		if order[i] != w {
			t.Fatalf("call order = %v, want %v", order, want)
		}
	}
}

func TestRunPruneSkippedWhenRollupFails(t *testing.T) {
	st := &fakeStore{rollupErr: errors.New("rollup boom")}
	runOnce(t, st, Options{DefaultRetentionDays: 14, TZ: "UTC"})

	order := st.callOrder()
	for _, step := range order {
		if step == "prune" {
			t.Fatalf("prune ran despite rollup failure: order=%v", order)
		}
	}
	want := []string{"settings", "rollup", "sessions", "notification_log"}
	if len(order) != len(want) {
		t.Fatalf("call order = %v, want %v", order, want)
	}
	for i, w := range want {
		if order[i] != w {
			t.Fatalf("call order = %v, want %v", order, want)
		}
	}
}

func TestRunRetentionFromSettingsOverridesDefault(t *testing.T) {
	custom := 30
	st := &fakeStore{settings: model.InstanceSettings{RetentionDays: &custom}}
	runOnce(t, st, Options{DefaultRetentionDays: 14, TZ: "UTC"})

	if len(st.pruneCalls) != 1 {
		t.Fatalf("prune calls = %d, want 1", len(st.pruneCalls))
	}
	before := st.pruneCalls[0]
	wantBefore := retentionCutoff("UTC", 30)
	if !before.Equal(wantBefore) {
		t.Fatalf("prune before = %s, want %s (30-day retention, UTC midnight)", before, wantBefore)
	}
}

func TestRunRetentionFallsBackToDefaultOnSettingsError(t *testing.T) {
	st := &fakeStore{settingsErr: errors.New("settings boom")}
	runOnce(t, st, Options{DefaultRetentionDays: 5, TZ: "UTC"})

	if len(st.pruneCalls) != 1 {
		t.Fatalf("prune calls = %d, want 1", len(st.pruneCalls))
	}
	before := st.pruneCalls[0]
	wantBefore := retentionCutoff("UTC", 5)
	if !before.Equal(wantBefore) {
		t.Fatalf("prune before = %s, want %s (default 5-day retention, UTC midnight)", before, wantBefore)
	}
}

// TestRetentionCutoffIsLocalMidnight guards against the cutoff drifting
// with the time of day the job happens to run: it must always land exactly
// on a calendar-day boundary in the configured time zone, not N*24h before
// whatever instant the job started (which slowly creeps earlier every day
// the job is a little late, and can prune less than a full day's slack).
func TestRetentionCutoffIsLocalMidnight(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Fatal(err)
	}
	got := retentionCutoff("Asia/Jakarta", 5)
	wantDay := time.Now().In(loc).AddDate(0, 0, -5)
	want := time.Date(wantDay.Year(), wantDay.Month(), wantDay.Day(), 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("retentionCutoff = %s, want %s (local midnight)", got, want)
	}
	if h, m, s := got.Clock(); h != 0 || m != 0 || s != 0 {
		t.Fatalf("retentionCutoff = %s, not aligned to local midnight", got)
	}
}

func TestRunOnSettingsInvokedOnSuccess(t *testing.T) {
	st := &fakeStore{settings: model.InstanceSettings{BlockPrivateTargets: true}}
	var got model.InstanceSettings
	var calls int
	var mu sync.Mutex
	runOnce(t, st, Options{
		DefaultRetentionDays: 14,
		TZ:                   "UTC",
		OnSettings: func(s model.InstanceSettings) {
			mu.Lock()
			defer mu.Unlock()
			got = s
			calls++
		},
	})

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("OnSettings calls = %d, want 1", calls)
	}
	if !got.BlockPrivateTargets {
		t.Fatalf("OnSettings received %+v, want BlockPrivateTargets=true", got)
	}
}

func TestRunOnSettingsNotInvokedOnError(t *testing.T) {
	st := &fakeStore{settingsErr: errors.New("settings boom")}
	var calls int
	var mu sync.Mutex
	runOnce(t, st, Options{
		DefaultRetentionDays: 14,
		TZ:                   "UTC",
		OnSettings: func(model.InstanceSettings) {
			mu.Lock()
			defer mu.Unlock()
			calls++
		},
	})

	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("OnSettings calls = %d, want 0 when settings failed to load", calls)
	}
}

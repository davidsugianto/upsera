package maintenance

import (
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

func TestActiveAt(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	utc := time.UTC
	start := time.Date(2024, 1, 1, 22, 0, 0, 0, utc)
	win := func(r model.Recurrence) model.MaintenanceWindow {
		return model.MaintenanceWindow{StartsAt: start, EndsAt: start.Add(2 * time.Hour), Recurrence: r}
	}
	// 02:30 local on the day before the 2024-03-10 spring-forward.
	dstStart := time.Date(2024, 3, 9, 2, 30, 0, 0, ny)
	dstWin := model.MaintenanceWindow{StartsAt: dstStart, EndsAt: dstStart.Add(time.Hour), Recurrence: model.RecurDaily}

	cases := []struct {
		name string
		w    model.MaintenanceWindow
		loc  *time.Location
		t    time.Time
		want bool
	}{
		{"none before start", win(model.RecurNone), utc, start.Add(-time.Second), false},
		{"none at start", win(model.RecurNone), utc, start, true},
		{"none inside", win(model.RecurNone), utc, start.Add(time.Hour), true},
		{"none at end", win(model.RecurNone), utc, start.Add(2 * time.Hour), false},
		{"daily before start", win(model.RecurDaily), utc, start.Add(-time.Hour), false},
		{"daily 3rd occurrence", win(model.RecurDaily), utc, start.AddDate(0, 0, 2).Add(90 * time.Minute), true},
		{"daily between occurrences", win(model.RecurDaily), utc, start.AddDate(0, 0, 2).Add(3 * time.Hour), false},
		{"daily occurrence spans midnight", win(model.RecurDaily), utc, start.AddDate(0, 0, 3).Add(time.Hour + 59*time.Minute), true},
		{"weekly 3rd occurrence", win(model.RecurWeekly), utc, start.AddDate(0, 0, 14).Add(time.Hour), true},
		{"weekly off day", win(model.RecurWeekly), utc, start.AddDate(0, 0, 15).Add(time.Hour), false},
		{"dst: same wall clock after spring-forward", dstWin, ny, time.Date(2024, 3, 11, 2, 45, 0, 0, ny), true},
		{"dst: not an hour late after spring-forward", dstWin, ny, time.Date(2024, 3, 11, 3, 45, 0, 0, ny), false},
		{"dst: before the local start", dstWin, ny, time.Date(2024, 3, 11, 2, 15, 0, 0, ny), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ActiveAt(tc.w, tc.loc, tc.t); got != tc.want {
				t.Fatalf("ActiveAt(%v) = %v, want %v", tc.t, got, tc.want)
			}
		})
	}
}

func TestRegistryActive(t *testing.T) {
	now := time.Now()
	r := NewRegistry(time.UTC)
	r.Set([]model.MaintenanceWindow{
		{ID: 2, Name: "second", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), Recurrence: model.RecurNone, MonitorIDs: []int64{7}},
		{ID: 1, Name: "first", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), Recurrence: model.RecurNone, MonitorIDs: []int64{7}},
	})
	if name, ok := r.Active(7, now); !ok || name != "first" {
		t.Fatalf("Active = %q %v, want lowest id window", name, ok)
	}
	if _, ok := r.Active(8, now); ok {
		t.Fatal("monitor not in any window reported active")
	}
	r.Remove(1)
	r.Remove(2)
	if _, ok := r.Active(7, now); ok {
		t.Fatal("removed window still active")
	}
}

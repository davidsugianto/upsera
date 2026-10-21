package model_test

import (
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

func TestTruncateMessage(t *testing.T) {
	t.Run("short message unchanged", func(t *testing.T) {
		if got := model.TruncateMessage("ok"); got != "ok" {
			t.Fatalf("got %q, want %q", got, "ok")
		}
	})

	t.Run("NUL bytes stripped", func(t *testing.T) {
		got := model.TruncateMessage("hello\x00world")
		if got != "helloworld" {
			t.Fatalf("got %q, want %q", got, "helloworld")
		}
	})

	t.Run("invalid UTF-8 replaced", func(t *testing.T) {
		got := model.TruncateMessage("bad\xffbyte")
		want := "bad\uFFFDbyte"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("sanitized before truncation, not after", func(t *testing.T) {
		// 50 NUL bytes followed by exactly MaxMessageLen 'a's: the raw rune
		// count (250) exceeds MaxMessageLen, but once the NULs are stripped
		// the remaining 200 'a's fit exactly and must not be truncated or
		// carry any NUL bytes into the stored message. Truncating before
		// sanitizing would instead keep the leading NULs and cut valid
		// content short.
		msg := strings.Repeat("\x00", 50) + strings.Repeat("a", model.MaxMessageLen)
		got := model.TruncateMessage(msg)
		want := strings.Repeat("a", model.MaxMessageLen)
		if got != want {
			t.Fatalf("got %d runes (contains NUL: %v), want %d clean 'a' runes",
				len([]rune(got)), strings.ContainsRune(got, 0), model.MaxMessageLen)
		}
	})

	t.Run("still truncates long sanitized messages", func(t *testing.T) {
		got := model.TruncateMessage(strings.Repeat("a", model.MaxMessageLen+50))
		r := []rune(got)
		if len(r) != model.MaxMessageLen {
			t.Fatalf("len = %d, want %d", len(r), model.MaxMessageLen)
		}
		if r[len(r)-1] != '…' {
			t.Fatalf("last rune = %q, want ellipsis", r[len(r)-1])
		}
	})
}

func TestStatusDegraded(t *testing.T) {
	if model.StatusDegraded != 4 || model.StatusDegraded.String() != "degraded" {
		t.Fatalf("StatusDegraded = %d %q", model.StatusDegraded, model.StatusDegraded.String())
	}
	var s model.Status
	if err := s.UnmarshalText([]byte("degraded")); err != nil || s != model.StatusDegraded {
		t.Fatalf("UnmarshalText(degraded) = %v, %v", s, err)
	}
	enum := model.StatusDegraded.Schema(nil).Enum
	found := false
	for _, v := range enum {
		found = found || v == "degraded"
	}
	if !found {
		t.Fatalf("schema enum %v lacks degraded", enum)
	}
	for _, e := range []model.AlertEvent{model.EventDegraded, model.EventLatencyRecovered} {
		if !e.Valid() {
			t.Fatalf("%q not in AlertEvents", e)
		}
	}
	if model.EventDegraded != "degraded" || model.EventLatencyRecovered != "latency_recovered" {
		t.Fatal("event names changed")
	}
}

func TestSLOModel(t *testing.T) {
	for _, e := range []model.AlertEvent{model.EventSLOBurn, model.EventSLOBurnRecovered} {
		if !e.Valid() {
			t.Fatalf("%q not in AlertEvents", e)
		}
	}
	if model.EventSLOBurn != "slo_burn" || model.EventSLOBurnRecovered != "slo_burn_recovered" {
		t.Fatal("event names changed")
	}
	enum := model.SLOFiringNone.Schema(nil).Enum
	if len(enum) != 3 || enum[0] != "none" || enum[1] != "slow" || enum[2] != "fast" {
		t.Fatalf("firing enum = %v", enum)
	}
	since := new(time.Now())
	cases := []struct {
		fast, slow *time.Time
		want       model.SLOFiring
	}{{nil, nil, model.SLOFiringNone}, {nil, since, model.SLOFiringSlow}, {since, nil, model.SLOFiringFast}, {since, since, model.SLOFiringFast}}
	for _, c := range cases {
		if got := (model.SLO{FastFiringSince: c.fast, SlowFiringSince: c.slow}).Firing(); got != c.want {
			t.Errorf("Firing(%v, %v) = %q, want %q", c.fast != nil, c.slow != nil, got, c.want)
		}
	}
}

func TestRawDaysStart(t *testing.T) {
	jkt, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Fatal(err)
	}
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		now  time.Time
		loc  *time.Location
		want time.Time
	}{
		// 2026-03-10 01:30 in Jakarta is still 2026-03-09 in UTC.
		{time.Date(2026, 3, 9, 18, 30, 0, 0, time.UTC), jkt, time.Date(2026, 3, 9, 0, 0, 0, 0, jkt)},
		{time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC), time.UTC, time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)},
		// Across the DST change (2026-03-08 in New York): midnight of yesterday.
		{time.Date(2026, 3, 9, 12, 0, 0, 0, ny), ny, time.Date(2026, 3, 8, 0, 0, 0, 0, ny)},
		// First of the month: yesterday is in the previous month.
		{time.Date(2026, 3, 1, 9, 0, 0, 0, jkt), jkt, time.Date(2026, 2, 28, 0, 0, 0, 0, jkt)},
	}
	for _, c := range cases {
		if got := model.RawDaysStart(c.now, c.loc); !got.Equal(c.want) {
			t.Errorf("RawDaysStart(%v, %v) = %v, want %v", c.now, c.loc, got, c.want)
		}
	}
}

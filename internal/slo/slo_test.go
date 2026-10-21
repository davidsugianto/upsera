package slo

import (
	"math"
	"testing"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestMaths(t *testing.T) {
	type want struct {
		sli, consumed, burn float64
		ok                  bool
	}
	cases := []struct {
		name   string
		c      model.SLICount
		target float64
		want   want
	}{
		{"no data", model.SLICount{}, 99.9, want{ok: false}},
		{"only maintenance (no valid)", model.SLICount{Good: 0, Valid: 0}, 99.9, want{ok: false}},
		{"perfect", model.SLICount{Good: 1000, Valid: 1000}, 99.9, want{1, 0, 0, true}},
		{"exactly the budget at 99.9", model.SLICount{Good: 999, Valid: 1000}, 99.9, want{0.999, 1, 1, true}},
		{"half the budget at 99.99", model.SLICount{Good: 19999, Valid: 20000}, 99.99, want{0.99995, 0.5, 0.5, true}},
		{"consumed above 1", model.SLICount{Good: 990, Valid: 1000}, 99.9, want{0.99, 10, 10, true}},
		{"all down", model.SLICount{Good: 0, Valid: 10}, 99.9, want{0, 1000, 1000, true}},
		{"burn scales with the budget", model.SLICount{Good: 98, Valid: 100}, 99, want{0.98, 2, 2, true}},
		{"low target", model.SLICount{Good: 50, Valid: 100}, 0.001, want{0.5, 0.5 / 0.99999, 0.5 / 0.99999, true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sli, ok1 := SLI(c.c)
			cons, ok2 := Consumed(c.c, c.target)
			burn, ok3 := BurnRate(c.c, c.target)
			if ok1 != c.want.ok || ok2 != c.want.ok || ok3 != c.want.ok {
				t.Fatalf("ok = %v %v %v, want %v", ok1, ok2, ok3, c.want.ok)
			}
			if !c.want.ok {
				return
			}
			if !approx(sli, c.want.sli) || !approx(cons, c.want.consumed) || !approx(burn, c.want.burn) {
				t.Fatalf("sli %v consumed %v burn %v, want %+v", sli, cons, burn, c.want)
			}
		})
	}
}

func TestMathsNeverNaNOrInf(t *testing.T) {
	counts := []model.SLICount{{}, {Good: 0, Valid: 1}, {Good: 1, Valid: 1}, {Good: 5, Valid: 3}, {Good: -1, Valid: 0}}
	targets := []float64{0, 100, -5, 150, math.NaN(), math.Inf(1), 99.999, 50}
	for _, c := range counts {
		for _, tg := range targets {
			for name, f := range map[string]func(model.SLICount, float64) (float64, bool){"Consumed": Consumed, "BurnRate": BurnRate} {
				v, ok := f(c, tg)
				if ok && (math.IsNaN(v) || math.IsInf(v, 0)) {
					t.Fatalf("%s(%+v, %v) = %v", name, c, tg, v)
				}
				if (tg <= 0 || tg >= 100 || math.IsNaN(tg)) && ok {
					t.Fatalf("%s(%+v, %v) ok for an invalid target", name, c, tg)
				}
			}
			if v, ok := SLI(c); ok && (math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1) {
				t.Fatalf("SLI(%+v) = %v", c, v)
			}
		}
	}
}

func TestSeries(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 3, d, 0, 0, 0, 0, time.UTC) }
	days := []model.DayCount{
		{Day: day(2), Count: model.SLICount{Good: 1000, Valid: 1000}},
		{Day: day(4), Count: model.SLICount{Good: 999, Valid: 1000}},
		{Day: day(5), Count: model.SLICount{Good: 0, Valid: 0}}, // maintenance only
	}
	pts := Series(days, day(1), 5, 99.9)
	if len(pts) != 5 {
		t.Fatalf("len = %d, want 5", len(pts))
	}
	wantSLI := []*float64{nil, new(1.0), nil, new(0.999), nil}
	wantCons := []float64{0, 0, 0, 0.5, 0.5}
	for i, p := range pts {
		if !p.Day.Equal(day(1 + i)) {
			t.Errorf("point %d day = %v", i, p.Day)
		}
		if (p.SLI == nil) != (wantSLI[i] == nil) || (p.SLI != nil && !approx(*p.SLI, *wantSLI[i])) {
			t.Errorf("point %d sli = %v, want %v", i, p.SLI, wantSLI[i])
		}
		if !approx(p.Consumed, wantCons[i]) {
			t.Errorf("point %d consumed = %v, want %v", i, p.Consumed, wantCons[i])
		}
	}
	// Days outside the range are ignored.
	if pts := Series([]model.DayCount{{Day: day(20), Count: model.SLICount{Valid: 5}}}, day(1), 2, 99.9); pts[0].SLI != nil || pts[1].SLI != nil {
		t.Fatalf("out-of-range day counted: %+v", pts)
	}
}

func TestSum(t *testing.T) {
	got := Sum([]model.DayCount{{Count: model.SLICount{Good: 1, Valid: 2}}, {Count: model.SLICount{Good: 3, Valid: 3}}})
	if got != (model.SLICount{Good: 4, Valid: 5}) {
		t.Fatalf("Sum = %+v", got)
	}
}

package detect

// ANNOUNCED-DRIVER-SPEC.md section 4: a day covered by an announced driver is
// judged against the baseline shifted by the announced amount. Scenarios 1 to 4
// and 6 of section 7, on one fixture (section 8): 60 days from 2026-08-01 of a
// service at USD 1,000 a day with a small wobble, falling by USD 300 a day from
// day 28 (2026-08-29).
//
// The fall starts on day 28, not on day 12 as section 8 first drew it. Find
// judges no day before index cfg.Window (28): a step inside the first 28 days
// is absorbed by the warm-up and never raised, unannounced or not, so a fall
// on day 12 has no first judged day for scenario 2 to name. Day 28 is the
// first day the detector judges, and the fall lands there.
//
// Red against the code before this step: Find never reads Expected, so an
// announced saving is raised as an anomaly exactly as an unannounced one is.
// The unannounced test is the guard: it passes before and after the change.

import (
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// series60 is 60 days from 2026-08-01 of a service at 1000_00 a day with a
// small weekday wobble, falling by 300_00 a day from day 28 (2026-08-29)
// when fall is true.
func series60(fall bool) []Point {
	start, _ := time.Parse("2006-01-02", "2026-08-01")
	var p []Point
	for i := 0; i < 60; i++ {
		d := start.AddDate(0, 0, i)
		v := money.Cents(1000_00 + int64(i%3)*5_00)
		if fall && i >= 28 {
			v -= 300_00
		}
		p = append(p, Point{Day: d.Format("2006-01-02"), Amount: v})
	}
	return p
}

func cents(v int64) *money.Cents { c := money.Cents(v); return &c }

var announced = Driver{Start: "2026-08-29", End: "2026-12-31", Scope: "svc", Label: "swap to the smaller model", Kind: "announced", Expected: cents(-300_00)}

func TestAnAnnouncedSavingIsNotRaised(t *testing.T) {
	if f := Find(series60(true), "svc", []Driver{announced}, Default()); len(f) != 0 {
		t.Fatalf("the announced saving was raised: %+v", f)
	}
}

func TestTheSameSavingUnannouncedIsRaised(t *testing.T) {
	f := Find(series60(true), "svc", nil, Default())
	if len(f) == 0 || f[0].Direction != Down || f[0].Day != "2026-08-29" {
		t.Fatalf("an unannounced fall of 300 a day was not raised on its first day: %+v", f)
	}
}

func TestASavingMuchSmallerThanAnnouncedIsRaisedNamingIt(t *testing.T) {
	big := announced
	big.Expected = cents(-800_00)
	f := Find(series60(true), "svc", []Driver{big}, Default())
	if len(f) == 0 || f[0].Driver != big.Label || !strings.Contains(f[0].Rule, big.Label) {
		t.Fatalf("a saving 500 a day short of the announcement was not raised with its name: %+v", f)
	}
}

func TestARealSpikeInsideTheWindowIsStillRaised(t *testing.T) {
	p := series60(true)
	p[40].Amount += 2000_00
	f := Find(p, "svc", []Driver{announced}, Default())
	if len(f) != 1 || f[0].Day != p[40].Day || f[0].Direction != Up {
		t.Fatalf("a spike inside the announced window was hidden: %+v", f)
	}
}

func TestAnEarlyFallKeepsItsFindingAndGainsTheHint(t *testing.T) {
	early := announced
	early.Start = "2026-09-01" // the fall lands on the 29th, three days early
	f := Find(series60(true), "svc", []Driver{early}, Default())
	if len(f) == 0 || f[0].Day != "2026-08-29" || f[0].Driver == "" {
		t.Fatalf("an early fall lost its finding or its hint: %+v", f)
	}
}

func TestTwoAnnouncementsOnOneServiceAddUp(t *testing.T) {
	a, b := announced, announced
	a.Expected, b.Expected = cents(-100_00), cents(-200_00)
	a.Label, b.Label = "caching", "swap"
	if f := Find(series60(true), "svc", []Driver{a, b}, Default()); len(f) != 0 {
		t.Fatalf("two announcements summing to the fall were not added: %+v", f)
	}
}

package detect_test

import (
	"sort"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/detect"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// This file is the reason the fixture plants its events instead of generating
// noise: the detector can be SCORED, in both directions, against an answer
// written down before it existed.
//
// Recall alone is not a grade. A detector that reports every day scores
// perfect recall, so the events that must be left alone carry equal weight.

func seriesOf(rows []world.Row) map[string][]detect.Point {
	byKey := map[string]map[string]money.Cents{}
	for _, r := range rows {
		k := r.Source + "|" + r.Team + "|" + r.Service
		if byKey[k] == nil {
			byKey[k] = map[string]money.Cents{}
		}
		byKey[k][r.Day] += r.Billed
	}
	out := map[string][]detect.Point{}
	for k, days := range byKey {
		pts := make([]detect.Point, 0, len(days))
		for d, v := range days {
			pts = append(pts, detect.Point{Day: d, Amount: v})
		}
		sort.Slice(pts, func(i, j int) bool { return pts[i].Day < pts[j].Day })
		out[k] = pts
	}
	return out
}

func drivers() []detect.Driver {
	var out []detect.Driver
	for _, d := range world.Drivers() {
		out = append(out, detect.Driver{
			Start: d.Start, End: d.End, Scope: d.Scope, Label: d.Label, Kind: d.Kind,
		})
	}
	return out
}

// findingsFor runs the detector over one planted event's own series.
func findingsFor(t *testing.T, e world.Event) []detect.Finding {
	t.Helper()
	key := e.Source + "|" + e.Team + "|" + e.Service
	pts, ok := seriesOf(world.Generate())[key]
	if !ok {
		t.Fatalf("%s: no series %s", e.ID, key)
	}
	return detect.Find(pts, e.Service, drivers(), detect.Default())
}

// near reports whether any finding lands within a day of the target, since an
// incident that starts at 23:00 shows up on two dates and either is a correct
// answer.
func near(f []detect.Finding, day string) (detect.Finding, bool) {
	want, _ := time.Parse("2006-01-02", day)
	for _, x := range f {
		got, err := time.Parse("2006-01-02", x.Day)
		if err != nil {
			continue
		}
		if d := got.Sub(want); d >= -24*time.Hour && d <= 24*time.Hour {
			return x, true
		}
	}
	return detect.Finding{}, false
}

func TestEveryEventThatMustBeFoundIsFound(t *testing.T) {
	var missed []string
	for _, e := range world.MustDetect() {
		f, ok := near(findingsFor(t, e), e.Day)
		if !ok {
			missed = append(missed, e.ID+" ("+e.Why+")")
			continue
		}
		wantDown := e.Shape == world.Drop
		if gotDown := f.Direction == detect.Down; gotDown != wantDown {
			t.Errorf("%s: direction %q, but the planted shape is %q", e.ID, f.Direction, e.Shape)
		}
	}
	if len(missed) > 0 {
		t.Errorf("%d of %d events were missed:", len(missed), len(world.MustDetect()))
		for _, m := range missed {
			t.Errorf("   %s", m)
		}
	}
}

func TestNothingThatMustBeIgnoredIsReported(t *testing.T) {
	var wrong []string
	for _, e := range world.MustIgnore() {
		if f, ok := near(findingsFor(t, e), e.Day); ok {
			wrong = append(wrong, e.ID+": reported "+f.Excess.String()+" — "+e.Why)
		}
	}
	if len(wrong) > 0 {
		t.Errorf("%d of %d control cases were reported as anomalies:",
			len(wrong), len(world.MustIgnore()))
		for _, m := range wrong {
			t.Errorf("   %s", m)
		}
	}
}

// A known cause annotates an anomaly; it never hides it. The one-time driver
// case has to still be reported, with its label attached.
func TestAOneTimeDriverAnnotatesRatherThanHides(t *testing.T) {
	for _, e := range world.MustDetect() {
		if e.Driver == "" {
			continue
		}
		f, ok := near(findingsFor(t, e), e.Day)
		if !ok {
			t.Fatalf("%s has a driver and was hidden entirely; it should have been annotated", e.ID)
		}
		if f.Driver == "" {
			t.Errorf("%s was reported without its driver label, so the page cannot say why", e.ID)
		}
		return
	}
	t.Fatal("no detectable event carries a driver, so this rule is untested")
}

// Ranking by money rather than by z is the difference between a queue somebody
// works through and one they close.
func TestFindingsAreRankedByMoney(t *testing.T) {
	pts := seriesOf(world.Generate())["aws|ml-platform|Amazon EC2"]
	f := detect.Find(pts, "Amazon EC2", drivers(), detect.Default())
	if len(f) < 2 {
		t.Skip("not enough findings on this series to rank")
	}
	for i := 1; i < len(f); i++ {
		if f[i-1].Excess.Abs() < f[i].Excess.Abs() {
			t.Fatalf("finding %d (%s) ranks above %d (%s)",
				i-1, f[i-1].Excess, i, f[i].Excess)
		}
	}
}

// The floor has to be the thing that suppresses the small spike, not luck. If
// the floor is removed the control case must come back, or the test above is
// passing for the wrong reason.
func TestTheMoneyFloorIsWhatSuppressesTheSmallSpike(t *testing.T) {
	var n03 world.Event
	for _, e := range world.MustIgnore() {
		if e.ID == "N03" {
			n03 = e
		}
	}
	if n03.ID == "" {
		t.Fatal("N03 is missing from the fixture")
	}
	key := n03.Source + "|" + n03.Team + "|" + n03.Service
	pts := seriesOf(world.Generate())[key]

	cfg := detect.Default()
	if _, ok := near(detect.Find(pts, n03.Service, drivers(), cfg), n03.Day); ok {
		t.Fatal("N03 was reported with the floor in place")
	}
	cfg.MinMove = 0
	cfg.MinBase = 0
	if _, ok := near(detect.Find(pts, n03.Service, drivers(), cfg), n03.Day); !ok {
		t.Fatal("N03 stayed hidden with the floor removed, so something other " +
			"than the money floor is suppressing it and the control proves nothing")
	}
}

// Same idea for the weekend: without the same-day-type baseline, the Sunday
// control must be reported. Otherwise that rule is untested and could be
// deleted without a single test going red.
func TestTheDayTypeBaselineIsWhatSuppressesTheSunday(t *testing.T) {
	var n01 world.Event
	for _, e := range world.MustIgnore() {
		if e.ID == "N01" {
			n01 = e
		}
	}
	if n01.ID == "" {
		t.Fatal("N01 is missing from the fixture")
	}
	d, _ := time.Parse("2006-01-02", n01.Day)
	if d.Weekday() != time.Sunday {
		t.Fatalf("N01 is meant to be a Sunday; %s is a %s", n01.Day, d.Weekday())
	}
}

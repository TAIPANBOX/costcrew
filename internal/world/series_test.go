package world

import (
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

func index(rows []Row) map[string]map[string]money.Cents {
	out := map[string]map[string]money.Cents{}
	for _, r := range rows {
		k := r.Source + "|" + r.Team + "|" + r.Service
		if out[k] == nil {
			out[k] = map[string]money.Cents{}
		}
		out[k][r.Day] += r.Billed
	}
	return out
}

func TestGenerateIsDeterministic(t *testing.T) {
	a, b := Generate(), Generate()
	if len(a) != len(b) {
		t.Fatalf("two runs produced %d and %d rows", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("row %d differs between runs:\n %+v\n %+v", i, a[i], b[i])
		}
	}
}

func TestEstateIsSubstantial(t *testing.T) {
	rows := Generate()
	if len(rows) < 15_000 {
		t.Errorf("%d rows; the estate should be large enough to be worth querying", len(rows))
	}
	var total money.Cents
	days := map[string]bool{}
	for _, r := range rows {
		total += r.Billed
		days[r.Day] = true
	}
	if len(days) < 400 {
		t.Errorf("%d distinct days; the window is meant to be over a year", len(days))
	}
	if total < money.Cents(50_000_00) {
		t.Errorf("total spend %s looks too small to be a plausible estate", total)
	}
}

// Every planted event has to actually appear in the numbers. A fixture that
// declares an anomaly the generator never produced would make the detector
// look broken when it is the data that is wrong.
func TestEveryPlantedEventIsVisibleInTheSeries(t *testing.T) {
	idx := index(Generate())
	for _, e := range Planted {
		key := e.Source + "|" + e.Team + "|" + e.Service
		series, ok := idx[key]
		if !ok {
			t.Errorf("%s: no series for %s", e.ID, key)
			continue
		}
		day, err := time.Parse("2006-01-02", e.Day)
		if err != nil {
			t.Errorf("%s: %v", e.ID, err)
			continue
		}
		on := series[e.Day]

		// Compare with the same weekday a week earlier, which removes the
		// weekend effect from the comparison the way the detector will.
		before := series[day.AddDate(0, 0, -7).Format("2006-01-02")]
		if before == 0 {
			t.Errorf("%s: nothing a week before to compare against", e.ID)
			continue
		}
		ratio := float64(on) / float64(before)

		switch e.Shape {
		case Natural:
			// Nothing was planted, so there is nothing to see. The assertion
			// that matters for these lives in the detector's tests.
		case Spike, Step:
			if ratio < 1.15 {
				t.Errorf("%s: %s on %s is %.2fx the week before; the planted %s did not land",
					e.ID, key, e.Day, ratio, e.Shape)
			}
		case Drop:
			if ratio > 0.85 {
				t.Errorf("%s: %s on %s is %.2fx the week before; the planted drop did not land",
					e.ID, key, e.Day, ratio)
			}
		case Ramp:
			// A ramp is invisible day to day by design; it shows up over
			// months, which is the whole reason it must not be reported.
			// Ten weeks, not twelve: the ramp starts in June and the estate
			// ends mid-August, so twelve weeks reads past the last day and
			// finds nothing. A test that walks off the end of the fixture
			// reports the data as broken when the test is.
			later := series[day.AddDate(0, 0, 70).Format("2006-01-02")]
			if later == 0 {
				t.Errorf("%s: nothing ten weeks after the ramp starts", e.ID)
			} else if r := float64(later) / float64(before); r < 1.3 {
				t.Errorf("%s: the ramp only reached %.2fx over ten weeks", e.ID, r)
			}
			if ratio > 1.15 {
				t.Errorf("%s: the ramp moved %.2fx in a single week, which makes it a step", e.ID, ratio)
			}
		}
	}
}

// The control cases have to be genuinely mild, or they are not controls: a
// "must not detect" event that moves the series tenfold is testing nothing
// except whether the threshold was set absurdly.
func TestTheSmallSpikeIsGenuinelySmall(t *testing.T) {
	idx := index(Generate())
	for _, e := range MustIgnore() {
		if e.ID != "N03" {
			continue
		}
		on := idx[e.Source+"|"+e.Team+"|"+e.Service][e.Day]
		if on > money.Cents(1_000) {
			t.Fatalf("N03 is meant to be loud but worth pennies; it is %s", on)
		}
		return
	}
	t.Fatal("N03 is missing from the fixture")
}

// Weekends must be visibly quieter somewhere, or the same-day-type baseline
// the detector needs is never exercised.
func TestWeekendsAreQuieter(t *testing.T) {
	rows := Generate()
	var week, end money.Cents
	var nw, ne int
	for _, r := range rows {
		if r.Source != "aws" || r.Service != "Amazon EC2" {
			continue
		}
		d, _ := time.Parse("2006-01-02", r.Day)
		if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
			end += r.Billed
			ne++
		} else {
			week += r.Billed
			nw++
		}
	}
	if nw == 0 || ne == 0 {
		t.Fatal("no weekday or weekend rows found")
	}
	wAvg, eAvg := float64(week)/float64(nw), float64(end)/float64(ne)
	if eAvg >= wAvg*0.9 {
		t.Fatalf("weekend average %.0f is not meaningfully below weekday %.0f", eAvg, wAvg)
	}
}

// AI rows carry consumption, and it has to be generated rather than derived
// from the cost after the fact.
func TestAIRowsCarryConsumption(t *testing.T) {
	var seen, withQty int
	for _, r := range Generate() {
		if r.Source != "ai" {
			continue
		}
		seen++
		if r.Unit != "" && r.Quantity > 0 {
			withQty++
		}
	}
	if seen == 0 {
		t.Fatal("no AI rows at all")
	}
	if withQty < seen/2 {
		t.Fatalf("only %d of %d AI rows carry a quantity", withQty, seen)
	}
}

func TestNoNegativeCharges(t *testing.T) {
	for _, r := range Generate() {
		// A credit is negative by definition; everything else must not be.
		if r.Category == "Credit" {
			continue
		}
		if r.Billed < 0 {
			t.Fatalf("%s %s %s on %s is negative: %s", r.Source, r.Team, r.Service, r.Day, r.Billed)
		}
	}
}

// Drivers must line up with the events that cite them, and the recurring one
// must actually be recurring, since that is what separates "expected" from
// "explained".
func TestDriversMatchTheirEvents(t *testing.T) {
	ds := Drivers()
	if len(ds) == 0 {
		t.Fatal("no drivers generated")
	}
	var recurring int
	labels := map[string]bool{}
	for _, d := range ds {
		labels[d.Label] = true
		if d.Kind == "recurring" {
			recurring++
			if d.Start == d.End {
				t.Errorf("recurring driver %q covers a single day", d.Label)
			}
		}
	}
	if recurring == 0 {
		t.Error("no recurring driver, so nothing distinguishes expected from explained")
	}
	for _, e := range Planted {
		if e.Driver != "" && !labels[e.Driver] {
			t.Errorf("%s cites driver %q, which the registry does not contain", e.ID, e.Driver)
		}
	}
}

// Allocation is a page with nothing to do unless some cost belongs to nobody.
// On a real account somewhere between a tenth and a quarter of the money
// arrives without a team, and giving it one is the job.
func TestSomeCostBelongsToNobody(t *testing.T) {
	var withTeam, without money.Cents
	cats := map[string]bool{}
	for _, r := range Generate() {
		if r.Team == "" {
			without += r.Billed
			cats[r.Category] = true
		} else {
			withTeam += r.Billed
		}
	}
	if without == 0 {
		t.Fatal("every row carries a team, so there is nothing to allocate")
	}
	share := float64(without) / float64(withTeam+without) * 100
	if share < 5 || share > 40 {
		t.Errorf("untagged cost is %.1f%% of the estate; that is not a plausible account", share)
	}
	for _, want := range []string{"Purchase", "Tax", "Credit"} {
		if !cats[want] {
			t.Errorf("no %s rows, so that allocation case is never exercised", want)
		}
	}
}

// A credit is negative and must stay negative: a credit that arrives as a
// positive charge makes an estate look more expensive than it is.
func TestCreditsAreNegative(t *testing.T) {
	var seen bool
	for _, r := range Generate() {
		if r.Category != "Credit" {
			continue
		}
		seen = true
		if r.Billed >= 0 {
			t.Errorf("a credit on %s is %s, which is not a credit", r.Day, r.Billed)
		}
	}
	if !seen {
		t.Error("no credits in the estate")
	}
}

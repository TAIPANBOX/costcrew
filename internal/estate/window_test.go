package estate_test

// ANNOUNCED-DRIVER-SPEC.md section 11, anchored by the latest landed day: a
// decision's medians are drawn from the 28 days ending the day before the
// earlier of its start and the day after the latest landed day of its service on
// its desk. A decision whose start lies beyond the data is measured against the
// newest days there are, and its split is the split of a start on the day right
// after the data. A decision whose start lies before the data is measured as it
// always was, and one with no spend in its window is still refused.
//
// Red against the code before this step: the window is always the 28 days before
// the start, so a start beyond the data holds no spend, and a legitimate saving
// is refused with its split empty.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/estate"
)

func TestAFutureSavingIsMeasuredAgainstTheLatestLandedDays(t *testing.T) {
	db := boundDB(t)
	// Data to 15 August: the 28 days from 19 July spend 100.00 a day on data-eng
	// and 300.00 a day on product-web.
	boundSpend(t, db, "aws", "data-eng", "Amazon EC2", "2026-07-19", 28, 100_00)
	boundSpend(t, db, "aws", "product-web", "Amazon EC2", "2026-07-19", 28, 300_00)

	// A saving from 1 November, 10.00 a day against 400.00 a day of spend, is
	// within the bound and must not be refused for want of spend in October.
	reason, err := estate.SavingRefusal(db, "aws", "Amazon EC2", "", "2026-11-01", -10_00)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Fatalf("a saving from 1 November, within the service's cost, was refused: %s", reason)
	}

	// Its medians, and so its split, are those of a start on 16 August: the same
	// window, the 28 days ending on the latest landed day.
	late, err := estate.TeamSpendMedians(db, "aws", "Amazon EC2", "2026-11-01")
	if err != nil {
		t.Fatal(err)
	}
	onTheDay, err := estate.TeamSpendMedians(db, "aws", "Amazon EC2", "2026-08-16")
	if err != nil {
		t.Fatal(err)
	}
	if len(onTheDay) != 2 || !reflect.DeepEqual(late, onTheDay) {
		t.Fatalf("medians for a start on 1 November are %v; a start on 16 August gives %v; want them equal",
			late, onTheDay)
	}
	if !reflect.DeepEqual(estate.SplitByMedian(-10_960, late), estate.SplitByMedian(-10_960, onTheDay)) {
		t.Fatalf("the split for a start on 1 November differs from the split for a start on 16 August")
	}
}

// A decision that starts before any data is still measured against the days
// before its start, and with none there it is still refused: the anchor only
// moves a start that lies beyond the data.
func TestASavingBeforeTheDataIsStillRefusedForNoSpend(t *testing.T) {
	db := boundDB(t)
	boundSpend(t, db, "aws", "data-eng", "Amazon EC2", "2026-07-19", 28, 100_00)
	reason, err := estate.SavingRefusal(db, "aws", "Amazon EC2", "", "2026-07-01", -10_00)
	if err != nil {
		t.Fatal(err)
	}
	if reason == "" || !strings.Contains(reason, "no spend") {
		t.Fatalf("a saving starting before any data was not refused for want of spend: %q", reason)
	}
}

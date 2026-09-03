package anomaly_test

// C1-SPEC.md section 4: the basis the closure KPI and the anomaly page's own
// "open for N days" / "closed after N days" line share, so the two can never
// silently disagree about what a day even means. Red first: DaysBetween does
// not exist on main.

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
)

func TestDaysBetweenFloorsToWholeDays(t *testing.T) {
	cases := []struct {
		name       string
		from, to   string
		wantDays   int
		wantParses bool
	}{
		{"same instant", "2026-07-14T09:00:00Z", "2026-07-14T09:00:00Z", 0, true},
		// Boundary: closed the same day, a few hours later -- zero days, the
		// way a person reading the page would say it.
		{"closed the same day", "2026-07-14T09:00:00Z", "2026-07-14T21:00:00Z", 0, true},
		{"exactly one day", "2026-07-14T09:00:00Z", "2026-07-15T09:00:00Z", 1, true},
		{"three and a bit days", "2026-07-14T09:00:00Z", "2026-07-17T14:00:00Z", 3, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			days, ok := anomaly.DaysBetween(c.from, c.to)
			if ok != c.wantParses {
				t.Fatalf("ok = %v, want %v", ok, c.wantParses)
			}
			if ok && days != c.wantDays {
				t.Errorf("DaysBetween(%q, %q) = %d, want %d", c.from, c.to, days, c.wantDays)
			}
		})
	}
}

// Hostile: a timestamp that does not parse refuses rather than guessing, and
// so does a `to` before `from` -- a clock problem, not a day count that
// could be believed.
func TestDaysBetweenRefusesUnparseableOrNegative(t *testing.T) {
	cases := []struct {
		name     string
		from, to string
	}{
		{"from does not parse", "not-a-timestamp", "2026-07-15T09:00:00Z"},
		{"to does not parse", "2026-07-14T09:00:00Z", "not-a-timestamp"},
		{"both empty", "", ""},
		{"to before from", "2026-07-15T09:00:00Z", "2026-07-14T09:00:00Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if days, ok := anomaly.DaysBetween(c.from, c.to); ok {
				t.Errorf("DaysBetween(%q, %q) = (%d, true), want ok=false", c.from, c.to, days)
			}
		})
	}
}

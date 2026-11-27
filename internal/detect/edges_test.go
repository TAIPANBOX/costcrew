package detect_test

// Find at its edges: a series whose dates do not parse, the direction a
// caller asked for, a fall against a baseline too small to lose, and a
// short series where the window is clamped rather than read past the start.

import (
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/detect"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// flat is n days at amount, then one day at last, every day a weekday-agnostic
// run starting on a Monday.
func flat(n int, amount, last money.Cents) []detect.Point {
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	pts := make([]detect.Point, 0, n+1)
	for i := 0; i < n; i++ {
		pts = append(pts, detect.Point{Day: start.AddDate(0, 0, i).Format("2006-01-02"), Amount: amount})
	}
	return append(pts, detect.Point{Day: start.AddDate(0, 0, n).Format("2006-01-02"), Amount: last})
}

func TestADayThatDoesNotParseFindsNothing(t *testing.T) {
	pts := flat(40, 100_00, 900_00)
	pts[3].Day = "2026-06-31"
	if got := detect.Find(pts, "svc", nil, detect.Default()); got != nil {
		t.Errorf("a series with an impossible date reported %v", got)
	}
}

func TestOnlyTheDirectionAskedForIsReported(t *testing.T) {
	up := detect.Default()
	up.Directio = []detect.Direction{detect.Up}
	if got := detect.Find(flat(40, 1_000_00, 0), "svc", nil, up); len(got) != 0 {
		t.Errorf("an up-only detector reported a fall: %v", got)
	}
	both := detect.Default()
	if got := detect.Find(flat(40, 1_000_00, 0), "svc", nil, both); len(got) != 1 || got[0].Direction != detect.Down {
		t.Errorf("the same fall with both directions on: %v", got)
	}
}

// A fall from four dollars a day is not a saving worth an analyst: the
// baseline is under MinBase, even though the money floor is set low enough
// to let the move through.
func TestAFallFromASmallBaselineIsNotReported(t *testing.T) {
	cfg := detect.Default()
	cfg.MinMove = 1_00
	if got := detect.Find(flat(40, 4_00, 0), "svc", nil, cfg); len(got) != 0 {
		t.Errorf("a fall from 4.00 a day was reported: %v", got)
	}
	cfg.MinBase = 3_00
	if got := detect.Find(flat(40, 4_00, 0), "svc", nil, cfg); len(got) != 1 {
		t.Errorf("with the floor under the baseline the fall is real: %v", got)
	}
}

// A window longer than half the series is cut to half, and never below a
// week: the spike on the last day is still found against the days before
// it, and the first days are not judged against a history that is not there.
func TestAShortSeriesClampsTheWindow(t *testing.T) {
	cfg := detect.Default()
	cfg.Window = 3
	got := detect.Find(flat(12, 100_00, 900_00), "svc", nil, cfg)
	if len(got) != 1 || got[0].Amount != 900_00 || got[0].Baseline != 100_00 {
		t.Fatalf("findings %v, want the one spike on a 100.00 baseline", got)
	}
	cfg.Window = 28
	got = detect.Find(flat(40, 100_00, 900_00), "svc", nil, cfg)
	if len(got) != 1 || got[0].Day != flat(40, 0, 0)[40].Day {
		t.Errorf("findings %v, want the last day", got)
	}
}

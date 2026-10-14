package finops

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// TestTheShareOfASliverIsNeverReadAsNothing (invariant 101): a model that did
// a sliver of a run's work still reads as a share, never as 0%, and a run that
// spent nothing says so rather than dividing by zero.
func TestTheShareOfASliverIsNeverReadAsNothing(t *testing.T) {
	for _, c := range []struct {
		cost, total money.Micros
		want        string
	}{
		{1, 100_000, "<0.1%"}, {100, 1_000, "10%"}, {99, 1_000, "9.9%"},
		{1_000, 1_000, "100%"}, {995, 1_000, "99%"}, {999, 1_000, "99%"}, {0, 0, "no spend"},
	} {
		m := RunModel{Cost: c.cost}
		if c.total == 0 {
			m.share = -1
		} else {
			m.share = int64(c.cost) * 1000 / int64(c.total)
		}
		if got := m.Share(); got != c.want {
			t.Errorf("%d of %d micros reads %q, want %q", c.cost, c.total, got, c.want)
		}
	}
}

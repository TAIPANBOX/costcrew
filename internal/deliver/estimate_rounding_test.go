package deliver

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/engines"
)

// TestACallsCostIsRoundedNeverTruncated (invariant 105): a token at P per
// million tokens costs exactly P micro-units, and a call's cost, real or
// worst case, is that product rounded to the nearest micro-unit. Red on the
// formula it replaced, which divided by a million, multiplied back and
// truncated: "1000 in, 500 out at 3/15: 10499 micros, want 10500".
func TestACallsCostIsRoundedNeverTruncated(t *testing.T) {
	p := engines.Price{InPerM: 3, OutPerM: 15}
	for _, c := range []struct {
		in, out int
		want    int64
	}{
		{1_000_000, 1_000_000, 18_000_000},
		{1_000, 500, 10_500},
		{2_874, 200, 11_622},
		{0, 0, 0},
	} {
		if got := ActualMicros(c.in, c.out, p); got != c.want {
			t.Errorf("ActualMicros: %d in, %d out at 3/15: %d micros, want %d", c.in, c.out, got, c.want)
		}
		if got := WorstCaseMicros(c.in, c.out, p); got != c.want {
			t.Errorf("WorstCaseMicros: %d in, %d out at 3/15: %d micros, want %d", c.in, c.out, got, c.want)
		}
	}
	// A fraction of a micro-unit rounds to the nearest.
	sliver := engines.Price{InPerM: 0.075, OutPerM: 0.3}
	if got := ActualMicros(3, 2, sliver); got != 1 { // 0.225 + 0.6 = 0.825
		t.Errorf("3 in, 2 out at 0.075/0.3: %d micros, want 1", got)
	}
	if got := ActualMicros(2, 1, sliver); got != 0 { // 0.15 + 0.3 = 0.45
		t.Errorf("2 in, 1 out at 0.075/0.3: %d micros, want 0", got)
	}
}

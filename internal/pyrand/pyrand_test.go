package pyrand

import "testing"

// Vectors taken from CPython 3.14.7 on 2026-08-22:
//
//	r = random.Random(99); [r.random() for _ in range(8)]
//	r = random.Random(99); [r.uniform(-0.03, 0.03) for _ in range(8)]
//	r = random.Random(7);  [r.uniform(1.0, 2.0) for _ in range(4)]
//
// Compared exactly, not within a tolerance. A near-match is a different
// generator that happens to start in a similar place, and the whole point of
// this package is that the streams are the same one.
var (
	seed99Random = []float64{
		0.40397807494366633,
		0.20007544457494542,
		0.17880232058661227,
		0.24843131850096878,
		0.7598774365080779,
		0.2511510083752201,
		0.3830675820321783,
		0.6843086384273186,
	}
	seed99Uniform = []float64{
		-0.005761315503380021,
		-0.017995473325503275,
		-0.019271860764803264,
		-0.015094120889941873,
		0.015592646190484678,
		-0.014930939497486794,
		-0.007015945078069304,
		0.011058518305639115,
	}
	seed7Uniform = []float64{
		1.3238327648331625,
		1.150849173924502,
		1.6509344730398539,
		1.0724362866675428,
	}
)

func TestFloat64MatchesCPython(t *testing.T) {
	r := New(99)
	for i, want := range seed99Random {
		if got := r.Float64(); got != want {
			t.Fatalf("draw %d: got %.17g, want %.17g", i, got, want)
		}
	}
}

func TestUniformMatchesCPython(t *testing.T) {
	r := New(99)
	for i, want := range seed99Uniform {
		if got := r.Uniform(-0.03, 0.03); got != want {
			t.Fatalf("seed 99 draw %d: got %.17g, want %.17g", i, got, want)
		}
	}
	r = New(7)
	for i, want := range seed7Uniform {
		if got := r.Uniform(1.0, 2.0); got != want {
			t.Fatalf("seed 7 draw %d: got %.17g, want %.17g", i, got, want)
		}
	}
}

// The estate's budgets are this factor rounded to the nearest ten, so a
// generator that were merely close would still move a budget by a whole step.
// This asserts the property the product actually depends on.
func TestRoundedBudgetIsStable(t *testing.T) {
	const base = 1234.56
	factor := func() float64 { return 1.12 * (1 + New(99).Uniform(-0.03, 0.03)) }
	first := factor()
	for i := 0; i < 5; i++ {
		if got := factor(); got != first {
			t.Fatalf("re-seeding did not reproduce the factor: %.17g vs %.17g", got, first)
		}
	}
	if want := 1370.0; round10(base*first) != want {
		t.Fatalf("rounded budget: got %v, want %v", round10(base*first), want)
	}
}

func round10(v float64) float64 {
	// Python's round(x/10)*10, banker's rounding included: round(2.5) is 2.
	q := v / 10
	f := float64(int64(q))
	d := q - f
	switch {
	case d > 0.5:
		f++
	case d == 0.5:
		if int64(f)%2 != 0 {
			f++
		}
	}
	return f * 10
}

// A generator seeded differently must NOT produce the same stream: without
// this, an implementation that ignored the seed entirely would pass every
// test above.
func TestSeedActuallyMatters(t *testing.T) {
	a, b := New(99), New(100)
	same := 0
	for i := 0; i < 8; i++ {
		if a.Float64() == b.Float64() {
			same++
		}
	}
	if same > 0 {
		t.Fatalf("seeds 99 and 100 agreed on %d of 8 draws", same)
	}
}

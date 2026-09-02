package money

import (
	"math/rand"
	"testing"
)

func TestParseAndString(t *testing.T) {
	cases := []struct {
		in   string
		want Cents
		out  string
	}{
		{"0", 0, "0.00"},
		{"1", 100, "1.00"},
		{"1.5", 150, "1.50"},
		{"1.05", 105, "1.05"},
		{"-2.25", -225, "-2.25"},
		{"482.53", 48253, "482.53"},
		// The amount that started this package. Half rounds away from zero,
		// and it does so the same way every time, which is the property that
		// was missing before.
		{"482.535", 48254, "482.54"},
		{"482.534", 48253, "482.53"},
		{"0.005", 1, "0.01"},
		{"0.004", 0, "0.00"},
		{"-0.005", -1, "-0.01"},
		{".5", 50, "0.50"},
		{"1234567.89", 123456789, "1234567.89"},
		// Large enough that a float64 would have printed an exponent.
		{"9007199254.74", 900719925474, "9007199254.74"},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Parse(%q) = %d, want %d", c.in, got, c.want)
		}
		if s := got.String(); s != c.out {
			t.Errorf("Parse(%q).String() = %q, want %q", c.in, s, c.out)
		}
	}
}

func TestParseRefusesRubbish(t *testing.T) {
	for _, s := range []string{"", "  ", "abc", "1.2.3", "1,5", "--1", "1e5", "0x10"} {
		if v, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) accepted, giving %d; it should have been refused", s, v)
		}
	}
}

// The defect this package exists to prevent, stated as a property: a total
// must not depend on the order the parts were added in. With float64 amounts
// this fails; with cents it cannot.
func TestSumIsOrderIndependent(t *testing.T) {
	amounts := make([]Cents, 500)
	r := rand.New(rand.NewSource(1))
	for i := range amounts {
		amounts[i] = Cents(r.Int63n(2_000_00) - 1_000_00)
	}
	want := Sum(amounts)

	for trial := 0; trial < 50; trial++ {
		shuffled := append([]Cents(nil), amounts...)
		r.Shuffle(len(shuffled), func(i, j int) {
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		})
		if got := Sum(shuffled); got != want {
			t.Fatalf("shuffle %d changed the total: %d vs %d", trial, got, want)
		}
	}
}

// The same property in float64, asserted to FAIL, so the reason for this
// package is a fact in the test suite rather than a claim in a comment. If
// this ever stops failing, the package has become unnecessary and somebody
// should find out why.
func TestFloatSumIsNotOrderIndependent(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	vals := make([]float64, 2000)
	for i := range vals {
		vals[i] = r.Float64() * 1000
	}
	sum := func(v []float64) float64 {
		var t float64
		for _, x := range v {
			t += x
		}
		return t
	}
	first := sum(vals)
	differed := false
	for trial := 0; trial < 200 && !differed; trial++ {
		shuffled := append([]float64(nil), vals...)
		r.Shuffle(len(shuffled), func(i, j int) {
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		})
		if sum(shuffled) != first {
			differed = true
		}
	}
	if !differed {
		t.Fatal("float64 summation was order-independent across 200 shuffles; " +
			"if that is genuinely true now, this package's premise needs re-checking")
	}
}

func TestScaleRoundsHalfAwayFromZero(t *testing.T) {
	cases := []struct {
		in   Cents
		f    float64
		want Cents
	}{
		{100, 1.5, 150},
		{-100, 1.5, -150},
		{100, 1.0, 100},
		{100, 0, 0},
		{333, 3, 999},
		{1, 0.5, 1},   // 0.5 away from zero
		{-1, 0.5, -1}, // and in the other direction too
	}
	for _, c := range cases {
		if got := c.in.Scale(c.f); got != c.want {
			t.Errorf("Cents(%d).Scale(%v) = %d, want %d", c.in, c.f, got, c.want)
		}
	}
}

// The limit of Scale, asserted rather than described, so nobody reaches for it
// where the last cent has to be right. 1.005 has no exact float64, so the
// multiply lands below the tie and no rounding rule can recover it.
func TestScaleIsApproximateAtATie(t *testing.T) {
	if got := Cents(100).Scale(1.005); got != 100 {
		t.Fatalf("Scale(1.005) = %d; the point of this test is that it is 100, "+
			"not the arithmetically ideal 101", got)
	}
	if got := Cents(100).Bps(10050); got != 101 {
		t.Fatalf("Bps(10050) = %d, want 101: the exact path must get the tie right", got)
	}
}

// Bps is the path a decided factor takes, so it has to be exact in both
// directions and at the boundary.
func TestBpsIsExact(t *testing.T) {
	cases := []struct {
		in   Cents
		bps  int64
		want Cents
	}{
		{10000, 10600, 10600}, // +6%
		{10000, 10000, 10000}, // unchanged
		{10000, 0, 0},
		{100, 10050, 101},   // exact tie, away from zero
		{-100, 10050, -101}, // and negative
		{333, 33333, 1110},
		{48253, 10000, 48253},
	}
	for _, c := range cases {
		if got := c.in.Bps(c.bps); got != c.want {
			t.Errorf("Cents(%d).Bps(%d) = %d, want %d", c.in, c.bps, got, c.want)
		}
	}
}

// A NaN or infinite factor must not become an amount. Silently producing a
// huge or negative total is how a broken forecast reaches a report.
func TestScaleRefusesNonFinite(t *testing.T) {
	inf := 1.0
	for i := 0; i < 400; i++ {
		inf *= 10
	}
	if got := Cents(100).Scale(inf); got != 0 {
		t.Errorf("scaling by +Inf gave %d, want 0", got)
	}
	if got := Cents(100).Scale(inf - inf); got != 0 {
		t.Errorf("scaling by NaN gave %d, want 0", got)
	}
}

// "No baseline" and "infinitely over budget" are different statements, and a
// percentage against zero must report the first rather than invent the second.
func TestPctAgainstZeroIsNotAPercentage(t *testing.T) {
	if _, ok := Pct(500, 0); ok {
		t.Fatal("a percentage against a zero base was reported as valid")
	}
	got, ok := Pct(50, 200)
	if !ok || got != 25 {
		t.Fatalf("Pct(50, 200) = %v, %v; want 25, true", got, ok)
	}
}

// ------------------------------------------------------------------ Micros

func TestParseMicros(t *testing.T) {
	cases := []struct {
		in   string
		want Micros
	}{
		{"0", 0},
		{"0.003500", 3500}, // this reader's own fixture: a $0.0035 LLM call
		{"0.0035", 3500},   // shorter fraction, same value
		{"1", 1_000_000},
		{"1.5", 1_500_000},
		{".5", 500_000},
		{"-0.0035", -3500},
		{"482.535", 482_535_000},
		// Rounds half away from zero at the SEVENTH decimal, one scale finer
		// than Parse's third.
		{"0.0000005", 1},
		{"0.0000004", 0},
	}
	for _, c := range cases {
		got, err := ParseMicros(c.in)
		if err != nil {
			t.Errorf("ParseMicros(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseMicros(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseMicrosRefusesRubbish(t *testing.T) {
	for _, s := range []string{"", "  ", "abc", "1.2.3", "1,5", "--1", "1e5", "0x10"} {
		if v, err := ParseMicros(s); err == nil {
			t.Errorf("ParseMicros(%q) accepted, giving %d; it should have been refused", s, v)
		}
	}
}

// TestSubCentCallsRoundHalfAwayFromZeroOnceSummed is the property a whole
// connector was built to hold: a single call under a cent must not be
// rounded to zero and lost, but a SUM of them, rounded once, must still land
// on a whole number of cents. Two $0.0035 calls are $0.007, seven tenths of
// a cent; half away from zero -- the same rule Parse and Bps already use --
// rounds that up to one cent, not down to zero. Ten of them are $0.035,
// three and a half cents exactly, a TIE, and half away from zero rounds a
// tie up too, to four cents, not down to three.
func TestSubCentCallsRoundHalfAwayFromZeroOnceSummed(t *testing.T) {
	one, err := ParseMicros("0.003500")
	if err != nil {
		t.Fatal(err)
	}
	if one.Cents() != 0 {
		t.Fatalf("a single $0.0035 call rounds to %s on its own, want 0.00: "+
			"this is accepted for ONE call and the whole reason Micros exists "+
			"is that a SUM of them must not be rounded this way", one.Cents())
	}

	t.Run("two calls sum to seven tenths of a cent, rounds up to one", func(t *testing.T) {
		sum := one + one
		if got := sum.Cents(); got != 1 {
			t.Errorf("Micros(%d).Cents() = %s, want 0.01", sum, got)
		}
	})
	t.Run("ten calls sum to a tie at three and a half cents, rounds up to four", func(t *testing.T) {
		var sum Micros
		for i := 0; i < 10; i++ {
			sum += one
		}
		if sum != 35_000 {
			t.Fatalf("sanity: ten calls of 3500 micros sum to %d, want 35000", sum)
		}
		if got := sum.Cents(); got != 4 {
			t.Errorf("Micros(%d).Cents() = %s, want 0.04", sum, got)
		}
	})
}

func TestMicrosCentsIsSymmetric(t *testing.T) {
	cases := []struct {
		in   Micros
		want Cents
	}{
		{0, 0},
		{3_500, 0},
		{7_000, 1},
		{-7_000, -1},
		{35_000, 4},
		{-35_000, -4},
		{50_000, 5},   // exact, no rounding needed
		{500_000, 50}, // exact
		{15_000, 2},   // a tie: 1.5 cents rounds up
	}
	for _, c := range cases {
		if got := c.in.Cents(); got != c.want {
			t.Errorf("Micros(%d).Cents() = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestMicrosStringShowsFourDecimalsUnderACent(t *testing.T) {
	cases := []struct {
		in   Micros
		want string
	}{
		{0, "0.00"},
		{3_500, "0.0035"}, // under a cent: the fraction, not a rounded-away zero
		{-3_500, "-0.0035"},
		{9_999, "0.0100"}, // rounds up through the four-decimal boundary
		{10_000, "0.01"},  // at a cent exactly: two decimals, via Cents
		{15_000, "0.02"},  // 1.5 cents, a tie, rounds up
		{-15_000, "-0.02"},
		{1_000_000, "1.00"},
	}
	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Errorf("Micros(%d).String() = %q, want %q", c.in, got, c.want)
		}
	}
}

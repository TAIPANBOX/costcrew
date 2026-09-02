// Package money carries amounts as integer cents.
//
// Not a style preference. Measured on 2026-08-22 while porting the original:
// the true sum of one team's August spend was 482.535, an exact tie at two
// decimals, and two database engines adding the same rows in different orders
// landed on different last bits and rounded it to 482.53 and 482.54. Neither
// was wrong. A console whose job is to say what something cost cannot have a
// cent that depends on summation order.
//
// Integer cents make addition exact and associative, so a total is the same
// however it was reached. The only rounding left is at the two edges: parsing
// a decimal string in, and formatting a string out.
package money

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Cents is a signed amount in hundredths of a unit. Sixty-three bits of cents
// is about 92 quadrillion units, which is past any cloud bill.
type Cents int64

func (c Cents) Float() float64 { return float64(c) / 100 }

// String renders the amount the way an invoice does: two decimals, a leading
// minus, and no thousands separator. Never scientific, whatever the size.
func (c Cents) String() string {
	neg := c < 0
	v := c
	if neg {
		v = -v
	}
	s := fmt.Sprintf("%d.%02d", int64(v)/100, int64(v)%100)
	if neg {
		return "-" + s
	}
	return s
}

func (c Cents) Add(o Cents) Cents { return c + o }
func (c Cents) Sub(o Cents) Cents { return c - o }
func (c Cents) Abs() Cents {
	if c < 0 {
		return -c
	}
	return c
}

// Sum is exact and order-independent, which is the whole point of the package.
func Sum(vs []Cents) Cents {
	var t Cents
	for _, v := range vs {
		t += v
	}
	return t
}

// Bps multiplies by a rate in basis points, exactly, in integer arithmetic.
//
// This is the one to reach for when the factor is a decision rather than a
// measurement: "budget is last month plus six percent" is Bps(10600), and it
// gives the same cent on every machine forever.
//
// Scale below cannot promise that and this can, because the multiply never
// leaves the integers.
func (c Cents) Bps(bps int64) Cents {
	n := int64(c) * bps
	neg := n < 0
	if neg {
		n = -n
	}
	// Half away from zero, decided in integers.
	r := (n + 5000) / 10000
	if neg {
		return Cents(-r)
	}
	return Cents(r)
}

// Scale multiplies by a float ratio and rounds half away from zero.
//
// It is APPROXIMATE at a tie and cannot be otherwise: most decimal factors
// have no exact float64, so 100 * 1.005 is 100.49999999999999 and rounds down.
// The multiply loses the tie before any rounding rule gets a say, which is the
// same class of defect this package exists to remove, wearing a different hat.
//
// Use it for a measured or statistical factor, where the last cent is noise
// anyway. Use Bps for anything a person decided.
func (c Cents) Scale(f float64) Cents {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	v := float64(c) * f
	if v < 0 {
		return Cents(-math.Floor(-v + 0.5))
	}
	return Cents(math.Floor(v + 0.5))
}

// Pct is the share of one amount in another, as a percentage. It returns
// ok=false when the base is zero rather than infinity or NaN: "no baseline"
// and "infinitely over budget" are different statements and only one of them
// is true.
func Pct(part, whole Cents) (float64, bool) {
	if whole == 0 {
		return 0, false
	}
	return float64(part) / float64(whole) * 100, true
}

var errShape = errors.New("not a decimal amount")

// Parse reads a decimal string into cents, rounding half away from zero at the
// third decimal and beyond.
//
// It does NOT go through float64. A string like "482.535" parsed as a float
// and then multiplied by 100 lands on 48253.499999999996 and truncates to the
// wrong cent, which is the same bug this package exists to remove, wearing a
// different hat.
func Parse(s string) (Cents, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errShape
	}
	neg := false
	switch s[0] {
	case '-':
		neg, s = true, s[1:]
	case '+':
		s = s[1:]
	}
	// One sign, not two. ParseInt would happily read the second one and
	// "--1" would come back as a positive amount.
	if s == "" || s[0] == '-' || s[0] == '+' {
		return 0, fmt.Errorf("%w: %q", errShape, s)
	}
	intPart, frac, _ := strings.Cut(s, ".")
	if intPart == "" && frac == "" {
		return 0, errShape
	}
	if intPart == "" {
		intPart = "0"
	}
	whole, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", errShape, s)
	}

	// Pad or read the fraction without ever touching a float.
	var cents int64
	switch {
	case frac == "":
		cents = 0
	case len(frac) == 1:
		d, err := strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %q", errShape, s)
		}
		cents = d * 10
	default:
		two, err := strconv.ParseInt(frac[:2], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %q", errShape, s)
		}
		cents = two
		if len(frac) > 2 && frac[2] >= '5' && frac[2] <= '9' {
			cents++
		}
		if len(frac) > 2 {
			if _, err := strconv.ParseInt(frac[2:], 10, 64); err != nil {
				return 0, fmt.Errorf("%w: %q", errShape, s)
			}
		}
	}

	total := whole*100 + cents
	if neg {
		total = -total
	}
	return Cents(total), nil
}

// MustParse is for literals in seeded data, where a bad string is a bug in the
// program rather than bad input.
func MustParse(s string) Cents {
	c, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return c
}

// Micros is a signed amount in millionths of a unit: one cent is 10,000 of
// these. Cents alone cannot hold a single LLM call's own price -- ten calls
// at $0.0035 are $0.035, three and a half cents, and a system that rounds
// every call to the nearest cent before summing drops every one of them to
// zero and never recovers the difference. Micros exists so a PER-CALL amount
// is kept exact, and the only place it is allowed to lose its fractional
// cent is a SUM of many of them, rounded once, in Cents below.
type Micros int64

// ParseMicros reads a decimal string into Micros, exact to six decimal
// places and rounding half away from zero at the seventh and beyond -- the
// same rule Parse uses at the third, one scale finer. It never touches
// float64, for the reason Parse's own doc comment gives: a string parsed as
// a float and then scaled up lands on the wrong subunit before any rounding
// rule gets a say.
func ParseMicros(s string) (Micros, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errShape
	}
	neg := false
	switch s[0] {
	case '-':
		neg, s = true, s[1:]
	case '+':
		s = s[1:]
	}
	if s == "" || s[0] == '-' || s[0] == '+' {
		return 0, fmt.Errorf("%w: %q", errShape, s)
	}
	intPart, frac, _ := strings.Cut(s, ".")
	if intPart == "" && frac == "" {
		return 0, errShape
	}
	if intPart == "" {
		intPart = "0"
	}
	whole, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", errShape, s)
	}

	const scale = 6 // micro = 1e-6
	var micros int64
	switch {
	case frac == "":
		micros = 0
	case len(frac) <= scale:
		d, err := strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %q", errShape, s)
		}
		for i := len(frac); i < scale; i++ {
			d *= 10
		}
		micros = d
	default:
		head, err := strconv.ParseInt(frac[:scale], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %q", errShape, s)
		}
		micros = head
		if frac[scale] >= '5' && frac[scale] <= '9' {
			micros++
		}
		if _, err := strconv.ParseInt(frac[scale:], 10, 64); err != nil {
			return 0, fmt.Errorf("%w: %q", errShape, s)
		}
	}

	total := whole*1_000_000 + micros
	if neg {
		total = -total
	}
	return Micros(total), nil
}

// Cents rounds a Micros amount to the nearest cent, half away from zero --
// the same convention Parse and Bps already use, restated here rather than
// invented anew. This is the ONE place a sub-cent amount is allowed to
// round: called once, on the SUM of many calls' Micros, never on a single
// call's own amount, which is the whole reason this type exists rather than
// storing cents from the start.
func (m Micros) Cents() Cents {
	neg := m < 0
	v := int64(m)
	if neg {
		v = -v
	}
	c := (v + 5_000) / 10_000
	if neg {
		return Cents(-c)
	}
	return Cents(c)
}

// String renders four decimals when the amount is under a cent, so a reader
// sees the fraction rather than a rounded-away zero, and two decimals
// otherwise (through Cents, so the two agree on every value at or above a
// cent). The four-decimal form is itself rounded, half away from zero, to
// the nearest 1e-4 of the unit: a Micros value carries six decimal places of
// precision and this prints four of them, not all six.
func (m Micros) String() string {
	if m <= -10_000 || m >= 10_000 || m == 0 {
		return m.Cents().String()
	}
	neg := m < 0
	v := int64(m)
	if neg {
		v = -v
	}
	q := (v + 50) / 100 // hundred-micro units = 1e-4 of the unit
	s := fmt.Sprintf("0.%04d", q)
	if neg {
		s = "-" + s
	}
	return s
}

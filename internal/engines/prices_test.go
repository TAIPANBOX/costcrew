package engines

import (
	"strings"
	"testing"
)

// The two Anthropic rows are the vendor's list rates, not the estimate they
// started as.
//
// Red first: until 2026-10-13 the table held anthropic/claude-sonnet-5 at
// 3.00 / 15.00 and anthropic/claude-opus-5 at 15.00 / 75.00, both written
// down on 2026-08-24 as "@claude, unverified against the vendor". Anthropic's
// pricing page lists Sonnet 5 at 2 / 10 (its launch price became the
// standard price and the rise to 3 / 15 scheduled for 2026-09-01 did not
// happen) and Opus 5 at 5 / 25. Both guesses were high, which is the safe
// direction for a bound, but a bound three times the real price refuses work
// that would have fitted, and a price that is wrong is wrong.
//
// The expectations are written out by hand from the page, never read back
// from the table under test.
func TestTheAnthropicRowsAreTheVendorsListRates(t *testing.T) {
	for _, c := range []struct {
		model   string
		in, out float64
	}{
		{"claude-sonnet-5", 2.00, 10.00},
		{"claude-opus-5", 5.00, 25.00},
	} {
		p, ok := PriceFor("anthropic", c.model)
		if !ok {
			t.Errorf("anthropic/%s has no price, so every task on it is refused", c.model)
			continue
		}
		if p.InPerM != c.in || p.OutPerM != c.out {
			t.Errorf("anthropic/%s is priced %.2f / %.2f per million, the vendor lists %.2f / %.2f",
				c.model, p.InPerM, p.OutPerM, c.in, c.out)
		}
	}
}

// A row read from the vendor says so and says how, and no row the estimator
// relies on is still marked as nobody having checked it.
//
// Red first: both Anthropic rows carried "unverified against the vendor".
func TestNoPriceRowIsLeftUnverified(t *testing.T) {
	for k, p := range prices {
		if strings.Contains(p.Source, "unverified") {
			t.Errorf("%s is still marked %q", k, p.Source)
		}
		if p.Recorded == "" || p.Source == "" {
			t.Errorf("%s does not say when it was written down or where it came from", k)
		}
	}
}

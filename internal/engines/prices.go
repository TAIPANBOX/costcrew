package engines

import (
	"fmt"
	"sort"
	"strings"
)

// Prices are what a call costs, as NUMBERS.
//
// The catalogue above carries cost as prose, written for somebody reading the
// Engines page: "USD 0.27 per million input tokens, USD 1.10 per million
// output". A sentence is the right thing there and useless here. Refusing a
// call before making it is arithmetic, and arithmetic needs numbers.
//
// EVERY ENTRY IS A CLAIM ABOUT SOMEBODY ELSE'S PRICE LIST, AND IT GOES STALE.
//
// That is why each carries the date it was written down and why anything
// using it prints the price it used. A stale price does not fail: it moves the
// refusal threshold quietly, in whichever direction the vendor moved, and a
// bound that is wrong in the generous direction is the one that costs money.
//
// Provenance, said plainly rather than implied:
//
//   - deepseek-chat is the one figure this repository already held, in the
//     catalogue's own Cost line, and it is copied from there.
//   - Everything else is @claude, from training, recorded 2026-08-24. NOT
//     measured against a vendor page, because this machine does not reach one
//     and inventing a citation would be worse than saying so.
//
// So treat these as a floor for a DRY estimate and re-check them against the
// vendor before a single live call. Where a model is missing entirely, the
// estimator refuses rather than guessing, which is the behaviour that keeps an
// unknown price from becoming a free pass.
type Price struct {
	// USD per million tokens.
	InPerM, OutPerM float64
	// When this was written down, and where it came from.
	Recorded string
	Source   string
}

// prices is keyed engine/model, the same string a passport and the roster use.
var prices = map[string]Price{
	"deepseek/deepseek-chat": {
		InPerM: 0.27, OutPerM: 1.10,
		Recorded: "2026-08-24",
		Source:   "this repository's own engine catalogue, which states it in prose",
	},
	"anthropic/claude-sonnet-5": {
		InPerM: 3.00, OutPerM: 15.00,
		Recorded: "2026-08-24", Source: "@claude, unverified against the vendor",
	},
	"anthropic/claude-opus-5": {
		InPerM: 15.00, OutPerM: 75.00,
		Recorded: "2026-08-24", Source: "@claude, unverified against the vendor",
	},
	"openrouter/deepseek/deepseek-chat": {
		InPerM: 0.27, OutPerM: 1.10,
		Recorded: "2026-08-24", Source: "@claude, the router passes the model's own price through",
	},
	"openrouter/moonshotai/kimi-k2": {
		InPerM: 0.60, OutPerM: 2.50,
		Recorded: "2026-08-24", Source: "@claude, unverified against the vendor",
	},
	"openrouter/anthropic/claude-sonnet-4": {
		InPerM: 3.00, OutPerM: 15.00,
		Recorded: "2026-08-24", Source: "@claude, unverified against the vendor",
	},
}

// PriceFor returns what one model costs, and whether a price is known at all.
//
// An unknown model is not free and must not be treated as free. The caller's
// job is to refuse, and this returns ok=false so that refusing is the easy
// path rather than the one somebody has to remember.
func PriceFor(engine, model string) (Price, bool) {
	if engine == "" {
		return Price{}, false
	}
	if p, ok := prices[engine+"/"+model]; ok {
		return p, true
	}
	p, ok := prices[model]
	return p, ok
}

// DefaultModel is the model an engine uses when nobody chose one.
//
// The hire form picks an ENGINE, which is a route and a bill, not a model, so
// something has to choose. The cheapest model the engine offers, deliberately:
// a default that costs the most is a default nobody meant to accept.
func DefaultModel(engine string) string {
	for _, e := range Catalogue {
		if e.ID != engine || len(e.Models) == 0 {
			continue
		}
		best, bestCost := "", 0.0
		for _, m := range e.Models {
			p, ok := PriceFor(engine, m)
			if !ok {
				continue
			}
			c := p.InPerM + p.OutPerM
			if best == "" || c < bestCost {
				best, bestCost = m, c
			}
		}
		if best != "" {
			return best
		}
		return e.Models[0]
	}
	return ""
}

// Metered says whether using this engine puts a charge on somebody's account,
// and whether this console has ever heard of it.
//
// A subscription and a local assistant do not meter: the spend is on a
// contract that already exists, which is the whole reason those two are in the
// catalogue. A bound that refused a subscription call for costing 0.00 would
// be refusing the engine most people will actually use.
//
// The second return is what stops an unknown engine from being waved through
// as free. This returned a bare false at first, and the estimator then read
// every analyst in the seeded roster as "on a subscription, nothing new
// billed", because that roster's engines are fixture names like
// "kimi-standard" and are not in this catalogue at all. Unknown is not free.
// It is unknown, and the only safe thing to do with it is refuse.
func Metered(engine string) (metered, known bool) {
	for _, e := range Catalogue {
		if e.ID == engine {
			return e.EnvVar != "", true
		}
	}
	return false, false
}

// PriceTable renders what is known, so a caller can print the numbers it is
// about to rely on rather than hiding them.
func PriceTable() string {
	keys := make([]string, 0, len(prices))
	for k := range prices {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		p := prices[k]
		fmt.Fprintf(&b, "  %-38s in %6.2f  out %6.2f  per million   %s, %s\n",
			k, p.InPerM, p.OutPerM, p.Recorded, p.Source)
	}
	return b.String()
}

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
//   - The DeepSeek and Kimi rows routed through OpenRouter are @measured 2026-08-24, by
//     GET https://openrouter.ai/api/v1/models with a live key, reading each
//     model's own pricing.prompt and pricing.completion and multiplying by a
//     million. They were @claude estimates first, and all were HIGH:
//     0.27/1.10 against a real 0.2574/1.0287, and 0.60/2.50 against 0.57/2.30.
//     High is the safe direction for a bound, and being close is not the same
//     as being right.
//   - The Anthropic model routed through OpenRouter is Sonnet 5 since
//     2026-10-15, when OpenRouter listed no provider at all for
//     anthropic/claude-sonnet-4 (`GET
//     https://openrouter.ai/api/v1/models/anthropic/claude-sonnet-4/endpoints`
//     answered an empty `endpoints`), so the old row priced a model nobody
//     served. Sonnet 5's endpoints list two prices, 2.00/10.00 (Anthropic and
//     the global endpoints) and 2.20/11.00 (the regional ones); the row takes
//     the higher, because OpenRouter picks the provider and low is the
//     direction that costs money.
//   - The two Anthropic direct entries are @measured 2026-10-13 from the
//     vendor's own price list, `curl -sL
//     https://platform.claude.com/docs/en/about-claude/pricing.md | grep -E
//     '^\| Claude (Sonnet 5|Opus 5) '`, its "Model pricing" rows (the API
//     lists models and not prices, so the page is the source). They were
//     @claude estimates from 2026-08-24 first, and both were HIGH: Sonnet 5
//     3/15 against a real 2/10 (its launch price became the standard price;
//     the rise to 3/15 scheduled for 2026-09-01 did not happen), Opus 5 15/75
//     against a real 5/25. These are the Claude API's global list rates; a
//     regional endpoint or `inference_geo` costs 10 percent more.
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
		InPerM: 0.2574, OutPerM: 1.0287,
		Recorded: "2026-08-24",
		Source:   "@measured, GET openrouter.ai/api/v1/models",
	},
	"anthropic/claude-sonnet-5": {
		InPerM: 2.00, OutPerM: 10.00,
		Recorded: "2026-10-13",
		Source:   "@measured, platform.claude.com/docs/en/about-claude/pricing.md, Model pricing",
	},
	"anthropic/claude-opus-5": {
		InPerM: 5.00, OutPerM: 25.00,
		Recorded: "2026-10-13",
		Source:   "@measured, platform.claude.com/docs/en/about-claude/pricing.md, Model pricing",
	},
	"openrouter/deepseek/deepseek-chat": {
		InPerM: 0.2574, OutPerM: 1.0287,
		Recorded: "2026-08-24", Source: "@measured, GET openrouter.ai/api/v1/models",
	},
	"openrouter/moonshotai/kimi-k2": {
		InPerM: 0.57, OutPerM: 2.30,
		Recorded: "2026-08-24", Source: "@measured, GET openrouter.ai/api/v1/models",
	},
	"openrouter/anthropic/claude-sonnet-5": {
		InPerM: 2.20, OutPerM: 11.00,
		Recorded: "2026-10-15", Source: "@measured, GET openrouter.ai/api/v1/models/anthropic/claude-sonnet-5/endpoints",
	},
	// Bedrock, eu-central-1, ON-DEMAND. Read from AWS's own price list, not
	// from a page: `aws pricing get-products --service-code AmazonBedrock
	// --filters model=<name> regionCode=eu-central-1`, taking the rows whose
	// feature is "On-demand Inference".
	//
	// Two things that are easy to get wrong and were both nearly got wrong
	// here. The unit is 1K TOKENS, not one token and not a million, so the
	// figure needs multiplying by a thousand and multiplying it by a million
	// produces a price a thousand times too high, which is safe but absurd.
	// And every model returns TWO on-demand-looking rows exactly half apart:
	// the cheaper is "Batch Inference", which this engine does not use. Taking
	// the cheaper one would put the bound at half the real price, and low is
	// the direction that costs money.
	//
	// The region is in the number. These are eu-central-1 and they are higher
	// than us-east-1: a price table keyed only by model is a price table that
	// is wrong for somebody.
	"bedrock/eu.amazon.nova-micro-v1:0": {
		InPerM: 0.046, OutPerM: 0.184,
		Recorded: "2026-09-01",
		Source:   "@measured, aws pricing get-products, AmazonBedrock, eu-central-1, on-demand",
	},
	"bedrock/eu.amazon.nova-lite-v1:0": {
		InPerM: 0.078, OutPerM: 0.312,
		Recorded: "2026-09-01",
		Source:   "@measured, aws pricing get-products, AmazonBedrock, eu-central-1, on-demand",
	},
	"bedrock/eu.amazon.nova-pro-v1:0": {
		InPerM: 1.05, OutPerM: 4.20,
		Recorded: "2026-09-01",
		Source:   "@measured, aws pricing get-products, AmazonBedrock, eu-central-1, on-demand",
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
	if engine == LocalID {
		// Any model the operator names, at the price the operator set. Never the
		// bare-model fallback below: "llama3" must not pick up a vendor's row.
		return localPrice()
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
	if engine == LocalID {
		// The operator's own model, or nothing: there is no cheapest one to
		// choose on somebody else's server.
		s, _ := Local()
		return s.Model
	}
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
			// Stated on the engine, not inferred from whether it holds a key.
			//
			// `e.EnvVar != ""` was the test until 2026-09-01 and it was right
			// by accident: every metered engine here happened to take its
			// credential from one variable. Bedrock bills per token and takes
			// none, so the inference read it as a subscription, which is the
			// reading this function's second return value exists to prevent.
			return e.Metered, true
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
	if s, ok := Local(); ok {
		p, _ := localPrice()
		fmt.Fprintf(&b, "  %-38s in %6.2f  out %6.2f  per million   %s, %s\n",
			LocalID+"/"+s.Model, p.InPerM, p.OutPerM, p.Recorded, p.Source)
	}
	return b.String()
}

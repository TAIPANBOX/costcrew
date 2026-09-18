package deliver

// Settlement is what the GATEWAY said a call cost, read off its response
// headers. costcrew#67: until 2026-09-18 nothing here read a response header
// at all, and a run whose one call the gateway settled at 0.05811 printed
// "Spent 0.0116" and booked that to the board, because tasks.live_micros,
// run.total() and the tool_call event all carried deliver.ActualMicros, the
// provider's token counts at THIS repository's own price table, which is an
// estimate of a bill and not the bill.
//
// Three headers, on every metered 2xx (tokenfuse crates/gateway/src/proxy.rs;
// the names are frozen in that repository's COMPATIBILITY.md):
//
//	x-fuse-cost-usd   THIS call's settled cost, "{:.6}" USD, e.g. 0.058110
//	x-fuse-spent-usd  the RUN's cumulative spend as the gateway's ledger sees it
//	x-fuse-price      known | fallback: whether the model was in its price book
//
// The per-call charge is the FIRST, never the second: this runner shares one
// run id across every task of an invocation and runs four at once, so the
// cumulative figure would book task N with every earlier task's calls, the
// overstatement invariant 18 exists to prevent. The second is read only for
// the run's summary line, as the largest value seen.
//
// A value that is missing, empty, duplicated, signed, non-numeric, negative
// or absurd is ABSENT: never a charge, never a panic. Money never passes
// through float64 here (invariant 25): the shape check below admits digits
// and one dot only, and money.ParseMicros does the arithmetic.

import (
	"net/http"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

const (
	HeaderFuseCostUSD  = "x-fuse-cost-usd"
	HeaderFuseSpentUSD = "x-fuse-spent-usd"
	HeaderFusePrice    = "x-fuse-price"
)

// MaxSettlementMicros is the absurd-value cap, 1,000,000 USD: the run's
// ceiling is typed by hand in dollars, and a settlement above a million
// dollars for one call or one run is a broken gateway, not a bill. It also
// keeps every accepted value far inside int64 and inside what
// money.Micros.Cents can round.
const MaxSettlementMicros int64 = 1_000_000 * 1_000_000

// maxFuseUSDBytes bounds a header value BEFORE it is parsed. "{:.6}" of any
// amount under the cap is at most 14 bytes ("999999.999999"); 32 leaves room
// for a wider integer part and refuses a megabyte unread.
const maxFuseUSDBytes = 32

// Settlement is carried on Result (embedded) and on every tool-loop round.
type Settlement struct {
	Settled       bool  // x-fuse-cost-usd was present, well-formed and inside the cap
	SettledMicros int64 // this call's own settled cost, when Settled; 0 otherwise

	RunSpentKnown  bool  // x-fuse-spent-usd was present and well-formed
	RunSpentMicros int64 // the run's cumulative spend as the gateway sees it, when known

	// PriceBasis is "known", "fallback", or "" when the header is absent or
	// carries a word this file does not know. Informational: the charge is
	// the settlement either way; the console line names "fallback".
	PriceBasis string
}

// ParseSettlement reads the three headers. It never panics and never errors:
// a bad value is absent, and the caller then prices the call itself.
func ParseSettlement(h http.Header) Settlement {
	var s Settlement
	if m, ok := parseFuseUSD(oneHeader(h, HeaderFuseCostUSD)); ok {
		s.Settled, s.SettledMicros = true, m
	}
	if m, ok := parseFuseUSD(oneHeader(h, HeaderFuseSpentUSD)); ok {
		s.RunSpentKnown, s.RunSpentMicros = true, m
	}
	switch v := oneHeader(h, HeaderFusePrice); v {
	case "known", "fallback":
		s.PriceBasis = v
	}
	return s
}

// oneHeader is the value when the header appears exactly once, else "": two
// values for one name is an ambiguity, and an ambiguous charge is no charge.
func oneHeader(h http.Header, name string) string {
	vs := h.Values(name)
	if len(vs) != 1 {
		return ""
	}
	return vs[0]
}

// parseFuseUSD is the stricter sibling of money.ParseMicros this file needs:
// ParseMicros accepts a leading sign, and a signed settlement is not a
// shape the gateway ever sends. Digits and at most one dot, at least one
// digit, at most maxFuseUSDBytes bytes after trimming; then ParseMicros;
// then the cap.
func parseFuseUSD(raw string) (int64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > maxFuseUSDBytes {
		return 0, false
	}
	dots, digits := 0, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			digits++
		case c == '.':
			dots++
		default:
			return 0, false
		}
	}
	if digits == 0 || dots > 1 {
		return 0, false
	}
	m, err := money.ParseMicros(s)
	if err != nil {
		return 0, false
	}
	if m < 0 || int64(m) > MaxSettlementMicros {
		return 0, false
	}
	return int64(m), true
}

// AddRound folds one more round's settlement into a task's running total. A
// task is settled only when EVERY round of it was: a sum that mixed the
// gateway's figure for some rounds with the runner's own for others would be
// a third kind of number under one heading. The run's spend is the largest
// cumulative seen; the price basis is "fallback" if any round said so,
// "known" only if every round did.
func (s Settlement) AddRound(r Settlement) Settlement {
	out := Settlement{
		Settled:        s.Settled && r.Settled,
		RunSpentKnown:  s.RunSpentKnown || r.RunSpentKnown,
		RunSpentMicros: max(s.RunSpentMicros, r.RunSpentMicros),
	}
	if out.Settled {
		out.SettledMicros = s.SettledMicros + r.SettledMicros
	}
	switch {
	case s.PriceBasis == "fallback" || r.PriceBasis == "fallback":
		out.PriceBasis = "fallback"
	case s.PriceBasis == "known" && r.PriceBasis == "known":
		out.PriceBasis = "known"
	}
	return out
}

// Charge is the ONE function every recording site reads (tasks.live_micros,
// run.settle, the tool_call event, the plan-ask ledger): the settlement when
// the call was settled, the caller's own priced figure otherwise. One
// function rather than an if at each site, for the reason reservedWorstCase
// exists in tools/run: two copies of a rule is how two figures come to
// disagree.
func Charge(s Settlement, priced int64) int64 {
	if s.Settled {
		return s.SettledMicros
	}
	return priced
}

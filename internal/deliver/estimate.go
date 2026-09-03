package deliver

// The worst-case pricing arithmetic tools/run's own price() has always done,
// shared here (B5-SPEC.md section 3 point 3) for the same reason Packet,
// Prompt and Tokens already are: Go refuses to import a second "package
// main", so internal/web's /cadence page cannot call tools/run's price()
// directly, and a second, hand-copied formula is exactly the drift B7-SPEC.md
// moved the packet builder here to avoid in the first place.

import (
	"database/sql"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/engines"
)

// WorstCaseMicros is one call's worst case, in micro-dollars: the prompt at
// the engine's input price, plus the full output cap at its output price,
// because how long an answer runs is not known before it is asked for. The
// same formula tools/run/main.go's price() has always used, extracted rather
// than reimplemented.
func WorstCaseMicros(promptTokens, maxOutputTokens int, p engines.Price) int64 {
	in := float64(promptTokens) / 1e6 * p.InPerM
	out := float64(maxOutputTokens) / 1e6 * p.OutPerM
	return int64((in + out) * 1e6)
}

// ActualMicros is one call's REAL cost, in micro-dollars, from the token
// counts a call actually reports rather than the worst case a call was
// bounded by. The same formula tools/run/loop.go's own roundCostMicros has
// always used (moved here, B6B-SPEC.md, so tools/bench's live path can
// price what a call actually cost the same way the runner's tool loop
// does, rather than a second copy that only looks like it); loop.go keeps
// a one-line wrapper of its old unexported name.
func ActualMicros(inTokens, outTokens int, p engines.Price) int64 {
	in := float64(inTokens) / 1e6 * p.InPerM
	out := float64(outTokens) / 1e6 * p.OutPerM
	return int64((in + out) * 1e6)
}

// estimateDate is the fixed, non-moving date tools/run's own price() sends
// its estimate-only prompt with: the estimate must not change because the
// clock did, and every date is the same ten bytes. Shared so the console's
// preview and the runner's own preflight price the identical prompt.
const estimateDate = "0000-00-00"

// MaxToolRounds and LoopsFor are tools/run/loop.go's own tool-calling-loop
// cap and per-engine multiplier, mirrored here (PRICE-DISPLAY-SPEC.md,
// 2026-09-03) so a worst case computed where "package main" cannot be
// imported -- this package, and internal/web's /cadence page through it --
// uses the IDENTICAL multiplier tools/run's own execute() reserves before
// the first call. tools/run/loop.go's maxToolRounds and loopsFor are now
// one-line wrappers of these two, the same "moved here, old name kept as a
// wrapper" shape every other shared formula in this file already uses (see
// WorstCaseMicros and ActualMicros' own comments).
//
// Found reading live.go and main.go while confirming the incident this spec
// describes: one execute() of a task on the tool loop (anthropic or
// openrouter) can make up to MaxToolRounds model calls, each one costing up
// to the SAME one-token-per-byte/full-output-cap bound a single call does
// (looping does not shrink the per-round bound, it multiplies the call
// COUNT), so the true worst case a run ever reserves for such a task is
// WorstCaseMicros times this, never WorstCaseMicros alone. Before this fix,
// EstimateWorstCase returned the unmultiplied figure -- correct for a
// single-call engine, understating by up to MaxToolRounds times for
// anthropic or openrouter, exactly the gap tools/run's own report() had.
const MaxToolRounds = 6

// LoopsFor is how many model calls one execute() of a task on this engine
// can make: the tool loop's ceiling for the two engines it covers, one call
// for every other engine.
func LoopsFor(engine string) int {
	switch engine {
	case "anthropic", "openrouter":
		return MaxToolRounds
	}
	return 1
}

// EstimateWorstCase prices one task for one analyst the way tools/run's own
// price() does -- the packet's bytes, the prompt built around them, and the
// engine's published rate -- and then reserves it the way tools/run's own
// execute() does: one call's own bound, times LoopsFor(a.Engine). It does
// not know or care about a task's own per-task guard (tools/run's price()
// layers that comparison on top for its own Verdict/Refused fields); this
// returns the RESERVED worstMicros, a model name, and whether it could be
// priced at all, which is exactly what a preview that has no task guard yet
// (B5-SPEC.md's due list, priced before a sprint exists) needs to show a
// person the number a live run of that same due list would actually
// reserve -- not price()'s own single-call e.WorstMicros field, which stays
// the one-call bound (tools/run's own reservedWorstCase(e) layers the same
// multiplier on top of THAT, separately, so the two call sites can never
// drift apart: see main.go's own comment).
//
// priced is false, and worstMicros and model carry no meaning, when the
// engine is unknown, unmetered, or has no published price -- the same three
// refusal reasons price() names, collapsed to one bool because a preview row
// has no verdict sentence to fill in, only a price or a dash.
func EstimateWorstCase(db *sql.DB, t crew.Task, a crew.Analyst, maxOutputTokens int) (worstMicros int64, model string, priced bool) {
	model = engines.DefaultModel(a.Engine)
	metered, known := engines.Metered(a.Engine)
	if !known || !metered {
		return 0, model, false
	}
	p, ok := engines.PriceFor(a.Engine, model)
	if !ok {
		return 0, model, false
	}
	pk := Packet(db, t, a, false)
	promptTokens := Tokens(Prompt(t, a, estimateDate, pk))
	oneCall := WorstCaseMicros(promptTokens, maxOutputTokens, p)
	return oneCall * int64(LoopsFor(a.Engine)), model, true
}

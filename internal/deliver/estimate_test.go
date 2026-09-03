package deliver_test

// B5-SPEC.md section 3 point 3: "-due prices the list the way -live prices a
// sprint (worst case per task from PerTask, the packet's bytes, the engine's
// published price)". tools/run's own price() already does this arithmetic;
// this is the piece pulled out so internal/web's /cadence page (which cannot
// import tools/run -- Go refuses a second "package main", the exact reason
// B7-SPEC.md moved Packet/Prompt/Tokens here in the first place) prices a
// due item through the SAME formula rather than a second copy that could
// drift from it.
//
// Red first: neither function exists on main, so this file does not
// compile -- a build error naming exactly WorstCaseMicros and
// EstimateWorstCase, the same shape this repository's own tests already
// treat as "red" for a brand-new export (tools/run/gateway_test.go's header).

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

func TestWorstCaseMicrosMultipliesByThePublishedRate(t *testing.T) {
	p := engines.Price{InPerM: 1_000_000, OutPerM: 2_000_000} // $1/token in, $2/token out, to keep the arithmetic readable
	got := deliver.WorstCaseMicros(3, 5, p)
	// 3 tokens in at $1 = $3; 5 tokens out at $2 = $10; total $13 = 13,000,000 micros.
	want := int64(13_000_000)
	if got != want {
		t.Errorf("WorstCaseMicros(3, 5, {1e6, 2e6}) = %d, want %d", got, want)
	}
}

func TestWorstCaseMicrosOfZeroTokensIsZero(t *testing.T) {
	p := engines.Price{InPerM: 3.00, OutPerM: 15.00}
	if got := deliver.WorstCaseMicros(0, 0, p); got != 0 {
		t.Errorf("WorstCaseMicros(0, 0, ...) = %d, want 0", got)
	}
}

// An unknown engine is refused, not priced as free: the console page must
// show the same "cannot be priced" state the runner's own price() shows,
// never a silent zero.
func TestEstimateWorstCaseOnAnUnknownEngineIsNotPriced(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	task := crew.Task{Title: "t", Goal: "g"}
	a := crew.Analyst{Name: "a", State: "active", Engine: "a-name-from-nowhere", Skills: []string{"anomaly-triage"}}

	_, _, priced := deliver.EstimateWorstCase(db, task, a, 2000)
	if priced {
		t.Error("an unknown engine was priced; it must come back unpriced, the same as tools/run's own price()")
	}
}

// A real, metered engine prices above zero, from the same PriceFor table
// tools/run's own price() reads.
func TestEstimateWorstCasePricesARealEngine(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	task := crew.Task{Title: "Explain the move", Goal: "say what happened"}
	a := crew.Analyst{Name: "a", State: "active", Engine: "openrouter", Skills: []string{"anomaly-triage"}}

	worst, model, priced := deliver.EstimateWorstCase(db, task, a, 2000)
	if !priced {
		t.Fatal("a real, metered engine (openrouter) came back unpriced")
	}
	if model == "" {
		t.Error("priced but named no model")
	}
	if worst <= 0 {
		t.Errorf("worst case = %d micros, want > 0", worst)
	}
}

// The date this prices against is fixed, not today's -- the same reason
// tools/run's own price() uses "0000-00-00": two estimates of the same task
// must not differ because the clock did, and that has to hold across
// packages now that both the runner and the console price the same item.
func TestEstimateWorstCaseDoesNotMoveWithTheClock(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	task := crew.Task{Title: "t", Goal: "g"}
	a := crew.Analyst{Name: "a", State: "active", Engine: "openrouter", Skills: []string{"anomaly-triage"}}

	w1, _, _ := deliver.EstimateWorstCase(db, task, a, 2000)
	w2, _, _ := deliver.EstimateWorstCase(db, task, a, 2000)
	if w1 != w2 {
		t.Errorf("two estimates of the same task differ: %d vs %d", w1, w2)
	}
}

// PRICE-DISPLAY-SPEC.md, 2026-09-03. The /cadence page shows this figure to
// a person before they flip the cadence switch on; the run that preview
// describes is tools/run -due -live, which creates the due items as
// ordinary tasks and runs them through the SAME execute() and the SAME tool
// loop (tools/run/loop.go) an ordinary sprint task goes through. A
// cadence-due task on anthropic or openrouter can therefore make up to
// loopsFor(engine) model calls in one execute(), each reserved at THIS
// task's own worst case before the first round is sent (live.go's own
// execute()) -- so EstimateWorstCase must return that RESERVED figure, not
// one call's own bound, or the preview understates by up to loopsFor's own
// factor exactly the way tools/run's own report() did.
//
// The expected multiplier (6) is hardcoded here rather than read from
// tools/run/loop.go's own loopsFor: this package cannot import "package
// main" to call it (the exact restriction this file's own package comment
// already names for Packet/Prompt/Tokens), and a test that reads the
// multiplier from wherever this fix stores it would only prove the function
// agrees with itself.
func TestEstimateWorstCaseReturnsTheReservedFigureNotOneCallsOwnBound(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	task := crew.Task{Title: "Explain the move", Goal: "say what happened"}
	a := crew.Analyst{Name: "a", State: "active", Engine: "anthropic", Skills: []string{"anomaly-triage"}}

	p, ok := engines.PriceFor(a.Engine, engines.DefaultModel(a.Engine))
	if !ok {
		t.Fatal("no published price for the fixture's own engine/model")
	}
	pk := deliver.Packet(db, task, a, false)
	oneCall := deliver.WorstCaseMicros(deliver.Tokens(deliver.Prompt(task, a, "0000-00-00", pk)), 2000, p)
	if oneCall <= 0 {
		t.Fatal("the fixture's own one-call worst case is zero")
	}

	worst, _, priced := deliver.EstimateWorstCase(db, task, a, 2000)
	if !priced {
		t.Fatal("the fixture came back unpriced")
	}
	const wantMultiplier = 6 // tools/run/loop.go's own loopsFor("anthropic")
	if want := oneCall * wantMultiplier; worst != want {
		t.Errorf("EstimateWorstCase = %d, want %d (one call's own bound %d times %d, "+
			"the tool loop's own multiplier for anthropic): the /cadence preview must "+
			"show what a live -due run of this task would actually reserve, not one "+
			"round's own bound", worst, want, oneCall, wantMultiplier)
	}
}

// TestEstimateWorstCaseIsUnchangedForASingleCallEngine is the boundary
// PRICE-DISPLAY-SPEC.md asks for on the other side: bedrock (and every
// engine outside the tool loop, tools/run/loop.go's runToolLoop) never
// makes more than one call per execute(), so the multiplier here must be a
// no-op, 1x, not a second bound layered on top of the first.
func TestEstimateWorstCaseIsUnchangedForASingleCallEngine(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()

	task := crew.Task{Title: "Explain the move", Goal: "say what happened"}
	a := crew.Analyst{Name: "a", State: "active", Engine: "bedrock", Skills: []string{"anomaly-triage"}}

	p, ok := engines.PriceFor(a.Engine, engines.DefaultModel(a.Engine))
	if !ok {
		t.Fatal("no published price for the fixture's own engine/model")
	}
	pk := deliver.Packet(db, task, a, false)
	oneCall := deliver.WorstCaseMicros(deliver.Tokens(deliver.Prompt(task, a, "0000-00-00", pk)), 2000, p)
	if oneCall <= 0 {
		t.Fatal("the fixture's own one-call worst case is zero")
	}

	worst, _, priced := deliver.EstimateWorstCase(db, task, a, 2000)
	if !priced {
		t.Fatal("the fixture came back unpriced")
	}
	if worst != oneCall {
		t.Errorf("EstimateWorstCase = %d for a single-call engine, want %d unchanged "+
			"(bedrock never enters the tool loop, so the multiplier must be a no-op "+
			"here)", worst, oneCall)
	}
}

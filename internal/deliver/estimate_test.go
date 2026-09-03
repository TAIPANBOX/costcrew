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

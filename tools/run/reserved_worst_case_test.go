package main

// PRICE-DISPLAY-SPEC.md, 2026-09-03. Found running the FIRST real live crew
// task on a real Anthropic account (`tools/run -live -only 294 -ceiling
// 0.04`): the dry-run report (no -live) printed "worst case 0.0385" for
// task 294; the live run's own pre-call reserve refused it, saying "this
// call could cost 0.2312" -- almost exactly 6x, loopsFor("anthropic")'s own
// return.
//
// report()'s summary line and per-task table, and price()'s own per-task
// Verdict, compared the guard against e.WorstMicros, ONE call's own bound.
// execute()'s reserve() call has always multiplied by loopsFor(e.Engine),
// because one execute() of a task on the tool loop (loop.go) can make up to
// that many model calls, each one reserved before the first round is ever
// sent. The two had never been checked against each other before tonight.
// spend()'s own whole-run preflight (live.go) and -due's own dueWorstMicros
// (due.go, tested in due_test.go) turned out to carry the identical gap,
// found reading this file rather than named by the spec: neither multiplies
// either, so both "the worst case of the whole run is checked... before the
// first call" (live.go's own package comment, point 3) and -due's matching
// promise were false for anthropic and openrouter tasks.
//
// reservedWorstCase(e) is the one formula report(), price()'s Verdict,
// execute()'s reserve() call, spend()'s preflight and dueWorstMicros all now
// share, so none of them can diverge from each other the way report() and
// reserve() just did.
//
// This test's own concrete numbers are this fixture's, not task 294's real
// prompt: the incident's own 0.0385 and 0.2312 depended on that night's
// exact packet and are not reproducible byte for byte. What every test below
// asserts is the STRUCTURE of the incident -- a number a person reads before
// choosing -ceiling must equal what a live run actually reserves -- which is
// exactly what was false regardless of the fixture used to show it.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// anthropicTaskPricedLikeTonight prices one task exactly the way a real
// sprint's dry run prices it (price(), the same function report()'s own
// table is built from), for an analyst on the same engine task 294's own
// analyst was hired with. db is nil, the same as TestAnUnknownEngineIsRefused
// and its neighbours already do: price()'s own packet() tolerates a nil db
// for a task carrying no Anomaly.
func anthropicTaskPricedLikeTonight(guard money.Cents) estimate {
	task := crew.Task{Title: "Explain the Amazon EC2 move on 2026-09-02",
		Goal: "410.00 above of baseline on the aws desk. Say what happened, " +
			"whether it recurs, and what it would take to stop it.",
		Budget: guard}
	an := crew.Analyst{Name: "investigator-aws", Engine: "anthropic", State: "active"}
	return price(nil, task, an, 2000)
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything it printed. report() and spend() both print with fmt.Printf
// rather than returning a string, so this is the only way to read what a
// person would actually see on the terminal.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	os.Stdout = old
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// isRefusal reports whether err is a budget refusal (runBudget.reserve's own
// error, wrapped by execute()), the same check spend()'s own goroutine makes
// (live.go) to tell "the ceiling said no" apart from "the call itself
// failed".
func isRefusal(err error) bool {
	var r refusal
	return errors.As(err, &r)
}

// reservedFigureViaExecute finds the exact worst case execute() reserves for
// e by probing runBudget directly rather than duplicating live.go's own
// arithmetic in the test: runBudget.reserve compares with a strict >, so a
// ceiling one micro under the reserved figure refuses and the figure itself
// does not -- the boundary between the two IS the reserved figure. Neither
// side of the probe ever reaches the network: execute()'s reserve() check
// runs before runToolLoop is called at all (live.go), so a refusal here
// never reaches http.Client.Do, and this test sets no ANTHROPIC_API_KEY and
// dials no server.
func reservedFigureViaExecute(t *testing.T, e estimate) int64 {
	t.Helper()
	lo, hi := int64(0), e.WorstMicros*int64(maxToolRounds)+1
	for lo < hi {
		mid := (lo + hi) / 2
		run := &runBudget{ceilingMicros: mid}
		err := execute(context.Background(), nil, nil, e, 2000, run, bus{}, gatewayConfig{})
		if err != nil && isRefusal(err) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// TestReportsWorstCaseIsWhatTheLiveRunWouldActuallyReserve is tonight's own
// regression, replayed: report()'s printed worst case (the number a person
// reads before choosing -ceiling) and the figure execute()'s own reserve()
// requires before it lets the first round through must be the SAME number.
//
// Red, on main, before this fix (@claude 2026-09-03, this fixture's own
// numbers, quoted from the actual failing run): report()'s printed worst
// case was $0.0373 (37,300 micros); the live reserve boundary was $0.2237
// (223,722 micros) -- exactly 6x, loopsFor("anthropic"):
//
//	reserved_worst_case_test.go:152: report()'s printed worst case (0.0373)
//	does not equal what a live run would actually reserve (223722 micros,
//	0.2237), or appears only in the summary line and not the per-task table
//	(want it twice: the run total and this task's own row); tonight's own
//	incident was exactly this gap, task 294: dry run 0.0385, reserve 0.2312,
//	ratio exactly loopsFor("anthropic")=6
func TestReportsWorstCaseIsWhatTheLiveRunWouldActuallyReserve(t *testing.T) {
	e := anthropicTaskPricedLikeTonight(money.Cents(100_00))
	if e.Refused || !e.Priced {
		t.Fatalf("the fixture task was not priced cleanly: refused=%v verdict=%q", e.Refused, e.Verdict)
	}
	if e.WorstMicros <= 0 {
		t.Fatal("the fixture's single-call worst case is zero; nothing to multiply")
	}

	out := captureStdout(t, func() {
		report(nil, []estimate{e}, 2000, money.Cents(0), false)
	})

	reserved := reservedFigureViaExecute(t, e)
	wantLine := usd(reserved)

	if strings.Count(out, wantLine) < 2 {
		t.Errorf("report()'s printed worst case (%s) does not equal what a live run "+
			"would actually reserve (%d micros, %s), or appears only in the summary "+
			"line and not the per-task table (want it twice: the run total and this "+
			"task's own row); tonight's own incident was exactly this gap, task 294: "+
			"dry run 0.0385, reserve 0.2312, ratio exactly loopsFor(\"anthropic\")=%d\nfull output:\n%s",
			usd(e.WorstMicros), reserved, wantLine, loopsFor("anthropic"), out)
	}
}

// TestPriceRefusesATaskTheMultipliedWorstCaseCannotAffordEvenWhenTheSingleCallCanAffordIt
// is the other half of the same gap: price()'s own per-task Verdict compared
// the guard against e.WorstMicros, the single call's own bound, so a task
// could print "inside its guard" in a dry run and then be refused live once
// reserve() applied the multiplier the Verdict was never compared against
// (PRICE-DISPLAY-SPEC.md's own words, "The fix", first bullet). The guard
// below sits strictly between the single-call worst case and the multiplied
// one, so the two readings disagree if and only if the bug is present.
func TestPriceRefusesATaskTheMultipliedWorstCaseCannotAffordEvenWhenTheSingleCallCanAffordIt(t *testing.T) {
	probe := anthropicTaskPricedLikeTonight(money.Cents(100_000_00))
	if probe.Refused || probe.WorstMicros <= 0 {
		t.Fatalf("could not learn the fixture's single-call worst case: %+v", probe)
	}
	single := probe.WorstMicros
	multiplied := single * int64(loopsFor("anthropic"))
	if multiplied <= single {
		t.Fatal(`loopsFor("anthropic") is 1: this test needs a looping engine to mean anything`)
	}

	guardMicros := single + (multiplied-single)/2
	guard := money.Cents((guardMicros + 9_999) / 10_000)
	if int64(guard)*10_000 <= single || int64(guard)*10_000 >= multiplied {
		t.Fatalf("the guard %s does not sit strictly between the single-call worst "+
			"case %s and the multiplied one %s; the fixture's numbers changed under "+
			"this test", guard, usd(single), usd(multiplied))
	}

	e := anthropicTaskPricedLikeTonight(guard)
	if !e.Refused {
		t.Errorf("a task whose MULTIPLIED worst case (%s) is past its guard (%s) was "+
			"priced as fitting inside it, verdict %q -- the guard only ever compared "+
			"the single call's own bound (%s)", usd(multiplied), guard, e.Verdict, usd(single))
	}
	if !strings.Contains(e.Verdict, "past what is left of its guard") {
		t.Errorf("refused for the wrong reason: %q", e.Verdict)
	}
}

// TestSpendRefusesTheWholeRunBeforeTheFirstCallWhenTheMultipliedWorstExceedsTheCeiling
// is spend()'s own copy of the same gap, found reading live.go while
// confirming report()'s (not named by PRICE-DISPLAY-SPEC.md's own fix list,
// which names report() and price()'s Verdict specifically, but the SAME
// unmultiplied sum, in the SAME file, guarding the SAME promise live.go's
// own package comment makes: point 3, "The worst case of the whole run is
// checked against that ceiling before the first call"). A ceiling strictly
// between the single-call and multiplied sums must refuse HERE, in spend()'s
// own preflight, rather than reach execute()'s reserve() only to fail there
// silently: spend()'s own goroutine prints a refusal and swallows it into a
// nil return (see spend()'s own comment, "A refusal stops the run" -- the
// function itself never returns that refusal to its caller), so a caller
// checking only spend()'s error -- as -due's own dueExecute does -- would
// see success.
//
// db is a real, empty store (dueTestDB, due_test.go): on unfixed code this
// run proceeds past the preflight into execute(), which settles nothing but
// still reaches crew.SettleLiveSpend(db) on the way out, and that needs real
// schema, not a nil *sql.DB, to run cleanly in either state of this fix.
func TestSpendRefusesTheWholeRunBeforeTheFirstCallWhenTheMultipliedWorstExceedsTheCeiling(t *testing.T) {
	probe := anthropicTaskPricedLikeTonight(money.Cents(100_000_00))
	if probe.Refused || probe.WorstMicros <= 0 {
		t.Fatalf("could not learn the fixture's single-call worst case: %+v", probe)
	}
	single := probe.WorstMicros
	multiplied := single * int64(loopsFor("anthropic"))
	guardMicros := single + (multiplied-single)/2
	cap := money.Cents((guardMicros + 9_999) / 10_000)

	// The per-task guard must not be what refuses this: generous, well past
	// the multiplied figure, so only the RUN's own ceiling is on trial.
	e := anthropicTaskPricedLikeTonight(money.Cents(100_000_00))
	e.Task.ID = 294 // tonight's own task id, for a reader matching this to the incident

	db := dueTestDB(t)
	err := spend(db, nil, []estimate{e}, 2000, cap, 0, bus{}, gatewayConfig{})
	if err == nil {
		t.Fatalf("spend() returned no error for a ceiling (%s) between the single-call "+
			"worst case (%s) and the multiplied one (%s): the run's own preflight, "+
			"\"the worst case of the whole run is checked... before the first call\" "+
			"(live.go's own package comment), let it through", cap, usd(single), usd(multiplied))
	}
	if !strings.Contains(err.Error(), "refused before the first call") {
		t.Errorf("refused for the wrong reason: %v", err)
	}
}

// -------------------------------------------------------------- boundaries

// TestReservedWorstCaseMultipliesOnlyTheLoopingEngines is the boundary
// PRICE-DISPLAY-SPEC.md names: openrouter gets the same multiplier as
// anthropic, and bedrock (and anything else outside the tool loop) is
// unchanged -- the multiplier is a no-op there, 1x, never applied twice and
// never skipped. This also catches the "multiply by a constant other than
// loopsFor's own return" mutant named in the spec's own Mutants paragraph: a
// hardcoded *6 regardless of engine would fail the bedrock/unknown case
// below, and a hardcoded *1 would fail the anthropic/openrouter cases.
func TestReservedWorstCaseMultipliesOnlyTheLoopingEngines(t *testing.T) {
	for _, c := range []struct {
		engine string
		want   int64
	}{
		{"anthropic", 6},
		{"openrouter", 6},
		{"bedrock", 1},
		{"a-name-from-nowhere", 1},
	} {
		e := estimate{Engine: c.engine, WorstMicros: 1000}
		if got := reservedWorstCase(e); got != 1000*c.want {
			t.Errorf("reservedWorstCase(engine=%q, WorstMicros=1000) = %d, want %d (%dx)",
				c.engine, got, 1000*c.want, c.want)
		}
	}
}

// TestReportAndPriceVerdictNeverDisagreeOnWhichFigureIsMultiplied is the
// third mutant PRICE-DISPLAY-SPEC.md names: "apply the multiplier to the
// guard-headroom comparison but not the printed total (or the reverse)".
// Both price()'s own Verdict/Refused and report()'s own printed total must
// treat the SAME task the SAME way at the SAME boundary: a guard set exactly
// one micro under the reserved figure refuses in both places, and a guard
// exactly at it refuses in neither.
func TestReportAndPriceVerdictNeverDisagreeOnWhichFigureIsMultiplied(t *testing.T) {
	probe := anthropicTaskPricedLikeTonight(money.Cents(100_000_00))
	if probe.Refused || probe.WorstMicros <= 0 {
		t.Fatalf("could not learn the fixture's single-call worst case: %+v", probe)
	}
	reserved := reservedWorstCase(probe)

	exact := money.Cents((reserved + 9_999) / 10_000) // smallest guard covering it exactly
	atExact := anthropicTaskPricedLikeTonight(exact)
	if atExact.Refused {
		t.Errorf("price() refused a task at a guard (%s) exactly covering its "+
			"reserved worst case (%s): %q", exact, usd(reserved), atExact.Verdict)
	}
	outAtExact := captureStdout(t, func() { report(nil, []estimate{atExact}, 2000, exact, true) })
	if strings.Contains(outAtExact, "OVER THE CEILING") {
		t.Errorf("report() called a guard exactly covering the reserved worst case "+
			"OVER THE CEILING:\n%s", outAtExact)
	}

	oneUnder := money.Cents(int64(exact) - 1)
	if int64(oneUnder)*10_000 >= reserved {
		t.Skip("this fixture's own rounding leaves no cent strictly under the reserved figure")
	}
	underExact := anthropicTaskPricedLikeTonight(oneUnder)
	if !underExact.Refused {
		t.Errorf("price() accepted a guard (%s) one cent under its reserved worst "+
			"case (%s): %q", oneUnder, usd(reserved), underExact.Verdict)
	}
}

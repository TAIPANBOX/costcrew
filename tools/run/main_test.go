package main

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// This binary cannot spend, and that is a property of its source rather than
// of anybody's intention.
//
// The safest first version of a thing that spends money is one that holds no
// way to. A flag that defaults to dry is a promise; an import list with no
// HTTP client in it is a fact. When the live half is written it goes in
// another file and this test is what makes that a deliberate act rather than
// a line that slid in.
func TestThisBinaryCannotSpend(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"net/http", "os/exec", "http.Get", "http.Post", "http.Client",
		"exec.Command", "API_KEY", "os.Getenv",
	} {
		if strings.Contains(string(src), forbidden) {
			t.Errorf("main.go now contains %q: this binary is supposed to be "+
				"unable to call anything or read a credential", forbidden)
		}
	}
}

// An engine nobody has heard of is refused, not waved through.
//
// The first version read every analyst in the seeded roster as "on a
// subscription, nothing new billed", because their engines were fixture names
// absent from the catalogue and the check returned a bare false. Unknown is
// not free, and the direction of that mistake is the one that spends.
func TestAnUnknownEngineIsRefused(t *testing.T) {
	e := price(
		crew.Task{Title: "something", Goal: "do it", Budget: money.Cents(1500)},
		crew.Analyst{Name: "an-analyst", Engine: "a-name-from-nowhere", State: "active"},
		2000)
	if !e.Refused {
		t.Errorf("an unknown engine was not refused: %q", e.Verdict)
	}
	if !strings.Contains(e.Verdict, "not an engine this console knows") {
		t.Errorf("it was refused for the wrong reason: %q", e.Verdict)
	}
}

// A quarter of a cent does not round to nothing.
//
// The estimate lives in micro-dollars because money.Cents floors one call on
// the cheap route to zero. The first version printed 0.00 against every task
// AND compared 0 against the guard, so the refusal could never fire: an
// estimator whose bound is always satisfied is not a bound.
func TestASubCentCallIsNotFree(t *testing.T) {
	e := price(
		crew.Task{Title: "explain the move", Goal: "say what happened", Budget: money.Cents(1500)},
		crew.Analyst{Name: "an-analyst", Engine: "openrouter", State: "active"},
		2000)
	if e.Refused {
		t.Fatalf("a normal task was refused: %q", e.Verdict)
	}
	if e.WorstMicros <= 0 {
		t.Fatal("the worst case is zero micro-dollars, so no guard can ever bind it")
	}
	if money.Cents(e.WorstMicros/10_000) != 0 {
		t.Logf("this call is %d micros, which is more than a cent; the rounding "+
			"trap this test is about needs a cheaper model to show", e.WorstMicros)
	}
}

// The per-task guard binds, and it binds on the OUTPUT cap, which is the part
// nobody knows before the call.
func TestTheGuardRefusesAnOutputCapItCannotAfford(t *testing.T) {
	task := crew.Task{Title: "t", Goal: "g", Budget: money.Cents(1500)}
	an := crew.Analyst{Name: "a", Engine: "openrouter", State: "active"}

	if e := price(task, an, 2000); e.Refused {
		t.Fatalf("refused at a normal cap: %q", e.Verdict)
	}
	// Same task, same guard, an output cap it cannot pay for.
	e := price(task, an, 200_000_000)
	if !e.Refused {
		t.Errorf("a cap of two hundred million tokens was not refused against a "+
			"guard of %s: %q", task.Budget, e.Verdict)
	}
	if !strings.Contains(e.Verdict, "past what is left of its guard") {
		t.Errorf("refused for the wrong reason: %q", e.Verdict)
	}
}

// A suspended analyst is not given work, and neither is an unassigned task.
func TestWorkIsNotPricedForSomebodyWhoCannotDoIt(t *testing.T) {
	for _, c := range []struct {
		name string
		a    crew.Analyst
		want string
	}{
		{"nobody assigned", crew.Analyst{}, "nobody is assigned"},
		{"suspended", crew.Analyst{Name: "a", Engine: "openrouter", State: "suspended"}, "is suspended"},
		{"no engine", crew.Analyst{Name: "a", State: "active"}, "no engine"},
	} {
		e := price(crew.Task{Title: "t", Budget: money.Cents(1500)}, c.a, 2000)
		if !e.Refused {
			t.Errorf("%s: not refused, said %q", c.name, e.Verdict)
		} else if !strings.Contains(e.Verdict, c.want) {
			t.Errorf("%s: refused saying %q, expected something about %q",
				c.name, e.Verdict, c.want)
		}
	}
}

// The worst case is a bound, and a bound is not exceeded.
//
// It was len/4, the usual rule of thumb, and the very first live call on the
// Anthropic route cost 0.0185 against a worst case of 0.0182: the prompt came
// in at 174 tokens where the rule predicted 66. A bound the first call steps
// over is an estimate wearing a bound's name.
//
// One token per byte is provable rather than better: no tokeniser splits below
// a byte.
func TestThePromptBoundIsNotExceededByAnyTokeniser(t *testing.T) {
	// Whatever a tokeniser does, it cannot make more tokens than there are
	// bytes, so the bound has to be at least the byte count.
	for _, s := range []string{
		"a",
		"You are supervisor, Crew supervisor on the management desk.",
		"Пояснення українською, де байтів більше ніж символів",
		strings.Repeat("token ", 500),
	} {
		if got := tokens(s); got < len(s) {
			t.Errorf("tokens(%d bytes) = %d, which is below the byte count and "+
				"therefore not an upper bound", len(s), got)
		}
	}
}

// The ceiling holds when several calls are in flight.
//
// A plain running total is not enough: four calls checking against the same
// unspent balance can each be individually correct and collectively walk past
// the ceiling. The budget reserves the worst case before a call and settles
// the difference after, so money in flight is money spent until proven
// otherwise.
func TestTheCeilingHoldsUnderConcurrency(t *testing.T) {
	// A ceiling that fits exactly three calls of 100 micros.
	r := &runBudget{ceilingMicros: 300}

	// Every reservation is HELD until all of them have been attempted, which
	// is the situation the reservation exists for: several calls in flight at
	// once, none of them settled yet.
	//
	// The first version settled immediately, so no two reservations ever
	// overlapped and the running total alone was enough. It passed against a
	// budget that ignored reservations entirely, which is the one fault it was
	// written to catch.
	var wg, hold sync.WaitGroup
	var mu sync.Mutex
	var allowed int
	hold.Add(1)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.reserve(100); err != nil {
				return
			}
			mu.Lock()
			allowed++
			mu.Unlock()
			hold.Wait() // in flight, unsettled, while the others try
			r.settle(100, 100)
		}()
	}
	// Let them all reach the reservation before anything settles.
	time.Sleep(50 * time.Millisecond)
	got := func() int { mu.Lock(); defer mu.Unlock(); return allowed }()
	hold.Done()
	wg.Wait()

	if got > 3 {
		t.Errorf("%d calls were let through against a ceiling that fits three, "+
			"with none of them settled: the reservation is not holding", got)
	}
	if r.total() > r.ceilingMicros {
		t.Errorf("spent %d against a ceiling of %d", r.total(), r.ceilingMicros)
	}
}

// A call that failed costs nothing, and its reservation comes back.
//
// Without this a run of flaky calls would exhaust its own ceiling having
// bought nothing at all.
func TestAFailedCallReturnsItsReservation(t *testing.T) {
	r := &runBudget{ceilingMicros: 100}
	if err := r.reserve(100); err != nil {
		t.Fatal(err)
	}
	r.settle(100, 0) // the call failed
	if r.total() != 0 {
		t.Errorf("a failed call charged %d micros", r.total())
	}
	if err := r.reserve(100); err != nil {
		t.Errorf("the ceiling is still held against a call that never happened: %v", err)
	}
}

// The bound covers the whole prompt, not the pieces it is built from.
//
// Red first: the estimator counted title, goal, mission, role and skills, and
// none of the fixed text around them. @measured on a real task, 2026-08-24: it
// bounded the prompt at 225 tokens while the prompt was 559 bytes.
//
// It held in practice, because a real tokeniser gives about a quarter of a
// token per byte, and every live call this session came in under it. That is
// not the point. The comment above tokens() claims one token per byte, "which
// no tokeniser can exceed", and that claim was false for two thirds of the
// string. A bound whose guarantee is narrower than its sentence is how the
// worst case was exceeded once already.
func TestThePromptBoundCoversTheWholePrompt(t *testing.T) {
	task := crew.Task{ID: 1,
		Title: "Explain the Amazon EC2 move on 2026-07-14",
		Goal: "2054.10 above of baseline on the aws desk. Say what happened, " +
			"whether it recurs, and what it would take to stop it."}
	a := crew.Analyst{Name: "triage-aws", Role: "Triage analyst", Desk: "aws",
		Engine:  "openrouter",
		State:   "active",
		Mission: "First look at every finding on the aws desk.",
		Skills:  []string{"triage", "aws"}}

	e := price(task, a, 1200)
	if e.Refused {
		t.Fatalf("the fixture was refused before it was priced: %s", e.Verdict)
	}

	sent := prompt(task, a, "2026-08-24")
	if e.PromptTokens < len(sent) {
		t.Errorf("the bound is %d tokens and the prompt is %d bytes, short by %d: "+
			"one token per byte is the only bound no tokeniser can exceed, and it "+
			"only holds over the string that is actually sent",
			e.PromptTokens, len(sent), len(sent)-e.PromptTokens)
	}
	// And it must not move because the clock did.
	e2 := price(task, a, 1200)
	if e.PromptTokens != e2.PromptTokens {
		t.Errorf("two estimates of one task differ, %d and %d", e.PromptTokens, e2.PromptTokens)
	}
}

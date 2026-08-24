package main

import (
	"os"
	"strings"
	"testing"

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

package main

// C2-SPEC.md section 4's third named mutant: "apply the rule before the
// stamp". internal/crew cannot import internal/finops at all -- apply.go
// already imports crew, so the reverse would cycle -- so
// crew.ValidateAndSaveOptions (the save-time gate) has no way to call
// finops.SetRule even by a wiring mistake; that much is a property of the
// import graph, checked by `go build` itself, not by a test. The one place
// a mistake of this SHAPE could actually compile is here: saveDraft, which
// already calls ValidateAndSaveOptions and already imports internal/finops
// (supervise.go, tools.go, this package). This is that test, one level up
// from internal/crew, at the place the mutant is actually reachable.

import (
	"fmt"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

// TestSavingAnAllocationRuleOptionNeverAppliesItBeforeAnyStamp: saving a
// deliverable that names a well-formed allocation.rule option stores the
// option (open, unrefused) and changes NOTHING in internal/finops. Only the
// owner's later stamp (internal/web/decisions.go's optionAction, the one
// production caller of finops.Apply) may call SetRule.
func TestSavingAnAllocationRuleOptionNeverAppliesItBeforeAnyStamp(t *testing.T) {
	db, tasks, _ := runnerTasks(t, 1)
	if err := finops.SeedRules(db); err != nil {
		t.Fatal(err)
	}
	rules, err := finops.Rules(db)
	if err != nil || len(rules) == 0 {
		t.Fatal(err)
	}
	var purchase finops.Rule
	for _, r := range rules {
		if r.Category == "Purchase" {
			purchase = r
		}
	}
	if purchase.ID == 0 {
		t.Fatal("no Purchase rule in the seeded defaults")
	}
	before := purchase.Method
	if before == finops.Even {
		t.Fatal("the fixture already uses even-split; this test needs a different starting method")
	}

	analyst := crew.Analyst{Name: "chargeback", Role: "Chargeback analyst", Desk: "management", State: "active"}
	body := "## The close pack\n\nSplit Purchase evenly instead of by usage.\n\n```options\n" +
		fmt.Sprintf(`{"options": [{"class": "allocation.rule", "summary": "split Purchase evenly instead of by usage",`+
			` "figure_cents": 0, "saving_cents": 0, "risk": "low", "needs": "the owner",`+
			` "target": {"rule_id": %d, "method": "even-split", "share": 1.0}}]}`, purchase.ID) +
		"\n```\n"

	if err := saveDraft(db, estimate{Task: tasks[0], Analyst: analyst},
		callResult{Text: body}, bus{}); err != nil {
		t.Fatal(err)
	}

	as, err := crew.Artifacts(db, tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 1 || as[0].State != crew.Draft {
		t.Fatalf("saveDraft did not leave one draft deliverable: %+v", as)
	}
	opts, err := crew.Options(db, as[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 1 || opts[0].Class != "allocation.rule" || opts[0].State != crew.OptionOpen {
		t.Fatalf("got %d option(s) %+v, want one open allocation.rule option", len(opts), opts)
	}

	after, err := finops.Rules(db)
	if err != nil {
		t.Fatal(err)
	}
	var gotAfter finops.Rule
	for _, r := range after {
		if r.ID == purchase.ID {
			gotAfter = r
		}
	}
	if gotAfter.Method != before {
		t.Errorf("rule %d's method is %q after saveDraft returned, want %q unchanged: "+
			"saving a deliverable must never apply allocation.rule before the owner's stamp",
			purchase.ID, gotAfter.Method, before)
	}
}

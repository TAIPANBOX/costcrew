package finops_test

// UnallocatedPots is C2-SPEC.md section 2's own requirement: the close pack
// owes "unallocated with the rule ids that produced it", and Allocate's own
// Unallocated field is a single summed number with no rule attached. These
// tests are red against unchanged code because the function does not exist
// yet.

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// The property TestAllocationAddsUpToTheBill already holds for Allocate's
// own Unallocated total; this is the same property for UnallocatedPots,
// individually, which is what proves the refactor that lets both walk the
// same rows did not let them drift apart.
func TestUnallocatedPotsSumToTheAllocationsOwnUnallocatedTotal(t *testing.T) {
	db := seeded(t)
	m := aMonth(t, db)
	a, err := finops.Allocate(db, m)
	if err != nil {
		t.Fatal(err)
	}
	pots, err := finops.UnallocatedPots(db, m)
	if err != nil {
		t.Fatal(err)
	}
	var sum money.Cents
	for _, p := range pots {
		sum += p.Amount
	}
	if sum != a.Unallocated {
		t.Fatalf("UnallocatedPots sums to %s, Allocate's own Unallocated is %s: the two disagree",
			sum, a.Unallocated)
	}
}

// Every rule set to Unallocated is the simplest case: every pot's RuleID
// must name the actual wildcard rule that put it there, in the SAME
// category, with a human reason.
func TestUnallocatedPotsNameTheRuleThatLeftEachOne(t *testing.T) {
	db := seeded(t)
	m := aMonth(t, db)
	rules, err := finops.Rules(db)
	if err != nil || len(rules) == 0 {
		t.Fatal(err)
	}
	for _, r := range rules {
		if err := finops.SetRule(db, r.ID, finops.Unallocated); err != nil {
			t.Fatal(err)
		}
	}
	pots, err := finops.UnallocatedPots(db, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(pots) == 0 {
		t.Fatal("every rule was set to unallocated, and UnallocatedPots found no pots")
	}
	ruleByID := map[int]finops.Rule{}
	for _, r := range rules {
		ruleByID[r.ID] = r
	}
	for _, p := range pots {
		if p.RuleID == 0 {
			t.Errorf("pot %s/%s has no rule id, but every category has a wildcard rule",
				p.Source, p.Category)
			continue
		}
		r, ok := ruleByID[p.RuleID]
		if !ok {
			t.Errorf("pot %s/%s names rule id %d, which does not exist",
				p.Source, p.Category, p.RuleID)
			continue
		}
		if r.Category != p.Category {
			t.Errorf("pot %s/%s names rule %d, whose category is %q",
				p.Source, p.Category, p.RuleID, r.Category)
		}
		if p.Reason == "" {
			t.Errorf("pot %s/%s has no reason", p.Source, p.Category)
		}
	}
}

// A category no rule names at all -- neither a source-specific one nor a
// wildcard -- is a real path Allocate's own "best := Unallocated" default
// already holds; UnallocatedPots must say so honestly (RuleID 0) rather
// than inventing a rule that was never consulted.
func TestUnallocatedPotsNameNoRuleWhenNoneCoversTheCategory(t *testing.T) {
	db := seeded(t)
	m := aMonth(t, db)
	mustExecArgs(t, db, `INSERT INTO charges (source, day, service, category, billed_cents)
		VALUES ('aws', ?, 'Support', 'Support', 12345)`, m+"-15")

	pots, err := finops.UnallocatedPots(db, m)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range pots {
		if p.Source == "aws" && p.Category == "Support" {
			found = true
			if p.RuleID != 0 {
				t.Errorf("Support has no rule, specific or wildcard, but RuleID is %d", p.RuleID)
			}
			if p.Reason == "" {
				t.Error("no reason given for a pot with no rule at all")
			}
		}
	}
	if !found {
		t.Fatal("the planted Support charge did not appear as an unallocated pot")
	}
}

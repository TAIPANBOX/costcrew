package finops_test

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

// TestSeedRulesWritesTheDefaultsOnceAndNeverAgain: a second seed on a store
// that already has rules adds none, so an operator's own rules are not
// duplicated or overwritten by starting the console again.
func TestSeedRulesWritesTheDefaultsOnceAndNeverAgain(t *testing.T) {
	db := covSchemaDB(t)
	if err := finops.SeedRules(db); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	first, err := finops.Rules(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 {
		t.Fatalf("first seed wrote %d rules, want the three defaults", len(first))
	}
	if err := finops.SeedRules(db); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	second, err := finops.Rules(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != len(first) {
		t.Errorf("a second seed changed the rule count from %d to %d", len(first), len(second))
	}
}

// A seed that cannot write a default says so, and the store is not left with
// some of the defaults and not others as though the seed had completed.
func TestSeedRulesRefusesWhenTheStoreRefusesADefault(t *testing.T) {
	db := covSchemaDB(t)
	if err := finops.SeedRules(db); err != nil {
		t.Fatal(err)
	}
	covExec(t, db, `DELETE FROM allocation_rules`)
	covExec(t, db, `CREATE TRIGGER refuse_rule BEFORE INSERT ON allocation_rules
		BEGIN SELECT RAISE(ABORT, 'store refused the rule'); END`)
	err := finops.SeedRules(db)
	if err == nil || !strings.Contains(err.Error(), "store refused the rule") {
		t.Fatalf("a refused default returned %v; want the store's own refusal", err)
	}
}

// The seed's own count cannot be read: the rule table is shadowed by a view
// over a table that is gone, so the read fails and the seed says so.
func TestSeedRulesSurfacesARuleTableThatCannotBeCounted(t *testing.T) {
	db := covSchemaDB(t)
	covExec(t, db, `CREATE TABLE rules_src(id INTEGER)`)
	covExec(t, db, `CREATE VIEW allocation_rules AS SELECT * FROM rules_src`)
	covExec(t, db, `DROP TABLE rules_src`)
	if err := finops.SeedRules(db); err == nil {
		t.Fatal("seeding over an unreadable rule table returned no error")
	}
}

func TestRulesSurfacesARuleTableThatCannotBeRead(t *testing.T) {
	db := covSchemaDB(t)
	covExec(t, db, `CREATE TABLE rules_src(id INTEGER)`)
	covExec(t, db, `CREATE VIEW allocation_rules AS SELECT * FROM rules_src`)
	covExec(t, db, `DROP TABLE rules_src`)
	if _, err := finops.Rules(db); err == nil {
		t.Fatal("reading an unreadable rule table returned no error")
	}
}

// A rule written for one desk takes that desk's own pots ahead of the
// wildcard rule. Here a desk-specific even split beats the proportional
// default for Purchase, so the pot is split equally between the two teams.
func TestADeskRuleOverridesTheWildcardRule(t *testing.T) {
	db := covSchemaDB(t)
	if err := finops.SeedRules(db); err != nil {
		t.Fatal(err)
	}
	covExec(t, db, `INSERT INTO allocation_rules(source, category, method, note)
		VALUES ('aws','Purchase','even-split','desk-specific')`)
	covExec(t, db, `INSERT INTO charges (source, day, service, team, category, billed_cents) VALUES
		('aws','2026-03-02','compute','team-b','Usage',1000),
		('aws','2026-03-03','compute','team-c','Usage',9000),
		('aws','2026-03-04','compute',NULL,'Purchase',600)`)
	a, err := finops.Allocate(db, "2026-03")
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	// The 600 pot goes 300 to each of the two teams that spent directly, because
	// the even-split rule for this desk wins over the proportional default. A
	// proportional split would have given the 9000-cent team 9/10 of it.
	shares := map[string]int64{}
	for _, tc := range a.Teams {
		if tc.Source == "aws" {
			shares[tc.Team] = int64(tc.Allocated)
		}
	}
	if shares["team-c"] != 300 || shares["team-b"] != 300 {
		t.Errorf("even-split desk rule gave team-c %d and team-b %d; want 300 each", shares["team-c"], shares["team-b"])
	}
}

// A pot on a desk with no team charges to carry it is left unallocated and
// named as such, not spread onto teams that never touched that desk.
func TestAPotWithNoTeamOnItsDeskStaysUnallocated(t *testing.T) {
	db := covSchemaDB(t)
	if err := finops.SeedRules(db); err != nil {
		t.Fatal(err)
	}
	covExec(t, db, `INSERT INTO charges (source, day, service, team, category, billed_cents)
		VALUES ('gcp','2026-03-04','network',NULL,'Tax',700)`)
	a, err := finops.Allocate(db, "2026-03")
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if a.Unallocated != 700 {
		t.Errorf("unallocated %d; want the 700 cents of the orphaned pot", a.Unallocated)
	}
	pots, err := finops.UnallocatedPots(db, "2026-03")
	if err != nil {
		t.Fatalf("unallocated pots: %v", err)
	}
	if len(pots) != 1 {
		t.Fatalf("got %d unallocated pots, want the one orphaned pot", len(pots))
	}
	if !strings.Contains(pots[0].Reason, "nothing on this desk to carry it") {
		t.Errorf("reason %q does not say the desk has nothing to carry the pot", pots[0].Reason)
	}
	if pots[0].RuleID == 0 {
		t.Error("the orphaned pot names no rule, so a reader cannot tell which rule produced it")
	}
}

// A desk whose teams net to zero has nothing to split by. A proportional
// rule cannot divide a pot over a zero basis, so the pot is named unallocated
// with the reason, rather than divided by zero.
func TestAProportionalPotOnAZeroNettingDeskIsNamedUnallocated(t *testing.T) {
	db := covSchemaDB(t)
	if err := finops.SeedRules(db); err != nil {
		t.Fatal(err)
	}
	covExec(t, db, `INSERT INTO charges (source, day, service, team, category, billed_cents)
		VALUES ('gcp','2026-03-02','network','team-x','Usage',500),
		       ('gcp','2026-03-03','network','team-x','Usage',-500)`)
	covExec(t, db, `INSERT INTO charges (source, day, service, team, category, billed_cents)
		VALUES ('gcp','2026-03-04','network',NULL,'Credit',-200)`)
	pots, err := finops.UnallocatedPots(db, "2026-03")
	if err != nil {
		t.Fatalf("unallocated pots: %v", err)
	}
	if len(pots) != 1 || !strings.Contains(pots[0].Reason, "nets to zero") {
		t.Fatalf("pots %v; want the one credit named as unsplittable because the desk nets to zero", pots)
	}
	a, err := finops.Allocate(db, "2026-03")
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if a.Unallocated != -200 {
		t.Errorf("unallocated %d; want the -200 credit left where it is", a.Unallocated)
	}
}

// A rule that splits by usage, on a desk where a pot exists but the rule's own
// method has no team to carry it, is named in the unallocated list with the
// method the rule names.
func TestUnallocatedPotsNameTheMethodOfAnOrphanedRule(t *testing.T) {
	db := covSchemaDB(t)
	if err := finops.SeedRules(db); err != nil {
		t.Fatal(err)
	}
	covExec(t, db, `INSERT INTO charges (source, day, service, team, category, billed_cents)
		VALUES ('azure','2026-03-04','vm',NULL,'Purchase',900)`)
	pots, err := finops.UnallocatedPots(db, "2026-03")
	if err != nil {
		t.Fatal(err)
	}
	if len(pots) != 1 {
		t.Fatalf("got %d pots, want the one orphaned purchase", len(pots))
	}
	if !strings.Contains(pots[0].Reason, "split by each team's direct usage") {
		t.Errorf("reason %q does not name the proportional method its rule uses", pots[0].Reason)
	}
}

// The three reads behind the allocation each refuse a stored amount that is not
// a whole number of cents, rather than rounding it into a team's chargeback.
func TestAllocationRefusesAFractionalTeamAmount(t *testing.T) {
	db := covSchemaDB(t)
	if err := finops.SeedRules(db); err != nil {
		t.Fatal(err)
	}
	covDayCharges(t, db, "aws", "compute", 1000, "2026-03-02")
	covExec(t, db, `UPDATE charges SET billed_cents = 0.5 WHERE day = '2026-03-02'`)
	if _, err := finops.Allocate(db, "2026-03"); err == nil {
		t.Fatal("a fractional team amount was allocated instead of refused")
	}
	if _, err := finops.UnallocatedPots(db, "2026-03"); err == nil {
		t.Fatal("a fractional team amount was listed as unallocated instead of refused")
	}
}

func TestAllocationRefusesAFractionalSharedAmount(t *testing.T) {
	db := covSchemaDB(t)
	if err := finops.SeedRules(db); err != nil {
		t.Fatal(err)
	}
	covExec(t, db, `INSERT INTO charges (source, day, service, team, category, billed_cents)
		VALUES ('aws','2026-03-04','compute',NULL,'Purchase',0.5)`)
	if _, err := finops.Allocate(db, "2026-03"); err == nil {
		t.Fatal("a fractional shared amount was allocated instead of refused")
	}
	if _, err := finops.UnallocatedPots(db, "2026-03"); err == nil {
		t.Fatal("a fractional shared amount was listed instead of refused")
	}
}

// Allocate and UnallocatedPots each read the charges and the rules; a store
// whose charges cannot be read says so instead of reporting an allocation
// with the spend missing.
func TestAllocationSurfacesAFailingChargesRead(t *testing.T) {
	db := covSchemaDB(t)
	if err := finops.SeedRules(db); err != nil {
		t.Fatal(err)
	}
	covDayCharges(t, db, "aws", "compute", 1000, "2026-03-02")
	covExec(t, db, `DROP TABLE charges`)
	if _, err := finops.Allocate(db, "2026-03"); err == nil {
		t.Fatal("allocation with the charges gone returned no error")
	}
	if _, err := finops.UnallocatedPots(db, "2026-03"); err == nil {
		t.Fatal("unallocated pots with the charges gone returned no error")
	}
}

// BudgetsFor reads each team's budget for the month beside its own usage
// spend. Only usage counts toward the actual, and a team with budget and no
// spend is still listed, at zero.
func TestBudgetsForSetsEachTeamsUsageAgainstItsBudget(t *testing.T) {
	db := covSchemaDB(t)
	covExec(t, db, estate.BudgetSchema)
	covExec(t, db, `INSERT INTO budgets(source, team, month, budget_cents) VALUES
		('aws','team-a','2026-03',2000), ('aws','team-b','2026-03',500)`)
	covExec(t, db, `INSERT INTO charges (source, day, service, team, category, billed_cents) VALUES
		('aws','2026-03-01','compute','team-a','Usage',1500),
		('aws','2026-03-02','compute','team-a','Purchase',999)`)
	rows, err := finops.BudgetsFor(db, "aws", "2026-03")
	if err != nil {
		t.Fatalf("budgets: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d budget rows, want the two teams with a budget", len(rows))
	}
	a, b := rows[0], rows[1]
	if a.Team != "team-a" || a.Budget != 2000 || a.Actual != 1500 || a.Variance != -500 {
		t.Errorf("team-a row %+v; want budget 2000, usage actual 1500 (the purchase is not usage), variance -500", a)
	}
	if b.Team != "team-b" || b.Budget != 500 || b.Actual != 0 || b.Variance != -500 {
		t.Errorf("team-b row %+v; want budget 500, actual 0, variance -500", b)
	}
}

func TestBudgetsForSurfacesAFailingBudgetRead(t *testing.T) {
	db := covSchemaDB(t)
	covExec(t, db, estate.BudgetSchema)
	covExec(t, db, `DROP TABLE budgets`)
	if _, err := finops.BudgetsFor(db, "aws", "2026-03"); err == nil {
		t.Fatal("budgets with the budget table gone returned no error")
	}
}

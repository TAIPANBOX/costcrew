package crew_test

// B4-STEP-TWO-SPEC.md section 4: "settles the actual cost into the
// supervisor's own spend (the same ledger a task's call settles into, so
// SpendInMonth sees it)". crew.SettlePlanAsk does not exist yet, so this
// file does not compile against main.
//
// The plan-ask call has no task or sprint row of its own: it is made BEFORE
// the sprint it plans is ever approved (crew.Approve refuses a second time
// on a label already on the board), so the tasks-JOIN-sprints query
// SpendInMonth has always used can never see it. crew.SettlePlanAsk is a
// small, dedicated ledger keyed by calendar month instead, and SpendInMonth
// adds it in -- additively, never replacing the existing tasks/sprints sum,
// which TestSpendInMonthStillSumsOrdinaryTaskSpend below holds.

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

func TestSettlePlanAskLandsInSpendInMonthForSupervisor(t *testing.T) {
	db := planDB(t)

	cents, err := crew.SettlePlanAsk(db, "2026-W37", "2026-09", "supervisor",
		2_342, crew.PlanAskAccepted, "")
	if err != nil {
		t.Fatal(err)
	}
	// (2342 + 9999) / 10000 = 1 cent, rounded UP, never down: the same "a
	// call that cost money must not record less than it cost" rule
	// SettleLiveSpend already holds.
	if cents != money.Cents(1) {
		t.Errorf("SettlePlanAsk returned %s, want 0.01", cents)
	}

	spent, err := crew.SpendInMonth(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if spent["supervisor"] != money.Cents(1) {
		t.Errorf("SpendInMonth(2026-09)[supervisor] = %s, want 0.01", spent["supervisor"])
	}
}

// A refused ask -- pre-call or post-call -- still costs whatever was
// actually spent (0 for a pre-call refusal, real micros for a call that was
// made but whose answer failed validation), and it lands the same way.
func TestSettlePlanAskRefusedStillSettlesWhateverItCost(t *testing.T) {
	db := planDB(t)
	if _, err := crew.SettlePlanAsk(db, "2026-W37", "2026-09", "supervisor",
		5_000, crew.PlanAskRefused, "the model invented an item with no ref"); err != nil {
		t.Fatal(err)
	}
	spent, err := crew.SpendInMonth(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if spent["supervisor"] != money.Cents(1) {
		t.Errorf("SpendInMonth(2026-09)[supervisor] = %s, want 0.01 (5000 micros rounded up)", spent["supervisor"])
	}
}

// A pre-call refusal (no gateway, or the worst case exceeded PerTask) costs
// nothing and settles nothing measurable.
func TestSettlePlanAskWithZeroMicrosAddsNothingMeasurable(t *testing.T) {
	db := planDB(t)
	cents, err := crew.SettlePlanAsk(db, "2026-W37", "2026-09", "supervisor",
		0, crew.PlanAskRefused, "no gateway is configured")
	if err != nil {
		t.Fatal(err)
	}
	if cents != money.Cents(0) {
		t.Errorf("SettlePlanAsk with 0 micros = %s, want 0.00", cents)
	}
}

// SpendInMonth still sums ordinary task spend exactly as before: the
// plan-ask ledger is ADDED, not substituted.
func TestSpendInMonthStillSumsOrdinaryTaskSpend(t *testing.T) {
	db := planDB(t)
	spend(t, db, "investigator-aws", "2026-09", 4_200)

	spent, err := crew.SpendInMonth(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if spent["investigator-aws"] != money.Cents(4_200) {
		t.Errorf("spend[investigator-aws] = %s, want 42.00", spent["investigator-aws"])
	}

	if _, err := crew.SettlePlanAsk(db, "2026-W37", "2026-09", "supervisor",
		10_000, crew.PlanAskAccepted, ""); err != nil {
		t.Fatal(err)
	}
	spent2, err := crew.SpendInMonth(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if spent2["investigator-aws"] != money.Cents(4_200) {
		t.Errorf("task spend moved after a plan-ask settled: %s, want unchanged at 42.00", spent2["investigator-aws"])
	}
	if spent2["supervisor"] != money.Cents(1) {
		t.Errorf("spend[supervisor] = %s, want 0.01", spent2["supervisor"])
	}
}

// Two plan-asks in the same month for the same analyst accumulate rather
// than overwrite.
func TestTwoPlanAsksInOneMonthAccumulate(t *testing.T) {
	db := planDB(t)
	if _, err := crew.SettlePlanAsk(db, "2026-W37", "2026-09", "supervisor",
		10_000, crew.PlanAskAccepted, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := crew.SettlePlanAsk(db, "2026-W38", "2026-09", "supervisor",
		10_000, crew.PlanAskAccepted, ""); err != nil {
		t.Fatal(err)
	}
	spent, err := crew.SpendInMonth(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if spent["supervisor"] != money.Cents(2) {
		t.Errorf("spend[supervisor] after two asks = %s, want 0.02", spent["supervisor"])
	}
}

// A plan-ask settled in a DIFFERENT month than the one being queried does
// not show up: the same "the month comes from when the work sat" rule
// SpendInMonth's own doc comment already states for tasks.
func TestAPlanAskInADifferentMonthDoesNotLeak(t *testing.T) {
	db := planDB(t)
	if _, err := crew.SettlePlanAsk(db, "2026-W35", "2026-08", "supervisor",
		10_000, crew.PlanAskAccepted, ""); err != nil {
		t.Fatal(err)
	}
	spent, err := crew.SpendInMonth(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := spent["supervisor"]; ok && spent["supervisor"] != 0 {
		t.Errorf("spend[supervisor] for 2026-09 = %s, want none: the ask settled in 2026-08", spent["supervisor"])
	}
}

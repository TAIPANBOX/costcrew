package estate_test

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

func plan(t *testing.T, csv string, now map[estate.BudgetKey]money.Cents,
	closed map[string]bool) estate.BudgetPlan {
	t.Helper()
	p, err := estate.ReadBudgets(strings.NewReader(csv), now, closed)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	return p
}

// Every problem in the file, not the first one.
//
// A person who fixes one typo, re-uploads, and finds the next is a person who
// stops using this. The file is four columns and small enough to check whole.
func TestABadFileReportsEveryProblemAtOnce(t *testing.T) {
	p := plan(t, `platform,team,month,budget_usd
aws,sre-platform,2026-09,960
aws,ml-platform,2026-09,not-a-number
gcp,research,2026-9,500
aws,sre-platform,2026-09,111
,growth,2026-09,10
aws,growth,2026-09,0
`, nil, nil)

	if len(p.Problems) != 5 {
		t.Errorf("reported %d problems, wanted 5: %v", len(p.Problems), p.Problems)
	}
	for _, want := range []string{
		"not an amount", "is not a month", "repeats", "are all required", "is not a budget",
	} {
		found := false
		for _, got := range p.Problems {
			if strings.Contains(got, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("nothing said %q: %v", want, p.Problems)
		}
	}
	// The one good row still makes it through: a file is not all-or-nothing
	// at the parsing stage, it is all-or-nothing at the WRITING stage.
	if len(p.Rows) != 1 {
		t.Errorf("%d rows planned, wanted the one good one", len(p.Rows))
	}
}

// The two directions that matter are called out separately.
func TestLoweredAndAlreadyChargedAreBothMarked(t *testing.T) {
	now := map[estate.BudgetKey]money.Cents{
		{"aws", "data-eng", "2026-07"}: money.Cents(1_000_00),
		{"aws", "growth", "2026-09"}:   money.Cents(100_00),
	}
	closed := map[string]bool{"2026-07": true}
	p := plan(t, `platform,team,month,budget_usd
aws,data-eng,2026-07,10.00
aws,growth,2026-09,500.00
aws,new-team,2026-09,50.00
`, now, closed)

	if p.Lowered != 1 {
		t.Errorf("%d lowered, wanted 1", p.Lowered)
	}
	if p.InClosed != 1 {
		t.Errorf("%d in a charged month, wanted 1", p.InClosed)
	}
	if p.Added != 1 {
		t.Errorf("%d new, wanted 1", p.Added)
	}
	for _, r := range p.Rows {
		if r.Team == "data-eng" && !(r.Lower && r.Closed) {
			t.Errorf("the row that goes down inside a charged month is marked "+
				"lower=%t closed=%t", r.Lower, r.Closed)
		}
	}
}

// A row that changes nothing is counted and not written.
func TestARowThatChangesNothingIsNotAChange(t *testing.T) {
	now := map[estate.BudgetKey]money.Cents{{"aws", "growth", "2026-09"}: money.Cents(500_00)}
	p := plan(t, "platform,team,month,budget_usd\naws,growth,2026-09,500.00\n", now, nil)
	if p.Unchanged != 1 || len(p.Rows) != 0 {
		t.Errorf("unchanged=%d rows=%d; a row that matches is not a write",
			p.Unchanged, len(p.Rows))
	}
}

// Columns are read by NAME.
//
// By position, a file whose author moved a column writes every budget into the
// wrong team, silently, and the page shows a plan that looks plausible.
func TestColumnsAreReadByNameNotPosition(t *testing.T) {
	p := plan(t, "budget_usd,month,team,platform\n960,2026-09,sre-platform,aws\n", nil, nil)
	if len(p.Rows) != 1 {
		t.Fatalf("%d rows, wanted 1: %v", len(p.Rows), p.Problems)
	}
	r := p.Rows[0]
	if r.Source != "aws" || r.Team != "sre-platform" || r.Month != "2026-09" ||
		r.Budget != money.Cents(960_00) {
		t.Errorf("read as %+v", r)
	}
	// And a file missing a column says which, rather than reading rubbish.
	if _, err := estate.ReadBudgets(strings.NewReader("team,month,budget_usd\na,2026-09,1\n"),
		nil, nil); err == nil || !strings.Contains(err.Error(), "platform") {
		t.Errorf("a file with no platform column was accepted: %v", err)
	}
}

// The fingerprint changes when the plan does.
func TestTheFingerprintFollowsThePlan(t *testing.T) {
	a := plan(t, "platform,team,month,budget_usd\naws,growth,2026-09,500\n", nil, nil)
	b := plan(t, "platform,team,month,budget_usd\naws,growth,2026-09,501\n", nil, nil)
	if a.Fingerprint() == b.Fingerprint() {
		t.Error("two different plans share a fingerprint, so the apply cannot tell them apart")
	}
	// And it follows what is ALREADY there, not only what is being written:
	// the same file against a different starting point is a different change.
	c := plan(t, "platform,team,month,budget_usd\naws,growth,2026-09,500\n",
		map[estate.BudgetKey]money.Cents{{"aws", "growth", "2026-09"}: money.Cents(9_00)}, nil)
	if a.Fingerprint() == c.Fingerprint() {
		t.Error("the same file over a different starting point has the same fingerprint")
	}
}

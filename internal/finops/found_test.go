package finops_test

import (
	"database/sql"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// Money found is money that was being spent and is not any more.
//
// The sum was over ABS(excess_cents), so a finding whose spend went DOWN
// against its baseline counted as money the crew found. One such finding in
// the seeded estate, Microsoft Sentinel at -152.79, turned 1254.35 into
// 1559.93 and carried into two more headline figures: "found, annualised",
// which is that number times twelve, and the return ratio the page uses to
// say whether the crew paid for itself.
//
// The bias ran in the flattering direction, which is the one nobody checks.
func TestADropInSpendIsNotMoneyFound(t *testing.T) {
	db := seeded(t)
	for _, sch := range []string{anomaly.Schema, crew.Schema} {
		if _, err := db.Exec(sch); err != nil {
			t.Fatal(err)
		}
	}
	// A decided finding whose spend fell. The fixture has one; this writes its
	// own so the test says what it is about rather than depending on which
	// direction the generator happened to give a row.
	ins := `INSERT INTO anomalies
		(id, source, team, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule_version, state, detected_at)
		VALUES (?,?,?,?,?,?,?,?,?,?, 'v1', 'accepted', '2026-07-06')`
	mustExecArgs(t, db, ins, "A-down", "azure", "sre-platform", "Microsoft Sentinel",
		"2026-07-04", "down", 34721, 50000, -15279, 3.1)
	mustExecArgs(t, db, ins, "A-up", "gcp", "ml-platform", "BigQuery",
		"2026-07-05", "up", 90000, 50000, 40000, 4.2)

	var signed, abs int64
	if err := db.QueryRow(`
		SELECT COALESCE(SUM(excess_cents),0), COALESCE(SUM(ABS(excess_cents)),0)
		  FROM anomalies WHERE state IN ('explained','accepted')`).
		Scan(&signed, &abs); err != nil {
		t.Fatal(err)
	}
	if signed == abs {
		t.Fatal("the two sums are equal, so this test cannot see the fault")
	}

	r, err := finops.Compute(db, aMonth(t, db))
	if err != nil {
		t.Fatal(err)
	}
	if int64(r.FoundMonthly) == abs {
		t.Errorf("found money is %s, the ABSOLUTE sum: a finding whose spend "+
			"went DOWN is counted as money the crew found. What was actually "+
			"decided on is %s", money.Cents(abs), money.Cents(signed))
	}
	if int64(r.FoundMonthly) != signed {
		t.Errorf("found money is %s and the decided excess is %s",
			money.Cents(r.FoundMonthly), money.Cents(signed))
	}
	// Everything derived moves with it, or the page contradicts its own
	// headline one tile later.
	if r.FoundAnnual != r.FoundMonthly*12 {
		t.Errorf("annualised is %s, not twelve times %s", r.FoundAnnual, r.FoundMonthly)
	}
}

func mustExecArgs(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

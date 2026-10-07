package finops_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// "What the crew cost" is judged against what it found, and what it found is
// signed: TestADropInSpendIsNotMoneyFound fixed the Results page, and the
// crew-cost KPI kept summing ABS(excess_cents) beside it. A finding whose
// spend went down counted as money found, in the one KPI whose target is
// "less than it finds", so a crew could meet it by detecting a stopped
// workload.
//
// The fixture is chosen so the two definitions give different VERDICTS, not
// only different numbers: an up finding a little under the crew's whole
// cost, and a drop that tips the absolute sum over it.
func TestTheCrewCostKPIDoesNotCountADropAsMoneyFound(t *testing.T) {
	db := kpiDB(t)
	var spent int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(spent_cents),0) FROM tasks`).Scan(&spent); err != nil {
		t.Fatal(err)
	}
	if spent < 100000 {
		t.Fatalf("the fixture's crew cost %d cents; this test needs a real figure to straddle", spent)
	}
	up := spent - 5000    // found: a little under what the crew cost
	down := int64(-10000) // a drop: worth detecting, not money anybody found
	ins := `INSERT INTO anomalies
		(id, source, team, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule_version, state, detected_at)
		VALUES (?,?,?,?,?,?,?,?,?,?, 'v1', 'accepted', '2026-07-06')`
	mustExecArgs(t, db, ins, "K-down", "azure", "sre-platform", "Microsoft Sentinel",
		"2026-07-04", "down", 40000, 50000, down, 3.1)
	mustExecArgs(t, db, ins, "K-up", "gcp", "ml-platform", "BigQuery",
		"2026-07-05", "up", 90000, 50000, up, 4.2)

	signed, abs := up+down, up-down
	if signed >= spent || abs < spent {
		t.Fatalf("the fixture does not straddle the crew's cost: signed %d, abs %d, cost %d",
			signed, abs, spent)
	}

	list, err := finops.KPIs(db, world.LastDay[:7])
	if err != nil {
		t.Fatal(err)
	}
	var k finops.KPI
	for _, c := range list {
		if c.ID == "crew-cost" {
			k = c
		}
	}
	if k.ID == "" {
		t.Fatal("no crew-cost KPI")
	}
	wantNote := fmt.Sprintf("against %s found", money.Cents(signed))
	if !strings.Contains(k.Note, wantNote) {
		t.Errorf("the crew-cost KPI does not state the signed figure %q: %q", wantNote, k.Note)
	}
	if strings.Contains(k.Note, fmt.Sprintf("against %s found", money.Cents(abs))) {
		t.Errorf("the crew-cost KPI states the absolute sum %s as money found: %q",
			money.Cents(abs), k.Note)
	}
	if want := fmt.Sprintf("a return of %.2fx", float64(signed)/float64(spent)); !strings.Contains(k.Note, want) {
		t.Errorf("the return in the note is not the signed one (%s): %q", want, k.Note)
	}
	if k.Meets {
		t.Errorf("the crew-cost KPI meets its target (less than it finds) on a crew that cost %s "+
			"and found %s: the drop was counted", money.Cents(spent), money.Cents(signed))
	}
}

// The Results page and the KPI state one figure, from one function.
func TestResultsAndTheCrewCostKPIAgreeOnMoneyFound(t *testing.T) {
	db := kpiDB(t)
	ins := `INSERT INTO anomalies
		(id, source, team, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule_version, state, detected_at)
		VALUES (?,?,?,?,?,?,?,?,?,?, 'v1', 'explained', '2026-07-06')`
	mustExecArgs(t, db, ins, "R-down", "azure", "sre-platform", "Microsoft Sentinel",
		"2026-07-04", "down", 34721, 50000, -15279, 3.1)
	mustExecArgs(t, db, ins, "R-up", "gcp", "ml-platform", "BigQuery",
		"2026-07-05", "up", 90000, 50000, 40000, 4.2)

	found, err := finops.FoundMonthly(db)
	if err != nil {
		t.Fatal(err)
	}
	res, err := finops.Compute(db, world.LastDay[:7])
	if err != nil {
		t.Fatal(err)
	}
	if found != res.FoundMonthly || found != 24721 {
		t.Fatalf("FoundMonthly %s, Results %s, want both 247.21 (40000 - 15279)", found, res.FoundMonthly)
	}
	list, err := finops.KPIs(db, world.LastDay[:7])
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range list {
		if k.ID == "crew-cost" && !strings.Contains(k.Note, "against "+found.String()+" found") {
			t.Errorf("the KPI says %q where Results says %s found", k.Note, found)
		}
	}
}

package finops_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// The KPI library says what of the crew's cost is real.
//
// Red first: "What the crew cost" read 3871.59 across 310 tasks with nothing
// saying that 0.24 of it was anybody's money, on the page whose own headline is
// that a library where everything reports a number is one where several are
// invented.
func TestTheCrewCostKPISaysWhatIsRealMoney(t *testing.T) {
	db := kpiDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET live_micros = 53_100 WHERE id IN
		 (SELECT id FROM tasks ORDER BY id LIMIT 2)`); err != nil {
		t.Fatal(err)
	}

	list, err := finops.KPIs(db, world.LastDay[:7])
	if err != nil {
		t.Fatal(err)
	}
	var note string
	for _, k := range list {
		if k.ID == "crew-cost" {
			note = k.Note
		}
	}
	if note == "" {
		t.Fatal("no crew-cost KPI")
	}
	// 106 200 micros over two tasks rounds up to 11 cents.
	if !strings.Contains(note, "0.11 of it is real money") {
		t.Errorf("the crew-cost KPI does not say what of its figure is real: %q", note)
	}
	if !strings.Contains(note, "2 tasks an agent actually wrote") {
		t.Errorf("it does not name how many tasks: %q", note)
	}
}

// And a console where nothing has been run says nothing about real money.
func TestTheKPISaysNothingAboutMoneyNobodySpent(t *testing.T) {
	db := kpiDB(t)
	list, err := finops.KPIs(db, world.LastDay[:7])
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range list {
		if strings.Contains(k.Note, "real money") {
			t.Errorf("%s claims real money was spent on an estate where no agent "+
				"has run: %q", k.ID, k.Note)
		}
	}
	if s := crew.RealMoney(0, 0); s != "" {
		t.Errorf("RealMoney(0,0) is %q, want empty", s)
	}
}

// The KPI library reports refusals on an estate with no tasks at all, not
// an error.
//
// Red first: SUM(CASE WHEN state='posted' ...) over zero rows is NULL
// regardless of the CASE inside it, and two of the three SUMs in the
// crew-cost query scanned straight into a plain int with no COALESCE
// around them (the third already had one). Found by tools/run's own
// coverage test for its new `kpis` tool, which is the first caller ever to
// run KPIs() against crew.Schema with nothing seeded into tasks: every
// existing caller either seeds the full roster (crew.Seed, kpiDB below) or
// never reaches an empty table at all.
func TestKPIsReportsRatherThanErrorsWithNoTasksAtAll(t *testing.T) {
	db := seeded(t)
	// anomaly.Schema too: KPIs() reads the anomalies table before it ever
	// reaches tasks, and this test is about the SECOND table being empty,
	// not the first being absent.
	for _, sch := range []string{anomaly.Schema, crew.Schema} {
		if _, err := db.Exec(sch); err != nil {
			t.Fatal(err)
		}
	}
	// crew.LiveSpend, which KPIs() also calls, reads tasks.live_micros,
	// which crew.Schema's CREATE TABLE does not carry: it is added by this
	// migration, exactly as tools/run's own test fixture needs it too.
	if err := crew.EnsureLiveSpendLedger(db); err != nil {
		t.Fatal(err)
	}
	// crew.Schema alone, no crew.Seed: the tasks table exists and holds
	// nothing.
	if _, err := finops.KPIs(db, world.LastDay[:7]); err != nil {
		t.Fatalf("KPIs() over an empty tasks table: %v", err)
	}
}

// kpiDB is a store with the crew plane, which seeded() alone does not have:
// Compute reads tasks, and a KPI over a table that is not there measures
// nothing while reporting a number.
func kpiDB(t *testing.T) *sql.DB {
	t.Helper()
	db := seeded(t)
	for _, sch := range []string{anomaly.Schema, crew.Schema} {
		if _, err := db.Exec(sch); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := crew.Seed(db, nil); err != nil {
		t.Fatal(err)
	}
	return db
}

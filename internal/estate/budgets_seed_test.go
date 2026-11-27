package estate_test

// SeedBudgets on a hand-built ledger, so every budget is a number this test
// can work out: last month's actual grown by the team's rate, the open month
// judged against the last CLOSED month, an unknown team at the default rate,
// and a second run that changes nothing.

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

func ledgerDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.DB().Exec(estate.SeedSchema); err != nil {
		t.Fatal(err)
	}
	return st.DB()
}

func usage(t *testing.T, db *sql.DB, team, day string, cents int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO charges(source, day, service, team, category, billed_cents)
		VALUES ('aws', ?, 'Amazon EC2', ?, 'Usage', ?)`, day, team, cents); err != nil {
		t.Fatal(err)
	}
}

func budgetOf(t *testing.T, db *sql.DB, team, month string) int64 {
	t.Helper()
	var c int64
	if err := db.QueryRow(`SELECT budget_cents FROM budgets WHERE source='aws' AND team=? AND month=?`,
		team, month).Scan(&c); err != nil {
		t.Fatalf("budget for %s %s: %v", team, month, err)
	}
	return c
}

func TestSeedBudgetsFromTheMonthBeforeAndTheLastClosedMonth(t *testing.T) {
	db := ledgerDB(t)
	usage(t, db, "ml-platform", "2026-06-10", 1_000_00)
	usage(t, db, "ml-platform", "2026-07-10", 2_000_00)
	usage(t, db, "ml-platform", "2026-08-03", 500_00) // the open month, three days in
	usage(t, db, "skunkworks", "2026-07-02", 1_000_00)
	// Not usage, and no team: neither is budgeted from.
	if _, err := db.Exec(`INSERT INTO charges(source, day, service, team, category, billed_cents)
		VALUES ('aws', '2026-07-01', 'Tax', 'ml-platform', 'Tax', 999999),
		       ('aws', '2026-07-01', 'Amazon S3', NULL, 'Usage', 999999)`); err != nil {
		t.Fatal(err)
	}

	if err := estate.SeedBudgets(db); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		team, month string
		want        int64
	}{
		{"ml-platform", "2026-06", 1_090_00}, // no month before: its own actual, at 109%
		{"ml-platform", "2026-07", 1_090_00}, // June's actual at 109%
		{"ml-platform", "2026-08", 2_180_00}, // the open month: July, the last closed, at 109%
		{"skunkworks", "2026-07", 1_050_00},  // a team with no rate of its own: 105%
	} {
		if got := budgetOf(t, db, c.team, c.month); got != c.want {
			t.Errorf("%s %s: budget %d, want %d", c.team, c.month, got, c.want)
		}
	}

	// A second run leaves every budget as it was, even with more spend in.
	usage(t, db, "ml-platform", "2026-07-20", 9_000_00)
	if err := estate.SeedBudgets(db); err != nil {
		t.Fatal(err)
	}
	if got := budgetOf(t, db, "ml-platform", "2026-08"); got != 2_180_00 {
		t.Errorf("a second SeedBudgets moved the open month's budget to %d", got)
	}
}

func TestSeedBudgetsRefusesALedgerWithNoUsage(t *testing.T) {
	db := ledgerDB(t)
	err := estate.SeedBudgets(db)
	if err == nil || !strings.Contains(err.Error(), "no usage rows") {
		t.Fatalf("an empty ledger: %v", err)
	}
}

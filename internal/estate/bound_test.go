package estate_test

// ANNOUNCED-DRIVER-SPEC.md section 11, finding (b): a saving larger than what the
// service, or the named team's share of it, costs on the desk is refused. The
// bound is the median daily spend over the 28 days before the start. A saving
// with no spend to measure against is refused. An increase is never bounded.
//
// Red against the stub SavingRefusal, which refuses nothing: every refusal
// case fails, and the three acceptance cases pass as guards.

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/estate"

	_ "modernc.org/sqlite"
)

// boundDB is an empty estate with the drivers columns.
func boundDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(estate.SeedSchema); err != nil {
		t.Fatal(err)
	}
	if err := estate.EnsureDriverColumns(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// boundSpend writes days of one team's spend on one service, a day at a time.
func boundSpend(t *testing.T, db *sql.DB, desk, team, service, first string, days int, cents int64) {
	t.Helper()
	start, err := time.Parse("2006-01-02", first)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < days; i++ {
		day := start.AddDate(0, 0, i).Format("2006-01-02")
		if _, err := db.Exec(`INSERT INTO charges
			(source, day, service, team, category, billed_cents) VALUES (?,?,?,?,'Usage',?)`,
			desk, day, service, team, cents); err != nil {
			t.Fatal(err)
		}
	}
}

// The 28 days before 1 August 2026 are 4 July to 31 July.
const boundStart = "2026-08-01"

func TestASavingOverTheMedianSpendIsRefused(t *testing.T) {
	db := boundDB(t)
	boundSpend(t, db, "aws", "data-eng", "Amazon EC2", "2026-07-04", 28, 260_00)
	reason, err := estate.SavingRefusal(db, "aws", "Amazon EC2", "", boundStart, -300_00)
	if err != nil {
		t.Fatal(err)
	}
	if reason == "" {
		t.Fatal("a saving of 300.00 a day on a service that costs 260.00 a day was accepted")
	}
	for _, want := range []string{"300.00", "260.00", "2026-07-04", "2026-07-31"} {
		if !strings.Contains(reason, want) {
			t.Errorf("the refusal %q does not name %s", reason, want)
		}
	}
}

func TestASavingAtTheMedianSpendIsAccepted(t *testing.T) {
	db := boundDB(t)
	boundSpend(t, db, "aws", "data-eng", "Amazon EC2", "2026-07-04", 28, 260_00)
	if reason, err := estate.SavingRefusal(db, "aws", "Amazon EC2", "", boundStart, -260_00); err != nil || reason != "" {
		t.Fatalf("a saving equal to the median was refused: %q, %v", reason, err)
	}
}

func TestASavingWithNoSpendToMeasureIsRefused(t *testing.T) {
	db := boundDB(t)
	boundSpend(t, db, "aws", "data-eng", "Amazon EC2", "2026-09-01", 28, 260_00) // after the start only
	reason, err := estate.SavingRefusal(db, "aws", "Amazon EC2", "", boundStart, -10_00)
	if err != nil {
		t.Fatal(err)
	}
	if reason == "" || !strings.Contains(reason, "no spend") {
		t.Fatalf("a saving with no spend before the start was not refused as having nothing to measure: %q", reason)
	}
}

func TestAnIncreaseIsNotBounded(t *testing.T) {
	db := boundDB(t)
	boundSpend(t, db, "ai", "ml-platform", "Anthropic API", "2026-07-04", 28, 100_00)
	if reason, err := estate.SavingRefusal(db, "ai", "Anthropic API", "", boundStart, 20*100_00*10); err != nil || reason != "" {
		t.Fatalf("a twenty-fold increase was refused: %q, %v", reason, err)
	}
	// An increase with no spend to measure against is not refused either.
	if reason, err := estate.SavingRefusal(db, "ai", "Unseen service", "", boundStart, 5_00); err != nil || reason != "" {
		t.Fatalf("an increase with no spend was refused: %q, %v", reason, err)
	}
}

// A named team is bounded by its own spend, not the service's: the service
// costs 400.00 a day across two teams, data-eng 100.00 of it, and a 150.00 saving
// named to data-eng is over its bound although it is under the service's.
func TestANamedTeamsSavingIsBoundedByThatTeamsSpend(t *testing.T) {
	db := boundDB(t)
	boundSpend(t, db, "aws", "data-eng", "Amazon EC2", "2026-07-04", 28, 100_00)
	boundSpend(t, db, "aws", "ml-platform", "Amazon EC2", "2026-07-04", 28, 300_00)
	if reason, err := estate.SavingRefusal(db, "aws", "Amazon EC2", "", boundStart, -150_00); err != nil || reason != "" {
		t.Fatalf("a 150.00 saving split over a 400.00 service was refused: %q, %v", reason, err)
	}
	reason, err := estate.SavingRefusal(db, "aws", "Amazon EC2", "data-eng", boundStart, -150_00)
	if err != nil {
		t.Fatal(err)
	}
	if reason == "" {
		t.Fatal("a 150.00 saving named to data-eng, which spends 100.00, was accepted")
	}
	if !strings.Contains(reason, "data-eng") {
		t.Errorf("the team's refusal %q does not name the team", reason)
	}
}

// The bound is the median, not the mean. Twenty-seven days at 100.00 and one
// day at 2800.00 give a median of 100.00 and a mean of 196.43, so a 150.00
// saving is over the median and under the mean: it must be refused.
func TestTheBoundIsTheMedianNotTheMean(t *testing.T) {
	db := boundDB(t)
	boundSpend(t, db, "aws", "data-eng", "Amazon EC2", "2026-07-04", 27, 100_00)
	boundSpend(t, db, "aws", "data-eng", "Amazon EC2", "2026-07-31", 1, 2800_00)
	reason, err := estate.SavingRefusal(db, "aws", "Amazon EC2", "", boundStart, -150_00)
	if err != nil {
		t.Fatal(err)
	}
	if reason == "" {
		t.Fatal("a 150.00 saving over a median of 100.00 was accepted: the bound is compared with the mean")
	}
}

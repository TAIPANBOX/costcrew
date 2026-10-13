package crew_test

// ANNOUNCED-DRIVER-SPEC.md section 11, finding (b), at save time: a driver.announced
// option whose saving is larger than the service's median daily spend on its
// desk, over the 28 days before its start, is refused whole, with the reason, and
// nothing is saved. The same option within the bound is saved.
//
// Red against the code before this step: the save-time gate does not ask the
// spend, so the over-bound option is saved.

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
)

// boundSaveDB is the options store with the estate's charges table added: the
// bound reads the spend.
func boundSaveDB(t *testing.T) *sql.DB {
	t.Helper()
	db := optionsTestDB(t)
	if _, err := db.Exec(estate.SeedSchema); err != nil {
		t.Fatal(err)
	}
	if err := estate.EnsureDriverColumns(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// boundSaveSpend writes days of usage on one desk's service, a day at a time.
func boundSaveSpend(t *testing.T, db *sql.DB, desk, service, first string, days int, cents int64) {
	t.Helper()
	start, err := time.Parse("2006-01-02", first)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < days; i++ {
		day := start.AddDate(0, 0, i).Format("2006-01-02")
		if _, err := db.Exec(`INSERT INTO charges
			(source, day, service, team, category, billed_cents) VALUES (?,?,?,NULL,'Usage',?)`,
			desk, day, service, cents); err != nil {
			t.Fatal(err)
		}
	}
}

// The task is on desk aws. 260.00 a day for the 28 days before 1 August.
func TestAnAnnouncedSavingOverItsBoundIsRefusedWhenSaved(t *testing.T) {
	db := boundSaveDB(t)
	boundSaveSpend(t, db, "aws", "Test Service", "2026-07-04", 28, 260_00)
	taskID := plantPlainTask(t, db)
	target := `{"scope":"Test Service","start":"2026-08-01","end":"2026-08-30","daily_cents":-30000}`
	body := driverOptionBody("driver.announced", target)
	artID := plantDraftArtifact(t, db, taskID, body)

	refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "optimizer-aws", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !refused {
		t.Fatal("a saving of 300.00 a day on a service that costs 260.00 a day was saved")
	}
	if !strings.Contains(reason, "larger than the median daily spend") || !strings.Contains(reason, "260.00") {
		t.Errorf("the refusal %q does not name the bound and the median", reason)
	}
	if opts, _ := crew.Options(db, artID); len(opts) != 0 {
		t.Errorf("a refused option was saved anyway: %d row(s)", len(opts))
	}
}

// The same saving, at the median, is saved.
func TestAnAnnouncedSavingAtItsBoundIsSaved(t *testing.T) {
	db := boundSaveDB(t)
	boundSaveSpend(t, db, "aws", "Test Service", "2026-07-04", 28, 260_00)
	taskID := plantPlainTask(t, db)
	target := `{"scope":"Test Service","start":"2026-08-01","end":"2026-08-30","daily_cents":-26000}`
	body := driverOptionBody("driver.announced", target)
	artID := plantDraftArtifact(t, db, taskID, body)

	refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "optimizer-aws", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if refused {
		t.Fatalf("a saving equal to the median was refused: %s", reason)
	}
	if opts, _ := crew.Options(db, artID); len(opts) != 1 {
		t.Errorf("the accepted option was not saved: %d row(s)", len(opts))
	}
}

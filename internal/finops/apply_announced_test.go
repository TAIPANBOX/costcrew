package finops_test

// ANNOUNCED-DRIVER-SPEC.md sections 2 and 6: an owner's answer on a
// driver.announced option writes one announced drivers row, and nobody else's
// answer does. The supervisor's own pass never applies the class, and a
// direct stamp by the supervisor is refused at the apply path too, so the
// owner-only rule holds for this class without relying on the pass to remember it.
//
// Red against the code before this step: applySideEffect has no case for the
// class, so an owner's answer applies the option and writes no drivers row,
// and a target that reaches Apply is never checked.

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

func announcedCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM drivers WHERE kind='announced'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAnOwnersAnswerOnAnAnnouncedOptionWritesTheDriver(t *testing.T) {
	db := applyTestDB(t)
	// The saving is bounded by the spend before its start (section 11): 400.00 a
	// day over the 28 days before 1 November, against a saving of 300.00 a day.
	annSpend(t, db, "aws", "svc-a", "2026-10-04", 28, 400_00)
	target := `{"scope":"svc-a","start":"2026-11-01","end":"2026-11-30","daily_cents":-30000}`
	opt := plantOptionWithTarget(t, db, "aws", "", "driver.announced", "swap to the smaller model", target)

	if err := finops.ApplyAs(db, opt, crew.OwnerAnswer("y.mercer"), nil); err != nil {
		t.Fatal(err)
	}
	if n := announcedCount(t, db); n != 1 {
		t.Fatalf("announced drivers = %d, want exactly one", n)
	}
	var scope, start, end, source string
	var expected sql.NullInt64
	if err := db.QueryRow(`SELECT scope, date_start, date_end, source, expected_daily_cents
		FROM drivers WHERE kind='announced'`).Scan(&scope, &start, &end, &source, &expected); err != nil {
		t.Fatal(err)
	}
	if scope != "svc-a" || start != "2026-11-01" || end != "2026-11-30" {
		t.Errorf("window = %s %s..%s, want svc-a 2026-11-01..2026-11-30", scope, start, end)
	}
	if source != "aws" {
		t.Errorf("source = %q, want the task's desk aws", source)
	}
	if !expected.Valid || expected.Int64 != -30000 {
		t.Errorf("expected_daily_cents = %v, want -30000", expected)
	}
	if got := mustGetOption(t, db, opt.Artifact, opt.Ordinal); got.State != crew.OptionApplied {
		t.Errorf("option state %q, want applied", got.State)
	}
}

// The supervisor's own pass (roles.yaml: the class is the owner's, and no
// role decides it alone) carries the option to the owner and writes nothing,
// even inside T.anomaly and with a target that is well-formed.
func TestTheSupervisorsPassNeverAppliesAnAnnouncedOption(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	target := `{"scope":"svc-a","start":"2026-11-01","end":"2026-11-30","daily_cents":-30000}`
	plantPostedOptionWithTarget(t, db, sprintID, "aws", "y.mercer", "driver.announced",
		"swap to the smaller model", 30000, "low", target)

	before := announcedCount(t, db)
	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Applied) != 0 {
		t.Fatalf("the supervisor applied %d option(s); driver.announced is the owner's alone", len(pass.Applied))
	}
	if len(pass.Carried) != 1 {
		t.Fatalf("carried %d, want 1: the option goes to the owner", len(pass.Carried))
	}
	if after := announcedCount(t, db); after != before {
		t.Fatalf("announced drivers went from %d to %d: the supervisor wrote a row", before, after)
	}
}

func TestAnAnnouncedOptionWithNoTargetWritesNothing(t *testing.T) {
	db := applyTestDB(t)
	opt := plantOption(t, db, "aws", "", "driver.announced", "a swap with no window")

	before := announcedCount(t, db)
	err := finops.ApplyAs(db, opt, crew.OwnerAnswer("y.mercer"), nil)
	if err == nil {
		t.Fatal("driver.announced with no target was applied without error")
	}
	if !strings.Contains(err.Error(), "driver.announced") {
		t.Errorf("error %q does not name the class", err.Error())
	}
	if after := announcedCount(t, db); after != before {
		t.Errorf("announced drivers went from %d to %d, want unchanged", before, after)
	}
	if got := mustGetOption(t, db, opt.Artifact, opt.Ordinal); got.State != crew.OptionOpen {
		t.Errorf("option state %q, want still open: Apply returned an error", got.State)
	}
}

// An option that reached Apply by bypassing the save-time gate is refused
// there too, by the same validation (ANNOUNCED-DRIVER-SPEC.md section 2, "again
// at apply time").
func TestAnAnnouncedOptionThatBypassedSaveIsRefusedAtApply(t *testing.T) {
	db := applyTestDB(t)
	wide := `{"scope":"*","start":"2026-11-01","end":"2026-11-30","daily_cents":-30000}`
	opt := plantOptionWithTarget(t, db, "aws", "", "driver.announced", "a wide saving", wide)

	before := announcedCount(t, db)
	if err := finops.ApplyAs(db, opt, crew.OwnerAnswer("y.mercer"), nil); err == nil {
		t.Fatal("a wide-scope announced target was applied")
	}
	if after := announcedCount(t, db); after != before {
		t.Errorf("announced drivers went from %d to %d, want unchanged", before, after)
	}
}

// The owner-only rule on the apply path itself: a stamp that names the
// supervisor is refused for this class, whichever code path reaches Apply.
func TestTheSupervisorStampCannotApplyAnAnnouncedOption(t *testing.T) {
	db := applyTestDB(t)
	target := `{"scope":"svc-a","start":"2026-11-01","end":"2026-11-30","daily_cents":-30000}`
	opt := plantOptionWithTarget(t, db, "aws", "", "driver.announced", "a swap", target)

	before := announcedCount(t, db)
	if err := finops.Apply(db, opt, "supervisor", nil); err == nil {
		t.Fatal("the supervisor's stamp applied driver.announced")
	} else if !strings.Contains(err.Error(), "owner") {
		t.Errorf("error %q does not say the owner decides this", err.Error())
	}
	if after := announcedCount(t, db); after != before {
		t.Errorf("announced drivers went from %d to %d, want unchanged", before, after)
	}
	if got := mustGetOption(t, db, opt.Artifact, opt.Ordinal); got.State != crew.OptionOpen {
		t.Errorf("option state %q, want still open", got.State)
	}
}

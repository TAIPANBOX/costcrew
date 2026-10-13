package finops_test

// ANNOUNCED-DRIVER-SPEC.md section 11, finding (b), at apply time: an option that
// reached Apply without passing the save-time gate is bound the same way. A
// saving larger than the service's median daily spend over the 28 days before its
// start is refused, with the reason; no drivers row is written and the option
// stays open for the owner.
//
// Red against the code before this step: applyAnnounced does not ask the spend.

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

func TestApplyRefusesASavingOverItsBoundWithItsReason(t *testing.T) {
	db := applyTestDB(t)
	// 260.00 a day for the 28 days before 1 August; no other service is charged
	// with this name, so the window holds only what this test wrote.
	annSpend(t, db, "aws", "Test Service", "2026-07-04", 28, 260_00)
	target := `{"scope":"Test Service","start":"2026-08-01","end":"2026-08-30","daily_cents":-30000}`
	opt := plantOptionWithTarget(t, db, "aws", "", "driver.announced", "a bound saving", target)

	before := announcedCount(t, db)
	err := finops.ApplyAs(db, opt, crew.OwnerAnswer("y.mercer"), nil)
	if err == nil {
		t.Fatal("a saving of 300.00 a day on a service that costs 260.00 a day was applied")
	}
	if !strings.Contains(err.Error(), "larger than the median daily spend") || !strings.Contains(err.Error(), "260.00") {
		t.Errorf("the refusal %q does not name the bound and the median", err.Error())
	}
	if after := announcedCount(t, db); after != before {
		t.Errorf("announced drivers went from %d to %d: a refused saving wrote a row", before, after)
	}
	if got := mustGetOption(t, db, opt.Artifact, opt.Ordinal); got.State != crew.OptionOpen {
		t.Errorf("option state %q, want still open: the apply was refused", got.State)
	}
}

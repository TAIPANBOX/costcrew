package deliver

// costcrew#74: the chargeback analyst proposes the unit rules, so the close
// pack names every customer unit that has spend and no rule yet, and says in
// what shape a proposal is written. Without it the analyst has no way to
// learn the unit names from its packet.

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

func plantUnitCharge(t *testing.T, db *sql.DB, day, unit string, cents int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO charges(source, day, service, team, category, billed_cents,
		quantity, unit, meter, model, provenance)
		VALUES ('ai', ?, 'LLM inference', ?, 'Usage', ?, 1, 'tokens', 'm', 'mdl', 'tokenfuse-focus')`,
		day, unit, cents); err != nil {
		t.Fatal(err)
	}
}

func TestClosePackSectionNamesAUnitWithNoRuleAndTheShapeOfAProposal(t *testing.T) {
	db := closePackTestDB(t)
	period := aClosePackMonth(t, db)
	plantUnitCharge(t, db, period+"-10", "acme-eu", 12345)
	task := crew.Task{Title: "Close the books, " + period, Desk: "management"}

	got := closePackSection(db, chargebackAnalyst, task)
	for _, want := range []string{
		"Customer units",
		"acme-eu",
		"123.45",
		"no unit rule yet",
		`"unit"`,
		`"business_unit"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the close pack does not carry %q for a unit with no rule:\n%s", want, got)
		}
	}
}

func TestClosePackSectionNamesTheBusinessUnitOfARuledUnit(t *testing.T) {
	db := closePackTestDB(t)
	period := aClosePackMonth(t, db)
	plantUnitCharge(t, db, period+"-10", "acme-eu", 12345)
	// A rule a person stamped earlier; the row is written the way the
	// stamp writes it (finops_test holds the stamp itself).
	if _, err := db.Exec(`INSERT INTO unit_rules(unit, business_unit, decided_by, artifact, ordinal, applied_at)
		VALUES ('acme-eu','Acme Europe','owner1',1,1,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	task := crew.Task{Title: "Close the books, " + period, Desk: "management"}

	got := closePackSection(db, chargebackAnalyst, task)
	if !strings.Contains(got, "Acme Europe") {
		t.Errorf("the close pack does not name the business unit a rule gave acme-eu:\n%s", got)
	}
	if strings.Contains(got, "no unit rule yet") {
		t.Errorf("the close pack calls a ruled unit unruled:\n%s", got)
	}
}

func TestClosePackSectionSaysNothingOfUnitsOnTheGeneratedEstate(t *testing.T) {
	db := closePackTestDB(t)
	period := aClosePackMonth(t, db)
	task := crew.Task{Title: "Close the books, " + period, Desk: "management"}

	if got := closePackSection(db, chargebackAnalyst, task); strings.Contains(got, "Customer units") {
		t.Errorf("an estate with no customer units has a Customer units section:\n%s", got)
	}
}

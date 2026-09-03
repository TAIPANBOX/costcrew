package finops_test

// C6-SPEC.md section 4. Red first, run against main before internal/finops/licences.go
// existed: every test below fails to COMPILE ("undefined: finops.Licences",
// "undefined: finops.RenewalsWithin").

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// importSaasSeatsFixture reads internal/connectors/testdata/saas-seats-2026-09-03.csv
// (the connectors package's own fixture; see its provenance comment there)
// into db.
func importSaasSeatsFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	src := filepath.Join("..", "connectors", "testdata", "saas-seats-2026-09-03.csv")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seats.csv"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := connectors.Save(db, "saas-seats", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := connectors.Import(db, "saas-seats", false, connectors.ImportOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestLicencesEmptyOnAFreshInstallNotAnError(t *testing.T) {
	db := bareDB(t)
	if err := connectors.EnsureLicenceSchema(db); err != nil {
		t.Fatal(err)
	}
	rows, err := finops.Licences(db)
	if err != nil {
		t.Fatalf("Licences on an empty table returned an error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("Licences on an empty table returned %d rows, want 0", len(rows))
	}
}

// The Gherkin scenario "idle seats are counted, not guessed"
// (features/renewals.feature): idle and waste come from the actual imported
// numbers, exact, never a derived estimate the way the generated fixture's
// own hashPct produces one.
func TestIdleSeatsAreCountedNotGuessed(t *testing.T) {
	db := bareDB(t)
	importSaasSeatsFixture(t, db)

	rows, err := finops.Licences(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("got %d licences, want 4", len(rows))
	}
	var totalIdle int
	var totalWaste money.Cents
	byVendor := map[string]int{}
	for i, l := range rows {
		byVendor[l.Vendor] = i
		wantIdle := l.Issued - l.Active30
		if l.Idle() != wantIdle {
			t.Errorf("%s: Idle() = %d, want %d (Issued %d - Active30 %d)",
				l.Vendor, l.Idle(), wantIdle, l.Issued, l.Active30)
		}
		wantWaste := money.Cents(int64(wantIdle) * int64(l.PerSeat))
		if l.Waste() != wantWaste {
			t.Errorf("%s: Waste() = %s, want %s (%d idle * %s per seat)",
				l.Vendor, l.Waste(), wantWaste, wantIdle, l.PerSeat)
		}
		totalIdle += l.Idle()
		totalWaste += l.Waste()
	}
	// The fixture's own known totals: (60-42)+(80-39)+(150-150)+(25-24) = 60
	// idle seats; 18*11500 + 41*4500 + 0*2100 + 1*13200 = 404700 cents.
	if totalIdle != 60 {
		t.Errorf("total idle = %d, want 60", totalIdle)
	}
	if totalWaste != money.Cents(404700) {
		t.Errorf("total waste = %s, want %s", totalWaste, money.Cents(404700))
	}
	if _, ok := byVendor["Zendesk"]; !ok {
		t.Fatal("this test's own fixture does not carry Zendesk; broken setup")
	}
}

// Boundary: issued equal to active is zero waste, not refused and not a
// special case in the arithmetic.
func TestIssuedEqualsActiveIsZeroWaste(t *testing.T) {
	db := bareDB(t)
	importSaasSeatsFixture(t, db)
	rows, err := finops.Licences(db)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range rows {
		if l.Vendor != "GitHub" {
			continue
		}
		found = true
		if l.Issued != l.Active30 {
			t.Fatalf("this test's own fixture does not have GitHub issued==active: %d vs %d",
				l.Issued, l.Active30)
		}
		if l.Idle() != 0 {
			t.Errorf("GitHub Idle() = %d, want 0", l.Idle())
		}
		if l.Waste() != 0 {
			t.Errorf("GitHub Waste() = %s, want 0.00", l.Waste())
		}
	}
	if !found {
		t.Fatal("GitHub row not found; this test's own fixture is broken")
	}
}

// The Gherkin scenario "the calendar says what renews and when notice is
// due" (features/renewals.feature), at the computation layer: the next
// ninety days from a fixed reference day, boundaries included both ends.
func TestRenewalsWithinNinetyDays(t *testing.T) {
	db := bareDB(t)
	importSaasSeatsFixture(t, db)

	// The fixture's own dates, relative to "2026-09-03": Zendesk renews that
	// same day (0 days out), Figma at exactly 90, GitHub at 28, and NetSuite
	// at 91 -- one day past the edge, so this test has something to prove
	// the filter actually excludes.
	rows, err := finops.RenewalsWithin(db, 90, "2026-09-03")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, l := range rows {
		got[l.Vendor] = true
	}
	for _, want := range []string{"Zendesk", "Figma", "GitHub"} {
		if !got[want] {
			t.Errorf("RenewalsWithin(90, 2026-09-03) is missing %s", want)
		}
	}
	if got["NetSuite"] {
		t.Errorf("RenewalsWithin(90, 2026-09-03) includes NetSuite, which renews 91 days out")
	}
	if len(rows) != 3 {
		t.Errorf("RenewalsWithin(90, 2026-09-03) returned %d rows, want 3: %v", len(rows), rows)
	}
}

// Boundary: a renewal exactly ninety days out is included (the edge itself,
// not just "under" it).
func TestRenewalsWithinIncludesTheExactEdge(t *testing.T) {
	db := bareDB(t)
	importSaasSeatsFixture(t, db)
	rows, err := finops.RenewalsWithin(db, 90, "2026-09-03")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range rows {
		if l.Vendor == "Figma" {
			found = true
			if l.Renews != "2026-12-02" {
				t.Fatalf("this test's own fixture does not have Figma at the 90-day edge: renews %s", l.Renews)
			}
		}
	}
	if !found {
		t.Error("Figma (renewing exactly 90 days out) was excluded; the boundary is not inclusive")
	}
}

// Boundary: a renewal today (zero days out) is included.
func TestRenewalsWithinIncludesToday(t *testing.T) {
	db := bareDB(t)
	importSaasSeatsFixture(t, db)
	rows, err := finops.RenewalsWithin(db, 90, "2026-09-03")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range rows {
		if l.Vendor == "Zendesk" && l.Renews == "2026-09-03" {
			return
		}
	}
	t.Error("Zendesk (renewing today) was excluded from RenewalsWithin(90, 2026-09-03)")
}

func TestRenewalsWithinAWindowOfZeroOnlyMatchesToday(t *testing.T) {
	db := bareDB(t)
	importSaasSeatsFixture(t, db)
	rows, err := finops.RenewalsWithin(db, 0, "2026-09-03")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Vendor != "Zendesk" {
		t.Errorf("RenewalsWithin(0, 2026-09-03) = %v, want exactly [Zendesk]", rows)
	}
}

// The notice deadline is NoticeDays before Renews, computed, not guessed.
func TestNoticeDeadlineComputation(t *testing.T) {
	db := bareDB(t)
	importSaasSeatsFixture(t, db)
	rows, err := finops.Licences(db)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Zendesk":  "2026-08-19", // 2026-09-03 minus 15 days
		"Figma":    "2026-11-02", // 2026-12-02 minus 30 days
		"NetSuite": "2026-09-04", // 2026-12-03 minus 90 days
	}
	got := map[string]string{}
	for _, l := range rows {
		got[l.Vendor] = l.NoticeDeadline()
	}
	for vendor, deadline := range want {
		if got[vendor] != deadline {
			t.Errorf("%s: NoticeDeadline() = %q, want %q", vendor, got[vendor], deadline)
		}
	}
}

// The Zendesk row's notice deadline (2026-08-19) is already before this
// test's own reference day (2026-09-03): the raw arithmetic must say so
// plainly rather than clamp it to today or hide it.
func TestNoticeDeadlineAlreadyPassedIsComputedPlainly(t *testing.T) {
	db := bareDB(t)
	importSaasSeatsFixture(t, db)
	rows, err := finops.Licences(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range rows {
		if l.Vendor != "Zendesk" {
			continue
		}
		deadline := l.NoticeDeadline()
		if deadline >= "2026-09-03" {
			t.Errorf("Zendesk's notice deadline %q is not before 2026-09-03; this test's own fixture is broken", deadline)
		}
		return
	}
	t.Fatal("Zendesk row not found")
}

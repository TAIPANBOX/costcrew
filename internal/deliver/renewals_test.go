package deliver

// C6-SPEC.md section 4. Red first, run against main before this file and
// packet.go's own renewalsSection existed: every test below fails to
// COMPILE ("undefined: renewalsSection"), and TestRenewalsSectionReachesThePacket
// fails on content (Packet() carries no "SaaS renewal calendar" text at
// all, since main's Packet has no gate for the two SaaS roles' skills).

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// importSaasSeatsFixtureInto is this package's own copy of the same helper
// internal/finops/licences_test.go and internal/connectors/saasseats_test.go
// already carry: internal/deliver has no dependency on either of those
// packages' test files, so a fourth copy is one import away rather than a
// shared test-only package this module does not otherwise need.
func importSaasSeatsFixtureInto(t *testing.T, db *sql.DB) {
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

// The Gherkin scenario "the calendar says what renews and when notice is
// due" (features/renewals.feature): the section names every renewal inside
// the window, each with its own notice deadline, from a fixed reference day
// so the test does not depend on when it happens to run.
func TestRenewalsSectionListsTheCalendarWithNoticeDeadlines(t *testing.T) {
	db := deliverTestDB(t)
	importSaasSeatsFixtureInto(t, db)

	s := renewalsSection(db, "2026-09-03")
	if s == "" {
		t.Fatal("renewalsSection returned nothing against an imported fixture")
	}
	for _, want := range []string{
		"Zendesk / Suite Professional",
		"Figma / Organization",
		"renews:          2026-09-03",
		"notice deadline: 2026-08-19 (already passed)",
		"issued/active:   60/42 over 30 days (idle 18)",
		"waste:           2070.00 a month",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("renewalsSection does not contain %q:\n%s", want, s)
		}
	}
	// NetSuite renews 91 days out (past the ninety-day window) and must not
	// appear at all.
	if strings.Contains(s, "NetSuite") {
		t.Errorf("renewalsSection names NetSuite, which is outside the ninety-day window:\n%s", s)
	}
}

// Boundary: a notice deadline still ahead of today is not flagged.
func TestRenewalsSectionDoesNotFlagANoticeDeadlineStillAhead(t *testing.T) {
	db := deliverTestDB(t)
	importSaasSeatsFixtureInto(t, db)
	s := renewalsSection(db, "2026-09-03")
	if !strings.Contains(s, "notice deadline: 2026-11-02\n") {
		t.Errorf("Figma's own notice deadline (2026-11-02, still ahead of 2026-09-03) is missing or wrongly flagged:\n%s", s)
	}
	if strings.Contains(s, "2026-11-02 (already passed)") {
		t.Errorf("a notice deadline that has not happened yet was flagged as passed:\n%s", s)
	}
}

// C6-SPEC.md section 2: "no benchmark" where none exists, never a number
// with no source. There is no benchmark connector anywhere in this practice
// today, so every renewal says so.
func TestRenewalsSectionSaysNoBenchmark(t *testing.T) {
	db := deliverTestDB(t)
	importSaasSeatsFixtureInto(t, db)
	s := renewalsSection(db, "2026-09-03")
	n := strings.Count(s, "no benchmark")
	if n != 3 {
		t.Errorf(`renewalsSection says "no benchmark" %d times, want 3 (once per renewal inside the window):`+"\n%s", n, s)
	}
}

// Additive: nothing imported, nothing shown. The same rule every other
// section in packet.go already holds.
func TestRenewalsSectionIsEmptyWithNothingImported(t *testing.T) {
	db := deliverTestDB(t)
	if s := renewalsSection(db, "2026-09-03"); s != "" {
		t.Errorf("renewalsSection on a store with no imported licences = %q, want empty", s)
	}
}

// Packet() reaches renewalsSection for the SaaS portfolio manager's own
// skills, and for the renewals analyst's, through the same gate the other
// desk-shaped sections already use.
func TestPacketCarriesTheRenewalsSectionForBothSaasRoles(t *testing.T) {
	db := deliverTestDB(t)
	importSaasSeatsFixtureInto(t, db)
	task := crew.Task{ID: 1, Desk: "saas"}

	manager := crew.Analyst{Name: "saas-manager", State: "active",
		Skills: []string{"licence-reconciliation", "renewal-calendar"}}
	p := Packet(db, task, manager, false)
	if !strings.Contains(p, "The SaaS renewal calendar") {
		t.Errorf("the saas-portfolio-manager's own packet does not carry the renewals section:\n%s", p)
	}

	renewals := crew.Analyst{Name: "renewals", State: "active",
		Skills: []string{"renewal-negotiation-prep", "vendor-benchmarking"}}
	p2 := Packet(db, task, renewals, false)
	if !strings.Contains(p2, "The SaaS renewal calendar") {
		t.Errorf("the renewals-analyst's own packet does not carry the renewals section:\n%s", p2)
	}
}

// And an unrelated role, on an unrelated desk, does not carry it: the gate
// is on the SKILL, not merely on something having been imported.
func TestPacketDoesNotCarryTheRenewalsSectionForAnUnrelatedRole(t *testing.T) {
	db := deliverTestDB(t)
	importSaasSeatsFixtureInto(t, db)
	task := crew.Task{ID: 1, Desk: "aws"}
	investigator := crew.Analyst{Name: "investigator-aws", State: "active",
		Skills: []string{"variance-commentary", "anomaly-triage", "driver-classification"}}
	p := Packet(db, task, investigator, false)
	if strings.Contains(p, "The SaaS renewal calendar") {
		t.Errorf("an aws investigator's packet carries the SaaS renewals section:\n%s", p)
	}
}

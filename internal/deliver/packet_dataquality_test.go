package deliver

// C9-SPEC.md section 2: "Packet section: dataQualitySection for the
// data-quality role: the three figures per source with the thresholds and
// which is crossed."
//
// Red first, against main: Packet carries no such section, and no skill
// gate for data-quality-checks/tag-coverage exists in it at all, so the
// data-quality analyst's own packet is silent about the one thing its
// mission is to check.

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

func dqPacketTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := deliverTestDB(t)
	if err := finops.SeedRules(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPacketCarriesDataQualityForTheDataQualityRole(t *testing.T) {
	db := dqPacketTestDB(t)
	// A stale, well-untagged aws desk: nothing charged for far longer than
	// T.stale, so the section names it CROSSED rather than merely present.
	if _, err := db.Exec(`INSERT INTO charges(source, day, service, team, category, billed_cents)
		VALUES ('aws', '2026-01-01', 'svc', NULL, 'Usage', 5000)`); err != nil {
		t.Fatal(err)
	}

	task := crew.Task{ID: 1, Desk: "management"}
	a := crew.Analyst{Name: "data-quality", State: "active",
		Skills: []string{"data-quality-checks", "tag-coverage"}}

	got := Packet(db, task, a, false)
	if !strings.Contains(got, "Data quality") {
		t.Fatalf("the data-quality analyst's packet carries no data quality section:\n%s", got)
	}
	if !strings.Contains(got, "aws") {
		t.Errorf("the data quality section does not name aws:\n%s", got)
	}
	if !strings.Contains(got, "CROSSED") {
		t.Errorf("a source stale since 2026-01-01 does not read as crossed:\n%s", got)
	}
}

// A role with no data-quality skill sees nothing about it: the section is
// gated on the skill, the same way reportingSection is gated on
// exec-reporting/showback-narration/variance-commentary.
func TestPacketOmitsDataQualityForAnUnrelatedRole(t *testing.T) {
	db := dqPacketTestDB(t)
	task := crew.Task{ID: 1, Desk: "aws"}
	a := crew.Analyst{Name: "triage-aws", State: "active", Skills: []string{"anomaly-triage"}}

	got := Packet(db, task, a, false)
	if strings.Contains(got, "Data quality") {
		t.Errorf("an unrelated role's packet carries the data quality section:\n%s", got)
	}
}

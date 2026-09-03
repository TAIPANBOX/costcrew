package deliver

// C8-SPEC.md section 2's first bullet: executiveSection, the exec-reporter's
// own packet section -- the four KPI figures with last period's value and
// the delta, and the last three posted explanations on the desks whose
// spend moved most. internal/finops/executive_test.go holds the four
// numbers themselves (Executive()); this file holds what Packet() does with
// them: the section's presence, its gate (only an analyst carrying
// "decision-framing"), the refusal wording, the moved-desk ranking, the
// boundary of a desk with nothing posted, and the 1 MB hostile body.

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// plantCharge writes one row into charges directly: the minimal shape
// executiveSection's own moved-desk ranking and Executive()'s KPI
// computation both read, independent of estate.Seed's much larger generated
// world (which cannot produce the exact, deterministic deltas these tests
// need between two named desks).
func plantCharge(t *testing.T, db *sql.DB, source, day, service, team string, billedCents int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO charges(source, day, service, team, category, billed_cents)
		VALUES (?,?,?,?, 'Compute', ?)`, source, day, service, nullIfEmpty(team), billedCents); err != nil {
		t.Fatal(err)
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// twoDeskThreeMonthDB carries three months so executivePeriod() (kpi.go)
// has a period (the last COMPLETE month, 2026-02) AND a previous one
// (2026-01) to compare against -- 2026-03 exists only to make 2026-02 not
// be the newest month, which is what "complete" means here. aws moves by
// +40000 between the two compared months; gcp moves by +2000: aws is
// unambiguously "the desk whose spend moved most".
func twoDeskThreeMonthDB(t *testing.T) *sql.DB {
	t.Helper()
	db := deliverTestDB(t)
	plantCharge(t, db, "aws", "2026-01-10", "Amazon EC2", "ml-platform", 10000)
	plantCharge(t, db, "gcp", "2026-01-10", "GKE", "research", 10000)
	plantCharge(t, db, "aws", "2026-02-10", "Amazon EC2", "ml-platform", 50000)
	plantCharge(t, db, "gcp", "2026-02-10", "GKE", "research", 12000)
	plantCharge(t, db, "aws", "2026-03-10", "Amazon EC2", "ml-platform", 1000)
	return db
}

func execReporterAnalyst() crew.Analyst {
	return crew.Analyst{Name: "exec-reporter", State: "active",
		Skills: []string{"exec-reporting", "decision-framing"}}
}

func execReporterTask() crew.Task {
	// No anomaly, desk "management": crew.CadenceDue (internal/crew/plan.go)
	// gives an org-wide analyst's task the ANALYST's own desk, and
	// exec-reporter's is "management" (internal/world/world.go).
	return crew.Task{ID: 900, Desk: "management"}
}

// Red first, against main: Packet() has no executiveSection at all, and
// there is nothing in "exec-reporting" alone (shared with every desk
// reporter) that would produce four KPI figures. C8-SPEC.md section 4:
// "the exec-reporter packet carries the four numbers with previous values
// ... (today no such section)".
func TestExecutiveSectionCarriesTheFourNumbers(t *testing.T) {
	db := twoDeskThreeMonthDB(t)
	got := Packet(db, execReporterTask(), execReporterAnalyst(), false)

	if !strings.Contains(got, "The executive pack (2026-02)") {
		t.Fatalf("the packet does not carry the executive pack header at all:\n%s", got)
	}
	// The KPI's own Name, the SAME field the /kpis page itself shows, not
	// the bare id: "so the packet and the page cannot disagree",
	// C8-SPEC.md section 2, is a promise about the NUMBER, and a reader
	// comparing the two pages should recognise the label too.
	for _, want := range []string{"Allocation coverage:", "Cost with no owner:",
		"AI spend attributed to an agent:", "Cost per business outcome:"} {
		if !strings.Contains(got, want) {
			t.Errorf("the packet does not name %q among the four numbers:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "was ") {
		t.Errorf("the packet does not carry a previous-period value on a three-month estate:\n%s", got)
	}
}

// The gate: an analyst who shares "exec-reporting" with the desk reporters
// but does not carry "decision-framing" (exec-reporter's own second skill)
// gets reportingSection, never executiveSection -- the two are different
// sections for different roles, and a desk reporter's packet is about ONE
// desk's month, not four estate-wide numbers.
func TestExecutiveSectionIsAbsentForAPlainDeskReporter(t *testing.T) {
	db := twoDeskThreeMonthDB(t)
	a := crew.Analyst{Name: "reporter-aws", State: "active", Skills: []string{"exec-reporting", "showback-narration"}}
	got := Packet(db, crew.Task{ID: 901, Desk: "aws"}, a, false)
	if strings.Contains(got, "The executive pack") {
		t.Errorf("a plain desk reporter's packet carries the executive pack section:\n%s", got)
	}
}

// C8-SPEC.md section 4: "a refused KPI appears as refused in the packet,
// not as zero". cost-per-outcome never computes in this console (no
// outcome metric is connected until C7), on ANY estate.
func TestExecutiveSectionShowsARefusedKPIAsRefusedNeverZero(t *testing.T) {
	db := twoDeskThreeMonthDB(t)
	got := Packet(db, execReporterTask(), execReporterAnalyst(), false)

	if !strings.Contains(got, "Cost per business outcome: refused,") {
		t.Fatalf("the packet does not show cost-per-outcome as refused:\n%s", got)
	}
	for _, zero := range []string{"Cost per business outcome: 0", "Cost per business outcome: 0.0%"} {
		if strings.Contains(got, zero) {
			t.Errorf("the packet shows the refused KPI as a zero (%q):\n%s", zero, got)
		}
	}
}

// The desk whose spend moved most (aws, +40000 against gcp's +2000) is the
// one whose posted explanation reaches the pack.
func TestExecutiveSectionShowsTheLastExplanationOnTheDeskThatMovedMost(t *testing.T) {
	db := twoDeskThreeMonthDB(t)
	taskID := plantMemoryTask(t, db, "aws", "Explain the EC2 move")
	plantPostedArtifact(t, db, taskID, "investigator-aws",
		"The EC2 spend rose because a training run left forty instances up over a weekend.",
		"2026-02-20T10:00:00Z")
	// A red herring on the desk that moved LESS: present in the store, and
	// must not be what the pack picks.
	gcpTask := plantMemoryTask(t, db, "gcp", "Explain the GKE move")
	plantPostedArtifact(t, db, gcpTask, "investigator-gcp",
		"GKE ticked up slightly; nothing worth a conversation.", "2026-02-18T10:00:00Z")

	got := Packet(db, execReporterTask(), execReporterAnalyst(), false)

	if !strings.Contains(got, "Explain the EC2 move") {
		t.Errorf("the packet does not name the explanation on aws, the desk that moved most:\n%s", got)
	}
	if !strings.Contains(got, "forty instances up over a weekend") {
		t.Errorf("the packet does not carry the body of aws's own posted explanation:\n%s", got)
	}
}

// Boundary, C8-SPEC.md section 4: "a desk with no posted explanation". The
// top mover (aws) has nothing posted on it; the pack falls through to the
// next desk in the ranking (gcp) rather than showing nothing at all.
func TestExecutiveSectionFallsThroughADeskWithNoPostedExplanation(t *testing.T) {
	db := twoDeskThreeMonthDB(t)
	gcpTask := plantMemoryTask(t, db, "gcp", "Explain the GKE move")
	plantPostedArtifact(t, db, gcpTask, "investigator-gcp",
		"A quarterly refresh landed a week early.", "2026-02-18T10:00:00Z")

	got := Packet(db, execReporterTask(), execReporterAnalyst(), false)

	if !strings.Contains(got, "Explain the GKE move") {
		t.Errorf("the packet does not fall through to gcp when aws (the top mover) has "+
			"nothing posted:\n%s", got)
	}
	if !strings.Contains(got, "A quarterly refresh landed a week early.") {
		t.Errorf("the packet does not carry gcp's own posted explanation:\n%s", got)
	}
}

// Hostile, C8-SPEC.md section 4: "an explanation body of 1 MB (trimmed)".
func TestExecutiveSectionTrimsAOneMegabyteExplanationBody(t *testing.T) {
	db := twoDeskThreeMonthDB(t)
	taskID := plantMemoryTask(t, db, "aws", "A very long explanation")
	huge := strings.Repeat("x", 1<<20)
	plantPostedArtifact(t, db, taskID, "investigator-aws", huge, "2026-02-20T10:00:00Z")

	got := Packet(db, execReporterTask(), execReporterAnalyst(), false)
	if len(got) > packetMaxBytes {
		t.Fatalf("a 1 MB explanation body blew the packet's own 12 KiB cap: %d bytes", len(got))
	}
	if strings.Count(got, "x") > 1000 {
		t.Errorf("the 1 MB body does not look trimmed: %d 'x' characters in a %d byte packet",
			strings.Count(got, "x"), len(got))
	}
}

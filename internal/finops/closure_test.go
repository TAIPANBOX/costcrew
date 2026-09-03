package finops_test

// C1-SPEC.md section 4: the closure KPI reports a median on a desk with two
// closed anomalies and refuses on one with none. Red first:
// AnomalyClosureDays does not exist on main.

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

func closureTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := seeded(t) // finops_test.go: estate.Seed + finops.SeedRules
	if _, err := db.Exec(anomaly.Schema); err != nil {
		t.Fatal(err)
	}
	return db
}

// plantClosed writes one anomaly, closed, on desk with the given
// detected_at/closed_at pair -- the two columns the closure KPI reads.
func plantClosed(t *testing.T, db *sql.DB, id, desk, detectedAt, closedAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO anomalies
		(id, source, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule_version, state, detected_at, closed_at)
		VALUES (?, ?, 'Amazon EC2', '2026-07-14', 'up', 10000, 5000, 5000, 4.1,
		        'v1', 'accepted', ?, ?)`, id, desk, detectedAt, closedAt); err != nil {
		t.Fatal(err)
	}
}

// plantOpen writes one OPEN anomaly on desk: detected, never closed.
func plantOpen(t *testing.T, db *sql.DB, id, desk, detectedAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO anomalies
		(id, source, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule_version, state, detected_at)
		VALUES (?, ?, 'Amazon EC2', '2026-07-14', 'up', 10000, 5000, 5000, 4.1,
		        'v1', 'open', ?)`, id, desk, detectedAt); err != nil {
		t.Fatal(err)
	}
}

func TestAnomalyClosureDaysReportsTheMedianOfTwoClosedAnomalies(t *testing.T) {
	db := closureTestDB(t)
	// Two days, and six days: the median of {2, 6} is 4.
	plantClosed(t, db, "A-close-1", "aws", "2026-07-10T09:00:00Z", "2026-07-12T09:00:00Z")
	plantClosed(t, db, "A-close-2", "aws", "2026-07-10T09:00:00Z", "2026-07-16T09:00:00Z")

	got, err := finops.AnomalyClosureDays(db, "aws", "2026-07")
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasVal {
		t.Fatalf("HasVal = false, Blocked = %q; two closed anomalies should report", got.Blocked)
	}
	if got.Days != 4 {
		t.Errorf("median days = %v, want 4 (median of 2 and 6)", got.Days)
	}
	if got.N != 2 {
		t.Errorf("N = %d, want 2", got.N)
	}
}

func TestAnomalyClosureDaysRefusesADeskWithNoClosure(t *testing.T) {
	db := closureTestDB(t)
	// gcp has anomalies, but none closed: the KPI must refuse rather than
	// report zero, the same way every KPI in this library refuses instead
	// of inventing a number it has no evidence for.
	plantOpen(t, db, "A-open-1", "gcp", "2026-07-10T09:00:00Z")

	got, err := finops.AnomalyClosureDays(db, "gcp", "2026-07")
	if err != nil {
		t.Fatal(err)
	}
	if got.HasVal {
		t.Fatalf("HasVal = true (days %v) for a desk with nothing closed", got.Days)
	}
	if strings.TrimSpace(got.Blocked) == "" {
		t.Error("no reason was given for the refusal")
	}

	// And a desk with no anomalies at all refuses the same honest way,
	// rather than a different error shape for "never measured" versus
	// "measured and found nothing closed".
	got, err = finops.AnomalyClosureDays(db, "azure", "2026-07")
	if err != nil {
		t.Fatal(err)
	}
	if got.HasVal || strings.TrimSpace(got.Blocked) == "" {
		t.Errorf("a desk with no anomalies at all: HasVal=%v Blocked=%q", got.HasVal, got.Blocked)
	}
}

// Boundary: closed the same day is zero days, not refused and not negative.
func TestAnomalyClosureDaysIsZeroWhenClosedTheSameDay(t *testing.T) {
	db := closureTestDB(t)
	plantClosed(t, db, "A-same-day", "onprem", "2026-07-10T09:00:00Z", "2026-07-10T21:00:00Z")

	got, err := finops.AnomalyClosureDays(db, "onprem", "2026-07")
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasVal {
		t.Fatalf("HasVal = false, Blocked = %q", got.Blocked)
	}
	if got.Days != 0 {
		t.Errorf("median days = %v, want 0 for same-day closure", got.Days)
	}
}

// Hostile: a detected_at that does not parse is excluded from the median,
// not treated as a crash and not silently averaged in as some invented
// number -- and the result says so, rather than reporting the median of one
// valid row as though nothing were wrong.
func TestAnomalyClosureDaysExcludesARowWhoseDetectedAtWontParse(t *testing.T) {
	db := closureTestDB(t)
	plantClosed(t, db, "A-good-row", "saas", "2026-07-10T09:00:00Z", "2026-07-15T09:00:00Z") // 5 days
	plantClosed(t, db, "A-bad-row", "saas", "not-a-timestamp", "2026-07-15T09:00:00Z")

	got, err := finops.AnomalyClosureDays(db, "saas", "2026-07")
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasVal {
		t.Fatalf("HasVal = false, Blocked = %q; one good row should still report", got.Blocked)
	}
	if got.Days != 5 {
		t.Errorf("median days = %v, want 5 from the one row that parses", got.Days)
	}
	if got.N != 1 {
		t.Errorf("N = %d, want 1 (the unparseable row excluded)", got.N)
	}
	if strings.TrimSpace(got.Note) == "" {
		t.Error("the excluded row is not mentioned anywhere: this refuses silently for that one row")
	}

	// And a desk where EVERY closed row is unparseable refuses outright,
	// rather than reporting a median of zero rows.
	plantClosed(t, db, "A-all-bad", "ai", "also-not-a-timestamp", "2026-07-15T09:00:00Z")
	got, err = finops.AnomalyClosureDays(db, "ai", "2026-07")
	if err != nil {
		t.Fatal(err)
	}
	if got.HasVal {
		t.Fatalf("HasVal = true with every closed row unparseable")
	}
}

// The month filter: a closure outside the requested month must not move a
// median that is supposed to be about this month's performance.
func TestAnomalyClosureDaysOnlyCountsClosuresWithinTheMonth(t *testing.T) {
	db := closureTestDB(t)
	plantClosed(t, db, "A-in-month", "aws", "2026-07-10T09:00:00Z", "2026-07-12T09:00:00Z")     // July, 2 days
	plantClosed(t, db, "A-out-of-month", "aws", "2026-06-01T09:00:00Z", "2026-06-20T09:00:00Z") // June, 19 days

	got, err := finops.AnomalyClosureDays(db, "aws", "2026-07")
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasVal || got.N != 1 || got.Days != 2 {
		t.Errorf("July only: HasVal=%v N=%d Days=%v, want true 1 2", got.HasVal, got.N, got.Days)
	}

	// "" means every month the estate holds: both rows count, median of {2, 19} = 10.5.
	got, err = finops.AnomalyClosureDays(db, "aws", "")
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasVal || got.N != 2 || got.Days != 10.5 {
		t.Errorf("every month: HasVal=%v N=%d Days=%v, want true 2 10.5", got.HasVal, got.N, got.Days)
	}
}

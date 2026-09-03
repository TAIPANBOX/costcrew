package main

import (
	"database/sql"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// importedTestDB is a store shaped like imported data: anomalies exist, but
// NONE of them carry a driver (world.Drivers() is never seeded), which is
// B7-SPEC.md section 2's own switch between the two scoring modes. Tasks
// and artifacts are planted by hand, the same way provenance_test.go and
// packet_test.go already do for tools/run's own tests.
func importedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.DB()
	for _, schema := range []string{crew.Schema, estate.SeedSchema, anomaly.Schema} {
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := crew.EnsureArtifactProvenance(db); err != nil {
		t.Fatal(err)
	}
	if err := crew.EnsureLiveSpendLedger(db); err != nil {
		t.Fatal(err)
	}
	if _, err := crew.SeedRoster(db, "test"); err != nil {
		t.Fatal(err)
	}
	return db
}

func plantImportedAnomaly(t *testing.T, db *sql.DB, id, source, service, day string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO anomalies
		(id, source, team, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule, rule_version, driver, caused_by, caused_by_kind,
		 handled_by, state, reason, detected_at, closed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, source, "", service, day, "up", int64(money.Cents(10_000)), int64(money.Cents(2_000)),
		int64(money.Cents(8_000)), 4.0, "z-score", anomaly.RuleVersion, "", "", "",
		"", string(anomaly.Open), "", "2026-08-01T00:00:00Z", nil); err != nil {
		t.Fatal(err)
	}
}

// plantStampedTask inserts a task assigned to assignee, from anomalyID, with
// one artifact in the given state -- the shape selectStampCases reads.
func plantStampedTask(t *testing.T, db *sql.DB, assignee, desk, anomalyID string, state crew.ArtifactState) int {
	t.Helper()
	res, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, anomaly, created, updated)
		VALUES ('t', 'g', ?, ?, 'done', 0, 0, ?, datetime('now'), datetime('now'))`,
		assignee, desk, anomalyID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifacts (task, author, title, body, state, created, source)
		VALUES (?,?,?,?,?,datetime('now'),'live')`,
		id, assignee, "d", "body", string(state)); err != nil {
		t.Fatal(err)
	}
	return int(id)
}

// B7-SPEC.md section 5's own boundary: "on a store without drivers the
// bench switches to stamp scoring".
func TestHasAnyDriverIsFalseOnImportedData(t *testing.T) {
	db := importedTestDB(t)
	plantImportedAnomaly(t, db, "A-imp1", "gcp", "GKE", "2026-06-22")
	got, err := hasAnyDriver(db)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("a store with no driver on any anomaly reported hasAnyDriver = true")
	}
}

func TestHasAnyDriverIsTrueOnTheGeneratedFixture(t *testing.T) {
	db := seededTestDB(t)
	got, err := hasAnyDriver(db)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("the generated fixture carries two driver labels and hasAnyDriver said false")
	}
}

// B7-SPEC.md section 2's closing paragraph: "a case is an anomaly whose
// task has a posted or returned deliverable; the score is posted (accepted
// first pass) versus returned ... without any model call."
func TestSelectStampCasesReadsPostedAndReturned(t *testing.T) {
	db := importedTestDB(t)
	plantImportedAnomaly(t, db, "A-imp1", "gcp", "GKE", "2026-06-22")
	plantImportedAnomaly(t, db, "A-imp2", "gcp", "Amazon S3", "2026-06-23")
	plantImportedAnomaly(t, db, "A-imp3", "azure", "Azure SQL", "2026-06-24")
	plantStampedTask(t, db, "triage-gcp", "gcp", "A-imp1", crew.PostedDraft)
	plantStampedTask(t, db, "triage-gcp", "gcp", "A-imp2", crew.ReturnedDraft)
	// A draft (neither posted nor returned) is not a scoreable case.
	plantStampedTask(t, db, "triage-gcp", "gcp", "A-imp3", crew.Draft)

	cases, total, err := selectStampCases(db, "triage", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2: the still-drafted task must not count", total)
	}
	posted, returned := 0, 0
	for _, c := range cases {
		switch c.Outcome {
		case outcomePosted:
			posted++
		case outcomeReturned:
			returned++
		}
	}
	if posted != 1 || returned != 1 {
		t.Errorf("posted=%d returned=%d, want 1, 1", posted, returned)
	}
}

// A task assigned to an analyst outside the requested skill's role family
// is not a case, and neither is one whose analyst was hired with a
// different engine than the one requested.
func TestSelectStampCasesFiltersBySkillAndEngine(t *testing.T) {
	db := importedTestDB(t)
	plantImportedAnomaly(t, db, "A-imp1", "gcp", "GKE", "2026-06-22")
	plantImportedAnomaly(t, db, "A-imp2", "gcp", "Amazon S3", "2026-06-23")
	// investigator-gcp is NOT a triage analyst: excluded from -skill triage.
	plantStampedTask(t, db, "investigator-gcp", "gcp", "A-imp1", crew.PostedDraft)
	plantStampedTask(t, db, "triage-gcp", "gcp", "A-imp2", crew.PostedDraft)

	cases, total, err := selectStampCases(db, "triage", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(cases) != 1 {
		t.Fatalf("total=%d cases=%d, want 1, 1: only the triage-gcp task qualifies", total, len(cases))
	}

	// triage-gcp is seeded on "openrouter" (internal/world/world.go's
	// buildCrew: cheap := "openrouter"); filtering by "anthropic" excludes it.
	cases2, total2, err := selectStampCases(db, "triage", "anthropic", 20)
	if err != nil {
		t.Fatal(err)
	}
	if total2 != 0 || len(cases2) != 0 {
		t.Errorf("total=%d cases=%d, want 0, 0: triage-gcp was not hired on anthropic",
			total2, len(cases2))
	}
}

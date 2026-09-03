package crew_test

// C1-SPEC.md section 4: OwnerOfAnomaly returns the team's owner when set and
// the analyst's owner otherwise, with the reason. Red first: OwnerOfAnomaly
// does not exist on main.

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// ownerTestDB is a bare store with just the tables OwnerOfAnomaly's own
// chain touches: anomalies (the team), tasks (the analyst's owner, stamped
// by FromAnomaly's own OwnerOf(db, assignee) at task creation -- B3), and
// analysts (whose owner that call reads).
func ownerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(anomaly.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(crew.Schema); err != nil {
		t.Fatal(err)
	}
	if err := crew.EnsureTeamOwner(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// plantAnomalyWithTeam writes just enough of an anomalies row for the owner
// lookup: id and team. Its own name, not plan_test.go's plantAnomaly (id,
// source, service, day, excessCents, no team): a different fixture for a
// different question.
func plantAnomalyWithTeam(t *testing.T, db *sql.DB, id, team string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO anomalies
		(id, source, team, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule_version, state, detected_at)
		VALUES (?, 'aws', ?, 'Amazon EC2', '2026-07-14', 'up', 10000, 5000, 5000, 4.1,
		        'v1', 'open', '2026-07-14T09:00:00Z')`, id, nullableTeam(team)); err != nil {
		t.Fatal(err)
	}
}

func nullableTeam(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// plantTaskForAnomaly writes the task FromAnomaly would have written: one
// row, this anomaly's id, this owner already stamped on it (the exact
// column OwnerOf(db, assignee) fills at creation time, per crew.go's own
// FromAnomaly).
func plantTaskForAnomaly(t *testing.T, db *sql.DB, anomalyID, owner string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, anomaly, owner, created, updated)
		VALUES ('investigate', 'find the cause', 'investigator-aws', 'aws', 'active', 0, 0, ?, ?,
		        datetime('now'), datetime('now'))`, anomalyID, nullableTeam(owner)); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerOfAnomalyReturnsTheTeamsOwnerWhenSet(t *testing.T) {
	db := ownerTestDB(t)
	plantAnomalyWithTeam(t, db, "A-team-owned", "ml-platform")
	plantTaskForAnomaly(t, db, "A-team-owned", "t.langley") // the analyst's own owner, should lose

	if _, err := db.Exec(`INSERT INTO teams(name, owner) VALUES('ml-platform', 'y.mercer')`); err != nil {
		t.Fatal(err)
	}

	owner, reason := crew.OwnerOfAnomaly(db, "A-team-owned")
	if owner != "y.mercer" {
		t.Errorf("owner = %q, want the team's own y.mercer, not the analyst's t.langley", owner)
	}
	if strings.TrimSpace(reason) == "" {
		t.Error("no reason was given for the owner lookup")
	}
	if !strings.Contains(reason, "ml-platform") {
		t.Errorf("reason %q does not say which team decided it", reason)
	}
}

func TestOwnerOfAnomalyFallsBackToTheAnalystsOwner(t *testing.T) {
	db := ownerTestDB(t)
	plantAnomalyWithTeam(t, db, "A-analyst-owned", "data-eng")
	plantTaskForAnomaly(t, db, "A-analyst-owned", "j.calder")
	// No row in teams at all for data-eng: the table exists (schema always
	// does) and simply has nothing to say about this team.

	owner, reason := crew.OwnerOfAnomaly(db, "A-analyst-owned")
	if owner != "j.calder" {
		t.Errorf("owner = %q, want the analyst's own owner j.calder", owner)
	}
	if strings.TrimSpace(reason) == "" {
		t.Error("no reason was given for the owner lookup")
	}
}

// Boundary: a team with no owner and an analyst with none either.
func TestOwnerOfAnomalyIsUnclaimedWithNeitherOwner(t *testing.T) {
	db := ownerTestDB(t)
	plantAnomalyWithTeam(t, db, "A-nobody-owned", "growth")
	plantTaskForAnomaly(t, db, "A-nobody-owned", "") // the task itself carries no owner

	owner, reason := crew.OwnerOfAnomaly(db, "A-nobody-owned")
	if owner != "unclaimed" {
		t.Errorf("owner = %q, want unclaimed", owner)
	}
	if strings.TrimSpace(reason) == "" {
		t.Error("unclaimed still needs a reason, so a reader knows both paths were tried")
	}
}

// A team's own name may explicitly have NO owner recorded at all (a row in
// teams naming the team but no owner column), which must fall back exactly
// like a team never mentioned in teams at all.
func TestOwnerOfAnomalyFallsBackWhenTheTeamRowHasNoOwner(t *testing.T) {
	db := ownerTestDB(t)
	plantAnomalyWithTeam(t, db, "A-empty-team-owner", "security")
	plantTaskForAnomaly(t, db, "A-empty-team-owner", "j.ashby")
	if _, err := db.Exec(`INSERT INTO teams(name, owner) VALUES('security', '')`); err != nil {
		t.Fatal(err)
	}

	owner, _ := crew.OwnerOfAnomaly(db, "A-empty-team-owner")
	if owner != "j.ashby" {
		t.Errorf("owner = %q, want the analyst's own owner j.ashby", owner)
	}
}

// Hostile: an owner name carrying a quote must survive the round trip
// through a parameterised query untouched -- proving this is not string
// concatenation wearing SQL syntax.
func TestOwnerOfAnomalyHandlesAQuoteInTheOwnersName(t *testing.T) {
	db := ownerTestDB(t)
	const quoted = `o'brien-owns-this`
	plantAnomalyWithTeam(t, db, "A-quoted-owner", "ml-platform")
	if _, err := db.Exec(`INSERT INTO teams(name, owner) VALUES('ml-platform', ?)`, quoted); err != nil {
		t.Fatal(err)
	}

	owner, _ := crew.OwnerOfAnomaly(db, "A-quoted-owner")
	if owner != quoted {
		t.Errorf("owner = %q, want %q untouched", owner, quoted)
	}

	// The table survived: a broken query would have refused the insert or
	// corrupted the row rather than quietly losing the quote.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM teams WHERE name='ml-platform'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("teams has %d rows named ml-platform, want 1", n)
	}
}

// An anomaly nobody has ever opened a task for (unassigned, no team row
// either) still gets an honest answer rather than an error a caller has to
// remember to check.
func TestOwnerOfAnomalyWithNoTaskAtAllIsUnclaimed(t *testing.T) {
	db := ownerTestDB(t)
	plantAnomalyWithTeam(t, db, "A-no-task", "research")

	owner, reason := crew.OwnerOfAnomaly(db, "A-no-task")
	if owner != "unclaimed" {
		t.Errorf("owner = %q, want unclaimed", owner)
	}
	if strings.TrimSpace(reason) == "" {
		t.Error("unclaimed still needs a reason")
	}
}

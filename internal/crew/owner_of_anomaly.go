package crew

// The anomaly desk's owner lookup. C1-SPEC.md section 2: an anomaly's team
// maps to an owner through the same tasks.owner chain B3 already reads for
// exactly this question from an OPTION's own side
// (internal/finops.ownerOfOption, internal/deliver.waitingOwner) -- the
// analyst's owner, stamped once at task creation (FromAnomaly's own
// OwnerOf(db, assignee)) and never silently re-derived from who owns the
// analyst today, per invariant 6 -- and, when the team has a named owner in
// teams, that person instead.

import (
	"database/sql"
	"fmt"
	"strings"
)

// TeamSchema is the team registry OwnerOfAnomaly reads. No table by this
// name existed anywhere in this console before this file: most teams have
// no row here at all, and that is a fine default, because the analyst's own
// owner already covers the question.
const TeamSchema = `CREATE TABLE IF NOT EXISTS teams(name TEXT PRIMARY KEY);`

// EnsureTeamOwner adds teams.owner, the way EnsureArtifactProvenance adds
// artifacts.source: read the schema, and migrate the column in rather than
// assume it, so a teams table some other change lands without this column
// still gains it here instead of refusing every query that names it.
func EnsureTeamOwner(db *sql.DB) error {
	if _, err := db.Exec(TeamSchema); err != nil {
		return fmt.Errorf("team schema: %w", err)
	}
	if _, err := db.Exec("ALTER TABLE teams ADD COLUMN owner TEXT"); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("adding teams.owner: %w", err)
	}
	return nil
}

// OwnerOfAnomaly is who to tell about one anomaly: the team's own named
// owner in teams when it has one, and the analyst's owner otherwise -- read
// off tasks.owner of the task this anomaly opened, never re-derived from
// the roster, the same chain finops.ownerOfOption and deliver.waitingOwner
// already read for the same question from an option's own side. reason
// says which of the two decided it, and "unclaimed" (SeededOwner's own
// word, owners.go) is the answer, with a reason, when neither has one --
// never an error a caller has to remember to check, because a page or an
// event still has to render something for a queue that never stops moving.
//
// Every value here travels through a parameterised query. A team or owner
// name carrying a quote is data, never SQL, all the way through.
func OwnerOfAnomaly(db *sql.DB, anomalyID string) (owner, reason string) {
	var team string
	_ = db.QueryRow(`SELECT COALESCE(team,'') FROM anomalies WHERE id=?`, anomalyID).Scan(&team)

	if team != "" {
		var teamOwner string
		_ = db.QueryRow(`SELECT COALESCE(owner,'') FROM teams WHERE name=?`, team).Scan(&teamOwner)
		if teamOwner != "" {
			return teamOwner, fmt.Sprintf("%s has a named team owner", team)
		}
	}

	var taskOwner string
	_ = db.QueryRow(`SELECT COALESCE(owner,'') FROM tasks WHERE anomaly=?`, anomalyID).Scan(&taskOwner)
	if taskOwner != "" {
		return taskOwner, "the analyst's own owner, from the task this anomaly opened"
	}

	if team == "" {
		return "unclaimed", "this anomaly names no team, and its own task has no owner either"
	}
	return "unclaimed", fmt.Sprintf(
		"%s has no named team owner, and its own task has no owner either", team)
}

package crew

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// EnsureOwnershipHistory records, on the charge itself, who answered for the
// agent that ran it.
//
// Before this, tasks carried only the assignee. Everything about ownership was
// then read from the agent's CURRENT owner, which made an agent that changed
// hands rewrite history: the new owner's lifetime figure jumped by an amount
// they had never authorised, and the previous owner's dropped by the same, so
// "what has this person spent" had no stable answer. The console could not say
// who owned an agent in July, and no wording on the page could fix it, because
// the fact was never written down.
//
// The column is stamped when the charge is made and is not rewritten
// afterwards, which is what makes it history rather than a second copy of the
// same mutable field. A transfer moves it on OPEN work only, matching the desk
// and matching what a transfer means: the new owner takes on what is running,
// not what is finished.
func EnsureOwnershipHistory(db *sql.DB) error {
	if _, err := db.Exec("ALTER TABLE tasks ADD COLUMN owner TEXT"); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("adding tasks.owner: %w", err)
	}
	// Backfill from the roster, once, for work charged before the column
	// existed. This is the one moment where current ownership is the best
	// available answer about the past, and it is recorded as such rather than
	// recomputed on every read: from here the two can legitimately differ, and
	// the difference is the history.
	if _, err := db.Exec(`
		UPDATE tasks
		   SET owner = COALESCE(
		       (SELECT a.owner FROM analysts a WHERE a.name = tasks.assignee), '')
		 WHERE owner IS NULL`); err != nil {
		return fmt.Errorf("backfilling tasks.owner: %w", err)
	}
	if _, err := db.Exec(
		`CREATE INDEX IF NOT EXISTS tasks_owner ON tasks(owner)`); err != nil {
		return fmt.Errorf("indexing tasks.owner: %w", err)
	}
	return nil
}

// SpendByOwner is what each person answers for, by the owner recorded on the
// charge rather than by who owns the agent today.
//
// period is a month like "2026-07", or "" for everything since the board
// opened.
func SpendByOwner(db *sql.DB, period string) (map[string]money.Cents, error) {
	q := `SELECT COALESCE(t.owner,''), COALESCE(SUM(t.spent_cents),0)
	        FROM tasks t`
	args := []any{}
	if period != "" {
		q += ` JOIN sprints s ON s.id = t.sprint WHERE substr(s.start,1,7) = ?`
		args = append(args, period)
	}
	q += ` GROUP BY 1`

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]money.Cents{}
	for rows.Next() {
		var who string
		var v int64
		if err := rows.Scan(&who, &v); err != nil {
			return nil, err
		}
		out[who] = money.Cents(v)
	}
	return out, rows.Err()
}

// OwnerOf is the owner to stamp on a charge for this agent right now.
//
// Read at the moment of the charge, not passed in by the caller, because a
// caller holding a stale Analyst would stamp a stale owner and the history
// would record a transfer that had already happened as though it had not.
func OwnerOf(db *sql.DB, agent string) string {
	var owner string
	_ = db.QueryRow(`SELECT COALESCE(owner,'') FROM analysts WHERE name=?`,
		agent).Scan(&owner)
	return owner
}

// ownerAt is OwnerOf inside a transaction, for the paths that build the board
// and the roster together and cannot read outside their own writes.
func ownerAt(tx *sql.Tx, agent string) string {
	var owner string
	_ = tx.QueryRow(`SELECT COALESCE(owner,'') FROM analysts WHERE name=?`,
		agent).Scan(&owner)
	return owner
}

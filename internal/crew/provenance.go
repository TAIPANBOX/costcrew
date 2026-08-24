package crew

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// A deliverable says whether a person's money bought it.
//
// The estate ships 279 generated drafts so that a new installation has
// something to review. A live run adds real ones, written by a real model on a
// real key, and the seeded and the real land in the same table, with the same
// author, the same state and the same shape.
//
// Two kinds of number under one heading is the exact fault this console spends
// its time catching in other people's data, and it does not get an exemption
// for its own. `docs/live-agents.md` named this before any of it was built:
//
//	a live run does not write into the seeded estate. It writes its own rows,
//	marked, and every page that sums them says which kind it is summing.
//
// The runner was built without the marker anyway, and 63 real drafts sat
// indistinguishable among 342 for one run. This is that marker.
//
// 'fixture' is the default for the same reason the column is NOT NULL: every
// row that predates this column was generated, and a row whose provenance is
// unknown must not read as evidence of a real call.
func EnsureArtifactProvenance(db *sql.DB) error {
	_, err := db.Exec(
		"ALTER TABLE artifacts ADD COLUMN source TEXT NOT NULL DEFAULT 'fixture'")
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("marking artifact provenance: %w", err)
	}
	return nil
}

// The ledger must not overstate what was spent.
//
// tasks.spent_cents is the ledger's unit and stays so. The trouble is that one
// model call costs a FRACTION of a cent, and rounding each one up on its own
// turns 44 calls of about half a cent into 44 whole cents.
//
// @measured, a full run on 2026-08-24: the router billed 0.2337 and the console
// recorded 0.56. Overstated by 140%, on the page whose heading is what the crew
// cost. A console that exists to catch exactly this in somebody else's data does
// not get to do it in its own.
//
// So the true amount accumulates here in micro-dollars and the cents are worked
// out ONCE, over the whole run, in SettleLiveSpend.
//
// Rounding per TASK was the first attempt and it fixed nothing, because the
// runner makes one call per task: 44 tasks each rounded their own half-cent up
// and the total was 0.44 again. The test that passed used 44 calls on a single
// task, which is not how anything works. A test can only prove what it
// describes.
func EnsureLiveSpendLedger(db *sql.DB) error {
	for _, col := range []string{
		// The truth, in millionths of a dollar.
		"live_micros INTEGER NOT NULL DEFAULT 0",
		// What has already been booked into spent_cents for this task, so the
		// settle pass is idempotent and stays right across several runs.
		"live_cents INTEGER NOT NULL DEFAULT 0",
	} {
		if _, err := db.Exec("ALTER TABLE tasks ADD COLUMN " + col); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("adding the live spend ledger: %w", err)
		}
	}
	return nil
}

// SettleLiveSpend turns the run's true cost into whole cents, once.
//
// Cents cannot hold a fifth of a cent, and one model call costs about that.
// Rounding each call up recorded 0.56 for a run that billed 0.2337; rounding
// each TASK up recorded the same, because there is one call per task. The only
// unit where the arithmetic can be right is the whole run.
//
// So: the run's exact total is rounded up ONCE, and those cents are handed out
// by largest remainder. A task that cost 0.4 of a cent may end up showing
// nothing, and the task beside it shows a whole cent; what is guaranteed is
// that the column adds up to what was actually billed, which is the figure a
// person reads.
//
// Idempotent, and correct across several runs: live_cents records what has
// already been booked, so this only ever writes the difference.
func SettleLiveSpend(db *sql.DB) (booked money.Cents, err error) {
	type row struct {
		id           int
		micros       int64
		alreadyCents int64
	}
	var rows []row
	rs, err := db.Query(
		`SELECT id, live_micros, live_cents FROM tasks WHERE live_micros > 0 ORDER BY id`)
	if err != nil {
		return 0, err
	}
	defer rs.Close()
	var total int64
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.id, &r.micros, &r.alreadyCents); err != nil {
			return 0, err
		}
		total += r.micros
		rows = append(rows, r)
	}
	if err := rs.Err(); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}

	// Up, never down: a run that cost money must not record less than it cost.
	want := (total + 9_999) / 10_000

	// Every task gets its whole cents; the leftover goes to the largest
	// remainders. Sorting by remainder and then by id keeps it deterministic,
	// which matters because the same estate must render the same way twice.
	give := make(map[int]int64, len(rows))
	var handed int64
	type rem struct {
		id int
		r  int64
	}
	rems := make([]rem, 0, len(rows))
	for _, r := range rows {
		whole := r.micros / 10_000
		give[r.id] = whole
		handed += whole
		rems = append(rems, rem{r.id, r.micros % 10_000})
	}
	sort.Slice(rems, func(i, j int) bool {
		if rems[i].r != rems[j].r {
			return rems[i].r > rems[j].r
		}
		return rems[i].id < rems[j].id
	})
	for i := 0; handed < want && i < len(rems); i++ {
		give[rems[i].id]++
		handed++
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, r := range rows {
		delta := give[r.id] - r.alreadyCents
		if delta == 0 {
			continue
		}
		if _, err := tx.Exec(`UPDATE tasks
			SET spent_cents = spent_cents + ?, live_cents = ?, updated = datetime('now')
			WHERE id = ?`, delta, give[r.id], r.id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return money.Cents(handed), nil
}

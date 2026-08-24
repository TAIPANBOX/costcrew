package crew

import (
	"database/sql"
	"fmt"
	"strings"
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
// So the true amount accumulates here in micro-dollars, and spent_cents follows
// the rounding of the TOTAL rather than the sum of the roundings. Rounding the
// total up keeps the old property that a call which cost something never
// records nothing, and costs at most one cent across a whole run instead of one
// cent per call.
func EnsureLiveSpendLedger(db *sql.DB) error {
	_, err := db.Exec(
		"ALTER TABLE tasks ADD COLUMN live_micros INTEGER NOT NULL DEFAULT 0")
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("adding the live spend ledger: %w", err)
	}
	return nil
}

package crew

// The desk halt: C9-SPEC.md section 2, "data.halt applied". A data-quality
// finding hands up data.halt naming a desk and a reason; the supervisor
// decides it alone (roles.yaml's own decides_alone list already carries the
// class), and applying it suspends every active analyst on that desk.
//
// Modelled on cadence_settings.go's own choice of a small dedicated table
// rather than reusing analysts.state/.reason alone: a halt has to answer
// three questions no per-analyst row can hold. Which DESK (a halt is one
// thing, not N separate suspensions that happen to share a reason). SINCE
// WHEN (T.stale_days, the supervisor's own hands_to_owner_conditions, is
// measured from when the crew actually stopped, never from any one
// analyst's own suspension date, and never reset by a second report of the
// same still-open problem -- see ApplyHalt). And WHOSE decision request a
// stale halt is carried to, once it has lasted that long (the owner of the
// task whose deliverable applied it, the same lookup finops.Supervise
// already uses for an ordinary carried option).
//
// `suspended` on the row is the exact list ApplyHalt suspended, not "every
// analyst this desk currently shows as suspended": a desk can carry an
// analyst suspended for an unrelated, older reason (migration-watch, kept
// suspended on the aws desk since its own migration finished), and LiftHalt
// must reactivate only the ones THIS halt put down, never one it never
// touched.

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/world"
)

// DeskHalt is one desk's own record of why its crew stopped.
type DeskHalt struct {
	Desk      string
	Reason    string
	Started   string // "2006-01-02", the day the FIRST data.halt on this desk applied
	AppliedBy string // who (which analyst, "supervisor" today) applied it
	Owner     string // whose decision request a stale halt is carried to
	Suspended []string
}

const HaltSchema = `
CREATE TABLE IF NOT EXISTS desk_halts(
  desk TEXT PRIMARY KEY, reason TEXT NOT NULL, started TEXT NOT NULL,
  applied_by TEXT NOT NULL, owner TEXT NOT NULL, suspended TEXT NOT NULL DEFAULT '');
`

func ensureHalts(db *sql.DB) error {
	_, err := db.Exec(HaltSchema)
	return err
}

// ActiveHalt reads the halt on one desk, if any.
func ActiveHalt(db *sql.DB, desk string) (DeskHalt, bool, error) {
	if err := ensureHalts(db); err != nil {
		return DeskHalt{}, false, err
	}
	var h DeskHalt
	var suspended string
	err := db.QueryRow(`SELECT desk, reason, started, applied_by, owner, suspended
		FROM desk_halts WHERE desk=?`, desk).
		Scan(&h.Desk, &h.Reason, &h.Started, &h.AppliedBy, &h.Owner, &suspended)
	if err == sql.ErrNoRows {
		return DeskHalt{}, false, nil
	}
	if err != nil {
		return DeskHalt{}, false, err
	}
	h.Suspended = splitList(suspended)
	return h, true, nil
}

// Halts lists every desk currently halted, by desk name.
func Halts(db *sql.DB) ([]DeskHalt, error) {
	if err := ensureHalts(db); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT desk, reason, started, applied_by, owner, suspended
		FROM desk_halts ORDER BY desk`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeskHalt
	for rows.Next() {
		var h DeskHalt
		var suspended string
		if err := rows.Scan(&h.Desk, &h.Reason, &h.Started, &h.AppliedBy, &h.Owner, &suspended); err != nil {
			return nil, err
		}
		h.Suspended = splitList(suspended)
		out = append(out, h)
	}
	return out, rows.Err()
}

// ApplyHalt suspends every ACTIVE analyst on desk, with reason, and records
// the halt with today as its start day. desk travels as plain data through a
// parameterised query throughout, same as every other write in this
// package: a source name carrying a quote is just a string to suspend
// nobody under (C9-SPEC.md section 4's hostile case).
//
// A desk already halted is a no-op: nothing is suspended twice, nothing new
// is journaled, and the ORIGINAL started day and reason stay exactly as
// they were (C9-SPEC.md section 4's own boundary, "a second halt on a
// halted desk (no double suspension)"). This is deliberate and not merely
// convenient: T.stale_days counts days since the crew actually stopped, and
// a start date that reset every time the same still-open problem got
// re-reported would mean an ongoing halt could never reach it.
func ApplyHalt(db *sql.DB, desk, reason, appliedBy, owner, today string, rec Recorder) (suspended []string, already bool, err error) {
	desk = strings.TrimSpace(desk)
	if desk == "" {
		return nil, false, fmt.Errorf("data.halt names no desk")
	}
	if strings.TrimSpace(reason) == "" {
		return nil, false, ErrNeedReason
	}
	if err := ensureHalts(db); err != nil {
		return nil, false, err
	}
	if _, has, err := ActiveHalt(db, desk); err != nil {
		return nil, false, err
	} else if has {
		return nil, true, nil
	}

	roster, err := Roster(db)
	if err != nil {
		return nil, false, err
	}
	for _, a := range roster {
		if a.Desk != desk || a.State != "active" {
			continue
		}
		if err := SetState(db, a.Name, string(world.Suspended), reason); err != nil {
			return suspended, false, err
		}
		suspended = append(suspended, a.Name)
		if rec != nil {
			// The event set is exactly what this state change already
			// carries elsewhere (internal/web/roster.go's setAnalystState):
			// C9-SPEC.md section 3, "no new wire type -- agent_state_changed
			// already carries a suspension".
			_ = rec.Emit("agent_state_changed", a.Name, "high", map[string]any{
				"analyst": a.Name, "state": "suspended", "reason": reason, "by": appliedBy,
			}, nil)
		}
	}
	if _, err := db.Exec(`INSERT INTO desk_halts(desk, reason, started, applied_by, owner, suspended)
		VALUES (?,?,?,?,?,?)`, desk, reason, today, appliedBy, owner, strings.Join(suspended, ",")); err != nil {
		return suspended, false, err
	}
	return suspended, false, nil
}

// LiftHalt returns every analyst THIS halt suspended to active, with reason
// journaled, and clears the halt. It insists on a reason for the same
// argument crew.Return and MarkOptionRefused already make: a reversal with
// no reason is indistinguishable from nobody having checked.
//
// Only the names this halt itself suspended (DeskHalt.Suspended), never
// "every analyst this desk currently shows as suspended": a desk can carry
// an analyst suspended for an older, unrelated reason, and lifting a halt
// must not silently reactivate it.
func LiftHalt(db *sql.DB, desk, reason, liftedBy string, rec Recorder) (reactivated []string, err error) {
	if strings.TrimSpace(reason) == "" {
		return nil, ErrNeedReason
	}
	h, has, err := ActiveHalt(db, desk)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, fmt.Errorf("%s is not halted", desk)
	}
	for _, name := range h.Suspended {
		if err := SetState(db, name, "active", ""); err != nil {
			return reactivated, err
		}
		reactivated = append(reactivated, name)
		if rec != nil {
			_ = rec.Emit("agent_state_changed", name, "info", map[string]any{
				"analyst": name, "state": "active", "reason": reason, "by": liftedBy,
			}, nil)
		}
	}
	if _, err := db.Exec(`DELETE FROM desk_halts WHERE desk=?`, desk); err != nil {
		return reactivated, err
	}
	return reactivated, nil
}

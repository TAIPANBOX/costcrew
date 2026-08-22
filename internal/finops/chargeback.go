package finops

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// Period is a month's chargeback: open while the numbers can still move,
// closed once somebody has frozen them.
//
// Closing is the point of the whole thing. An allocation that is recomputed
// every time somebody opens the page cannot be charged to anybody, because the
// number a team was told in March is not the number the page shows in April,
// and the team is right to refuse it.
type Period struct {
	Label    string
	Closed   bool
	FrozenAt string
	ClosedBy string
	Teams    []Frozen
	Total    money.Cents
}

type Frozen struct {
	Source    string
	Team      string
	Direct    money.Cents
	Allocated money.Cents
}

func (f Frozen) Loaded() money.Cents { return f.Direct + f.Allocated }

// Close freezes a month. The amounts are written down as they are at this
// moment and never recomputed, which is what makes them chargeable.
func Close(db *sql.DB, period, by string) error {
	if closed, err := IsClosed(db, period); err != nil {
		return err
	} else if closed {
		return fmt.Errorf("%s is already closed", period)
	}
	a, err := Allocate(db, period)
	if err != nil {
		return err
	}
	if len(a.Teams) == 0 {
		return fmt.Errorf("%s has nothing to charge", period)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, t := range a.Teams {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO chargeback
			(period, source, team, direct_cents, allocated_cents, frozen_at, closed_by)
			VALUES (?,?,?,?,?,?,?)`,
			period, t.Source, t.Team, int64(t.Direct), int64(t.Allocated), now, by); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Reopen unfreezes a month.
//
// It needs a reason for the same argument every other reversal here does: a
// team was told a number, and taking it back without saying why is how a
// chargeback stops being believed. The reason goes in the journal through the
// caller, not silently into the row.
func Reopen(db *sql.DB, period, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("reopening a closed period needs a reason: a team was " +
			"already told these numbers")
	}
	res, err := db.Exec(`DELETE FROM chargeback WHERE period=?`, period)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%s was not closed", period)
	}
	return nil
}

func IsClosed(db *sql.DB, period string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM chargeback WHERE period=?`, period).Scan(&n)
	return n > 0, err
}

// Frozen reads a closed period exactly as it was written.
func FrozenPeriod(db *sql.DB, period string) (Period, error) {
	p := Period{Label: period}
	rows, err := db.Query(`SELECT source, team, direct_cents, allocated_cents,
		COALESCE(frozen_at,''), COALESCE(closed_by,'')
		FROM chargeback WHERE period=? ORDER BY source, team`, period)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var f Frozen
		var d, a int64
		if err := rows.Scan(&f.Source, &f.Team, &d, &a, &p.FrozenAt, &p.ClosedBy); err != nil {
			return p, err
		}
		f.Direct, f.Allocated = money.Cents(d), money.Cents(a)
		p.Teams = append(p.Teams, f)
		p.Total += f.Loaded()
		p.Closed = true
	}
	return p, rows.Err()
}

// TrueUp compares what a team was charged with what the same month costs now.
//
// A closed month can still move: a provider issues a credit, a late charge
// lands, a tag is corrected. Pretending otherwise is how a chargeback quietly
// stops matching the invoice. This says the difference out loud instead.
type TrueUp struct {
	Source string
	Team   string
	Frozen money.Cents
	Now    money.Cents
	Delta  money.Cents
}

func TrueUpFor(db *sql.DB, period string) ([]TrueUp, money.Cents, error) {
	frozen, err := FrozenPeriod(db, period)
	if err != nil {
		return nil, 0, err
	}
	if !frozen.Closed {
		return nil, 0, nil
	}
	live, err := Allocate(db, period)
	if err != nil {
		return nil, 0, err
	}
	now := map[string]money.Cents{}
	for _, t := range live.Teams {
		now[t.Source+"|"+t.Team] = t.Loaded()
	}
	var out []TrueUp
	var total money.Cents
	for _, f := range frozen.Teams {
		n := now[f.Source+"|"+f.Team]
		d := n - f.Loaded()
		if d == 0 {
			continue
		}
		out = append(out, TrueUp{f.Source, f.Team, f.Loaded(), n, d})
		total += d
	}
	return out, total, nil
}

// ClosedPeriods lists what has been frozen, newest first.
func ClosedPeriods(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT period FROM chargeback ORDER BY period DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

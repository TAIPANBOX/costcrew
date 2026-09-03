package finops

// Licences reads the seat estate the saas-seats connector has actually
// imported (internal/connectors), the same shape internal/finops/ai.go
// already holds for the AI desk: real rows read back into the SAME type the
// GENERATED estate uses (world.Licence, world.AIUnit), so a page or a
// packet section does not need to know which source produced a row, only
// whether AIUnits/Licences returned anything at all.

import (
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// Licences is every imported seat line, per vendor and per product, read
// from the licences table (internal/connectors.EnsureLicenceSchema). Empty,
// not an error, on a store nothing has ever been imported into: the table
// still exists (every start ensures it, the same as ai_calls), it is just
// empty, and empty is exactly what tells the SaaS page and the renewals
// packet to fall back to the generated fixture.
//
// Sorted by vendor then product, not the order SQLite happens to return:
// invariant 7 (CLAUDE.md), the same reason AIUnits sorts its own keys rather
// than trusting map order -- a licences table has no such map in this
// function, but nothing about SQL's own row order is a promise either.
func Licences(db *sql.DB) ([]world.Licence, error) {
	rows, err := db.Query(`SELECT vendor, product, seats_issued, seats_active,
			active_window_days, per_seat_cents, renewal_date, term_months, notice_days
		FROM licences ORDER BY vendor, product`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []world.Licence
	for rows.Next() {
		var l world.Licence
		var perSeat int64
		if err := rows.Scan(&l.Vendor, &l.Product, &l.Issued, &l.Active30,
			&l.ActiveWindowDays, &perSeat, &l.Renews, &l.TermMonths, &l.NoticeDays); err != nil {
			return nil, err
		}
		l.PerSeat = money.Cents(perSeat)
		out = append(out, l)
	}
	return out, rows.Err()
}

// RenewalsWithin is the calendar question for imported licences: which of
// them are due to renew within `days` of `today` (an ISO "2006-01-02"
// date), each still carrying its own issued/active/idle/waste. The boundary
// is world.ExpiringWithin's own, restated here because that function reads
// world.Commitments and cannot be reused for a different slice: zero days
// out counts ("a renewal today"), exactly `days` out counts, one day past
// does not.
//
// today is a parameter, not time.Now() read inside this function, so the
// same call this desk's packet section makes at whatever real day it runs
// is also the call a test makes against a fixed date -- the discipline
// world.ExpiringWithin(days, from) already holds for the generated
// fixture's own dated commitments.
func RenewalsWithin(db *sql.DB, days int, today string) ([]world.Licence, error) {
	all, err := Licences(db)
	if err != nil {
		return nil, err
	}
	ref, err := time.Parse("2006-01-02", today)
	if err != nil {
		return nil, fmt.Errorf("today %q does not parse as a date: %w", today, err)
	}
	var out []world.Licence
	for _, l := range all {
		d, err := time.Parse("2006-01-02", l.Renews)
		if err != nil {
			// The reader that writes this table already refuses a row whose
			// RenewalDate does not parse, so this is unreached in practice;
			// skipped rather than erroring the whole calendar over one row
			// a future writer might get wrong.
			continue
		}
		delta := d.Sub(ref).Hours() / 24
		if delta >= 0 && delta <= float64(days) {
			out = append(out, l)
		}
	}
	// Already sorted by Licences' own ORDER BY; re-sorted here anyway
	// because a caller of this function alone, without reading Licences'
	// comment too, should not have to trust that it stays true.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Vendor != out[j].Vendor {
			return out[i].Vendor < out[j].Vendor
		}
		return out[i].Product < out[j].Product
	})
	return out, nil
}

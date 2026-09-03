package finops

// Real (connector-sourced) commitment coverage, utilisation, the expiry
// calendar and break-even -- C4-SPEC.md. world.Commitments, the generated
// waterline, stays the fallback: the same "real data first, the generated
// world only when the store has nothing" rule finops.AIUnits already gives
// the AI page over world.AIUnits.
//
// Every function here reads the commitments table
// internal/connectors/tokenfusefocus.go keeps (the FOCUS CommitmentDiscount*
// columns and ChargeCategory=Purchase rows, when a file carries them) and
// the charges table every reader shares. It ensures the commitments table
// exists on every call, the same defensive db.Exec(Schema) pattern
// connectors.go's own Load/Save/Test already use for the connections table,
// so a store nothing has ever imported into still answers "no real
// commitments" rather than erroring on a table that was never created.

import (
	"database/sql"
	"sort"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

func ensureCommitmentsTable(db *sql.DB) error {
	_, err := db.Exec(connectors.CommitmentSchema)
	return err
}

// RealCommitment is one row of the store's commitments table.
type RealCommitment struct {
	ID, Kind, Status, Unit, Source string
	Quantity                       float64
	MonthlyCents                   money.Cents
	Start, End                     string // "2006-01-02"
}

// HasRealCommitments says whether a connector has ever written a row here.
// The page and the packet section both use it to choose between the real
// reading below and world.Commitments, the generated fixture.
func HasRealCommitments(db *sql.DB) (bool, error) {
	if err := ensureCommitmentsTable(db); err != nil {
		return false, err
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM commitments`).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// RealCommitments reads every row, ordered the same way on every call:
// invariant 7 (TestPagesRenderTheSameTwice) applies here exactly as it does
// to every other plane this console renders.
func RealCommitments(db *sql.DB) ([]RealCommitment, error) {
	if err := ensureCommitmentsTable(db); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, kind, status, quantity, COALESCE(unit,''), source,
		date_start, date_end, monthly_cents FROM commitments ORDER BY source, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RealCommitment
	for rows.Next() {
		var c RealCommitment
		var cents int64
		if err := rows.Scan(&c.ID, &c.Kind, &c.Status, &c.Quantity, &c.Unit, &c.Source,
			&c.Start, &c.End, &cents); err != nil {
			return nil, err
		}
		c.MonthlyCents = money.Cents(cents)
		out = append(out, c)
	}
	return out, rows.Err()
}

// eligibleCents is what a commitment on this desk could cover this month:
// compute, database and accelerator charges for a cloud desk (the same
// resourceKind classification world.buildCommitments already applies to the
// generated waterline, via world.ResourceKind), or every usage charge for
// the ai desk. @claude 2026-09-03: an LLM API call is not a compute,
// database or accelerator RESOURCE in world.ResourceKind's own sense --
// that classification exists for rightsizing a VM or a database, and every
// service name this reader's own deriveCharges writes ("Anthropic API",
// "OpenRouter API") matches none of its patterns -- but a committed-spend
// or committed-throughput agreement with a model provider can legitimately
// cover any of the desk's usage, so "what kind of resource" is not the
// question there the way it is for a cloud desk.
func eligibleCents(db *sql.DB, source, month string) (money.Cents, error) {
	if source == "ai" {
		var cents int64
		err := db.QueryRow(`SELECT COALESCE(SUM(billed_cents),0) FROM charges
			WHERE source=? AND category='Usage' AND substr(day,1,7)=?`, source, month).Scan(&cents)
		return money.Cents(cents), err
	}
	rows, err := db.Query(`SELECT service, SUM(billed_cents) FROM charges
		WHERE source=? AND category='Usage' AND substr(day,1,7)=? GROUP BY service`, source, month)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var total int64
	for rows.Next() {
		var service string
		var cents int64
		if err := rows.Scan(&service, &cents); err != nil {
			return 0, err
		}
		if world.ResourceKind(service) != "" {
			total += cents
		}
	}
	return money.Cents(total), rows.Err()
}

// CoverageRow is one desk's committed spend against what it could cover,
// for one month.
type CoverageRow struct {
	Source         string
	CommittedCents money.Cents
	EligibleCents  money.Cents
	Pct            float64
	OK             bool // false: no eligible spend to be a percentage of
}

// Coverage is committed spend over eligible spend, per desk, for the
// commitments active during month: a commitment whose own window
// (date_start, date_end) does not reach into the month is not counted,
// so an expired commitment cannot inflate a month it no longer covers.
//
// One desk at a time, in Source order, so the result renders the same way
// on every call.
func Coverage(db *sql.DB, month string) ([]CoverageRow, error) {
	if err := ensureCommitmentsTable(db); err != nil {
		return nil, err
	}
	start, end := monthBounds(month)
	rows, err := db.Query(`SELECT source, COALESCE(SUM(monthly_cents),0) FROM commitments
		WHERE date_start <= ? AND date_end >= ? GROUP BY source ORDER BY source`, end, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CoverageRow
	for rows.Next() {
		var r CoverageRow
		var cents int64
		if err := rows.Scan(&r.Source, &cents); err != nil {
			return nil, err
		}
		r.CommittedCents = money.Cents(cents)
		elig, err := eligibleCents(db, r.Source, month)
		if err != nil {
			return nil, err
		}
		r.EligibleCents = elig
		// money.Pct on the two whole-cent integers directly: never through a
		// dollars-first, coarser-rounded intermediate. Mutant (a),
		// TestCoverageDoesNotRoundThroughDollarsFirst.
		r.Pct, r.OK = money.Pct(r.CommittedCents, r.EligibleCents)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// UtilisationRow is one commitment's own price against what the desk's
// real eligible spend justifies paying for it.
type UtilisationRow struct {
	RealCommitment
	Pct float64
	OK  bool // false: the commitment's own price is zero, nothing to divide by
}

// CommitmentUtilisation is used over committed, per commitment: the desk's
// eligible spend this month (the same figure Coverage divides by) over the
// commitment's own monthly price. Over 100% is a real, healthy answer -- the
// commitment costs less than what it is covering -- the same reading
// world.Commitment.BelowWaterline() already gives the generated side, just
// derived here rather than randomised.
//
// This does not apportion eligible spend across several commitments on the
// SAME desk: each is measured against the whole desk's eligible spend
// independently, which overstates a desk's utilisation when it holds more
// than one commitment. Named here rather than hidden, per this repository's
// own convention that a page (or a computation) states its own limits.
func CommitmentUtilisation(db *sql.DB, month string) ([]UtilisationRow, error) {
	cs, err := RealCommitments(db)
	if err != nil {
		return nil, err
	}
	out := make([]UtilisationRow, 0, len(cs))
	for _, c := range cs {
		elig, err := eligibleCents(db, c.Source, month)
		if err != nil {
			return nil, err
		}
		pct, ok := money.Pct(elig, c.MonthlyCents)
		out = append(out, UtilisationRow{RealCommitment: c, Pct: pct, OK: ok})
	}
	return out, nil
}

// ExpiringCommitments is the calendar: every real commitment whose date_end
// falls within days of from, inclusive of from itself (an expiry today is
// within 90 days of today), the same <= comparison world.ExpiringWithin
// already holds for the generated side. Soonest first.
func ExpiringCommitments(db *sql.DB, days int, from string) ([]RealCommitment, error) {
	cs, err := RealCommitments(db)
	if err != nil {
		return nil, err
	}
	ref, err := time.Parse("2006-01-02", from)
	if err != nil {
		return nil, nil
	}
	type dated struct {
		c    RealCommitment
		days float64
	}
	var out []dated
	for _, c := range cs {
		e, err := time.Parse("2006-01-02", c.End)
		if err != nil {
			continue
		}
		d := e.Sub(ref).Hours() / 24
		if d >= 0 && d <= float64(days) {
			out = append(out, dated{c, d})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].days < out[j].days })
	res := make([]RealCommitment, len(out))
	for i, d := range out {
		res[i] = d.c
	}
	return res, nil
}

// BreakEvenRow is one commitment's own buy-or-wait case: what it costs a
// month, what it would cost to keep running the same eligible spend
// on-demand, and how many months of that saving repay one month of the
// commitment's own price.
type BreakEvenRow struct {
	RealCommitment
	OnDemandCents      money.Cents
	MonthlySavingCents money.Cents // OnDemandCents - MonthlyCents; may be zero or negative
	Months             int         // ceil(MonthlyCents / MonthlySavingCents), whole cents throughout
	OK                 bool        // false: on-demand does not exceed the committed price, so this never breaks even
}

// BreakEvens ranks every real commitment by its own monthly saving,
// largest first: from the commitment's own price (MonthlyCents) and the
// on-demand run rate it would cover (the same eligibleCents Coverage and
// CommitmentUtilisation both read), never through a float division --
// Months is an exact integer ceiling over whole cents, the same "never
// round before summing, and never introduce a float where an integer
// already answers the question" discipline invariant 25 already holds
// for ai_calls.
func BreakEvens(db *sql.DB, month string) ([]BreakEvenRow, error) {
	cs, err := RealCommitments(db)
	if err != nil {
		return nil, err
	}
	out := make([]BreakEvenRow, 0, len(cs))
	for _, c := range cs {
		onDemand, err := eligibleCents(db, c.Source, month)
		if err != nil {
			return nil, err
		}
		saving := onDemand - c.MonthlyCents
		r := BreakEvenRow{RealCommitment: c, OnDemandCents: onDemand, MonthlySavingCents: saving}
		if saving > 0 {
			r.OK = true
			// Ceiling division on int64 cents: (a + b - 1) / b for positive
			// a, b. Exact; no float anywhere in this line.
			r.Months = int((int64(c.MonthlyCents) + int64(saving) - 1) / int64(saving))
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].MonthlySavingCents > out[j].MonthlySavingCents })
	return out, nil
}

// AsOfDay is the most recent day this store holds any charge for, real or
// generated. The packet's expiry calendar uses this as "today" rather than
// the wall clock, the same reason ExpiringWithin (world) takes an explicit
// from rather than reading time.Now() itself: a fixture's own relationship
// to the present is deliberately frozen (world.LastDay), and a connector's
// real import has its own "now" that a wall-clock read would drift away
// from between the day it was imported and the day this runs.
func AsOfDay(db *sql.DB) (string, error) {
	var day sql.NullString
	if err := db.QueryRow(`SELECT MAX(day) FROM charges`).Scan(&day); err != nil {
		return "", err
	}
	return day.String, nil
}

// Commitments is what the SaaS page's own Commitments panel renders: real
// rows, mapped into world.Commitment's own shape so the existing template
// and its sort comparators (internal/web/practice.go) need no change at
// all, when a connector has written any; world.Commitments, the generated
// waterline, otherwise. real says which one this call returned -- the same
// "real data first, the generated world only when the store has nothing"
// switch finops.AIUnits already gives the AI page over world.AIUnits.
//
// Hourly is MonthlyCents/730, the identical derivation
// world.buildCommitments already uses for the generated side, so Wasted()
// (money.Cents * 730 * idle share) reads back the same monthly figure this
// function started from, to the cent, when Used is 100.
func Commitments(db *sql.DB) (rows []world.Commitment, real bool, err error) {
	has, err := HasRealCommitments(db)
	if err != nil {
		return nil, false, err
	}
	if !has {
		return world.Commitments, false, nil
	}
	cs, err := RealCommitments(db)
	if err != nil {
		return nil, false, err
	}
	period, err := OpenPeriod(db)
	if err != nil {
		return nil, false, err
	}
	util := map[string]UtilisationRow{}
	if period != "" {
		if us, uerr := CommitmentUtilisation(db, period); uerr == nil {
			for _, u := range us {
				util[u.ID] = u
			}
		}
	}
	out := make([]world.Commitment, 0, len(cs))
	for _, c := range cs {
		name := c.ID
		if c.Kind != "" {
			name = c.Kind + " (" + c.ID + ")"
		}
		// Used is 0 (BelowWaterline reads that as below the line) when
		// utilisation could not be computed for this one commitment --
		// commitmentsSection (internal/deliver) is where that refusal is
		// actually SAID; this page has no "unknown" state for the column
		// and 0 is the direction that does not overclaim health.
		out = append(out, world.Commitment{
			Source: c.Source, Name: name, Kind: c.Kind,
			Hourly:  c.MonthlyCents / 730,
			Used:    util[c.ID].Pct,
			Expires: c.End, Term: c.Start + " to " + c.End,
			Note: "Read from a connector.",
		})
	}
	return out, true, nil
}

// monthBounds is a "2006-01" month's own first and last calendar day, as
// "2006-01-02" strings.
func monthBounds(month string) (start, end string) {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return month + "-01", month + "-31"
	}
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1)
	return first.Format("2006-01-02"), last.Format("2006-01-02")
}

package finops

import (
	"database/sql"
	"fmt"
	"sort"

	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// LatestRealAIMonth is the most recent month with real (provenance IS NOT
// NULL) AI charges, if any. The AI page uses it to decide which month to
// show once a connector has written real rows: unlike the generated world,
// whose "current" month is fixed at world.LastDay, real data lands on
// whatever month the imported file happens to cover, and a page that kept
// looking at the generated world's own last day would show nothing real
// simply because the file was for a different month.
func LatestRealAIMonth(db *sql.DB) (month string, ok bool, err error) {
	var day sql.NullString
	err = db.QueryRow(`SELECT MAX(day) FROM charges
		WHERE source='ai' AND unit='tokens' AND provenance IS NOT NULL`).Scan(&day)
	if err != nil {
		return "", false, err
	}
	if !day.Valid || len(day.String) < 7 {
		return "", false, nil
	}
	return day.String[:7], true, nil
}

// AIUnits reads the AI desk's economics for one month from the STORE, the
// way world.AIUnits() reads them from the generated series in memory. The AI
// page uses this once real rows exist and falls back to world.AIUnits()
// otherwise, so a fresh install still has something to show.
//
// hasOutcomes says which Actions this call returned: real counts of
// non-empty x_outcome values when any exist for the month, or the same fixed
// ratio world.AIUnits() uses when none do. It is a MONTH-WIDE decision, not
// a per-row one — mixing tagged and estimated Actions within the same
// column would print two different kinds of number with nothing to tell
// them apart, and the page says which one it printed.
func AIUnits(db *sql.DB, month string) (units []world.AIUnit, hasOutcomes bool, err error) {
	// provenance IS NOT NULL: without it this reads the GENERATED rows too,
	// since they are also source='ai' AND unit='tokens', and the AI page
	// would then say "read from a connector" on an install where nothing
	// was ever imported — found by the parity gate comparing a fresh
	// install against itself, where /ai differed only in that label despite
	// neither server ever having imported anything.
	rows, err := db.Query(`SELECT COALESCE(team,''), model,
			COALESCE(SUM(billed_cents),0), COALESCE(SUM(quantity),0)
		FROM charges WHERE source='ai' AND unit='tokens' AND provenance IS NOT NULL
			AND day LIKE ?
		GROUP BY COALESCE(team,''), model`, month+"%")
	if err != nil {
		return nil, false, err
	}
	type key struct{ team, model string }
	byKey := map[key]*world.AIUnit{}
	for rows.Next() {
		var team, model string
		var cents int64
		var qty float64
		if err := rows.Scan(&team, &model, &cents, &qty); err != nil {
			rows.Close()
			return nil, false, err
		}
		byKey[key{team, model}] = &world.AIUnit{
			Month: month, Team: team, Model: model,
			Cost: money.Cents(cents), Tokens: int64(qty),
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	rows.Close()

	var tagged int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_calls
		WHERE day LIKE ? AND blocked=0 AND outcome IS NOT NULL AND outcome<>''`,
		month+"%").Scan(&tagged); err != nil {
		return nil, false, err
	}
	hasOutcomes = tagged > 0

	if hasOutcomes {
		actRows, err := db.Query(`SELECT COALESCE(team,''), model, COUNT(*)
			FROM ai_calls WHERE day LIKE ? AND blocked=0
				AND outcome IS NOT NULL AND outcome<>''
			GROUP BY COALESCE(team,''), model`, month+"%")
		if err != nil {
			return nil, false, err
		}
		for actRows.Next() {
			var team, model string
			var n int
			if err := actRows.Scan(&team, &model, &n); err != nil {
				actRows.Close()
				return nil, false, err
			}
			if u, ok := byKey[key{team, model}]; ok {
				u.Actions = n
			}
		}
		if err := actRows.Err(); err != nil {
			actRows.Close()
			return nil, false, err
		}
		actRows.Close()
	} else {
		// The same fixed ratio world.AIUnits() uses, for the same reason:
		// there is nothing else to derive it from until an outcome is
		// tagged.
		for _, u := range byKey {
			u.Actions = int(u.Tokens / 4_800)
			u.Deflected = u.Actions * 38 / 100
		}
	}

	keys := make([]key, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	// Sorted, not map order: invariant 7 (TestAIUnitsAreOrderedTheSameEveryCall,
	// TestPagesRenderTheSameTwice) exists precisely because ranging a Go map
	// is a rotation from a random start, not a shuffle, and the store-backed
	// path must hold the same property the in-memory one does.
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].team != keys[j].team {
			return keys[i].team < keys[j].team
		}
		return keys[i].model < keys[j].model
	})
	units = make([]world.AIUnit, 0, len(keys))
	for _, k := range keys {
		units = append(units, *byKey[k])
	}
	return units, hasOutcomes, nil
}

// AgentAIRow is one agent's economics for the month, read from ai_calls
// rather than from charges: charges has no agent column, by design, because
// the daily ledger is where a team's spend is reconciled and an individual
// agent's calls are not.
type AgentAIRow struct {
	Agent        string
	Calls        int
	Tokens       int64
	Cost         money.Cents
	BlockedCalls int
}

// AIByAgent is the per-agent table section 6 asks for. BlockedCalls is a
// COUNT, not an amount: the FOCUS row for a blocked call carries BilledCost
// zero, because the guard refused before anything was spent, and the amount
// it RESERVED before refusing is not in the export. Printing a dollar figure
// for that would be inventing one; the count is what is actually known.
func AIByAgent(db *sql.DB, month string) ([]AgentAIRow, error) {
	rows, err := db.Query(`SELECT agent, COUNT(*),
			COALESCE(SUM(tokens_in+tokens_out),0), COALESCE(SUM(billed_cents),0),
			SUM(CASE WHEN blocked=1 THEN 1 ELSE 0 END)
		FROM ai_calls WHERE day LIKE ?
		GROUP BY agent ORDER BY 4 DESC, agent ASC`, month+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentAIRow
	for rows.Next() {
		var r AgentAIRow
		var cents int64
		if err := rows.Scan(&r.Agent, &r.Calls, &r.Tokens, &cents, &r.BlockedCalls); err != nil {
			return nil, err
		}
		r.Cost = money.Cents(cents)
		out = append(out, r)
	}
	return out, rows.Err()
}

// MixedMoneyNote is invariant 20 extended to charges: a figure that mixes
// generated and real money says so. Empty when it does not, which — with
// the tokenfuse-focus reader's own refusal 1 holding — is every store this
// step builds going forward; the one way to still see both is a store built
// BEFORE this step, with generated AI rows a connector was never pointed at
// replacing.
func MixedMoneyNote(db *sql.DB, source, month string) (string, error) {
	var genN, realN int
	var genCents, realCents int64
	err := db.QueryRow(`SELECT
			COALESCE(SUM(CASE WHEN provenance IS NULL THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN provenance IS NULL THEN billed_cents ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN provenance IS NOT NULL THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN provenance IS NOT NULL THEN billed_cents ELSE 0 END),0)
		FROM charges WHERE source=? AND day LIKE ?`, source, month+"%").
		Scan(&genN, &genCents, &realN, &realCents)
	if err != nil {
		return "", err
	}
	// Row COUNTS decide this, not the cents: a real row can legitimately sum
	// to zero cents (a call under half a cent rounds down) and still be a
	// real row that must not be hidden by a comparison that only looks at
	// the amount.
	if genN == 0 || realN == 0 {
		return "", nil
	}
	return fmt.Sprintf("This total mixes %s of real spend a connector wrote with %s still "+
		"carried from the generated estate: this store was built before this reader existed "+
		"and its generated AI rows were never replaced.",
		money.Cents(realCents), money.Cents(genCents)), nil
}

// AttributionCoverage is the share of real AI spend (provenance IS NOT NULL)
// this month with an agent attributed to it, and whether there was any real
// AI spend to measure at all. hasData is false on every store before a
// connector like tokenfuse-focus has run, which is when the KPI stays
// blocked rather than reporting a hollow 0% or a meaningless 100%.
func AttributionCoverage(db *sql.DB, month string) (pct float64, hasData bool, err error) {
	var total, attributed int64
	err = db.QueryRow(`
		SELECT COALESCE(SUM(c.billed_cents),0), COALESCE(SUM(CASE WHEN EXISTS(
				SELECT 1 FROM attribution a
				WHERE a.source = c.source AND a.team = COALESCE(c.team,'')
				  AND a.service = c.service AND c.day BETWEEN a.day_start AND a.day_end
			) THEN c.billed_cents ELSE 0 END),0)
		FROM charges c
		WHERE c.source='ai' AND c.provenance IS NOT NULL AND c.day LIKE ?`,
		month+"%").Scan(&total, &attributed)
	if err != nil {
		return 0, false, err
	}
	if total == 0 {
		return 0, false, nil
	}
	return float64(attributed) / float64(total) * 100, true, nil
}

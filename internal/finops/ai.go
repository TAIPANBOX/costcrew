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
//
// Cost is money.Micros, not money.Cents: this is a PER-AGENT figure, not a
// daily-ledger sum, and an agent whose calls this month are all a few tenths
// of a cent each must not print as $0.00. Micros.String() shows four
// decimals for exactly that case and two otherwise, so this prints the same
// way charges.billed_cents does once the amount clears a cent.
type AgentAIRow struct {
	Agent        string
	Calls        int
	Tokens       int64
	Cost         money.Micros
	BlockedCalls int
}

// AIByAgent is the per-agent table section 6 asks for. BlockedCalls is a
// COUNT, not an amount: the FOCUS row for a blocked call carries BilledCost
// zero, because the guard refused before anything was spent, and the amount
// it RESERVED before refusing is not in the export. Printing a dollar figure
// for that would be inventing one; the count is what is actually known.
func AIByAgent(db *sql.DB, month string) ([]AgentAIRow, error) {
	rows, err := db.Query(`SELECT agent, COUNT(*),
			COALESCE(SUM(tokens_in+tokens_out),0), COALESCE(SUM(billed_microusd),0),
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
		var micros int64
		if err := rows.Scan(&r.Agent, &r.Calls, &r.Tokens, &micros, &r.BlockedCalls); err != nil {
			return nil, err
		}
		r.Cost = money.Micros(micros)
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

// ------------------------------------------------------------ C7: the AI desk

// aiCallsTableExists is CostPerOutcome's own guard: a store built before
// this step, or one estate.Seed alone has touched, has no ai_calls table at
// all, and KPIs() must report that as a refusal for this one measure rather
// than an error for the whole library. In production the table always
// exists by the time any request reaches here (cmd/costcrew/main.go calls
// connectors.EnsureFocusSchema once, unconditionally, at every start,
// before anything is served); this guard is for every OTHER caller of
// CostPerOutcome that builds its own store without repeating that
// sequence -- KPIs()'s own existing tests among them.
func aiCallsTableExists(db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='ai_calls'`).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ModelAIRow is AgentAIRow's own shape, grouped by model instead of agent:
// aiSpendSection (C7-SPEC.md section 2) needs both cuts of the same
// ai_calls rows, and this codebase's convention is one query per cut
// (AIByAgent, AIByModel) rather than one row shape trying to serve both.
type ModelAIRow struct {
	Model        string
	Calls        int
	Tokens       int64
	Cost         money.Micros
	BlockedCalls int
}

// AIByModel is AIByAgent's own query, grouped by model rather than agent,
// ordered by cost the same way (see AIByAgent's own comment for why
// BlockedCalls is a count, never an amount).
func AIByModel(db *sql.DB, month string) ([]ModelAIRow, error) {
	rows, err := db.Query(`SELECT model, COUNT(*),
			COALESCE(SUM(tokens_in+tokens_out),0), COALESCE(SUM(billed_microusd),0),
			SUM(CASE WHEN blocked=1 THEN 1 ELSE 0 END)
		FROM ai_calls WHERE day LIKE ?
		GROUP BY model ORDER BY 4 DESC, model ASC`, month+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelAIRow
	for rows.Next() {
		var r ModelAIRow
		var micros int64
		if err := rows.Scan(&r.Model, &r.Calls, &r.Tokens, &micros, &r.BlockedCalls); err != nil {
			return nil, err
		}
		r.Cost = money.Micros(micros)
		out = append(out, r)
	}
	return out, rows.Err()
}

// OutcomeCountsByAgent is how many of an agent's non-blocked calls this
// month carry an outcome (x_outcome, non-empty): the denominator
// unitEconomicsSection and CostPerOutcome divide an agent's own cost by. An
// agent absent from the returned map tagged zero, the same as an agent
// present with 0 -- callers key it by AgentAIRow.Agent and treat a missing
// key as zero, never as an error.
func OutcomeCountsByAgent(db *sql.DB, month string) (map[string]int, error) {
	rows, err := db.Query(`SELECT agent, COUNT(*) FROM ai_calls
		WHERE day LIKE ? AND blocked=0 AND outcome IS NOT NULL AND outcome<>''
		GROUP BY agent`, month+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var agent string
		var n int
		if err := rows.Scan(&agent, &n); err != nil {
			return nil, err
		}
		out[agent] = n
	}
	return out, rows.Err()
}

// BasisCounts is the month's ai_calls split by x_cost_basis
// (settled|estimated|blocked): aiSpendSection's own "estimated share",
// because an estimated figure is not the same evidentiary strength as a
// settled one, and the packet says which basis it is reporting on.
func BasisCounts(db *sql.DB, month string) (settled, estimated, blocked int, err error) {
	err = db.QueryRow(`SELECT
			COALESCE(SUM(CASE WHEN basis='settled' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN basis='estimated' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN basis='blocked' THEN 1 ELSE 0 END),0)
		FROM ai_calls WHERE day LIKE ?`, month+"%").Scan(&settled, &estimated, &blocked)
	return
}

// CostPerOutcome is the AI desk's month-wide cost per outcome (C7-SPEC.md
// section 2): every agent that spent this month (at least one non-blocked
// call) contributes its WHOLE cost to the numerator, and every outcome any
// of them tagged contributes one to the denominator -- an agent whose calls
// cost money and tagged nothing still counts in the cost, which is the
// point: a cost per outcome that only ever counted tagged calls would get
// CHEAPER as an agent tagged less, not more honest. hasVal is false, and
// agentsWithNone equals agentsTotal, when nothing was tagged at all; the
// KPI's own refusal names that count, and its Note carries the same count
// as a caveat even while it reports, when the coverage is only partial.
//
// An agent whose only calls this month were blocked spent nothing and is
// counted in neither agentsTotal nor agentsWithNone: it never had a cost
// needing a denominator, so it is not an agent that "set none" -- it is an
// agent that was not asked. Sum stays in Micros throughout, never rounded
// to Cents until String() renders it for a reader: invariant 25 applied
// here the same way deriveCharges already applies it to the daily ledger.
//
// A store nobody has ever pointed the tokenfuse-focus reader at (or an
// older one from before this step) has no ai_calls table at all -- checked
// first, and treated as "nothing to report" rather than a database error,
// the same principle the anomalies and tasks queries elsewhere in this file
// already hold with COALESCE for an empty table, extended here to a table
// that may not exist yet at all. KPIs() must never fail outright because
// one measure's own table is missing; this file's own header says why.
func CostPerOutcome(db *sql.DB, month string) (perOutcome money.Micros, hasVal bool, agentsWithNone, agentsTotal int, err error) {
	has, err := aiCallsTableExists(db)
	if err != nil {
		return 0, false, 0, 0, err
	}
	if !has {
		return 0, false, 0, 0, nil
	}
	byAgent, err := AIByAgent(db, month)
	if err != nil {
		return 0, false, 0, 0, err
	}
	counts, err := OutcomeCountsByAgent(db, month)
	if err != nil {
		return 0, false, 0, 0, err
	}
	var totalCost money.Micros
	var totalOutcomes int
	for _, r := range byAgent {
		if r.Calls-r.BlockedCalls == 0 {
			continue
		}
		agentsTotal++
		totalCost += r.Cost
		n := counts[r.Agent]
		totalOutcomes += n
		if n == 0 {
			agentsWithNone++
		}
	}
	if totalOutcomes == 0 {
		return 0, false, agentsWithNone, agentsTotal, nil
	}
	return money.Micros(int64(totalCost) / int64(totalOutcomes)), true, agentsWithNone, agentsTotal, nil
}

package finops

// Customer units and the showback (costcrew#74).
//
// The TokenFuse FOCUS reader writes a row's x_unit as the charge's Team, so a
// box's AI spend arrives already labelled by the unit the gateway knows, "aws"
// or "finops". The generated estate has ten teams and /export/showback.csv
// listed those ten and nothing else, which on a console that holds only real
// rows meant an empty file for a bill of several dollars. A unit is not a
// roster team and nobody has yet said which business unit it is charged back
// under, so a unit-keyed allocation.rule is how a person says it: the
// chargeback analyst proposes it as an option, and a stamp applies it
// (applyUnitRule, reached only from applySideEffect, so only from ApplyAs).
//
// Until a unit has a rule its spend is still in the file, as ONE line,
// "(unruled units)": a showback that silently drops a customer's spend does
// not add up to the invoice, and a showback that prints a unit's name as a
// team before any person has ruled on it tells a team a number nobody
// decided to tell it.

import (
	"database/sql"
	"fmt"
	"sort"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

const unitRulesSchema = `
CREATE TABLE IF NOT EXISTS unit_rules(
  unit TEXT PRIMARY KEY, business_unit TEXT NOT NULL,
  decided_by TEXT NOT NULL, artifact INTEGER NOT NULL, ordinal INTEGER NOT NULL,
  applied_at TEXT NOT NULL);
`

// UnruledTeam and UnruledBusinessUnit label the one showback line that holds
// the spend of every unit no rule covers yet.
const (
	UnruledTeam         = "(unruled units)"
	UnruledBusinessUnit = "(no rule yet)"
)

// UnitRule is one stamped rule: this unit is charged back under this business
// unit, decided by this person, on this option.
type UnitRule struct {
	Unit, BusinessUnit, DecidedBy, AppliedAt string
	Artifact, Ordinal                        int
}

// UnitRules lists the stamped unit rules, by unit.
func UnitRules(db *sql.DB) ([]UnitRule, error) {
	rows, err := db.Query(`SELECT unit, business_unit, decided_by, artifact, ordinal, applied_at
		FROM unit_rules ORDER BY unit`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnitRule
	for rows.Next() {
		var r UnitRule
		if err := rows.Scan(&r.Unit, &r.BusinessUnit, &r.DecidedBy, &r.Artifact, &r.Ordinal, &r.AppliedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// applyUnitRule is the only writer of unit_rules, and it is unexported on
// purpose: it is reached from applySideEffect, which is reached from ApplyAs,
// which is the stamp (the owner's through the console, or the supervisor's own
// act for a class it may decide, which allocation.rule is not). Nothing that
// saves a proposal calls it.
//
// The refusal is the same function the save-time gate asks
// (crew.UnitRuleRefusal): a unit with no row the TokenFuse reader wrote, or a
// roster team's name, changes nothing and leaves the option unapplied. A
// second stamp on the same unit replaces its business unit and its stamper
// rather than adding a second rule for one unit.
func applyUnitRule(db *sql.DB, opt crew.Option, tgt crew.UnitTarget, actor string) error {
	reason, err := crew.UnitRuleRefusal(db, tgt.Unit)
	if err != nil {
		return err
	}
	if reason != "" {
		return fmt.Errorf("allocation.rule for unit %q refused: %s", tgt.Unit, reason)
	}
	_, err = db.Exec(`INSERT INTO unit_rules(unit, business_unit, decided_by, artifact, ordinal, applied_at)
		VALUES (?,?,?,?,?,datetime('now'))
		ON CONFLICT(unit) DO UPDATE SET business_unit=excluded.business_unit,
			decided_by=excluded.decided_by, artifact=excluded.artifact,
			ordinal=excluded.ordinal, applied_at=excluded.applied_at`,
		tgt.Unit, tgt.BusinessUnit, actor, opt.Artifact, opt.Ordinal)
	return err
}

// UnitLine is one customer unit's month: what it was charged directly, what
// was pushed onto it, and whether a rule has said which business unit it is
// charged back under.
type UnitLine struct {
	Unit, BusinessUnit string
	Ruled              bool
	Direct, Allocated  money.Cents
}

func (u UnitLine) Loaded() money.Cents { return u.Direct + u.Allocated }

// Units lists every customer unit with spend in the period, by name: a
// team in the allocation that is not on the roster, with the rule that covers
// it when there is one.
func Units(db *sql.DB, period string) ([]UnitLine, error) {
	a, err := Allocate(db, period)
	if err != nil {
		return nil, err
	}
	rules, err := UnitRules(db)
	if err != nil {
		return nil, err
	}
	return UnitsOf(a, rules), nil
}

func rosterTeams() map[string]bool {
	m := make(map[string]bool, len(world.Teams))
	for _, t := range world.Teams {
		m[t.Name] = true
	}
	return m
}

// UnitsOf is Units over an allocation already in hand, so a page that has
// computed one does not compute it twice and cannot disagree with itself.
func UnitsOf(a Allocation, rules []UnitRule) []UnitLine {
	roster := rosterTeams()
	ruled := make(map[string]UnitRule, len(rules))
	for _, r := range rules {
		ruled[r.Unit] = r
	}
	by := map[string]*UnitLine{}
	for _, tc := range a.Teams {
		if roster[tc.Team] {
			continue
		}
		l := by[tc.Team]
		if l == nil {
			l = &UnitLine{Unit: tc.Team}
			if r, ok := ruled[tc.Team]; ok {
				l.Ruled, l.BusinessUnit = true, r.BusinessUnit
			}
			by[tc.Team] = l
		}
		l.Direct += tc.Direct
		l.Allocated += tc.Allocated
	}
	out := make([]UnitLine, 0, len(by))
	for _, l := range by {
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Unit < out[j].Unit })
	return out
}

// ShowbackRow is one line of the showback export.
type ShowbackRow struct {
	Team, BusinessUnit string
	Direct, Allocated  money.Cents
}

func (r ShowbackRow) Loaded() money.Cents { return r.Direct + r.Allocated }

// Showback is the export's rows for a period: the roster's teams in roster
// order (what the file always carried), then one row per unit a rule covers,
// by name, then, only if some unit has no rule, one "(unruled units)" row
// holding all of theirs. Together with Allocation.Unallocated the rows account
// for the whole bill, which is the property a showback is judged by.
func Showback(db *sql.DB, period string) ([]ShowbackRow, error) {
	a, err := Allocate(db, period)
	if err != nil {
		return nil, err
	}
	rules, err := UnitRules(db)
	if err != nil {
		return nil, err
	}
	return ShowbackOf(a, rules), nil
}

// ShowbackOf is Showback over an allocation already in hand.
func ShowbackOf(a Allocation, rules []UnitRule) []ShowbackRow {
	type pair struct{ direct, allocated money.Cents }
	byTeam := map[string]pair{}
	for _, t := range a.Teams {
		v := byTeam[t.Team]
		byTeam[t.Team] = pair{v.direct + t.Direct, v.allocated + t.Allocated}
	}
	var rows []ShowbackRow
	for _, team := range world.Teams {
		v, ok := byTeam[team.Name]
		if !ok {
			continue
		}
		rows = append(rows, ShowbackRow{team.Name, team.Unit, v.direct, v.allocated})
	}
	unruled := ShowbackRow{Team: UnruledTeam, BusinessUnit: UnruledBusinessUnit}
	var haveUnruled bool
	for _, u := range UnitsOf(a, rules) {
		if u.Ruled {
			rows = append(rows, ShowbackRow{u.Unit, u.BusinessUnit, u.Direct, u.Allocated})
			continue
		}
		haveUnruled = true
		unruled.Direct += u.Direct
		unruled.Allocated += u.Allocated
	}
	if haveUnruled {
		rows = append(rows, unruled)
	}
	return rows
}

// Package finops is the money work: giving shared cost an owner, closing a
// period so the numbers stop moving, and saying what the crew found.
//
// The distinction the whole package turns on is between cost that has a team
// and cost that does not. Direct cost is easy and nobody needs a product for
// it. What a FinOps team is actually asked to do is take the quarter of the
// bill that arrived with nobody's name on it and give it one, defensibly
// enough that the team it lands on does not simply refuse it.
package finops

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// Method is how one pot of shared cost is split.
type Method string

const (
	// Proportional splits by each team's direct usage, which is the default
	// because it is the one a team argues with least: you pay for the discount
	// in proportion to what you used it on.
	Proportional Method = "proportional-usage"
	// Even splits equally, for a cost that exists because the organisation
	// exists rather than because anybody used anything.
	Even Method = "even-split"
	// Unallocated leaves it where it is, on purpose and visibly.
	Unallocated Method = "unallocated"
)

// Rule says what to do with one category of shared cost on one desk.
type Rule struct {
	ID       int
	Source   string // * for every desk
	Category string // Purchase, Tax, Credit
	Method   Method
	Note     string
}

const Schema = `
CREATE TABLE IF NOT EXISTS allocation_rules(
  id INTEGER PRIMARY KEY, source TEXT NOT NULL, category TEXT NOT NULL,
  method TEXT NOT NULL, note TEXT);
CREATE TABLE IF NOT EXISTS chargeback(
  period TEXT NOT NULL, source TEXT NOT NULL, team TEXT NOT NULL,
  direct_cents INTEGER NOT NULL, allocated_cents INTEGER NOT NULL,
  frozen_at TEXT, closed_by TEXT,
  PRIMARY KEY (period, source, team));
`

// SeedRules writes the defaults, each with the reason it is the default.
func SeedRules(db *sql.DB) error {
	if _, err := db.Exec(Schema); err != nil {
		return err
	}
	var have int
	if err := db.QueryRow(`SELECT COUNT(*) FROM allocation_rules`).Scan(&have); err != nil {
		return err
	}
	if have > 0 {
		return nil
	}
	defaults := []Rule{
		{Source: "*", Category: "Purchase", Method: Proportional,
			Note: "A commitment is bought against usage, so it is split by usage. " +
				"The team that used the most of it carries the most of it."},
		{Source: "*", Category: "Tax", Method: Proportional,
			Note: "Tax follows the bill it was charged on."},
		{Source: "*", Category: "Credit", Method: Proportional,
			Note: "A discount is shared the way the spend that earned it was."},
	}
	for _, r := range defaults {
		if _, err := db.Exec(`INSERT INTO allocation_rules(source, category, method, note)
			VALUES (?,?,?,?)`, r.Source, r.Category, string(r.Method), r.Note); err != nil {
			return err
		}
	}
	return nil
}

func Rules(db *sql.DB) ([]Rule, error) {
	rows, err := db.Query(`SELECT id, source, category, method, COALESCE(note,'')
		FROM allocation_rules ORDER BY source, category`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var r Rule
		var m string
		if err := rows.Scan(&r.ID, &r.Source, &r.Category, &m, &r.Note); err != nil {
			return nil, err
		}
		r.Method = Method(m)
		out = append(out, r)
	}
	return out, rows.Err()
}

func SetRule(db *sql.DB, id int, method Method) error {
	switch method {
	case Proportional, Even, Unallocated:
	default:
		return fmt.Errorf("no such method: %q", method)
	}
	res, err := db.Exec(`UPDATE allocation_rules SET method=? WHERE id=?`, string(method), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no such rule")
	}
	return nil
}

// ------------------------------------------------------------- allocation

// TeamCost is one team's month: what it spent directly, what was pushed onto
// it, and the two added together.
type TeamCost struct {
	Source    string
	Team      string
	Direct    money.Cents
	Allocated money.Cents
	Shares    map[string]money.Cents // which pot each allocated slice came from
}

func (t TeamCost) Loaded() money.Cents { return t.Direct + t.Allocated }

// Allocation is a month's answer, and it carries what it could NOT place as a
// first-class number rather than folding it in and hoping.
type Allocation struct {
	Period      string
	Teams       []TeamCost
	Direct      money.Cents
	Shared      money.Cents
	Placed      money.Cents
	Unallocated money.Cents
	Coverage    float64 // direct + placed, over the whole bill
}

// Allocate splits a month's shared cost across the teams that spent directly.
//
// Money is redistributed in CENTS and the remainder is handed to the largest
// team rather than dropped. Splitting 100.00 three ways loses a cent to
// rounding, and a chargeback that does not add up to the invoice is one the
// finance team sends straight back.
func Allocate(db *sql.DB, period string) (Allocation, error) {
	out := Allocation{Period: period}

	rules, err := Rules(db)
	if err != nil {
		return out, err
	}
	method := func(source, category string) Method {
		best := Unallocated
		for _, r := range rules {
			if r.Category != category {
				continue
			}
			if r.Source == source {
				return r.Method
			}
			if r.Source == "*" {
				best = r.Method
			}
		}
		return best
	}

	// Direct cost: the rows that already have a team.
	direct := map[string]map[string]money.Cents{} // source -> team -> cents
	rows, err := db.Query(`SELECT source, team, SUM(billed_cents) FROM charges
		WHERE substr(day,1,7)=? AND team IS NOT NULL AND team <> ''
		GROUP BY 1,2 ORDER BY 1,2`, period)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var src, team string
		var v int64
		if err := rows.Scan(&src, &team, &v); err != nil {
			rows.Close()
			return out, err
		}
		if direct[src] == nil {
			direct[src] = map[string]money.Cents{}
		}
		direct[src][team] += money.Cents(v)
		out.Direct += money.Cents(v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	// Shared cost: the rows that have none.
	type pot struct {
		source, category string
		amount           money.Cents
	}
	var pots []pot
	prows, err := db.Query(`SELECT source, category, SUM(billed_cents) FROM charges
		WHERE substr(day,1,7)=? AND (team IS NULL OR team='')
		GROUP BY 1,2 ORDER BY 1,2`, period)
	if err != nil {
		return out, err
	}
	for prows.Next() {
		var p pot
		var v int64
		if err := prows.Scan(&p.source, &p.category, &v); err != nil {
			prows.Close()
			return out, err
		}
		p.amount = money.Cents(v)
		out.Shared += p.amount
		pots = append(pots, p)
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return out, err
	}

	alloc := map[string]map[string]TeamCost{}
	get := func(src, team string) TeamCost {
		if alloc[src] == nil {
			alloc[src] = map[string]TeamCost{}
		}
		tc, ok := alloc[src][team]
		if !ok {
			tc = TeamCost{Source: src, Team: team, Shares: map[string]money.Cents{}}
		}
		return tc
	}

	for src, teams := range direct {
		for team, amt := range teams {
			tc := get(src, team)
			tc.Direct = amt
			alloc[src][team] = tc
		}
	}

	for _, p := range pots {
		m := method(p.source, p.category)
		if m == Unallocated {
			out.Unallocated += p.amount
			continue
		}
		teams := direct[p.source]
		if len(teams) == 0 {
			// Nothing on this desk to carry it. Left where it is and counted,
			// rather than quietly spread onto teams that never touched it.
			out.Unallocated += p.amount
			continue
		}
		names := make([]string, 0, len(teams))
		for t := range teams {
			names = append(names, t)
		}
		sort.Strings(names)

		var basis money.Cents
		for _, t := range names {
			if m == Even {
				basis += 1
			} else {
				basis += teams[t]
			}
		}
		if basis == 0 {
			out.Unallocated += p.amount
			continue
		}

		var placed money.Cents
		biggest, biggestAmt := names[0], money.Cents(0)
		for _, t := range names {
			var share money.Cents
			if m == Even {
				share = money.Cents(int64(p.amount) / int64(len(names)))
			} else {
				share = money.Cents(int64(p.amount) * int64(teams[t]) / int64(basis))
			}
			tc := get(p.source, t)
			tc.Allocated += share
			tc.Shares[p.category] += share
			alloc[p.source][t] = tc
			placed += share
			if teams[t] > biggestAmt {
				biggest, biggestAmt = t, teams[t]
			}
		}
		// The remainder goes somewhere rather than nowhere. A chargeback that
		// does not add up to the invoice is one finance sends back.
		if rem := p.amount - placed; rem != 0 {
			tc := get(p.source, biggest)
			tc.Allocated += rem
			tc.Shares[p.category] += rem
			alloc[p.source][biggest] = tc
			placed += rem
		}
		out.Placed += placed
	}

	for _, teams := range alloc {
		for _, tc := range teams {
			out.Teams = append(out.Teams, tc)
		}
	}
	sort.Slice(out.Teams, func(i, j int) bool {
		if out.Teams[i].Source != out.Teams[j].Source {
			return out.Teams[i].Source < out.Teams[j].Source
		}
		return out.Teams[i].Loaded() > out.Teams[j].Loaded()
	})

	total := out.Direct + out.Shared
	if total != 0 {
		out.Coverage = float64(out.Direct+out.Placed) / float64(total) * 100
	}
	return out, nil
}

// Months lists the periods the estate covers, newest first.
func Months(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT substr(day,1,7) FROM charges ORDER BY 1 DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func MethodNote(m Method) string {
	switch m {
	case Proportional:
		return "split by each team's direct usage"
	case Even:
		return "split equally between the teams on the desk"
	case Unallocated:
		return "left where it is, and counted as unallocated"
	}
	return string(m)
}

func ValidMethods() []Method { return []Method{Proportional, Even, Unallocated} }

func MethodFrom(s string) (Method, error) {
	for _, m := range ValidMethods() {
		if strings.EqualFold(string(m), s) {
			return m, nil
		}
	}
	return "", fmt.Errorf("no such method: %q", s)
}

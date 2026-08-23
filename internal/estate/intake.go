package estate

import (
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// Putting numbers in by hand, which is how a budget actually arrives.
//
// Every other figure in this console is read from a bill. A budget is not: it
// is a decision somebody made in a meeting, and until now the console could
// hand out a template for one and had nowhere to take it back. A download that
// leads nowhere is worse than no download, because somebody fills it in.
//
// The discipline is the one the outbound integration uses, and for the same
// reason: this writes the figure a team is measured against, so nothing is
// written until the whole file has been read, every row checked, and the
// difference shown.

// BudgetRowIn is one line of the file, after parsing.
type BudgetRowIn struct {
	Line   int
	Source string
	Team   string
	Month  string
	Budget money.Cents

	Now    money.Cents // what the store holds, zero when it holds none
	HasNow bool
	New    bool
	Lower  bool
	Closed bool // the month has been charged; changing it rewrites an invoice
}

// BudgetPlan is what the file would do.
type BudgetPlan struct {
	Rows      []BudgetRowIn
	Unchanged int
	Added     int
	Lowered   int
	InClosed  int
	Problems  []string
}

func (p BudgetPlan) Empty() bool { return len(p.Rows) == 0 }

// Fingerprint identifies THIS plan, so the apply cannot send a different one.
func (p BudgetPlan) Fingerprint() string { return fingerprint(p.Rows) }

// ReadBudgets parses a CSV and says what it would do, touching nothing.
//
// Every problem is collected rather than returned on the first one: a person
// who fixed one typo and re-uploaded to find the next is a person who will
// stop using this, and the file is small enough to check whole.
func ReadBudgets(r io.Reader, current map[BudgetKey]money.Cents, closed map[string]bool) (BudgetPlan, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	records, err := cr.ReadAll()
	if err != nil {
		return BudgetPlan{}, fmt.Errorf("this is not a CSV this console can read: %w", err)
	}
	if len(records) == 0 {
		return BudgetPlan{}, fmt.Errorf("the file is empty")
	}

	// The header, by NAME rather than by position, so a column order that
	// differs from the template is not a silent misreading of every row.
	head := map[string]int{}
	for i, h := range records[0] {
		head[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, want := range []string{"platform", "team", "month", "budget_usd"} {
		if _, ok := head[want]; !ok {
			return BudgetPlan{}, fmt.Errorf("the file has no %q column. The template's "+
				"header is: platform, team, month, budget_usd", want)
		}
	}

	var p BudgetPlan
	seen := map[BudgetKey]int{}
	for n, rec := range records[1:] {
		line := n + 2 // 1-based, and the header is line 1
		get := func(col string) string {
			i := head[col]
			if i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		row := BudgetRowIn{Line: line, Source: get("platform"), Team: get("team"),
			Month: get("month")}
		switch {
		case row.Source == "" || row.Team == "" || row.Month == "":
			p.Problems = append(p.Problems, fmt.Sprintf(
				"line %d: platform, team and month are all required", line))
			continue
		case !validMonth(row.Month):
			p.Problems = append(p.Problems, fmt.Sprintf(
				"line %d: %q is not a month; write it as 2026-09", line, row.Month))
			continue
		}
		amount, err := money.Parse(get("budget_usd"))
		if err != nil {
			p.Problems = append(p.Problems, fmt.Sprintf(
				"line %d: %q is not an amount; write it as 960 or 960.00", line, get("budget_usd")))
			continue
		}
		if amount <= 0 {
			p.Problems = append(p.Problems, fmt.Sprintf(
				"line %d: a budget of %s is not a budget, it is a stop. "+
					"Leave the row out to leave the budget alone", line, amount))
			continue
		}
		row.Budget = amount

		key := BudgetKey{row.Source, row.Team, row.Month}
		if first, dup := seen[key]; dup {
			p.Problems = append(p.Problems, fmt.Sprintf(
				"line %d repeats %s / %s / %s, first given on line %d. "+
					"Two answers for one team-month is not a decision",
				line, row.Source, row.Team, row.Month, first))
			continue
		}
		seen[key] = line

		row.Now, row.HasNow = current[key]
		row.New = !row.HasNow
		row.Lower = row.HasNow && row.Budget < row.Now
		row.Closed = closed[row.Month]

		if row.HasNow && row.Now == row.Budget {
			p.Unchanged++
			continue
		}
		if row.New {
			p.Added++
		}
		if row.Lower {
			p.Lowered++
		}
		if row.Closed {
			p.InClosed++
		}
		p.Rows = append(p.Rows, row)
	}
	sort.Slice(p.Rows, func(i, j int) bool { return p.Rows[i].Line < p.Rows[j].Line })
	return p, nil
}

// BudgetKey is one team's month on one desk.
type BudgetKey struct{ Source, Team, Month string }

func validMonth(s string) bool {
	if len(s) != 7 || s[4] != '-' {
		return false
	}
	for i, c := range s {
		if i == 4 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return s[5] == '0' && s[6] >= '1' && s[6] <= '9' ||
		s[5] == '1' && s[6] >= '0' && s[6] <= '2'
}

// fingerprint identifies a plan by what it would write and what is there now.
//
// The same device the outbound budget push uses, for the same reason: a person
// reads a difference, thinks about it, and clicks apply. If the apply re-reads
// the file and re-plans, what they approved and what is written are two
// different things and nothing would say so.
func fingerprint(rows []BudgetRowIn) string {
	h := sha256.New()
	for _, r := range rows {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d\x00%d\x00%t\n",
			r.Source, r.Team, r.Month, int64(r.Budget), int64(r.Now), r.HasNow)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// ApplyBudgets writes the plan, and only the plan it was shown as.
//
// One transaction: a half-applied budget file leaves some teams measured
// against a decision and some against the one before it, and nothing on any
// page would say which is which.
func ApplyBudgets(db *sql.DB, p BudgetPlan, expect string) (int, error) {
	if got := p.Fingerprint(); got != expect {
		return 0, fmt.Errorf("this is not the file that was checked: it was shown as %s "+
			"and is now %s. Upload it again and look at the difference", expect, got)
	}
	if p.Empty() {
		return 0, nil
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, r := range p.Rows {
		if _, err := tx.Exec(`INSERT INTO budgets(source, team, month, budget_cents)
			VALUES (?,?,?,?)
			ON CONFLICT(source, team, month) DO UPDATE SET budget_cents = excluded.budget_cents`,
			r.Source, r.Team, r.Month, int64(r.Budget)); err != nil {
			return 0, fmt.Errorf("line %d (%s / %s / %s): %w",
				r.Line, r.Source, r.Team, r.Month, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(p.Rows), nil
}

// CurrentBudgets is what the store holds, keyed the way a file is read.
func CurrentBudgets(db *sql.DB) (map[BudgetKey]money.Cents, error) {
	rows, err := db.Query(`SELECT source, team, month, budget_cents FROM budgets`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[BudgetKey]money.Cents{}
	for rows.Next() {
		var k BudgetKey
		var v int64
		if err := rows.Scan(&k.Source, &k.Team, &k.Month, &v); err != nil {
			return nil, err
		}
		out[k] = money.Cents(v)
	}
	return out, rows.Err()
}

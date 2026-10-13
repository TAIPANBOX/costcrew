package estate

// Spend medians, and the split of a service-level step across teams, for
// announced decisions (ANNOUNCED-DRIVER-SPEC.md sections 5 and 11). Every
// figure here is integer cents. Nothing rounds before it is summed: the split
// is exact, and its remainder is handed to the largest share.

import (
	"database/sql"
	"math/big"
	"sort"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// PreStartDays is how many days before a decision's start its medians are
// drawn from (ANNOUNCED-DRIVER-SPEC.md sections 5 and 11).
const PreStartDays = 28

// PreStartWindow is the window a decision's medians are drawn from: the
// PreStartDays days ending the day before start, both ends inclusive, as
// YYYY-MM-DD strings.
func PreStartWindow(start string) (from, to string) {
	t, err := time.Parse("2006-01-02", start)
	if err != nil {
		return start, start
	}
	return t.AddDate(0, 0, -PreStartDays).Format("2006-01-02"), t.AddDate(0, 0, -1).Format("2006-01-02")
}

// SpendMedian is the median daily spend of one desk's service over the window
// before start, on the days that have a charge. A team of "" means every team
// on the desk. ok is false when no day in the window has spend.
func SpendMedian(db *sql.DB, source, service, team, start string) (money.Cents, bool, error) {
	from, to := PreStartWindow(start)
	q := `SELECT day, SUM(billed_cents) FROM charges
		WHERE source=? AND service=? AND day>=? AND day<=?`
	args := []any{source, service, from, to}
	if team != "" {
		q += ` AND team=?`
		args = append(args, team)
	}
	q += ` GROUP BY day`
	rows, err := db.Query(q, args...)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	var vals []money.Cents
	for rows.Next() {
		var day string
		var v int64
		if err := rows.Scan(&day, &v); err != nil {
			return 0, false, err
		}
		vals = append(vals, money.Cents(v))
	}
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	if len(vals) == 0 {
		return 0, false, nil
	}
	return medianOf(vals), true, nil
}

// TeamSpendMedians returns each team on the desk that has spend on the service
// in the window before start, with that team's median daily spend. A team with
// no such spend is absent from the map.
func TeamSpendMedians(db *sql.DB, source, service, start string) (map[string]money.Cents, error) {
	from, to := PreStartWindow(start)
	rows, err := db.Query(`SELECT team, day, SUM(billed_cents) FROM charges
		WHERE source=? AND service=? AND team IS NOT NULL AND team<>'' AND day>=? AND day<=?
		GROUP BY team, day`, source, service, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byTeam := map[string][]money.Cents{}
	for rows.Next() {
		var team, day string
		var v int64
		if err := rows.Scan(&team, &day, &v); err != nil {
			return nil, err
		}
		byTeam[team] = append(byTeam[team], money.Cents(v))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]money.Cents, len(byTeam))
	for team, vals := range byTeam {
		out[team] = medianOf(vals)
	}
	return out, nil
}

// SplitByMedian divides a daily step across teams in proportion to each team's
// median daily spend. Each share is the step times that team's median, divided
// by the total of the medians, rounded toward zero. The shares then sum to the
// step exactly: the remainder, which is smaller than the number of teams, goes
// to the largest share, and among equal largest shares to the first team in
// name order. A team with no spend gets zero. With no spend anywhere there is
// nothing to split against, and the result is empty: the step attaches to no
// team at all.
func SplitByMedian(daily money.Cents, medians map[string]money.Cents) map[string]money.Cents {
	out := map[string]money.Cents{}
	names := make([]string, 0, len(medians))
	total := new(big.Int)
	for name, m := range medians {
		names = append(names, name)
		if m > 0 {
			total.Add(total, big.NewInt(int64(m)))
		}
	}
	sort.Strings(names)
	if total.Sign() == 0 {
		return out
	}
	sum := new(big.Int)
	step := big.NewInt(int64(daily))
	for _, name := range names {
		m := medians[name]
		if m <= 0 {
			out[name] = 0
			continue
		}
		s := new(big.Int).Mul(step, big.NewInt(int64(m)))
		s.Quo(s, total)
		out[name] = money.Cents(s.Int64())
		sum.Add(sum, s)
	}
	rem := new(big.Int).Sub(step, sum)
	if rem.Sign() != 0 {
		best := ""
		for _, name := range names {
			if medians[name] <= 0 {
				continue
			}
			if best == "" || medians[name] > medians[best] {
				best = name
			}
		}
		out[best] += money.Cents(rem.Int64())
	}
	return out
}

// medianOf is the median of vals: the middle value, or for an even count the
// integer mean of the two middle values rounded down, as internal/detect's
// median is.
func medianOf(vals []money.Cents) money.Cents {
	s := append([]money.Cents(nil), vals...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

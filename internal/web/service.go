package web

import (
	"database/sql"
	"net/http"
	"sort"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

var (
	tplService  = page("service.html")
	tplServices = page("services.html")
)

// The money lives in services, and until now there was no page for one.
//
// A reader could open a team, a desk or an agent and not the thing that
// actually costs: "Amazon EC2" appeared on six pages as text. It is the level
// a FinOps conversation is actually held at, because it is the level a
// provider bills at and the level somebody can act on.

type serviceRow struct {
	Name    string
	Source  string
	Teams   int
	Amount  money.Cents
	Prev    money.Cents
	Change  money.Cents
	Share   float64
	Open    int
	Kind    string // compute, database, accelerator, or empty
	Metered string // the unit it is metered in, when it has one
}

// services totals one month per service, with the month before beside it.
func services(db *sql.DB, month, prev string) ([]serviceRow, money.Cents, error) {
	rows, err := db.Query(`SELECT service, source,
			COUNT(DISTINCT COALESCE(team,'')),
			SUM(CASE WHEN substr(day,1,7)=? THEN billed_cents ELSE 0 END),
			SUM(CASE WHEN substr(day,1,7)=? THEN billed_cents ELSE 0 END),
			COALESCE(MAX(unit),'')
		FROM charges WHERE substr(day,1,7) IN (?,?)
		GROUP BY 1,2 ORDER BY 4 DESC`, month, prev, month, prev)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []serviceRow
	var total money.Cents
	for rows.Next() {
		var r serviceRow
		var now, was int64
		if err := rows.Scan(&r.Name, &r.Source, &r.Teams, &now, &was, &r.Metered); err != nil {
			return nil, 0, err
		}
		r.Amount, r.Prev = money.Cents(now), money.Cents(was)
		r.Change = r.Amount - r.Prev
		r.Kind = world.ResourceKind(r.Name)
		total += r.Amount
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if total != 0 {
		for i := range out {
			out[i].Share = float64(out[i].Amount) / float64(total) * 100
		}
	}
	return out, total, nil
}

func (s *Server) services(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	period, months := s.period(r)
	rows, total, err := services(s.db, period, prevMonth(period))
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	open, _ := anomaly.List(s.db, anomaly.Filter{State: anomaly.Open})
	for i := range rows {
		for _, a := range open {
			if a.Service == rows[i].Name && a.Source == rows[i].Source {
				rows[i].Open++
			}
		}
	}
	// Where the money is concentrated, said as a number rather than left for
	// the reader to add up: in most estates a handful of services are the bill
	// and the rest is noise, and knowing which is which is the first move.
	sorted := append([]serviceRow(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Amount > sorted[j].Amount })
	var running money.Cents
	topN := 0
	for _, x := range sorted {
		if running >= total*8/10 {
			break
		}
		running += x.Amount
		topN++
	}

	srt := readSort(r, "amount", true)
	applySort(rows, srt, map[string]func(a, b serviceRow) int{
		"service": func(a, b serviceRow) int { return cmpString(a.Name, b.Name) },
		"desk":    func(a, b serviceRow) int { return cmpString(a.Source, b.Source) },
		"kind":    func(a, b serviceRow) int { return cmpString(a.Kind, b.Kind) },
		"teams":   func(a, b serviceRow) int { return cmpInt(a.Teams, b.Teams) },
		"amount":  func(a, b serviceRow) int { return cmpInt64(int64(a.Amount), int64(b.Amount)) },
		"change":  func(a, b serviceRow) int { return cmpInt64(int64(a.Change), int64(b.Change)) },
		"open":    func(a, b serviceRow) int { return cmpInt(a.Open, b.Open) },
	}, "amount")

	s.render(w, tplServices, struct {
		shell
		Rows   []serviceRow
		Total  money.Cents
		TopN   int
		Period string
		Prev   string
		Months []string
		Sort   sortSpec
	}{s.shellFor(r, "Services", "services"), rows, total, topN, period,
		prevMonth(period), months, srt})
}

type meterRow struct {
	Month    string
	Quantity float64
	Unit     string
	Cost     money.Cents
	PerUnit  money.Cents
}

func (s *Server) service(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	name := r.PathValue("name")
	period, months := s.period(r)

	// Which desk it is billed under. A service can appear on more than one,
	// and the page says so rather than picking the first and hiding the rest.
	var desks []string
	if rows, err := s.db.Query(`SELECT DISTINCT source FROM charges WHERE service=? ORDER BY 1`,
		name); err == nil {
		for rows.Next() {
			var d string
			if rows.Scan(&d) == nil {
				desks = append(desks, d)
			}
		}
		rows.Close()
	}
	if len(desks) == 0 {
		http.Error(w, "no such service", http.StatusNotFound)
		return
	}

	byTeam, total, _ := breakdown(s.db, `service=? AND substr(day,1,7)='`+period+`'`, name, "team")
	byCategory, _, _ := breakdown(s.db, `service=? AND substr(day,1,7)='`+period+`'`, name, "category")
	trend, _ := monthly(s.db, `service=?`, name)

	anoms, _ := anomaly.List(s.db, anomaly.Filter{})
	var mine []anomaly.Anomaly
	for _, a := range anoms {
		if a.Service == name {
			mine = append(mine, a)
		}
	}

	// The resources inside it, and what they are metered in. Together these
	// are the two ways to act on a service: resize what is in it, or use less
	// of what it charges for.
	var util []world.Utilisation
	var resizable money.Cents
	for _, u := range world.UtilisationRows {
		if u.Service == name {
			util = append(util, u)
			resizable += u.Saving
		}
	}

	var meters []meterRow
	if rows, err := s.db.Query(`SELECT substr(day,1,7) m, COALESCE(unit,''),
			SUM(quantity), SUM(billed_cents)
		FROM charges WHERE service=? AND COALESCE(unit,'') <> ''
		GROUP BY 1,2 ORDER BY 1 DESC LIMIT 12`, name); err == nil {
		for rows.Next() {
			var mr meterRow
			var cost int64
			if rows.Scan(&mr.Month, &mr.Unit, &mr.Quantity, &cost) == nil {
				mr.Cost = money.Cents(cost)
				if mr.Quantity > 0 {
					mr.PerUnit = money.Cents(float64(cost) / mr.Quantity * 1_000_000)
				}
				meters = append(meters, mr)
			}
		}
		rows.Close()
	}

	s.render(w, tplService, struct {
		shell
		Name       string
		Desks      []string
		Kind       string
		Total      money.Cents
		ByTeam     []spendRow
		ByCategory []spendRow
		Trend      []monthRow
		Anomalies  []anomaly.Anomaly
		Util       []world.Utilisation
		Resizable  money.Cents
		Meters     []meterRow
		Period     string
		Months     []string
	}{s.shellFor(r, name, "services"), name, desks, world.ResourceKind(name), total,
		byTeam, byCategory, trend, mine, util, resizable, meters, period, months})
}

package web

import (
	"database/sql"
	"net/http"
	"net/url"
	"sort"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

var (
	tplTeam  = page("team.html")
	tplDesk  = page("desk.html")
	tplTeams = page("teams.html")
	tplDesks = page("desks.html")
)

// A name on a page should be a way in.
//
// Every team, desk and analyst the console mentions is somebody's whole world
// for the afternoon, and a table cell that names one and does not open it
// makes the reader go back to the top and filter by hand. These two pages are
// the destination for every such link.

type spendRow struct {
	Key     string
	Service string
	Amount  money.Cents
	Share   float64
}

type monthRow struct {
	Month  string
	Amount money.Cents
	Change money.Cents
	HasChg bool
}

// monthly walks a series of months and works out what changed between them,
// which is the column somebody actually reads.
func monthly(db *sql.DB, where string, arg any) ([]monthRow, error) {
	rows, err := db.Query(`SELECT substr(day,1,7) m, SUM(billed_cents)
		FROM charges WHERE `+where+` GROUP BY 1 ORDER BY 1`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []monthRow
	for rows.Next() {
		var r monthRow
		var v int64
		if err := rows.Scan(&r.Month, &v); err != nil {
			return nil, err
		}
		r.Amount = money.Cents(v)
		if n := len(out); n > 0 {
			r.Change = r.Amount - out[n-1].Amount
			r.HasChg = true
		}
		out = append(out, r)
	}
	// Newest first: the month somebody came to look at is the last one.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

func breakdown(db *sql.DB, where string, arg any, keyCol string) ([]spendRow, money.Cents, error) {
	rows, err := db.Query(`SELECT `+keyCol+`, SUM(billed_cents) FROM charges
		WHERE `+where+` GROUP BY 1 ORDER BY 2 DESC`, arg)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []spendRow
	var total money.Cents
	for rows.Next() {
		var k sql.NullString
		var v int64
		if err := rows.Scan(&k, &v); err != nil {
			return nil, 0, err
		}
		name := k.String
		if !k.Valid || name == "" {
			name = "(untagged)"
		}
		out = append(out, spendRow{Key: name, Amount: money.Cents(v)})
		total += money.Cents(v)
	}
	if total != 0 {
		for i := range out {
			out[i].Share = float64(out[i].Amount) / float64(total) * 100
		}
	}
	return out, total, rows.Err()
}

// The two tables a team page and a desk page both carry, so their comparators
// are written once. A breakdown sorts by money or by name; a trend sorts by
// month, by cost, or by how much it moved, which is the column somebody scans
// a trend for.
func spendSort(r *http.Request, param string) (sortSpec, map[string]func(a, b spendRow) int) {
	return readSortNamed(r, param, "amount", true), map[string]func(a, b spendRow) int{
		"name":   func(a, b spendRow) int { return cmpString(a.Key, b.Key) },
		"amount": func(a, b spendRow) int { return cmpInt64(int64(a.Amount), int64(b.Amount)) },
	}
}

func trendSort(r *http.Request, param string) (sortSpec, map[string]func(a, b monthRow) int) {
	return readSortNamed(r, param, "month", true), map[string]func(a, b monthRow) int{
		"month":  func(a, b monthRow) int { return cmpString(a.Month, b.Month) },
		"amount": func(a, b monthRow) int { return cmpInt64(int64(a.Amount), int64(b.Amount)) },
		"change": func(a, b monthRow) int { return cmpInt64(abs64(int64(a.Change)), abs64(int64(b.Change))) },
	}
}

func utilSort(r *http.Request, param string) (sortSpec, map[string]func(a, b world.Utilisation) int) {
	return readSortNamed(r, param, "monthly", true), map[string]func(a, b world.Utilisation) int{
		"resource": func(a, b world.Utilisation) int { return cmpString(a.Resource, b.Resource) },
		"cpu":      func(a, b world.Utilisation) int { return cmpFloat(a.P95CPU, b.P95CPU) },
		"monthly":  func(a, b world.Utilisation) int { return cmpInt64(int64(a.Monthly), int64(b.Monthly)) },
		"saving":   func(a, b world.Utilisation) int { return cmpInt64(int64(a.Saving), int64(b.Saving)) },
	}
}

// -------------------------------------------------------------------- team

func (s *Server) team(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	name := r.PathValue("name")
	var team world.Team
	for _, t := range world.Teams {
		if t.Name == name {
			team = t
		}
	}
	if team.Name == "" {
		http.Error(w, "no such team", http.StatusNotFound)
		return
	}
	period, months := s.period(r)

	byService, total, err := breakdown(s.db, `team=? AND substr(day,1,7)='`+period+`'`, name, "service")
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	byDesk, _, _ := breakdown(s.db, `team=? AND substr(day,1,7)='`+period+`'`, name, "source")
	trend, _ := monthly(s.db, `team=?`, name)

	anoms, _ := anomaly.List(s.db, anomaly.Filter{})
	var mine []anomaly.Anomaly
	var openMoney money.Cents
	for _, a := range anoms {
		if a.Team == name {
			mine = append(mine, a)
			if a.State == anomaly.Open {
				openMoney += a.Excess.Abs()
			}
		}
	}

	// The allocation is what this team is actually charged, which is a
	// different and larger number than what it spent directly.
	alloc, _ := finops.Allocate(s.db, period)
	var direct, allocated money.Cents
	for _, tc := range alloc.Teams {
		if tc.Team == name {
			direct += tc.Direct
			allocated += tc.Allocated
		}
	}

	var licences []world.Licence
	var seatWaste money.Cents
	for _, l := range world.Licences {
		if l.Team == name {
			licences = append(licences, l)
			seatWaste += l.Waste()
		}
	}
	var util []world.Utilisation
	for _, u := range world.UtilisationRows {
		if u.Team == name {
			util = append(util, u)
		}
	}

	srt := readSort(r, "amount", true)
	applySort(byService, srt, map[string]func(a, b spendRow) int{
		"service": func(a, b spendRow) int { return cmpString(a.Key, b.Key) },
		"amount":  func(a, b spendRow) int { return cmpInt64(int64(a.Amount), int64(b.Amount)) },
		"share":   func(a, b spendRow) int { return cmpFloat(a.Share, b.Share) },
	}, "amount")

	tsrt, tcmp := trendSort(r, "tsort")
	applySort(trend, tsrt, tcmp, "month")
	usrt, ucmp := utilSort(r, "usort")
	applySort(util, usrt, ucmp, "monthly")
	dsrt, dcmp := spendSort(r, "dsort")
	applySort(byDesk, dsrt, dcmp, "amount")

	s.render(w, tplTeam, struct {
		shell
		T         world.Team
		Period    string
		Months    []string
		ByService []spendRow
		ByDesk    []spendRow
		Trend     []monthRow
		Total     money.Cents
		Direct    money.Cents
		Allocated money.Cents
		Loaded    money.Cents
		Anomalies []anomaly.Anomaly
		OpenMoney money.Cents
		Licences  []world.Licence
		SeatWaste money.Cents
		Util      []world.Utilisation
		Sort      sortSpec
		SortTrend sortSpec
		SortUtil  sortSpec
		SortDesk  sortSpec
	}{s.shellFor(r, name, "teams"), team, period, months, byService, byDesk,
		trend, total, direct, allocated, direct + allocated, mine, openMoney,
		licences, seatWaste, util, srt, tsrt, usrt, dsrt})
}

// -------------------------------------------------------------------- desk

func (s *Server) desk(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	name := r.PathValue("name")
	var desk world.Desk
	for _, d := range world.Desks {
		if d.Name == name {
			desk = d
		}
	}
	if desk.Name == "" {
		// "Desk" means two things here: a spend source, and the desk an
		// analyst sits at. They mostly coincide, and "management" does not:
		// twelve analysts sit there and no cloud bills it. Their cards linked
		// here and got a bare 404, and the owners page for whoever holds them
		// carried twelve of those links on one screen.
		//
		// So the reader is sent where the question they clicked CAN be
		// answered, which is the crew filtered to that desk, rather than told
		// the desk does not exist while twelve agents sit at it.
		if crewDeskExists(s.db, name) {
			http.Redirect(w, r, "/staff?desk="+url.QueryEscape(name), http.StatusSeeOther)
			return
		}
		http.Error(w, "no such desk", http.StatusNotFound)
		return
	}
	period, months := s.period(r)

	byTeam, total, err := breakdown(s.db, `source=? AND substr(day,1,7)='`+period+`'`, name, "team")
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	byService, _, _ := breakdown(s.db, `source=? AND substr(day,1,7)='`+period+`'`, name, "service")
	trend, _ := monthly(s.db, `source=?`, name)

	anoms, _ := anomaly.List(s.db, anomaly.Filter{Source: name})
	var openMoney money.Cents
	for _, a := range anoms {
		if a.State == anomaly.Open {
			openMoney += a.Excess.Abs()
		}
	}

	roster, _ := crew.Roster(s.db)
	scores, _ := crew.Scoreboards(s.db)
	var analysts []staffRow
	for _, a := range roster {
		if a.Desk == name {
			analysts = append(analysts, staffRow{a, scores[a.Name], agentChip(a.State)})
		}
	}
	tasks, _ := crew.Tasks(s.db, crew.TaskFilter{Desk: name})

	var commitments []world.Commitment
	for _, c := range world.Commitments {
		if c.Source == name {
			commitments = append(commitments, c)
		}
	}

	budgets, _ := estateBudgets(s.db, name, period)
	halt, halted, _ := crew.ActiveHalt(s.db, name)

	srt := readSort(r, "amount", true)
	applySort(byTeam, srt, map[string]func(a, b spendRow) int{
		"team":   func(a, b spendRow) int { return cmpString(a.Key, b.Key) },
		"amount": func(a, b spendRow) int { return cmpInt64(int64(a.Amount), int64(b.Amount)) },
		"share":  func(a, b spendRow) int { return cmpFloat(a.Share, b.Share) },
	}, "amount")

	tsrt, tcmp := trendSort(r, "tsort")
	applySort(trend, tsrt, tcmp, "month")
	vsrt, vcmp := spendSort(r, "vsort")
	applySort(byService, vsrt, vcmp, "amount")
	msrt, mcmp := spendSort(r, "msort")
	applySort(byTeam, msrt, mcmp, "amount")
	bsrt := readSortNamed(r, "bsort", "var", true)
	applySort(budgets, bsrt, map[string]func(a, b budgetLine) int{
		"team":   func(a, b budgetLine) int { return cmpString(a.Team, b.Team) },
		"budget": func(a, b budgetLine) int { return cmpInt64(int64(a.Budget), int64(b.Budget)) },
		"actual": func(a, b budgetLine) int { return cmpInt64(int64(a.Actual), int64(b.Actual)) },
		// Signed, not by size: on a budget table the sign is the whole point,
		// and the end somebody opens it for is the over one.
		"var": func(a, b budgetLine) int { return cmpInt64(int64(a.Var), int64(b.Var)) },
		"pct": func(a, b budgetLine) int { return cmpFloat(a.VarPct, b.VarPct) },
	}, "var")
	asrt := readSortNamed(r, "asort", "spent", true)
	applySort(analysts, asrt, map[string]func(a, b staffRow) int{
		"analyst": func(a, b staffRow) int { return cmpString(a.Name, b.Name) },
		"open":    func(a, b staffRow) int { return cmpInt(a.Score.Open, b.Score.Open) },
		"posted":  func(a, b staffRow) int { return cmpInt(a.Score.Posted, b.Score.Posted) },
		"rate":    func(a, b staffRow) int { return cmpFloat(a.Score.FirstPass, b.Score.FirstPass) },
		"spent":   func(a, b staffRow) int { return cmpInt64(int64(a.Score.Spent), int64(b.Score.Spent)) },
		"state":   func(a, b staffRow) int { return cmpString(a.State, b.State) },
	}, "spent")

	s.render(w, tplDesk, struct {
		shell
		D           world.Desk
		Period      string
		Months      []string
		ByTeam      []spendRow
		ByService   []spendRow
		Trend       []monthRow
		Total       money.Cents
		Anomalies   []anomaly.Anomaly
		OpenMoney   money.Cents
		Analysts    []staffRow
		Tasks       []taskView
		Commitments []world.Commitment
		Budgets     []budgetLine
		Halted      bool
		Halt        crew.DeskHalt
		Sort        sortSpec
		SortTrend   sortSpec
		SortService sortSpec
		SortTeam    sortSpec
		SortStaff   sortSpec
		SortBudget  sortSpec
	}{s.shellFor(r, name, "desks"), desk, period, months, byTeam, byService,
		trend, total, anoms, openMoney, analysts, views(tasks), commitments,
		budgets, halted, halt, srt, tsrt, vsrt, msrt, asrt, bsrt})
}

type budgetLine struct {
	Team   string
	Budget money.Cents
	Actual money.Cents
	Var    money.Cents
	VarPct float64
	HasPct bool
}

func estateBudgets(db *sql.DB, source, period string) ([]budgetLine, error) {
	rows, err := finops.BudgetsFor(db, source, period)
	if err != nil {
		return nil, err
	}
	out := make([]budgetLine, 0, len(rows))
	for _, b := range rows {
		pct, ok := money.Pct(b.Variance, b.Budget)
		out = append(out, budgetLine{b.Team, b.Budget, b.Actual, b.Variance, pct, ok})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Team < out[j].Team })
	return out, nil
}

// ------------------------------------------------------------- the indexes

type teamRow struct {
	world.Team
	Direct, Allocated, OpenMoney money.Cents
	Open                         int
}

func (t teamRow) Loaded() money.Cents { return t.Direct + t.Allocated }

func (s *Server) teams(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	period, months := s.period(r)
	alloc, err := finops.Allocate(s.db, period)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	anoms, _ := anomaly.List(s.db, anomaly.Filter{State: anomaly.Open})

	rows := make([]teamRow, 0, len(world.Teams))
	for _, t := range world.Teams {
		row := teamRow{Team: t}
		for _, tc := range alloc.Teams {
			if tc.Team == t.Name {
				row.Direct += tc.Direct
				row.Allocated += tc.Allocated
			}
		}
		for _, a := range anoms {
			if a.Team == t.Name {
				row.Open++
				row.OpenMoney += a.Excess.Abs()
			}
		}
		rows = append(rows, row)
	}

	srt := readSort(r, "loaded", true)
	applySort(rows, srt, map[string]func(a, b teamRow) int{
		"team":      func(a, b teamRow) int { return cmpString(a.Name, b.Name) },
		"unit":      func(a, b teamRow) int { return cmpString(a.Unit, b.Unit) },
		"direct":    func(a, b teamRow) int { return cmpInt64(int64(a.Direct), int64(b.Direct)) },
		"allocated": func(a, b teamRow) int { return cmpInt64(int64(a.Allocated), int64(b.Allocated)) },
		"loaded":    func(a, b teamRow) int { return cmpInt64(int64(a.Loaded()), int64(b.Loaded())) },
		"anomalies": func(a, b teamRow) int { return cmpInt(a.Open, b.Open) },
		"openmoney": func(a, b teamRow) int { return cmpInt64(int64(a.OpenMoney), int64(b.OpenMoney)) },
	}, "loaded")

	s.render(w, tplTeams, struct {
		shell
		Rows   []teamRow
		Period string
		Months []string
		Sort   sortSpec
	}{s.shellFor(r, "Teams", "teams"), rows, period, months, srt})
}

type deskRow struct {
	world.Desk
	Cost, OpenMoney money.Cents
	Share           float64
	Open, Analysts  int
}

func (s *Server) desks(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	period, months := s.period(r)
	totals, err := estate.Totals(s.db, period)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	anoms, _ := anomaly.List(s.db, anomaly.Filter{State: anomaly.Open})
	roster, _ := crew.Roster(s.db)

	var whole money.Cents
	for _, v := range totals {
		whole += v
	}
	rows := make([]deskRow, 0, len(world.Desks))
	for _, d := range world.Desks {
		row := deskRow{Desk: d, Cost: totals[d.Name]}
		if whole != 0 {
			row.Share = float64(row.Cost) / float64(whole) * 100
		}
		for _, a := range anoms {
			if a.Source == d.Name {
				row.Open++
				row.OpenMoney += a.Excess.Abs()
			}
		}
		for _, a := range roster {
			if a.Desk == d.Name {
				row.Analysts++
			}
		}
		rows = append(rows, row)
	}

	srt := readSort(r, "cost", true)
	applySort(rows, srt, map[string]func(a, b deskRow) int{
		"desk":      func(a, b deskRow) int { return cmpString(a.Name, b.Name) },
		"kind":      func(a, b deskRow) int { return cmpString(a.Kind, b.Kind) },
		"cost":      func(a, b deskRow) int { return cmpInt64(int64(a.Cost), int64(b.Cost)) },
		"share":     func(a, b deskRow) int { return cmpFloat(a.Share, b.Share) },
		"anomalies": func(a, b deskRow) int { return cmpInt(a.Open, b.Open) },
		"openmoney": func(a, b deskRow) int { return cmpInt64(int64(a.OpenMoney), int64(b.OpenMoney)) },
		"analysts":  func(a, b deskRow) int { return cmpInt(a.Analysts, b.Analysts) },
	}, "cost")

	s.render(w, tplDesks, struct {
		shell
		Rows   []deskRow
		Period string
		Months []string
		Sort   sortSpec
	}{s.shellFor(r, "Desks", "desks"), rows, period, months, srt})
}

// crewDeskExists says whether any analyst sits at this desk, which is a
// different question from whether a cloud bills to it.
func crewDeskExists(db *sql.DB, name string) bool {
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM analysts WHERE desk=?`, name).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

package web

import (
	"database/sql"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

//go:embed templates/*.html
var templateFS embed.FS

// Each page is parsed with the layout rather than into one big set, because
// every page defines a block called "content" and a single set would let the
// last one parsed win silently.
func page(name string) *template.Template {
	return template.Must(template.ParseFS(templateFS,
		"templates/layout.html", "templates/"+name))
}

var (
	tplOverview  = page("overview.html")
	tplAnomalies = page("anomalies.html")
	tplAnomaly   = page("anomaly.html")
	tplBudgets   = page("budgets.html")
)

// shell is what every page needs regardless of what it shows.
type shell struct {
	Title     string
	Nav       string
	CSRF      string
	Msg       string
	OpenCount int
}

func (s *Server) shellFor(r *http.Request, title, nav string) shell {
	open, _ := anomaly.List(s.db, anomaly.Filter{State: anomaly.Open})
	return shell{
		Title:     title,
		Nav:       nav,
		CSRF:      s.au.CSRFToken(s.sessionToken(r)),
		Msg:       r.URL.Query().Get("msg"),
		OpenCount: len(open),
	}
}

// guard refuses anyone without a session and sends them somewhere they can
// actually do something. Returning the user rather than a bool keeps the
// caller from having to look it up again.
//
// The distinction between /login and /signup is not a nicety. On a fresh
// install there is no account, so sending somebody to a sign-in form is a dead
// end: they stare at two fields with no credentials that could possibly work.
// Found by handing the thing over and being asked what the password was.
func (s *Server) guard(w http.ResponseWriter, r *http.Request) *auth.User {
	u := s.current(r)
	if u == nil {
		http.Redirect(w, r, s.entryPoint(), http.StatusSeeOther)
		return nil
	}
	return u
}

// entryPoint is /signup while the installation is unclaimed, /login after.
func (s *Server) entryPoint() string {
	if n, err := s.au.Count(); err == nil && n == 0 {
		return "/signup"
	}
	return "/login"
}

func (s *Server) render(w http.ResponseWriter, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		// The response is already partly written by now, so this cannot become
		// a clean 500. Say so in the log rather than pretending it rendered.
		fmt.Printf("costcrew: rendering %s: %v\n", t.Name(), err)
	}
}

func redirectMsg(w http.ResponseWriter, r *http.Request, to, msg string) {
	if msg != "" {
		to += "?msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// ---------------------------------------------------------------- overview

type deskLine struct {
	Name, Kind string
	Total      money.Cents
}

// mover is one service whose bill moved between the last two months.
//
// The number people ask for first is never the total, it is what CHANGED, and
// a total on its own answers a question nobody had.
type mover struct {
	Service string
	Source  string
	Now     money.Cents
	Was     money.Cents
	Change  money.Cents
}

// movers compares the two most recent months, per desk and service.
//
// Both months are read in the SAME query so a service that appeared this month
// and one that vanished last month are both present, with the missing side at
// zero rather than absent. A join would have dropped exactly the rows the page
// exists to show.
//
// through cuts the earlier month at the same day of the month the current one
// has reached. Eleven days against a whole month is not a comparison; it is a
// minus sign in front of every row on the page, and a reader who acts on it
// congratulates a team for a saving that is just the calendar.
func movers(db *sql.DB, now, prev, through string) ([]mover, error) {
	rows, err := db.Query(`SELECT source, service,
			SUM(CASE WHEN substr(day,1,7)=? THEN billed_cents ELSE 0 END),
			SUM(CASE WHEN substr(day,1,7)=? AND substr(day,9,2)<=? THEN billed_cents ELSE 0 END)
		FROM charges WHERE substr(day,1,7) IN (?,?)
		GROUP BY 1,2`, now, prev, through, now, prev)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mover
	for rows.Next() {
		var m mover
		var a, b int64
		if err := rows.Scan(&m.Source, &m.Service, &a, &b); err != nil {
			return nil, err
		}
		m.Now, m.Was = money.Cents(a), money.Cents(b)
		m.Change = m.Now - m.Was
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		return abs64(int64(out[i].Change)) > abs64(int64(out[j].Change))
	})
	if len(out) > 8 {
		out = out[:8]
	}
	return out, nil
}

// totalsThrough is estate.Totals for a month cut at a day of the month.
//
// It exists so an open month can be set beside the same span of the month
// before, which is the only comparison a part-month supports.
func totalsThrough(db *sql.DB, month, through string) (map[string]money.Cents, error) {
	rows, err := db.Query(`SELECT source, SUM(billed_cents) FROM charges
		WHERE substr(day,1,7)=? AND substr(day,9,2)<=? GROUP BY 1`, month, through)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]money.Cents{}
	for rows.Next() {
		var k string
		var v int64
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = money.Cents(v)
	}
	return out, rows.Err()
}

// prevMonth is the calendar month before a "2006-01" string.
func prevMonth(m string) string {
	t, err := time.Parse("2006-01", m)
	if err != nil {
		return m
	}
	return t.AddDate(0, -1, 0).Format("2006-01")
}

type deskTile struct {
	deskLine
	Was    money.Cents
	Change money.Cents
	HasChg bool
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	month := world.LastDay[:7]
	prev := prevMonth(month)
	totals, err := estate.Totals(s.db, month)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	// The month before, cut at the same day of the month, so every tile
	// carries a direction and not just a size, and the direction is real.
	//
	// The open month is a part-month. Set beside a whole one it loses by the
	// length of the calendar, which would paint the whole page green and tell
	// a reader the estate had halved its bill overnight.
	through := world.LastDay[8:]
	was, err := totalsThrough(s.db, prev, through)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}

	var desks []deskTile
	var thisMonth, lastMonth money.Cents
	for _, d := range world.Desks {
		t := deskTile{deskLine{d.Name, d.Kind, totals[d.Name]}, was[d.Name], 0, false}
		t.Change = t.Total - t.Was
		t.HasChg = t.Was != 0
		thisMonth += t.Total
		lastMonth += t.Was
		desks = append(desks, t)
	}

	open, _ := anomaly.List(s.db, anomaly.Filter{State: anomaly.Open})
	var waiting money.Cents
	for _, a := range open {
		waiting += a.Excess
	}
	counts, _ := anomaly.Counts(s.db)
	top := open
	if len(top) > 8 {
		top = top[:8]
	}

	mv, err := movers(s.db, month, prev, through)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}

	// Budget health is measured on the last CLOSED month, not the open one.
	//
	// A monthly guard against eleven days of spending is not exceeded yet by
	// arithmetic, so the open month reports zero over budget however badly it
	// is going. Zero is the number somebody would quote in a meeting, so the
	// page must not show it: it reports the last month that actually finished,
	// and says which month that was.
	var over, under int
	var overBy money.Cents
	for _, d := range world.Desks {
		lines, err := estate.BudgetVsActual(s.db, d.Name)
		if err != nil {
			continue
		}
		for _, b := range lines {
			if b.Month != prev {
				continue
			}
			if b.Variance > 0 {
				over++
				overBy += b.Variance
			} else {
				under++
			}
		}
	}

	board, _ := crew.Tasks(s.db, crew.TaskFilter{})
	openTasks := 0
	for _, t := range board {
		if t.State != "done" {
			openTasks++
		}
	}
	pending, _ := crew.AwaitingStamp(s.db)
	stamps := len(pending)

	change := thisMonth - lastMonth
	pct := 0.0
	if lastMonth != 0 {
		pct = float64(change) / float64(lastMonth) * 100
	}

	s.render(w, tplOverview, struct {
		shell
		Month, Prev string
		Through     string
		Desks       []deskTile
		Open        []anomaly.Anomaly
		OpenN       int
		Waiting     money.Cents
		Counts      []struct {
			State anomaly.State
			N     int
		}
		Movers    []mover
		ThisMonth money.Cents
		LastMonth money.Cents
		Change    money.Cents
		Pct       float64
		HasPct    bool
		Over      int
		Under     int
		OverBy    money.Cents
		OpenTasks int
		Stamps    int
	}{s.shellFor(r, "Overview", "overview"), month, prev, through, desks, top, len(open),
		waiting, counts, mv, thisMonth, lastMonth, change, pct, lastMonth != 0,
		over, under, overBy, openTasks, stamps})
}

// --------------------------------------------------------------- anomalies

type stateTile struct {
	State anomaly.State
	N     int
	Hint  string
}

var stateHints = map[anomaly.State]string{
	anomaly.Open:      "nobody has looked",
	anomaly.Triaged:   "an analyst owns it",
	anomaly.Explained: "an answer, awaiting a person",
	anomaly.Accepted:  "the answer stands",
	anomaly.Dismissed: "not pursued, and why",
}

func (s *Server) anomalies(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	f := anomaly.Filter{
		State:     anomaly.State(r.URL.Query().Get("state")),
		Source:    r.URL.Query().Get("source"),
		Direction: r.URL.Query().Get("direction"),
		CausedBy:  r.URL.Query().Get("caused_by"),
		HandledBy: r.URL.Query().Get("handled_by"),
	}
	rows, err := anomaly.List(s.db, f)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	counts, err := anomaly.Counts(s.db)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	tiles := make([]stateTile, 0, len(counts))
	for _, c := range counts {
		tiles = append(tiles, stateTile{c.State, c.N, stateHints[c.State]})
	}
	srt := readSort(r, "money", true)
	applySort(rows, srt, map[string]func(a, b anomaly.Anomaly) int{
		"money":   func(a, b anomaly.Anomaly) int { return cmpInt64(int64(a.Excess.Abs()), int64(b.Excess.Abs())) },
		"day":     func(a, b anomaly.Anomaly) int { return cmpString(a.Day, b.Day) },
		"source":  func(a, b anomaly.Anomaly) int { return cmpString(a.Source, b.Source) },
		"team":    func(a, b anomaly.Anomaly) int { return cmpString(a.Team, b.Team) },
		"service": func(a, b anomaly.Anomaly) int { return cmpString(a.Service, b.Service) },
		"caused":  func(a, b anomaly.Anomaly) int { return cmpString(a.CausedBy, b.CausedBy) },
		"handled": func(a, b anomaly.Anomaly) int { return cmpString(a.HandledBy, b.HandledBy) },
		"state":   func(a, b anomaly.Anomaly) int { return cmpString(string(a.State), string(b.State)) },
		"z":       func(a, b anomaly.Anomaly) int { return cmpFloat(a.Z, b.Z) },
	}, "money")

	openRows, _ := anomaly.List(s.db, anomaly.Filter{State: anomaly.Open})
	var openMoney money.Cents
	for _, a := range openRows {
		openMoney += a.Excess.Abs()
	}

	var sources []string
	for _, d := range world.Desks {
		sources = append(sources, d.Name)
	}
	s.render(w, tplAnomalies, struct {
		shell
		Rows      []anomaly.Anomaly
		Counts    []stateTile
		States    []anomaly.State
		Sources   []string
		F         anomaly.Filter
		OpenMoney money.Cents
		Sort      sortSpec
	}{
		s.shellFor(r, "Anomalies", "anomalies"), rows, tiles,
		[]anomaly.State{anomaly.Open, anomaly.Triaged, anomaly.Explained,
			anomaly.Accepted, anomaly.Dismissed},
		sources, f, openMoney, srt,
	})
}

func (s *Server) anomalyPage(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	a, err := anomaly.Get(s.db, r.PathValue("id"))
	if err != nil {
		http.Error(w, "no such anomaly", http.StatusNotFound)
		return
	}
	days, vals, err := estate.SeriesDays(s.db, estate.SeriesKey{
		Source: a.Source, Team: a.Team, Service: a.Service})
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}

	s.render(w, tplAnomaly, struct {
		shell
		A          anomaly.Anomaly
		Spark      template.HTML
		ZText      string
		Analysts   []string
		Actionable bool
	}{
		s.shellFor(r, "Anomaly "+a.ID, "anomalies"),
		a,
		spark(days, vals, a.Day, a.Baseline, a.Direction),
		strconv.FormatFloat(a.Z, 'f', 1, 64),
		s.activeAnalysts(),
		a.State != anomaly.Accepted && a.State != anomaly.Dismissed,
	})
}

// ----------------------------------------------------------------- actions

func (s *Server) anomalyAction(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.guard(w, r)
		if u == nil {
			return
		}
		id := r.PathValue("id")
		back := "/anomalies/" + id
		if err := r.ParseForm(); err != nil {
			redirectMsg(w, r, back, "reload the page and try again")
			return
		}
		if !s.au.CSRFOK(s.sessionToken(r), r.PostFormValue("csrf")) {
			redirectMsg(w, r, back, "reload the page and try again")
			return
		}
		// Acting costs money and moves state, which is exactly where the line
		// between a viewer and an operator is drawn.
		if !u.May("operator") {
			redirectMsg(w, r, back, "your account may read and export, but not act")
			return
		}

		var err error
		switch kind {
		case "assign":
			err = anomaly.Assign(s.db, id, r.PostFormValue("analyst"), s.rec)
		case "explain":
			err = anomaly.Explain(s.db, id, r.PostFormValue("reason"), s.rec)
		case "dismiss":
			err = anomaly.Dismiss(s.db, id, r.PostFormValue("reason"), s.rec)
		}
		switch {
		case err == nil:
			redirectMsg(w, r, back, "")
		case strings.Contains(err.Error(), "needs a reason"):
			redirectMsg(w, r, back, "that needs a reason: without one nobody can tell it from not having looked")
		case strings.Contains(err.Error(), "already closed"):
			redirectMsg(w, r, back, "this anomaly is already closed")
		default:
			redirectMsg(w, r, back, "that did not work: "+err.Error())
		}
	}
}

// ------------------------------------------------------------------ others

func (s *Server) budgets(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "aws"
	}
	rows, err := estate.BudgetVsActual(s.db, source)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	var sources []string
	for _, d := range world.Desks {
		sources = append(sources, d.Name)
	}
	srt := readSort(r, "month", true)
	applySort(rows, srt, map[string]func(a, b estate.BudgetRow) int{
		"month":  func(a, b estate.BudgetRow) int { return cmpString(a.Month, b.Month) },
		"team":   func(a, b estate.BudgetRow) int { return cmpString(a.Team, b.Team) },
		"budget": func(a, b estate.BudgetRow) int { return cmpInt64(int64(a.Budget), int64(b.Budget)) },
		"actual": func(a, b estate.BudgetRow) int { return cmpInt64(int64(a.Actual), int64(b.Actual)) },
		"var":    func(a, b estate.BudgetRow) int { return cmpInt64(int64(a.Variance), int64(b.Variance)) },
		"pct":    func(a, b estate.BudgetRow) int { return cmpFloat(a.VariancePct, b.VariancePct) },
	}, "month")

	// Over and under are kept apart rather than netted. A desk that is nine
	// thousand over on one team and nine thousand under on another is not a
	// desk on budget, and a single net figure says it is.
	var over, under money.Cents
	for _, b := range rows {
		if b.Variance > 0 {
			over += b.Variance
		} else {
			under += -b.Variance
		}
	}
	s.render(w, tplBudgets, struct {
		shell
		Rows        []estate.BudgetRow
		Sources     []string
		Source      string
		Over, Under money.Cents
		Sort        sortSpec
	}{s.shellFor(r, "Budgets", "budgets"), rows, sources, source, over, under, srt})
}

// ---------------------------------------------------------------- sparkline

// spark draws the series with its baseline and the day in question marked.
//
// Hand-built SVG rather than a chart library: it is forty lines, it has no
// dependency, and a console that cannot draw a line without three hundred
// kilobytes of JavaScript is one that stops working the day that library does.
func spark(days []string, vals []money.Cents, mark string, baseline money.Cents, dir string) template.HTML {
	if len(vals) < 2 {
		return ""
	}
	// The last hundred days, which is enough context to see the shape without
	// squeezing a year into six hundred pixels.
	if len(vals) > 100 {
		days, vals = days[len(days)-100:], vals[len(vals)-100:]
	}
	const w, h, pad = 640.0, 92.0, 6.0
	max := baseline
	for _, v := range vals {
		if v > max {
			max = v
		}
	}
	if max == 0 {
		max = 1
	}
	x := func(i int) float64 { return pad + float64(i)*(w-2*pad)/float64(len(vals)-1) }
	y := func(v money.Cents) float64 {
		return h - pad - float64(v)/float64(max)*(h-2*pad)
	}

	var line, area strings.Builder
	for i, v := range vals {
		cmd := "L"
		if i == 0 {
			cmd = "M"
		}
		fmt.Fprintf(&line, "%s%.1f %.1f ", cmd, x(i), y(v))
	}
	fmt.Fprintf(&area, "M%.1f %.1f %sL%.1f %.1f Z",
		x(0), h-pad, strings.TrimPrefix(line.String(), "M"), x(len(vals)-1), h-pad)

	var dot string
	for i, d := range days {
		if d == mark {
			cls := "mark"
			if dir == "down" {
				cls = "mark down"
			}
			dot = fmt.Sprintf(`<circle class="%s" cx="%.1f" cy="%.1f" r="3.5"/>`,
				cls, x(i), y(vals[i]))
			break
		}
	}

	return template.HTML(fmt.Sprintf(
		`<svg class="spark" viewBox="0 0 %.0f %.0f" preserveAspectRatio="none" role="img" `+
			`aria-label="Daily spend for this series, with the baseline and the flagged day marked">`+
			`<path class="band" d="%s"/><path class="line" d="%s"/>`+
			`<line class="base" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>%s</svg>`,
		w, h, area.String(), strings.TrimSpace(line.String()),
		pad, y(baseline), w-pad, y(baseline), dot))
}

package web

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/auth"
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
	tplCrew      = page("crew.html")
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

// guard refuses anyone without a session and sends them to sign in. Returning
// the user rather than a bool keeps the caller from having to look it up again.
func (s *Server) guard(w http.ResponseWriter, r *http.Request) *auth.User {
	u := s.current(r)
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return nil
	}
	return u
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

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	month := world.LastDay[:7]
	totals, err := estate.Totals(s.db, month)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	var desks []deskLine
	for _, d := range world.Desks {
		desks = append(desks, deskLine{d.Name, d.Kind, totals[d.Name]})
	}
	open, _ := anomaly.List(s.db, anomaly.Filter{State: anomaly.Open})
	if len(open) > 8 {
		open = open[:8]
	}
	s.render(w, tplOverview, struct {
		shell
		Month string
		Desks []deskLine
		Open  []anomaly.Anomaly
	}{s.shellFor(r, "Overview", "overview"), month, desks, open})
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
	}{
		s.shellFor(r, "Anomalies", "anomalies"), rows, tiles,
		[]anomaly.State{anomaly.Open, anomaly.Triaged, anomaly.Explained,
			anomaly.Accepted, anomaly.Dismissed},
		sources, f, openMoney,
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

	var analysts []string
	for _, c := range world.Crew {
		if c.State == world.Active {
			analysts = append(analysts, c.Name)
		}
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
		analysts,
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
	s.render(w, tplBudgets, struct {
		shell
		Rows    []estate.BudgetRow
		Sources []string
		Source  string
	}{s.shellFor(r, "Budgets", "budgets"), rows, sources, source})
}

func (s *Server) crew(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	s.render(w, tplCrew, struct {
		shell
		Crew      []world.Agent
		DeskCount int
	}{s.shellFor(r, "Crew", "crew"), world.Crew, len(world.Desks)})
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

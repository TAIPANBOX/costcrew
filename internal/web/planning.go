package web

import (
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

var (
	tplForecast   = page("forecast.html")
	tplExplainers = page("explainers.html")
	tplPlan       = page("plan.html")
)

// ---------------------------------------------------------------- forecast

type projRow struct {
	Source string
	Amount money.Cents
}

func (s *Server) forecast(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	// The OPEN month, not the last closed one: a forecast is about a month
	// that has not finished, which is the opposite of every other page here.
	open := world.LastDay[:7]
	proj, basis, err := finops.Project(s.db, open)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	var rows []projRow
	for _, d := range world.Desks {
		if v, ok := proj[d.Name]; ok {
			rows = append(rows, projRow{d.Name, v})
		}
	}
	psrt := readSortNamed(r, "psort", "amount", true)
	applySort(rows, psrt, map[string]func(a, b projRow) int{
		"desk":   func(a, b projRow) int { return cmpString(a.Source, b.Source) },
		"amount": func(a, b projRow) int { return cmpInt64(int64(a.Amount), int64(b.Amount)) },
	}, "amount")
	frozen, _ := finops.IsFrozen(s.db, open)
	history, err := finops.Forecasts(s.db, open)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	acc, scored, hasAcc, _ := finops.Accuracy(s.db, open)

	spec := readSortNamed(r, "hsort", "period", true)
	applySort(history, spec, map[string]func(x, y finops.Forecast) int{
		"period":   func(x, y finops.Forecast) int { return cmpString(x.Period, y.Period) },
		"desk":     func(x, y finops.Forecast) int { return cmpString(x.Source, y.Source) },
		"forecast": func(x, y finops.Forecast) int { return cmpInt64(int64(x.Forecast), int64(y.Forecast)) },
		"actual":   func(x, y finops.Forecast) int { return cmpInt64(int64(x.Actual), int64(y.Actual)) },
		"error":    func(x, y finops.Forecast) int { return cmpFloat(absf(x.ErrorPct), absf(y.ErrorPct)) },
		"grade":    func(x, y finops.Forecast) int { return cmpString(x.Grade, y.Grade) },
	}, "period")

	s.render(w, tplForecast, struct {
		shell
		Period         string
		Basis          string
		Projection     []projRow
		Rows           []finops.Forecast
		Frozen         bool
		Accuracy       float64
		Scored         int
		HasAccuracy    bool
		Ladder         string
		CanAct         bool
		Sort           sortSpec
		SortProjection sortSpec
	}{s.shellFor(r, "Forecast", "forecast"), open, basis, rows, history,
		frozen, acc, scored, hasAcc, finops.LadderText(), u.May("operator"), spec, psrt})
}

func (s *Server) freezeForecast(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	if !s.checked(w, r, "/forecast", u) {
		return
	}
	p := r.PostFormValue("period")
	if err := finops.Freeze(s.db, p, u.Username); err != nil {
		redirectMsg(w, r, "/forecast", err.Error())
		return
	}
	if s.rec != nil {
		_ = s.rec.Emit("forecast_frozen", "forecaster", "info", map[string]any{
			"period": p, "by": u.Username,
		}, s.delegation(u.Username, "forecaster"))
	}
	redirectMsg(w, r, "/forecast", "")
}

// -------------------------------------------------------------- explainers

type explainerView struct {
	crew.Explainer
	Rendered template.HTML
}

func (s *Server) explainers(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	list, err := crew.Explainers(s.db)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	rows := make([]explainerView, 0, len(list))
	for _, e := range list {
		rows = append(rows, explainerView{e, renderBody(e.Body)})
	}
	teams := make([]string, 0, len(world.Teams))
	for _, t := range world.Teams {
		teams = append(teams, t.Name)
	}
	s.render(w, tplExplainers, struct {
		shell
		Rows     []explainerView
		Teams    []string
		Analysts []string
		CanAct   bool
	}{s.shellFor(r, "Explainers", "explainers"), rows, teams,
		s.activeAnalysts(), u.May("operator")})
}

func (s *Server) commissionExplainer(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	if !s.checked(w, r, "/explainers", u) {
		return
	}
	amount, err := money.Parse(r.PostFormValue("amount"))
	if err != nil {
		redirectMsg(w, r, "/explainers", "the amount must look like 500.00")
		return
	}
	_, err = crew.Commission(s.db, r.PostFormValue("team"), r.PostFormValue("topic"),
		"the team", r.PostFormValue("author"), amount)
	s.done(w, r, "/explainers", err)
}

func (s *Server) explainerAction(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.guard(w, r)
		if u == nil {
			return
		}
		if !s.checked(w, r, "/explainers", u) {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			redirectMsg(w, r, "/explainers", "no such explainer")
			return
		}
		if kind == "publish" {
			// Recorded as the person's act. Something written in a team's name
			// and sent without review is how a practice loses a team it needs.
			err = crew.Publish(s.db, id, u.Username)
			if err == nil && s.rec != nil {
				if e, e2 := crew.GetExplainer(s.db, id); e2 == nil {
					_ = s.rec.Emit("explainer_published", e.Author, "info", map[string]any{
						"team": e.Team, "topic": e.Topic, "published_by": u.Username,
					}, s.delegation(u.Username, e.Author))
				}
			}
		} else {
			err = crew.ReturnExplainer(s.db, id, r.PostFormValue("reason"))
		}
		s.done(w, r, "/explainers", err)
	}
}

// ---------------------------------------------------------------- planning

func (s *Server) planPage(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	label, start, end := nextWeek(s)
	goal := r.URL.Query().Get("goal")
	p, err := crew.Propose(s.db, label, start, end, goal)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	// Default: the guard, biggest first. A plan is read to decide what to cut,
	// and the thing you cut is the expensive one.
	srt := readSort(r, "guard", true)
	applySort(p.Items, srt, map[string]func(a, b crew.PlanItem) int{
		"task":    func(a, b crew.PlanItem) int { return cmpString(a.Title, b.Title) },
		"analyst": func(a, b crew.PlanItem) int { return cmpString(a.Assignee, b.Assignee) },
		"desk":    func(a, b crew.PlanItem) int { return cmpString(a.Desk, b.Desk) },
		"guard":   func(a, b crew.PlanItem) int { return cmpInt64(int64(a.Budget), int64(b.Budget)) },
		"because": func(a, b crew.PlanItem) int { return cmpString(a.Why, b.Why) },
	}, "guard")
	s.render(w, tplPlan, struct {
		shell
		P      crew.Plan
		CanAct bool
		Sort   sortSpec
	}{s.shellFor(r, "Plan a sprint", "sprints"), p, u.May("operator"), srt})
}

// nextWeek is the Monday after the last sprint on the board, not after today:
// the estate is dated, and planning against the wall clock would propose a
// sprint months away from the data it is about.
func nextWeek(s *Server) (label, start, end string) {
	var last string
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(finish),'') FROM sprints`).Scan(&last)
	base, err := time.Parse("2006-01-02", last)
	if err != nil {
		base, _ = time.Parse("2006-01-02", world.LastDay)
	}
	d := base.AddDate(0, 0, 1)
	for d.Weekday() != time.Monday {
		d = d.AddDate(0, 0, 1)
	}
	y, wk := d.ISOWeek()
	return strconvWeek(y, wk), d.Format("2006-01-02"), d.AddDate(0, 0, 6).Format("2006-01-02")
}

func strconvWeek(y, w int) string {
	return strconv.Itoa(y) + "-W" + pad2(w)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func (s *Server) approvePlan(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	if !s.checked(w, r, "/sprint/plan", u) {
		return
	}
	p, err := crew.Propose(s.db, r.PostFormValue("label"),
		r.PostFormValue("start"), r.PostFormValue("end"), r.PostFormValue("goal"))
	if err != nil {
		redirectMsg(w, r, "/sprint/plan", err.Error())
		return
	}
	// "owner": a web session is always a person, and sprint.approve is the
	// owner's class (ROLES-2026-09.md section 1).
	n, err := crew.Approve(s.db, p, "owner")
	if err != nil {
		redirectMsg(w, r, "/sprint/plan", err.Error())
		return
	}
	if s.rec != nil {
		_ = s.rec.Emit("sprint_planned", "supervisor", "info", map[string]any{
			"sprint": p.Label, "tasks": n, "approved_by": u.Username,
		}, s.delegation(u.Username, "supervisor"))
	}
	redirectMsg(w, r, "/sprints", "")
}

func (s *Server) closeSprint(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "no such sprint", http.StatusNotFound)
		return
	}
	back := "/sprint/" + strconv.Itoa(id)
	if !s.checked(w, r, back, u) {
		return
	}
	stillOpen, err := crew.CloseSprint(s.db, id)
	if err != nil {
		redirectMsg(w, r, back, err.Error())
		return
	}
	// Open work is NOT closed with the sprint. A sprint that tidies itself up
	// on the way out hides exactly what a retrospective needs to see.
	msg := ""
	if stillOpen > 0 {
		msg = "closed, and " + strconv.Itoa(stillOpen) +
			" tasks are still open: they stay open, because they did not finish"
	}
	redirectMsg(w, r, back, msg)
}

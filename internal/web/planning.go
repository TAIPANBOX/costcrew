package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/stack"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

var (
	tplForecast   = page("forecast.html")
	tplExplainers = page("explainers.html")
	tplPlan       = page("plan.html")
)

// ---------------------------------------------------------------- forecast

// projRow is one desk's own driver-aware projection: C3-SPEC.md's own
// figure, basis and driver lines, computed per desk because
// finops.ProjectWithDrivers is a per-desk question (a driver's scope and
// window belong to one desk, never to the whole estate at once).
type projRow struct {
	Source  string
	Amount  money.Cents
	Basis   string
	Drivers []finops.DriverLine
}

func (s *Server) forecast(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	// The OPEN month, not the last closed one: a forecast is about a month
	// that has not finished, which is the opposite of every other page here.
	open := world.LastDay[:7]
	var rows []projRow
	for _, d := range world.Desks {
		amt, basis, lines, err := finops.ProjectWithDrivers(s.db, d.Name, open)
		if err != nil {
			// This desk has nothing landed yet this month: the same
			// "no row for this desk" a plain map lookup used to produce
			// silently, kept explicit here rather than treated as a store
			// failure.
			continue
		}
		rows = append(rows, projRow{d.Name, amt, basis, lines})
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
	}{s.shellFor(r, "Forecast", "forecast"), open, rows, history,
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
	// TeamIsReal guards the page's own team link: C8-SPEC.md's executive
	// pack publishes with Team "leadership", which is not one of
	// world.Teams's ten and would otherwise render a dead link to
	// /team/leadership (drill.go's team() answers 404 for any name that is
	// not a real one). A row whose Team IS real still links, exactly as
	// before this field existed.
	TeamIsReal bool
}

func isRealTeam(name string) bool {
	for _, t := range world.Teams {
		if t.Name == name {
			return true
		}
	}
	return false
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
	// ?audience=leadership is C8-SPEC.md section 2's "the leadership page":
	// the explainers page filtered to the leadership audience. Every OTHER
	// explainer's Audience is the fixed string "the team" (commissionExplainer
	// below); the executive pack's own is "leadership"
	// (internal/finops.applyExplainerPublish). Empty (the ordinary page) shows
	// every row, unfiltered, exactly as before this parameter existed.
	audience := r.URL.Query().Get("audience")
	rows := make([]explainerView, 0, len(list))
	for _, e := range list {
		if audience != "" && e.Audience != audience {
			continue
		}
		rows = append(rows, explainerView{e, renderBody(e.Body), isRealTeam(e.Team)})
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
		Audience string
	}{s.shellFor(r, "Explainers", "explainers"), rows, teams,
		s.activeAnalysts(), u.May("operator"), audience})
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

// planAskMaxTokens mirrors cadence.go's own dueEstimateMaxTokens and
// tools/run's -max-tokens default: the output cap the one planning call is
// priced and made with.
const planAskMaxTokens = 2000

// planItemSort is the sort map both the deterministic and the model's own
// table share: one PlanItem shape, one set of column names.
var planItemSort = map[string]func(a, b crew.PlanItem) int{
	"task":    func(a, b crew.PlanItem) int { return cmpString(a.Title, b.Title) },
	"analyst": func(a, b crew.PlanItem) int { return cmpString(a.Assignee, b.Assignee) },
	"desk":    func(a, b crew.PlanItem) int { return cmpString(a.Desk, b.Desk) },
	"guard":   func(a, b crew.PlanItem) int { return cmpInt64(int64(a.Budget), int64(b.Budget)) },
	"because": func(a, b crew.PlanItem) int { return cmpString(a.Why, b.Why) },
}

// planPageView is what templates/plan.html renders, for GET /sprint/plan
// (unchanged: Asked stays false and every Ask* field its zero value, so
// nothing extra renders) and for POST /sprint/plan/ask (the same template,
// with the extra fields filled in) alike -- one shape, so the ask
// handler's render call needs no template of its own.
type planPageView struct {
	shell
	P      crew.Plan
	CanAct bool
	Sort   sortSpec

	// Populated only by askPlan. Asked distinguishes "never asked" (GET) from
	// "asked and the model named zero items" (ModelPlanOK true, ModelPlan.Items
	// empty), which a zero-valued ModelPlan alone could not.
	Asked        bool
	AskRefusal   string // non-empty: the ask itself, or the model's answer, was refused
	AskRawAnswer string // the model's own text, shown WHOLE beside a refusal reason
	ModelPlan    crew.Plan
	ModelPlanOK  bool // true: ModelPlan is a real, approvable plan
}

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
	applySort(p.Items, srt, planItemSort, "guard")
	s.render(w, tplPlan, planPageView{
		shell: s.shellFor(r, "Plan a sprint", "sprints"), P: p, CanAct: u.May("operator"), Sort: srt,
	})
}

// askPlan is B4-STEP-TWO-SPEC.md section 4: prices the supervisor's one
// planning call, refuses before making it when the worst case is over its
// own PerTask or no gateway is configured, makes it, validates the answer,
// settles the actual cost and journals plan_asked either way, then renders
// the SAME plan page with the model's plan (or its refusal) alongside the
// deterministic one.
//
// It renders directly rather than redirecting -- the one handler in this
// package that does, besides internal/web/intake.go's own preview step,
// which this mirrors: a two-step "show what would happen, then let a
// separate act commit it" flow, where round-tripping the shown answer
// through a hidden form field (verified again, from scratch, at the second
// step) is safer than trusting a server-side copy of what an earlier
// moment produced.
func (s *Server) askPlan(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	if !s.checked(w, r, "/sprint/plan", u) {
		return
	}
	label := r.PostFormValue("label")
	start := r.PostFormValue("start")
	end := r.PostFormValue("end")
	goal := r.PostFormValue("goal")

	det, err := crew.Propose(s.db, label, start, end, goal)
	if err != nil {
		redirectMsg(w, r, "/sprint/plan", err.Error())
		return
	}
	srt := readSort(r, "guard", true)
	applySort(det.Items, srt, planItemSort, "guard")

	view := planPageView{
		shell: s.shellFor(r, "Plan a sprint", "sprints"), P: det, CanAct: u.May("operator"),
		Sort: srt, Asked: true,
	}

	month := ""
	if len(start) >= 7 {
		month = start[:7]
	}

	roster, err := crew.Roster(s.db)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	var sup crew.Analyst
	supOK := false
	for _, a := range roster {
		if a.Name == "supervisor" {
			sup, supOK = a, true
		}
	}
	if !supOK || sup.State != "active" {
		s.refusePlanAsk(w, view, u, label, month, 0, "no active supervisor analyst is on the roster")
		return
	}

	spent, err := crew.SpendInMonth(s.db, month)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}

	packet := deliver.PlanPacket(s.db, det, roster, spent)
	prompt := deliver.PlanPrompt(sup, packet)
	worstMicros, model, priced := deliver.PlanWorstCase(sup, prompt, planAskMaxTokens)
	if !priced {
		s.refusePlanAsk(w, view, u, label, month, 0, fmt.Sprintf(
			"%s cannot be priced (an unknown or unmetered engine), so the call is refused before it is made", sup.Engine))
		return
	}
	worstCents := money.Cents((worstMicros + 9_999) / 10_000)
	if worstCents > sup.PerTask {
		s.refusePlanAsk(w, view, u, label, month, 0, fmt.Sprintf(
			"the worst case for this call is %s, over the supervisor's own per-task guard %s: "+
				"refused before it is made", worstCents, sup.PerTask))
		return
	}
	// The console never falls back to calling a vendor directly. tools/run's
	// own -live does, when -gateway is unset; this handler does not, the
	// same rule tools/bench's own -live keeps ("-live needs -gateway: the
	// bench's spend must be metered exactly like the crew's, through the
	// same TokenFuse gateway, never a direct call"). A browser click that
	// can spend real money should never fall back to an unmetered direct
	// call merely because nobody configured routing.
	if s.gateway == "" {
		s.refusePlanAsk(w, view, u, label, month, 0,
			"no TokenFuse gateway is configured for this console; the supervisor's spend must "+
				"be metered through it, so the call is refused rather than made directly")
		return
	}

	runID := fmt.Sprintf("plan-ask-%d", time.Now().UTC().UnixNano())
	gw := deliver.Gateway{
		URL: s.gateway, RunID: runID, AgentID: stack.AgentURI(s.host, "supervisor"),
		BudgetUSD: deliver.GatewayBudgetUSD(sup.PerTask, sup.PerTask),
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	res, callErr := deliver.Call(ctx, sup.Engine, model, prompt, planAskMaxTokens, gw)
	if callErr != nil {
		reason := callErr.Error()
		var gwr deliver.GatewayRefusal
		if errors.As(callErr, &gwr) {
			reason = gwr.Error()
		}
		// The call reached the gateway (or the vendor) and failed there,
		// which is not the same as never having been priced: nothing was
		// actually spent, so this settles zero, same as the pre-call
		// refusals above.
		s.refusePlanAsk(w, view, u, label, month, 0, "the call could not be made: "+reason)
		return
	}

	price, _ := engines.PriceFor(sup.Engine, model) // known good: PlanWorstCase already confirmed priced
	actualMicros := deliver.WorstCaseMicros(res.InTokens, res.OutTokens, price)

	// AskRawAnswer is set on every branch, accepted included: it is not only
	// what a refusal shows, it is also the /sprint/plan/approve-model form's
	// own hidden "answer" field, round-tripped so that handler can
	// re-validate the SAME text from scratch rather than trust a stored
	// copy of what this moment accepted.
	items, found, reason := crew.ValidatePlanAnswer(res.Text, det, roster, spent)
	view.AskRawAnswer = res.Text
	switch {
	case !found:
		view.AskRefusal = "the model's answer carried no fenced ```plan block"
	case reason != "":
		view.AskRefusal = reason
	default:
		mp := modelPlanFrom(det, items)
		applySort(mp.Items, srt, planItemSort, "guard")
		view.ModelPlan, view.ModelPlanOK = mp, true
	}

	outcome := crew.PlanAskAccepted
	if view.AskRefusal != "" {
		outcome = crew.PlanAskRefused
	}
	cents, err := crew.SettlePlanAsk(s.db, label, month, "supervisor", actualMicros, outcome, view.AskRefusal)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	s.journalPlanAsked(u, label, cents, string(outcome), view.AskRefusal)
	s.render(w, tplPlan, view)
}

// refusePlanAsk settles whatever the ask cost (0 for every case that calls
// this: every caller above is a refusal BEFORE deliver.Call ever runs),
// journals it, and renders the page with the refusal shown.
func (s *Server) refusePlanAsk(w http.ResponseWriter, view planPageView, u *auth.User, label, month string, micros int64, reason string) {
	view.AskRefusal = reason
	cents, err := crew.SettlePlanAsk(s.db, label, month, "supervisor", micros, crew.PlanAskRefused, reason)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	s.journalPlanAsked(u, label, cents, string(crew.PlanAskRefused), reason)
	s.render(w, tplPlan, view)
}

// journalPlanAsked is section 4's own words: "journals sprint_planned-style
// plan_asked with the cost and the outcome (accepted, refused with
// reason)".
func (s *Server) journalPlanAsked(u *auth.User, label string, cost money.Cents, outcome, reason string) {
	if s.rec == nil {
		return
	}
	data := map[string]any{
		"sprint": label, "cost_cents": int64(cost), "outcome": outcome, "asked_by": u.Username,
	}
	if reason != "" {
		data["reason"] = reason
	}
	_ = s.rec.Emit("plan_asked", "supervisor", "info", data, s.delegation(u.Username, "supervisor"))
}

// modelPlanFrom rebuilds a crew.Plan from the model's own validated items:
// Title, Goal, Desk and Skill are the deterministic item's own (never the
// model's to invent); Assignee, Budget and Why are the model's.
func modelPlanFrom(det crew.Plan, items []crew.PlanAnswerItem) crew.Plan {
	mp := crew.Plan{Label: det.Label, Start: det.Start, End: det.End,
		Goal: det.Goal, TypedGoal: det.TypedGoal, Existing: det.Existing}
	for _, pa := range items {
		d := det.Items[pa.Ref-1]
		it := crew.PlanItem{Title: d.Title, Goal: d.Goal, Assignee: pa.Assignee,
			Desk: d.Desk, Budget: pa.Budget, Why: pa.Why, Skill: d.Skill}
		mp.Items = append(mp.Items, it)
		mp.Budget += it.Budget
	}
	return mp
}

// approveModelPlan is the model plan's own approve form: it re-validates
// the model's raw answer (posted back in a hidden field, escaped by
// html/template on the way out and decoded by the browser on the way back
// in, never trusted as-is) against a FRESHLY computed deterministic plan
// and roster, exactly the way askPlan validated it the first time, so
// nothing here trusts a stale copy of what an earlier moment accepted --
// crew.Approve itself is unchanged, called with a crew.Plan this handler
// builds the same way askPlan's own preview did.
func (s *Server) approveModelPlan(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	if !s.checked(w, r, "/sprint/plan", u) {
		return
	}
	label := r.PostFormValue("label")
	start := r.PostFormValue("start")
	end := r.PostFormValue("end")
	goal := r.PostFormValue("goal")
	answer := r.PostFormValue("answer")

	det, err := crew.Propose(s.db, label, start, end, goal)
	if err != nil {
		redirectMsg(w, r, "/sprint/plan", err.Error())
		return
	}
	roster, err := crew.Roster(s.db)
	if err != nil {
		redirectMsg(w, r, "/sprint/plan", "store unavailable")
		return
	}
	month := ""
	if len(start) >= 7 {
		month = start[:7]
	}
	spent, err := crew.SpendInMonth(s.db, month)
	if err != nil {
		redirectMsg(w, r, "/sprint/plan", "store unavailable")
		return
	}

	items, found, reason := crew.ValidatePlanAnswer(answer, det, roster, spent)
	if !found || reason != "" {
		msg := "the model's plan no longer re-validates against the current roster; ask again"
		if reason != "" {
			msg += ": " + reason
		}
		redirectMsg(w, r, "/sprint/plan", msg)
		return
	}
	mp := modelPlanFrom(det, items)
	mp.Existing = det.Existing

	// "owner": a web session is always a person, and sprint.approve is the
	// owner's class (ROLES-2026-09.md section 1) -- the same actor
	// approvePlan already passes for the deterministic plan.
	n, err := crew.Approve(s.db, mp, "owner")
	if err != nil {
		redirectMsg(w, r, "/sprint/plan", err.Error())
		return
	}
	if s.rec != nil {
		_ = s.rec.Emit("sprint_planned", "supervisor", "info", map[string]any{
			"sprint": mp.Label, "tasks": n, "approved_by": u.Username, "source": "model",
		}, s.delegation(u.Username, "supervisor"))
	}
	redirectMsg(w, r, "/sprints", "")
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

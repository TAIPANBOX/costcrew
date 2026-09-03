package crew

// Sprint planning: B4-SPEC.md step one. Extracted from crew.go's own
// "planning" section because the five-source, skill-routed version below is
// long enough to want its own file, the same way options.go and decision.go
// each hold one B3 concern; Plan, PlanItem and Approve's caller-facing shape
// do not change here, and Approve itself stays in crew.go, untouched.
//
// `@yurii 2026-09-02`, on what the supervisor is for: "він вже сам
// розподіляє це все між агентами... в залежності від моделі, від всього,
// задачі." And on the chain of command: "Вони мають вирішувати це все
// згідно своїх посадових інструкцій."
//
// The five sources (B4-SPEC.md section 2): unowned anomalies and blocked
// tasks are what main already reads, unchanged; cadence-due work, returned
// deliverables and open decision requests are new. A sixth pass matches the
// sprint's own goal text against the skill taxonomy (section 3). Every
// source's item still carries Why, naming the row it came from.
//
// No model call anywhere in this file: routing is entirely deterministic,
// per the spec's own "no model call in this step; that is B4 step two and a
// separate decision."

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// Plan is a proposed sprint: what the crew would do next, routed to analysts
// by skill, desk and headroom, with the guards it would run under.
//
// It is a PROPOSAL. Nothing is created until an operator approves it, and the
// approval is what materialises the tasks. A planner that writes straight to
// the board is one that spends the budget before anybody agreed to the work.
type Plan struct {
	Label    string
	Start    string
	End      string
	Goal     string
	Items    []PlanItem
	Budget   money.Cents
	Existing bool // this sprint is already on the board

	// TypedGoal is exactly what the sprint form's goal field carried, before
	// the empty-goal default below is substituted into Goal: what the plan
	// page re-shows in the box and carries as the approve form's hidden
	// field, so approving a plan runs the same goal back through Propose
	// rather than the storage sentence Goal defaults to when nobody typed
	// one (section 3: "empty is allowed and means 'the five sources only'").
	TypedGoal string

	// GoalUnmatched is true once TypedGoal is non-empty and named no skill
	// this console's taxonomy recognises at all, active or not (section 3:
	// "a goal that matches no skill adds nothing and the plan page says so
	// in one sentence"). A goal naming a skill that exists only on an
	// inactive analyst is the OTHER case section 4 names ("nobody active
	// holds the skill"), answered with a supervisor item instead, so it does
	// not set this flag.
	GoalUnmatched bool
}

type PlanItem struct {
	Title    string
	Goal     string
	Assignee string
	Desk     string
	Budget   money.Cents
	Why      string
}

// skillAnomalyTriage is the skill source 1's routing looks for: the
// taxonomy's name for triage (mandate.go's rightsForSkill, and
// world.go's own triage-*/investigator-* Skills lists).
const skillAnomalyTriage = "anomaly-triage"

// engineCheap and engineStrong mirror world.go's own local `cheap, strong :=
// "openrouter", "anthropic"` (buildCrew): the two engine routes this console
// actually has. Duplicated as literals rather than imported because
// world.go never exports them -- the same choice triageDesk already made
// for desk-to-assignee naming.
const (
	engineCheap  = "openrouter"
	engineStrong = "anthropic"
)

// engineByClass is section 4's "engine by class" table, named once so a test
// can cite it directly rather than re-deriving it from prose: triage and
// variance commentary route to the cheap engine, decision framing,
// executive reporting and commitment modelling to the strong one. Keyed by
// skill name, because skill is the vocabulary sources 1, and the goal,
// already route by -- "decision-framing", "exec-reporting" and
// "commitment-modelling" are literal entries of mandate.go's own
// rightsForSkill map, not a second, hand-invented naming.
//
// This chooses BETWEEN analysts already qualified by skill, desk and
// headroom; it never changes an analyst's own engine, which is set at hire.
// `@claude` 2026-09-02: this table, like the day-counts below, is not yet
// his words -- see the report's NOT PROVEN line.
var engineByClass = map[string]string{
	skillAnomalyTriage:     engineCheap,
	"variance-commentary":  engineCheap,
	"decision-framing":     engineStrong,
	"exec-reporting":       engineStrong,
	"commitment-modelling": engineStrong,
}

// cadenceDayCounts is how many days back from the sprint's start section
// 2.3's cadence words count before an analyst is due. A plain day count
// rather than a calendar-month walk -- `@claude`, the same kind of judgement
// call as engineByClass above, named once here so a test can cite the table
// instead of the arithmetic. "on-request" is deliberately absent: it is
// never due on its own (section 2.3).
var cadenceDayCounts = map[string]int{
	"daily":       1,
	"weekly":      7,
	"fortnightly": 14,
	"monthly":     30,
}

// Propose builds the next sprint from what the estate actually needs, which
// is the difference between a plan and a template: every item names the
// thing it came from. goal is free text from the sprint form; empty means
// "the five sources only" (section 3).
func Propose(db *sql.DB, label, start, end, goal string) (Plan, error) {
	p := Plan{Label: label, Start: start, End: end, TypedGoal: goal,
		Goal: "Close what is open, and explain what is not."}
	if strings.TrimSpace(goal) != "" {
		p.Goal = goal
	}

	var exists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sprints WHERE label=?`, label).Scan(&exists); err != nil {
		return p, err
	}
	p.Existing = exists > 0

	roster, err := Roster(db)
	if err != nil {
		return p, err
	}
	month := ""
	if len(start) >= 7 {
		month = start[:7]
	}
	spent, err := SpendInMonth(db, month)
	if err != nil {
		return p, err
	}

	if err := proposeAnomalies(db, &p, roster, spent); err != nil {
		return p, err
	}
	if err := proposeBlocked(db, &p); err != nil {
		return p, err
	}
	if err := proposeCadenceDue(db, &p, roster, start, spent); err != nil {
		return p, err
	}
	if err := proposeReturned(db, &p, roster, spent); err != nil {
		return p, err
	}
	if err := proposeDecisionRequests(db, &p); err != nil {
		return p, err
	}
	proposeGoal(&p, roster, goal, spent)

	for _, it := range p.Items {
		p.Budget += it.Budget
	}
	return p, nil
}

// ---------------------------------------------------------------- source 1

// proposeAnomalies is section 2 item 1: the six largest open, unowned
// anomalies, to the desk's triage analyst. Unchanged whenever a desk has one
// or zero active analysts holding anomaly-triage, which is every desk today
// except aws, gcp and azure (each of which has both a triage-* and an
// investigator-* analyst carrying the skill): section 4's routing (skill,
// desk, headroom, engine, PerTask) applies only "where more than one
// analyst on the desk has the skill".
func proposeAnomalies(db *sql.DB, p *Plan, roster []Analyst, spent map[string]money.Cents) error {
	rows, err := db.Query(`SELECT id, source, service, day, excess_cents
		FROM anomalies WHERE state='open' AND (handled_by IS NULL OR handled_by='')
		ORDER BY ABS(excess_cents) DESC LIMIT 6`)
	if err != nil {
		return err
	}
	type row struct {
		id, source, service, day string
		excess                   int64
	}
	var found []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.source, &r.service, &r.day, &r.excess); err != nil {
			rows.Close()
			return err
		}
		found = append(found, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, r := range found {
		title := fmt.Sprintf("Explain the %s move on %s", r.service, r.day)
		goal := fmt.Sprintf("%s against baseline on the %s desk.", money.Cents(r.excess), r.source)
		baseWhy := "anomaly " + r.id + " is open and unowned"

		candidates := candidatesWithSkill(roster, skillAnomalyTriage, r.source)
		if len(candidates) <= 1 {
			// Priced from PerTask when the desk's triage analyst is on the
			// roster at all -- active or not, this is a naming lookup, not
			// a routing choice -- so two desks never price the same work
			// differently for no reason. Review of this PR found the old
			// 15.00 literal used here even when the analyst it names is
			// right there with its own guard; the literal survives only
			// when nobody of that name exists on the roster.
			assignee := triageDesk(r.source)
			budget := money.Cents(15_00)
			if a, ok := findAnalyst(roster, assignee); ok {
				budget = a.PerTask
			}
			p.Items = append(p.Items, PlanItem{
				Title: title, Goal: goal, Assignee: assignee,
				Desk: r.source, Budget: budget, Why: baseWhy,
			})
			continue
		}
		p.Items = append(p.Items, routedItem(title, goal, baseWhy, candidates, skillAnomalyTriage, spent))
	}
	return nil
}

func triageDesk(source string) string {
	switch source {
	case "aws", "gcp", "azure":
		return "triage-" + source
	case "ai":
		return "triage-ai"
	case "saas":
		return "saas-manager"
	}
	return "investigator-onprem"
}

// ---------------------------------------------------------------- source 2

// proposeBlocked is section 2 item 2, unchanged: the three oldest blocked
// tasks, back to their assignee.
func proposeBlocked(db *sql.DB, p *Plan) error {
	blocked, err := Tasks(db, TaskFilter{State: Blocked})
	if err != nil {
		return err
	}
	for i, t := range blocked {
		if i >= 3 {
			break
		}
		p.Items = append(p.Items, PlanItem{
			Title:    "Unblock: " + t.Title,
			Goal:     t.Reason,
			Assignee: t.Assignee,
			Desk:     t.Desk,
			Budget:   money.Cents(10_00),
			Why:      fmt.Sprintf("task %d has been blocked since %s", t.ID, t.Updated),
		})
	}
	return nil
}

// ---------------------------------------------------------------- source 3

// proposeCadenceDue is section 2 item 3: an active analyst is due when its
// last posted deliverable is older than its cadence counted back from the
// sprint's start, or it has never posted; on-request is never due on its
// own. One item per sprint, never one per day.
//
// B5-SPEC.md section 3 point 2: the body moved to CadenceDue, exported, so
// this is now a thin wrapper. The plan and the runner's `-due` mode call the
// SAME function so the page, the plan and the runner cannot disagree about
// what is due.
func proposeCadenceDue(db *sql.DB, p *Plan, roster []Analyst, start string, spent map[string]money.Cents) error {
	items, err := CadenceDue(db, roster, start, spent)
	if err != nil {
		return err
	}
	p.Items = append(p.Items, items...)
	return nil
}

// CadenceDue is section 2 item 3's source, exported (B5-SPEC.md section 3
// point 2) so `tools/run`'s `-due` mode and the console's `/cadence` page
// build the due list through the exact same routing, pricing-guard and
// headroom logic Propose already uses -- never a second, hand-kept copy that
// could drift from it. db and roster are what Propose already loaded; start
// is the date cadence is measured against ("today" for the runner and the
// console, the sprint's own start date for a plan); spent is this month's
// spend per analyst (crew.SpendInMonth).
func CadenceDue(db *sql.DB, roster []Analyst, start string, spent map[string]money.Cents) ([]PlanItem, error) {
	var items []PlanItem
	cutoffBase, perr := time.Parse("2006-01-02", start)
	if perr != nil {
		return nil, nil // no parseable date: nothing is measured due against it
	}

	// C9-SPEC.md section 2: "-due and Propose skip a halted desk and say so
	// in Why." One line per halted desk, unconditionally -- not gated on
	// finding some particular analyst still due there -- because
	// data.halt's own side effect already suspends every analyst it finds,
	// and this function's own "state != active" rule below would otherwise
	// silently skip every one of them with no explanation at all: a desk
	// whose whole crew is already suspended must still say WHY its cadence
	// work stopped, the same "skip it, but say so" shape this loop already
	// keeps for a due analyst with no headroom left this month.
	halted := map[string]DeskHalt{}
	halts, herr := Halts(db)
	if herr != nil {
		return nil, herr
	}
	for _, h := range halts {
		halted[h.Desk] = h
		items = append(items, PlanItem{
			Title: "Cadence skipped: " + h.Desk + " is halted", Goal: h.Reason,
			Assignee: "supervisor", Desk: "management", Budget: 0,
			Why: fmt.Sprintf("data.halt on %s since %s: %s", h.Desk, h.Started, h.Reason),
		})
	}

	for _, a := range roster {
		if a.State != "active" {
			continue
		}
		if _, is := halted[a.Desk]; is {
			// Already explained once above, per desk: an analyst hired (or
			// reactivated) onto an already-halted desk is skipped without a
			// second, per-analyst note.
			continue
		}
		fields := strings.Fields(a.Cadence)
		if len(fields) == 0 {
			continue
		}
		// "weekly, and the close pack monthly" (the chargeback family): read
		// the first word only, per section 2 item 3's own instruction.
		word := strings.TrimRight(fields[0], ",")
		if word == "on-request" {
			continue
		}
		days, known := cadenceDayCounts[word]
		if !known {
			continue // an unrecognised cadence word decides nothing rather than guessing
		}

		lastPosted, everPosted, err := lastPostedDate(db, a.Name)
		if err != nil {
			return nil, err
		}
		due := !everPosted
		if everPosted {
			cutoff := cutoffBase.AddDate(0, 0, -days)
			postedT, perr := time.Parse("2006-01-02", firstTen(lastPosted))
			due = perr != nil || postedT.Before(cutoff)
		}
		if !due {
			continue
		}

		role, ok := RoleForDesk(a.Name, a.Desk)
		owes, mission := role.Owes, role.Mission
		if !ok {
			owes, mission = a.Mission, a.Mission
		}
		title := "This sprint's " + word + " work: " + firstClause(owes)
		when := "never posted"
		if everPosted {
			when = firstTen(lastPosted)
		}
		why := fmt.Sprintf("%s cadence, last posted %s", word, when)

		if headroomOf(a, spent) <= 0 {
			items = append(items, PlanItem{
				Title: title, Goal: mission, Assignee: "supervisor", Desk: "management",
				Budget: 0, Why: why + fmt.Sprintf("; %s skipped: no headroom this month", a.Name),
			})
			continue
		}
		items = append(items, PlanItem{
			Title: title, Goal: mission, Assignee: a.Name, Desk: a.Desk,
			Budget: a.PerTask, Why: why,
		})
	}
	return items, nil
}

// lastPostedDate is the newest artifacts.stamped for a posted deliverable
// whose task is assigned to name, joined through tasks.assignee (section 2
// item 3's own wording).
func lastPostedDate(db *sql.DB, name string) (date string, found bool, err error) {
	var d sql.NullString
	err = db.QueryRow(`SELECT MAX(a.stamped) FROM artifacts a
		JOIN tasks t ON t.id = a.task
		WHERE t.assignee = ? AND a.state = 'posted'`, name).Scan(&d)
	if err != nil {
		return "", false, err
	}
	if !d.Valid || d.String == "" {
		return "", false, nil
	}
	return d.String, true, nil
}

func firstTen(s string) string {
	if len(s) < 10 {
		return s
	}
	return s[:10]
}

// firstClause is section 2 item 3's "titled from the family's owes text
// (first clause)": roles.yaml writes every Owes field as clauses joined by
// ";", so the first clause is everything before the first one (or, for the
// handful of roles whose Owes is one sentence with none, the whole text
// minus its trailing full stop).
func firstClause(owes string) string {
	clause := owes
	if i := strings.Index(owes, ";"); i >= 0 {
		clause = owes[:i]
	}
	clause = strings.TrimSpace(clause)
	return strings.TrimSuffix(clause, ".")
}

// ---------------------------------------------------------------- source 4

// proposeReturned is section 2 item 4: every artifact in state returned
// whose task is not done, back to the same assignee, with the return reason
// verbatim as Goal.
func proposeReturned(db *sql.DB, p *Plan, roster []Analyst, spent map[string]money.Cents) error {
	rows, err := db.Query(`SELECT a.id, a.title, COALESCE(a.reason,''), t.title,
		COALESCE(t.assignee,''), COALESCE(t.desk,'')
		FROM artifacts a JOIN tasks t ON t.id = a.task
		WHERE a.state = 'returned' AND t.state <> 'done'
		ORDER BY a.id`)
	if err != nil {
		return err
	}
	type row struct {
		artID                                       int
		artTitle, reason, taskTitle, assignee, desk string
	}
	var found []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.artID, &r.artTitle, &r.reason, &r.taskTitle, &r.assignee, &r.desk); err != nil {
			rows.Close()
			return err
		}
		found = append(found, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, r := range found {
		title := "Rework: " + r.taskTitle
		// Neither artifacts nor tasks records WHO returned a deliverable --
		// Return's actorLink ("owner"/"supervisor") is a coarse link name,
		// never stored on the row -- so this names the artifact, which is
		// knowable, and does not invent a person. See the report's NOT
		// PROVEN line.
		why := fmt.Sprintf("artifact %d, %q, was returned for rework", r.artID, r.artTitle)

		a, known := findAnalyst(roster, r.assignee)
		if r.assignee == "" || !known || a.State != "active" {
			p.Items = append(p.Items, PlanItem{
				Title: title, Goal: r.reason, Assignee: "supervisor", Desk: "management",
				Budget: 0, Why: why,
			})
			continue
		}
		if headroomOf(a, spent) <= 0 {
			p.Items = append(p.Items, PlanItem{
				Title: title, Goal: r.reason, Assignee: "supervisor", Desk: "management",
				Budget: 0, Why: why + fmt.Sprintf("; %s skipped: no headroom this month", a.Name),
			})
			continue
		}
		p.Items = append(p.Items, PlanItem{
			Title: title, Goal: r.reason, Assignee: a.Name, Desk: r.desk,
			Budget: a.PerTask, Why: why,
		})
	}
	return nil
}

// ---------------------------------------------------------------- source 5

// proposeDecisionRequests is section 2 item 5: every decision request whose
// options are still carried, one item per owner naming how many are
// waiting. The crew cannot answer these; the plan shows the person that
// they are waiting, so this is not scoped to the sprint being planned --
// deliberately: a stale request from a past sprint is exactly what a person
// planning the next one needs to see.
func proposeDecisionRequests(db *sql.DB, p *Plan) error {
	rows, err := db.Query(`SELECT artifact, sprint, owner, COALESCE(lapses,'')
		FROM decision_requests ORDER BY sprint, owner`)
	if err != nil {
		return err
	}
	type row struct {
		artifact, sprint int
		owner, lapses    string
	}
	var found []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.artifact, &r.sprint, &r.owner, &r.lapses); err != nil {
			rows.Close()
			return err
		}
		found = append(found, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, r := range found {
		carried, err := CarriedOptionsFor(db, r.sprint, r.owner)
		if err != nil {
			return err
		}
		if len(carried) == 0 {
			continue
		}
		label := sprintLabelOf(db, r.sprint)
		p.Items = append(p.Items, PlanItem{
			Title:    fmt.Sprintf("Decision request for %s: %d option(s) waiting", r.owner, len(carried)),
			Assignee: "supervisor",
			Desk:     "management",
			Budget:   0,
			Why:      fmt.Sprintf("from %s, dated %s", label, r.lapses),
		})
	}
	return nil
}

func sprintLabelOf(db *sql.DB, sprintID int) string {
	var label string
	if err := db.QueryRow(`SELECT label FROM sprints WHERE id=?`, sprintID).Scan(&label); err != nil {
		return fmt.Sprintf("sprint %d", sprintID)
	}
	return label
}

// ------------------------------------------------------------------- goal

// desksInGoal is section 3's "aws, gcp, azure, ai, saas, onprem": read from
// world.Desks rather than a second, hand-kept literal list, so the two
// cannot drift.
func desksInGoal() map[string]bool {
	out := make(map[string]bool, len(world.Desks))
	for _, d := range world.Desks {
		out[d.Name] = true
	}
	return out
}

// proposeGoal is section 3, extended by review of this PR to match what a
// person actually types rather than only the taxonomy's own exact strings.
// Split the goal into lowercase words, and for each one, in order:
//
//   - (a) an exact skill token (crew.SkillPool, exactly the roster's skills
//     per invariant 21): route to the active analyst holding it on the desk
//     the goal names, or the one with the most headroom among those that
//     have it;
//   - (b) a roster NAME, exact ("commitments", "renewals", "forecaster",
//     "data-quality"...): route straight to that analyst if it is active,
//     the same "nobody active holds" fallback as (a) otherwise -- reusing
//     chooseAnalyst's own empty-candidates path rather than a second one;
//   - (c) the first hyphen-delimited segment of a hyphenated skill,
//     singular or plural (one trailing "s" stripped from either side, never
//     more): "commitment" and "commitments" both reach
//     commitment-modelling. A segment more than one skill shares (today
//     only "renewal": renewal-calendar and renewal-negotiation-prep) is
//     left unresolved rather than guessed at; the roster-name form in (b)
//     is what disambiguates "renewals" on its own.
//
// A word satisfying none of the three adds nothing. GoalUnmatched is set
// only when NO word in the whole goal matched any of the three.
func proposeGoal(p *Plan, roster []Analyst, goal string, spent map[string]money.Cents) {
	words := goalWords(goal)
	if len(words) == 0 {
		return
	}
	desk := goalDesk(words)
	pool := map[string]bool{}
	for _, s := range SkillPool {
		pool[s] = true
	}
	desks := desksInGoal()

	matchedAny := false
	seen := map[string]bool{}
	for _, w := range words {
		if desks[w] || seen[w] {
			continue
		}

		if pool[w] {
			seen[w] = true
			matchedAny = true
			addSkillGoalItem(p, roster, goal, w, desk, spent)
			continue
		}
		if _, ok := findAnalyst(roster, w); ok {
			seen[w] = true
			matchedAny = true
			addNameGoalItem(p, roster, goal, w, spent)
			continue
		}
		if skill, ok := resolveFirstSegment(w); ok {
			seen[w] = true
			matchedAny = true
			addSkillGoalItem(p, roster, goal, skill, desk, spent)
			continue
		}
	}
	if !matchedAny {
		p.GoalUnmatched = true
	}
}

// goalTitle is section 3's "<goal clause>, trimmed to 120": the goal text
// itself, cut on a rune boundary so a hostile multi-byte goal is never cut
// mid-character.
func goalTitle(goal string) string {
	r := []rune(goal)
	if len(r) > 120 {
		r = r[:120]
	}
	return string(r)
}

// addSkillGoalItem is rule (a) and (c): skill is always the taxonomy's own
// full name (the exact token for (a), the resolved skill for (c)), so the
// item's Why, its candidate search and its engine-class lookup all read
// the same string regardless of which rule found it.
func addSkillGoalItem(p *Plan, roster []Analyst, goal, skill, desk string, spent map[string]money.Cents) {
	why := fmt.Sprintf("the sprint goal names %s", skill)
	candidates := candidatesWithSkill(roster, skill, desk)
	p.Items = append(p.Items, routedItem(goalTitle(goal), goal, why, candidates, skill, spent))
}

// addNameGoalItem is rule (b): candidates is the one named analyst, or
// nothing at all when it is not active, so chooseAnalyst's existing
// "nobody active holds" and headroom-skip paths answer for a name exactly
// as they already do for a skill -- one mechanism, not two.
func addNameGoalItem(p *Plan, roster []Analyst, goal, name string, spent map[string]money.Cents) {
	why := fmt.Sprintf("the sprint goal names %s", name)
	candidates := candidateByName(roster, name)
	p.Items = append(p.Items, routedItem(goalTitle(goal), goal, why, candidates, name, spent))
}

// candidateByName is rule (b)'s candidate set.
func candidateByName(roster []Analyst, name string) []Analyst {
	a, ok := findAnalyst(roster, name)
	if !ok || a.State != "active" {
		return nil
	}
	return []Analyst{a}
}

// skillFirstSegments maps the first hyphen-delimited segment of every
// hyphenated skill in the taxonomy to the skill name(s) that share it,
// built once the same way SkillPool is: a derived var, not a second
// hand-kept list that could drift from it.
var skillFirstSegments = buildSkillFirstSegments()

func buildSkillFirstSegments() map[string][]string {
	out := map[string][]string{}
	for _, s := range SkillPool {
		if i := strings.Index(s, "-"); i >= 0 {
			seg := s[:i]
			out[seg] = append(out[seg], s)
		}
	}
	return out
}

// goalSegmentMinLetters and goalSegmentExcludedFiller narrow rule (c), on
// review: a goal is free prose, and an item added because a short, common
// word ("data", "cost", "model", "step") or ordinary filler ("this
// sprint") happened to be a skill's first segment is noise a person has to
// strike out every time. Rules (a) and (b) are untouched -- an exact skill
// token or an exact roster name is never ambiguous the way a bare word
// fragment is.
const (
	// goalSegmentMinLetters: at least this many letters after stripping one
	// trailing "s", so "capacity", "commitment" and "sustainability" still
	// reach a skill and "data", "cost" and "step" do not.
	goalSegmentMinLetters = 6
	// goalSegmentExcludedFiller is "sprint": six letters on its own, so the
	// length rule alone would not stop "this sprint" from reaching
	// sprint-planning, and it is exactly the filler a sprint-planning goal
	// is written in.
	goalSegmentExcludedFiller = "sprint"
)

// resolveFirstSegment is rule (c). Both sides are normalised by stripping
// at most one trailing "s" before comparing, so "commitment" and
// "commitments" both reach a segment written singular ("commitment") and
// would equally reach one written plural, if the taxonomy ever had one.
func resolveFirstSegment(word string) (skill string, ok bool) {
	wnorm := strings.TrimSuffix(word, "s")
	if len(wnorm) < goalSegmentMinLetters || wnorm == goalSegmentExcludedFiller {
		return "", false
	}
	for seg, skills := range skillFirstSegments {
		if wnorm != strings.TrimSuffix(seg, "s") {
			continue
		}
		if len(skills) != 1 {
			return "", false // shared by more than one skill: not guessed at
		}
		return skills[0], true
	}
	return "", false
}

// goalWords lowercases free text and splits it on whitespace, trimming
// surrounding punctuation from each token: "commitments," and
// "commitment-modelling." still compare equal to a bare skill name. A
// skill's own internal hyphen survives, because Trim only strips the ENDS
// of a token, never the middle, which is what lets a hyphenated skill name
// (the taxonomy's usual shape, "anomaly-triage") match as one token without
// any special-casing.
func goalWords(goal string) []string {
	fields := strings.Fields(strings.ToLower(goal))
	out := make([]string, 0, len(fields))
	for _, w := range fields {
		w = strings.Trim(w, ".,;:!?()\"'")
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

// goalDesk is section 3's "if it names a desk word": the first one found,
// in the goal's own word order, so two desk words in one goal resolve the
// same way on every call rather than by map order.
func goalDesk(words []string) string {
	desks := desksInGoal()
	for _, w := range words {
		if desks[w] {
			return w
		}
	}
	return ""
}

// ---------------------------------------------------------------- routing

// candidatesWithSkill is every ACTIVE analyst holding skill, from an
// already-loaded roster, optionally narrowed to one desk (desk == "" means
// every desk). Suspended, restricted, onboarding, probation and over-guard
// analysts never appear here: section 4, "suspended or retired analysts are
// never routed to."
func candidatesWithSkill(roster []Analyst, skill, desk string) []Analyst {
	var out []Analyst
	for _, a := range roster {
		if a.State != "active" {
			continue
		}
		if desk != "" && a.Desk != desk {
			continue
		}
		if hasSkill(a.Skills, skill) {
			out = append(out, a)
		}
	}
	return out
}

func hasSkill(skills []string, want string) bool {
	for _, s := range skills {
		if s == want {
			return true
		}
	}
	return false
}

func findAnalyst(roster []Analyst, name string) (Analyst, bool) {
	for _, a := range roster {
		if a.Name == name {
			return a, true
		}
	}
	return Analyst{}, false
}

func headroomOf(a Analyst, spent map[string]money.Cents) money.Cents {
	return a.Monthly - spent[a.Name]
}

// chooseAnalyst applies section 4's tiebreak order over candidates already
// filtered to the skill (and desk, when the item has one): most headroom
// first, then the engine this class of work prefers (engineByClass),
// finally the name, so the same inputs always choose the same analyst
// (invariant 7, CLAUDE.md). Every candidate with no headroom left this
// month is skipped rather than silently chosen anyway; note names every
// skip, so nothing about the routing is hidden from the item's Why.
func chooseAnalyst(candidates []Analyst, class string, spent map[string]money.Cents) (chosen Analyst, ok bool, note string) {
	if len(candidates) == 0 {
		return Analyst{}, false, "nobody active holds " + class
	}
	type cand struct {
		a        Analyst
		headroom money.Cents
	}
	cs := make([]cand, len(candidates))
	for i, a := range candidates {
		cs[i] = cand{a, headroomOf(a, spent)}
	}

	// Step one: drop everyone with no headroom left this month, before
	// anything else is compared. Every skip is named, regardless of where
	// the skipped analyst would otherwise have ranked, so nothing about the
	// routing is hidden from the item's Why.
	var skipped []string
	var viable []cand
	for _, c := range cs {
		if c.headroom <= 0 {
			skipped = append(skipped, fmt.Sprintf("%s skipped: no headroom this month", c.a.Name))
			continue
		}
		viable = append(viable, c)
	}
	if len(viable) == 0 {
		return Analyst{}, false, strings.Join(skipped, "; ")
	}

	// Among analysts who already have headroom to spend: the engine the
	// class prefers first (review of this PR found the sort ordering
	// headroom BEFORE engine, which made engineByClass nearly dead code --
	// a real-cents tie is rare, so the engine preference almost never got
	// a turn), then the most headroom, then the name.
	preferred := engineByClass[class]
	sort.SliceStable(viable, func(i, j int) bool {
		if preferred != "" {
			im, jm := viable[i].a.Engine == preferred, viable[j].a.Engine == preferred
			if im != jm {
				return im
			}
		}
		if viable[i].headroom != viable[j].headroom {
			return viable[i].headroom > viable[j].headroom
		}
		return viable[i].a.Name < viable[j].a.Name
	})
	return viable[0].a, true, strings.Join(skipped, "; ")
}

// routedItem applies chooseAnalyst and folds its note into Why, falling
// back to the supervisor (desk management, budget zero) when nothing
// qualifies -- never dropped, the same shape source 5 already uses for a
// decision the crew cannot answer on its own.
func routedItem(title, goal, baseWhy string, candidates []Analyst, class string, spent map[string]money.Cents) PlanItem {
	chosen, ok, note := chooseAnalyst(candidates, class, spent)
	why := baseWhy
	if note != "" {
		why += "; " + note
	}
	if !ok {
		return PlanItem{Title: title, Goal: goal, Assignee: "supervisor", Desk: "management",
			Budget: 0, Why: why}
	}
	return PlanItem{Title: title, Goal: goal, Assignee: chosen.Name, Desk: chosen.Desk,
		Budget: chosen.PerTask, Why: why}
}

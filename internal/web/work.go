package web

import (
	"html"
	"html/template"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// Only the two constructs the crew's own output actually uses.
var (
	boldRe = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	// Single asterisks, but only where they are not part of a ** pair: bold is
	// replaced first, so by the time this runs the pairs are gone.
	italicRe = regexp.MustCompile(`\*([^*\n]+)\*`)
	// "1." or "12)" at the start of a line.
	numberedRe = regexp.MustCompile(`^\d+[.)]\s+`)
)

var (
	tplBoard   = page("board.html")
	tplSprints = page("sprints.html")
	tplSprint  = page("sprint.html")
	tplTask    = page("task.html")
	tplStaff   = page("staff.html")
	tplAnalyst = page("analyst.html")
)

// stateChip maps a task's state onto the visual vocabulary the anomaly plane
// already uses, so a reader learns one set of shapes rather than two.
func stateChip(s crew.TaskState) string {
	switch s {
	case crew.Posted:
		return "accepted"
	case crew.Done:
		return "explained"
	case crew.Blocked, crew.Returned:
		return "open"
	case crew.Active:
		return "triaged"
	}
	return "dismissed"
}

// taskView is a task with the little the template cannot work out for itself.
type taskView struct {
	crew.Task
	StateChip string
}

func views(ts []crew.Task) []taskView {
	out := make([]taskView, 0, len(ts))
	for _, t := range ts {
		out = append(out, taskView{t, stateChip(t.State)})
	}
	return out
}

// activeAnalysts is the rota, read from the store rather than the fixture:
// somebody hired this morning has to appear in the assign menu this morning.
func (s *Server) activeAnalysts() []string {
	names, err := crew.ActiveNames(s.db)
	if err != nil {
		return nil
	}
	return names
}

// -------------------------------------------------------------------- board

type lane struct {
	Name  string
	Tasks []taskView
}

func (s *Server) board(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	f := crew.TaskFilter{
		Desk:     r.URL.Query().Get("desk"),
		Assignee: r.URL.Query().Get("assignee"),
	}
	all, err := crew.Tasks(s.db, f)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	by := map[crew.TaskState][]crew.Task{}
	for _, t := range all {
		by[t.State] = append(by[t.State], t)
	}
	// Stopped work first. A board that puts "done" on the left and buries what
	// is stuck on the right is a board that hides its own problems.
	order := []struct {
		name  string
		state crew.TaskState
	}{
		{"Blocked", crew.Blocked},
		{"Returned", crew.Returned},
		{"In flight", crew.Active},
		{"Queued", crew.Queued},
		{"Waiting on a stamp", crew.Done},
		{"Posted", crew.Posted},
	}
	// Waiting on a stamp is derived from the deliverables rather than from a
	// task state, because a task can be "active" while its output is written
	// and waiting. That lane is the reviewer's queue and it has to be right.
	stamping, _ := crew.AwaitingStamp(s.db)
	inLane := map[int]bool{}
	for _, t := range stamping {
		inLane[t.ID] = true
	}

	var lanes []lane
	for _, o := range order {
		ts := by[o.state]
		if o.state == crew.Done {
			ts = stamping
		} else {
			var keep []crew.Task
			for _, t := range ts {
				if !inLane[t.ID] {
					keep = append(keep, t)
				}
			}
			ts = keep
		}
		// The posted lane would otherwise be hundreds of rows nobody reads.
		if o.state == crew.Posted && len(ts) > 12 {
			ts = ts[:12]
		}
		lanes = append(lanes, lane{o.name, views(ts)})
	}

	desks, _ := crew.Desks(s.db)
	s.render(w, tplBoard, struct {
		shell
		Lanes    []lane
		Desks    []string
		Analysts []string
		Desk     string
		Assignee string
	}{s.shellFor(r, "Board", "board"), lanes, desks, s.activeAnalysts(),
		f.Desk, f.Assignee})
}

// ------------------------------------------------------------------ sprints

func (s *Server) sprints(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	sp, err := crew.Sprints(s.db)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	spec := readSort(r, "label", true)
	applySort(sp, spec, map[string]func(x, y crew.Sprint) int{
		"label":  func(x, y crew.Sprint) int { return cmpString(x.Label, y.Label) },
		"goal":   func(x, y crew.Sprint) int { return cmpString(x.Goal, y.Goal) },
		"tasks":  func(x, y crew.Sprint) int { return cmpInt(x.Tasks, y.Tasks) },
		"open":   func(x, y crew.Sprint) int { return cmpInt(x.Open, y.Open) },
		"posted": func(x, y crew.Sprint) int { return cmpInt(x.Posted, y.Posted) },
		"spent":  func(x, y crew.Sprint) int { return cmpInt64(int64(x.Spent), int64(y.Spent)) },
		"budget": func(x, y crew.Sprint) int { return cmpInt64(int64(x.Budget), int64(y.Budget)) },
		"state":  func(x, y crew.Sprint) int { return cmpString(x.State, y.State) },
	}, "label")
	s.render(w, tplSprints, struct {
		shell
		Sprints []crew.Sprint
		CanAct  bool
		Sort    sortSpec
	}{s.shellFor(r, "Sprints", "sprints"), sp, u.May("operator"), spec})
}

func (s *Server) sprintPage(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "no such sprint", http.StatusNotFound)
		return
	}
	all, err := crew.Sprints(s.db)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	var found crew.Sprint
	for _, x := range all {
		if x.ID == id {
			found = x
		}
	}
	if found.ID == 0 {
		http.Error(w, "no such sprint", http.StatusNotFound)
		return
	}
	ts, _ := crew.Tasks(s.db, crew.TaskFilter{Sprint: id})
	rows := views(ts)
	srt := readSort(r, "state", false)
	applySort(rows, srt, map[string]func(a, b taskView) int{
		"task":    func(a, b taskView) int { return cmpString(a.Title, b.Title) },
		"analyst": func(a, b taskView) int { return cmpString(a.Assignee, b.Assignee) },
		"desk":    func(a, b taskView) int { return cmpString(a.Desk, b.Desk) },
		"spent":   func(a, b taskView) int { return cmpInt64(int64(a.Spent), int64(b.Spent)) },
		"state":   func(a, b taskView) int { return cmpString(string(a.State), string(b.State)) },
	}, "state")
	s.render(w, tplSprint, struct {
		shell
		S      crew.Sprint
		Tasks  []taskView
		CanAct bool
		Sort   sortSpec
	}{s.shellFor(r, found.Label, "sprints"), found, rows, u.May("operator"), srt})
}

// --------------------------------------------------------------------- task

type artView struct {
	crew.Artifact
	Rendered template.HTML
	// Options is the deliverable's own machine-readable list, B3-SPEC.md
	// section 2: html/template escapes every field by default, which is what
	// keeps a script tag in an option's summary rendering as text rather
	// than as markup -- unlike Rendered above, nothing here is ever wrapped
	// in template.HTML.
	Options []optionView
}

// optionView adds the two figures formatted as money, which the template
// cannot do for itself from a bare int64 of cents.
type optionView struct {
	crew.Option
	Figure money.Cents
	Saving money.Cents
}

func optionViews(opts []crew.Option) []optionView {
	out := make([]optionView, 0, len(opts))
	for _, o := range opts {
		out = append(out, optionView{o, money.Cents(o.FigureCents), money.Cents(o.SavingCents)})
	}
	return out
}

// renderBody turns an analyst's markdown-ish output into something readable
// without pulling in a markdown library.
//
// Escaped FIRST and marked up second: the body is written by a model, and a
// model's output is untrusted input like any other. Handling only the three
// constructs the crew actually emits is the point, not a limitation.
// renderBody turns a deliverable into HTML.
//
// Deliberately tiny: no markdown library, because the console has no runtime
// and one dependency here would be the first. It handles what a deliverable
// actually contains and nothing else.
//
// The list of what that IS grew from looking at the running product. The seeded
// drafts were written to match this renderer, so it only ever handled bold and
// "## ", and everything agreed with itself. Then a model wrote 44 of them and
// the page showed its syntax back to the reader:
//
//	### **Anomaly Summary**
//	---
//	1. **What happened?** Identify the root cause.
//	- **Established Cause:** *None confirmed at this time.*
//
// Four things in one paragraph that this had no idea about. The prompt now asks
// for a narrower format too, but a prompt is a request and this is the part
// that holds: a model that ignores the request must still render.
func renderBody(src string) template.HTML {
	var b strings.Builder
	list := false
	closeList := func() {
		if list {
			b.WriteString("</ul>")
			list = false
		}
	}
	for _, para := range strings.Split(standalone(src), "\n\n") {
		p := strings.TrimSpace(para)
		if p == "" {
			continue
		}
		// A rule on its own is a rule; inside a paragraph it is punctuation.
		if isRule(p) {
			closeList()
			b.WriteString("<hr>")
			continue
		}
		if h, level := heading(p); h != "" {
			closeList()
			tag := "h4"
			if level <= 2 {
				tag = "h3"
			}
			b.WriteString("<" + tag + ">" + inline(h) + "</" + tag + ">")
			continue
		}
		if items := bullets(p); items != nil {
			if !list {
				b.WriteString("<ul>")
				list = true
			}
			for _, it := range items {
				b.WriteString("<li>" + inline(it) + "</li>")
			}
			continue
		}
		closeList()
		b.WriteString("<p>" + inline(p) + "</p>")
	}
	closeList()
	return template.HTML(b.String())
}

// standalone puts a blank line around every heading and every rule.
//
// A model does not leave one. The run produced
//
//	### **Anomaly Summary**  \n**Observation:** On 2026-07-14, EC2 spiked.
//
// with a single newline between them, so the heading and the sentence under it
// arrived as ONE paragraph and the hashes went to the page as text. The first
// version of the fixture had a blank line there, because that is the shape I
// would have written, and the test passed while the running page was wrong.
// The fixture is now the bytes the run actually produced.
func standalone(src string) string {
	lines := strings.Split(src, "\n")
	var b strings.Builder
	for i, l := range lines {
		t := strings.TrimSpace(l)
		_, lvl := heading(t)
		if lvl > 0 || isRule(t) {
			if i > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(t)
			if i < len(lines)-1 {
				b.WriteString("\n\n")
			}
			continue
		}
		b.WriteString(l)
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// isRule is a line of dashes or asterisks and nothing else.
func isRule(p string) bool {
	p = strings.TrimSpace(p)
	if len(p) < 3 {
		return false
	}
	return strings.Trim(p, "-") == "" || strings.Trim(p, "*") == "" ||
		strings.Trim(p, "_") == ""
}

// heading returns the text of a "#"-prefixed line and how deep it is.
//
// Any depth, because a model picks its own: this saw #, ###, and #### in one
// deliverable. Only the first line is considered, so a paragraph that merely
// contains a hash is left alone.
func heading(p string) (string, int) {
	line := p
	if i := strings.IndexByte(p, '\n'); i >= 0 {
		line = p[:i]
	}
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n >= len(line) || line[n] != ' ' {
		return "", 0
	}
	if line != p {
		return "", 0 // a heading with a paragraph glued to it is not a heading
	}
	return strings.TrimSpace(line[n+1:]), n
}

// bullets splits a paragraph whose every line is a list item.
//
// All of them or none: one dash in the middle of prose is a dash. Numbered and
// dashed alike, because a model uses both and a reader wants a list either way.
func bullets(p string) []string {
	lines := strings.Split(p, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		switch {
		case strings.HasPrefix(t, "- "), strings.HasPrefix(t, "* "):
			out = append(out, strings.TrimSpace(t[2:]))
		case numberedRe.MatchString(t):
			out = append(out, strings.TrimSpace(numberedRe.ReplaceAllString(t, "")))
		default:
			return nil
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// inline escapes the text and then puts back the two marks worth keeping.
//
// Escaping FIRST is the whole point: a deliverable is written by a model and
// must not be able to put a tag on this page.
func inline(s string) string {
	esc := html.EscapeString(s)
	esc = boldRe.ReplaceAllString(esc, "<strong>$1</strong>")
	esc = italicRe.ReplaceAllString(esc, "<em>$1</em>")
	// A model ends lines with two spaces to mean a break. Without this the
	// whole paragraph runs together.
	esc = strings.ReplaceAll(esc, "  \n", "<br>")
	return strings.ReplaceAll(esc, "\n", " ")
}

func (s *Server) taskPage(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "no such task", http.StatusNotFound)
		return
	}
	t, err := crew.GetTask(s.db, id)
	if err != nil {
		http.Error(w, "no such task", http.StatusNotFound)
		return
	}
	arts, _ := crew.Artifacts(s.db, id)
	notes, _ := crew.Comments(s.db, id)

	av := make([]artView, 0, len(arts))
	for _, a := range arts {
		opts, _ := crew.Options(s.db, a.ID)
		av = append(av, artView{a, renderBody(a.Body), optionViews(opts)})
	}
	// The sprint's label, so the page can link the week this belongs to
	// rather than printing a row id nobody can look up.
	label := ""
	if sprints, err := crew.Sprints(s.db); err == nil {
		for _, sp := range sprints {
			if sp.ID == t.Sprint {
				label = sp.Label
				break
			}
		}
	}
	s.render(w, tplTask, struct {
		shell
		T           crew.Task
		StateChip   string
		SprintLabel string
		Arts        []artView
		Notes       []crew.Comment
		Analysts    []string
		CanAct      bool
	}{s.shellFor(r, t.Title, "board"), t, stateChip(t.State), label, av, notes,
		s.activeAnalysts(), u.May("operator")})
}

// -------------------------------------------------------------------- staff

type staffRow struct {
	crew.Analyst
	Score crew.Scoreboard
	Chip  string
}

func agentChip(state string) string {
	switch state {
	case "active":
		return "accepted"
	case "suspended", "over-guard":
		return "open"
	case "probation", "restricted":
		return "triaged"
	}
	return "explained"
}

func (s *Server) staff(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	scores, err := crew.Scoreboards(s.db)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	roster, err := crew.Roster(s.db)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	// Narrowed to one desk when asked. A crew desk with no cloud behind it,
	// like "management", has no desk page of its own, so /desk/{name} sends
	// the reader here rather than answering 404 while twelve analysts sit
	// there. The tiles below deliberately keep counting the WHOLE crew: they
	// are the figures the KPI page is checked against, and a filtered total
	// that still calls itself "what the crew cost" would be the sort of
	// disagreement this console exists to avoid.
	deskFilter := strings.TrimSpace(r.URL.Query().Get("desk"))

	rows := make([]staffRow, 0, len(roster))
	// The crew's own totals, so the figure the KPI page reports for what the
	// crew costs has somewhere to be checked against. A number that appears in
	// exactly one place cannot be checked at all.
	var totalGuard money.Cents
	var byState int
	states := map[string]int{}
	onRoster := map[string]bool{}
	for _, a := range roster {
		sc := scores[a.Name]
		if deskFilter != "" && a.Desk != deskFilter {
			continue
		}
		rows = append(rows, staffRow{a, sc, agentChip(a.State)})
		totalGuard += a.Monthly
		states[a.State]++
		onRoster[a.Name] = true
	}

	// The BOARD's totals, not the roster's.
	//
	// Summing over the roster silently drops work charged to a name that is
	// not on it, and the drop is exactly the kind that hides: 46 cents on one
	// unassigned task, against which the AI page reported the board's own
	// total and the two pages disagreed. Work nobody owns is a governance
	// finding in its own right, so it is counted and then named.
	var totalSpent, offRosterSpent money.Cents
	var tasks, open, posted, returned, offRosterTasks int
	for name, sc := range scores {
		totalSpent += sc.Spent
		tasks += sc.Tasks
		open += sc.Open
		posted += sc.Posted
		returned += sc.Returned
		if !onRoster[name] {
			offRosterSpent += sc.Spent
			offRosterTasks += sc.Tasks
		}
	}
	byState = states["active"]
	unattested, ofThose := crew.Unattested(roster)

	// Analysts that have spent past the guard they were given, IN THE MONTH
	// the guard is about.
	//
	// Setting a lifetime total against a monthly guard put twenty-one of
	// thirty-nine over budget, which is not a finding, it is five months of
	// work compared with one month of allowance.
	month := world.LastDay[:7]
	inMonth, _ := crew.SpendInMonth(s.db, month)

	// What of this figure is real. Everything else on this page is generated,
	// and one number covering both kinds is the fault this console spends its
	// time catching in other people's data.
	liveMicros, liveTasks, _ := crew.LiveSpend(s.db)
	realMoney := crew.RealMoney(liveMicros, liveTasks)
	var overGuard int
	var overBy, spentThisMonth money.Cents
	for _, a := range roster {
		v := inMonth[a.Name]
		spentThisMonth += v
		if a.Monthly > 0 && v > a.Monthly {
			overGuard++
			overBy += v - a.Monthly
		}
	}
	firstPass := 0.0
	if posted+returned > 0 {
		firstPass = float64(posted) / float64(posted+returned) * 100
	}
	srt := readSort(r, "name", false)
	applySort(rows, srt, map[string]func(a, b staffRow) int{
		"name":     func(a, b staffRow) int { return cmpString(a.Name, b.Name) },
		"desk":     func(a, b staffRow) int { return cmpString(a.Desk, b.Desk) },
		"open":     func(a, b staffRow) int { return cmpInt(a.Score.Open, b.Score.Open) },
		"posted":   func(a, b staffRow) int { return cmpInt(a.Score.Posted, b.Score.Posted) },
		"returned": func(a, b staffRow) int { return cmpInt(a.Score.Returned, b.Score.Returned) },
		"rate":     func(a, b staffRow) int { return cmpFloat(a.Score.FirstPass, b.Score.FirstPass) },
		"spent":    func(a, b staffRow) int { return cmpInt64(int64(a.Score.Spent), int64(b.Score.Spent)) },
		"state":    func(a, b staffRow) int { return cmpString(a.State, b.State) },
	}, "name")
	s.render(w, tplStaff, struct {
		shell
		Rows                          []staffRow
		CanAct                        bool
		Sort                          sortSpec
		Spent, Guard                  money.Cents
		Tasks, Open, Posted, Returned int
		Active                        int
		FirstPass                     float64
		States                        map[string]int
		OffRoster                     money.Cents
		OffRosterTasks                int
		OverGuard                     int
		OverBy                        money.Cents
		Month                         string
		ThisMonth                     money.Cents
		Unattested                    int
		OfThose                       int
		RealMoney                     string
	}{s.shellFor(r, "Crew", "staff"), rows, u.May("operator"), srt,
		totalSpent, totalGuard, tasks, open, posted, returned, byState,
		firstPass, states, offRosterSpent, offRosterTasks, overGuard, overBy,
		month, spentThisMonth, unattested, ofThose, realMoney})
}

// ------------------------------------------------------------------ actions

func (s *Server) taskAction(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.guard(w, r)
		if u == nil {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "no such task", http.StatusNotFound)
			return
		}
		back := "/task/" + strconv.Itoa(id)
		if !s.checked(w, r, back, u) {
			return
		}
		switch kind {
		case "assign":
			err = crew.Assign(s.db, id, r.PostFormValue("analyst"))
		case "block":
			err = crew.Block(s.db, id, r.PostFormValue("reason"))
		case "comment":
			err = crew.Comment_(s.db, id, u.Username, r.PostFormValue("body"))
		}
		s.done(w, r, back, err)
	}
}

func (s *Server) artifactAction(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.guard(w, r)
		if u == nil {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "no such deliverable", http.StatusNotFound)
			return
		}
		back := "/board"
		if t, err := crew.TaskOfArtifact(s.db, id); err == nil {
			back = "/task/" + strconv.Itoa(t)
		}
		if !s.checked(w, r, back, u) {
			return
		}
		if kind == "post" {
			// The stamp is a person's act and it is recorded as that person's,
			// never as the analyst's: the whole point of the gate is that it
			// was a human who agreed. "owner" is the acting link: a web
			// session is always a person, and B1A-SPEC.md section 2 says the
			// owner link decides everything that exists today.
			err = crew.Post(s.db, id, u.Username, "owner")
		} else {
			err = crew.Return(s.db, id, r.PostFormValue("reason"), "owner")
		}
		s.done(w, r, back, err)
	}
}

// checked runs the two gates every action shares.
func (s *Server) checked(w http.ResponseWriter, r *http.Request, back string, u interface{ May(string) bool }) bool {
	if err := r.ParseForm(); err != nil {
		redirectMsg(w, r, back, "reload the page and try again")
		return false
	}
	if !s.au.CSRFOK(s.sessionToken(r), r.PostFormValue("csrf")) {
		redirectMsg(w, r, back, "reload the page and try again")
		return false
	}
	if !u.May("operator") {
		redirectMsg(w, r, back, "your account may read and export, but not act")
		return false
	}
	return true
}

func (s *Server) done(w http.ResponseWriter, r *http.Request, back string, err error) {
	switch {
	case err == nil:
		redirectMsg(w, r, back, "")
	case strings.Contains(err.Error(), "needs a reason"):
		redirectMsg(w, r, back, "that needs a reason: without one nobody can tell it from not having looked")
	case strings.Contains(err.Error(), "already posted"):
		redirectMsg(w, r, back, "that is already posted, and a stamp is not taken back")
	default:
		redirectMsg(w, r, back, "that did not work: "+err.Error())
	}
}

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
)

// Only the two constructs the crew's own output actually uses.
var boldRe = regexp.MustCompile(`\*\*([^*]+)\*\*`)

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
	s.render(w, tplSprint, struct {
		shell
		S      crew.Sprint
		Tasks  []taskView
		CanAct bool
	}{s.shellFor(r, found.Label, "sprints"), found, views(ts), u.May("operator")})
}

// --------------------------------------------------------------------- task

type artView struct {
	crew.Artifact
	Rendered template.HTML
}

// renderBody turns an analyst's markdown-ish output into something readable
// without pulling in a markdown library.
//
// Escaped FIRST and marked up second: the body is written by a model, and a
// model's output is untrusted input like any other. Handling only the three
// constructs the crew actually emits is the point, not a limitation.
func renderBody(src string) template.HTML {
	var b strings.Builder
	for _, para := range strings.Split(src, "\n\n") {
		p := strings.TrimSpace(para)
		if p == "" {
			continue
		}
		esc := html.EscapeString(p)
		esc = boldRe.ReplaceAllString(esc, "<strong>$1</strong>")
		if strings.HasPrefix(p, "## ") {
			b.WriteString("<h3>" + strings.TrimPrefix(esc, "## ") + "</h3>")
			continue
		}
		b.WriteString("<p>" + esc + "</p>")
	}
	return template.HTML(b.String())
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
		av = append(av, artView{a, renderBody(a.Body)})
	}
	s.render(w, tplTask, struct {
		shell
		T         crew.Task
		StateChip string
		Arts      []artView
		Notes     []crew.Comment
		Analysts  []string
		CanAct    bool
	}{s.shellFor(r, t.Title, "board"), t, stateChip(t.State), av, notes,
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
	}{s.shellFor(r, "Crew", "staff"), rows, u.May("operator"), srt,
		totalSpent, totalGuard, tasks, open, posted, returned, byState,
		firstPass, states})
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
			// was a human who agreed.
			err = crew.Post(s.db, id, u.Username)
		} else {
			err = crew.Return(s.db, id, r.PostFormValue("reason"))
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

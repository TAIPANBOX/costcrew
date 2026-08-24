package web

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/TAIPANBOX/agent-stack-go/passport"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// The agent's card.
//
// One page that answers everything somebody asks about an agent before they
// let it near money: who it is, who vouches for it, what it may do, what it
// has done, what it cost, what it broke and who was on the other end. The
// console has the pieces scattered across five tables; this puts them on one
// page in the order the questions actually get asked.
//
// The order is the argument. Identity first, because nothing below it means
// anything if the identity is a name somebody typed. Then the mandate, then
// what it may do, then what it did, then the raw feed the rest is folded from.

// rightMeans says in one line what a granted right actually permits.
//
// A page that lists "sql-readonly" as a chip and stops has told a reader who
// already knew, and nobody else. The rights are the part of the card a person
// signing off on an agent reads most carefully, so each one says what it lets
// the agent reach and, where it matters, what it still cannot do.
var rightMeans = map[string]string{
	"figures-read":      "read the estate's charges, budgets and totals",
	"sql-readonly":      "run its own SELECT against the store; no write reaches the database",
	"budgets-read":      "read the guards and what has been spent against them",
	"propose-only":      "draft an answer, which a person still has to post",
	"close-covered":     "close a task once its artifact is posted and covered",
	"channel-post":      "post into the team channel it reports to",
	"publish-explainer": "publish a written explainer in a team's name, after review",
	"export-data":       "export a table as CSV or a report as one file",
	"kpi-registry":      "register and update a KPI definition",
}

// cannotEver is what no analyst may do here, whatever it was granted.
//
// The honest half, and it is on the card for the same reason the connector
// pages carry theirs: an agent's capability list read alone always reads as a
// longer list than it is.
var cannotEver = []string{
	"move money, raise a purchase or change a commitment",
	"write to the estate's charges, or to any cloud account",
	"grant itself a right, or change its own guards",
	"approve its own artifact, close its own anomaly, or sign off its own sprint",
}

type eventRow struct {
	when   string // RFC 3339, as the stream writes it
	Kind   string
	Detail string
	Sev    string
}

// When trims the stream's timestamp to what a person reads.
func (e eventRow) When() string {
	if len(e.when) >= 16 {
		return strings.Replace(e.when[:16], "T", " ", 1)
	}
	return e.when
}

type childRow struct {
	Name  string
	Role  string
	State string
}

type sprintWork struct {
	Sprint string
	Tasks  int
	Posted int
	Spent  money.Cents
}

// agentEvent is one line of the agent-event stream, as this console writes it.
type agentEvent struct {
	TS         string            `json:"ts"`
	Type       string            `json:"type"`
	AgentID    string            `json:"agent_id"`
	Severity   string            `json:"severity"`
	OnBehalfOf []string          `json:"on_behalf_of"`
	Data       map[string]any    `json:"data"`
	Extra      map[string]string `json:"-"`
}

// analystEvents is this agent's rows from the AGENT-EVENT stream.
//
// Not from the store's journal: that chain records who signed in and what
// changed about the installation, and it never names an analyst. The agent
// events are the record of what the agents did, they carry the delegation
// chain, and they are the thing another service in the stack would read about
// this agent. A card that showed the wrong log would be worse than one that
// showed none, because it looks like an answer.
//
// The whole file is read and the tail kept. It is an append-only NDJSON stream
// on local disk, sized by how much this console has done, and reading it in
// one pass is simpler than an index that could go stale.
func (s *Server) analystEvents(name string, limit int) ([]eventRow, error) {
	if s.eventsPath == "" {
		return nil, nil
	}
	f, err := os.Open(s.eventsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // switched on, nothing written yet
		}
		return nil, err
	}
	defer f.Close()

	want := "/" + name
	var out []eventRow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e agentEvent
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		// The agent that acted, anyone it acted for, and anything the payload
		// names it as: an analyst is as much part of an event that blames it
		// as of one it emitted.
		hit := strings.HasSuffix(e.AgentID, want)
		for _, o := range e.OnBehalfOf {
			hit = hit || strings.HasSuffix(o, want)
		}
		if !hit {
			for _, v := range e.Data {
				if str, ok := v.(string); ok && wordIn(str, name) {
					hit = true
					break
				}
			}
		}
		if !hit {
			continue
		}
		who := e.AgentID
		if i := strings.LastIndex(who, "/"); i >= 0 {
			who = who[i+1:]
		}
		detail := summarise(e.Data)
		if who != name {
			detail = "by " + who + ": " + detail
		}
		// The console's own word for it, with the estate's beside it.
		//
		// This page shows what went on the wire, and what goes on the wire is
		// the shared vocabulary: a detected anomaly leaves here as
		// "spend_spike". Both belong on the card. Showing only the wire word
		// makes a reader match it against a console that never says it;
		// showing only the console's word hides what another service in the
		// estate will actually see.
		kind := e.Type
		if own, ok := e.Data["costcrew_type"].(string); ok && own != "" && own != e.Type {
			kind = own + " → " + e.Type
		}
		out = append(out, eventRow{when: e.TS, Kind: kind, Detail: detail, Sev: e.Severity})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// Newest first, and only as many as the card can usefully show.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// wordIn reports whether name appears in s as a whole comma or space
// separated item, or as the whole value.
func wordIn(s, name string) bool {
	if s == name {
		return true
	}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '/' || r == ';'
	}) {
		if f == name {
			return true
		}
	}
	return false
}

// deskSpend is what the desk this agent watches actually costs, so its guards
// can be read against the size of the thing it is guarding.
func deskSpend(db *sql.DB, desk, month string) (money.Cents, error) {
	var v sql.NullInt64
	err := db.QueryRow(`SELECT SUM(billed_cents) FROM charges
		WHERE source=? AND substr(day,1,7)=?`, desk, month).Scan(&v)
	return money.Cents(v.Int64), err
}

func (s *Server) analyst(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	name := r.PathValue("name")
	a, err := crew.GetAnalyst(s.db, name)
	if err != nil {
		http.Error(w, "no such analyst", http.StatusNotFound)
		return
	}
	month := world.LastDay[:7]

	scores, _ := crew.Scoreboards(s.db)
	ts, _ := crew.Tasks(s.db, crew.TaskFilter{Assignee: name})
	caused, _ := anomaly.List(s.db, anomaly.Filter{CausedBy: name})
	handled, _ := anomaly.List(s.db, anomaly.Filter{HandledBy: name})
	events, _ := s.analystEvents(name, 40)

	// What stopped, from the board. See stops.go for why this does not come
	// from the event stream.
	stops, _ := stopsFor(s.db, name)
	ssrt := readSortNamed(r, "ssort", "when", true)
	applySort(stops, ssrt, map[string]func(a, b agentStop) int{
		"when":   func(a, b agentStop) int { return cmpString(a.When, b.When) },
		"kind":   func(a, b agentStop) int { return cmpString(a.Kind, b.Kind) },
		"task":   func(a, b agentStop) int { return cmpString(a.Task, b.Task) },
		"spent":  func(a, b agentStop) int { return cmpInt64(int64(a.Spent), int64(b.Spent)) },
		"reason": func(a, b agentStop) int { return cmpString(a.Reason, b.Reason) },
	}, "when")
	spend, _ := deskSpend(s.db, a.Desk, month)

	// Who acts under it. The roster is the only record of this, and it is what
	// the passport's parent field is built from, so the two cannot disagree.
	roster, _ := crew.Roster(s.db)
	var children []childRow
	for _, o := range roster {
		if o.Parent == a.Name {
			children = append(children, childRow{o.Name, o.Role, o.State})
		}
	}

	// Its work grouped by sprint, so a reader sees a rhythm rather than a
	// list: an agent that posted four, then four, then none is a different
	// story from one that posted twelve in a week and stopped.
	// The sprint's LABEL, not its row id. "2026-W31" is a week somebody
	// remembers; "17" is a primary key.
	label := map[int]string{}
	if sprints, err := crew.Sprints(s.db); err == nil {
		for _, sp := range sprints {
			label[sp.ID] = sp.Label
		}
	}
	bySprint := map[string]*sprintWork{}
	for _, t := range ts {
		k := label[t.Sprint]
		if k == "" {
			k = "not in a sprint"
		}
		if bySprint[k] == nil {
			bySprint[k] = &sprintWork{Sprint: k}
		}
		sw := bySprint[k]
		sw.Tasks++
		sw.Spent += t.Spent
		if t.State == "posted" || t.State == "done" || t.State == "accepted" {
			sw.Posted++
		}
	}
	rhythm := make([]sprintWork, 0, len(bySprint))
	for _, v := range bySprint {
		rhythm = append(rhythm, *v)
	}
	sort.Slice(rhythm, func(i, j int) bool { return rhythm[i].Sprint > rhythm[j].Sprint })

	// The engine it runs on, named rather than left as an id, and what that
	// engine costs to run.
	var engine engines.Engine
	for _, e := range engines.Catalogue {
		if e.ID == a.Engine {
			engine = e
			break
		}
	}

	// The document. When the governance plane is off there is no passport to
	// show, and the card says so instead of rendering a plausible one nobody
	// published.
	var doc *passport.Passport
	var docJSON string
	if s.passportFor != nil {
		p := s.passportFor(a)
		doc = &p
		if buf, err := json.MarshalIndent(p, "", "  "); err == nil {
			docJSON = string(buf)
		}
	}

	var rights []struct{ Right, Means string }
	for _, right := range a.Rights {
		means := rightMeans[right]
		if means == "" {
			means = "granted, and this console has no description for it"
		}
		rights = append(rights, struct{ Right, Means string }{right, means})
	}

	// What the desks list looks like on the transfer form, and who could own
	// it. Both are read live rather than from the fixture, so an account
	// created this morning can be handed an agent this morning.
	accounts, _ := s.au.List()
	owners := make([]string, 0, len(accounts))
	for _, x := range accounts {
		owners = append(owners, x.Username)
	}
	others := make([]string, 0, len(roster))
	for _, o := range roster {
		if o.Name != a.Name {
			others = append(others, o.Name)
		}
	}
	openWork, _ := crew.OpenWork(s.db, name)

	// Work this agent did on a desk it is no longer on. After a transfer its
	// finished tasks stay charged where they were, and a card that showed one
	// total would be quietly spanning two owners.
	elsewhere := map[string]money.Cents{}
	if rows, err := s.db.Query(`SELECT COALESCE(desk,''), COALESCE(SUM(spent_cents),0)
		FROM tasks WHERE assignee = ? GROUP BY 1`, name); err == nil {
		for rows.Next() {
			var d string
			var v int64
			if rows.Scan(&d, &v) == nil && d != a.Desk && v > 0 {
				elsewhere[d] = money.Cents(v)
			}
		}
		rows.Close()
	}

	rsrt := readSortNamed(r, "rsort", "sprint", true)
	applySort(rhythm, rsrt, map[string]func(a, b sprintWork) int{
		"sprint": func(a, b sprintWork) int { return cmpString(a.Sprint, b.Sprint) },
		"tasks":  func(a, b sprintWork) int { return cmpInt(a.Tasks, b.Tasks) },
		"posted": func(a, b sprintWork) int { return cmpInt(a.Posted, b.Posted) },
		"spent":  func(a, b sprintWork) int { return cmpInt64(int64(a.Spent), int64(b.Spent)) },
	}, "sprint")
	work := views(ts)
	wsrt := readSortNamed(r, "wsort", "state", false)
	applySort(work, wsrt, map[string]func(a, b taskView) int{
		"task":   func(a, b taskView) int { return cmpString(a.Title, b.Title) },
		"sprint": func(a, b taskView) int { return cmpInt(a.Sprint, b.Sprint) },
		"spent":  func(a, b taskView) int { return cmpInt64(int64(a.Spent), int64(b.Spent)) },
		"state":  func(a, b taskView) int { return cmpString(string(a.State), string(b.State)) },
	}, "state")

	sc := scores[name]
	// What its work has cost against what it was allowed, as a percentage, so
	// the bar on the page is a real proportion and not a guess.
	guardUsed := 0.0
	if a.Monthly > 0 {
		guardUsed = float64(sc.Spent) / float64(a.Monthly) * 100
	}

	// What of this agent's cost is real. Per-analyst, not a share of the total.
	liveMicros, liveTasks, _ := crew.LiveSpendBy(s.db, a.Name)

	s.render(w, tplAnalyst, struct {
		shell
		A          crew.Analyst
		Score      crew.Scoreboard
		Chip       string
		Tasks      []taskView
		Caused     []anomaly.Anomaly
		Handled    []anomaly.Anomaly
		Events     []eventRow
		Children   []childRow
		Rhythm     []sprintWork
		Rights     []struct{ Right, Means string }
		Cannot     []string
		Engine     engines.Engine
		Passport   *passport.Passport
		JSON       string
		DeskSpend  money.Cents
		Month      string
		GuardUsed  float64
		Host       string
		CanAct     bool
		MayManage  bool
		Desks      []string
		Owners     []string
		Others     []string
		OpenWork   int
		Elsewhere  map[string]money.Cents
		SortRhythm sortSpec
		SortWork   sortSpec
		Stops      []agentStop
		StopCount  stopSummary
		SortStops  sortSpec
		RealMoney  string
	}{s.shellFor(r, a.Name, "staff"), a, sc, agentChip(a.State),
		work, caused, handled, events, children, rhythm, rights, cannotEver,
		engine, doc, docJSON, spend, month, guardUsed, s.host, u.May("operator"),
		mayManage(u, a), append(deskNames(), "management"), owners, others,
		openWork, elsewhere, rsrt, wsrt, stops, summariseStops(stops), ssrt,
		crew.RealMoney(liveMicros, liveTasks)})
}

// analystPassport serves the document itself.
//
// The same bytes the writer publishes, so somebody can curl this, diff it
// against the file on disk, and get nothing back. A card that renders a
// document is only trustworthy if the document is reachable.
func (s *Server) analystPassport(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	a, err := crew.GetAnalyst(s.db, r.PathValue("name"))
	if err != nil {
		http.Error(w, "no such analyst", http.StatusNotFound)
		return
	}
	if s.passportFor == nil {
		http.Error(w, "the governance plane is switched off on this installation, "+
			"so no passport is published", http.StatusNotFound)
		return
	}
	buf, err := json.MarshalIndent(s.passportFor(a), "", "  ")
	if err != nil {
		http.Error(w, "the document could not be built", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline; filename="+a.Name+".json")
	_, _ = w.Write(append(buf, '\n'))
}

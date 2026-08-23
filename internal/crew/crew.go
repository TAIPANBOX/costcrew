// Package crew is the work: sprints, the tasks in them, the deliverables
// analysts write, and the operator's stamp that makes one count.
//
// The rule the whole plane is built around: an analyst proposes and only a
// person disposes. A deliverable exists as a draft the moment it is written,
// and changes nothing until somebody stamps it. Returning one requires a
// reason, for the same argument the anomaly plane makes: a rejection with no
// reason cannot be told apart from nobody having read it.
package crew

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// TaskState is where a piece of work has got to.
type TaskState string

const (
	Queued   TaskState = "queued"   // planned, not started
	Active   TaskState = "active"   // an analyst is on it
	Blocked  TaskState = "blocked"  // stopped, with a reason
	Returned TaskState = "returned" // came back from review, with a reason
	Done     TaskState = "done"     // written, awaiting a stamp
	Posted   TaskState = "posted"   // stamped: it counts
)

var openStates = map[TaskState]bool{Queued: true, Active: true, Blocked: true, Returned: true}

// ArtifactState is what has happened to one deliverable.
type ArtifactState string

const (
	Draft         ArtifactState = "draft"
	ReturnedDraft ArtifactState = "returned"
	PostedDraft   ArtifactState = "posted"
)

type Sprint struct {
	ID     int
	Label  string // 2026-W31
	Start  string
	End    string
	State  string // planned, active, closed
	Goal   string
	Tasks  int
	Open   int
	Posted int
	Spent  money.Cents
	Budget money.Cents
}

type Task struct {
	ID       int
	Sprint   int
	Title    string
	Goal     string
	Assignee string
	Desk     string
	State    TaskState
	Reason   string      // required for blocked and returned
	Budget   money.Cents // the per-task guard this work runs under
	Spent    money.Cents
	Anomaly  string // the anomaly this came from, when it came from one
	Created  string
	Updated  string
}

func (t Task) Open() bool { return openStates[t.State] }

// OverGuard is the question a per-task budget exists to answer, and it is
// asked of the task rather than of the analyst, because a guard nobody checks
// is a number in a form.
func (t Task) OverGuard() bool { return t.Budget > 0 && t.Spent > t.Budget }

type Artifact struct {
	ID      int
	Task    int
	Author  string
	Title   string
	Body    string
	State   ArtifactState
	Reason  string // why it came back, when it did
	Created string
	Stamped string
	Stamper string
}

type Comment struct {
	ID      int
	Task    int
	Author  string
	Body    string
	Created string
}

// tasks.owner is who answered for the agent when the charge was made.
//
// Stamped once, at the charge, and moved only while the work is still open.
// Without it, spend has to be read from the agent's CURRENT owner, and an
// agent changing hands rewrites history for both people: the new owner's total
// jumps by an amount they never authorised and the previous owner's drops by
// the same.
//
// No SQL comments in this string. The driver executes it as one multi-statement
// exec and a "--" comment inside it silently breaks the statement it sits in:
// the column vanished from the CREATE TABLE and the failure surfaced two
// statements later as "no such column: owner" on the index.
const Schema = `
CREATE TABLE IF NOT EXISTS sprints(
  id INTEGER PRIMARY KEY, label TEXT NOT NULL, start TEXT, finish TEXT,
  state TEXT NOT NULL, goal TEXT);
CREATE TABLE IF NOT EXISTS tasks(
  id INTEGER PRIMARY KEY, sprint INTEGER, title TEXT NOT NULL, goal TEXT,
  assignee TEXT, desk TEXT, state TEXT NOT NULL, reason TEXT,
  budget_cents INTEGER DEFAULT 0, spent_cents INTEGER DEFAULT 0,
  anomaly TEXT, created TEXT, updated TEXT, owner TEXT);
CREATE TABLE IF NOT EXISTS artifacts(
  id INTEGER PRIMARY KEY, task INTEGER NOT NULL, author TEXT, title TEXT,
  body TEXT, state TEXT NOT NULL, reason TEXT, created TEXT,
  stamped TEXT, stamper TEXT);
CREATE TABLE IF NOT EXISTS comments(
  id INTEGER PRIMARY KEY, task INTEGER NOT NULL, author TEXT, body TEXT,
  created TEXT);
CREATE INDEX IF NOT EXISTS tasks_sprint ON tasks(sprint, state);
CREATE INDEX IF NOT EXISTS tasks_assignee ON tasks(assignee, state);
CREATE INDEX IF NOT EXISTS tasks_owner ON tasks(owner);
CREATE INDEX IF NOT EXISTS artifacts_task ON artifacts(task);
`

// ------------------------------------------------------------------ reads

func Sprints(db *sql.DB) ([]Sprint, error) {
	rows, err := db.Query(`
		SELECT s.id, s.label, COALESCE(s.start,''), COALESCE(s.finish,''),
		       s.state, COALESCE(s.goal,''),
		       COUNT(t.id),
		       SUM(CASE WHEN t.state IN ('queued','active','blocked','returned') THEN 1 ELSE 0 END),
		       SUM(CASE WHEN t.state='posted' THEN 1 ELSE 0 END),
		       COALESCE(SUM(t.spent_cents),0), COALESCE(SUM(t.budget_cents),0)
		FROM sprints s LEFT JOIN tasks t ON t.sprint = s.id
		GROUP BY s.id ORDER BY s.label DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sprint
	for rows.Next() {
		var s Sprint
		var spent, budget int64
		var open, posted sql.NullInt64
		if err := rows.Scan(&s.ID, &s.Label, &s.Start, &s.End, &s.State, &s.Goal,
			&s.Tasks, &open, &posted, &spent, &budget); err != nil {
			return nil, err
		}
		s.Open, s.Posted = int(open.Int64), int(posted.Int64)
		s.Spent, s.Budget = money.Cents(spent), money.Cents(budget)
		out = append(out, s)
	}
	return out, rows.Err()
}

type TaskFilter struct {
	Sprint   int
	Assignee string
	Desk     string
	State    TaskState
	Anomaly  string
	OpenOnly bool
}

func Tasks(db *sql.DB, f TaskFilter) ([]Task, error) {
	q := `SELECT id, COALESCE(sprint,0), title, COALESCE(goal,''),
		COALESCE(assignee,''), COALESCE(desk,''), state, COALESCE(reason,''),
		budget_cents, spent_cents, COALESCE(anomaly,''),
		COALESCE(created,''), COALESCE(updated,'') FROM tasks WHERE 1=1`
	var args []any
	if f.Sprint > 0 {
		q += " AND sprint=?"
		args = append(args, f.Sprint)
	}
	for clause, v := range map[string]string{
		" AND assignee=?": f.Assignee, " AND desk=?": f.Desk,
		" AND state=?": string(f.State), " AND anomaly=?": f.Anomaly,
	} {
		if v != "" {
			q += clause
			args = append(args, v)
		}
	}
	if f.OpenOnly {
		q += " AND state IN ('queued','active','blocked','returned')"
	}
	q += " ORDER BY id DESC"

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		var budget, spent int64
		var state string
		if err := rows.Scan(&t.ID, &t.Sprint, &t.Title, &t.Goal, &t.Assignee,
			&t.Desk, &state, &t.Reason, &budget, &spent, &t.Anomaly,
			&t.Created, &t.Updated); err != nil {
			return nil, err
		}
		t.State = TaskState(state)
		t.Budget, t.Spent = money.Cents(budget), money.Cents(spent)
		out = append(out, t)
	}
	return out, rows.Err()
}

func GetTask(db *sql.DB, id int) (Task, error) {
	ts, err := Tasks(db, TaskFilter{})
	if err != nil {
		return Task{}, err
	}
	for _, t := range ts {
		if t.ID == id {
			return t, nil
		}
	}
	return Task{}, ErrNotFound
}

func Artifacts(db *sql.DB, task int) ([]Artifact, error) {
	rows, err := db.Query(`SELECT id, task, COALESCE(author,''), COALESCE(title,''),
		COALESCE(body,''), state, COALESCE(reason,''), COALESCE(created,''),
		COALESCE(stamped,''), COALESCE(stamper,'')
		FROM artifacts WHERE task=? ORDER BY id`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		var a Artifact
		var state string
		if err := rows.Scan(&a.ID, &a.Task, &a.Author, &a.Title, &a.Body,
			&state, &a.Reason, &a.Created, &a.Stamped, &a.Stamper); err != nil {
			return nil, err
		}
		a.State = ArtifactState(state)
		out = append(out, a)
	}
	return out, rows.Err()
}

func Comments(db *sql.DB, task int) ([]Comment, error) {
	rows, err := db.Query(`SELECT id, task, COALESCE(author,''), COALESCE(body,''),
		COALESCE(created,'') FROM comments WHERE task=? ORDER BY id`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Comment
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.Task, &c.Author, &c.Body, &c.Created); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Scoreboard is one analyst's record, which is what a staff card is for.
type Scoreboard struct {
	Analyst   string
	Tasks     int
	Open      int
	Posted    int
	Returned  int
	Blocked   int
	Spent     money.Cents
	FirstPass float64 // posted over (posted + returned)
	HasRate   bool
	Anomalies int // handled
}

func Scoreboards(db *sql.DB) (map[string]Scoreboard, error) {
	out := map[string]Scoreboard{}
	rows, err := db.Query(`SELECT COALESCE(assignee,''), state, COUNT(*), COALESCE(SUM(spent_cents),0)
		FROM tasks GROUP BY 1,2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var who, state string
		var n int
		var spent int64
		if err := rows.Scan(&who, &state, &n, &spent); err != nil {
			return nil, err
		}
		s := out[who]
		s.Analyst = who
		s.Tasks += n
		s.Spent += money.Cents(spent)
		switch TaskState(state) {
		case Posted:
			s.Posted += n
		case Returned:
			s.Returned += n
		case Blocked:
			s.Blocked += n
		}
		if openStates[TaskState(state)] {
			s.Open += n
		}
		out[who] = s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// First-pass acceptance, and it stays UNSET rather than 100% when nobody
	// has reviewed anything: a rate over no reviews is not a rate, and a
	// hopeful default is how a new analyst looks like the best one.
	for k, s := range out {
		if judged := s.Posted + s.Returned; judged > 0 {
			s.FirstPass = float64(s.Posted) / float64(judged) * 100
			s.HasRate = true
		}
		out[k] = s
	}

	arows, err := db.Query(`SELECT COALESCE(handled_by,''), COUNT(*) FROM anomalies
		WHERE handled_by IS NOT NULL AND handled_by <> '' GROUP BY 1`)
	if err == nil {
		defer arows.Close()
		for arows.Next() {
			var who string
			var n int
			if err := arows.Scan(&who, &n); err == nil {
				s := out[who]
				s.Analyst = who
				s.Anomalies = n
				out[who] = s
			}
		}
	}
	return out, nil
}

// ------------------------------------------------------------------ writes

var (
	ErrNotFound   = fmt.Errorf("no such task")
	ErrNeedReason = fmt.Errorf("this decision needs a reason")
	ErrSettled    = fmt.Errorf("this is already posted")
)

func stamp() string { return time.Now().UTC().Format(time.RFC3339) }

// Assign hands a task to an analyst and starts it.
func Assign(db *sql.DB, id int, analyst string) error {
	t, err := GetTask(db, id)
	if err != nil {
		return err
	}
	if t.State == Posted {
		return ErrSettled
	}
	_, err = db.Exec(`UPDATE tasks SET assignee=?, state=?, reason=NULL, updated=? WHERE id=?`,
		analyst, string(Active), stamp(), id)
	return err
}

// Block stops a task, and insists on why. A blocked task with no reason is
// indistinguishable from one nobody picked up.
func Block(db *sql.DB, id int, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return ErrNeedReason
	}
	t, err := GetTask(db, id)
	if err != nil {
		return err
	}
	if t.State == Posted {
		return ErrSettled
	}
	_, err = db.Exec(`UPDATE tasks SET state=?, reason=?, updated=? WHERE id=?`,
		string(Blocked), reason, stamp(), id)
	return err
}

// Comment adds a note to the thread. Notes are not decisions and need no
// reason, which is exactly why they are separate from the states above.
func Comment_(db *sql.DB, task int, author, body string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("an empty comment says nothing")
	}
	if _, err := GetTask(db, task); err != nil {
		return err
	}
	_, err := db.Exec(`INSERT INTO comments(task, author, body, created) VALUES (?,?,?,?)`,
		task, author, body, stamp())
	return err
}

// Post is the stamp. It is the only thing that makes a deliverable count, and
// it is a person's act rather than an analyst's.
func Post(db *sql.DB, artifactID int, stamper string) error {
	var task int
	var state string
	err := db.QueryRow(`SELECT task, state FROM artifacts WHERE id=?`, artifactID).Scan(&task, &state)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if ArtifactState(state) == PostedDraft {
		return ErrSettled
	}
	now := stamp()
	if _, err := db.Exec(`UPDATE artifacts SET state=?, stamped=?, stamper=?, reason=NULL WHERE id=?`,
		string(PostedDraft), now, stamper, artifactID); err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE tasks SET state=?, updated=? WHERE id=?`, string(Posted), now, task)
	return err
}

// Return sends a deliverable back, and the reason is the whole point: it is
// what the analyst is meant to act on.
func Return(db *sql.DB, artifactID int, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return ErrNeedReason
	}
	var task int
	var state string
	err := db.QueryRow(`SELECT task, state FROM artifacts WHERE id=?`, artifactID).Scan(&task, &state)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if ArtifactState(state) == PostedDraft {
		return ErrSettled
	}
	now := stamp()
	if _, err := db.Exec(`UPDATE artifacts SET state=?, reason=? WHERE id=?`,
		string(ReturnedDraft), reason, artifactID); err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE tasks SET state=?, reason=?, updated=? WHERE id=?`,
		string(Returned), reason, now, task)
	return err
}

// FromAnomaly opens a task to investigate one anomaly, and records where it
// came from so the two are joined in both directions.
func FromAnomaly(db *sql.DB, anomalyID, title, goal, assignee, desk string, budget money.Cents) (int, error) {
	// One task per anomaly. Two people opening the same investigation twice is
	// how the same day gets explained by two analysts with different answers.
	var existing int
	err := db.QueryRow(`SELECT id FROM tasks WHERE anomaly=?`, anomalyID).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	sprint, err := currentSprint(db)
	if err != nil {
		return 0, err
	}
	now := stamp()
	// The owner is read HERE rather than passed in: a caller holding an
	// Analyst loaded before a transfer would stamp the previous owner, and the
	// history would record a handover as though it had not happened.
	res, err := db.Exec(`INSERT INTO tasks
		(sprint, title, goal, assignee, desk, state, budget_cents, spent_cents, anomaly, created, updated, owner)
		VALUES (?,?,?,?,?,?,?,0,?,?,?,?)`,
		sprint, title, goal, nullIf(assignee), nullIf(desk),
		stateFor(assignee), int64(budget), anomalyID, now, now,
		OwnerOf(db, assignee))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return int(id), err
}

func stateFor(assignee string) string {
	if assignee == "" {
		return string(Queued)
	}
	return string(Active)
}

func currentSprint(db *sql.DB) (int, error) {
	var id int
	err := db.QueryRow(`SELECT id FROM sprints WHERE state='active' ORDER BY label DESC LIMIT 1`).Scan(&id)
	if err == sql.ErrNoRows {
		err = db.QueryRow(`SELECT id FROM sprints ORDER BY label DESC LIMIT 1`).Scan(&id)
	}
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Desks lists the desks that actually have work, in a stable order.
func Desks(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT COALESCE(desk,'') FROM tasks WHERE desk IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		if d != "" {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out, rows.Err()
}

// TaskOfArtifact answers which task a deliverable belongs to, so an action on
// one can send the reader back where they came from.
func TaskOfArtifact(db *sql.DB, artifactID int) (int, error) {
	var task int
	err := db.QueryRow(`SELECT task FROM artifacts WHERE id=?`, artifactID).Scan(&task)
	if err == sql.ErrNoRows {
		return 0, ErrNotFound
	}
	return task, err
}

// AwaitingStamp is the work that is written and not yet judged.
//
// It is derived from ARTIFACTS, not from a task state. The two drift: a task
// can sit in "active" while its deliverable is already written and waiting,
// which is exactly the case a reviewer needs to see. Counting task states
// instead measured a proxy and reported zero while six drafts sat unread.
func AwaitingStamp(db *sql.DB) ([]Task, error) {
	rows, err := db.Query(`SELECT DISTINCT t.id FROM tasks t
		JOIN artifacts a ON a.task = t.id
		WHERE a.state = 'draft' ORDER BY t.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []Task
	for _, id := range ids {
		if t, err := GetTask(db, id); err == nil {
			out = append(out, t)
		}
	}
	return out, nil
}

// ------------------------------------------------------------- planning

// Plan is a proposed sprint: what the crew would do next, routed to analysts
// by desk, with the guards it would run under.
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
}

type PlanItem struct {
	Title    string
	Goal     string
	Assignee string
	Desk     string
	Budget   money.Cents
	Why      string
}

// Propose builds the next sprint from what the estate actually needs, which is
// the difference between a plan and a template: every item names the thing it
// came from.
func Propose(db *sql.DB, label, start, end string) (Plan, error) {
	p := Plan{Label: label, Start: start, End: end,
		Goal: "Close what is open, and explain what is not."}

	var exists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sprints WHERE label=?`, label).Scan(&exists); err != nil {
		return p, err
	}
	p.Existing = exists > 0

	// Open anomalies with nobody on them are the first call on the crew's
	// time, largest first.
	rows, err := db.Query(`SELECT id, source, service, day, excess_cents
		FROM anomalies WHERE state='open' AND (handled_by IS NULL OR handled_by='')
		ORDER BY ABS(excess_cents) DESC LIMIT 6`)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var id, source, service, day string
		var excess int64
		if err := rows.Scan(&id, &source, &service, &day, &excess); err != nil {
			rows.Close()
			return p, err
		}
		p.Items = append(p.Items, PlanItem{
			Title:    fmt.Sprintf("Explain the %s move on %s", service, day),
			Goal:     fmt.Sprintf("%s against baseline on the %s desk.", money.Cents(excess), source),
			Assignee: triageDesk(source),
			Desk:     source,
			Budget:   money.Cents(15_00),
			Why:      "anomaly " + id + " is open and unowned",
		})
	}
	rows.Close()

	// Work that stopped, because a blocked task nobody revisits is the quiet
	// way a sprint's capacity disappears.
	blocked, err := Tasks(db, TaskFilter{State: Blocked})
	if err != nil {
		return p, err
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

	for _, it := range p.Items {
		p.Budget += it.Budget
	}
	return p, nil
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

// Approve materialises a plan onto the board. This is the only thing that
// creates the tasks, and it is a person's act.
func Approve(db *sql.DB, p Plan) (int, error) {
	if len(p.Items) == 0 {
		return 0, fmt.Errorf("there is nothing to plan: no anomaly is unowned and " +
			"nothing is blocked, which is a good week rather than a problem")
	}
	if p.Existing {
		return 0, fmt.Errorf("%s is already on the board", p.Label)
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT INTO sprints(label, start, finish, state, goal)
		VALUES (?,?,?,?,?)`, p.Label, p.Start, p.End, "planned", p.Goal)
	if err != nil {
		return 0, err
	}
	sid, _ := res.LastInsertId()

	now := stamp()
	n := 0
	for _, it := range p.Items {
		state := Queued
		if it.Assignee != "" {
			state = Active
		}
		if _, err := tx.Exec(`INSERT INTO tasks
			(sprint, title, goal, assignee, desk, state, budget_cents, spent_cents,
			 created, updated, owner)
			VALUES (?,?,?,?,?,?,?,0,?,?,?)`,
			sid, it.Title, it.Goal, nullIf(it.Assignee), nullIf(it.Desk),
			string(state), int64(it.Budget), now, now,
			ownerAt(tx, it.Assignee)); err != nil {
			return n, err
		}
		n++
	}
	return n, tx.Commit()
}

// CloseSprint stops a sprint accepting new work. Open tasks are NOT closed
// with it: work that did not finish did not finish, and a sprint that tidies
// itself up on the way out hides exactly the thing a retrospective needs.
func CloseSprint(db *sql.DB, id int) (int, error) {
	var state string
	if err := db.QueryRow(`SELECT state FROM sprints WHERE id=?`, id).Scan(&state); err == sql.ErrNoRows {
		return 0, fmt.Errorf("no such sprint")
	} else if err != nil {
		return 0, err
	}
	if state == "closed" {
		return 0, fmt.Errorf("that sprint is already closed")
	}
	var stillOpen int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE sprint=? AND
		state IN ('queued','active','blocked','returned')`, id).Scan(&stillOpen); err != nil {
		return 0, err
	}
	_, err := db.Exec(`UPDATE sprints SET state='closed' WHERE id=?`, id)
	return stillOpen, err
}

// SpendInMonth is what each analyst's work cost inside one month.
//
// The guard on an analyst is MONTHLY, and its Scoreboard.Spent is everything
// it has ever been charged with. Setting one against the other says most of
// the crew is over budget when the truth is that the board covers five months
// and the guard covers one. It is the same mistake as comparing a part-month
// bill with a whole-month budget, from the other end.
//
// The month comes from the sprint the work sat in, because that is when the
// work was done. A task with no sprint has no month and is left out rather
// than being charged to whichever month somebody is looking at.
func SpendInMonth(db *sql.DB, period string) (map[string]money.Cents, error) {
	rows, err := db.Query(`SELECT COALESCE(t.assignee,''), COALESCE(SUM(t.spent_cents),0)
		FROM tasks t JOIN sprints s ON s.id = t.sprint
		WHERE substr(s.start,1,7) = ? GROUP BY 1`, period)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]money.Cents{}
	for rows.Next() {
		var who string
		var v int64
		if err := rows.Scan(&who, &v); err != nil {
			return nil, err
		}
		out[who] = money.Cents(v)
	}
	return out, rows.Err()
}

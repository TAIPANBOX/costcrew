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

const Schema = `
CREATE TABLE IF NOT EXISTS sprints(
  id INTEGER PRIMARY KEY, label TEXT NOT NULL, start TEXT, finish TEXT,
  state TEXT NOT NULL, goal TEXT);
CREATE TABLE IF NOT EXISTS tasks(
  id INTEGER PRIMARY KEY, sprint INTEGER, title TEXT NOT NULL, goal TEXT,
  assignee TEXT, desk TEXT, state TEXT NOT NULL, reason TEXT,
  budget_cents INTEGER DEFAULT 0, spent_cents INTEGER DEFAULT 0,
  anomaly TEXT, created TEXT, updated TEXT);
CREATE TABLE IF NOT EXISTS artifacts(
  id INTEGER PRIMARY KEY, task INTEGER NOT NULL, author TEXT, title TEXT,
  body TEXT, state TEXT NOT NULL, reason TEXT, created TEXT,
  stamped TEXT, stamper TEXT);
CREATE TABLE IF NOT EXISTS comments(
  id INTEGER PRIMARY KEY, task INTEGER NOT NULL, author TEXT, body TEXT,
  created TEXT);
CREATE INDEX IF NOT EXISTS tasks_sprint ON tasks(sprint, state);
CREATE INDEX IF NOT EXISTS tasks_assignee ON tasks(assignee, state);
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
	res, err := db.Exec(`INSERT INTO tasks
		(sprint, title, goal, assignee, desk, state, budget_cents, spent_cents, anomaly, created, updated)
		VALUES (?,?,?,?,?,?,?,0,?,?,?)`,
		sprint, title, goal, nullIf(assignee), nullIf(desk),
		stateFor(assignee), int64(budget), anomalyID, now, now)
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

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
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
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

	// Source is "fixture" for a generated draft and "live" for one a model
	// actually wrote against somebody's key. See EnsureArtifactProvenance.
	Source string
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
CREATE TABLE IF NOT EXISTS artifact_options(
  artifact INTEGER NOT NULL, ordinal INTEGER NOT NULL, class TEXT NOT NULL,
  summary TEXT, figure_cents INTEGER NOT NULL DEFAULT 0,
  saving_cents INTEGER NOT NULL DEFAULT 0, risk TEXT, needs TEXT,
  evidence TEXT, target TEXT, state TEXT NOT NULL, decided_by TEXT, decided_at TEXT,
  reason TEXT,
  PRIMARY KEY (artifact, ordinal));
CREATE TABLE IF NOT EXISTS decision_requests(
  artifact INTEGER PRIMARY KEY, sprint INTEGER NOT NULL, owner TEXT NOT NULL,
  lapses TEXT, created TEXT);
CREATE TABLE IF NOT EXISTS plan_asks(
  id INTEGER PRIMARY KEY, sprint_label TEXT NOT NULL, month TEXT NOT NULL,
  analyst TEXT NOT NULL, micros INTEGER NOT NULL, cents INTEGER NOT NULL,
  outcome TEXT NOT NULL, reason TEXT, created TEXT);
CREATE INDEX IF NOT EXISTS tasks_sprint ON tasks(sprint, state);
CREATE INDEX IF NOT EXISTS tasks_assignee ON tasks(assignee, state);
CREATE INDEX IF NOT EXISTS tasks_owner ON tasks(owner);
CREATE INDEX IF NOT EXISTS artifacts_task ON artifacts(task);
CREATE INDEX IF NOT EXISTS artifact_options_state ON artifact_options(state);
CREATE INDEX IF NOT EXISTS decision_requests_owner ON decision_requests(owner, sprint);
CREATE INDEX IF NOT EXISTS plan_asks_month ON plan_asks(analyst, month);
CREATE INDEX IF NOT EXISTS plan_asks_label ON plan_asks(sprint_label);
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
		COALESCE(stamped,''), COALESCE(stamper,''),
		COALESCE(source,'fixture')
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
			&state, &a.Reason, &a.Created, &a.Stamped, &a.Stamper,
			&a.Source); err != nil {
			return nil, err
		}
		a.State = ArtifactState(state)
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetArtifact reads one deliverable by id. Artifacts reads by TASK, because
// every caller inside this package already has one; this is for a caller
// that only has the artifact id, the same shape internal/web/decisions.go's
// own unexported getArtifact already holds for the identical reason --
// internal/finops.applyExplainerPublish (C8-SPEC.md) is the one outside
// this package.
func GetArtifact(db *sql.DB, id int) (Artifact, error) {
	taskID, err := TaskOfArtifact(db, id)
	if err != nil {
		return Artifact{}, err
	}
	arts, err := Artifacts(db, taskID)
	if err != nil {
		return Artifact{}, err
	}
	for _, a := range arts {
		if a.ID == id {
			return a, nil
		}
	}
	return Artifact{}, ErrNotFound
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
	if err := AssignableTo(db, analyst); err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE tasks SET assignee=?, state=?, reason=NULL, updated=? WHERE id=?`,
		analyst, string(Active), stamp(), id)
	return err
}

// ErrSuspended is returned when work is handed to an analyst whose mandate has
// been withdrawn.
var ErrSuspended = errors.New("this analyst is suspended")

// AssignableTo refuses to hand new work to a suspended analyst.
//
// The task page builds its dropdown from ActiveNames, so nobody clicking
// through the console can pick a suspended name. That is the UI, and the UI is
// not the rule: this function is, and without it a form post carrying a name
// the dropdown never offered put the task on the board anyway.
//
// It refuses suspension and nothing else, deliberately. Suspension is the one
// state where RightsFor hands back nothing at all and the live runner refuses
// to price the task, so those three agree on one meaning: the mandate is
// withdrawn. Probation, restricted and onboarding are narrower authority, not
// withdrawn authority, and an analyst on probation that could be given no work
// could never come off it.
func AssignableTo(db *sql.DB, analyst string) error {
	if strings.TrimSpace(analyst) == "" {
		return nil // unassigning is not an assignment
	}
	var state string
	err := db.QueryRow(`SELECT state FROM analysts WHERE name=?`, analyst).Scan(&state)
	switch {
	case err == sql.ErrNoRows:
		return fmt.Errorf("no such analyst: %q", analyst)
	case err != nil:
		return err
	case state == string(world.Suspended):
		return fmt.Errorf("%w: %s", ErrSuspended, analyst)
	}
	return nil
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

// ErrMayNotDecide is returned when actorLink is not the link this practice's
// job descriptions have decide the class Post, Return or Approve stands for.
var ErrMayNotDecide = errors.New("this link may not decide this class")

// Post is the stamp. It is the only thing that makes a deliverable count, and
// it is a person's act rather than an analyst's.
//
// actorLink is the acting link this console asks MayDecide about before
// stamping: "analyst", "supervisor" or "owner". Every caller today is a
// person acting through the console, i.e. the owner link, which
// B1A-SPEC.md section 2 says decides everything that exists -- so the two
// callers below (internal/web/work.go, internal/web/planning.go) both pass
// "owner", and Post's refusal here is not reachable from a real request
// today. It is reachable from a test, which is the point: the check is real
// code on the path a stamp takes, not a promise standing next to it.
func Post(db *sql.DB, artifactID int, stamper, actorLink string) error {
	if ok, reason := MayDecide(actorLink, ClassTaskAccept); !ok {
		return fmt.Errorf("%w: %s", ErrMayNotDecide, reason)
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
	if _, err := db.Exec(`UPDATE artifacts SET state=?, stamped=?, stamper=?, reason=NULL WHERE id=?`,
		string(PostedDraft), now, stamper, artifactID); err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE tasks SET state=?, updated=? WHERE id=?`, string(Posted), now, task)
	return err
}

// Return sends a deliverable back, and the reason is the whole point: it is
// what the analyst is meant to act on.
//
// actorLink is checked against MayDecide the same way Post's is; see Post's
// comment for why every real caller passes "owner" and the refusal path is
// exercised by a test rather than by a request.
func Return(db *sql.DB, artifactID int, reason, actorLink string) error {
	if strings.TrimSpace(reason) == "" {
		return ErrNeedReason
	}
	if ok, why := MayDecide(actorLink, ClassTaskReturn); !ok {
		return fmt.Errorf("%w: %s", ErrMayNotDecide, why)
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

// TaskOwner reads tasks.owner: the account that answers for the analyst who
// was assigned this task when the charge was made (see the Schema comment on
// this column). The supervisor's pass reads it to decide whose decision
// request a carried option belongs in, rather than re-deriving "who owns
// this analyst today" from the roster, which is exactly the drift invariant
// 6 exists to prevent.
func TaskOwner(db *sql.DB, taskID int) (string, error) {
	var owner string
	err := db.QueryRow(`SELECT COALESCE(owner,'') FROM tasks WHERE id=?`, taskID).Scan(&owner)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	return owner, err
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
//
// Plan, PlanItem, Propose and triageDesk moved to plan.go (B4-SPEC.md): the
// five-source, skill-routed version is long enough to want its own file, the
// same way options.go and decision.go each hold one B3 concern. Approve
// below is unchanged and stays here.

// Approve materialises a plan onto the board. This is the only thing that
// creates the tasks, and it is a person's act.
//
// actorLink is checked against MayDecide the same way Post's is; see Post's
// comment for why every real caller passes "owner" and the refusal path is
// exercised by a test rather than by a request. sprint.approve is owned by
// "owner" (ROLES-2026-09.md section 1), which is what a real caller passing
// "supervisor" or "analyst" here would be refused against.
func Approve(db *sql.DB, p Plan, actorLink string) (int, error) {
	if ok, why := MayDecide(actorLink, ClassSprintApprove); !ok {
		return 0, fmt.Errorf("%w: %s", ErrMayNotDecide, why)
	}
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
//
// The supervisor's own plan-ask spend (B4-STEP-TWO-SPEC.md section 4) is
// added in on top of the tasks/sprints sum above, additively, never
// replacing it: that call is made BEFORE the sprint it plans is ever
// approved (crew.Approve refuses a second time on a label already on the
// board), so it has no task or sprint row of its own for the join above to
// find, and SettlePlanAsk keeps its own small ledger (plan_asks) keyed by
// calendar month instead, for exactly this read.
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
	if err := rows.Err(); err != nil {
		return nil, err
	}

	prows, err := db.Query(`SELECT analyst, COALESCE(SUM(cents),0) FROM plan_asks
		WHERE month = ? GROUP BY analyst`, period)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	for prows.Next() {
		var who string
		var v int64
		if err := prows.Scan(&who, &v); err != nil {
			return nil, err
		}
		out[who] += money.Cents(v)
	}
	return out, prows.Err()
}

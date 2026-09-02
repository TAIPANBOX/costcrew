package crew

// The decision request: the supervisor's own deliverable, one per owner per
// sprint, that carries the classes its job description hands up.
// B3-SPEC.md section 4, steps 5 and 6.
//
// `@yurii 2026-09-02`: "супервайзер питає власника тільки тоді, коли він сам
// не може вирішити це питання, тобто, що стосується безпосередньо взаємодії
// людей або прийняття якихось ключових рішень, а не щоразу, коли в агента
// виникають якісь спірні моменти."

import (
	"database/sql"
)

// supervisorTaskTitle names the one task per sprint every decision request
// is a deliverable of, so EnsureSupervisorTask can find it again rather than
// creating a second one.
const supervisorTaskTitle = "Decision requests"

// EnsureSupervisorTask finds, or creates, the supervisor's own task for a
// sprint. Every decision request the supervisor's pass writes is a
// deliverable of this ONE task, never a task per owner: the task is the
// supervisor's work item ("route what this sprint handed up"), and the
// owners it writes to are which artifacts hang off it.
func EnsureSupervisorTask(db *sql.DB, sprintID int) (int, error) {
	var id int
	err := db.QueryRow(`SELECT id FROM tasks WHERE sprint=? AND assignee='supervisor' AND title=?`,
		sprintID, supervisorTaskTitle).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	now := stamp()
	res, err := db.Exec(`INSERT INTO tasks
		(sprint, title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated, owner)
		VALUES (?,?,?,?,?,?,0,0,?,?,?)`,
		sprintID, supervisorTaskTitle,
		"Route what this sprint's deliverables handed up: apply what the "+
			"supervisor's own job description decides, and ask the owner for the rest.",
		"supervisor", "management", string(Active), now, now, OwnerOf(db, "supervisor"))
	if err != nil {
		return 0, err
	}
	lid, err := res.LastInsertId()
	return int(lid), err
}

// DecisionRequestFor finds the decision request artifact already written for
// one owner in one sprint, if any -- what "one decision request per owner
// per sprint" (B3-SPEC.md section 6) is checked against: running the
// supervisor's pass twice must not write a second one.
func DecisionRequestFor(db *sql.DB, sprintID int, owner string) (artifactID int, found bool, err error) {
	err = db.QueryRow(`SELECT artifact FROM decision_requests WHERE sprint=? AND owner=?`,
		sprintID, owner).Scan(&artifactID)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	return artifactID, err == nil, err
}

// ExistingLapses reads the date already on file for one owner's request in
// one sprint, if any: the date WriteDecisionRequest must not move on a
// rewrite, and decisionRequestBody must render honestly, past or not.
func ExistingLapses(db *sql.DB, sprintID int, owner string) (lapses string, found bool, err error) {
	err = db.QueryRow(`SELECT lapses FROM decision_requests WHERE sprint=? AND owner=?`,
		sprintID, owner).Scan(&lapses)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return lapses, err == nil, err
}

// WriteDecisionRequest writes, or rewrites the body of, the one decision
// request for (sprintID, owner). Rewriting rather than duplicating is what
// makes running the supervisor's pass a second time (more options carried
// since the first run) still "one decision request per owner per sprint".
//
// lapses is used ONLY the first time a request is created. On a rewrite the
// stored date is left exactly as it was: a promise "answer by X" whose X
// keeps moving every time the pass reruns is the false promise heraldyx
// once made ("eventually times out") and had to retract. Nothing enforces
// the date either way -- see decisionRequestBody's own words -- but at
// least it stays the date it always was. Call ExistingLapses first if the
// caller needs to know what that date already is, e.g. to render the body
// with the SAME date this write is about to (not) change.
func WriteDecisionRequest(db *sql.DB, sprintID int, owner, body, lapses string) (int, error) {
	if artID, found, err := DecisionRequestFor(db, sprintID, owner); err != nil {
		return 0, err
	} else if found {
		if _, err := db.Exec(`UPDATE artifacts SET body=?, state=? WHERE id=?`,
			body, string(Draft), artID); err != nil {
			return 0, err
		}
		return artID, nil
	}

	taskID, err := EnsureSupervisorTask(db, sprintID)
	if err != nil {
		return 0, err
	}
	res, err := db.Exec(`INSERT INTO artifacts (task, author, title, body, state, created)
		VALUES (?, 'supervisor', ?, ?, 'draft', datetime('now'))`,
		taskID, "Decision request for "+owner, body)
	if err != nil {
		return 0, err
	}
	artID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := db.Exec(`INSERT INTO decision_requests (artifact, sprint, owner, lapses, created)
		VALUES (?,?,?,?,datetime('now'))`, artID, sprintID, owner, lapses); err != nil {
		return 0, err
	}
	return int(artID), nil
}

// PostDecisionRequestIfComplete is section 5's closing rule: "a decision
// request with every option answered is posted". Called after every
// apply/refuse on a carried option; a no-op until CarriedOptionsFor(sprint,
// owner) is empty, at which point the decision request's own artifact
// becomes posted -- not through Post itself, which would ask MayDecide about
// task.accept for an actor this call has no person to name, but the same
// state transition, because the request needed no further stamp of its own:
// every option already carries whoever decided IT.
func PostDecisionRequestIfComplete(db *sql.DB, sprintID int, owner string) error {
	remaining, err := CarriedOptionsFor(db, sprintID, owner)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return nil
	}
	artID, found, err := DecisionRequestFor(db, sprintID, owner)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if _, err := db.Exec(`UPDATE artifacts SET state=?, stamped=datetime('now'), stamper=? WHERE id=? AND state<>?`,
		string(PostedDraft), owner, artID, string(PostedDraft)); err != nil {
		return err
	}
	return nil
}

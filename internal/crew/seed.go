package crew

import (
	"database/sql"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// Seed fills the board with the work a crew this size would have done.
//
// A console that opens on an empty board teaches nobody anything: the states
// that matter are the unhappy ones, and they only exist in a history. So the
// fixture carries blocked work, returned work, work over its guard, and an
// analyst on probation whose first-pass rate is genuinely poor rather than
// asserted.
//
// Deterministic by construction: every choice comes from a hash of the thing
// being decided, never from a sequential generator, so the board is identical
// on every machine and does not depend on iteration order.
func Seed(db *sql.DB, anomalies []AnomalySeed) (sprints, tasks, artifacts int, err error) {
	if _, err := db.Exec(Schema); err != nil {
		return 0, 0, 0, err
	}
	var have int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sprints`).Scan(&have); err != nil {
		return 0, 0, 0, err
	}
	if have > 0 {
		return 0, 0, 0, nil
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, 0, 0, err
	}
	defer tx.Rollback()

	weeks := isoWeeks(world.FirstDay, world.LastDay)
	// Twenty sprints is a season of work: enough for a rate to mean something
	// and few enough that the board is still readable.
	if len(weeks) > 20 {
		weeks = weeks[len(weeks)-20:]
	}

	taskID, artID := 0, 0
	for i, wk := range weeks {
		state := "closed"
		switch {
		case i == len(weeks)-1:
			state = "active"
		case i == len(weeks)-2:
			state = "closed"
		}
		res, err := tx.Exec(`INSERT INTO sprints(label, start, finish, state, goal) VALUES (?,?,?,?,?)`,
			wk.label, wk.start, wk.end, state, sprintGoal(wk.label))
		if err != nil {
			return 0, 0, 0, err
		}
		sid64, _ := res.LastInsertId()
		sid := int(sid64)
		sprints++

		for _, t := range plannedWork(wk, sid, state) {
			taskID++
			if _, err := tx.Exec(`INSERT INTO tasks
				(id, sprint, title, goal, assignee, desk, state, reason,
				 budget_cents, spent_cents, anomaly, created, updated)
				VALUES (?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
				taskID, sid, t.Title, t.Goal, t.Assignee, t.Desk, string(t.State),
				nullIf(t.Reason), int64(t.Budget), int64(t.Spent),
				wk.start, wk.end); err != nil {
				return 0, 0, 0, err
			}
			tasks++
			if a, ok := deliverableFor(t, taskID, wk); ok {
				artID++
				if _, err := tx.Exec(`INSERT INTO artifacts
					(id, task, author, title, body, state, reason, created, stamped, stamper)
					VALUES (?,?,?,?,?,?,?,?,?,?)`,
					artID, taskID, a.Author, a.Title, a.Body, string(a.State),
					nullIf(a.Reason), wk.end, nullIf(a.Stamped), nullIf(a.Stamper)); err != nil {
					return 0, 0, 0, err
				}
				artifacts++
			}
		}
	}

	// Every anomaly the estate produced gets its investigation opened in the
	// current sprint, which is what joins the two planes in both directions.
	var current int
	if err := tx.QueryRow(`SELECT id FROM sprints WHERE state='active' LIMIT 1`).Scan(&current); err != nil {
		current = sprints
	}
	for _, an := range anomalies {
		taskID++
		assignee, state := triageFor(an), Active
		if pick(an.ID, 3) == 0 {
			assignee, state = "", Queued
		}
		if _, err := tx.Exec(`INSERT INTO tasks
			(id, sprint, title, goal, assignee, desk, state, reason,
			 budget_cents, spent_cents, anomaly, created, updated)
			VALUES (?,?,?,?,?,?,?,NULL,?,?,?,?,?)`,
			taskID, current,
			fmt.Sprintf("Explain the %s move on %s", an.Service, an.Day),
			fmt.Sprintf("%s %s of baseline on the %s desk. Say what happened, "+
				"whether it recurs, and what it would take to stop it.",
				an.Excess.String(), directionWord(an.Direction), an.Source),
			nullIf(assignee), an.Source, string(state),
			int64(money.Cents(15_00)), int64(triageCost(an.ID)),
			an.ID, an.Day, an.Day); err != nil {
			return 0, 0, 0, err
		}
		tasks++
	}

	return sprints, tasks, artifacts, tx.Commit()
}

// AnomalySeed is the little this package needs to know about an anomaly, so
// it does not depend on the package that owns them.
type AnomalySeed struct {
	ID        string
	Source    string
	Service   string
	Day       string
	Direction string
	Excess    money.Cents
}

func directionWord(d string) string {
	if d == "down" {
		return "below"
	}
	return "above"
}

func triageFor(a AnomalySeed) string {
	switch a.Source {
	case "aws", "gcp", "azure":
		return "triage-" + a.Source
	case "ai":
		return "triage-ai"
	case "saas":
		return "saas-manager"
	}
	return "investigator-onprem"
}

func triageCost(id string) money.Cents {
	// Cents, so a triage that cost 42 cents reads as 0.42 and not as free.
	return money.Cents(20 + pick(id, 260))
}

// pick is a deterministic small integer derived from a string.
func pick(s string, n int) int {
	h := fnv.New64a()
	h.Write([]byte(s))
	return int(h.Sum64() % uint64(n))
}

type week struct{ label, start, end string }

func isoWeeks(from, to string) []week {
	f, err := time.Parse("2006-01-02", from)
	if err != nil {
		return nil
	}
	t, err := time.Parse("2006-01-02", to)
	if err != nil {
		return nil
	}
	// Walk to the first Monday so a sprint is a working week rather than an
	// arbitrary seven days.
	for f.Weekday() != time.Monday {
		f = f.AddDate(0, 0, 1)
	}
	var out []week
	for d := f; !d.After(t); d = d.AddDate(0, 0, 7) {
		y, w := d.ISOWeek()
		out = append(out, week{
			label: fmt.Sprintf("%d-W%02d", y, w),
			start: d.Format("2006-01-02"),
			end:   d.AddDate(0, 0, 6).Format("2006-01-02"),
		})
	}
	return out
}

func sprintGoal(label string) string {
	goals := []string{
		"Close the month: variance commentary on every desk that moved.",
		"Commitment coverage: what expires, what to renew, what to let lapse.",
		"Allocation: get untagged spend under a rule rather than under a heading.",
		"Unit economics for the AI desk: cost per outcome, not cost per token.",
		"Rightsizing pass on the two largest desks, evidence attached.",
		"Chargeback close, with a true-up somebody can read.",
	}
	return goals[pick(label, len(goals))]
}

// plannedWork is what a desk does in a week.
func plannedWork(wk week, sprint int, sprintState string) []Task {
	type spec struct {
		title, goal, assignee, desk string
	}
	specs := []spec{
		{"Variance commentary, aws", "Explain every team's month-on-month move above 5 percent.", "investigator-aws", "aws"},
		{"Rightsizing candidates, aws", "Proven at p95 over 14 days, with the rule printed beside the finding.", "optimizer-aws", "aws"},
		{"Desk report, gcp", "One page a stakeholder can act on, every number tied to its source.", "reporter-gcp", "gcp"},
		{"Commitment waterline", "Utilisation against the 80 percent line, and what expires inside 90 days.", "commitments", "management"},
		{"Allocation coverage", "How much cost still has no team, and which rule would give it one.", "chargeback", "management"},
		{"AI spend review", "Tokens and GPU hours beside cost; price separated from volume.", "ai-spend", "ai"},
		{"Azure tag coverage", "Which subscriptions are still untagged and who owns them.", "investigator-azure", "azure"},
		{"SaaS seats against issued", "Money sitting in licences nobody signed into this quarter.", "saas-manager", "saas"},
		{"Forecast freeze", "Freeze the month's forecast and record the accuracy of the last one.", "forecaster", "management"},
	}

	var out []Task
	for i, s := range specs {
		key := wk.label + "|" + s.title
		// Not every desk does everything every week.
		if pick(key, 10) < 3 {
			continue
		}
		t := Task{
			Sprint: sprint, Title: s.title + " (" + wk.label + ")",
			Goal: s.goal, Assignee: s.assignee, Desk: s.desk,
			Budget: money.Cents(1500 + pick(key, 1500)),
		}
		t.Spent = money.Cents(pick(key+"spend", int(t.Budget)+400))

		switch {
		case sprintState == "active":
			// The open sprint is a working week: mostly in flight.
			switch pick(key+"state", 10) {
			case 0:
				t.State, t.Reason = Blocked, blockedReasons[pick(key, len(blockedReasons))]
			case 1:
				t.State = Queued
			case 2:
				t.State = Done
			default:
				t.State = Active
			}
		default:
			switch pick(key+"state", 12) {
			case 0:
				t.State, t.Reason = Returned, returnReasons[pick(key, len(returnReasons))]
			case 1:
				t.State, t.Reason = Blocked, blockedReasons[pick(key, len(blockedReasons))]
			default:
				t.State = Posted
			}
		}
		// The analyst on probation genuinely has work coming back, rather than
		// a rate asserted on a card.
		if i%4 == 0 && pick(key+"probation", 5) == 0 {
			t.Assignee = "intake-triage"
			t.State, t.Reason = Returned, returnReasons[pick(key, len(returnReasons))]
		}
		out = append(out, t)
	}
	return out
}

var blockedReasons = []string{
	"Tagging feed from the azure desk has been stale since the 9th; the numbers would be wrong.",
	"Waiting on the platform team to confirm which account owns the untagged EC2.",
	"Cost Explorer call is metered and has not been approved for this month yet.",
	"The commitment inventory export has not landed; nothing to reconcile against.",
}

var returnReasons = []string{
	"Ranked by z-score rather than by money. Re-rank and resubmit.",
	"The saving is stated as saved. It is found until somebody acts on it.",
	"Two figures have no provenance id, so nobody can check them.",
	"Says 'significant increase' without saying how much or against what.",
	"The recommendation has no owner and no date, so it cannot be actioned.",
}

// deliverableFor writes the analyst's output, when there is one.
func deliverableFor(t Task, taskID int, wk week) (Artifact, bool) {
	switch t.State {
	case Queued, Blocked:
		return Artifact{}, false
	}
	key := fmt.Sprintf("%s|%d", wk.label, taskID)
	a := Artifact{
		Task: taskID, Author: t.Assignee,
		Title:   strings.TrimSuffix(t.Title, " ("+wk.label+")"),
		Body:    body(t, wk, key),
		Created: wk.end,
	}
	switch t.State {
	case Posted:
		a.State, a.Stamped, a.Stamper = PostedDraft, wk.end, "operator"
	case Returned:
		a.State, a.Reason = ReturnedDraft, t.Reason
	default:
		a.State = Draft
	}
	return a, true
}

func body(t Task, wk week, key string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", strings.TrimSuffix(t.Title, " ("+wk.label+")"))
	fmt.Fprintf(&b, "%s\n\n", t.Goal)
	fmt.Fprintf(&b, "**What moved.** The %s desk finished the week at %s against a "+
		"plan of %s. The move is concentrated in two teams rather than spread, "+
		"which is why this is one finding and not four.\n\n",
		t.Desk, money.Cents(80_000+pick(key, 40_000)).String(),
		money.Cents(85_000+pick(key+"p", 30_000)).String())
	fmt.Fprintf(&b, "**What it would take.** The change is reversible inside a sprint "+
		"and needs one owner on the platform side. Estimated at %s a month, "+
		"found rather than saved: nothing moves until somebody acts.\n\n",
		money.Cents(20_000+pick(key+"s", 60_000)).String())
	b.WriteString("**What I could not establish.** Two of the largest line items " +
		"carry no team tag, so the split between them is an assumption and is " +
		"marked as one in the table.\n")
	return b.String()
}

package web_test

// The task page renders an option's own fields, B3-SPEC.md section 2. The
// hostile-input case a save-time refusal cannot cover on its own -- a script
// tag in an option's summary is legal TEXT, not a malformed block -- is a
// rendering property, held here rather than in internal/crew's parser tests.

import (
	"strconv"
	"strings"
	"testing"
)

func TestAScriptTagInAnOptionSummaryRendersAsText(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "boss", "boss-password-2026")

	taskID := plantOptionTask(t, h, "<script>alert(1)</script>", "aws")

	code, body, _ := h.get(t, "/task/"+strconv.Itoa(taskID))
	if code != 200 {
		t.Fatalf("/task/%d answered %d", taskID, code)
	}
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatalf("the option's summary reached the page as a literal <script> tag:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("the escaped form is not on the page at all, so this test may not be "+
			"looking at the option's summary:\n%s", body)
	}
}

// And the ordinary case: the class and state are there for a reader to see,
// which is the reason section 2 asks the task page to render options at all.
func TestTheTaskPageShowsAnOptionsClassAndState(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "boss", "boss-password-2026")

	taskID := plantOptionTask(t, h, "a scheduled batch job", "aws")

	code, body, _ := h.get(t, "/task/"+strconv.Itoa(taskID))
	if code != 200 {
		t.Fatalf("/task/%d answered %d", taskID, code)
	}
	if !strings.Contains(body, "anomaly.explain") {
		t.Errorf("the option's class is not on the page:\n%s", body)
	}
	if !strings.Contains(body, "open") {
		t.Errorf("the option's state is not on the page:\n%s", body)
	}
}

func plantOptionTask(t *testing.T, h *harness, summary, desk string) int {
	t.Helper()
	db := h.st.DB()
	tres, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated)
		VALUES ('a task', 'a goal', ?, ?, 'active', 0, 0, datetime('now'), datetime('now'))`,
		"investigator-"+desk, desk)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := tres.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	ares, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created)
		VALUES (?, ?, 'a deliverable', 'a deliverable body', 'draft', datetime('now'))`,
		taskID, "investigator-"+desk)
	if err != nil {
		t.Fatal(err)
	}
	artID, err := ares.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state)
		VALUES (?, 1, 'anomaly.explain', ?, 100000, 0, 'low', 'nothing', '[]', 'open')`,
		artID, summary); err != nil {
		t.Fatal(err)
	}
	return int(taskID)
}

// DRIVER-WINDOW-SPEC.md section 3: "the task page renders the window beside
// a driver option (the option view already renders class and summary)."
// Red against unchanged code: optionView carries no Window field and the
// template has no such row, so the window's own dates were never on the
// page at all.
func TestTheTaskPageShowsADriverOptionsWindow(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "boss", "boss-password-2026")

	taskID := plantDriverOptionTask(t, h, "aws", `{"start": "2026-08-01", "end": "2026-08-30"}`)

	code, body, _ := h.get(t, "/task/"+strconv.Itoa(taskID))
	if code != 200 {
		t.Fatalf("/task/%d answered %d", taskID, code)
	}
	if !strings.Contains(body, "2026-08-01 to 2026-08-30") {
		t.Errorf("the driver option's own window is not on the page:\n%s", body)
	}
}

// And the other direction: an option with no target (every non-driver class,
// and a driver option saved before this spec) renders no Window row at all,
// rather than an empty one.
func TestTheTaskPageOmitsTheWindowRowForAnOptionWithNoTarget(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "boss", "boss-password-2026")

	taskID := plantOptionTask(t, h, "a scheduled batch job", "aws") // anomaly.explain, no target column at all

	code, body, _ := h.get(t, "/task/"+strconv.Itoa(taskID))
	if code != 200 {
		t.Fatalf("/task/%d answered %d", taskID, code)
	}
	if strings.Contains(body, "<dt>Window</dt>") {
		t.Errorf("a Window row rendered for an option with no target:\n%s", body)
	}
}

// plantDriverOptionTask is plantOptionTask for a driver.recurring option
// carrying a target, since that helper's own class (anomaly.explain) never
// has one.
func plantDriverOptionTask(t *testing.T, h *harness, desk, target string) int {
	t.Helper()
	db := h.st.DB()
	tres, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated)
		VALUES ('a task', 'a goal', ?, ?, 'active', 0, 0, datetime('now'), datetime('now'))`,
		"investigator-"+desk, desk)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := tres.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	ares, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created)
		VALUES (?, ?, 'a deliverable', 'a deliverable body', 'draft', datetime('now'))`,
		taskID, "investigator-"+desk)
	if err != nil {
		t.Fatal(err)
	}
	artID, err := ares.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, target, state)
		VALUES (?, 1, 'driver.recurring', 'a weekly batch job', 100000, 0, 'low', 'nothing', '[]', ?, 'open')`,
		artID, target); err != nil {
		t.Fatal(err)
	}
	return int(taskID)
}

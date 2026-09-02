package crew_test

// The options block: B3-SPEC.md section 2's rules, each with a test, and the
// hostile inputs section 6 names.
//
// `@yurii 2026-09-02`: "він має давати на вибір якісь певні рішення, які він
// вважає за потрібне спочатку супервайзеру, тобто головному агенту, а вже
// той має запитувати юзера, користувача, власника цих агентів, що робити
// далі."

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// validOptionsBlock is one legal anomaly.explain option, the investigator
// family's own class (roles.yaml), reused as the base every hostile-input
// subtest below mutates exactly one thing in.
const validOptionsBlock = "## What happened\nA batch job ran long.\n\n```options\n" +
	`{"options": [{"class": "anomaly.explain", "summary": "a scheduled batch job",` +
	` "figure_cents": 104000, "saving_cents": 0, "risk": "low", "needs": "nothing",` +
	` "evidence": ["series aws/ml-platform/Amazon EC2"]}]}` + "\n```\n"

// TestOptionsBlockHostileInputs is B3-SPEC.md section 6's list, minus the
// script tag in summary (that is a RENDERING property, held by
// internal/web's test that the task page escapes it, not a save-time
// refusal: a script tag is legal TEXT for an option to carry).
func TestOptionsBlockHostileInputs(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"not JSON", "## Something happened\n\n```options\nthis is not json at all\n```\n"},
		{"a class that does not exist", "## Something\n\n```options\n" +
			`{"options": [{"class": "make.coffee", "summary": "x", "figure_cents": 100}]}` + "\n```\n"},
		{"a negative figure", "## Something\n\n```options\n" +
			`{"options": [{"class": "anomaly.explain", "summary": "x", "figure_cents": -500}]}` + "\n```\n"},
		{"50 options", "## Something\n\n```options\n{\"options\": [" +
			strings.TrimSuffix(strings.Repeat(`{"class":"anomaly.explain","summary":"x","figure_cents":1},`, 50), ",") +
			"]}\n```\n"},
		{"a 1 MB block", "## Something\n\n```options\n" +
			`{"options": [{"class": "anomaly.explain", "summary": "` +
			strings.Repeat("x", 1_100_000) + `", "figure_cents": 100}]}` + "\n```\n"},
		{"a string where an integer goes", "## Something\n\n```options\n" +
			`{"options": [{"class": "anomaly.explain", "summary": "x", "figure_cents": "a lot"}]}` + "\n```\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := optionsTestDB(t)
			taskID := plantPlainTask(t, db)
			artID := plantDraftArtifact(t, db, taskID, c.body)

			refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "investigator-aws", c.body, nil)
			if err != nil {
				t.Fatalf("ValidateAndSaveOptions returned an error rather than a refusal: %v", err)
			}
			if !refused {
				t.Fatalf("%s: want refused, got accepted", c.name)
			}
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s: refused with no reason", c.name)
			}
			opts, err := crew.Options(db, artID)
			if err != nil {
				t.Fatal(err)
			}
			if len(opts) != 0 {
				t.Errorf("%s: %d options were stored despite the refusal", c.name, len(opts))
			}
		})
	}
}

// A whole-number figure, and a class inside the role's own vocabulary, is
// accepted: the positive case every hostile-input subtest above is a
// deviation from.
func TestAWellFormedOptionIsAccepted(t *testing.T) {
	db := optionsTestDB(t)
	taskID := plantPlainTask(t, db)
	artID := plantDraftArtifact(t, db, taskID, validOptionsBlock)

	refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "investigator-aws", validOptionsBlock, nil)
	if err != nil {
		t.Fatal(err)
	}
	if refused {
		t.Fatalf("a well-formed option in the role's own vocabulary was refused: %s", reason)
	}
	opts, err := crew.Options(db, artID)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 1 {
		t.Fatalf("got %d options, want 1", len(opts))
	}
	if opts[0].Class != "anomaly.explain" || opts[0].FigureCents != 104000 || opts[0].State != crew.OptionOpen {
		t.Errorf("option stored wrong: %+v", opts[0])
	}
}

// Zero options is refused for a role whose own decides_alone list is not
// entirely prose (an investigator decides anomaly.explain, a real class), and
// accepted for one whose is (a reporter's are both commentary classes).
func TestZeroOptionsIsAllowedOnlyForProse(t *testing.T) {
	db := optionsTestDB(t)

	investigatorTask := plantPlainTask(t, db)
	prose := "## Just a note\nNothing to decide here.\n"
	artID := plantDraftArtifact(t, db, investigatorTask, prose)
	refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "investigator-aws", prose, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !refused {
		t.Errorf("an investigator's deliverable with no options block was accepted")
	}
	if strings.TrimSpace(reason) == "" {
		t.Errorf("refused with no reason")
	}

	// A reporter's OWN decides_alone is commentary-only, but its hands_up
	// (explainer.publish, message.team) is not: it still owes an options
	// block once those classes are in play, so it is refused too. This is
	// the fix AllowsNoOptions needed -- checking decides_alone alone let a
	// role with a real hands_up list skip the block entirely.
	reporterTask := plantPlainTask(t, db)
	artID2 := plantDraftArtifact(t, db, reporterTask, prose)
	refused2, reason2, err := crew.ValidateAndSaveOptions(db, artID2, "reporter-aws", prose, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !refused2 {
		t.Errorf("a reporter's prose deliverable was accepted with no options block, even "+
			"though its own hands_up (explainer.publish, message.team) is not prose: %s", reason2)
	}

	// A role whose whole vocabulary -- decides_alone AND hands_up -- is
	// empty has no machine-checked class to attach an option to at all, and
	// is genuinely allowed to skip the block.
	benchTask := plantPlainTask(t, db)
	artID3 := plantDraftArtifact(t, db, benchTask, prose)
	refused3, reason3, err := crew.ValidateAndSaveOptions(db, artID3, "benchmarking", prose, nil)
	if err != nil {
		t.Fatal(err)
	}
	if refused3 {
		t.Errorf("a role with no decides_alone and no hands_up was refused for naming no options: %s", reason3)
	}
}

// AllowsNoOptions is false for a role whose decides_alone is empty but
// whose hands_up is not: such a role still owes an options block once a
// hands-up class is in play, and checking decides_alone alone let it skip
// the block entirely, vacuously true on nothing.
func TestAllowsNoOptionsIsFalseForAHandsUpOnlyRole(t *testing.T) {
	role := crew.JobDescription{Family: "test-hands-up-only", HandsUp: []string{"period.close"}}
	if crew.AllowsNoOptions(role) {
		t.Error("a role whose only classes are hands_up was allowed to skip the options block")
	}
}

func optionsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.DB()
	if _, err := db.Exec(crew.Schema); err != nil {
		t.Fatal(err)
	}
	return db
}

func plantPlainTask(t *testing.T, db *sql.DB) int {
	t.Helper()
	res, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated)
		VALUES ('a task', 'a goal', 'investigator-aws', 'aws', 'active', 0, 0,
		        datetime('now'), datetime('now'))`)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return int(id)
}

func plantDraftArtifact(t *testing.T, db *sql.DB, taskID int, body string) int {
	t.Helper()
	res, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created)
		VALUES (?, 'investigator-aws', 'a deliverable', ?, 'draft', datetime('now'))`,
		taskID, body)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return int(id)
}

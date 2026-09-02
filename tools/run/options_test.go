package main

// B3-SPEC.md section 6's first two named tests, and where saveDraft's own
// options orchestration (parse, save or refuse-and-return, journal) is
// actually exercised: the two rows of the apply table that need no anomaly
// or period context to prove (a legal class is saved; an illegal one is
// refused and the task comes back), so runnerTasks (provenance_test.go) is
// enough scaffolding.

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

const anInvestigatorsOptionsBlock = "## What happened\nA batch job ran long on the 14th.\n\n" +
	"```options\n" +
	`{"options": [{"class": "anomaly.explain", "summary": "a scheduled batch job, documented",` +
	` "figure_cents": 104000, "saving_cents": 0, "risk": "low", "needs": "nothing",` +
	` "evidence": ["series aws/ml-platform/Amazon EC2"]}]}` + "\n```\n"

// Red first, against the code before this step: today's saveDraft writes
// only artifacts and tasks.spent_cents, and the fenced block above lands in
// artifacts.body as plain prose -- nothing parses it, and there is no
// artifact_options table for a row to land in at all.
func TestADeliverableEndsInOptionsTheRoleMayName(t *testing.T) {
	db, tasks, _ := runnerTasks(t, 1)
	analyst := crew.Analyst{Name: "investigator-aws", Role: "Investigator", Desk: "aws", State: "active"}

	if err := saveDraft(db, estimate{Task: tasks[0], Analyst: analyst},
		callResult{Text: anInvestigatorsOptionsBlock}, bus{}); err != nil {
		t.Fatal(err)
	}

	as, err := crew.Artifacts(db, tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 1 {
		t.Fatalf("wrote %d artifacts, want 1", len(as))
	}
	if as[0].State != crew.Draft {
		t.Errorf("state %q: a legal option must not return the deliverable", as[0].State)
	}

	opts, err := crew.Options(db, as[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 1 {
		t.Fatalf("got %d options, want 1", len(opts))
	}
	if opts[0].Class != "anomaly.explain" {
		t.Errorf("class %q, want anomaly.explain", opts[0].Class)
	}
	if opts[0].State != crew.OptionOpen {
		t.Errorf("state %q, want open", opts[0].State)
	}
	if opts[0].FigureCents != 104000 {
		t.Errorf("figure_cents %d, want 104000", opts[0].FigureCents)
	}

	task, err := crew.GetTask(db, tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.State == crew.Returned {
		t.Errorf("the task was returned even though the option is legal")
	}
}

// Red first, against the code before this step: for the same reason -- there
// is no options block mechanism at all yet, so nothing could refuse a class
// outside the role's own vocabulary, let alone return the deliverable for it.
func TestAnOptionOutsideTheRoleIsRefusedAndReturned(t *testing.T) {
	db, tasks, _ := runnerTasks(t, 1)
	analyst := crew.Analyst{Name: "investigator-aws", Role: "Investigator", Desk: "aws", State: "active"}
	body := "## What should happen\nClose the books.\n\n```options\n" +
		`{"options": [{"class": "period.close", "summary": "close August",` +
		` "figure_cents": 500000, "saving_cents": 0, "risk": "low", "needs": "the owner"}]}` +
		"\n```\n"

	if err := saveDraft(db, estimate{Task: tasks[0], Analyst: analyst},
		callResult{Text: body}, bus{}); err != nil {
		t.Fatal(err)
	}

	as, err := crew.Artifacts(db, tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 1 {
		t.Fatalf("wrote %d artifacts, want 1", len(as))
	}
	if as[0].State != crew.ReturnedDraft {
		t.Errorf("state %q, want returned", as[0].State)
	}
	if strings.TrimSpace(as[0].Reason) == "" {
		t.Errorf("returned with no reason")
	}

	opts, err := crew.Options(db, as[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 0 {
		t.Errorf("%d options were stored despite the refusal: an investigator does not "+
			"own period.close", len(opts))
	}

	task, err := crew.GetTask(db, tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.State != crew.Returned {
		t.Errorf("task state %q, want returned", task.State)
	}
	if strings.TrimSpace(task.Reason) == "" {
		t.Errorf("the task came back with no reason")
	}
}

package finops_test

// C9-SPEC.md section 2: "data.halt applied (internal/finops/apply.go):
// suspends every active analyst on the named desk with the reason ...
// records the halt with its start day; -due and Propose skip a halted desk
// and say so in Why."
//
// Red first, against main: applySideEffect has no data.halt case yet, so
// Apply on such an option today falls through to the "recorded only, no
// side effect" default -- the same shape as allocation.rule and budget.set
// -- and neither the roster nor -due changes at all. This file's own
// assertions are what turn that silence red.

import (
	"database/sql"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// plantHaltOption writes a task owned by owner, a posted deliverable by
// writer, and one open data.halt option naming targetDesk in its Needs
// field (the generic option shape has no dedicated "desk" column; Needs is
// the one already meant to carry what a person would have to do -- here,
// which desk) and reason in its Summary ("naming the desk and the reason",
// roles.yaml's own owes line for this role).
func plantHaltOption(t *testing.T, db *sql.DB, writer, owner, targetDesk, reason string) crew.Option {
	t.Helper()
	tres, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated, owner)
		VALUES ('Data quality sweep', 'trace every figure to a charge', ?, 'management',
		        'active', 0, 0, datetime('now'), datetime('now'), ?)`, writer, owner)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := tres.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	ares, err := db.Exec(`INSERT INTO artifacts (task, author, title, body, state, created)
		VALUES (?, ?, 'Data quality report', 'body', 'posted', datetime('now'))`, taskID, writer)
	if err != nil {
		t.Fatal(err)
	}
	artID, err := ares.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state)
		VALUES (?, 1, 'data.halt', ?, 0, 0, '', ?, '[]', 'open')`,
		artID, reason, targetDesk); err != nil {
		t.Fatal(err)
	}
	return crew.Option{Artifact: int(artID), Ordinal: 1, Class: "data.halt",
		Summary: reason, Needs: targetDesk, State: crew.OptionOpen}
}

func hireOnto(t *testing.T, db *sql.DB, name, desk, owner string) {
	t.Helper()
	if err := crew.Hire(db, crew.Analyst{
		Name: name, Role: "test analyst", Desk: desk, Engine: "openrouter",
		Skills: []string{"anomaly-triage"}, Rights: []string{"figures-read"},
		PerTask: money.Cents(1000), Monthly: money.Cents(10000), Cadence: "daily",
		Audience: "the desk", Owner: owner, Parent: "supervisor", Attestation: "none",
	}); err != nil {
		t.Fatal(err)
	}
}

// Red first: applying a data.halt option suspends the desk's analysts with
// the reason, and -due (crew.CadenceDue, the function it shares) skips the
// desk afterwards.
func TestApplyDataHaltSuspendsTheDesksAnalystsAndDueSkipsIt(t *testing.T) {
	db := applyTestDB(t)
	hireOnto(t, db, "triage-onprem", "onprem", "y.mercer")
	hireOnto(t, db, "investigator-onprem", "onprem", "y.mercer")

	reason := "the onprem tagging feed has been stale for 6 days"
	opt := plantHaltOption(t, db, "data-quality", "y.mercer", "onprem", reason)

	if err := finops.Apply(db, opt, "supervisor", nil); err != nil {
		t.Fatalf("Apply(data.halt): %v", err)
	}

	for _, name := range []string{"triage-onprem", "investigator-onprem"} {
		a, err := crew.GetAnalyst(db, name)
		if err != nil {
			t.Fatal(err)
		}
		if a.State != "suspended" {
			t.Errorf("%s.State = %q after data.halt applied, want suspended", name, a.State)
		}
		if a.Reason != reason {
			t.Errorf("%s.Reason = %q, want %q", name, a.Reason, reason)
		}
	}

	h, found, err := crew.ActiveHalt(db, "onprem")
	if err != nil || !found {
		t.Fatalf("ActiveHalt(onprem) after Apply: found=%v err=%v", found, err)
	}
	if h.Owner != "y.mercer" {
		t.Errorf("halt owner = %q, want y.mercer (the task's own owner, the same lookup "+
			"an ordinary carried option already uses)", h.Owner)
	}

	got := mustGetOption(t, db, opt.Artifact, opt.Ordinal)
	if got.State != crew.OptionApplied {
		t.Errorf("option state = %q, want applied", got.State)
	}

	// -due skips it: crew.CadenceDue is the exact function tools/run's -due
	// mode calls (dueItemsAndEstimates), so proving it here proves -due too.
	hireOnto(t, db, "late-hire-onprem", "onprem", "y.mercer") // active, after the halt
	roster, err := crew.Roster(db)
	if err != nil {
		t.Fatal(err)
	}
	items, err := crew.CadenceDue(db, roster, "2026-09-05", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Assignee == "late-hire-onprem" {
			t.Errorf("-due proposed cadence work for late-hire-onprem on the halted onprem "+
				"desk: %+v", it)
		}
	}
}

// Hostile: an option naming no desk in Needs is refused rather than
// suspending nothing and pretending it worked.
func TestApplyDataHaltRefusesWhenTheOptionNamesNoDesk(t *testing.T) {
	db := applyTestDB(t)
	opt := plantHaltOption(t, db, "data-quality", "y.mercer", "", "some reason")
	if err := finops.Apply(db, opt, "supervisor", nil); err == nil {
		t.Fatal("Apply(data.halt) with an empty Needs (no desk) was accepted")
	}
}

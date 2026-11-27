package crew_test

// The roster's refusals, each with the reason a person is shown: hiring,
// changing an analyst's state, re-briefing it and removing it. A refusal
// writes nothing, which each test reads back.

import (
	"errors"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

func validHire(name string) crew.Analyst {
	return crew.Analyst{
		Name: name, Role: "test analyst", Desk: "aws", Engine: "mock",
		Skills: []string{"anomaly-triage"}, Rights: []string{"figures-read"},
		PerTask: 1_00, Monthly: 10_00, Cadence: "on-request",
		Audience: "the desk", Owner: "installer", Parent: "supervisor", Attestation: "none",
	}
}

func TestHireRefusals(t *testing.T) {
	db, _ := withRoster(t)
	cases := []struct {
		name string
		edit func(*crew.Analyst)
		want string
	}{
		{"no role", func(a *crew.Analyst) { a.Role = "  " }, "no role"},
		{"per-task above monthly", func(a *crew.Analyst) { a.PerTask = 20_00 }, "is above the monthly one"},
		{"an unknown cadence", func(a *crew.Analyst) { a.Cadence = "hourly" }, "cadence must be one of"},
		{"already on the crew", func(a *crew.Analyst) { a.Name = "triage-aws" }, `"triage-aws" is already on the crew`},
		{"its own parent", func(a *crew.Analyst) { a.Parent = a.Name }, "cannot act on its own behalf"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := validHire("new-hire")
			c.edit(&a)
			err := crew.Hire(db, a)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Hire: %v, want %q", err, c.want)
			}
			if a.Name == "new-hire" {
				if _, err := crew.GetAnalyst(db, "new-hire"); err == nil {
					t.Error("a refused hire is on the roster")
				}
			}
		})
	}
	if err := crew.Hire(db, validHire("new-hire")); err != nil {
		t.Fatalf("the valid hire every case above deviates from: %v", err)
	}
}

func TestSetStateRefusals(t *testing.T) {
	db, _ := withRoster(t)
	if err := crew.SetState(db, "triage-aws", "retired", "gone"); err == nil ||
		!strings.Contains(err.Error(), `no such state: "retired"`) {
		t.Errorf("unknown state: %v", err)
	}
	if err := crew.SetState(db, "nobody-here", "suspended", "a reason"); err == nil ||
		!strings.Contains(err.Error(), "no such analyst") {
		t.Errorf("unknown analyst: %v", err)
	}
	if err := crew.SetState(db, "triage-aws", "probation", "missed two deadlines"); err != nil {
		t.Fatal(err)
	}
	// Back to active clears the reason.
	if err := crew.SetState(db, "triage-aws", "active", "ignored"); err != nil {
		t.Fatal(err)
	}
	a, err := crew.GetAnalyst(db, "triage-aws")
	if err != nil {
		t.Fatal(err)
	}
	if a.State != "active" || a.Reason != "" {
		t.Errorf("after reactivation: state %q reason %q, want active and no reason", a.State, a.Reason)
	}
}

func TestRebriefRefusals(t *testing.T) {
	db, _ := withRoster(t)
	a, err := crew.GetAnalyst(db, "triage-aws")
	if err != nil {
		t.Fatal(err)
	}
	ghost := a
	ghost.Name = "nobody-here"
	if err := crew.Rebrief(db, ghost); err == nil || !strings.Contains(err.Error(), "no such analyst") {
		t.Errorf("unknown analyst: %v", err)
	}
	bad := a
	bad.PerTask, bad.Monthly = 5_00, 1_00
	if err := crew.Rebrief(db, bad); err == nil || !strings.Contains(err.Error(), "must not exceed") {
		t.Errorf("guards inverted: %v", err)
	}
	bad = a
	bad.Cadence = "hourly"
	if err := crew.Rebrief(db, bad); err == nil || !strings.Contains(err.Error(), "cadence must be one of") {
		t.Errorf("unknown cadence: %v", err)
	}
	after, err := crew.GetAnalyst(db, "triage-aws")
	if err != nil {
		t.Fatal(err)
	}
	if after.PerTask != a.PerTask || after.Cadence != a.Cadence {
		t.Error("a refused re-brief changed the analyst")
	}
}

func TestRemoveRefusals(t *testing.T) {
	db, _ := withRoster(t)
	if err := crew.Remove(db, "nobody-here", "a-person"); err == nil || !strings.Contains(err.Error(), "no such analyst") {
		t.Errorf("unknown analyst: %v", err)
	}
	if err := crew.Remove(db, "supervisor", "a-person"); err == nil || !strings.Contains(err.Error(), "orphan the whole crew") {
		t.Errorf("the supervisor: %v", err)
	}
	// partner-aws has seven analysts acting under it.
	err := crew.Remove(db, "partner-aws", "a-person")
	if err == nil || !strings.Contains(err.Error(), "analysts act under this one") {
		t.Errorf("a parent: %v", err)
	}
	if _, err := crew.GetAnalyst(db, "partner-aws"); err != nil {
		t.Error("a refused removal took partner-aws off the roster")
	}

	// One open task: refused, and the count is singular.
	if err := crew.Hire(db, validHire("short-lived")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tasks (title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated)
		VALUES ('t', 'g', 'short-lived', 'aws', 'blocked', 0, 0, datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	err = crew.Remove(db, "short-lived", "a-person")
	if !errors.Is(err, crew.ErrHasWork) || !strings.Contains(err.Error(), ": 1 open task.") {
		t.Errorf("open work: %v", err)
	}
	if _, err := db.Exec(`UPDATE tasks SET state='posted' WHERE assignee='short-lived'`); err != nil {
		t.Fatal(err)
	}
	if err := crew.Remove(db, "short-lived", "a-person"); err != nil {
		t.Fatal(err)
	}
	if _, err := crew.GetAnalyst(db, "short-lived"); err == nil {
		t.Error("a removed analyst is still on the roster")
	}
	var kept int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE assignee='short-lived'`).Scan(&kept); err != nil || kept != 1 {
		t.Errorf("the removed analyst's finished work: %d rows, %v; want it kept", kept, err)
	}
}

package deliver_test

// B4-STEP-TWO-SPEC.md section 2: PlanPacket, PlanPrompt and PlanWorstCase do
// not exist yet, so this file does not compile against main -- the same
// shape estimate_test.go's own header already documents for a feature built
// from nothing.
//
// Grepped internal/crew/roles.yaml's supervisor family before writing these
// (per the spec's own instruction): its `reads` bullet lists the goal, the
// five sources' own items, the roster with skills/state/headroom, which is
// what PlanPacket assembles below -- crew.Propose has already gathered the
// first two of those into one crew.Plan, so this reads no anomaly, task or
// decision-request table itself.

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

func supervisorAnalyst() crew.Analyst {
	return crew.Analyst{
		Name: "supervisor", Role: "Crew supervisor", Desk: "management",
		State: "active", Engine: "anthropic",
		Skills:  []string{"sprint-planning", "routing", "escalation"},
		PerTask: money.Cents(500), Monthly: money.Cents(20000),
	}
}

func onePlanItem() crew.Plan {
	return crew.Plan{
		Label: "2026-W37", Start: "2026-09-08", End: "2026-09-14",
		Goal: "Close what is open, and explain what is not.",
		Items: []crew.PlanItem{
			{Title: "Explain the EC2 move on 2026-08-30", Assignee: "investigator-aws",
				Desk: "aws", Budget: money.Cents(15_00), Why: "anomaly A1 is open and unowned",
				Skill: "anomaly-triage"},
		},
	}
}

func tinyRoster() []crew.Analyst {
	return []crew.Analyst{
		supervisorAnalyst(),
		{Name: "investigator-aws", Desk: "aws", State: "active", Engine: "openrouter",
			Skills: []string{"anomaly-triage"}, PerTask: money.Cents(15_00), Monthly: money.Cents(500_00)},
		{Name: "retired-analyst", Desk: "aws", State: "suspended", Engine: "openrouter",
			Skills: []string{"anomaly-triage"}, PerTask: money.Cents(15_00), Monthly: money.Cents(500_00)},
	}
}

func TestPlanPacketCarriesTheGoalVerbatim(t *testing.T) {
	p := onePlanItem()
	got := deliver.PlanPacket(nil, p, tinyRoster(), map[string]money.Cents{})
	if !strings.Contains(got, p.Goal) {
		t.Errorf("packet does not contain the goal verbatim %q:\n%s", p.Goal, got)
	}
}

func TestPlanPacketNumbersItemsFromOne(t *testing.T) {
	p := onePlanItem()
	p.Items = append(p.Items, crew.PlanItem{Title: "second item", Assignee: "supervisor",
		Desk: "management", Budget: 0, Why: "decision waiting"})
	got := deliver.PlanPacket(nil, p, tinyRoster(), map[string]money.Cents{})
	if !strings.Contains(got, "#1 Explain the EC2 move on 2026-08-30") {
		t.Errorf("packet does not number item 1:\n%s", got)
	}
	if !strings.Contains(got, "#2 second item") {
		t.Errorf("packet does not number item 2:\n%s", got)
	}
	for _, want := range []string{"why:", "anomaly A1 is open and unowned",
		"investigator-aws", "aws", "15.00"} {
		if !strings.Contains(got, want) {
			t.Errorf("packet is missing %q for item 1:\n%s", want, got)
		}
	}
}

// The roster line carries name, desk, skills, engine and headroom; a
// suspended analyst never appears.
func TestPlanPacketRosterLineListsOnlyActiveAnalysts(t *testing.T) {
	spent := map[string]money.Cents{"investigator-aws": money.Cents(100_00)}
	got := deliver.PlanPacket(nil, onePlanItem(), tinyRoster(), spent)
	if !strings.Contains(got, "investigator-aws") {
		t.Fatalf("packet is missing the active analyst:\n%s", got)
	}
	if strings.Contains(got, "retired-analyst") {
		t.Errorf("packet names a suspended analyst:\n%s", got)
	}
	// headroom this month: 500.00 monthly minus 100.00 spent = 400.00
	if !strings.Contains(got, "400.00") {
		t.Errorf("packet does not carry investigator-aws's headroom (400.00):\n%s", got)
	}
	if !strings.Contains(got, "anomaly-triage") || !strings.Contains(got, "openrouter") {
		t.Errorf("packet is missing the roster's own skills or engine:\n%s", got)
	}
}

// The supervisor's own job description block is present.
func TestPlanPacketCarriesTheSupervisorsJobDescription(t *testing.T) {
	got := deliver.PlanPacket(nil, onePlanItem(), tinyRoster(), map[string]money.Cents{})
	if !strings.Contains(got, "Your job description") {
		t.Errorf("packet is missing the job description block:\n%s", got)
	}
	if !strings.Contains(got, "sprint.plan") {
		t.Errorf("packet's job description does not name a class the supervisor decides alone:\n%s", got)
	}
}

// Section 2's own words: "bounded to packetMaxBytes like the task packet,
// items first, roster second, the job description never trimmed". A plan
// with many items and a roster large enough to force the packet over its
// cap must still carry the job description block whole.
func TestPlanPacketNeverTrimsTheJobDescriptionEvenWhenOverflowing(t *testing.T) {
	p := crew.Plan{Label: "2026-W37", Start: "2026-09-08", End: "2026-09-14",
		Goal: "Close what is open, and explain what is not."}
	for i := 0; i < 400; i++ {
		p.Items = append(p.Items, crew.PlanItem{
			Title: strings.Repeat("x", 80), Assignee: "investigator-aws", Desk: "aws",
			Budget: money.Cents(15_00), Why: strings.Repeat("w", 200), Skill: "anomaly-triage",
		})
	}
	var roster []crew.Analyst
	roster = append(roster, supervisorAnalyst())
	for i := 0; i < 60; i++ {
		roster = append(roster, crew.Analyst{
			Name: strings.Repeat("n", 30), Desk: "aws", State: "active", Engine: "openrouter",
			Skills:  []string{"anomaly-triage", "driver-classification", "rightsizing-analysis"},
			PerTask: money.Cents(15_00), Monthly: money.Cents(500_00),
		})
	}

	got := deliver.PlanPacket(nil, p, roster, map[string]money.Cents{})
	// packetMaxBytes itself is unexported; 12*1024 is the literal this test
	// mirrors packet.go's own constant with. Found on the seeded estate (70
	// items, 39 active analysts) before this assertion existed: the packet
	// came back 12,332 bytes, 44 over -- BoundBytes(roster, 0) returns its
	// own truncation note WHOLE rather than nothing when the budget handed to
	// it has already shrunk to zero, so "contains the job description" and
	// "ends with it" both stayed true while the packet quietly grew past its
	// own stated cap. This is the assertion that actually holds the bound;
	// the two below only hold WHERE it was cut from.
	const packetMaxBytes = 12 * 1024
	if len(got) > packetMaxBytes {
		t.Errorf("packet is %d bytes, over the %d byte cap it is supposed to hold to", len(got), packetMaxBytes)
	}
	want := deliver.JobDescriptionBlock("supervisor", "management")
	if !strings.Contains(got, want) {
		t.Errorf("an overflowing packet dropped (part of) the job description block")
	}
	if !strings.HasSuffix(got, want) {
		t.Errorf("the job description block is not the packet's own tail; something was appended after it")
	}
}

func TestPlanPromptAsksForAFencedPlanBlock(t *testing.T) {
	got := deliver.PlanPrompt(supervisorAnalyst(), "PACKET-GOES-HERE")
	if !strings.Contains(got, "PACKET-GOES-HERE") {
		t.Error("prompt does not carry the packet text")
	}
	if !strings.Contains(got, "```plan") {
		t.Error("prompt does not ask for a fenced plan block")
	}
	if !strings.Contains(got, "ref") || !strings.Contains(got, "budget_cents") || !strings.Contains(got, "why") {
		t.Error("prompt does not describe the plan block's own fields")
	}
}

// PlanWorstCase prices the REAL prompt sent, not a stand-in: a supervisor
// with a large packet must price higher than one with a small one, at the
// same engine and model.
func TestPlanWorstCaseGrowsWithTheRealPacket(t *testing.T) {
	sup := supervisorAnalyst()
	small := deliver.PlanPrompt(sup, "short packet")
	big := deliver.PlanPrompt(sup, strings.Repeat("a longer packet with more items ", 200))

	smallWorst, model, priced := deliver.PlanWorstCase(sup, small, 2000)
	if !priced {
		t.Fatal("expected the supervisor's engine to be priced")
	}
	if model == "" {
		t.Error("expected a model name")
	}
	bigWorst, _, priced2 := deliver.PlanWorstCase(sup, big, 2000)
	if !priced2 {
		t.Fatal("expected the supervisor's engine to be priced")
	}
	if bigWorst <= smallWorst {
		t.Errorf("worst case did not grow with the packet: small=%d big=%d", smallWorst, bigWorst)
	}
}

func TestPlanWorstCaseOnAnUnknownEngineIsNotPriced(t *testing.T) {
	sup := supervisorAnalyst()
	sup.Engine = "a-name-from-nowhere"
	_, _, priced := deliver.PlanWorstCase(sup, "packet", 2000)
	if priced {
		t.Error("an unknown engine was priced; it must come back unpriced")
	}
}

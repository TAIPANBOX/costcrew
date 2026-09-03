package crew_test

// B4-STEP-TWO-SPEC.md section 6, in the order the testing rule names them:
// red first, boundaries, hostile input, and the four named mutants
// (gates-have-teeth.sh). crew.ValidatePlanAnswer does not exist yet, so this
// file does not compile against main -- the same shape tools/run/due_test.go's
// own header already documents for a feature built from nothing.
//
// Every test builds its own crew.Plan literal rather than going through
// crew.Propose: ValidatePlanAnswer takes the deterministic plan as a plain
// value, so a unit test can hand it exactly the shape under test without
// seeding anomalies, tasks or a sprint to make Propose produce it.

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// planWithOneRoutedItem is a deterministic plan with one item routed by
// skill (item #1, anomaly-triage on the aws desk), so a model answer can
// legitimately re-route it to another active holder of the same skill.
func planWithOneRoutedItem(assignee string, budget money.Cents) crew.Plan {
	return crew.Plan{
		Label: "2026-W99", Start: "2026-09-08", End: "2026-09-14",
		Goal: "Close what is open, and explain what is not.",
		Items: []crew.PlanItem{
			{Title: "Explain the EC2 move on 2026-08-30", Goal: "vs baseline",
				Assignee: assignee, Desk: "aws", Budget: budget,
				Why: "anomaly A1 is open and unowned", Skill: "anomaly-triage"},
		},
	}
}

// ------------------------------------------------------------------ helpers

func analyst(name, desk, engine string, skills []string, perTask, monthly money.Cents) crew.Analyst {
	return crew.Analyst{
		Name: name, Role: "test analyst", Desk: desk, Engine: engine, State: "active",
		Skills: skills, Rights: []string{"figures-read"},
		PerTask: perTask, Monthly: monthly, Cadence: "on-request", Audience: "the desk",
	}
}

// ------------------------------------------------------------------ red first

// A fake model answer that re-routes item #1 to an active holder of its own
// skill is accepted.
func TestARerouteToAnActiveHolderOfTheSameSkillIsAccepted(t *testing.T) {
	det := planWithOneRoutedItem("investigator-aws", money.Cents(15_00))
	roster := []crew.Analyst{
		analyst("investigator-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(15_00), money.Cents(500_00)),
		analyst("triage-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(12_00), money.Cents(500_00)),
	}
	spent := map[string]money.Cents{}
	body := "```plan\n" +
		`{"items": [{"ref": 1, "assignee": "triage-aws", "budget_cents": 1000, "why": "triage-aws has more headroom this week"}]}` +
		"\n```"

	items, found, reason := crew.ValidatePlanAnswer(body, det, roster, spent)
	if !found {
		t.Fatal("expected a plan block to be found")
	}
	if reason != "" {
		t.Fatalf("expected acceptance, got refusal: %s", reason)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if items[0].Ref != 1 || items[0].Assignee != "triage-aws" || items[0].Budget != money.Cents(10_00) {
		t.Errorf("item = %+v, want ref=1 assignee=triage-aws budget=10.00", items[0])
	}
	if items[0].Why != "triage-aws has more headroom this week" {
		t.Errorf("why = %q", items[0].Why)
	}
}

// An answer that invents an item without a ref is refused whole, with the
// reason, and no items at all -- section 3's own rule, "never partially
// applied".
func TestAnItemWithNoRefIsRefusedWhole(t *testing.T) {
	det := planWithOneRoutedItem("investigator-aws", money.Cents(15_00))
	roster := []crew.Analyst{analyst("investigator-aws", "aws", "openrouter",
		[]string{"anomaly-triage"}, money.Cents(15_00), money.Cents(500_00))}
	body := "```plan\n" +
		`{"items": [{"assignee": "investigator-aws", "budget_cents": 500, "why": "invented work"}]}` +
		"\n```"

	items, found, reason := crew.ValidatePlanAnswer(body, det, roster, map[string]money.Cents{})
	if !found {
		t.Fatal("expected a plan block to be found")
	}
	if reason == "" {
		t.Fatal("expected a refusal for an item with no ref")
	}
	if items != nil {
		t.Errorf("items = %+v, want nil: a refused answer is never partially applied", items)
	}
}

// An answer routing to a suspended analyst is refused.
func TestARouteToASuspendedAnalystIsRefused(t *testing.T) {
	det := planWithOneRoutedItem("investigator-aws", money.Cents(15_00))
	suspended := analyst("triage-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(12_00), money.Cents(500_00))
	suspended.State = "suspended"
	roster := []crew.Analyst{
		analyst("investigator-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(15_00), money.Cents(500_00)),
		suspended,
	}
	body := "```plan\n" +
		`{"items": [{"ref": 1, "assignee": "triage-aws", "budget_cents": 1000, "why": "reroute"}]}` +
		"\n```"

	_, found, reason := crew.ValidatePlanAnswer(body, det, roster, map[string]money.Cents{})
	if !found {
		t.Fatal("expected a plan block to be found")
	}
	if reason == "" {
		t.Fatal("expected a refusal for routing to a suspended analyst")
	}
	if !strings.Contains(reason, "triage-aws") {
		t.Errorf("reason %q does not name the suspended analyst", reason)
	}
}

// An answer raising a budget above the deterministic item's own is refused:
// "budget_cents may only go down".
func TestABudgetRaisedAboveTheDeterministicItemIsRefused(t *testing.T) {
	det := planWithOneRoutedItem("investigator-aws", money.Cents(15_00))
	roster := []crew.Analyst{analyst("investigator-aws", "aws", "openrouter",
		[]string{"anomaly-triage"}, money.Cents(50_00), money.Cents(500_00))}
	body := "```plan\n" +
		`{"items": [{"ref": 1, "assignee": "investigator-aws", "budget_cents": 2000, "why": "more budget please"}]}` +
		"\n```"

	_, found, reason := crew.ValidatePlanAnswer(body, det, roster, map[string]money.Cents{})
	if !found {
		t.Fatal("expected a plan block to be found")
	}
	if reason == "" {
		t.Fatal("expected a refusal for a budget raised above the deterministic item's own")
	}
	if !strings.Contains(reason, "down") && !strings.Contains(reason, "1500") && !strings.Contains(reason, "15.00") {
		t.Errorf("reason %q does not explain the budget-only-down rule", reason)
	}
}

// A route to an analyst with no headroom left this month is refused.
func TestARouteToAnAnalystWithNoHeadroomIsRefused(t *testing.T) {
	det := planWithOneRoutedItem("investigator-aws", money.Cents(15_00))
	roster := []crew.Analyst{
		analyst("investigator-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(15_00), money.Cents(500_00)),
		analyst("triage-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(12_00), money.Cents(100_00)),
	}
	spent := map[string]money.Cents{"triage-aws": money.Cents(100_00)} // exactly spent through its guard
	body := "```plan\n" +
		`{"items": [{"ref": 1, "assignee": "triage-aws", "budget_cents": 1000, "why": "reroute"}]}` +
		"\n```"

	_, found, reason := crew.ValidatePlanAnswer(body, det, roster, spent)
	if !found {
		t.Fatal("expected a plan block to be found")
	}
	if reason == "" {
		t.Fatal("expected a refusal for no headroom left")
	}
	if !strings.Contains(reason, "headroom") {
		t.Errorf("reason %q does not name headroom", reason)
	}
}

// A route to an assignee not holding the item's own skill is refused, even
// though the assignee is active and on the right desk.
func TestARouteToAnAnalystWithoutTheItemsSkillIsRefused(t *testing.T) {
	det := planWithOneRoutedItem("investigator-aws", money.Cents(15_00))
	roster := []crew.Analyst{
		analyst("investigator-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(15_00), money.Cents(500_00)),
		analyst("optimizer-aws", "aws", "openrouter", []string{"rightsizing-analysis"}, money.Cents(18_00), money.Cents(500_00)),
	}
	body := "```plan\n" +
		`{"items": [{"ref": 1, "assignee": "optimizer-aws", "budget_cents": 1000, "why": "reroute"}]}` +
		"\n```"

	_, found, reason := crew.ValidatePlanAnswer(body, det, roster, map[string]money.Cents{})
	if !found {
		t.Fatal("expected a plan block to be found")
	}
	if reason == "" {
		t.Fatal("expected a refusal for lacking the item's own skill")
	}
}

// A route to an assignee not on the item's own desk is refused.
func TestARouteOffTheItemsDeskIsRefused(t *testing.T) {
	det := planWithOneRoutedItem("investigator-aws", money.Cents(15_00))
	roster := []crew.Analyst{
		analyst("investigator-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(15_00), money.Cents(500_00)),
		analyst("investigator-gcp", "gcp", "openrouter", []string{"anomaly-triage"}, money.Cents(15_00), money.Cents(500_00)),
	}
	body := "```plan\n" +
		`{"items": [{"ref": 1, "assignee": "investigator-gcp", "budget_cents": 1000, "why": "reroute"}]}` +
		"\n```"

	_, found, reason := crew.ValidatePlanAnswer(body, det, roster, map[string]money.Cents{})
	if !found {
		t.Fatal("expected a plan block to be found")
	}
	if reason == "" {
		t.Fatal("expected a refusal for a route off the item's own desk")
	}
}

// An item whose deterministic slot carries no Skill (blocked rework,
// cadence-due, returned work, a decision request) cannot be re-routed to a
// different assignee at all: there is no pool this function can check an
// alternative against.
func TestAnUnroutableItemCannotBeReassigned(t *testing.T) {
	det := crew.Plan{Items: []crew.PlanItem{
		{Title: "Unblock: something", Assignee: "investigator-aws", Desk: "aws",
			Budget: money.Cents(10_00), Why: "task 7 has been blocked since 2026-08-20"},
	}}
	roster := []crew.Analyst{
		analyst("investigator-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(15_00), money.Cents(500_00)),
		analyst("triage-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(12_00), money.Cents(500_00)),
	}
	body := "```plan\n" +
		`{"items": [{"ref": 1, "assignee": "triage-aws", "budget_cents": 500, "why": "reroute"}]}` +
		"\n```"

	_, found, reason := crew.ValidatePlanAnswer(body, det, roster, map[string]money.Cents{})
	if !found {
		t.Fatal("expected a plan block to be found")
	}
	if reason == "" {
		t.Fatal("expected a refusal: this item has no skill to re-route by")
	}
}

// Keeping the same assignee on an unroutable item, only changing its why and
// lowering its budget, is accepted.
func TestAnUnroutableItemKeepingTheSameAssigneeIsAccepted(t *testing.T) {
	det := crew.Plan{Items: []crew.PlanItem{
		{Title: "Unblock: something", Assignee: "investigator-aws", Desk: "aws",
			Budget: money.Cents(10_00), Why: "task 7 has been blocked since 2026-08-20"},
	}}
	roster := []crew.Analyst{
		analyst("investigator-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(15_00), money.Cents(500_00)),
	}
	body := "```plan\n" +
		`{"items": [{"ref": 1, "assignee": "investigator-aws", "budget_cents": 500, "why": "still the right analyst, less to spend"}]}` +
		"\n```"

	items, found, reason := crew.ValidatePlanAnswer(body, det, roster, map[string]money.Cents{})
	if !found {
		t.Fatal("expected a plan block to be found")
	}
	if reason != "" {
		t.Fatalf("expected acceptance, got refusal: %s", reason)
	}
	if len(items) != 1 || items[0].Assignee != "investigator-aws" {
		t.Errorf("items = %+v", items)
	}
}

// -------------------------------------------------------------- boundaries

// Zero items is a legal answer: "nothing this sprint".
func TestZeroItemsIsALegalAnswer(t *testing.T) {
	det := planWithOneRoutedItem("investigator-aws", money.Cents(15_00))
	roster := []crew.Analyst{analyst("investigator-aws", "aws", "openrouter",
		[]string{"anomaly-triage"}, money.Cents(15_00), money.Cents(500_00))}
	body := "```plan\n{\"items\": []}\n```"

	items, found, reason := crew.ValidatePlanAnswer(body, det, roster, map[string]money.Cents{})
	if !found {
		t.Fatal("expected a plan block to be found")
	}
	if reason != "" {
		t.Fatalf("zero items must be legal, got refusal: %s", reason)
	}
	if len(items) != 0 {
		t.Errorf("items = %+v, want none", items)
	}
}

// Every deterministic item can be dropped: the model's answer names none of
// the refs the deterministic plan carries, and that is accepted the same as
// zero items -- it is the same shape from the deterministic plan's own side.
func TestEveryDeterministicItemCanBeDropped(t *testing.T) {
	det := crew.Plan{Items: []crew.PlanItem{
		{Title: "one", Assignee: "investigator-aws", Desk: "aws", Budget: money.Cents(15_00), Skill: "anomaly-triage"},
		{Title: "two", Assignee: "investigator-gcp", Desk: "gcp", Budget: money.Cents(15_00), Skill: "anomaly-triage"},
	}}
	roster := []crew.Analyst{
		analyst("investigator-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(15_00), money.Cents(500_00)),
		analyst("investigator-gcp", "gcp", "openrouter", []string{"anomaly-triage"}, money.Cents(15_00), money.Cents(500_00)),
	}
	body := "```plan\n{\"items\": []}\n```"

	items, found, reason := crew.ValidatePlanAnswer(body, det, roster, map[string]money.Cents{})
	if !found || reason != "" || len(items) != 0 {
		t.Fatalf("items=%+v found=%v reason=%q, want found=true reason=\"\" items=none", items, found, reason)
	}
}

// The same ref named twice is refused.
func TestTheSameRefTwiceIsRefused(t *testing.T) {
	det := crew.Plan{Items: []crew.PlanItem{
		{Title: "one", Assignee: "investigator-aws", Desk: "aws", Budget: money.Cents(15_00), Skill: "anomaly-triage"},
		{Title: "two", Assignee: "investigator-gcp", Desk: "gcp", Budget: money.Cents(15_00), Skill: "anomaly-triage"},
	}}
	roster := []crew.Analyst{
		analyst("investigator-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(15_00), money.Cents(500_00)),
		analyst("investigator-gcp", "gcp", "openrouter", []string{"anomaly-triage"}, money.Cents(15_00), money.Cents(500_00)),
	}
	body := "```plan\n" + `{"items": [` +
		`{"ref": 1, "assignee": "investigator-aws", "budget_cents": 100, "why": "a"},` +
		`{"ref": 1, "assignee": "investigator-aws", "budget_cents": 200, "why": "b"}` +
		`]}` + "\n```"

	_, found, reason := crew.ValidatePlanAnswer(body, det, roster, map[string]money.Cents{})
	if !found {
		t.Fatal("expected a plan block to be found")
	}
	if reason == "" {
		t.Fatal("expected a refusal for the same ref twice")
	}
}

// A why of exactly 240 bytes is accepted; 241 is refused.
func TestAWhyOfExactly240BytesIsAcceptedAnd241IsRefused(t *testing.T) {
	det := planWithOneRoutedItem("investigator-aws", money.Cents(15_00))
	roster := []crew.Analyst{analyst("investigator-aws", "aws", "openrouter",
		[]string{"anomaly-triage"}, money.Cents(15_00), money.Cents(500_00))}

	why240 := strings.Repeat("a", 240)
	body := "```plan\n" +
		`{"items": [{"ref": 1, "assignee": "investigator-aws", "budget_cents": 1000, "why": "` + why240 + `"}]}` +
		"\n```"
	items, found, reason := crew.ValidatePlanAnswer(body, det, roster, map[string]money.Cents{})
	if !found || reason != "" {
		t.Fatalf("240-byte why: found=%v reason=%q, want accepted", found, reason)
	}
	if len(items) != 1 || len(items[0].Why) != 240 {
		t.Errorf("items = %+v", items)
	}

	why241 := strings.Repeat("a", 241)
	body2 := "```plan\n" +
		`{"items": [{"ref": 1, "assignee": "investigator-aws", "budget_cents": 1000, "why": "` + why241 + `"}]}` +
		"\n```"
	_, found2, reason2 := crew.ValidatePlanAnswer(body2, det, roster, map[string]money.Cents{})
	if !found2 || reason2 == "" {
		t.Fatalf("241-byte why: found=%v reason=%q, want refused", found2, reason2)
	}
}

// -------------------------------------------------------------- hostile input

func TestPlanAnswerHostileInputs(t *testing.T) {
	det := planWithOneRoutedItem("investigator-aws", money.Cents(15_00))
	roster := []crew.Analyst{analyst("investigator-aws", "aws", "openrouter",
		[]string{"anomaly-triage"}, money.Cents(15_00), money.Cents(500_00))}
	spent := map[string]money.Cents{}

	cases := []struct {
		name string
		body string
	}{
		{"not JSON", "```plan\nnot json at all\n```"},
		{"1 MB", "```plan\n" + strings.Repeat("x", 1<<20) + "\n```"},
		{"a string where ref's integer goes",
			"```plan\n" + `{"items": [{"ref": "one", "assignee": "investigator-aws", "budget_cents": 100, "why": "x"}]}` + "\n```"},
		{"a string where budget_cents' integer goes",
			"```plan\n" + `{"items": [{"ref": 1, "assignee": "investigator-aws", "budget_cents": "a lot", "why": "x"}]}` + "\n```"},
		{"more items than the deterministic plan has",
			"```plan\n" + `{"items": [` +
				`{"ref": 1, "assignee": "investigator-aws", "budget_cents": 100, "why": "a"},` +
				`{"ref": 1, "assignee": "investigator-aws", "budget_cents": 100, "why": "b"}]}` + "\n```"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			items, found, reason := crew.ValidatePlanAnswer(c.body, det, roster, spent)
			if !found {
				t.Fatal("expected a plan block to be found")
			}
			if reason == "" {
				t.Fatalf("expected a refusal for %s", c.name)
			}
			if items != nil {
				t.Errorf("items = %+v, want nil on refusal", items)
			}
		})
	}
}

// A script tag in why is data, not markup: this function does not render
// anything, but nothing here should strip or unescape it either -- the
// rendering layer (internal/web/templates/plan.html) is what must show it
// as text, and that is a separate test (internal/web).
func TestAScriptTagInWhySurvivesUnchanged(t *testing.T) {
	det := planWithOneRoutedItem("investigator-aws", money.Cents(15_00))
	roster := []crew.Analyst{analyst("investigator-aws", "aws", "openrouter",
		[]string{"anomaly-triage"}, money.Cents(15_00), money.Cents(500_00))}
	body := "```plan\n" +
		`{"items": [{"ref": 1, "assignee": "investigator-aws", "budget_cents": 100, "why": "<script>alert(1)</script>"}]}` +
		"\n```"
	items, found, reason := crew.ValidatePlanAnswer(body, det, roster, map[string]money.Cents{})
	if !found || reason != "" {
		t.Fatalf("found=%v reason=%q, want accepted", found, reason)
	}
	if items[0].Why != "<script>alert(1)</script>" {
		t.Errorf("why = %q, want the script tag verbatim", items[0].Why)
	}
}

// No fenced block at all: found is false, and that is not itself a refusal.
func TestNoPlanBlockAtAllIsNotFound(t *testing.T) {
	det := planWithOneRoutedItem("investigator-aws", money.Cents(15_00))
	items, found, reason := crew.ValidatePlanAnswer("just prose, no fenced block", det, nil, nil)
	if found {
		t.Error("found = true, want false")
	}
	if reason != "" {
		t.Errorf("reason = %q, want empty: no block found is not itself a refusal", reason)
	}
	if items != nil {
		t.Errorf("items = %+v, want nil", items)
	}
}

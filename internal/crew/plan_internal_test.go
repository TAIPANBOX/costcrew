package crew

// Direct, package-internal tests of B4-SPEC.md's pure helper functions:
// tighter and faster than driving them through Propose, and what lets
// firstClause and goalWords be checked against every edge named in section
// 6 without also standing up a store for each one.

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

func TestFirstClauseSplitsOnTheFirstSemicolon(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"a triage note per finding: real, expected; the hand-off to an investigator",
			"a triage note per finding: real, expected"},
		{"the evidence pack, rule by rule, followed or not, with the row that shows it.",
			"the evidence pack, rule by rule, followed or not, with the row that shows it"},
		{"", ""},
	} {
		if got := firstClause(tc.in); got != tc.want {
			t.Errorf("firstClause(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGoalWordsTrimsPunctuationAndKeepsHyphens(t *testing.T) {
	got := goalWords(`Commitment-modelling, please -- and "variance-commentary"!`)
	// Lowercased; a hyphen is never in the trim cutset, so a hyphenated
	// skill name survives as one token, but surrounding punctuation
	// (comma, quotes, "!") is stripped from each token's ends.
	want := []string{"commitment-modelling", "please", "--", "and", "variance-commentary"}
	if len(got) != len(want) {
		t.Fatalf("goalWords = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("goalWords[%d] = %q, want %q (full: %v)", i, got[i], w, got)
		}
	}
}

// Every purely-punctuation token collapses to nothing and is dropped,
// EXCEPT one made only of hyphens: "-" is deliberately not in the trim
// cutset (a hyphenated skill name has to survive whole), so "---" comes
// through as a token. It still matches no real skill downstream --
// TestAGoalOfOnlyPunctuationMatchesNothing (plan_test.go) is what proves
// that end of the property, through Propose.
func TestGoalWordsOnPunctuationOnlyLeavesOnlyTheHyphenRun(t *testing.T) {
	got := goalWords("!!! ... ??? ,,, ---")
	if len(got) != 1 || got[0] != "---" {
		t.Errorf("goalWords on punctuation only = %v, want [---]", got)
	}
}

func TestCandidatesWithSkillExcludesEveryInactiveState(t *testing.T) {
	roster := []Analyst{
		{Name: "a", State: "active", Desk: "aws", Skills: []string{"anomaly-triage"}},
		{Name: "b", State: "suspended", Desk: "aws", Skills: []string{"anomaly-triage"}},
		{Name: "c", State: "probation", Desk: "aws", Skills: []string{"anomaly-triage"}},
		{Name: "d", State: "onboarding", Desk: "aws", Skills: []string{"anomaly-triage"}},
		{Name: "e", State: "restricted", Desk: "aws", Skills: []string{"anomaly-triage"}},
		{Name: "f", State: "over-guard", Desk: "aws", Skills: []string{"anomaly-triage"}},
		{Name: "g", State: "active", Desk: "gcp", Skills: []string{"anomaly-triage"}},      // wrong desk
		{Name: "h", State: "active", Desk: "aws", Skills: []string{"variance-commentary"}}, // wrong skill
	}
	got := candidatesWithSkill(roster, "anomaly-triage", "aws")
	if len(got) != 1 || got[0].Name != "a" {
		t.Errorf("candidatesWithSkill = %v, want exactly [a]", got)
	}
}

func TestChooseAnalystTiebreaksByEngineThenName(t *testing.T) {
	spent := map[string]money.Cents{}
	// Same headroom (both untouched Monthly), different engines: the class
	// prefers "strong", so the strong-engine candidate must win regardless
	// of name order.
	candidates := []Analyst{
		{Name: "z-cheap", Engine: engineCheap, Monthly: money.Cents(1000)},
		{Name: "a-strong", Engine: engineStrong, Monthly: money.Cents(1000)},
	}
	chosen, ok, note := chooseAnalyst(candidates, "decision-framing", spent)
	if !ok || chosen.Name != "a-strong" {
		t.Errorf("chosen = %q (ok=%v), want a-strong (decision-framing prefers the strong engine)", chosen.Name, ok)
	}
	if note != "" {
		t.Errorf("note = %q, want empty (nobody had zero headroom)", note)
	}
}

func TestChooseAnalystFallsBackToNameWhenEngineDoesNotDiscriminate(t *testing.T) {
	spent := map[string]money.Cents{}
	candidates := []Analyst{
		{Name: "zed", Engine: engineCheap, Monthly: money.Cents(1000)},
		{Name: "abe", Engine: engineCheap, Monthly: money.Cents(1000)},
	}
	// "some-unrelated-skill" is not in engineByClass, so engine preference
	// never enters the comparison and the name is what breaks the tie.
	chosen, ok, _ := chooseAnalyst(candidates, "some-unrelated-skill", spent)
	if !ok || chosen.Name != "abe" {
		t.Errorf("chosen = %q, want abe (alphabetically first on a full tie)", chosen.Name)
	}
}

func TestChooseAnalystOnNoCandidatesNamesTheSkill(t *testing.T) {
	_, ok, note := chooseAnalyst(nil, "anomaly-triage", nil)
	if ok {
		t.Error("chooseAnalyst succeeded with no candidates")
	}
	if note != "nobody active holds anomaly-triage" {
		t.Errorf("note = %q, want it to name the skill", note)
	}
}

func TestHeadroomOfIsMonthlyMinusSpent(t *testing.T) {
	a := Analyst{Name: "x", Monthly: money.Cents(10000)}
	spent := map[string]money.Cents{"x": money.Cents(4000)}
	if got := headroomOf(a, spent); got != money.Cents(6000) {
		t.Errorf("headroomOf = %s, want 60.00", got)
	}
	// An analyst nobody has spent against yet still has a real headroom:
	// the zero value of a missing map entry is exactly right here, not a
	// case that needs its own branch.
	if got := headroomOf(a, map[string]money.Cents{}); got != money.Cents(10000) {
		t.Errorf("headroomOf with no spend recorded = %s, want the full 100.00", got)
	}
}

func TestDesksInGoalMatchesWorldDesksExactly(t *testing.T) {
	got := desksInGoal()
	for _, want := range []string{"aws", "gcp", "azure", "ai", "saas", "onprem"} {
		if !got[want] {
			t.Errorf("desksInGoal() is missing %q", want)
		}
	}
	if len(got) != 6 {
		t.Errorf("desksInGoal() has %d entries, want 6", len(got))
	}
}

func TestGoalDeskReturnsTheFirstDeskWordInGoalOrder(t *testing.T) {
	if got := goalDesk([]string{"a", "gcp", "aws", "b"}); got != "gcp" {
		t.Errorf("goalDesk = %q, want gcp (the first desk word encountered)", got)
	}
	if got := goalDesk([]string{"no", "desk", "here"}); got != "" {
		t.Errorf("goalDesk = %q, want empty", got)
	}
}

func TestTriageDeskUnchangedMapping(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"aws", "triage-aws"}, {"gcp", "triage-gcp"}, {"azure", "triage-azure"},
		{"ai", "triage-ai"}, {"saas", "saas-manager"}, {"onprem", "investigator-onprem"},
	} {
		if got := triageDesk(tc.source); got != tc.want {
			t.Errorf("triageDesk(%q) = %q, want %q", tc.source, got, tc.want)
		}
	}
}

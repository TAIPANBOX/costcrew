package main

// Tests for B1a's prompt-packet half: jobDescriptionBlock and prompt()'s one
// call into it. See internal/crew/roles_test.go for the enforcement half.
//
// Red first, against the code before this step: prompt() made no such call
// and jobDescriptionBlock did not exist, so this test failed to compile
// ("undefined: jobDescriptionBlock" once written standalone, or, written
// against prompt()'s output as below, simply found no "Your job description"
// block at all). B1A-SPEC.md section 4.

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// The prompt packet for an investigator carries its job description:
// anomaly.explain under "decide alone", and purchase under "never" -- the
// crew's own word (ROLES-2026-09.md section 1) for what "commit money"
// means, which is why it is annotated onto the never line rather than left
// only under "hands up". B1A-SPEC.md section 4.
func TestThePromptCarriesTheJobDescription(t *testing.T) {
	task := crew.Task{ID: 1, Title: "Explain the Amazon EC2 move on 2026-07-14",
		Goal: "184000 above baseline on the aws desk."}
	a := crew.Analyst{Name: "investigator-aws", Role: "Investigator (aws desk)", Desk: "aws",
		Engine: "openrouter", State: "active",
		Mission: "Explain, within a day, every movement in the aws desk's bill."}

	sent := prompt(task, a, "2026-08-24", "")

	if !strings.Contains(sent, "Your job description") {
		t.Fatalf("the prompt carries no job description block:\n%s", sent)
	}

	decideLine := lineContaining(t, sent, "You may decide alone:")
	if !strings.Contains(decideLine, "anomaly.explain") {
		t.Errorf("investigator-aws's \"decide alone\" line does not name anomaly.explain:\n%s", decideLine)
	}

	neverLine := lineContaining(t, sent, "You never:")
	if !strings.Contains(neverLine, "purchase") {
		t.Errorf("investigator-aws's \"never\" line does not name purchase:\n%s", neverLine)
	}

	// Reads, cadence and audience are the same fields the card shows,
	// carried in the same words: nothing here is a paraphrase of roles.yaml.
	r, ok := crew.RoleForDesk("investigator-aws", "aws")
	if !ok {
		t.Fatal("investigator-aws matches no role family; this test's fixture is stale")
	}
	if !strings.Contains(sent, r.Reads) {
		t.Errorf("the prompt does not carry the role's Reads text verbatim")
	}
	if !strings.Contains(sent, "Reports: "+r.Cadence+" to "+r.Audience) {
		t.Errorf("the prompt does not carry \"Reports: %s to %s\"", r.Cadence, r.Audience)
	}
}

// An analyst whose name matches no role family (a hire made by hand, before
// a family existed for it) gets no job-description block: additive, not
// misleading.
func TestThePromptOmitsTheBlockForAnUnmatchedAnalyst(t *testing.T) {
	task := crew.Task{ID: 1, Title: "Something", Goal: "Do it."}
	a := crew.Analyst{Name: "a-name-from-nowhere", Role: "Nobody's role", Desk: "aws",
		Engine: "openrouter", State: "active"}

	sent := prompt(task, a, "2026-08-24", "")
	if strings.Contains(sent, "Your job description") {
		t.Errorf("a name no role family matches still got a job description block:\n%s", sent)
	}
	if got := jobDescriptionBlock(a.Name, a.Desk); got != "" {
		t.Errorf("jobDescriptionBlock(%q, %q) = %q, want empty", a.Name, a.Desk, got)
	}
}

// lineContaining returns the single line of s that contains want, failing
// the test if there is none.
func lineContaining(t *testing.T, s, want string) string {
	t.Helper()
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, want) {
			return line
		}
	}
	t.Fatalf("no line of the prompt contains %q:\n%s", want, s)
	return ""
}

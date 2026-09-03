package deliver_test

// DRIVER-WINDOW-SPEC.md section 3: "the two driver classes' target shape, in
// one sentence, from the same roles data" the class list above it already
// reads from. Red against unchanged code: optionsBlockInstructions names no
// target at all, for any role, so the "present" case below failed to find
// the sentence.

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
)

func TestPromptNamesTheDriverTargetShapeForASupervisor(t *testing.T) {
	task := crew.Task{Title: "route the sprint", Goal: "apply what can be decided alone"}
	analyst := crew.Analyst{Name: "supervisor", Desk: "management"}
	got := deliver.Prompt(task, analyst, "2026-09-03", "")
	if !strings.Contains(got, `"target": {"start"`) {
		t.Errorf("prompt for supervisor (driver.recurring in its own decides_alone) does not "+
			"name the target shape:\n%s", got)
	}
}

func TestPromptNamesTheDriverTargetShapeForAnInvestigator(t *testing.T) {
	task := crew.Task{Title: "explain the series", Goal: "say what moved"}
	analyst := crew.Analyst{Name: "investigator-aws", Desk: "aws"}
	got := deliver.Prompt(task, analyst, "2026-09-03", "")
	if !strings.Contains(got, `"target": {"start"`) {
		t.Errorf("prompt for investigator-aws (driver.one-time in its own decides_alone) does "+
			"not name the target shape:\n%s", got)
	}
}

func TestPromptOmitsTheDriverTargetShapeForARoleWithNeitherDriverClass(t *testing.T) {
	task := crew.Task{Title: "write the showback narration", Goal: "explain the desk's month"}
	analyst := crew.Analyst{Name: "reporter-aws", Desk: "aws"}
	got := deliver.Prompt(task, analyst, "2026-09-03", "")
	if strings.Contains(got, "driver.recurring and driver.one-time") {
		t.Errorf("prompt for reporter-aws (neither driver class in its own vocabulary) names "+
			"the driver target shape anyway:\n%s", got)
	}
}

package main

import (
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"testing"
)

// TestAnthropicIsAskedForAnAnswerRatherThanReasoning moved to
// internal/deliver/call_test.go (B6B-SPEC.md): it tested anthropicBody
// directly, which moved with call() and no longer exists in this package.
// This file keeps TestTheModelIsToldTheDate, which tests prompt() -- the
// wrapper over internal/deliver.Prompt that did NOT move (B7) -- unaffected.

// The model is told the date.
//
// Red first: a live run produced "**Date:** [Today's Date]" on the face of a
// deliverable a person was meant to read. A model has no clock, so it either
// gets the date or it guesses, and this console's whole argument is that a
// figure nobody can check is worse than no figure.
func TestTheModelIsToldTheDate(t *testing.T) {
	p := prompt(crew.Task{ID: 1, Title: "a task", Goal: "a goal"},
		crew.Analyst{Name: "triage-aws", Role: "analyst", Desk: "aws"},
		"2026-08-24", "")
	if !strings.Contains(p, "Today is 2026-08-24.") {
		t.Errorf("the prompt does not carry the date, so the model fills the "+
			"gap itself:\n%s", p)
	}
	// And it is asked for the format the console can render.
	if !strings.Contains(p, "## headings") {
		t.Error("the prompt does not name the format, so the model picks its own " +
			"and the page shows the syntax back to the reader")
	}
}

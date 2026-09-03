package main

// The two mock engines. B7-SPEC.md section 4.
//
// Neither calls anything: both build a deterministic deliverable straight
// from the anomaly the bench already holds (the same figures the packet
// itself would show), so every test and every gate runs with no key and no
// money, and the scorer is exercised on both a wrong answer and a right one.

import (
	"encoding/json"
	"fmt"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
)

const (
	engineMock       = "mock"
	engineMockOracle = "mock-oracle"

	// mockWrongCause is FIXED, not per-case: B7-SPEC.md section 4 says "a
	// fixed wrong string", so the mock's cause score is 0% on every case
	// it is ever run against, not just the ones this fixture happens to hold.
	mockWrongCause = "a scheduled maintenance window, unrelated to this event"
)

// isMockEngine says whether name is one of the two engines this file
// answers for. Neither is ever selectable with -live (main.go's flag
// validation), and neither is a real engines.Catalogue entry.
func isMockEngine(name string) bool {
	return name == engineMock || name == engineMockOracle
}

// mockOptionsBlock is the fenced ```options block every mock deliverable
// ends in, built through encoding/json rather than a format string: a
// driver label can carry a character (a quote, a backslash) a hand-rolled
// %q would not always escape the way JSON needs.
func mockOptionsBlock(class, summary string) string {
	type rawOption struct {
		Class       string   `json:"class"`
		Summary     string   `json:"summary"`
		FigureCents int64    `json:"figure_cents"`
		SavingCents int64    `json:"saving_cents"`
		Risk        string   `json:"risk"`
		Needs       string   `json:"needs"`
		Evidence    []string `json:"evidence"`
	}
	block := struct {
		Options []rawOption `json:"options"`
	}{[]rawOption{{
		Class: class, Summary: summary, Risk: "low", Needs: "nothing",
		Evidence: []string{"mock"},
	}}}
	raw, _ := json.Marshal(block)
	return "```options\n" + string(raw) + "\n```\n"
}

// oppositeKind is what a deliberately-wrong mock deliverable claims: the
// kind the driver is NOT, so mock's kind score is exactly as predictable as
// its cause score.
func oppositeKind(trueKind string) string {
	if trueKind == "recurring" {
		return "one-time"
	}
	return "recurring"
}

// mockDeliverable answers as if a model had written it, from the anomaly
// and its true kind alone -- no prompt is read, no network is touched.
//
// oracle=false names the service and the day the packet shows (so mock
// scores 100% on both) and a fixed wrong cause and the wrong kind (0% on
// cause, 0% on kind): the baseline that proves the scorer can say no.
//
// oracle=true is handed the truth and names it: the true cause AND the
// true kind, so it scores 100% across the board -- proof the scorer can
// say yes.
func mockDeliverable(an anomaly.Anomaly, trueKind string, oracle bool) string {
	cause, kind := mockWrongCause, oppositeKind(trueKind)
	if oracle {
		cause, kind = an.Driver, trueKind
	}
	body := fmt.Sprintf(
		"## Mock deliverable\n\nservice: %s\nday: %s\nThis looks like a %s driver.\n\n",
		an.Service, an.Day, kind)
	return body + mockOptionsBlock("anomaly.explain", cause)
}

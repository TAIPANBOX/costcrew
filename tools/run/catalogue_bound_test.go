package main

// costcrew#67, section 4: the worst case must cover the tool catalogue the
// loop actually sends as `tools` on every round but the last
// (tools/run/tools.go's anthropicTools()/openAITools()), not only the
// task's own prompt bytes. The issue's own arithmetic: 0.05811 settled -
// 200 output tokens x 75e-6 = 0.04311 of input, / 15e-6 = 2874 input
// tokens, against a prompt the estimate had bounded at about 2833 bytes.
//
// R2 (this file) compiles at the base (cb90412) and goes red with a
// FIGURE: the base bounds only e.PromptTokens, understating the real worst
// case by the catalogue's own bytes at the input rate. R1 (added once
// internal/deliver.ToolCatalogueTokens exists) is red by compile at the
// base and, once the two constants exist at 0, red a second time naming
// the real rendered byte counts -- the figures that become the constants.

import (
	"encoding/json"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// catalogueBoundTask is the same task shape anthropicTaskPricedLikeTonight
// prices (reserved_worst_case_test.go), for an analyst on a different
// engine: the two looping engines (anthropic, openrouter) must each carry
// their own catalogue's bytes on this exact packet, and bedrock (outside
// the loop) must not.
func catalogueBoundTask(engine, name string) (crew.Task, crew.Analyst) {
	task := crew.Task{Title: "Explain the Amazon EC2 move on 2026-09-02",
		Goal: "410.00 above of baseline on the aws desk. Say what happened, " +
			"whether it recurs, and what it would take to stop it.",
		Budget: money.Cents(100_00)}
	return task, crew.Analyst{Name: name, Engine: engine, State: "active"}
}

// TestTheWorstCaseCoversTheToolCatalogueTheLoopActuallySends is R2: the
// worst case for a looping engine must equal the prompt plus the REAL
// rendered catalogue's bytes at the input rate, never the prompt alone,
// and an engine outside the loop (bedrock) must be unchanged.
//
// It references only e.PromptTokens, e.WorstMicros, e.Price and the two
// catalogue renderers, so it compiles at the base; it deliberately never
// reads e.CatalogueTokens, which does not exist there yet.
func TestTheWorstCaseCoversTheToolCatalogueTheLoopActuallySends(t *testing.T) {
	e := anthropicTaskPricedLikeTonight(money.Cents(100_00))
	if !e.Priced || e.Refused {
		t.Fatalf("the fixture task was not priced cleanly: priced=%v refused=%v verdict=%q",
			e.Priced, e.Refused, e.Verdict)
	}
	raw, err := json.Marshal(anthropicTools())
	if err != nil {
		t.Fatal(err)
	}
	want := deliver.WorstCaseMicros(e.PromptTokens+len(raw), 2000, e.Price)
	if e.WorstMicros != want {
		t.Errorf("the worst case %s does not cover the %d-byte tool catalogue the loop "+
			"sends on every round: want %s (prompt %d + catalogue %d tokens at the input "+
			"rate, plus the output cap); costcrew#67 settled 2874 input tokens against a "+
			"prompt bounded at about 2833", usd(e.WorstMicros), len(raw), usd(want),
			e.PromptTokens, len(raw))
	}

	orTask, orAnalyst := catalogueBoundTask("openrouter", "investigator-aws")
	eo := price(nil, orTask, orAnalyst, 2000)
	if !eo.Priced || eo.Refused {
		t.Fatalf("the openrouter fixture task was not priced cleanly: priced=%v refused=%v verdict=%q",
			eo.Priced, eo.Refused, eo.Verdict)
	}
	rawO, err := json.Marshal(openAITools())
	if err != nil {
		t.Fatal(err)
	}
	wantO := deliver.WorstCaseMicros(eo.PromptTokens+len(rawO), 2000, eo.Price)
	if eo.WorstMicros != wantO {
		t.Errorf("openrouter: the worst case %s does not cover the %d-byte tool catalogue "+
			"the loop sends on every round: want %s (prompt %d + catalogue %d tokens at the "+
			"input rate, plus the output cap)", usd(eo.WorstMicros), len(rawO), usd(wantO),
			eo.PromptTokens, len(rawO))
	}

	bTask, bAnalyst := catalogueBoundTask("bedrock", "b")
	eb := price(nil, bTask, bAnalyst, 2000)
	if eb.Priced {
		wantB := deliver.WorstCaseMicros(eb.PromptTokens, 2000, eb.Price)
		if eb.WorstMicros != wantB {
			t.Errorf("bedrock's worst case %s moved although bedrock is sent no catalogue "+
				"at all: want %s, unchanged", usd(eb.WorstMicros), usd(wantB))
		}
	}
}

// TestTheToolCatalogueBoundIsWhatTheRunnerActuallySends is R1: it renders
// the REAL catalogue both providers see and requires
// deliver.ToolCatalogueTokens to equal its byte length exactly, in both
// directions -- adding a tool turns this red with the new figure in its
// own message, which is the moment to move the pinned constant.
func TestTheToolCatalogueBoundIsWhatTheRunnerActuallySends(t *testing.T) {
	for _, c := range []struct {
		engine string
		tools  []map[string]any
	}{
		{"anthropic", anthropicTools()},
		{"openrouter", openAITools()},
	} {
		raw, err := json.Marshal(c.tools)
		if err != nil {
			t.Fatal(err)
		}
		got := deliver.ToolCatalogueTokens(c.engine)
		if got != len(raw) {
			t.Errorf("deliver.ToolCatalogueTokens(%q) = %d, but the catalogue tools/run "+
				"actually sends renders to %d bytes: set the constant in "+
				"internal/deliver/estimate.go to %d", c.engine, got, len(raw), len(raw))
		}
	}
	if got := deliver.ToolCatalogueTokens("bedrock"); got != 0 {
		t.Errorf("deliver.ToolCatalogueTokens(\"bedrock\") = %d, want 0", got)
	}
	if got := deliver.ToolCatalogueTokens("a-name-from-nowhere"); got != 0 {
		t.Errorf("deliver.ToolCatalogueTokens(\"a-name-from-nowhere\") = %d, want 0", got)
	}
}

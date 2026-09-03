package main

// B4-STEP-TWO-SPEC.md section 5: "No cadence run ever asks the model to
// plan: -due stays deterministic by construction." Section 6's own
// red-first list names this directly: "-due never calls the planner".
//
// This is the same shape TestThisBinaryCannotSpend (main_test.go) already
// holds for main.go: read every production source file in this package for
// the literal names of the symbols the planner would need, and require none
// of them appear. -due's own arithmetic (price(), CadenceDue, runDueOn) is
// untouched by this step, so the honest proof is that nothing in this
// package ever names the four new deliver/crew symbols a plan-ask would
// require, not merely that due.go's own diff is empty.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// planAskSymbols are the exported names B4 step two added: internal/deliver's
// PlanPacket, PlanPrompt and PlanWorstCase (internal/deliver/plan_packet.go),
// and internal/crew's ValidatePlanAnswer (internal/crew/plan_ask.go). None of
// them exist anywhere in this package's own production files.
var planAskSymbols = []string{
	"PlanPacket", "PlanPrompt", "PlanWorstCase", "ValidatePlanAnswer",
}

func TestDueNeverCallsThePlanner(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		for _, sym := range planAskSymbols {
			if strings.Contains(string(src), sym) {
				t.Errorf("%s names %q: -due (and every other path in this package) must stay "+
					"deterministic by construction, and this step's own model-planning symbols "+
					"must never be reachable from it", name, sym)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("no production file was scanned; this test measured nothing")
	}
}

package deliver

// The seams of the policy: what a binary's main does with the flag, what a
// hand-typed brief does under masked, and what the catalogue's decision table
// says.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

func TestConfiguringInstallsTheParsedPolicyAndTheStoreCanBeBoundToIt(t *testing.T) {
	in := newInstallation(t)
	prev := ActivePolicy()
	defer func() { SetActivePolicy(prev) }()

	dir := t.TempDir()
	mode, err := ConfigurePromptData("masked", dir)
	if err != nil || mode != PromptMasked {
		t.Fatalf("ConfigurePromptData(masked) = %q, %v", mode, err)
	}
	if ActivePolicy().Mode() != PromptMasked {
		t.Errorf("the active policy is %s after configuring masked", ActivePolicy().Mode())
	}
	if ActivePolicy().Full() || ActivePolicy().Aggregates() {
		t.Error("the active policy is neither masked nor restricted")
	}
	if _, err := os.Stat(filepath.Join(dir, KeyFileName)); err != nil {
		t.Errorf("configuring masked made no key in the data directory: %v", err)
	}

	// unbound, it still masks the static vocabulary; bound, it knows the store
	if got := ActivePolicy().MaskText("Amazon EC2 on aws"); strings.Contains(got, "aws") {
		t.Errorf("an unbound policy left a desk of the generated estate: %q", got)
	}
	BindActivePolicy(in.db)
	if got := ActivePolicy().MaskText("spend on Amazon EC2"); strings.Contains(got, "Amazon EC2") {
		t.Errorf("a bound policy left a service of the store: %q", got)
	}

	if _, err := ConfigurePromptData("full", dir); err != nil || !ActivePolicy().Full() {
		t.Errorf("configuring full did not leave a full policy: %v", err)
	}
	// a nil policy changes nothing
	restore := SetActivePolicy(nil)
	restore()
	if !ActivePolicy().Full() {
		t.Error("installing a nil policy replaced the active one")
	}
}

func TestAMaskingModeNeedsADataDirectoryToKeepItsKeyIn(t *testing.T) {
	for _, mode := range []PromptData{PromptMasked, PromptAggregates} {
		if _, err := NewPolicy(mode, ""); err == nil {
			t.Errorf("%s was started with nowhere to keep its key", mode)
		}
	}
	if _, err := NewPolicy("maskd", t.TempDir()); err == nil {
		t.Error("a policy was built for a mode outside the vocabulary")
	}
	if _, err := NewPolicy(PromptFull, ""); err != nil {
		t.Errorf("full needs no directory: %v", err)
	}
}

func TestOnlyTheRoleFamilysOwnBriefIsSentUnderMasked(t *testing.T) {
	roster, err := crew.Roster(newInstallation(t).db)
	if err != nil {
		t.Fatal(err)
	}
	var seeded crew.Analyst
	for _, a := range roster {
		if r, ok := crew.RoleForDesk(a.Name, a.Desk); ok && r.Mission == a.Mission && a.Mission != "" {
			seeded = a
			break
		}
	}
	if seeded.Name == "" {
		t.Fatal("no seeded analyst carries its role family's own mission, so the test has nothing to compare")
	}
	hired := seeded
	hired.Mission = "Watch the ml-platform team's spend for Priya and report to her."

	for _, mode := range []PromptData{PromptMasked, PromptAggregates} {
		p, _ := NewPolicy(mode, t.TempDir())
		if got := briefFor(p, seeded); got != seeded.Mission {
			t.Errorf("%s: a role family's own mission was changed: %q", mode, got)
		}
		if got := briefFor(p, hired); got != WithheldFreeText {
			t.Errorf("%s: a hand-typed mission was sent: %q", mode, got)
		}
	}
	full, _ := NewPolicy(PromptFull, t.TempDir())
	if got := briefFor(full, hired); got != hired.Mission {
		t.Errorf("full: a hand-typed mission was changed: %q", got)
	}
	// an analyst no role family matches has only what somebody typed
	orphan := crew.Analyst{Name: "x-nobody", Desk: "nowhere", Mission: "anything"}
	p, _ := NewPolicy(PromptMasked, t.TempDir())
	if got := briefFor(p, orphan); got != WithheldFreeText {
		t.Errorf("a brief with no role family behind it was sent: %q", got)
	}
}

func TestEachModeOffersTheToolsItsTableSays(t *testing.T) {
	cases := map[PromptData]map[string]bool{
		PromptFull:       {"charges_query": true, "ai_calls_query": true, "anomaly": true, "kpis": true},
		PromptMasked:     {"charges_query": false, "ai_calls_query": false, "anomaly": true, "series": true, "kpis": true},
		PromptAggregates: {"charges_query": false, "ai_calls_query": false, "anomaly": false, "series": false, "drivers": false, "kpis": true, "team_month": true},
	}
	for mode, want := range cases {
		p, _ := NewPolicy(mode, t.TempDir())
		for tool, offered := range want {
			if got := p.ToolOffered(tool); got != offered {
				t.Errorf("%s: ToolOffered(%s) = %v, want %v", mode, tool, got, offered)
			}
		}
		if mode != PromptFull && p.ToolOffered("") {
			t.Errorf("%s offered a tool with no name", mode)
		}
	}
}

func TestEveryModeSaysWhichItIs(t *testing.T) {
	seen := map[string]bool{}
	for _, mode := range []PromptData{PromptFull, PromptMasked, PromptAggregates} {
		p, _ := NewPolicy(mode, t.TempDir())
		line := p.ModeLine()
		if !strings.HasPrefix(line, "Prompt data policy: "+string(mode)+".") {
			t.Errorf("%s: the mode line begins %q", mode, line)
		}
		if strings.Contains(line, "\n") {
			t.Errorf("%s: the mode line is more than one line", mode)
		}
		if seen[line] {
			t.Errorf("%s: two modes share one mode line", mode)
		}
		seen[line] = true
	}
}

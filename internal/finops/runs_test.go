package finops_test

// Invariant 101: the run grain. The fixture,
// internal/connectors/testdata/tokenfuse-focus-runs-2026-10-14.csv, is seven
// runs in TokenFuse 1.8.0's export shape: w2-b12-A called Opus three times and
// then Haiku twice (96% of its spend on Opus, its last call on Haiku, the
// shape forge measured on 2026-10-14), run-killed ran into a kill, run-budget
// was stopped by its budget on its last call, run-on had a call refused and
// went on, one call named no run, run-blocked-only was a refusal for identity
// and spent nothing, and run-day2 crossed midnight.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

const exportRuns = "tokenfuse-focus-runs-2026-10-14.csv"

func runsByID(t *testing.T) (map[string]finops.RunRow, []finops.RunRow) {
	t.Helper()
	db := bareDB(t)
	importExports(t, db, exportRuns)
	runs, err := finops.AIRuns(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]finops.RunRow{}
	for _, r := range runs {
		by[r.RunID] = r
	}
	return by, runs
}

func TestARunThatSwitchedModelsListsEveryModelWithItsShare(t *testing.T) {
	by, _ := runsByID(t)
	r, ok := by["w2-b12-A"]
	if !ok {
		t.Fatal("no row for run w2-b12-A")
	}
	if !r.Switched() || len(r.Models) != 2 {
		t.Fatalf("w2-b12-A called two models and reads as %d: %+v", len(r.Models), r.Models)
	}
	type m struct {
		model, share string
		calls        int
		cost         money.Micros
	}
	var got []m
	for _, x := range r.Models {
		got = append(got, m{x.Model, x.Share(), x.Calls, x.Cost})
	}
	want := []m{{"claude-opus-5-5", "96%", 3, 48_000}, {"claude-haiku-5-5", "4.0%", 2, 2_000}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("w2-b12-A's models, in the order called:\n got %+v\nwant %+v", got, want)
	}
	if r.LastModel != "claude-haiku-5-5" {
		t.Errorf("the last call's model is %q, want claude-haiku-5-5", r.LastModel)
	}
	if r.Cost != 50_000 || r.Calls != 5 || r.State() != "ran" {
		t.Errorf("w2-b12-A: %s over %d calls, %q; want 0.05 over 5, ran", r.Cost, r.Calls, r.State())
	}
	if one := by["run-budget"]; one.Switched() || len(one.Models) != 1 || one.Models[0].Share() != "100%" {
		t.Errorf("a one-model run reads as switched or not whole: %+v", one.Models)
	}
}

func TestARunSaysHowItEndedAsTheGatewaysRowsShow(t *testing.T) {
	by, _ := runsByID(t)
	for run, want := range map[string]string{
		"run-killed":       "killed",
		"run-budget":       "stopped by the gateway: budget_exceeded",
		"run-on":           "1 call refused, then went on",
		"run-blocked-only": "stopped by the gateway: identity_mismatch",
		"w2-b12-A":         "ran",
		"":                 "ran",
	} {
		if got := by[run].State(); got != want {
			t.Errorf("run %q: %q, want %q", run, got, want)
		}
	}
	if k := by["run-killed"]; !k.Killed || k.BlockedText() != "1 killed" {
		t.Errorf("run-killed: killed %v, blocked %q", k.Killed, k.BlockedText())
	}
	if b := by["run-blocked-only"]; b.Models[0].Share() != "no spend" || b.Cost != 0 {
		t.Errorf("a run that spent nothing reads a share of %q", b.Models[0].Share())
	}
	if d := by["run-day2"]; d.Calls != 2 || d.Cost != 8_000 || !strings.HasPrefix(d.First, "2026-10-14") || !strings.HasPrefix(d.Last, "2026-10-15") {
		t.Errorf("a run across midnight is not one run: %+v", d)
	}
}

func TestRunsAreListedLargestSpendFirstTheSameEveryCall(t *testing.T) {
	db := bareDB(t)
	importExports(t, db, exportRuns)
	first, err := finops.AIRuns(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := finops.AIRuns(db, "2026-10")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("call %d read the same month's runs differently", i+2)
		}
	}
	var ids []string
	for _, r := range first {
		ids = append(ids, r.RunID)
	}
	want := []string{"w2-b12-A", "run-killed", "run-day2", "run-budget", "", "run-on", "run-blocked-only"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("runs in order:\n got %q\nwant %q", ids, want)
	}
	s := finops.SummarizeRuns(first)
	if s != (finops.RunsSummary{Runs: 7, Switched: 1, Killed: 1, Stopped: 2}) {
		t.Errorf("summary %+v, want 7 runs, 1 switched, 1 killed, 2 stopped", s)
	}
}

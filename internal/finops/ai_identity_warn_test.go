package finops_test

// Invariant 100: a call TOKENFUSE_IDENTITY_STRICT=warn forwarded although the
// identity check failed is marked, with its spend, per agent, per team and per
// run. The fixture is internal/connectors/testdata/tokenfuse-focus-1.8.0-2026-10-14.csv:
// one honest call, three forwarded under warn mode (home-routers: two calls of
// run-7 at 0.12 and 0.03; edge-lab: one call of run-8 at 0.01), one refusal
// for identity and one warned call the Breaker then blocked for budget.

import (
	"reflect"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

const export180 = "tokenfuse-focus-1.8.0-2026-10-14.csv"

func TestAForwardedUnderWarnCallIsMarkedPerTeamAndPerRun(t *testing.T) {
	db := bareDB(t)
	importExports(t, db, export180)
	w, err := finops.IdentityWarnings(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if w.Calls != 3 || w.Cost != money.Micros(160_000) {
		t.Errorf("forwarded under warn: %d calls, %s; want 3 calls, 0.16 (a refusal and a Breaker block spent nothing and were not forwarded)",
			w.Calls, w.Cost)
	}
	if w.Reported != 6 || w.Unreported != 0 {
		t.Errorf("reported %d, unreported %d; want 6 and 0 for a 1.8.0 export", w.Reported, w.Unreported)
	}
	type g struct {
		key, reasons string
		calls        int
		cost         money.Micros
		agents       []string
	}
	got := func(gs []finops.WarnGroup) []g {
		var out []g
		for _, x := range gs {
			out = append(out, g{x.Key, x.ReasonText(), x.Calls, x.Cost, x.Agents})
		}
		return out
	}
	wantTeams := []g{
		{"home-routers", "2 agent_id_not_allowed", 2, 150_000, []string{"agent://acme.example/finops/flint"}},
		{"edge-lab", "1 unit_chosen_by_agent_id", 1, 10_000, []string{"agent://acme.example/routers/edge"}},
	}
	if gt := got(w.ByTeam); !reflect.DeepEqual(gt, wantTeams) {
		t.Errorf("by team:\n got %+v\nwant %+v", gt, wantTeams)
	}
	wantRuns := []g{
		{"run-7", "2 agent_id_not_allowed", 2, 150_000, []string{"agent://acme.example/finops/flint"}},
		{"run-8", "1 unit_chosen_by_agent_id", 1, 10_000, []string{"agent://acme.example/routers/edge"}},
	}
	if gr := got(w.ByRun); !reflect.DeepEqual(gr, wantRuns) {
		t.Errorf("by run:\n got %+v\nwant %+v", gr, wantRuns)
	}
	if w.TeamCalls["home-routers"] != 2 || w.TeamCalls["edge-lab"] != 1 || w.TeamCalls["finops"] != 0 {
		t.Errorf("the per-team flag: %v", w.TeamCalls)
	}
}

func TestTheAgentRowCountsItsForwardedCallsInsideItsSpend(t *testing.T) {
	db := bareDB(t)
	importExports(t, db, export180)
	rows, err := finops.AIByAgent(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]finops.AgentAIRow{}
	for _, r := range rows {
		by[r.Agent] = r
	}
	flint := by["agent://acme.example/finops/flint"]
	if flint.WarnedCalls != 2 || flint.WarnedCost != money.Micros(150_000) {
		t.Errorf("flint's forwarded calls: %d, %s; want 2, 0.15", flint.WarnedCalls, flint.WarnedCost)
	}
	// The forwarded calls are real spend: still inside the row's own figures.
	if flint.Calls != 4 || flint.Cost != money.Micros(200_000) {
		t.Errorf("flint's row: %d calls, %s; want 4 calls (one honest, two forwarded, one blocked) and 0.20", flint.Calls, flint.Cost)
	}
	if key := by["key:imposter"]; key.WarnedCalls != 0 {
		t.Error("a refusal for identity is counted as a forwarded call")
	}
}

func TestAnOlderExportIsCountedAsNotReportingIdentityWarn(t *testing.T) {
	db := bareDB(t)
	importExports(t, db, export170)
	w, err := finops.IdentityWarnings(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if w.Calls != 0 || w.Reported != 0 || w.Unreported != 4 || len(w.ByTeam) != 0 || len(w.ByRun) != 0 {
		t.Errorf("a 1.7.0 export: %+v; want nothing forwarded, 0 reported, 4 unreported", w)
	}
}

func TestWarnGroupsAreOrderedTheSameEveryCall(t *testing.T) {
	db := bareDB(t)
	importExports(t, db, export180)
	first, err := finops.IdentityWarnings(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := finops.IdentityWarnings(db, "2026-10")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("call %d read the same month in a different order", i+2)
		}
	}
}

func TestWarnGroupsBreakTiesOnCallsAndThenTheKey(t *testing.T) {
	db := bareDB(t)
	importExports(t, db, export180)
	// Three more forwarded calls, all in run-9 for edge-lab's agent: one at
	// 0.01 (the same spend as run-8, more calls) and two at nothing.
	if _, err := db.Exec(`INSERT INTO ai_calls (file_sha256, row_no, ts, day, team, agent, run_id,
			provider, model, tokens_in, tokens_out, billed_microusd, blocked, basis, key_id, block_reason, identity_reason)
		VALUES ('t', 1, '2026-10-14T11:00:00Z', '2026-10-14', 'zz-lab', 'agent://x/y', 'run-9', 'Anthropic', 'm', 1, 1, 5000, 0, 'settled', 'k', '', 'agent_id_missing'),
		       ('t', 2, '2026-10-14T11:00:01Z', '2026-10-14', 'zz-lab', 'agent://x/y', 'run-9', 'Anthropic', 'm', 1, 1, 5000, 0, 'settled', 'k', '', 'agent_id_missing'),
		       ('t', 3, '2026-10-14T11:00:02Z', '2026-10-14', 'aa-lab', 'agent://x/z', 'run-0', 'Anthropic', 'm', 1, 1, 10000, 0, 'settled', 'k', '', 'agent_id_missing')`); err != nil {
		t.Fatal(err)
	}
	w, err := finops.IdentityWarnings(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	var runs, teams []string
	for _, g := range w.ByRun {
		runs = append(runs, g.Key)
	}
	for _, g := range w.ByTeam {
		teams = append(teams, g.Key)
	}
	// run-9 and zz-lab: 0.01 over two calls; run-0, run-8, aa-lab, edge-lab:
	// 0.01 over one call, then by name.
	if want := []string{"run-7", "run-9", "run-0", "run-8"}; !reflect.DeepEqual(runs, want) {
		t.Errorf("runs in order: %q, want %q", runs, want)
	}
	if want := []string{"home-routers", "zz-lab", "aa-lab", "edge-lab"}; !reflect.DeepEqual(teams, want) {
		t.Errorf("teams in order: %q, want %q", teams, want)
	}
}

func TestIdentityWarningsReportsAStoreThatCannotBeRead(t *testing.T) {
	db := bareDB(t)
	importExports(t, db, export180)
	db.Close()
	if _, err := finops.IdentityWarnings(db, "2026-10"); err == nil {
		t.Error("IdentityWarnings read a closed store without an error")
	}
	if _, err := finops.AIByAgent(db, "2026-10"); err == nil {
		t.Error("AIByAgent read a closed store without an error")
	}
}

package finops_test

// C7-SPEC.md: the ai-spend and unit-econ-ai KPIs and the queries their
// packets are built from. Red first, against the code before this step:
// AIByModel, OutcomeCountsByAgent, BasisCounts and CostPerOutcome did not
// exist at all, so every test below failed to compile ("undefined:
// finops.AIByModel" and so on) -- the same red-first shape
// TestBenchPacketHidesTheDriverLabelAndItsKind's own comment already
// documents in internal/deliver for exactly this reason: a brand new
// function has no green state to have been broken from. cost-per-outcome
// itself was always Blocked, unconditionally (kpi.go's own comment said so
// before this step, verbatim: "the business metric this would divide by is
// not connected"), so the two KPI-level tests below are red the ordinary
// way too: HasVal and Value could never have been anything but false and
// empty.

import (
	"database/sql"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

func TestAIByModelCountsCallsTokensCostAndBlocked(t *testing.T) {
	db := bareDB(t)
	month := importFixture(t, db, false)

	rows, err := finops.AIByModel(db, month)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("AIByModel returned %d rows, want 3 (haiku, sonnet, opus): %+v", len(rows), rows)
	}
	byModel := map[string]finops.ModelAIRow{}
	for _, r := range rows {
		byModel[r.Model] = r
	}
	haiku := byModel["claude-haiku-4-5"]
	if haiku.Calls != 2 || haiku.BlockedCalls != 0 || haiku.Cost != 7_000 {
		t.Errorf("haiku = %+v, want 2 calls, 0 blocked, 7000 micros", haiku)
	}
	sonnet := byModel["claude-sonnet-4-5"]
	if sonnet.Calls != 1 || sonnet.BlockedCalls != 0 || sonnet.Cost != 10_500 {
		t.Errorf("sonnet = %+v, want 1 call, 0 blocked, 10500 micros", sonnet)
	}
	// opus carries both the settled call (forecaster) AND the blocked one
	// (deep-analysis): grouping is by model, not by agent, so this is the
	// one row where BlockedCalls is not zero, and its cost must still be
	// exactly the settled call's amount -- the blocked one is guaranteed
	// zero by the reader's own parse-time refusal, but this proves the sum
	// is right rather than merely trusting that guarantee.
	opus := byModel["claude-opus-4-5"]
	if opus.Calls != 2 || opus.BlockedCalls != 1 {
		t.Errorf("opus = %+v, want 2 calls (1 settled, 1 blocked)", opus)
	}
	if opus.Cost != 17_500 {
		t.Errorf("opus cost = %d micros, want 17500 (the blocked call adds zero)", opus.Cost)
	}
}

func TestOutcomeCountsByAgentCountsOnlyNonBlockedTaggedCalls(t *testing.T) {
	db := bareDB(t)
	month := importFixture(t, db, false)

	counts, err := finops.OutcomeCountsByAgent(db, month)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"agent://taipanbox.dev/costcrew/triage-aws": 1, // case_resolved
		"agent://taipanbox.dev/costcrew/forecaster": 1, // escalated
		// deep-analysis's one call is blocked and carries no outcome: absent
		// from the map entirely, not present with 0.
	}
	for agent, n := range want {
		if got := counts[agent]; got != n {
			t.Errorf("counts[%q] = %d, want %d: %+v", agent, got, n, counts)
		}
	}
	if _, ok := counts["agent://taipanbox.dev/costcrew/deep-analysis"]; ok {
		t.Errorf("deep-analysis (blocked-only) is present in the map at all: %+v", counts)
	}
}

func TestBasisCountsSplitsSettledEstimatedBlocked(t *testing.T) {
	db := bareDB(t)
	month := importFixture(t, db, false)

	settled, estimated, blocked, err := finops.BasisCounts(db, month)
	if err != nil {
		t.Fatal(err)
	}
	if settled != 4 || estimated != 0 || blocked != 1 {
		t.Errorf("BasisCounts = settled=%d estimated=%d blocked=%d, want 4, 0, 1",
			settled, estimated, blocked)
	}
}

// TestCostPerOutcomeReportsOnceAnyOutcomeExists is the fixture case: both
// real agents (triage-aws, forecaster) tag exactly one outcome each, so the
// desk-wide figure is their combined cost over their combined outcome
// count, and neither counts as "set none" -- the fixture alone cannot prove
// that sentence; TestCostPerOutcomeCountsAnAgentWithCostAndNoOutcome does.
func TestCostPerOutcomeReportsOnceAnyOutcomeExists(t *testing.T) {
	db := bareDB(t)
	month := importFixture(t, db, false)

	perOutcome, hasVal, withNone, total, err := finops.CostPerOutcome(db, month)
	if err != nil {
		t.Fatal(err)
	}
	if !hasVal {
		t.Fatal("hasVal = false, want true: two of the fixture's agents tag an outcome")
	}
	// (7000 + 28000) micros over 2 outcomes = 17500 micros/outcome exactly.
	if perOutcome != 17_500 {
		t.Errorf("perOutcome = %d micros, want 17500 (35000 total over 2 outcomes)", perOutcome)
	}
	if total != 2 {
		t.Errorf("agentsTotal = %d, want 2 (triage-aws, forecaster; deep-analysis spent nothing)", total)
	}
	if withNone != 0 {
		t.Errorf("agentsWithNone = %d, want 0: both agents that spent this month tagged an outcome", withNone)
	}
}

// TestCostPerOutcomeCountsAnAgentWithCostAndNoOutcome is C7-SPEC.md section
// 4's own boundary ("an agent with cost and no outcome"), planted by hand
// because the fixture's own two real agents both tag one outcome each. It
// also proves the "count blocked rows as cost" mutant this step names: a
// third agent whose only call is blocked spends nothing and must be
// excluded from both counts, not read as a third agent that "set none".
//
// perOutcome pools BOTH real agents' cost over the one outcome either of
// them tagged (CostPerOutcome's own doc comment: "an agent whose calls cost
// money and tagged nothing still counts in the cost"), so it is
// untagged-agent's 3,000,000 micros landing in the SAME denominator as
// tagged-agent's 5,000,000 that this test actually proves; a version that
// silently dropped the untagged agent's cost from the numerator would still
// pass a test that only checked tagged-agent's own share.
func TestCostPerOutcomeCountsAnAgentWithCostAndNoOutcome(t *testing.T) {
	db := bareDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	plantAICall(t, db, aiCallRow{
		Agent: "tagged-agent", Model: "claude-haiku-4-5", Day: "2026-09-05",
		Micros: 5_000_000, Outcome: "case_resolved",
	})
	plantAICall(t, db, aiCallRow{
		Agent: "untagged-agent", Model: "claude-sonnet-4-5", Day: "2026-09-05",
		Micros: 3_000_000, // real cost, no outcome
	})
	plantAICall(t, db, aiCallRow{
		Agent: "blocked-only-agent", Model: "claude-opus-4-5", Day: "2026-09-05",
		Micros: 0, Blocked: true, Basis: "blocked",
	})

	perOutcome, hasVal, withNone, total, err := finops.CostPerOutcome(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if !hasVal {
		t.Fatal("hasVal = false, want true: tagged-agent tags an outcome")
	}
	if perOutcome != 8_000_000 {
		t.Errorf("perOutcome = %d micros, want 8000000 (5000000 + 3000000, the desk's whole "+
			"real cost, over the one outcome tagged-agent tagged)", perOutcome)
	}
	if total != 2 {
		t.Errorf("agentsTotal = %d, want 2 (tagged-agent, untagged-agent; the blocked-only "+
			"agent spent nothing and must not be counted as a third)", total)
	}
	if withNone != 1 {
		t.Errorf("agentsWithNone = %d, want 1 (untagged-agent only)", withNone)
	}
}

// TestCostPerOutcomeRefusesWithACountWhenNoOutcomeExists is the whole-month
// refusal: every agent that spent this month tagged nothing.
func TestCostPerOutcomeRefusesWithACountWhenNoOutcomeExists(t *testing.T) {
	db := bareDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	plantAICall(t, db, aiCallRow{Agent: "a1", Model: "claude-haiku-4-5", Day: "2026-09-05", Micros: 1_000_000})
	plantAICall(t, db, aiCallRow{Agent: "a2", Model: "claude-haiku-4-5", Day: "2026-09-05", Micros: 2_000_000})

	perOutcome, hasVal, withNone, total, err := finops.CostPerOutcome(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if hasVal {
		t.Fatalf("hasVal = true with nothing tagged this month, perOutcome=%d", perOutcome)
	}
	if total != 2 || withNone != 2 {
		t.Errorf("agentsTotal=%d agentsWithNone=%d, want 2 and 2 (both agents spent and "+
			"tagged nothing)", total, withNone)
	}
}

// TestCostPerOutcomeRefusesOnAnEmptyMonth is the plainer refusal: nothing
// spent on the AI desk at all this month, agentsTotal itself is 0.
func TestCostPerOutcomeRefusesOnAnEmptyMonth(t *testing.T) {
	db := bareDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	_, hasVal, withNone, total, err := finops.CostPerOutcome(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if hasVal || total != 0 || withNone != 0 {
		t.Errorf("hasVal=%v total=%d withNone=%d, want false, 0, 0 on an empty month",
			hasVal, total, withNone)
	}
}

// TestCostPerOutcomeSumsMicrosExactlyNotCentsPerRow is the mutant this step
// names by its own words: sum cents per row before the total. Ten calls at
// 3,500 micros ($0.0035) each, all landing on one agent, one of them tagged:
// AIByAgent's own SQL sums them exactly (35,000 micros), and CostPerOutcome
// must carry that exact figure through its own desk-wide sum untouched.
// Proven by hand: mutating CostPerOutcome's accumulation to round EACH
// agent's cost to its own nearest cent before adding it in (35,000 micros,
// 3.5 cents, rounds to 4 cents = 40,000 micros) turned this test red,
// wanting 35000 and getting 40000, then reverted -- named in the PR body
// rather than kept as a scripts/gates-have-teeth.sh case, the same way
// charges_query.go's other security layers are proven by hand there.
// Summing the exact Micros and never touching Cents anywhere in the path
// (invariant 25's own property) is what this test guards going forward.
func TestCostPerOutcomeSumsMicrosExactlyNotCentsPerRow(t *testing.T) {
	db := bareDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		outcome := ""
		if i == 0 {
			outcome = "case_resolved" // one outcome for the whole agent's cost
		}
		plantAICall(t, db, aiCallRow{
			Agent: "sub-cent-agent", Model: "claude-haiku-4-5", Day: "2026-09-05",
			Micros: 3_500, Outcome: outcome,
		})
	}
	perOutcome, hasVal, _, _, err := finops.CostPerOutcome(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if !hasVal {
		t.Fatal("hasVal = false, want true: one of the ten calls tags an outcome")
	}
	if perOutcome != 35_000 {
		t.Errorf("perOutcome = %d micros, want 35000 (10 x 3500, summed exactly, over 1 "+
			"outcome); rounding each call to cents first would give 0", perOutcome)
	}
}

func TestCostPerOutcomeKPIReportsAfterTheFixtureImport(t *testing.T) {
	db := kpiDB(t)
	month := importFixture(t, db, true)

	ks, err := finops.KPIs(db, month)
	if err != nil {
		t.Fatal(err)
	}
	var k finops.KPI
	for _, x := range ks {
		if x.ID == "cost-per-outcome" {
			k = x
		}
	}
	if k.Blocked != "" {
		t.Errorf("cost-per-outcome is still blocked after a real import: %q", k.Blocked)
	}
	// 17500 micros/outcome is 1.75 cents, which String() rounds to the
	// nearest cent for display (Micros.String() only keeps four decimals
	// under a cent): "0.02".
	if !k.HasVal || k.Value != "0.02" {
		t.Errorf("cost-per-outcome = %+v, want HasVal and Value \"0.02\"", k)
	}
	if k.Note != "" {
		t.Errorf("cost-per-outcome carries a Note %q with full coverage (both of the "+
			"fixture's real agents tag an outcome); want no caveat", k.Note)
	}
}

// TestCostPerOutcomeKPINotesPartialCoverage is the caveat half of the same
// KPI: it still reports once ANY outcome exists, but says how many of the
// agents that spent this month tagged none, rather than letting a partial
// figure read as a complete one.
func TestCostPerOutcomeKPINotesPartialCoverage(t *testing.T) {
	db := kpiDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	plantAICall(t, db, aiCallRow{
		Agent: "tagged-agent", Model: "claude-haiku-4-5", Day: "2026-09-05",
		Micros: 5_000_000, Outcome: "case_resolved",
	})
	plantAICall(t, db, aiCallRow{
		Agent: "untagged-agent", Model: "claude-sonnet-4-5", Day: "2026-09-05",
		Micros: 3_000_000,
	})

	ks, err := finops.KPIs(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	var k finops.KPI
	for _, x := range ks {
		if x.ID == "cost-per-outcome" {
			k = x
		}
	}
	if k.Blocked != "" {
		t.Fatalf("cost-per-outcome is blocked with one agent's outcome tagged: %q", k.Blocked)
	}
	if !k.HasVal {
		t.Fatal("cost-per-outcome HasVal = false with one agent's outcome tagged")
	}
	if k.Note == "" || !strings.Contains(k.Note, "1 of 2 agents") {
		t.Errorf("cost-per-outcome Note = %q, want it to name 1 of 2 agents set none", k.Note)
	}
}

// TestCostPerOutcomeKPIRefusesWithACountBeforeAnyImport is kpiDB(t) exactly
// as TestAgentAttributionKPIBecomesComputedAfterAnImport's own "before"
// half already uses it: no EnsureFocusSchema, no import, so ai_calls does
// not exist as a table at all yet. KPIs() must still return a refusal for
// this one measure, not an error for the whole library -- this is the
// regression this step found in its own red-first pass: CostPerOutcome's
// first version queried ai_calls unconditionally and turned this exact
// scenario into a hard error, breaking the sibling test named above.
func TestCostPerOutcomeKPIRefusesWithACountBeforeAnyImport(t *testing.T) {
	db := kpiDB(t)
	ks, err := finops.KPIs(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	var k finops.KPI
	for _, x := range ks {
		if x.ID == "cost-per-outcome" {
			k = x
		}
	}
	if k.Blocked == "" {
		t.Fatal("cost-per-outcome is not blocked before anything real has been imported")
	}
	if k.HasVal {
		t.Errorf("cost-per-outcome HasVal = true alongside a Blocked refusal: %q", k.Blocked)
	}
	// Value and Unit stay at their Go zero value while blocked, exactly as
	// they did before this step (the struct literal never set them): the
	// /kpis page's own sort by value compares .Value regardless of .HasVal,
	// and a non-empty placeholder here would silently break its tie with
	// carbon-per-workload's own permanent "" -- found by the parity gate,
	// /kpis?sort=value&dir=asc, and this is the regression test for it.
	if k.Value != "" || k.Unit != "" {
		t.Errorf("cost-per-outcome Value=%q Unit=%q while blocked, want both empty", k.Value, k.Unit)
	}
}

// ------------------------------------------------------------------ helpers

type aiCallRow struct {
	Agent, Model, Day, Outcome, Basis string
	Micros                            int64
	Blocked                           bool
}

var plantedAIRowNo int64

// plantAICall inserts one row directly into ai_calls, bypassing the CSV
// reader: these tests are about the queries and the KPI over ai_calls, not
// about the reader itself (already proven in internal/connectors), so this
// builds only the rows a scenario needs, the same shape
// TestAIUnitsFallsBackToTheFixedRatioWithNoOutcomes already does with a
// direct INSERT into charges for the same reason. Every call gets its own
// row_no (ai_calls' primary key is file_sha256, row_no), so a test can plant
// as many rows as it needs under one shared fake file id.
func plantAICall(t *testing.T, db *sql.DB, row aiCallRow) {
	t.Helper()
	basis := row.Basis
	if basis == "" {
		basis = "settled"
	}
	blockedInt := 0
	if row.Blocked {
		blockedInt = 1
	}
	var outcome any
	if row.Outcome != "" {
		outcome = row.Outcome
	}
	rowNo := atomic.AddInt64(&plantedAIRowNo, 1)
	if _, err := db.Exec(`INSERT INTO ai_calls
		(file_sha256, row_no, ts, day, team, agent, run_id, parent_run_id,
		 provider, model, tokens_in, tokens_out, billed_microusd, blocked, basis,
		 outcome, tool_calls)
		VALUES ('planted-test',?,?,?,NULL,?,NULL,NULL,'Anthropic',?,0,0,?,?,?,?,NULL)`,
		rowNo, row.Day+"T00:00:00Z", row.Day, row.Agent, row.Model, row.Micros,
		blockedInt, basis, outcome); err != nil {
		t.Fatal(err)
	}
}

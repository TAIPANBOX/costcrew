package deliver

// C7-SPEC.md section 2: aiSpendSection and unitEconomicsSection. Red first,
// against the code before this step: neither function existed, Packet()
// carried no skill-gated block for either, and this package had never
// imported the fixture CSV -- every test below either failed to compile
// ("undefined: aiSpendSection") or, once the functions existed as stubs
// returning "", failed on its own assertion that the packet names an
// agent, a model or a cost per outcome it did not carry.

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/crew"
)

func TestAISpendSectionNamesTheAgentAndTheModel(t *testing.T) {
	db := deliverTestDB(t)
	importFixtureDB(t, db)

	task := crew.Task{ID: 1, Desk: "ai"}
	a := crew.Analyst{Name: "ai-spend", State: "active", Skills: []string{"ai-spend-analysis", "token-economics"}}
	p := Packet(db, task, a, false)

	if p == "" {
		t.Fatal("the ai-spend analyst's packet is empty against a store carrying real AI spend")
	}
	if !strings.Contains(p, "The AI desk's month") {
		t.Errorf("packet does not carry the AI desk section header:\n%s", p)
	}
	// forecaster is the highest-cost agent in the fixture (28000 micros
	// against triage-aws's 7000), and must appear in the "by agent" cut.
	if !strings.Contains(p, "agent://taipanbox.dev/costcrew/forecaster") {
		t.Errorf("packet does not name forecaster, the agent with the highest cost:\n%s", p)
	}
	if !strings.Contains(p, "agent://taipanbox.dev/costcrew/triage-aws") {
		t.Errorf("packet does not name triage-aws:\n%s", p)
	}
	// Grouped by model too: all three models the fixture carries.
	for _, model := range []string{"claude-haiku-4-5", "claude-sonnet-4-5", "claude-opus-4-5"} {
		if !strings.Contains(p, model) {
			t.Errorf("packet does not name model %q:\n%s", model, p)
		}
	}
	if strings.Contains(p, "and 0 more") {
		t.Errorf("packet claims a cut with nothing left over:\n%s", p)
	}
}

// TestAISpendSectionCountsBlockedCallsAsTheSaving is the second named
// scenario: the blocked call (deep-analysis, BilledCost 0.000000) is a
// COUNT, named as the guard's saving, never folded into the desk's cost.
func TestAISpendSectionCountsBlockedCallsAsTheSaving(t *testing.T) {
	db := deliverTestDB(t)
	importFixtureDB(t, db)

	task := crew.Task{ID: 1, Desk: "ai"}
	a := crew.Analyst{Name: "ai-spend", State: "active", Skills: []string{"ai-spend-analysis"}}
	p := Packet(db, task, a, false)

	if !strings.Contains(p, "1 blocked") {
		t.Errorf("packet does not count the one blocked call:\n%s", p)
	}
	if !strings.Contains(p, "guard's saving") {
		t.Errorf("packet does not name the blocked call as the guard's saving rather than a cost:\n%s", p)
	}
	// Total calls this month: 5 (4 settled + 1 blocked).
	if !strings.Contains(p, "5 calls") {
		t.Errorf("packet does not count all 5 calls (4 settled + 1 blocked):\n%s", p)
	}
}

func TestUnitEconAIPacketNamesCostPerOutcomeFromTheFixture(t *testing.T) {
	db := deliverTestDB(t)
	importFixtureDB(t, db)

	task := crew.Task{ID: 1, Desk: "ai"}
	a := crew.Analyst{Name: "unit-econ-ai", State: "active", Skills: []string{"unit-economics", "cost-per-outcome"}}
	p := Packet(db, task, a, false)

	if p == "" {
		t.Fatal("the unit-econ-ai analyst's packet is empty against a store carrying real AI spend")
	}
	if !strings.Contains(p, "cost per outcome") {
		t.Errorf("packet does not carry the unit-economics header:\n%s", p)
	}
	if !strings.Contains(p, "per outcome") {
		t.Errorf("packet does not name a per-outcome figure for any agent:\n%s", p)
	}
	// Both real agents tag exactly one outcome each in the fixture, so
	// neither should be named as having set none.
	if strings.Contains(p, "cost with no outcome header") {
		t.Errorf("packet names an agent with no outcome, but both real agents in the fixture "+
			"tag one:\n%s", p)
	}
}

// TestUnitEconAIPacketNamesAgentsWithCostAndNoOutcome is C7-SPEC.md section
// 4's own boundary ("an agent with cost and no outcome"): planted by hand
// because the CSV fixture's own two real agents both tag one outcome each.
// It also proves "a cost with no outcome is said, not invented" (the
// feature file's own scenario): the untagged agent's name appears, and no
// per-outcome FIGURE is invented for it.
func TestUnitEconAIPacketNamesAgentsWithCostAndNoOutcome(t *testing.T) {
	db := deliverTestDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	plantAICallRow(t, db, plantedAIRow{Agent: "tagged-agent", Model: "claude-haiku-4-5",
		Day: "2026-09-05", Micros: 5_000_000, Outcome: "case_resolved"})
	plantAICallRow(t, db, plantedAIRow{Agent: "untagged-agent", Model: "claude-sonnet-4-5",
		Day: "2026-09-05", Micros: 3_000_000})

	task := crew.Task{ID: 1, Desk: "ai"}
	a := crew.Analyst{Name: "unit-econ-ai", State: "active", Skills: []string{"unit-economics"}}
	p := Packet(db, task, a, false)

	if !strings.Contains(p, "cost with no outcome header, said rather than invented, for: untagged-agent") {
		t.Errorf("packet does not name untagged-agent as a cost with no outcome:\n%s", p)
	}
	if !strings.Contains(p, "tagged-agent") {
		t.Errorf("packet does not carry tagged-agent's own per-outcome figure:\n%s", p)
	}
	// The untagged agent must not get an invented per-outcome FIGURE of its
	// own: exactly one data line carries " per outcome (n=" -- the
	// section's own header also contains the bare words "cost per outcome",
	// which is why this checks the figure's own shape rather than counting
	// the phrase.
	if n := strings.Count(p, " per outcome (n="); n != 1 {
		t.Errorf("packet carries %d per-outcome figures, want exactly 1 (tagged-agent's own): "+
			"a second would mean one was invented for the untagged agent:\n%s", n, p)
	}
}

// TestAISpendSectionWithOneCallThisMonth is the boundary named in section
// 4: a month with one call. No "and N more" line, since there is nothing
// left over to cut.
func TestAISpendSectionWithOneCallThisMonth(t *testing.T) {
	db := deliverTestDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	plantAICallRow(t, db, plantedAIRow{Agent: "solo-agent", Model: "claude-haiku-4-5",
		Day: "2026-09-05", Micros: 1_000_000, Outcome: "done"})

	p := aiSpendSection(db)
	if p == "" {
		t.Fatal("aiSpendSection is empty with one real call this month")
	}
	if !strings.Contains(p, "1 calls") {
		t.Errorf("packet does not count the single call:\n%s", p)
	}
	if strings.Contains(p, "and ") && strings.Contains(p, " more") {
		t.Errorf("a single agent and a single model both got an \"and N more\" line:\n%s", p)
	}
}

// TestAISpendSectionCapsAtTenWithAndNMore is the other boundary section 4
// names: twelve agents, only ten shown, "and 2 more".
func TestAISpendSectionCapsAtTenWithAndNMore(t *testing.T) {
	db := deliverTestDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		plantAICallRow(t, db, plantedAIRow{
			Agent: "agent-" + strconv.Itoa(i), Model: "claude-haiku-4-5",
			Day: "2026-09-05", Micros: int64(1_000_000 + i*1_000), Outcome: "done",
		})
	}
	p := aiSpendSection(db)
	if !strings.Contains(p, "and 2 more") {
		t.Errorf("packet does not cut the agent list at ten with \"and 2 more\":\n%s", p)
	}
}

// TestAISpendSectionStaysBoundedWithAHugeOutcomeValue is the hostile case
// section 4 names: a 1 MB x_outcome. Neither section ever prints the raw
// outcome text (only whether one exists, and its count), so this stays
// small regardless of how large the tagged value is -- proven directly
// rather than assumed.
func TestAISpendSectionStaysBoundedWithAHugeOutcomeValue(t *testing.T) {
	db := deliverTestDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("x", 1_000_000)
	plantAICallRow(t, db, plantedAIRow{Agent: "huge-outcome-agent", Model: "claude-haiku-4-5",
		Day: "2026-09-05", Micros: 1_000_000, Outcome: huge})

	spend := aiSpendSection(db)
	if len(spend) > 4096 {
		t.Errorf("aiSpendSection is %d bytes with a 1 MB outcome tagged, want it bounded "+
			"well under that: the raw outcome text must never be printed", len(spend))
	}
	econ := unitEconomicsSection(db)
	if len(econ) > 4096 {
		t.Errorf("unitEconomicsSection is %d bytes with a 1 MB outcome tagged, want it "+
			"bounded well under that", len(econ))
	}
	if strings.Contains(econ, huge) || strings.Contains(spend, huge) {
		t.Error("a section printed the raw 1 MB outcome value verbatim")
	}
}

// TestPacketOmitsAIDeskSectionsWithoutTheSkill is the gate itself: an
// analyst without the AI desk's own skills gets neither section, even
// against a store that carries real AI spend.
func TestPacketOmitsAIDeskSectionsWithoutTheSkill(t *testing.T) {
	db := deliverTestDB(t)
	importFixtureDB(t, db)

	task := crew.Task{ID: 1, Desk: "aws"}
	a := crew.Analyst{Name: "reporter-aws", State: "active", Skills: []string{"exec-reporting"}}
	p := Packet(db, task, a, false)

	if strings.Contains(p, "The AI desk's month") {
		t.Errorf("an analyst without ai-spend's own skills was handed the AI spend section:\n%s", p)
	}
	if strings.Contains(p, "cost per outcome") {
		t.Errorf("an analyst without unit-econ-ai's own skills was handed the unit economics "+
			"section:\n%s", p)
	}
}

// TestAISpendSectionEmptyWithoutRealData is the other half of "additive,
// never misleading": nothing real has ever landed, so the section is
// absent, not a header over nothing.
func TestAISpendSectionEmptyWithoutRealData(t *testing.T) {
	db := deliverTestDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	if s := aiSpendSection(db); s != "" {
		t.Errorf("aiSpendSection = %q, want empty with no real AI spend imported", s)
	}
	if s := unitEconomicsSection(db); s != "" {
		t.Errorf("unitEconomicsSection = %q, want empty with no real AI spend imported", s)
	}
}

// TestAISpendAndUnitEconomicsYieldBeforeMemoryUnderTheCap extends invariant
// 30: the AI desk's own two sections sit in the SAME tier as reporting and
// forecasting, before ownHistorySection (memory), so under the 12 KiB cap
// memory is still what yields first -- never the AI desk's own figures.
func TestAISpendAndUnitEconomicsYieldBeforeMemoryUnderTheCap(t *testing.T) {
	db := deliverTestDB(t)
	importFixtureDB(t, db)

	const analystName = "ai-spend"
	// A long history of past posted deliverables on the same desk, so
	// ownHistorySection alone would push the packet well past 12 KiB if it
	// were not the thing trimmed first.
	for i := 0; i < 3; i++ {
		taskID := plantMemoryTask(t, db, "ai", "past AI desk work")
		plantPostedArtifact(t, db, taskID, analystName,
			strings.Repeat("history filler text ", 300), "2026-08-2"+strconv.Itoa(i)+"T00:00:00Z")
	}

	task := crew.Task{ID: 999, Desk: "ai"}
	a := crew.Analyst{Name: analystName, State: "active", Skills: []string{"ai-spend-analysis"}}
	p := Packet(db, task, a, false)

	if len(p) > packetMaxBytes {
		t.Fatalf("packet is %d bytes, want at most the %d byte cap", len(p), packetMaxBytes)
	}
	if !strings.Contains(p, "The AI desk's month") {
		t.Errorf("the AI spend section yielded to memory under the cap, want the reverse:\n"+
			"first 400 bytes: %s", firstN(p, 400))
	}
}

// ------------------------------------------------------------------ helpers

// importFixtureDB reads internal/connectors/testdata/tokenfuse-focus-2026-09-02.csv
// (the same fixture connectors' and finops' own tests use) into db.
func importFixtureDB(t *testing.T, db *sql.DB) {
	t.Helper()
	src := filepath.Join("..", "connectors", "testdata", "tokenfuse-focus-2026-09-02.csv")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "focus.csv"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := connectors.Save(db, "tokenfuse-focus", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := connectors.Import(db, "tokenfuse-focus", false, connectors.ImportOptions{}); err != nil {
		t.Fatal(err)
	}
}

type plantedAIRow struct {
	Agent, Model, Day, Outcome string
	Micros                     int64
}

var plantedAIDeskRowNo int64

// plantAICallRow inserts one row directly into ai_calls, bypassing the CSV
// reader: these tests are about the packet built over ai_calls, not about
// the reader itself (already proven in internal/connectors), so this
// builds only the rows a scenario needs -- the same shape plantAnomaly and
// plantDriver in packet_test.go already use for their own tables.
//
// It also writes a matching charges row, the way deriveCharges does inside
// the SAME transaction the real reader runs in: aiSpendSection and
// unitEconomicsSection both find their month through
// finops.LatestRealAIMonth, which reads charges.provenance, not ai_calls
// directly (the AI page in internal/web/practice.go already establishes
// that as the one way this console decides which month is "real"). A test
// that planted ai_calls alone, the way an earlier draft of this file did,
// found every section empty -- not because the section was wrong, but
// because the fixture broke an invariant the real reader always holds.
func plantAICallRow(t *testing.T, db *sql.DB, row plantedAIRow) {
	t.Helper()
	var outcome any
	if row.Outcome != "" {
		outcome = row.Outcome
	}
	plantedAIDeskRowNo++
	if _, err := db.Exec(`INSERT INTO ai_calls
		(file_sha256, row_no, ts, day, team, agent, run_id, parent_run_id,
		 provider, model, tokens_in, tokens_out, billed_microusd, blocked, basis,
		 outcome, tool_calls)
		VALUES ('planted-test',?,?,?,NULL,?,NULL,NULL,'Anthropic',?,0,0,?,0,'settled',?,NULL)`,
		plantedAIDeskRowNo, row.Day+"T00:00:00Z", row.Day, row.Agent, row.Model, row.Micros, outcome); err != nil {
		t.Fatal(err)
	}
	cents := (row.Micros + 5_000) / 10_000 // Micros.Cents(), half away from zero, positive-only here
	if _, err := db.Exec(`INSERT INTO charges
		(source, day, service, team, category, billed_cents, quantity, unit, meter, model, provenance)
		VALUES ('ai',?,'Anthropic API',NULL,'Usage',?,0,'tokens',?,?,'planted-test')`,
		row.Day, cents, row.Model, row.Model); err != nil {
		t.Fatal(err)
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

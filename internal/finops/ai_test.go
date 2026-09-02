package finops_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// bareDB is a store with no generated estate at all, the shape a fresh
// install has before estate.Seed ever runs against it: the tests that import
// the real fixture need this rather than seeded(), because seeded() already
// wrote generated charges and the reader's own refusal 1 would then require
// -replace-generated before anything of this package's business is reached.
func bareDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// importFixture reads internal/connectors/testdata/tokenfuse-focus-2026-09-02.csv
// (the same fixture the connectors package's own red-first tests use; see
// its provenance comment there) into db and returns the month it landed on.
// replaceGenerated is passed straight to ImportOptions, for a db (such as
// kpiDB's) that already carries the generated estate.
func importFixture(t *testing.T, db *sql.DB, replaceGenerated bool) string {
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
	if _, err := connectors.Import(db, "tokenfuse-focus", false,
		connectors.ImportOptions{ReplaceGenerated: replaceGenerated}); err != nil {
		t.Fatal(err)
	}
	return "2026-09"
}

func TestAIUnitsReadsRealRowsAndPicksActionsSource(t *testing.T) {
	db := bareDB(t)
	month := importFixture(t, db, false)

	units, hasOutcomes, err := finops.AIUnits(db, month)
	if err != nil {
		t.Fatal(err)
	}
	if !hasOutcomes {
		t.Error("hasOutcomes = false, but two of the fixture's rows carry x_outcome")
	}
	if len(units) != 3 {
		t.Fatalf("AIUnits returned %d rows, want 3 (haiku, sonnet, opus): %+v", len(units), units)
	}
	want := map[string]int{
		"claude-haiku-4-5": 1, // 2 calls, one tagged case_resolved
		// sonnet's single call carries no outcome
		"claude-opus-4-5": 1, // the non-blocked call carries escalated
	}
	got := map[string]int{}
	for _, u := range units {
		if u.Team != "" {
			t.Errorf("unit for %s has team %q, want empty: x_unit is blank in this fixture", u.Model, u.Team)
		}
		got[u.Model] = u.Actions
	}
	for model, n := range want {
		if got[model] != n {
			t.Errorf("%s Actions = %d, want %d (from tagged outcomes, not the fixed ratio)", model, got[model], n)
		}
	}
	if got["claude-sonnet-4-5"] != 0 {
		t.Errorf("sonnet Actions = %d, want 0: its one call carries no outcome", got["claude-sonnet-4-5"])
	}
}

func TestAIUnitsFallsBackToTheFixedRatioWithNoOutcomes(t *testing.T) {
	db := bareDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	// A month with no tagged outcomes at all: world.AIUnits()'s own fixed
	// ratio (tokens / 4800) is what world.AIUnits() (the generated path) uses,
	// and finops.AIUnits must fall back to the same arithmetic when nothing
	// in ai_calls for the month is tagged.
	if _, err := db.Exec(`INSERT INTO charges
		(source, day, service, team, category, billed_cents, quantity, unit, meter, model, provenance)
		VALUES ('ai','2026-05-01','Anthropic API',NULL,'Usage',500,4800,'tokens','claude-haiku-4-5','claude-haiku-4-5','tokenfuse-focus')`); err != nil {
		t.Fatal(err)
	}
	units, hasOutcomes, err := finops.AIUnits(db, "2026-05")
	if err != nil {
		t.Fatal(err)
	}
	if hasOutcomes {
		t.Error("hasOutcomes = true with nothing tagged for the month")
	}
	if len(units) != 1 {
		t.Fatalf("units = %+v, want 1 row", units)
	}
	if units[0].Actions != 1 { // 4800 tokens / 4800
		t.Errorf("Actions = %d, want 1 from the fixed ratio", units[0].Actions)
	}
}

// TestAIUnitsIgnoresGeneratedRows is a regression test for a bug the parity
// gate found: comparing a fresh install against itself (nothing ever
// imported on either side) turned up ONE difference, /ai saying "read from
// a connector" instead of "across every model", because AIUnits's query
// read source='ai' AND unit='tokens' charges without also requiring
// provenance IS NOT NULL, and the generated world's own AI rows match that
// filter just as well as a connector's real ones do.
func TestAIUnitsIgnoresGeneratedRows(t *testing.T) {
	db := seeded(t) // estate.Seed only: nothing real has ever been imported
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	month := world.LastDay[:7]

	units, _, err := finops.AIUnits(db, month)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 0 {
		t.Errorf("AIUnits returned %d rows from a store where nothing was ever "+
			"imported, want 0: %+v", len(units), units)
	}
}

func TestAIByAgentCountsCallsTokensCostAndBlocked(t *testing.T) {
	db := bareDB(t)
	month := importFixture(t, db, false)

	rows, err := finops.AIByAgent(db, month)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("AIByAgent returned %d rows, want 3: %+v", len(rows), rows)
	}
	byAgent := map[string]finops.AgentAIRow{}
	for _, r := range rows {
		byAgent[r.Agent] = r
	}
	// triage-aws made two $0.0035 haiku calls: 7000 micros, under a cent, so
	// this is exactly the figure that must NOT print as $0.00 -- the whole
	// reason Cost is money.Micros here rather than money.Cents.
	triage := byAgent["agent://taipanbox.dev/costcrew/triage-aws"]
	if triage.Calls != 2 || triage.BlockedCalls != 0 {
		t.Errorf("triage-aws = %+v, want 2 calls, 0 blocked", triage)
	}
	if triage.Cost != 7_000 {
		t.Errorf("triage-aws cost = %d micros, want 7000 ($0.0035 x 2)", triage.Cost)
	}
	if s := triage.Cost.String(); s != "0.0070" {
		t.Errorf("triage-aws cost.String() = %q, want \"0.0070\" (under a cent: four decimals)", s)
	}
	forecaster := byAgent["agent://taipanbox.dev/costcrew/forecaster"]
	if forecaster.Calls != 2 || forecaster.BlockedCalls != 0 {
		t.Errorf("forecaster = %+v, want 2 calls, 0 blocked", forecaster)
	}
	if forecaster.Cost != 28_000 { // $0.0105 (sonnet) + $0.0175 (opus)
		t.Errorf("forecaster cost = %d micros, want 28000", forecaster.Cost)
	}
	deep := byAgent["agent://taipanbox.dev/costcrew/deep-analysis"]
	if deep.Calls != 1 || deep.BlockedCalls != 1 {
		t.Errorf("deep-analysis = %+v, want 1 call, 1 blocked", deep)
	}
	if deep.Cost != 0 {
		t.Errorf("deep-analysis cost = %s, want 0: the blocked call never billed anything", deep.Cost)
	}
}

// TestMixedMoneyNoteHoldsBothDirections is invariant 20 extended to charges.
// A store with only real rows, or only generated ones, says nothing extra;
// a store carrying both (which, with refusal 1 in the reader holding, means
// a store built before this step) says so, by row count rather than by
// amount, because a real row can legitimately sum to zero cents and must not
// be hidden by a check that only looks at the total.
func TestMixedMoneyNoteHoldsBothDirections(t *testing.T) {
	t.Run("only real, says nothing", func(t *testing.T) {
		db := bareDB(t)
		month := importFixture(t, db, false)
		note, err := finops.MixedMoneyNote(db, "ai", month)
		if err != nil {
			t.Fatal(err)
		}
		if note != "" {
			t.Errorf("note = %q, want empty: this store never held generated AI charges", note)
		}
	})

	t.Run("only generated, says nothing", func(t *testing.T) {
		db := seeded(t)
		month := aMonth(t, db)
		note, err := finops.MixedMoneyNote(db, "ai", month)
		if err != nil {
			t.Fatal(err)
		}
		if note != "" {
			t.Errorf("note = %q, want empty: nothing real has been imported", note)
		}
	})

	t.Run("both present, says so", func(t *testing.T) {
		db := seeded(t)
		month := aMonth(t, db)
		// A real row landed in the SAME store, on the SAME month, without
		// refusal 1's wipe: the store-before-this-step case named above,
		// constructed directly because refusal 1 makes it otherwise
		// unreachable through the reader.
		if _, err := db.Exec(`INSERT INTO charges
			(source, day, service, team, category, billed_cents, quantity, unit, meter, model, provenance)
			VALUES ('ai',?,'Anthropic API',NULL,'Usage',1,1500,'tokens','claude-haiku-4-5','claude-haiku-4-5','tokenfuse-focus')`,
			month+"-01"); err != nil {
			t.Fatal(err)
		}
		note, err := finops.MixedMoneyNote(db, "ai", month)
		if err != nil {
			t.Fatal(err)
		}
		if note == "" {
			t.Fatal("note is empty with both a generated and a real AI row present this month")
		}
		if !strings.Contains(note, "real spend") || !strings.Contains(note, "generated estate") {
			t.Errorf("note does not describe both kinds: %q", note)
		}
	})
}

func TestAttributionCoverage(t *testing.T) {
	db := bareDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}

	if _, hasData, err := finops.AttributionCoverage(db, "2026-09"); err != nil {
		t.Fatal(err)
	} else if hasData {
		t.Error("hasData = true with no real AI spend imported yet")
	}

	month := importFixture(t, db, false)
	pct, hasData, err := finops.AttributionCoverage(db, month)
	if err != nil {
		t.Fatal(err)
	}
	if !hasData {
		t.Fatal("hasData = false after a real import")
	}
	if pct != 100 {
		t.Errorf("coverage = %.1f%%, want 100: deriveAttribution writes one row for every "+
			"(day,team,service) this reader touches", pct)
	}
}

// TestAgentAttributionKPIBecomesComputedAfterAnImport is section 1's own
// promise: "the KPI agent-attribution... can run on money somebody actually
// spent". Before this step it was always Blocked, unconditionally; a real
// import must turn it into a reporting KPI rather than leave the same
// hard-coded refusal now that there is something to compute.
func TestAgentAttributionKPIBecomesComputedAfterAnImport(t *testing.T) {
	// kpiDB is realmoney_test.go's own helper: seeded() (estate.Seed plus
	// finops.SeedRules) with anomaly.Schema, crew.Schema and a real
	// crew.Seed on top, which is what KPIs() needs to run at all without
	// crashing on a table that exists but has never had a row in it.
	db := kpiDB(t)
	before, err := finops.KPIs(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	var beforeK finops.KPI
	for _, k := range before {
		if k.ID == "agent-attribution" {
			beforeK = k
		}
	}
	if beforeK.Blocked == "" {
		t.Fatal("agent-attribution is not blocked before anything real has been imported")
	}
	if beforeK.Note != "" {
		t.Errorf("agent-attribution carries a Note %q alongside its Blocked refusal; the "+
			"template renders both, so a store with nothing real yet would say both "+
			"\"real spend a connector wrote\" and \"model calls do not carry an agent "+
			"header\" in the same row", beforeK.Note)
	}

	// kpiDB's store already carries the generated estate (through Seed),
	// so this is the store-already-seeded case and needs the flag.
	month := importFixture(t, db, true)
	after, err := finops.KPIs(db, month)
	if err != nil {
		t.Fatal(err)
	}
	var afterK finops.KPI
	for _, k := range after {
		if k.ID == "agent-attribution" {
			afterK = k
		}
	}
	if afterK.Blocked != "" {
		t.Errorf("agent-attribution is still blocked after a real import: %q", afterK.Blocked)
	}
	if !afterK.HasVal || afterK.Value != "100" {
		t.Errorf("agent-attribution = %+v, want HasVal and Value 100", afterK)
	}
	if !afterK.Meets {
		t.Error("agent-attribution does not meet its own >= 90 target at 100%")
	}
}

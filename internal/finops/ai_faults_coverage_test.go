package finops_test

import (
	"database/sql"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

// TestLatestRealAIMonthReadsOnlyImportedSpend: the month of the newest
// imported token charge is named; a generated row (no provenance) is not
// evidence that a connector ever ran.
func TestLatestRealAIMonthReadsOnlyImportedSpend(t *testing.T) {
	db := covSchemaDB(t)
	month, ok, err := finops.LatestRealAIMonth(db)
	if err != nil || ok || month != "" {
		t.Fatalf("empty store named %q, %v, %v; want no month", month, ok, err)
	}
	covExec(t, db, `INSERT INTO charges (source, day, service, team, category, billed_cents, unit, model, provenance)
		VALUES ('ai','2026-02-14','tokens','team-a','Usage',900,'tokens','m1','connector'),
		       ('ai','2026-03-02','tokens','team-a','Usage',400,'tokens','m1',NULL)`)
	month, ok, err = finops.LatestRealAIMonth(db)
	if err != nil {
		t.Fatalf("latest real month: %v", err)
	}
	if !ok || month != "2026-02" {
		t.Errorf("latest real month %q, %v; want 2026-02, true (the March row is generated)", month, ok)
	}
}

func TestAIUnitsOrdersTeamsAndModelsTheSameEveryCall(t *testing.T) {
	db := covSchemaDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	covExec(t, db, `INSERT INTO charges (source, day, service, team, category, billed_cents, unit, quantity, model, provenance)
		VALUES ('ai','2026-03-03','tokens','team-b','Usage',300,'tokens',10,'m1','connector'),
		       ('ai','2026-03-04','tokens','team-a','Usage',200,'tokens',5,'m2','connector')`)
	units, _, err := finops.AIUnits(db, "2026-03")
	if err != nil {
		t.Fatalf("ai units: %v", err)
	}
	if len(units) != 2 {
		t.Fatalf("got %d units, want one per team", len(units))
	}
	if units[0].Team != "team-a" || units[1].Team != "team-b" {
		t.Errorf("units ordered %s, %s; want team-a before team-b", units[0].Team, units[1].Team)
	}
}

// A charge row with no model cannot be read into a unit. The read refuses,
// rather than making up a model name for it.
func TestAIUnitsRefusesAChargeWithNoModel(t *testing.T) {
	db := covSchemaDB(t)
	covExec(t, db, `INSERT INTO charges (source, day, service, team, category, billed_cents, unit, quantity, model, provenance)
		VALUES ('ai','2026-03-03','tokens','team-a','Usage',300,'tokens',10,NULL,'connector')`)
	if _, _, err := finops.AIUnits(db, "2026-03"); err == nil {
		t.Fatal("a charge with no model was read into a unit instead of refused")
	}
}

func TestAIUnitsSurfacesAFailingCallTable(t *testing.T) {
	db := covSchemaDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	covExec(t, db, `DROP TABLE ai_calls`)
	if _, _, err := finops.AIUnits(db, "2026-03"); err == nil {
		t.Fatal("ai units with the call table gone returned no error")
	}
}

// A tagged-call count that reads but whose per-team count does not: the read
// refuses as a whole rather than printing the actions without the team split.
func TestAIUnitsRefusesWhenTheOutcomeCountCannotBeRead(t *testing.T) {
	db := covSchemaDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	plantAICall(t, db, aiCallRow{Day: "2026-03-05", Agent: "agent://a", Model: "m1", Micros: 100, Outcome: "done"})
	covExec(t, db, `ALTER TABLE ai_calls RENAME COLUMN team TO team_renamed`)
	if _, _, err := finops.AIUnits(db, "2026-03"); err == nil {
		t.Fatal("ai units with the per-team outcome count unreadable returned no error")
	}
}

func TestAIByAgentRefusesAStoredMicrousdThatIsNotWhole(t *testing.T) {
	db := covSchemaDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	plantAICall(t, db, aiCallRow{Day: "2026-03-05", Agent: "agent://a", Model: "m1", Micros: 100})
	covExec(t, db, `UPDATE ai_calls SET billed_microusd = 1.5`)
	if _, err := finops.AIByAgent(db, "2026-03"); err == nil {
		t.Fatal("a fractional micro-dollar amount was summed instead of refused")
	}
}

// The agent's blocked calls by reason are a second read. If that read fails,
// the agent table does not print with the blocked column silently empty.
func TestAIByAgentRefusesWhenTheBlockReasonsCannotBeRead(t *testing.T) {
	db := covSchemaDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	plantAICall(t, db, aiCallRow{Day: "2026-03-05", Agent: "agent://a", Model: "m1", Micros: 100, Blocked: true})
	covExec(t, db, `ALTER TABLE ai_calls RENAME COLUMN block_reason TO block_reason_x`)
	if _, err := finops.AIByAgent(db, "2026-03"); err == nil {
		t.Fatal("agents with the block reasons unreadable returned no error")
	}
}

func TestAIReadsSurfaceAMissingCallTable(t *testing.T) {
	cases := map[string]func(db *sql.DB) error{
		"BlockedByReason":      func(db *sql.DB) error { _, err := finops.BlockedByReason(db, "2026-03"); return err },
		"AIByModel":            func(db *sql.DB) error { _, err := finops.AIByModel(db, "2026-03"); return err },
		"OutcomeCountsByAgent": func(db *sql.DB) error { _, err := finops.OutcomeCountsByAgent(db, "2026-03"); return err },
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			db := covSchemaDB(t)
			if err := connectors.EnsureFocusSchema(db); err != nil {
				t.Fatal(err)
			}
			covExec(t, db, `DROP TABLE ai_calls`)
			if err := run(db); err == nil {
				t.Fatalf("%s with the call table gone returned no error", name)
			}
		})
	}
}

// CostPerOutcome reads the agents' spend and their outcome counts. A failure
// in either is the failure of the whole figure, not a figure with that part
// quietly treated as zero.
func TestCostPerOutcomeRefusesWhenItsAgentReadFails(t *testing.T) {
	db := covSchemaDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	plantAICall(t, db, aiCallRow{Day: "2026-03-05", Agent: "agent://a", Model: "m1", Micros: 100, Outcome: "done"})
	covExec(t, db, `ALTER TABLE ai_calls RENAME COLUMN tokens_in TO tokens_in_renamed`)
	if _, _, _, _, err := finops.CostPerOutcome(db, "2026-03"); err == nil {
		t.Fatal("cost per outcome with the agent read broken returned no error")
	}
}

func TestCostPerOutcomeRefusesWhenItsOutcomeCountFails(t *testing.T) {
	db := covSchemaDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	plantAICall(t, db, aiCallRow{Day: "2026-03-05", Agent: "agent://a", Model: "m1", Micros: 100, Outcome: "done"})
	covExec(t, db, `ALTER TABLE ai_calls RENAME COLUMN outcome TO outcome_renamed`)
	if _, _, _, _, err := finops.CostPerOutcome(db, "2026-03"); err == nil {
		t.Fatal("cost per outcome with the outcome count broken returned no error")
	}
}

package finops_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/finops"
)

// covNopRecorder satisfies finops.Recorder for calls that take one only so
// the signature is complete. Nothing here inspects what it was given.
type covNopRecorder struct{}

func (covNopRecorder) Emit(string, string, string, map[string]any, []string) error { return nil }

// covClosedStore is a store whose handle has been closed. Every statement
// on it fails with the driver's own "database is closed" error, so each
// reader's FIRST failing statement is exercised without any table being
// broken by hand. The error must come back to the caller: a reader that
// swallowed it would report an empty result as though the store said so.
func covClosedStore(t *testing.T) *sql.DB {
	t.Helper()
	db := covSchemaDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("closing the store for the test: %v", err)
	}
	return db
}

// TestEveryDBReaderSurfacesAStoreFailure calls each finops function that
// reads or writes the store, on a closed store, and requires the failure to
// reach the caller. The assertion names the store's own error text, so a
// reader that returns nil, or a made-up error, goes red here.
func TestEveryDBReaderSurfacesAStoreFailure(t *testing.T) {
	const period = "2026-03"
	cases := []struct {
		name string
		run  func(db *sql.DB) error
	}{
		{"LatestRealAIMonth", func(db *sql.DB) error { _, _, err := finops.LatestRealAIMonth(db); return err }},
		{"AIUnits", func(db *sql.DB) error { _, _, err := finops.AIUnits(db, period); return err }},
		{"BlockedByReason", func(db *sql.DB) error { _, err := finops.BlockedByReason(db, period); return err }},
		{"ExportMix", func(db *sql.DB) error { _, _, err := finops.ExportMix(db, period); return err }},
		{"AIByAgent", func(db *sql.DB) error { _, err := finops.AIByAgent(db, period); return err }},
		{"MixedMoneyNote", func(db *sql.DB) error { _, err := finops.MixedMoneyNote(db, "ai", period); return err }},
		{"AttributionCoverage", func(db *sql.DB) error { _, _, err := finops.AttributionCoverage(db, period); return err }},
		{"AIByModel", func(db *sql.DB) error { _, err := finops.AIByModel(db, period); return err }},
		{"OutcomeCountsByAgent", func(db *sql.DB) error { _, err := finops.OutcomeCountsByAgent(db, period); return err }},
		{"BasisCounts", func(db *sql.DB) error { _, _, _, err := finops.BasisCounts(db, period); return err }},
		{"CostPerOutcome", func(db *sql.DB) error { _, _, _, _, err := finops.CostPerOutcome(db, period); return err }},
		{"AnnouncedEffects", func(db *sql.DB) error { _, err := finops.AnnouncedEffects(db, period); return err }},
		{"AnnouncedEffectOf", func(db *sql.DB) error { _, _, err := finops.AnnouncedEffectOf(db, "label", "desk"); return err }},
		{"SeedRules", func(db *sql.DB) error { return finops.SeedRules(db) }},
		{"Rules", func(db *sql.DB) error { _, err := finops.Rules(db); return err }},
		{"SetRule", func(db *sql.DB) error { return finops.SetRule(db, 1, finops.Even) }},
		{"Allocate", func(db *sql.DB) error { _, err := finops.Allocate(db, period); return err }},
		{"UnallocatedPots", func(db *sql.DB) error { _, err := finops.UnallocatedPots(db, period); return err }},
		{"Months", func(db *sql.DB) error { _, err := finops.Months(db); return err }},
		{"BudgetsFor", func(db *sql.DB) error { _, err := finops.BudgetsFor(db, "aws", period); return err }},
		{"Close", func(db *sql.DB) error { return finops.Close(db, period, "owner") }},
		{"Reopen", func(db *sql.DB) error { return finops.Reopen(db, period, "why") }},
		{"IsClosed", func(db *sql.DB) error { _, err := finops.IsClosed(db, period); return err }},
		{"FrozenPeriod", func(db *sql.DB) error { _, err := finops.FrozenPeriod(db, period); return err }},
		{"TrueUpFor", func(db *sql.DB) error { _, _, err := finops.TrueUpFor(db, period); return err }},
		{"ClosedPeriods", func(db *sql.DB) error { _, err := finops.ClosedPeriods(db); return err }},
		{"AnomalyClosureDays", func(db *sql.DB) error { _, err := finops.AnomalyClosureDays(db, "desk", period); return err }},
		{"HasRealCommitments", func(db *sql.DB) error { _, err := finops.HasRealCommitments(db); return err }},
		{"RealCommitments", func(db *sql.DB) error { _, err := finops.RealCommitments(db); return err }},
		{"Coverage", func(db *sql.DB) error { _, err := finops.Coverage(db, period); return err }},
		{"CommitmentUtilisation", func(db *sql.DB) error { _, err := finops.CommitmentUtilisation(db, period); return err }},
		{"ExpiringCommitments", func(db *sql.DB) error { _, err := finops.ExpiringCommitments(db, 30, "2026-03-01"); return err }},
		{"BreakEvens", func(db *sql.DB) error { _, err := finops.BreakEvens(db, period); return err }},
		{"AsOfDay", func(db *sql.DB) error { _, err := finops.AsOfDay(db); return err }},
		{"Commitments", func(db *sql.DB) error { _, _, err := finops.Commitments(db); return err }},
		{"DataQuality", func(db *sql.DB) error { _, err := finops.DataQuality(db, "2026-03-15"); return err }},
		{"Project", func(db *sql.DB) error { _, _, err := finops.Project(db, period); return err }},
		{"ProjectAsAt", func(db *sql.DB) error { _, _, err := finops.ProjectAsAt(db, period, 15); return err }},
		{"ProjectWithDrivers", func(db *sql.DB) error { _, _, _, err := finops.ProjectWithDrivers(db, "desk", period); return err }},
		{"Freeze", func(db *sql.DB) error { return finops.Freeze(db, period, "owner") }},
		{"SetForecastBasis", func(db *sql.DB) error { return finops.SetForecastBasis(db, period, "basis") }},
		{"IsFrozen", func(db *sql.DB) error { _, err := finops.IsFrozen(db, period); return err }},
		{"Forecasts", func(db *sql.DB) error { _, err := finops.Forecasts(db, "2026-04"); return err }},
		{"OpenPeriod", func(db *sql.DB) error { _, err := finops.OpenPeriod(db); return err }},
		{"Accuracy", func(db *sql.DB) error { _, _, _, err := finops.Accuracy(db, "2026-04"); return err }},
		{"Missed", func(db *sql.DB) error { _, err := finops.Missed(db, "desk", period, ""); return err }},
		{"LargestMiss", func(db *sql.DB) error { _, _, err := finops.LargestMiss(db, "2026-04"); return err }},
		{"FrozenPeriods", func(db *sql.DB) error { _, err := finops.FrozenPeriods(db); return err }},
		{"InvoiceReconciliation", func(db *sql.DB) error { _, _, _, err := finops.InvoiceReconciliation(db, period); return err }},
		{"Licences", func(db *sql.DB) error { _, err := finops.Licences(db); return err }},
		{"RenewalsWithin", func(db *sql.DB) error { _, err := finops.RenewalsWithin(db, 30, "2026-03-01"); return err }},
		{"KPIs", func(db *sql.DB) error { _, err := finops.KPIs(db, period); return err }},
		{"Executive", func(db *sql.DB) error { _, _, _, err := finops.Executive(db); return err }},
		{"Maturity", func(db *sql.DB) error { _, err := finops.Maturity(db, period); return err }},
		{"Compute", func(db *sql.DB) error { _, err := finops.Compute(db, period); return err }},
		{"FoundMonthly", func(db *sql.DB) error { _, err := finops.FoundMonthly(db); return err }},
		{"Supervise", func(db *sql.DB) error { _, err := finops.Supervise(db, 1, covNopRecorder{}); return err }},
		{"UnitRules", func(db *sql.DB) error { _, err := finops.UnitRules(db); return err }},
		{"Units", func(db *sql.DB) error { _, err := finops.Units(db, period); return err }},
		{"Showback", func(db *sql.DB) error { _, err := finops.Showback(db, period); return err }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := covClosedStore(t)
			err := c.run(db)
			if err == nil {
				t.Fatalf("%s on a closed store returned no error: the failure was swallowed", c.name)
			}
			if !strings.Contains(err.Error(), "database is closed") {
				t.Fatalf("%s on a closed store returned %q, not the store's own failure", c.name, err)
			}
		})
	}
}

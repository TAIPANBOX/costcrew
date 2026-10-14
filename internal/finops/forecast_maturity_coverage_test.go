package finops

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/estate"
)

// The forecasting capability the maturity page reports is read from the
// frozen months and their scores. These tests build those states directly,
// since forecastLevel and forecastEvidence are not exported.

func maturityStore(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "maturity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(estate.SeedSchema + ForecastSchema); err != nil {
		t.Fatal(err)
	}
	return db
}

func maturityCharge(t *testing.T, db *sql.DB, day string, cents int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO charges
		(source, day, service, team, category, billed_cents) VALUES ('aws', ?, 'compute', 'team-a', 'Usage', ?)`,
		day, cents); err != nil {
		t.Fatal(err)
	}
}

func maturityForecast(t *testing.T, db *sql.DB, period string, cents int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO forecasts
		(period, source, forecast_cents, basis, frozen_at, frozen_by)
		VALUES (?, 'aws', ?, 'test basis', '2026-01-10T00:00:00Z', 'tester')`, period, cents); err != nil {
		t.Fatal(err)
	}
}

// A forecast the month's actual matches exactly is trusted: the highest rung.
func TestForecastLevelIsTrustedWhenTheFrozenFigureMatchesTheActual(t *testing.T) {
	db := maturityStore(t)
	maturityCharge(t, db, "2026-02-10", 10000)
	maturityCharge(t, db, "2026-03-02", 500)
	maturityForecast(t, db, "2026-02", 10000)
	if got := forecastLevel(db, "2026-03"); got != 3 {
		t.Errorf("an exact forecast reads level %d, want 3 (trusted)", got)
	}
	want := "1 months frozen, 1 month-desks scored, average error 0.0%."
	if got := forecastEvidence(db, "2026-03"); got != want {
		t.Errorf("evidence %q, want %q", got, want)
	}
}

// A scored forecast outside the trusted band is still a practice, one rung
// lower: a 16.7 percent miss reads as 2.
func TestForecastLevelIsWorkingWhenTheScoredMissIsOutsideTheTrustedBand(t *testing.T) {
	db := maturityStore(t)
	maturityCharge(t, db, "2026-02-10", 10000)
	maturityCharge(t, db, "2026-03-02", 500)
	maturityForecast(t, db, "2026-02", 12000)
	if got := forecastLevel(db, "2026-03"); got != 2 {
		t.Errorf("a 16.7%% miss reads level %d, want 2 (working)", got)
	}
	want := "1 months frozen, 1 month-desks scored, average error 16.7%."
	if got := forecastEvidence(db, "2026-03"); got != want {
		t.Errorf("evidence %q, want %q", got, want)
	}
}

// A frozen month whose own month is still running has nothing scored. The
// practice is frozen but unproven, which is level 1, and the evidence says the
// month has not finished.
func TestForecastLevelIsStartingWhenFrozenButNothingIsScored(t *testing.T) {
	db := maturityStore(t)
	maturityCharge(t, db, "2026-03-02", 500)
	maturityForecast(t, db, "2026-03", 15000)
	if got := forecastLevel(db, "2026-03"); got != 1 {
		t.Errorf("a frozen, unscored forecast reads level %d, want 1", got)
	}
	want := "1 months frozen, none finished yet, so nothing is scored."
	if got := forecastEvidence(db, "2026-03"); got != want {
		t.Errorf("evidence %q, want %q", got, want)
	}
}

// Nothing frozen: no level, and the evidence says no forecast has ever been
// checked, rather than a zero accuracy.
func TestForecastLevelIsNoneWhenNothingHasBeenFrozen(t *testing.T) {
	db := maturityStore(t)
	maturityCharge(t, db, "2026-03-02", 500)
	if got := forecastLevel(db, "2026-03"); got != 0 {
		t.Errorf("nothing frozen reads level %d, want 0", got)
	}
	want := "Nothing has been frozen, so no forecast has ever been held to what happened."
	if got := forecastEvidence(db, "2026-03"); got != want {
		t.Errorf("evidence %q, want %q", got, want)
	}
}

// A store that cannot list its frozen months reads as level 0, not as the
// level of whatever it could still see.
func TestForecastLevelIsZeroOnAStoreThatCannotBeRead(t *testing.T) {
	db := maturityStore(t)
	maturityForecast(t, db, "2026-02", 10000)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if got := forecastLevel(db, "2026-03"); got != 0 {
		t.Errorf("a closed store reads level %d, want 0", got)
	}
}

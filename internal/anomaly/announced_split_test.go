package anomaly_test

// ANNOUNCED-DRIVER-SPEC.md section 11, finding (a), at the level the live run
// hit: one service on one desk, four teams charged for it, and one announced
// saving on the service. The detector judges each team's series on its own, so
// the step must reach each series in its own share, and a named team must move
// on its own series only.
//
// Red against the code before this step: every series takes the whole step, so
// the teams whose spend fell less than the whole step raise a finding each.

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/detect"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/world"

	_ "modernc.org/sqlite"
)

type splitRec struct{}

func (splitRec) Emit(kind, actor, severity string, data map[string]any, onBehalfOf []string) error {
	return nil
}

// splitDB is an empty estate with the anomaly schema and the drivers columns.
func splitDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{estate.SeedSchema, anomaly.Schema} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := estate.EnsureDriverExpectedColumn(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// splitCharge writes days of one team's spend on one service, a day at a time.
func splitCharge(t *testing.T, db *sql.DB, desk, team, service, first string, days int, cents int64) {
	t.Helper()
	start, err := time.Parse("2006-01-02", first)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < days; i++ {
		day := start.AddDate(0, 0, i).Format("2006-01-02")
		if _, err := db.Exec(`INSERT INTO charges
			(source, day, service, team, category, billed_cents) VALUES (?,?,?,?,'Usage',?)`,
			desk, day, service, team, cents); err != nil {
			t.Fatal(err)
		}
	}
}

// splitAnnounce registers an announced saving as an owner's answer would.
func splitAnnounce(t *testing.T, db *sql.DB, desk, service, team, start, end string, daily int64, label string) {
	t.Helper()
	v := daily
	if err := estate.InsertDriver(db, world.Driver{Start: start, End: end, Scope: service, Label: label,
		Kind: "announced", Source: desk, Expected: &v, Team: team}); err != nil {
		t.Fatal(err)
	}
}

func splitRun(t *testing.T, db *sql.DB) int {
	t.Helper()
	found, _, err := anomaly.Run(db, time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC), detect.Default(), splitRec{})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// The live shape: four teams with unequal, flat spend before 1 August. The
// service-level saving of 300.00 a day lands in proportion, so each team's
// spend falls by its own share: 30.00, 60.00, 90.00 and 120.00 a day.
func TestTheServiceLevelSavingThatLandsProportionallyRaisesNothing(t *testing.T) {
	db := splitDB(t)
	levels := map[string]int64{"data-eng": 100_00, "product-web": 200_00, "sre-platform": 300_00, "ml-platform": 400_00}
	drops := map[string]int64{"data-eng": 30_00, "product-web": 60_00, "sre-platform": 90_00, "ml-platform": 120_00}
	for team, lvl := range levels {
		splitCharge(t, db, "aws", team, "Amazon EC2", "2026-07-01", 31, lvl)
		splitCharge(t, db, "aws", team, "Amazon EC2", "2026-08-01", 30, lvl-drops[team])
	}
	splitAnnounce(t, db, "aws", "Amazon EC2", "", "2026-08-01", "2026-08-30", -300_00, "rightsize the fleet")

	if found := splitRun(t, db); found != 0 {
		t.Fatalf("a saving that landed in proportion raised %d finding(s); each team should judge its own share", found)
	}
}

// A named team takes the whole step on its own series and no other: data-eng
// falls by 300.00 a day, exactly as announced, and the other three stay flat.
// No series may raise a finding.
func TestATeamNamedSavingMovesOnlyThatTeam(t *testing.T) {
	db := splitDB(t)
	splitCharge(t, db, "aws", "data-eng", "Amazon EC2", "2026-07-01", 31, 400_00)
	splitCharge(t, db, "aws", "data-eng", "Amazon EC2", "2026-08-01", 30, 100_00)
	for team, lvl := range map[string]int64{"product-web": 200_00, "sre-platform": 300_00, "ml-platform": 100_00} {
		splitCharge(t, db, "aws", team, "Amazon EC2", "2026-07-01", 61, lvl)
	}
	splitAnnounce(t, db, "aws", "Amazon EC2", "data-eng", "2026-08-01", "2026-08-30", -300_00, "data-eng rightsizing")

	if found := splitRun(t, db); found != 0 {
		t.Fatalf("a saving named to data-eng raised %d finding(s); only data-eng's own series may move", found)
	}
}

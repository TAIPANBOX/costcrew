package finops_test

// ANNOUNCED-DRIVER-SPEC.md section 11: a decision that names a team measures
// that team's spend, and its clean baseline is that team's median over the 28
// days before the start. A decision that names no team keeps the service-level
// figure. The two must disagree here, or the test measures nothing.
//
// Red against the code before this step: AnnouncedEffects reads the service's
// spend for every decision, so the named team's own fall is lost in the other
// team's rise.

import (
	"database/sql"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// annSpendTeam writes one team's usage charge a day for days days from first.
func annSpendTeam(t *testing.T, db *sql.DB, source, service, team, first string, days int, cents int64) {
	t.Helper()
	start, err := time.Parse("2006-01-02", first)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < days; i++ {
		day := start.AddDate(0, 0, i).Format("2006-01-02")
		if _, err := db.Exec(`INSERT INTO charges
			(source, day, service, team, category, billed_cents) VALUES (?,?,?,?,'Usage',?)`,
			source, day, service, team, cents); err != nil {
			t.Fatal(err)
		}
	}
}

// annDriverTeam registers an announced decision, naming a team when team is not
// empty, as an owner's answer would.
func annDriverTeam(t *testing.T, db *sql.DB, source, scope, team, start, end string, expected int64, label string) {
	t.Helper()
	v := expected
	if err := estate.InsertDriver(db, world.Driver{Start: start, End: end, Scope: scope, Label: label,
		Kind: "announced", Source: source, Expected: &v, Team: team}); err != nil {
		t.Fatal(err)
	}
}

func TestAMeasuredTeamDecisionReadsThatTeamsSpend(t *testing.T) {
	db := annFixture(t)
	if _, err := db.Exec(crew.Schema); err != nil {
		t.Fatal(err)
	}
	// data-eng falls by 30.00 a day from the 5th; product-web rises by 60.00 on
	// the same days, so the service as a whole moves by +30.00.
	annSpendTeam(t, db, "aws", "svc-team", "data-eng", "2026-09-07", 24, 100_00)
	annSpendTeam(t, db, "aws", "svc-team", "data-eng", "2026-10-01", 4, 100_00)
	annSpendTeam(t, db, "aws", "svc-team", "data-eng", "2026-10-05", 27, 70_00)
	annSpendTeam(t, db, "aws", "svc-team", "product-web", "2026-09-07", 24, 200_00)
	annSpendTeam(t, db, "aws", "svc-team", "product-web", "2026-10-01", 4, 200_00)
	annSpendTeam(t, db, "aws", "svc-team", "product-web", "2026-10-05", 27, 260_00)
	annDriverTeam(t, db, "aws", "svc-team", "data-eng", "2026-10-05", "2026-10-31", -30_00, "data-eng trim")
	annDriverTeam(t, db, "aws", "svc-team", "", "2026-10-05", "2026-10-31", -30_00, "service trim")

	effects, err := finops.AnnouncedEffects(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]finops.AnnouncedEffect{}
	for _, e := range effects {
		got[e.Label] = e
	}
	if e := got["data-eng trim"]; e.Measured != -30_00 {
		t.Errorf("a decision naming data-eng measured %s, want data-eng's own -30.00 a day", e.Measured)
	}
	if e := got["service trim"]; e.Measured != 30_00 {
		t.Errorf("a decision naming no team measured %s, want the service's +30.00 a day", e.Measured)
	}
}

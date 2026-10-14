package finops_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/finops"
)

// Each reader that composes others stops at the first sub-read that fails, and
// reports that failure. These cases break one store relation at a time on a
// full estate and require the reader to name that relation in its error. A
// reader that went on with a figure missing would report a number that was
// never read.
func TestTheKPIAndReportReadersNameTheStoreRelationThatFailed(t *testing.T) {
	const period = "2026-03"
	cases := []struct {
		name    string
		breakIt func(t *testing.T, db *sql.DB)
		run     func(db *sql.DB) error
		want    string
	}{
		{
			name:    "KPIs needs the allocation rules",
			breakIt: func(t *testing.T, db *sql.DB) { covExec(t, db, `DROP TABLE allocation_rules`) },
			run:     func(db *sql.DB) error { _, err := finops.KPIs(db, period); return err },
			want:    "no such table: allocation_rules",
		},
		{
			name:    "KPIs needs the anomaly register",
			breakIt: func(t *testing.T, db *sql.DB) { covExec(t, db, `DROP TABLE anomalies`) },
			run:     func(db *sql.DB) error { _, err := finops.KPIs(db, period); return err },
			want:    "no such table: anomalies",
		},
		{
			name:    "KPIs needs the board's tasks",
			breakIt: func(t *testing.T, db *sql.DB) { covExec(t, db, `DROP TABLE tasks`) },
			run:     func(db *sql.DB) error { _, err := finops.KPIs(db, period); return err },
			want:    "no such table: tasks",
		},
		{
			name:    "KPIs needs the attribution table",
			breakIt: func(t *testing.T, db *sql.DB) { covExec(t, db, `DROP TABLE attribution`) },
			run:     func(db *sql.DB) error { _, err := finops.KPIs(db, period); return err },
			want:    "no such table: attribution",
		},
		{
			name: "KPIs needs the frozen forecasts to score accuracy",
			breakIt: func(t *testing.T, db *sql.DB) {
				// A view named forecasts over a table that is then dropped: the
				// CREATE TABLE IF NOT EXISTS the readers run first is a no-op, and
				// the first read of the forecasts fails.
				covExec(t, db, `CREATE TABLE fc_src(period TEXT)`)
				covExec(t, db, `CREATE VIEW forecasts AS SELECT * FROM fc_src`)
				covExec(t, db, `DROP TABLE fc_src`)
			},
			run:  func(db *sql.DB) error { _, err := finops.KPIs(db, period); return err },
			want: "no such table: main.fc_src",
		},
		{
			name:    "maturity needs the closed periods",
			breakIt: func(t *testing.T, db *sql.DB) { covExec(t, db, `DROP TABLE chargeback`) },
			run:     func(db *sql.DB) error { _, err := finops.Maturity(db, period); return err },
			want:    "no such table: chargeback",
		},
		{
			name:    "the compute results need the anomaly register",
			breakIt: func(t *testing.T, db *sql.DB) { covExec(t, db, `DROP TABLE anomalies`) },
			run:     func(db *sql.DB) error { _, err := finops.Compute(db, period); return err },
			want:    "no such table: anomalies",
		},
		{
			name:    "the unit lines need the unit rules",
			breakIt: func(t *testing.T, db *sql.DB) { covExec(t, db, `DROP TABLE unit_rules`) },
			run:     func(db *sql.DB) error { _, err := finops.Units(db, period); return err },
			want:    "no such table: unit_rules",
		},
		{
			name:    "the showback needs the unit rules",
			breakIt: func(t *testing.T, db *sql.DB) { covExec(t, db, `DROP TABLE unit_rules`) },
			run:     func(db *sql.DB) error { _, err := finops.Showback(db, period); return err },
			want:    "no such table: unit_rules",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := kpiReadyDB(t)
			c.breakIt(t, db)
			err := c.run(db)
			if err == nil {
				t.Fatal("the reader returned no error with its store relation gone")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not name the missing relation (%q)", err, c.want)
			}
		})
	}
}

// Executive's figures are read for the period and the one before it. A store
// with a single month still answers, with no previous period to compare to.
func TestExecutiveNamesOnlyTheMonthItHasWhenThereIsOne(t *testing.T) {
	db := singleMonthDB(t)
	_, period, previous, err := finops.Executive(db)
	if err != nil {
		t.Fatalf("executive with one month: %v", err)
	}
	if period != "2026-01" || previous != "" {
		t.Errorf("executive period %q, previous %q; want 2026-01 and no previous month", period, previous)
	}
}

func TestExecutiveOnAnEmptyStoreHasNoPeriod(t *testing.T) {
	db := estateSchemaOnlyDB(t)
	figures, period, previous, err := finops.Executive(db)
	if err != nil {
		t.Fatalf("executive on an empty store: %v", err)
	}
	if figures != nil || period != "" || previous != "" {
		t.Errorf("an empty store gave figures %v for period %q, previous %q; want nothing", figures, period, previous)
	}
}

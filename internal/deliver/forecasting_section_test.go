package deliver

// C3-SPEC.md section 2: forecastingSection gains the driver lines, and,
// when a frozen forecast for a closed period exists, the miss: frozen,
// actual, difference, and the drivers in the actual period that were not
// in the projection ("missed").

import (
	"fmt"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// The forecaster's packet names which drivers moved the CURRENT period's own
// projection, and by how much -- C3-SPEC.md section 1.
func TestForecastingSectionShowsDriverLines(t *testing.T) {
	db := deliverTestDB(t)
	for d := 1; d <= 10; d++ {
		day := fmt.Sprintf("2026-05-%02d", d)
		amt := int64(1000)
		if d == 5 {
			amt = 6000
		}
		if _, err := db.Exec(`INSERT INTO charges (source,day,service,team,category,billed_cents)
			VALUES ('aws',?,'Amazon EC2','test-team','Usage',?)`, day, amt); err != nil {
			t.Fatal(err)
		}
	}
	plantDriver(t, db, world.Driver{
		Start: "2026-05-05", End: "2026-05-05", Scope: "*",
		Label: "Test migration", Kind: "one-time", Source: "aws",
	})

	got := forecastingSection(db, "aws")
	if got == "" {
		t.Fatal("forecastingSection returned nothing")
	}
	if !strings.Contains(got, "Test migration") {
		t.Errorf("forecastingSection does not name the driver it applied:\n%s", got)
	}
	if !strings.Contains(got, "one-time") {
		t.Errorf("forecastingSection does not name the driver's kind:\n%s", got)
	}
	if !strings.Contains(got, "60.00") {
		t.Errorf("forecastingSection does not name the driver's own effect:\n%s", got)
	}
	if !strings.Contains(got, "run-rate projection:") {
		t.Errorf("forecastingSection dropped the run-rate line it always carried:\n%s", got)
	}
}

// After a freeze and a period end, the packet carries the miss: frozen,
// actual, difference, and the driver that explains it but that the freeze
// never knew about.
func TestForecastingSectionShowsTheMissWithItsMissedDriver(t *testing.T) {
	db := deliverTestDB(t)
	for d := 1; d <= 20; d++ {
		day := fmt.Sprintf("2026-01-%02d", d)
		if _, err := db.Exec(`INSERT INTO charges (source,day,service,team,category,billed_cents)
			VALUES ('aws',?,'Amazon EC2','test-team','Usage',1000)`, day); err != nil {
			t.Fatal(err)
		}
	}
	// A later month, so OpenPeriod reads February and January is scored.
	if _, err := db.Exec(`INSERT INTO charges (source,day,service,team,category,billed_cents)
		VALUES ('aws','2026-02-01','Amazon EC2','test-team','Usage',100)`); err != nil {
		t.Fatal(err)
	}
	if err := finops.FreezeAsAt(db, "2026-01", "tester", 10); err != nil {
		t.Fatal(err)
	}
	// Registered only after the freeze: exactly the driver a freeze made on
	// day 10 could not have known about.
	plantDriver(t, db, world.Driver{
		Start: "2026-01-15", End: "2026-01-15", Scope: "*",
		Label: "Retroactively found cause", Kind: "one-time", Source: "aws",
	})

	got := forecastingSection(db, "aws")
	if !strings.Contains(got, "miss (") {
		t.Fatalf("forecastingSection does not carry a miss line:\n%s", got)
	}
	if !strings.Contains(got, "frozen") || !strings.Contains(got, "actual") || !strings.Contains(got, "difference") {
		t.Errorf("miss line does not name frozen, actual and difference:\n%s", got)
	}
	if !strings.Contains(got, "Retroactively found cause") {
		t.Errorf("forecastingSection does not name the missed driver:\n%s", got)
	}
}

// The current, still-open period is frozen too (the normal case: history.go
// seeds "including the open one" and the seeded estate does the same),
// so the highest-period freeze is unscored. The miss from the last CLOSED
// period must still show, not disappear because a newer, unscored freeze
// exists. Found running the packet against the real seeded estate
// (tools/packetdump, this PR's own report): onprem's own August packet,
// with August already frozen mid-month, never once showed a miss for any
// earlier month.
func TestForecastingSectionShowsTheMissOfAClosedPeriodEvenWhenTheOpenOneIsAlsoFrozen(t *testing.T) {
	db := deliverTestDB(t)
	for d := 1; d <= 20; d++ {
		day := fmt.Sprintf("2026-01-%02d", d)
		if _, err := db.Exec(`INSERT INTO charges (source,day,service,team,category,billed_cents)
			VALUES ('aws',?,'Amazon EC2','test-team','Usage',1000)`, day); err != nil {
			t.Fatal(err)
		}
	}
	// The open period: some data, so it can be frozen and projected too.
	if _, err := db.Exec(`INSERT INTO charges (source,day,service,team,category,billed_cents)
		VALUES ('aws','2026-02-01','Amazon EC2','test-team','Usage',500)`); err != nil {
		t.Fatal(err)
	}
	if err := finops.FreezeAsAt(db, "2026-01", "tester", 10); err != nil {
		t.Fatal(err)
	}
	// The current month, frozen too -- the normal case a real installation
	// hits every month, not a contrived edge.
	if err := finops.Freeze(db, "2026-02", "tester"); err != nil {
		t.Fatal(err)
	}
	plantDriver(t, db, world.Driver{
		Start: "2026-01-15", End: "2026-01-15", Scope: "*",
		Label: "Retroactively found cause", Kind: "one-time", Source: "aws",
	})

	got := forecastingSection(db, "aws")
	if !strings.Contains(got, "for 2026-02") {
		t.Errorf("forecastingSection does not show the current month's own freeze:\n%s", got)
	}
	if !strings.Contains(got, "miss (") {
		t.Fatalf("forecastingSection dropped the closed month's miss once the open month was also frozen:\n%s", got)
	}
	if !strings.Contains(got, "Retroactively found cause") {
		t.Errorf("forecastingSection does not name the missed driver from the closed month:\n%s", got)
	}
}

// Boundary: a miss of exactly zero is still reported, not silently dropped
// the way an omitted (empty) section would read.
func TestForecastingSectionShowsAMissOfZero(t *testing.T) {
	db := deliverTestDB(t)
	for d := 1; d <= 10; d++ {
		day := fmt.Sprintf("2026-01-%02d", d)
		if _, err := db.Exec(`INSERT INTO charges (source,day,service,team,category,billed_cents)
			VALUES ('aws',?,'Amazon EC2','test-team','Usage',1000)`, day); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO charges (source,day,service,team,category,billed_cents)
		VALUES ('aws','2026-02-01','Amazon EC2','test-team','Usage',100)`); err != nil {
		t.Fatal(err)
	}
	// Frozen BY HAND at the exact actual, so frozen == actual: this
	// boundary is about the zero-difference RENDERING, not about
	// contriving a perfect forecast out of the projection formula.
	if _, err := db.Exec(finops.ForecastSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO forecasts (period,source,forecast_cents,basis,frozen_at,frozen_by)
		VALUES ('2026-01','aws',10000,'a hand-frozen exact figure','2026-01-10T00:00:00Z','tester')`); err != nil {
		t.Fatal(err)
	}

	got := forecastingSection(db, "aws")
	if !strings.Contains(got, "difference 0.00") {
		t.Errorf("a zero miss is not rendered as 0.00 (dropped or malformed instead):\n%s", got)
	}
}

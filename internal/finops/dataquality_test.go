package finops_test

// C9-SPEC.md section 2, "Measurements": finops.DataQuality(db, day), one
// Finding per source, freshness against T.stale and tag coverage /
// unallocated share against T.untagged.
//
// Red first, against main: finops.DataQuality and finops.Finding do not
// exist yet, so this file does not compile -- the same shape apply_test.go's
// own header already documents for a table built from nothing.

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

// dqTestDB is a fresh, empty estate: charges and allocation_rules schemas,
// nothing seeded. DataQuality must measure every one of world.Desks even
// when a source has never been charged at all, which is exactly the shape a
// hand-built empty store gives and estate.Seed's own generated fixture
// (always fully populated) cannot.
func dqTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := seeded(t) // finops_test.go: estate.Seed + finops.SeedRules, real rows on every desk
	return db
}

// emptyDQDB is dqTestDB without estate.Seed's generated rows at all: only
// the schemas, so a test can plant exactly the charges it wants to reason
// about and nothing else muddies a source's freshness or share.
func emptyDQDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS charges(
		source TEXT NOT NULL, day TEXT NOT NULL, service TEXT NOT NULL,
		team TEXT, category TEXT NOT NULL, billed_cents INTEGER NOT NULL,
		quantity REAL, unit TEXT, meter TEXT, model TEXT, provenance TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := finops.SeedRules(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func plantTaggedCharge(t *testing.T, db *sql.DB, source, day, team, category string, cents int64) {
	t.Helper()
	var teamVal any
	if team != "" {
		teamVal = team
	}
	if _, err := db.Exec(`INSERT INTO charges(source, day, service, team, category, billed_cents)
		VALUES (?,?, 'svc', ?, ?, ?)`, source, day, teamVal, category, cents); err != nil {
		t.Fatal(err)
	}
}

func findingFor(t *testing.T, findings []finops.Finding, source string) finops.Finding {
	t.Helper()
	for _, f := range findings {
		if f.Source == source {
			return f
		}
	}
	t.Fatalf("no Finding for source %q among %d findings", source, len(findings))
	return finops.Finding{}
}

func addDays(day string, n int) string {
	tt, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return tt.AddDate(0, 0, n).Format("2006-01-02")
}

func mustStaleDays(t *testing.T) int {
	t.Helper()
	th, ok := crew.ThresholdFor("T.stale")
	if !ok {
		t.Fatal("roles.yaml carries no T.stale threshold")
	}
	// "3": a plain integer.
	n := 0
	for _, r := range th.Value {
		if r < '0' || r > '9' {
			t.Fatalf("T.stale's value %q is not a plain integer", th.Value)
		}
		n = n*10 + int(r-'0')
	}
	if n == 0 {
		t.Fatalf("T.stale's value %q parsed to 0", th.Value)
	}
	return n
}

func mustUntaggedPct(t *testing.T) int64 {
	t.Helper()
	th, ok := crew.ThresholdFor("T.untagged")
	if !ok {
		t.Fatal("roles.yaml carries no T.untagged threshold")
	}
	i := 0
	for i < len(th.Value) && th.Value[i] != '%' {
		i++
	}
	if i == 0 || i == len(th.Value) {
		t.Fatalf("T.untagged's value %q names no leading whole-number percentage", th.Value)
	}
	var n int64
	for _, r := range th.Value[:i] {
		if r < '0' || r > '9' {
			t.Fatalf("T.untagged's value %q names no leading whole-number percentage", th.Value)
		}
		n = n*10 + int64(r-'0')
	}
	return n
}

// ---------------------------------------------------------------- red first

// A source with no charge for T.stale days is reported stale. Boundary:
// exactly T.stale days IS stale, and one day short is NOT.
func TestASourceWithNoChargeForTStaleDaysIsReportedStale(t *testing.T) {
	staleDays := mustStaleDays(t)
	today := "2026-09-10"

	dbStale := emptyDQDB(t)
	plantTaggedCharge(t, dbStale, "aws", addDays(today, -staleDays), "team-x", "Usage", 5000)
	findings, err := finops.DataQuality(dbStale, today)
	if err != nil {
		t.Fatal(err)
	}
	f := findingFor(t, findings, "aws")
	if !f.HasCharge {
		t.Fatal("HasCharge = false; a charge was planted")
	}
	if f.FreshnessDays != staleDays {
		t.Errorf("FreshnessDays = %d, want %d (exactly T.stale)", f.FreshnessDays, staleDays)
	}
	if !f.Stale {
		t.Errorf("a source last charged exactly T.stale (%d) days ago was not reported stale", staleDays)
	}
	if !f.Crossed {
		t.Error("Crossed = false on a stale finding")
	}

	dbFresh := emptyDQDB(t)
	plantTaggedCharge(t, dbFresh, "aws", addDays(today, -(staleDays-1)), "team-x", "Usage", 5000)
	findings2, err := finops.DataQuality(dbFresh, today)
	if err != nil {
		t.Fatal(err)
	}
	f2 := findingFor(t, findings2, "aws")
	if f2.Stale {
		t.Errorf("a source last charged %d days ago (one short of T.stale=%d) was reported stale",
			staleDays-1, staleDays)
	}
}

// A source that has never been charged at all is stale too -- the
// conservative direction, since "no measurement" is not "fresh".
func TestASourceWithNoChargeAtAllIsStale(t *testing.T) {
	db := emptyDQDB(t)
	findings, err := finops.DataQuality(db, "2026-09-10")
	if err != nil {
		t.Fatal(err)
	}
	f := findingFor(t, findings, "aws")
	if f.HasCharge {
		t.Fatal("HasCharge = true; nothing was planted for aws")
	}
	if !f.Stale || !f.Crossed {
		t.Error("a source with no charge on record at all was not reported stale")
	}
}

// Mutant this catches: measure freshness from today instead of the last
// charge. A charge thirty days old must read as thirty days stale, not zero.
func TestFreshnessIsMeasuredFromTheLastChargeNotFromToday(t *testing.T) {
	db := emptyDQDB(t)
	today := "2026-09-10"
	plantTaggedCharge(t, db, "aws", "2026-08-11", "team-x", "Usage", 5000) // 30 days before today

	findings, err := finops.DataQuality(db, today)
	if err != nil {
		t.Fatal(err)
	}
	f := findingFor(t, findings, "aws")
	if f.FreshnessDays != 30 {
		t.Fatalf("FreshnessDays = %d, want 30: freshness is measured from the LAST CHARGE "+
			"(2026-08-11), never from today. A mutant that measured from today instead "+
			"would report 0 regardless of how old the last charge actually is", f.FreshnessDays)
	}
	if !f.Stale {
		t.Error("30 days since the last charge was not reported stale")
	}
}

// Untagged share above T.untagged is reported. A category with a
// proportional allocation rule (Purchase) and a direct-cost team to place it
// against isolates "untagged" (measured BEFORE any rule runs) from
// "unallocated" (what a rule could not place): the pot here gets fully
// redistributed, so only the untagged dimension crosses.
func TestUntaggedShareAboveTUntaggedIsReported(t *testing.T) {
	untaggedPct := mustUntaggedPct(t)
	db := emptyDQDB(t)
	today := "2026-09-10"
	plantTaggedCharge(t, db, "aws", today, "team-x", "Purchase", 8000) // direct, tagged
	plantTaggedCharge(t, db, "aws", today, "", "Purchase", 2000)       // 20% of the month, no team

	findings, err := finops.DataQuality(db, today)
	if err != nil {
		t.Fatal(err)
	}
	f := findingFor(t, findings, "aws")
	if f.UntaggedThresholdPct != untaggedPct {
		t.Errorf("UntaggedThresholdPct = %d, want %d (T.untagged)", f.UntaggedThresholdPct, untaggedPct)
	}
	if !f.UntaggedCrossed {
		t.Errorf("20%% untagged did not cross T.untagged (%d%%): UntaggedPct=%.1f UntaggedCents=%s MonthCents=%s",
			untaggedPct, f.UntaggedPct, f.UntaggedCents, f.MonthCents)
	}
	if !f.Crossed {
		t.Error("Crossed = false on an untagged finding")
	}
	if f.Reason == "" {
		t.Error("a crossed finding carries no reason to put in a halt request")
	}
}

// The unallocated dimension crosses independently of the untagged one: a
// pot with no direct-cost team on the same desk to redistribute it onto is
// left exactly where Allocate finds it ("nothing on this desk to carry
// it"), so the WHOLE month reads as both untagged and unallocated at once,
// and reasonFor names the unallocated crossing specifically.
func TestUnallocatedShareAboveTUntaggedIsReported(t *testing.T) {
	untaggedPct := mustUntaggedPct(t)
	db := emptyDQDB(t)
	today := "2026-09-10"
	// No direct-cost charge on aws at all: the shared pot below has no team
	// to redistribute onto, so it stays Unallocated rather than Placed.
	plantTaggedCharge(t, db, "aws", today, "", "Purchase", 5000)

	findings, err := finops.DataQuality(db, today)
	if err != nil {
		t.Fatal(err)
	}
	f := findingFor(t, findings, "aws")
	if !f.UnallocatedCrossed {
		t.Errorf("100%% unallocated (no team to place it on) did not cross T.untagged (%d%%): %+v",
			untaggedPct, f)
	}
	if !strings.Contains(f.Reason, "unallocated") {
		t.Errorf("the reason does not name the unallocated crossing: %q", f.Reason)
	}
}

// A source under both thresholds is not reported at all.
func TestASourceUnderBothThresholdsIsNotCrossed(t *testing.T) {
	db := emptyDQDB(t)
	today := "2026-09-10"
	plantTaggedCharge(t, db, "aws", today, "team-x", "Purchase", 9900)
	plantTaggedCharge(t, db, "aws", today, "", "Purchase", 100) // 1%, comfortably under T.untagged

	findings, err := finops.DataQuality(db, today)
	if err != nil {
		t.Fatal(err)
	}
	f := findingFor(t, findings, "aws")
	if f.Crossed {
		t.Errorf("a fresh, well-tagged source was reported crossed: %+v", f)
	}
}

// DataQuality measures every one of world.Desks (six), whether or not this
// estate has charged all of them.
func TestDataQualityMeasuresEveryDesk(t *testing.T) {
	db := emptyDQDB(t)
	plantTaggedCharge(t, db, "aws", "2026-09-10", "team-x", "Usage", 1000)
	findings, err := finops.DataQuality(db, "2026-09-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 6 {
		t.Fatalf("DataQuality returned %d findings, want 6 (one per world.Desks): %+v", len(findings), findings)
	}
}

// Against the real seeded estate, for the report's own "packet from the
// seeded estate" requirement: DataQuality must run cleanly and cents-exact
// against real generated data, not only a hand-built fixture.
func TestDataQualityRunsCleanlyAgainstTheSeededEstate(t *testing.T) {
	db := dqTestDB(t)
	findings, err := finops.DataQuality(db, "2026-09-03")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 6 {
		t.Fatalf("findings = %d, want 6", len(findings))
	}
	for _, f := range findings {
		if f.MonthCents < 0 || f.UntaggedCents < 0 || f.UnallocatedCents < 0 {
			t.Errorf("%s: negative figure in %+v", f.Source, f)
		}
		if f.UntaggedCents > f.MonthCents {
			t.Errorf("%s: UntaggedCents %s exceeds MonthCents %s", f.Source, f.UntaggedCents, f.MonthCents)
		}
	}
}

package deliver

// C5-SPEC.md section 2's packet bullet: recommendationsSection for the
// optimizer roles, top ten by saving with the risk sentence a short
// lookback carries. Red first against main: recommendationsSection does
// not exist, so this file does not compile.

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// importFixtureInto copies internal/connectors/testdata/<name> into its own
// fresh folder and imports it through the named connector, so this
// package's own tests are proven against a real fixture import rather than
// rows planted by hand -- the report's own required evidence.
func importFixtureInto(t *testing.T, db *sql.DB, connectorID, fixtureName string) {
	t.Helper()
	src := filepath.Join("..", "connectors", "testdata", fixtureName)
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, fixtureName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := connectors.Save(db, connectorID, map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := connectors.Import(db, connectorID, false, connectors.ImportOptions{}); err != nil {
		t.Fatal(err)
	}
}

// plantRecommendation writes one row directly, for a test about the
// SECTION's own ranking and cap rather than about a reader's parsing:
// saving_cents = 100*i, so resource-11 is the highest saving and
// resource-0 the lowest (a saving of zero, same boundary the golden
// fixture also carries).
func plantRecommendation(t *testing.T, db *sql.DB, desk string, i int) {
	t.Helper()
	id := fmt.Sprintf("%s:resource-%d", desk, i)
	if _, err := db.Exec(`INSERT INTO recommendations
		(id, provider, desk, resource, action, current, recommended,
		 monthly_saving_cents, lookback_days, source_file, imported_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		id, desk, desk, fmt.Sprintf("resource-%d", i), "resize", "big", "small",
		100*i, 30, "planted.csv", "2026-09-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
}

// TestRecommendationsSectionRanksBySavingFromAFixtureImport is the report's
// own required evidence: the optimizer's packet, pasted from a real
// fixture import. Checked as the FULL order of all five rows, not one
// pairwise comparison: the fixture's own Current strings (m5.2xlarge,
// c5.4xlarge, r5.xlarge, t3.large, m5.xlarge) happen to rank i-0a1b before
// i-0b2c whether the comparator reads MonthlySavingCents or Current, so a
// single "184.20 before 92.10" check passes even under the "rank by
// current cost" mutant -- @measured 2026-09-03 by hand-planting exactly
// that mutation and running this test: it still passed. The full sequence
// below does not have that coincidence (the mutant's own order is
// Terminate/r5.xlarge/m5.xlarge/m5.2xlarge/c5.4xlarge, entirely different
// from the saving-descending one), and TestRecommendationsSectionCapsAtTenWithAndNMore
// is the test that actually stands as this invariant's gates-have-teeth.sh
// case, for the same reason: its planted rows all share ONE Current value,
// so the mutant degenerates to sorting by resource name and cuts the two
// highest-saving rows instead of the two lowest.
func TestRecommendationsSectionRanksBySavingFromAFixtureImport(t *testing.T) {
	db := deliverTestDB(t)
	importFixtureInto(t, db, "aws-rightsizing", "aws-rightsizing-2026-09-02.csv")

	got := recommendationsSection(db, "aws")
	if got == "" {
		t.Fatal("recommendationsSection is empty after a real fixture import")
	}
	t.Logf("recommendationsSection(db, \"aws\"):\n%s", got)

	// The fixture's own five rows, in the saving-descending order the
	// section must produce: 184.20, 92.10, 45.60, 12.50, 0.00.
	wantOrder := []string{
		"i-0a1b2c3d4e5f60789", // 184.20
		"i-0b2c3d4e5f607890a", // 92.10
		"i-0d4e5f60789ab123c", // 45.60
		"i-0e5f60789ab123cd4", // 12.50
		"i-0c3d4e5f60789ab12", // 0.00
	}
	last := -1
	for _, resource := range wantOrder {
		at := strings.Index(got, resource)
		if at < 0 {
			t.Fatalf("%s is missing from the section:\n%s", resource, got)
		}
		if at < last {
			t.Errorf("the rows are not in saving-descending order (%s appears out of "+
				"place):\n%s", resource, got)
		}
		last = at
	}
	if !strings.Contains(got, "184.20") {
		t.Errorf("the saving figure itself is not in the section:\n%s", got)
	}
	if !strings.Contains(got, "m5.2xlarge") || !strings.Contains(got, "m5.large") {
		t.Errorf("current and recommended size are not both in the section:\n%s", got)
	}
	if !strings.Contains(got, "Modify") {
		t.Errorf("the action is not in the section:\n%s", got)
	}
	if !strings.Contains(got, "0.00") {
		t.Errorf("the zero-saving row (a boundary, not a reason to hide it) is missing:\n%s", got)
	}
}

// TestRecommendationsSectionFlagsShortLookbackNotLong is the C5-SPEC.md
// boundary and mutant 2's own catch: "a lookback of 14 days flagged, 90
// not." The fixture's own i-0a1b... row carries 14 days and i-0b2c...
// carries 90.
func TestRecommendationsSectionFlagsShortLookbackNotLong(t *testing.T) {
	db := deliverTestDB(t)
	importFixtureInto(t, db, "aws-rightsizing", "aws-rightsizing-2026-09-02.csv")

	got := recommendationsSection(db, "aws")
	risk := "a monthly job looks idle to it"

	fourteenLine := lineContaining(got, "i-0a1b2c3d4e5f60789")
	if !strings.Contains(fourteenLine, risk) {
		t.Errorf("the 14-day lookback row does not carry the risk sentence: %q", fourteenLine)
	}
	if !strings.Contains(fourteenLine, "14") {
		t.Errorf("the 14-day row does not name its own lookback: %q", fourteenLine)
	}

	ninetyLine := lineContaining(got, "i-0b2c3d4e5f607890a")
	if strings.Contains(ninetyLine, risk) {
		t.Errorf("the 90-day lookback row wrongly carries the risk sentence: %q", ninetyLine)
	}
	if !strings.Contains(ninetyLine, "90") {
		t.Errorf("the 90-day row does not name its own lookback: %q", ninetyLine)
	}
}

// TestRecommendationsSectionCapsAtTenWithAndNMore: twelve rows on one
// desk, ranked, only the top ten shown, "and 2 more" trailing.
func TestRecommendationsSectionCapsAtTenWithAndNMore(t *testing.T) {
	db := deliverTestDB(t)
	if err := connectors.EnsureRecommendationsSchema(db); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		plantRecommendation(t, db, "gcp", i)
	}

	got := recommendationsSection(db, "gcp")
	shown := strings.Count(got, "resource-")
	if shown != 10 {
		t.Errorf("recommendationsSection shows %d rows, want the top ten:\n%s", shown, got)
	}
	if !strings.Contains(got, "and 2 more") {
		t.Errorf("the trailing count is missing or wrong:\n%s", got)
	}
	// The two highest-saving rows (11 and 10, planted with the largest
	// saving_cents above) must be among the ten shown; the two lowest (0
	// and 1) must be the ones left out.
	if !strings.Contains(got, "resource-11") || !strings.Contains(got, "resource-10") {
		t.Errorf("the two highest-saving rows are not both shown:\n%s", got)
	}
	if strings.Contains(got, "resource-0:") || strings.Contains(got, " resource-0 ") ||
		strings.HasSuffix(strings.TrimRight(got, "\n"), "resource-0") {
		t.Errorf("the lowest-saving row was shown instead of cut:\n%s", got)
	}
}

// TestRecommendationsSectionEmptyForADeskWithNoImports: the additive rule
// every other packet section already holds -- a section with nothing to
// say is absent, not a header over nothing.
func TestRecommendationsSectionEmptyForADeskWithNoImports(t *testing.T) {
	db := deliverTestDB(t)
	if err := connectors.EnsureRecommendationsSchema(db); err != nil {
		t.Fatal(err)
	}
	if got := recommendationsSection(db, "onprem"); got != "" {
		t.Errorf("recommendationsSection for a desk with nothing imported = %q, want empty", got)
	}
}

// TestPacketIncludesRecommendationsOnlyForRightsizingAnalysis: the packet
// gate every other skill-scoped section already holds
// (HasString(a.Skills, ...)), proven both ways so a role gains the
// section only because its own skill earns it.
func TestPacketIncludesRecommendationsOnlyForRightsizingAnalysis(t *testing.T) {
	db := deliverTestDB(t)
	importFixtureInto(t, db, "aws-rightsizing", "aws-rightsizing-2026-09-02.csv")

	optimizer := crew.Analyst{Name: "optimizer-aws", State: "active", Skills: []string{"rightsizing-analysis"}}
	task := crew.Task{ID: 1, Desk: "aws"}
	got := Packet(db, task, optimizer, false)
	if !strings.Contains(got, "184.20") {
		t.Errorf("an optimizer's packet does not carry the recommendations section:\n%s", got)
	}

	triage := crew.Analyst{Name: "triage-aws", State: "active", Skills: []string{"anomaly-triage"}}
	got2 := Packet(db, task, triage, false)
	if strings.Contains(got2, "184.20") {
		t.Errorf("a triage analyst's packet wrongly carries the recommendations section:\n%s", got2)
	}
}

// --------------------------------------------------------------- helpers

func lineContaining(s, needle string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

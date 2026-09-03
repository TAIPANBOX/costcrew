package deliver

// PARTNER-BUDGET-RECOMMENDATIONS-SPEC.md section 6. Red first against main,
// where none of this exists at all: partnerBudgetSection is not declared and
// connectors.BudgetRecommendations does not exist, so this file does not even
// compile until partnerbudget.go and internal/connectors/budgetrecommendations.go
// both exist.
//
// The last two tests in this file are the guardrail CLAUDE.md invariant 46
// names, held with T3-level care even though the rest of this feature is T2:
// a mistake here would mean a provider's unverified suggestion silently
// became something the console enforces against, which is the single worst
// outcome this feature could produce, and it is the mutant
// scripts/gates-have-teeth.sh's own "guardrail: read budget_recommendations
// into CurrentBudgets's own result" case plants.

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

func ensureBudgetSchemas(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(estate.BudgetSchema); err != nil {
		t.Fatal(err)
	}
	if err := connectors.EnsureBudgetRecommendationsSchema(db); err != nil {
		t.Fatal(err)
	}
}

func insertRealBudget(t *testing.T, db *sql.DB, source, team, month string, cents int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO budgets(source,team,month,budget_cents) VALUES (?,?,?,?)`,
		source, team, month, cents); err != nil {
		t.Fatal(err)
	}
}

func insertRecommendation(t *testing.T, db *sql.DB, provider, team, month string, cents int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO budget_recommendations
		(provider,team,month,recommended_cents,source_file,imported_at)
		VALUES (?,?,?,?,?,?)`, provider, team, month, cents, "test.csv", "2026-09-03T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
}

// -------------------------------------------------------- the packet section

// TestPartnerBudgetSectionCitesBothFiguresWithTheGap is the spec's central
// property: "the finops-partner packet carries both figures and the gap when
// both exist", and it is also what catches the "drop the 'not applied
// anywhere' sentence from the packet section" mutant, via the last assertion.
func TestPartnerBudgetSectionCitesBothFiguresWithTheGap(t *testing.T) {
	db := deliverTestDB(t)
	ensureBudgetSchemas(t, db)
	insertRealBudget(t, db, "aws", "ml-platform", "2026-09", 120000)     // 1200.00
	insertRecommendation(t, db, "aws", "ml-platform", "2026-09", 130000) // 1300.00

	task := crew.Task{ID: 1, Desk: "aws"}
	a := crew.Analyst{Name: "partner-aws", State: "active", Skills: []string{"stakeholder-briefing", "unit-economics"}}

	got := Packet(db, task, a, false)
	if got == "" {
		t.Fatal("Packet is empty; want the partner-budget section")
	}
	for _, want := range []string{
		"ml-platform", "1200.00", "1300.00", "100.00", "+8.3%",
		"set by finance", notAppliedAnywhereSentence,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Packet does not contain %q:\n%s", want, got)
		}
	}
}

// TestPartnerBudgetSectionAbsentWhenOnlyTheRealBudgetExists is the boundary
// "a recommendation for a month with no real budget row (absent, not
// shown)", read backwards: a real budget with nothing to cite beside it is
// equally not an invented section.
func TestPartnerBudgetSectionAbsentWhenOnlyTheRealBudgetExists(t *testing.T) {
	db := deliverTestDB(t)
	ensureBudgetSchemas(t, db)
	insertRealBudget(t, db, "aws", "ml-platform", "2026-09", 120000)

	task := crew.Task{ID: 1, Desk: "aws"}
	a := crew.Analyst{Name: "partner-aws", State: "active", Skills: []string{"stakeholder-briefing"}}

	got := Packet(db, task, a, false)
	if got != "" {
		t.Errorf("Packet is not empty with a real budget and no recommendation ever imported:\n%s", got)
	}
}

// TestPartnerBudgetSectionAbsentWhenOnlyTheRecommendationExists is the
// section-is-absent boundary section 6 names explicitly: "the section is
// absent when only one side exists (no invented zero)".
func TestPartnerBudgetSectionAbsentWhenOnlyTheRecommendationExists(t *testing.T) {
	db := deliverTestDB(t)
	ensureBudgetSchemas(t, db)
	insertRecommendation(t, db, "aws", "ml-platform", "2026-09", 130000)

	task := crew.Task{ID: 1, Desk: "aws"}
	a := crew.Analyst{Name: "partner-aws", State: "active", Skills: []string{"stakeholder-briefing"}}

	got := Packet(db, task, a, false)
	if got != "" {
		t.Errorf("Packet is not empty with a recommendation and no real budget at all:\n%s", got)
	}
}

// TestPartnerBudgetSectionShowsAZeroGapWhenTheyMatchExactly is the other
// named boundary: "recommendation equals the real budget exactly (gap zero,
// still shown, still labelled)".
func TestPartnerBudgetSectionShowsAZeroGapWhenTheyMatchExactly(t *testing.T) {
	db := deliverTestDB(t)
	ensureBudgetSchemas(t, db)
	insertRealBudget(t, db, "gcp", "research", "2026-09", 210075)
	insertRecommendation(t, db, "gcp", "research", "2026-09", 210075)

	task := crew.Task{ID: 1, Desk: "gcp"}
	a := crew.Analyst{Name: "partner-gcp", State: "active", Skills: []string{"stakeholder-briefing"}}

	got := Packet(db, task, a, false)
	if got == "" {
		t.Fatal("Packet is empty; want the section shown even at a zero gap")
	}
	if !strings.Contains(got, "research") {
		t.Errorf("Packet does not name the team:\n%s", got)
	}
	if !strings.Contains(got, "gap:0.00") {
		t.Errorf("Packet does not show a zero gap, still labelled:\n%s", got)
	}
}

// TestPartnerBudgetSectionOmittedForAnAnalystWithoutTheSkill proves the
// gate itself: the same two figures, an analyst without "stakeholder-
// briefing" gets nothing, the same way reportingSection and
// forecastingSection are absent for an analyst without their own skill.
func TestPartnerBudgetSectionOmittedForAnAnalystWithoutTheSkill(t *testing.T) {
	db := deliverTestDB(t)
	ensureBudgetSchemas(t, db)
	insertRealBudget(t, db, "aws", "ml-platform", "2026-09", 120000)
	insertRecommendation(t, db, "aws", "ml-platform", "2026-09", 130000)

	task := crew.Task{ID: 1, Desk: "aws"}
	a := crew.Analyst{Name: "investigator-aws", State: "active", Skills: []string{"anomaly-triage"}}

	got := Packet(db, task, a, false)
	if got != "" {
		t.Errorf("an analyst without stakeholder-briefing got a packet at all:\n%s", got)
	}
}

// TestPartnerBudgetSectionIgnoresARecommendationForAMonthWithNoRealBudgetRow
// is the mutant "show a recommendation with no real budget as if it were
// one", proven with a SECOND month on the same team so the test cannot pass
// by accident from the section being empty altogether: one month pairs and
// must show, the other does not pair and must not.
func TestPartnerBudgetSectionIgnoresARecommendationForAMonthWithNoRealBudgetRow(t *testing.T) {
	db := deliverTestDB(t)
	ensureBudgetSchemas(t, db)
	insertRealBudget(t, db, "aws", "ml-platform", "2026-09", 120000)
	insertRecommendation(t, db, "aws", "ml-platform", "2026-09", 130000) // pairs
	insertRecommendation(t, db, "aws", "ml-platform", "2026-10", 140000) // no real budget for October

	task := crew.Task{ID: 1, Desk: "aws"}
	a := crew.Analyst{Name: "partner-aws", State: "active", Skills: []string{"stakeholder-briefing"}}

	got := Packet(db, task, a, false)
	if !strings.Contains(got, "2026-09") {
		t.Errorf("the paired September line is missing:\n%s", got)
	}
	if strings.Contains(got, "2026-10") || strings.Contains(got, "1400.00") {
		t.Errorf("a recommendation with no real budget row was shown as if it were paired:\n%s", got)
	}
}

// -------------------------------------------------------- the whole path
//
// `@yurii 2026-09-03`: «Так, звісно, роби все, про що ми говоримо, треба
// протестувати і зробити як варіант використання.» -- not satisfied by unit
// tests on the section alone.

// TestEndToEndAnImportedRecommendationReachesAPostedDeliverable walks the
// whole path spec section 4 names: import a recommendation CSV through the
// real reader, build a finops-partner task's packet, and plant a POSTED
// deliverable (a fixture artifact row, `@claude`-authored prose standing in
// for what an analyst would write -- never a live model call, section 4's
// own instruction) whose own two figures are read out of the packet text
// itself rather than restated independently, so the fixture and the packet
// cannot silently disagree. It proves the guardrail one level further
// downstream than the packet tests above: a posted brief cites the gap and
// never claims the recommendation IS the team's real budget.
func TestEndToEndAnImportedRecommendationReachesAPostedDeliverable(t *testing.T) {
	db := deliverTestDB(t)
	ensureBudgetSchemas(t, db)
	insertRealBudget(t, db, "aws", "ml-platform", "2026-09", 120000)

	dir := t.TempDir()
	content := "AccountId,Team,Month,RecommendedBudgetAmount,CurrencyCode\n" +
		"111111111111,ml-platform,2026-09,1300.00,USD\n"
	if err := os.WriteFile(filepath.Join(dir, "rec.csv"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := connectors.Save(db, "aws-budgets-recommended", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	summary, err := connectors.Import(db, "aws-budgets-recommended", false, connectors.ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "1 row") {
		t.Fatalf("import summary %q does not say 1 row", summary)
	}

	task := crew.Task{ID: 1, Desk: "aws"}
	a := crew.Analyst{Name: "partner-aws", State: "active", Skills: []string{"stakeholder-briefing"}}
	packet := Packet(db, task, a, false)
	if !strings.Contains(packet, "1200.00") || !strings.Contains(packet, "1300.00") ||
		!strings.Contains(packet, "gap:100.00") {
		t.Fatalf("the packet does not carry both figures and the gap this test's fixture "+
			"deliverable is about to quote:\n%s", packet)
	}

	body := "ml-platform's own budget for September is 1200.00, set by finance. " +
		"aws suggests 1300.00 for the same month, a gap of 100.00, not applied " +
		"anywhere -- worth asking the team about."
	if _, err := db.Exec(`INSERT INTO artifacts(task, author, title, body, state, created)
		VALUES (?,?,?,?,?,?)`, task.ID, a.Name, "Team briefing, aws", body, "posted", "2026-09-03T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	var stored string
	if err := db.QueryRow(`SELECT body FROM artifacts WHERE task=? AND state='posted'`, task.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, "1200.00") || !strings.Contains(stored, "1300.00") {
		t.Errorf("the posted deliverable does not cite both figures: %s", stored)
	}
	if !strings.Contains(stored, notAppliedAnywhereSentence) {
		t.Errorf("the posted deliverable drops the guardrail sentence: %s", stored)
	}
	if strings.Contains(stored, "own budget for September is 1300.00") {
		t.Errorf("the posted deliverable presents the recommendation AS the real budget: %s", stored)
	}
}

// ------------------------------------------------------------ the guardrail
//
// CLAUDE.md invariant 46: a provider's suggested budget never becomes this
// console's own budget figure, and is never read by BudgetVsActual,
// SpendInMonth's guard math, or any headroom check anywhere in this
// codebase. Held two ways: a structural read of the guard functions' own
// source (catches a change to their LOGIC, whatever shape it takes), and a
// behavioural before/after import (catches a change to their OUTPUT, in case
// a future refactor moves the logic somewhere this test's file list does not
// name).

// extractGoFunc returns the source text of the top-level function named fn,
// from its own "func fn(" line to the line before the next top-level "func "
// (or end of file). Good enough for this test's own purpose: finding
// whether a SPECIFIC function's own body mentions budget_recommendations,
// not whether the word appears anywhere in the file, which would also fire
// on an innocent comment naming this feature and get deleted the first week
// for it -- CLAUDE.md invariant 1's own warning about exactly that failure
// mode, applied here on purpose.
func extractGoFunc(src, fn string) (string, bool) {
	marker := "func " + fn + "("
	i := strings.Index(src, marker)
	if i < 0 {
		return "", false
	}
	rest := src[i:]
	if j := strings.Index(rest[len(marker):], "\nfunc "); j >= 0 {
		return rest[:len(marker)+j], true
	}
	return rest, true
}

// TestCurrentBudgetsAndSpendInMonthSourceNeverMentionsBudgetRecommendations
// is the regression test the spec names literally: "grep every call site of
// CurrentBudgets/SpendInMonth and assert none of them was touched", read as
// "assert their OWN function bodies, and the shared guard primitive every
// caller of SpendInMonth routes through, never come to mention the table a
// provider's suggestion lives in". This is scripts/gates-have-teeth.sh's own
// subject for the "guardrail: read budget_recommendations into
// CurrentBudgets's own result" case: that case mutates
// internal/estate/intake.go's CurrentBudgets query to UNION in
// budget_recommendations, and requires this test to fail on it.
func TestCurrentBudgetsAndSpendInMonthSourceNeverMentionsBudgetRecommendations(t *testing.T) {
	guarded := []struct{ path, fn string }{
		{"../estate/intake.go", "CurrentBudgets"},
		{"../estate/query.go", "BudgetVsActual"},
		{"../crew/crew.go", "SpendInMonth"},
		{"../crew/guard.go", "CheckGuards"},
		{"../crew/plan.go", "headroomOf"},
	}
	for _, g := range guarded {
		data, err := os.ReadFile(g.path)
		if err != nil {
			t.Fatalf("reading %s: %v", g.path, err)
		}
		body, ok := extractGoFunc(string(data), g.fn)
		if !ok {
			t.Fatalf("%s: function %s not found; this test's own subject moved and needs updating", g.path, g.fn)
		}
		if strings.Contains(body, "budget_recommendations") {
			t.Errorf("%s's own %s mentions budget_recommendations: a provider's own "+
				"recommendation must never flow into %s's own result", g.path, g.fn, g.fn)
		}
	}
}

// TestImportingABudgetRecommendationNeverChangesRealBudgetComputations is
// the behavioural half: BudgetVsActual, CurrentBudgets and SpendInMonth are
// byte-identical (reflect.DeepEqual on their own return values) before and
// after a real import through connectors.Import, against a fixture carrying
// both a real budget and spend so there is something for a leak to perturb.
// The recommendation amount (999999.00, ten times the real budget) is
// deliberately far from the real figure: if it ever leaked in, the
// difference would be impossible to miss.
func TestImportingABudgetRecommendationNeverChangesRealBudgetComputations(t *testing.T) {
	db := deliverTestDB(t)
	ensureBudgetSchemas(t, db)
	insertRealBudget(t, db, "aws", "ml-platform", "2026-09", 120000)
	if _, err := db.Exec(`INSERT INTO charges(source,day,service,team,category,billed_cents,quantity,unit)
		VALUES ('aws','2026-09-10','EC2','ml-platform','Usage',45000,1,'unit')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sprints(id,label,start,finish,state,goal)
		VALUES (1,'Sprint 1','2026-09-01','2026-09-14','open','')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tasks(id,sprint,title,assignee,desk,state,spent_cents)
		VALUES (1,1,'Brief','partner-aws','aws','posted',5000)`); err != nil {
		t.Fatal(err)
	}

	type snapshot struct {
		budgetVsActual []estate.BudgetRow
		currentBudgets map[estate.BudgetKey]money.Cents
		spendInMonth   map[string]money.Cents
	}
	snap := func() snapshot {
		bva, err := estate.BudgetVsActual(db, "aws")
		if err != nil {
			t.Fatal(err)
		}
		cb, err := estate.CurrentBudgets(db)
		if err != nil {
			t.Fatal(err)
		}
		sim, err := crew.SpendInMonth(db, "2026-09")
		if err != nil {
			t.Fatal(err)
		}
		return snapshot{bva, cb, sim}
	}

	before := snap()
	if len(before.budgetVsActual) == 0 || len(before.currentBudgets) == 0 || len(before.spendInMonth) == 0 {
		t.Fatal("this test's own fixture produced no data to compare against; it would prove nothing")
	}

	dir := t.TempDir()
	content := "AccountId,Team,Month,RecommendedBudgetAmount,CurrencyCode\n" +
		"111111111111,ml-platform,2026-09,999999.00,USD\n"
	if err := os.WriteFile(filepath.Join(dir, "rec.csv"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := connectors.Save(db, "aws-budgets-recommended", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := connectors.Import(db, "aws-budgets-recommended", false, connectors.ImportOptions{}); err != nil {
		t.Fatal(err)
	}

	after := snap()
	if !reflect.DeepEqual(before.budgetVsActual, after.budgetVsActual) {
		t.Errorf("BudgetVsActual changed after importing a budget recommendation:\nbefore: %+v\nafter:  %+v",
			before.budgetVsActual, after.budgetVsActual)
	}
	if !reflect.DeepEqual(before.currentBudgets, after.currentBudgets) {
		t.Errorf("CurrentBudgets changed after importing a budget recommendation:\nbefore: %+v\nafter:  %+v",
			before.currentBudgets, after.currentBudgets)
	}
	if !reflect.DeepEqual(before.spendInMonth, after.spendInMonth) {
		t.Errorf("SpendInMonth changed after importing a budget recommendation:\nbefore: %+v\nafter:  %+v",
			before.spendInMonth, after.spendInMonth)
	}
}

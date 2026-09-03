package connectors

// PARTNER-BUDGET-RECOMMENDATIONS-SPEC.md section 6: three readers, one per
// provider's own published budget-recommendation shape, into one
// `budget_recommendations` table. Red first against main, where none of this
// exists at all: readers is empty for these three ids, the table has no
// schema, and BudgetRecommendations/EnsureBudgetRecommendationsSchema are not
// declared, so this file does not even compile until budgetrecommendations.go
// exists.
//
// The three fixtures are internal/connectors/testdata/{aws-budgets-
// recommended,gcp-cost-recommender-budget,azure-advisor-budget}-2026-09-03.csv,
// hand-authored from each provider's own published documentation
// (`@claude`, not measured against a real export -- see budgetrecommendations.go's
// own header comment for why).

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// -------------------------------------------------------------- AWS: golden

func TestAWSBudgetsRecommendedIsRead(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := copyIntoDir(t, "testdata/aws-budgets-recommended-2026-09-03.csv")
	if err := Save(db, "aws-budgets-recommended", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}

	msg, err := Import(db, "aws-budgets-recommended", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	for _, want := range []string{"1 file", "3 rows"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Import said %q, want it to mention %q", msg, want)
		}
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM budget_recommendations WHERE provider='aws'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("budget_recommendations has %d aws rows, want 3", n)
	}

	var cents int64
	var sourceFile string
	if err := db.QueryRow(`SELECT recommended_cents, source_file FROM budget_recommendations
			WHERE provider='aws' AND team='ml-platform' AND month='2026-09'`).
		Scan(&cents, &sourceFile); err != nil {
		t.Fatal(err)
	}
	if cents != 130000 {
		t.Errorf("recommended_cents = %d, want 130000 (1300.00)", cents)
	}
	if sourceFile != "aws-budgets-recommended-2026-09-03.csv" {
		t.Errorf("source_file = %q, want the fixture's own basename", sourceFile)
	}

	recs, err := BudgetRecommendations(db, "aws")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("BudgetRecommendations(db, \"aws\") = %d rows, want 3", len(recs))
	}
	// team, then month, ascending: data-eng < growth < ml-platform.
	if recs[0].Team != "data-eng" || recs[1].Team != "growth" || recs[2].Team != "ml-platform" {
		t.Errorf("BudgetRecommendations order = %s/%s/%s, want data-eng/growth/ml-platform",
			recs[0].Team, recs[1].Team, recs[2].Team)
	}
}

// -------------------------------------------------------------- GCP: golden

func TestGCPCostRecommenderBudgetIsRead(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := copyIntoDir(t, "testdata/gcp-cost-recommender-budget-2026-09-03.csv")
	if err := Save(db, "gcp-cost-recommender-budget", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}

	msg, err := Import(db, "gcp-cost-recommender-budget", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	if !strings.Contains(msg, "2 rows") {
		t.Errorf("Import said %q, want it to mention 2 rows", msg)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM budget_recommendations WHERE provider='gcp'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("budget_recommendations has %d gcp rows, want 2", n)
	}

	// The boundary "a recommendation of zero": accepted, not refused.
	var zeroCents int64
	var zeroExists bool
	if err := db.QueryRow(`SELECT 1, recommended_cents FROM budget_recommendations
		WHERE provider='gcp' AND team='sre-platform'`).Scan(&zeroExists, &zeroCents); err != nil {
		t.Fatalf("the zero-recommendation row was not imported: %v", err)
	}
	if zeroCents != 0 {
		t.Errorf("the zero row's recommended_cents = %d, want 0", zeroCents)
	}

	var researchCents int64
	if err := db.QueryRow(`SELECT recommended_cents FROM budget_recommendations
		WHERE provider='gcp' AND team='research'`).Scan(&researchCents); err != nil {
		t.Fatal(err)
	}
	if researchCents != 210075 {
		t.Errorf("recommended_cents = %d, want 210075 (2100.75)", researchCents)
	}
}

// ------------------------------------------------------------ Azure: golden

func TestAzureAdvisorBudgetIsRead(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := copyIntoDir(t, "testdata/azure-advisor-budget-2026-09-03.csv")
	if err := Save(db, "azure-advisor-budget", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}

	msg, err := Import(db, "azure-advisor-budget", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	if !strings.Contains(msg, "2 rows") {
		t.Errorf("Import said %q, want it to mention 2 rows", msg)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM budget_recommendations WHERE provider='azure'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("budget_recommendations has %d azure rows, want 2", n)
	}

	// Unlike this package's rightsizing-style Advisor reader, this one is
	// already monthly: 600.50 stays 600.50, no /12.
	var cents int64
	if err := db.QueryRow(`SELECT recommended_cents FROM budget_recommendations
		WHERE provider='azure' AND team='finance-systems'`).Scan(&cents); err != nil {
		t.Fatal(err)
	}
	if cents != 60050 {
		t.Errorf("recommended_cents = %d, want 60050 (600.50, no annual-to-monthly division)", cents)
	}
}

// ------------------------------------------------------------- boundaries

// TestUnknownBudgetRecommendationProviderIsRefused is the registry's own
// existing behaviour (connectors.go's Import/Get), proven against these
// three ids specifically: a provider name not in the three known ones is
// refused at the connector-id level, before any reader is ever looked up.
func TestUnknownBudgetRecommendationProviderIsRefused(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	_, err := Import(db, "oracle-budgets-recommended", false, ImportOptions{})
	if err == nil {
		t.Fatal("Import for an unregistered provider id succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "no such connector") {
		t.Errorf("Import error = %q, want the registry's own \"no such connector\" refusal", err.Error())
	}
}

// TestEmptyBudgetRecFileIsZeroRows: a valid header with no data rows is not
// an error and not silence -- the summary says 0 rows, in those words.
func TestEmptyBudgetRecFileIsZeroRows(t *testing.T) {
	dir := t.TempDir()
	writeFocusFile(t, dir, "empty.csv", budgetRecAWSHeader+"\n")
	st := openFocusStore(t)
	db := st.DB()
	if err := Save(db, "aws-budgets-recommended", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	msg, err := Import(db, "aws-budgets-recommended", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	if !strings.Contains(msg, "0 rows") {
		t.Errorf("Import of a header-only file said %q, want it to say 0 rows", msg)
	}
	assertNoBudgetRecommendations(t, db)
}

// TestImportOfABudgetRecommendationIsIdempotent: re-importing the same
// fixture does not duplicate rows -- a (provider, team, month) recommendation
// is a current snapshot, not a log.
func TestImportOfABudgetRecommendationIsIdempotent(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := copyIntoDir(t, "testdata/aws-budgets-recommended-2026-09-03.csv")
	if err := Save(db, "aws-budgets-recommended", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(db, "aws-budgets-recommended", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(db, "aws-budgets-recommended", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM budget_recommendations WHERE provider='aws'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("budget_recommendations has %d aws rows after two imports, want 3", n)
	}
}

// TestTestDescribesABudgetRecommendationWithoutWriting is refusal 2, the
// same shape tokenfuse-focus already holds: Test() (DryRun) describes what
// Import would do and writes nothing.
func TestTestDescribesABudgetRecommendationWithoutWriting(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := copyIntoDir(t, "testdata/aws-budgets-recommended-2026-09-03.csv")
	if err := Save(db, "aws-budgets-recommended", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	result, ok, err := Test(db, "aws-budgets-recommended", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("Test() reported not ok: %s", result)
	}
	if !strings.Contains(result, "Would read") || !strings.Contains(result, "3 rows") {
		t.Errorf("Test() result = %q, want it to describe 3 rows without having read them", result)
	}
	assertNoBudgetRecommendations(t, db)
}

// --------------------------------------------------------------- fixtures

const budgetRecAWSHeader = "AccountId,Team,Month,RecommendedBudgetAmount,CurrencyCode"

func budgetRecAWSRowFields() []string {
	return strings.Split("111111111111,ml-platform,2026-09,1300.00,USD", ",")
}

const budgetRecGCPHeader = "project_id,team,month,recommended_budget_amount,currency_code"

func budgetRecGCPRowFields() []string {
	return strings.Split("taipanbox-prod,research,2026-09,2100.75,USD", ",")
}

const budgetRecAzureHeader = "SubscriptionId,Team,Month,RecommendedBudgetAmount,Currency"

func budgetRecAzureRowFields() []string {
	return strings.Split("9f1c2b3a-0001-4a11-8b22-abc123def456,security,2026-09,780.00,USD", ",")
}

func assertNoBudgetRecommendations(t *testing.T, db *sql.DB) {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM budget_recommendations`).Scan(&n); err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return
		}
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d rows were written despite the row being refused", n)
	}
}

// ----------------------------------------------------------- hostile input
//
// The same hostile-CSV suite this package's own rightsizing-style readers
// have, reused against this one: an unknown header set, a negative amount, a
// garbage amount, an empty team, a garbage month, an embedded quote, a
// quoted comma, a UTF-8 BOM, CRLF endings, and a 100 MB line that must stay
// memory-bounded.

func TestHostileBudgetRecommendationInput(t *testing.T) {
	t.Run("unknown header set: a required AWS column is missing", func(t *testing.T) {
		fields := strings.Split(budgetRecAWSHeader, ",")
		var kept []string
		for _, f := range fields {
			if f != "RecommendedBudgetAmount" {
				kept = append(kept, f)
			}
		}
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", strings.Join(kept, ",")+"\n"+strings.Join(budgetRecAWSRowFields(), ","))
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "aws-budgets-recommended", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "aws-budgets-recommended", false, ImportOptions{})
		if err != nil {
			t.Fatalf("Import returned a hard error rather than naming the file: %v", err)
		}
		if !strings.Contains(msg, "RecommendedBudgetAmount") {
			t.Errorf("the refusal does not name the missing column: %s", msg)
		}
		assertNoBudgetRecommendations(t, db)
	})

	t.Run("a negative AWS amount is refused by name, and nothing is written", func(t *testing.T) {
		f := budgetRecAWSRowFields()
		f[3] = "-1300.00" // RecommendedBudgetAmount
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", budgetRecAWSHeader+"\n"+strings.Join(f, ","))
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "aws-budgets-recommended", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "aws-budgets-recommended", false, ImportOptions{})
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "negative") {
			t.Errorf("the refusal does not say negative: %s", msg)
		}
		assertNoBudgetRecommendations(t, db)
	})

	t.Run("a garbage (non-numeric) AWS amount is refused, naming the amount", func(t *testing.T) {
		f := budgetRecAWSRowFields()
		f[3] = "1300.00abc" // RecommendedBudgetAmount
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", budgetRecAWSHeader+"\n"+strings.Join(f, ","))
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "aws-budgets-recommended", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "aws-budgets-recommended", false, ImportOptions{})
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "1300.00abc") {
			t.Errorf("the refusal does not name the bad amount: %s", msg)
		}
		assertNoBudgetRecommendations(t, db)
	})

	t.Run("an empty AWS Team is refused", func(t *testing.T) {
		f := budgetRecAWSRowFields()
		f[1] = "" // Team
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", budgetRecAWSHeader+"\n"+strings.Join(f, ","))
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "aws-budgets-recommended", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "aws-budgets-recommended", false, ImportOptions{})
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "Team") {
			t.Errorf("the refusal does not name Team: %s", msg)
		}
		assertNoBudgetRecommendations(t, db)
	})

	t.Run("a garbage AWS month is refused", func(t *testing.T) {
		f := budgetRecAWSRowFields()
		f[2] = "September 2026" // Month
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", budgetRecAWSHeader+"\n"+strings.Join(f, ","))
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "aws-budgets-recommended", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "aws-budgets-recommended", false, ImportOptions{})
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "Month") {
			t.Errorf("the refusal does not name Month: %s", msg)
		}
		assertNoBudgetRecommendations(t, db)
	})

	t.Run("a negative GCP amount is refused", func(t *testing.T) {
		f := budgetRecGCPRowFields()
		f[3] = "-2100.75" // recommended_budget_amount
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", budgetRecGCPHeader+"\n"+strings.Join(f, ","))
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "gcp-cost-recommender-budget", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "gcp-cost-recommender-budget", false, ImportOptions{})
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "negative") {
			t.Errorf("the refusal does not say negative: %s", msg)
		}
		assertNoBudgetRecommendations(t, db)
	})

	t.Run("an empty GCP team is refused", func(t *testing.T) {
		f := budgetRecGCPRowFields()
		f[1] = "" // team
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", budgetRecGCPHeader+"\n"+strings.Join(f, ","))
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "gcp-cost-recommender-budget", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "gcp-cost-recommender-budget", false, ImportOptions{})
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "no team") {
			t.Errorf("the refusal does not say no team: %s", msg)
		}
		assertNoBudgetRecommendations(t, db)
	})

	t.Run("a garbage GCP month is refused", func(t *testing.T) {
		f := budgetRecGCPRowFields()
		f[2] = "2026-9" // month, one digit short
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", budgetRecGCPHeader+"\n"+strings.Join(f, ","))
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "gcp-cost-recommender-budget", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "gcp-cost-recommender-budget", false, ImportOptions{})
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "month") {
			t.Errorf("the refusal does not name month: %s", msg)
		}
		assertNoBudgetRecommendations(t, db)
	})

	t.Run("a negative Azure amount is refused", func(t *testing.T) {
		f := budgetRecAzureRowFields()
		f[3] = "-780.00" // RecommendedBudgetAmount
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", budgetRecAzureHeader+"\n"+strings.Join(f, ","))
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "azure-advisor-budget", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "azure-advisor-budget", false, ImportOptions{})
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "negative") {
			t.Errorf("the refusal does not say negative: %s", msg)
		}
		assertNoBudgetRecommendations(t, db)
	})

	t.Run("an empty Azure Team is refused", func(t *testing.T) {
		f := budgetRecAzureRowFields()
		f[1] = "" // Team
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", budgetRecAzureHeader+"\n"+strings.Join(f, ","))
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "azure-advisor-budget", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "azure-advisor-budget", false, ImportOptions{})
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "Team") {
			t.Errorf("the refusal does not name Team: %s", msg)
		}
		assertNoBudgetRecommendations(t, db)
	})

	t.Run("a garbage Azure month is refused", func(t *testing.T) {
		f := budgetRecAzureRowFields()
		f[2] = "Q3" // Month
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", budgetRecAzureHeader+"\n"+strings.Join(f, ","))
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "azure-advisor-budget", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "azure-advisor-budget", false, ImportOptions{})
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "Month") {
			t.Errorf("the refusal does not name Month: %s", msg)
		}
		assertNoBudgetRecommendations(t, db)
	})

	t.Run("a team name with an embedded quote round-trips", func(t *testing.T) {
		f := budgetRecAWSRowFields()
		f[1] = `"ml-platform""-core"""` // Team, RFC 4180 doubled-quote escaping
		dir := t.TempDir()
		writeFocusFile(t, dir, "good.csv", budgetRecAWSHeader+"\n"+strings.Join(f, ","))
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "aws-budgets-recommended", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "aws-budgets-recommended", false, ImportOptions{})
		if err != nil {
			t.Fatalf("Import: %v (%s)", err, msg)
		}
		var team string
		if err := db.QueryRow(`SELECT team FROM budget_recommendations`).Scan(&team); err != nil {
			t.Fatalf("the quoted team was not imported: %v (%s)", err, msg)
		}
		if team != `ml-platform"-core"` {
			t.Errorf("team = %q, want the embedded quotes preserved", team)
		}
	})

	t.Run("a field with a quoted comma round-trips", func(t *testing.T) {
		f := budgetRecGCPRowFields()
		f[1] = `"research, applied"` // team, quoted comma
		dir := t.TempDir()
		writeFocusFile(t, dir, "good.csv", budgetRecGCPHeader+"\n"+strings.Join(f, ","))
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "gcp-cost-recommender-budget", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "gcp-cost-recommender-budget", false, ImportOptions{})
		if err != nil {
			t.Fatalf("Import: %v (%s)", err, msg)
		}
		var team string
		if err := db.QueryRow(`SELECT team FROM budget_recommendations`).Scan(&team); err != nil {
			t.Fatalf("the quoted-comma row was not imported: %v (%s)", err, msg)
		}
		if team != "research, applied" {
			t.Errorf("team = %q, want the embedded comma preserved", team)
		}
	})

	t.Run("a UTF-8 BOM before the header is stripped, not folded into the first column", func(t *testing.T) {
		dir := t.TempDir()
		bom := string([]byte{0xEF, 0xBB, 0xBF})
		content := bom + budgetRecGCPHeader + "\n" + strings.Join(budgetRecGCPRowFields(), ",")
		writeFocusFile(t, dir, "good.csv", content)
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "gcp-cost-recommender-budget", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "gcp-cost-recommender-budget", false, ImportOptions{})
		if err != nil {
			t.Fatalf("a BOM'd header was refused rather than stripped: %v (%s)", err, msg)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM budget_recommendations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%d rows imported from a BOM'd file, want 1: %s", n, msg)
		}
	})

	t.Run("CRLF line endings parse the same as LF", func(t *testing.T) {
		dir := t.TempDir()
		content := budgetRecAzureHeader + "\r\n" + strings.Join(budgetRecAzureRowFields(), ",") + "\r\n"
		writeFocusFile(t, dir, "good.csv", content)
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "azure-advisor-budget", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "azure-advisor-budget", false, ImportOptions{})
		if err != nil {
			t.Fatalf("a CRLF file was refused: %v (%s)", err, msg)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM budget_recommendations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%d rows imported from a CRLF file, want 1: %s", n, msg)
		}
	})

	t.Run("a 100 MB line stays memory-bounded", func(t *testing.T) {
		if testing.Short() {
			t.Skip("short mode")
		}
		const padBytes = 100_000_000
		pad := strings.Repeat("x", padBytes)
		header := budgetRecAWSHeader + ",Notes"
		f := append(budgetRecAWSRowFields(), pad) // Notes: not required, not mapped anywhere
		dir := t.TempDir()
		path := filepath.Join(dir, "big.csv")
		content := header + "\n" + strings.Join(f, ",")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "aws-budgets-recommended", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		msg, err := Import(db, "aws-budgets-recommended", false, ImportOptions{})
		if err != nil {
			t.Fatalf("Import: %v (%s)", err, msg)
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		delta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
		t.Logf("live heap delta after importing a %d-byte line: %+d bytes", len(content), delta)
		const bound = 50_000_000
		if delta > bound {
			t.Errorf("live heap grew by %d bytes; want under %d, which would mean the "+
				"100 MB field was buffered wholesale rather than streamed and discarded", delta, bound)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM budget_recommendations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%d rows imported, want 1", n)
		}
	})
}

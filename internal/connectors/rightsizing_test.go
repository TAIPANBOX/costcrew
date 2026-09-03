package connectors

// C5-SPEC.md: three readers, one per provider's own published rightsizing
// export, into one `recommendations` table. Red first against main, where
// none of this exists at all: readers is empty for these three IDs, the
// table has no schema, and Recommendations/EnsureRecommendationsSchema are
// not declared, so this file does not even compile yet.
//
// The three fixtures are internal/connectors/testdata/{aws-rightsizing,
// gcp-recommender,azure-advisor}-2026-09-02.csv, hand-authored from each
// provider's own public column documentation (`@claude`, not measured
// against a real export -- there is no stub server for a cloud console the
// way tokenfuse-focus-2026-09-02.csv has one).

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ------------------------------------------------------------- AWS: golden

func TestAWSRightsizingIsRead(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := copyIntoDir(t, "testdata/aws-rightsizing-2026-09-02.csv")
	if err := Save(db, "aws-rightsizing", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}

	msg, err := Import(db, "aws-rightsizing", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	for _, want := range []string{"1 file", "5 rows"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Import said %q, want it to mention %q", msg, want)
		}
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recommendations WHERE desk='aws'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("recommendations has %d aws rows, want 5", n)
	}

	var provider, action, current, recommended, sourceFile string
	var cents int64
	var lookback int
	if err := db.QueryRow(`SELECT provider, action, current, recommended,
			monthly_saving_cents, lookback_days, source_file
		FROM recommendations WHERE desk='aws' AND resource='i-0a1b2c3d4e5f60789'`).
		Scan(&provider, &action, &current, &recommended, &cents, &lookback, &sourceFile); err != nil {
		t.Fatal(err)
	}
	if provider != "aws" {
		t.Errorf("provider = %q, want aws", provider)
	}
	if action != "Modify" {
		t.Errorf("action = %q, want Modify", action)
	}
	if current != "m5.2xlarge" || recommended != "m5.large" {
		t.Errorf("current/recommended = %q/%q, want m5.2xlarge/m5.large", current, recommended)
	}
	if cents != 18420 {
		t.Errorf("monthly_saving_cents = %d, want 18420 (184.20)", cents)
	}
	if lookback != 14 {
		t.Errorf("lookback_days = %d, want 14", lookback)
	}
	if sourceFile != "aws-rightsizing-2026-09-02.csv" {
		t.Errorf("source_file = %q, want the fixture's own basename", sourceFile)
	}

	// The boundary "a saving of zero": row r5.xlarge->r5.large is accepted,
	// not refused, at exactly 0 cents.
	var zeroCents int64
	var zeroExists bool
	if err := db.QueryRow(`SELECT 1, monthly_saving_cents FROM recommendations
		WHERE resource='i-0c3d4e5f60789ab12'`).Scan(&zeroExists, &zeroCents); err != nil {
		t.Fatalf("the zero-saving row was not imported: %v", err)
	}
	if zeroCents != 0 {
		t.Errorf("the zero-saving row's monthly_saving_cents = %d, want 0", zeroCents)
	}

	// A Terminate row's own "recommended" is empty (there is no smaller
	// instance type to move to), and that must be stored as an empty
	// string, not silently dropped.
	var terminateRecommended string
	if err := db.QueryRow(`SELECT recommended FROM recommendations
		WHERE resource='i-0d4e5f60789ab123c'`).Scan(&terminateRecommended); err != nil {
		t.Fatal(err)
	}
	if terminateRecommended != "" {
		t.Errorf("a Terminate row's recommended = %q, want empty", terminateRecommended)
	}
}

// ------------------------------------------------------------- GCP: golden

func TestGCPRecommenderIsRead(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := copyIntoDir(t, "testdata/gcp-recommender-2026-09-02.csv")
	if err := Save(db, "gcp-recommender", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}

	msg, err := Import(db, "gcp-recommender", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	if !strings.Contains(msg, "3 rows") {
		t.Errorf("Import said %q, want it to mention 3 rows", msg)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recommendations WHERE desk='gcp'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("recommendations has %d gcp rows, want 3", n)
	}

	var action string
	var cents int64
	var lookback int
	if err := db.QueryRow(`SELECT action, monthly_saving_cents, lookback_days FROM recommendations
		WHERE resource='projects/taipanbox-prod/zones/europe-west1-b/instances/vm-batch-9'`).
		Scan(&action, &cents, &lookback); err != nil {
		t.Fatal(err)
	}
	if action != "STOP_VM" {
		t.Errorf("action = %q, want STOP_VM", action)
	}
	if cents != 21075 {
		t.Errorf("monthly_saving_cents = %d, want 21075 (210.75)", cents)
	}
	if lookback != 21 {
		t.Errorf("lookback_days = %d, want 21", lookback)
	}
}

// ----------------------------------------------------------- Azure: golden

func TestAzureAdvisorIsRead(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := copyIntoDir(t, "testdata/azure-advisor-2026-09-02.csv")
	if err := Save(db, "azure-advisor", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}

	msg, err := Import(db, "azure-advisor", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	if !strings.Contains(msg, "2 rows") {
		t.Errorf("Import said %q, want it to mention 2 rows", msg)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recommendations WHERE desk='azure'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("recommendations has %d azure rows, want 2", n)
	}

	// Advisor's own CSV reports POTENTIAL ANNUAL cost savings; this reader
	// must divide by twelve to land in the same monthly-cents unit every
	// other row in this table uses. 2208.00 / 12 = 184.00 exactly.
	var cents int64
	var lookback int
	if err := db.QueryRow(`SELECT monthly_saving_cents, lookback_days FROM recommendations
		WHERE resource LIKE '%vm-triage-eu-1'`).Scan(&cents, &lookback); err != nil {
		t.Fatal(err)
	}
	if cents != 18400 {
		t.Errorf("monthly_saving_cents = %d, want 18400 (2208.00/12 = 184.00)", cents)
	}
	if lookback != 7 {
		t.Errorf("lookback_days = %d, want 7", lookback)
	}
}

// TestAzureAnnualToMonthlyRoundsHalfAwayFromZero is the division on its own,
// with a value that does NOT divide evenly: $100.00 annual / 12 = $8.3333...,
// which must round to 833 cents (half away from zero at the fourth decimal
// and beyond), the same convention money.Parse and money.Bps already use.
func TestAzureAnnualToMonthlyRoundsHalfAwayFromZero(t *testing.T) {
	dir := t.TempDir()
	row := azureRowFields()
	row[6] = "100.00" // PotentialAnnualCostSavings
	writeFocusFile(t, dir, "one.csv", azureHeader+"\n"+strings.Join(row, ","))
	st := openFocusStore(t)
	db := st.DB()
	if err := Save(db, "azure-advisor", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(db, "azure-advisor", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	var cents int64
	if err := db.QueryRow(`SELECT monthly_saving_cents FROM recommendations`).Scan(&cents); err != nil {
		t.Fatal(err)
	}
	if cents != 833 {
		t.Errorf("monthly_saving_cents = %d, want 833 ($100.00/12 = $8.3333..., rounds to 8.33)", cents)
	}
}

// ------------------------------------------------------------- boundaries

// TestEmptyFileIsZeroRows: a valid header with no data rows is not an error
// and not silence -- the summary says 0 rows, in those words.
func TestEmptyFileIsZeroRows(t *testing.T) {
	dir := t.TempDir()
	writeFocusFile(t, dir, "empty.csv", awsHeader+"\n")
	st := openFocusStore(t)
	db := st.DB()
	if err := Save(db, "aws-rightsizing", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	msg, err := Import(db, "aws-rightsizing", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	if !strings.Contains(msg, "0 rows") {
		t.Errorf("Import of a header-only file said %q, want it to say 0 rows", msg)
	}
	assertNoRecommendations(t, db)
}

// TestImportIsIdempotent: re-importing the same fixture does not duplicate
// rows -- a resource's recommendation is a current snapshot, not a log.
func TestImportIsIdempotentForRightsizing(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := copyIntoDir(t, "testdata/aws-rightsizing-2026-09-02.csv")
	if err := Save(db, "aws-rightsizing", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(db, "aws-rightsizing", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(db, "aws-rightsizing", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recommendations WHERE desk='aws'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("recommendations has %d aws rows after two imports, want 5", n)
	}
}

// TestTestDescribesRightsizingWithoutWriting is refusal 2, the same shape
// tokenfuse-focus already holds: Test() (DryRun) describes what Import would
// do and writes nothing.
func TestTestDescribesRightsizingWithoutWriting(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := copyIntoDir(t, "testdata/aws-rightsizing-2026-09-02.csv")
	if err := Save(db, "aws-rightsizing", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	result, ok, err := Test(db, "aws-rightsizing", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("Test() reported not ok: %s", result)
	}
	if !strings.Contains(result, "Would read") || !strings.Contains(result, "5 rows") {
		t.Errorf("Test() result = %q, want it to describe 5 rows without having read them", result)
	}
	assertNoRecommendations(t, db)
}

// TestRecommendationsFiltersByDesk: two providers into one store, and
// Recommendations(db, desk) returns only its own desk's rows.
func TestRecommendationsFiltersByDesk(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	awsDir := copyIntoDir(t, "testdata/aws-rightsizing-2026-09-02.csv")
	gcpDir := copyIntoDir(t, "testdata/gcp-recommender-2026-09-02.csv")
	if err := Save(db, "aws-rightsizing", map[string]string{"path": awsDir}); err != nil {
		t.Fatal(err)
	}
	if err := Save(db, "gcp-recommender", map[string]string{"path": gcpDir}); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(db, "aws-rightsizing", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(db, "gcp-recommender", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}

	aws, err := Recommendations(db, "aws")
	if err != nil {
		t.Fatal(err)
	}
	if len(aws) != 5 {
		t.Errorf("Recommendations(db, \"aws\") = %d rows, want 5", len(aws))
	}
	for _, r := range aws {
		if r.Desk != "aws" {
			t.Errorf("Recommendations(db, \"aws\") returned a %s row", r.Desk)
		}
	}
	gcp, err := Recommendations(db, "gcp")
	if err != nil {
		t.Fatal(err)
	}
	if len(gcp) != 3 {
		t.Errorf("Recommendations(db, \"gcp\") = %d rows, want 3", len(gcp))
	}
	onprem, err := Recommendations(db, "onprem")
	if err != nil {
		t.Fatal(err)
	}
	if len(onprem) != 0 {
		t.Errorf("Recommendations(db, \"onprem\") = %d rows, want 0: nothing was ever imported for it", len(onprem))
	}
}

// --------------------------------------------------------------- fixtures

const awsHeader = "AccountId,InstanceId,Region,CurrentInstanceType,RecommendedInstanceType," +
	"RightsizingType,EstimatedMonthlySavings,CurrencyCode,LookbackPeriodInDays,Notes"

func awsRowFields() []string {
	return strings.Split("111111111111,i-0aaaaaaaaaaaaaaaa,us-east-1,m5.2xlarge,m5.large,"+
		"Modify,50.00,USD,14,", ",")
}

const gcpHeader = "project_id,resource,recommender_subtype,current_machine_type," +
	"recommended_machine_type,monthly_cost_savings,currency_code,observation_period_days,description"

func gcpRowFields() []string {
	return strings.Split("taipanbox-prod,projects/p/zones/z/instances/vm-1,CHANGE_MACHINE_TYPE,"+
		"n2-standard-8,n2-standard-4,50.00,USD,14,", ",")
}

const azureHeader = "SubscriptionId,ImpactedResource,Category,RecommendationType,CurrentSku," +
	"RecommendedSku,PotentialAnnualCostSavings,Currency,LookbackDays,ResourceGroup"

func azureRowFields() []string {
	return strings.Split("9f1c2b3a-0001-4a11-8b22-abc123def456,/subscriptions/9f/vm-1,Cost,"+
		"Right-size,Standard_D8s_v5,Standard_D4s_v5,600.00,USD,14,rg-1", ",")
}

func assertNoRecommendations(t *testing.T, db *sql.DB) {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recommendations`).Scan(&n); err != nil {
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

func TestHostileRightsizingInput(t *testing.T) {
	t.Run("unknown header set: a required AWS column is missing", func(t *testing.T) {
		fields := strings.Split(awsHeader, ",")
		var kept []string
		for _, f := range fields {
			if f != "RightsizingType" {
				kept = append(kept, f)
			}
		}
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", strings.Join(kept, ",")+"\n"+strings.Join(awsRowFields(), ","))
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "aws-rightsizing", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "aws-rightsizing", false, ImportOptions{})
		if err != nil {
			t.Fatalf("Import returned a hard error rather than naming the file: %v", err)
		}
		if !strings.Contains(msg, "RightsizingType") {
			t.Errorf("the refusal does not name the missing column: %s", msg)
		}
		assertNoRecommendations(t, db)
	})

	t.Run("a negative saving is refused by name, and nothing is written", func(t *testing.T) {
		f := awsRowFields()
		f[6] = "-50.00" // EstimatedMonthlySavings
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", awsHeader+"\n"+strings.Join(f, ","))
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "aws-rightsizing", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "aws-rightsizing", false, ImportOptions{})
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "negative") {
			t.Errorf("the refusal does not say negative: %s", msg)
		}
		assertNoRecommendations(t, db)
	})

	t.Run("a resource id with an embedded quote round-trips", func(t *testing.T) {
		f := awsRowFields()
		f[1] = `"i-0quoted""instance"""` // InstanceId, RFC 4180 doubled-quote escaping
		dir := t.TempDir()
		writeFocusFile(t, dir, "good.csv", awsHeader+"\n"+strings.Join(f, ","))
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "aws-rightsizing", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "aws-rightsizing", false, ImportOptions{})
		if err != nil {
			t.Fatalf("Import: %v (%s)", err, msg)
		}
		var resource string
		if err := db.QueryRow(`SELECT resource FROM recommendations`).Scan(&resource); err != nil {
			t.Fatalf("the quoted resource id was not imported: %v (%s)", err, msg)
		}
		if resource != `i-0quoted"instance"` {
			t.Errorf("resource = %q, want the embedded quotes preserved", resource)
		}
	})

	t.Run("a field with a quoted comma round-trips", func(t *testing.T) {
		f := gcpRowFields()
		f[1] = `"projects/p, staging/zones/z/instances/vm-1"` // resource, quoted comma
		dir := t.TempDir()
		writeFocusFile(t, dir, "good.csv", gcpHeader+"\n"+strings.Join(f, ","))
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "gcp-recommender", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "gcp-recommender", false, ImportOptions{})
		if err != nil {
			t.Fatalf("Import: %v (%s)", err, msg)
		}
		var resource string
		if err := db.QueryRow(`SELECT resource FROM recommendations`).Scan(&resource); err != nil {
			t.Fatalf("the quoted-comma row was not imported: %v (%s)", err, msg)
		}
		if resource != "projects/p, staging/zones/z/instances/vm-1" {
			t.Errorf("resource = %q, want the embedded comma preserved", resource)
		}
	})

	t.Run("a UTF-8 BOM before the header is stripped, not folded into the first column", func(t *testing.T) {
		dir := t.TempDir()
		bom := string([]byte{0xEF, 0xBB, 0xBF})
		content := bom + gcpHeader + "\n" + strings.Join(gcpRowFields(), ",")
		writeFocusFile(t, dir, "good.csv", content)
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "gcp-recommender", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "gcp-recommender", false, ImportOptions{})
		if err != nil {
			t.Fatalf("a BOM'd header was refused rather than stripped: %v (%s)", err, msg)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM recommendations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%d rows imported from a BOM'd file, want 1: %s", n, msg)
		}
	})

	t.Run("CRLF line endings parse the same as LF", func(t *testing.T) {
		dir := t.TempDir()
		content := azureHeader + "\r\n" + strings.Join(azureRowFields(), ",") + "\r\n"
		writeFocusFile(t, dir, "good.csv", content)
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "azure-advisor", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		msg, err := Import(db, "azure-advisor", false, ImportOptions{})
		if err != nil {
			t.Fatalf("a CRLF file was refused: %v (%s)", err, msg)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM recommendations`).Scan(&n); err != nil {
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
		f := awsRowFields()
		f[9] = pad // Notes: not required, not mapped anywhere
		dir := t.TempDir()
		path := filepath.Join(dir, "big.csv")
		content := awsHeader + "\n" + strings.Join(f, ",")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		st := openFocusStore(t)
		db := st.DB()
		if err := Save(db, "aws-rightsizing", map[string]string{"path": dir}); err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		msg, err := Import(db, "aws-rightsizing", false, ImportOptions{})
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
		if err := db.QueryRow(`SELECT COUNT(*) FROM recommendations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%d rows imported, want 1", n)
		}
	})
}

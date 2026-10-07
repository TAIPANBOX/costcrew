package connectors

// Tests for the plain-FOCUS folder readers registered for aws-data-exports and
// gcp-billing-export (cloudfocus.go). They go through the package's public
// door only (Save, Import, Test, Get) and plain SQL, so they compile against
// the code as it was before the readers existed and fail there by behaviour:
// Import answers "no live account is connected", not a compile error.
//
// The fixtures are hand-written from the FOCUS 1.2 column list as AWS Data
// Exports documents it (table FOCUS_1_2_AWS) and from the columns Google's
// FOCUS export documents. No account data, real or otherwise: every id,
// amount and name below was typed for the test. The ProviderName values
// ("AWS", "Google Cloud") are an assumption of the fixture, which the
// reader's provider_names setting exists to correct.

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/estate"
)

const (
	awsFocusFixture = "testdata/aws-data-exports-focus-1-2-sample.csv"
	gcpFocusFixture = "testdata/gcp-billing-export-focus-sample.csv"
)

// ------------------------------------------------------------------ helpers

func cfSave(t *testing.T, db *sql.DB, id, dir string, extra map[string]string) {
	t.Helper()
	cfg := map[string]string{"path": dir}
	for k, v := range extra {
		cfg[k] = v
	}
	if err := Save(db, id, cfg); err != nil {
		t.Fatal(err)
	}
}

// cfImportFresh imports dir with connector id into a brand new store that
// holds no generated estate, so the mixing refusal is not in the way.
func cfImportFresh(t *testing.T, id, dir string, extra map[string]string) (string, *sql.DB, error) {
	t.Helper()
	st := openFocusStore(t)
	db := st.DB()
	cfSave(t, db, id, dir, extra)
	msg, err := Import(db, id, false, ImportOptions{})
	return msg, db, err
}

var cfCols = []string{
	"BilledCost", "BillingCurrency", "ChargeCategory", "ChargeClass", "ChargePeriodEnd",
	"ChargePeriodStart", "InvoiceId", "ProviderName", "ServiceName", "SubAccountId",
	"Tags", "ChargeDescription",
}

// cfRow is one valid daily AWS-shaped row; a test overrides what it needs.
func cfRow(over map[string]string) map[string]string {
	r := map[string]string{
		"BilledCost": "1.00", "BillingCurrency": "USD", "ChargeCategory": "Usage",
		"ChargeClass": "", "ChargePeriodStart": "2026-09-01T00:00:00Z",
		"ChargePeriodEnd": "2026-09-02T00:00:00Z", "InvoiceId": "INV-1",
		"ProviderName": "AWS", "ServiceName": "Amazon Elastic Compute Cloud",
		"SubAccountId": "111111111111", "Tags": `{"team":"payments"}`,
		"ChargeDescription": "line item",
	}
	for k, v := range over {
		r[k] = v
	}
	return r
}

// cfCSV renders rows under cols with encoding/csv, so quoting is correct.
func cfCSV(cols []string, rows ...map[string]string) string {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	w.Write(cols)
	for _, r := range rows {
		rec := make([]string, len(cols))
		for i, c := range cols {
			rec[i] = r[c]
		}
		w.Write(rec)
	}
	w.Flush()
	return b.String()
}

func cfWrite(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func cfGzip(t *testing.T, dir, name, content string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write([]byte(content))
	gz.Close()
	cfWrite(t, dir, name, buf.String())
}

func cfCount(db *sql.DB, query string, args ...any) int {
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		return -1
	}
	return n
}

// cfCharges lists the real charges as comparable strings.
func cfCharges(t *testing.T, db *sql.DB, provenance string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT source, day, service, COALESCE(team,'-'), category,
		billed_cents, COALESCE(invoice_id,'-') FROM charges WHERE provenance=?
		ORDER BY day, service, category, COALESCE(team,''), COALESCE(invoice_id,'')`, provenance)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var src, day, svc, team, cat, inv string
		var cents int64
		if err := rows.Scan(&src, &day, &svc, &team, &cat, &cents, &inv); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s|%s|%s|%s|%s|%d|%s", src, day, svc, team, cat, cents, inv))
	}
	return out
}

func cfWant(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("charges differ.\n got:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// ---------------------------------------------------------- the golden reads

// TestAWSDataExportsFocusIsRead: an AWS Data Exports FOCUS 1.2 CSV in a
// folder lands in charges under the aws desk, every row marked with the
// connector as its provenance (real, never generated), money in cents
// rounded once from exact micros, the team taken from the Tags column.
func TestAWSDataExportsFocusIsRead(t *testing.T) {
	dir := copyIntoDir(t, awsFocusFixture)
	st := openFocusStore(t)
	db := st.DB()
	cfSave(t, db, "aws-data-exports", dir, nil)

	msg, err := Import(db, "aws-data-exports", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	t.Logf("Import said: %s", msg)

	// 10 fixture rows; the free-tier row (0.00) adds no money, so no charge.
	cfWant(t, cfCharges(t, db, "aws-data-exports"), []string{
		"aws|2026-08-31|Amazon Simple Storage Service|-|Adjustment|250|INV-2026-08",
		"aws|2026-09-01|Amazon Elastic Compute Cloud|-|Purchase|25000|INV-2026-09",
		"aws|2026-09-01|Amazon Elastic Compute Cloud|payments|Usage|12046|INV-2026-09",
		"aws|2026-09-01|Amazon Simple Storage Service|data|Usage|1250|INV-2026-09",
		"aws|2026-09-02|Amazon CloudFront|-|Usage|420|INV-2026-09",
		"aws|2026-09-02|Amazon Elastic Compute Cloud|payments|Usage|13010|INV-2026-09",
		"aws|2026-09-03|AWS Lambda|data|Usage|1250|INV-2026-09",
		"aws|2026-09-03|Amazon Elastic Compute Cloud|-|Credit|-1800|INV-2026-09",
		"aws|2026-09-03|Tax|-|Tax|3145|INV-2026-09",
	})

	// Real, not generated: nothing this reader wrote is left without a
	// provenance, which is what marks a row generated.
	if n := cfCount(db, `SELECT COUNT(*) FROM charges WHERE provenance IS NULL`); n != 0 {
		t.Errorf("%d charges rows carry no provenance, so they read as generated", n)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM charges WHERE provenance='aws-data-exports'`); n != 9 {
		t.Errorf("%d charges rows marked aws-data-exports, want 9", n)
	}
	for _, want := range []string{"1 file", "10 rows", "3 sub accounts", "2026-08-31 to 2026-09-03",
		"545.71 net BilledCost", "5 of 10 rows carry the team tag", "1 correction"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the sentence does not say %q: %s", want, msg)
		}
	}
}

// TestGCPBillingExportFocusIsRead: the same engine for a CSV flattened from
// the BigQuery FOCUS view, whose tags arrive as x_Tags, a list of key/value
// records rather than FOCUS's Tags object.
func TestGCPBillingExportFocusIsRead(t *testing.T) {
	dir := copyIntoDir(t, gcpFocusFixture)
	st := openFocusStore(t)
	db := st.DB()
	cfSave(t, db, "gcp-billing-export", dir, nil)

	msg, err := Import(db, "gcp-billing-export", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	cfWant(t, cfCharges(t, db, "gcp-billing-export"), []string{
		"gcp|2026-09-01|BigQuery|analytics|Usage|990|GCP-INV-1",
		"gcp|2026-09-01|Compute Engine|search|Usage|8812|GCP-INV-1",
		"gcp|2026-09-02|Cloud Storage|-|Usage|333|GCP-INV-1",
		"gcp|2026-09-02|Compute Engine|-|Adjustment|-150|GCP-INV-1",
		"gcp|2026-09-02|Tax|-|Tax|1420|GCP-INV-1",
	})
	if n := cfCount(db, `SELECT COUNT(*) FROM charges WHERE source='aws'`); n != 0 {
		t.Errorf("a GCP export wrote %d rows under the aws desk", n)
	}
}

// TestTheCloudFocusReadersAreBuiltAndAskForAFolder holds the catalogue half:
// both entries are Built because a reader is registered (invariant 22 derives
// it), both take a folder, and the GCP entry no longer asks for a project and
// dataset it never used.
func TestTheCloudFocusReadersAreBuiltAndAskForAFolder(t *testing.T) {
	for _, id := range []string{"aws-data-exports", "gcp-billing-export"} {
		c, ok := Get(id)
		if !ok {
			t.Fatalf("%s is not in the catalogue", id)
		}
		if c.Status != Built {
			t.Errorf("%s is %q, want built", id, c.Status)
		}
		if len(c.Inputs) == 0 || c.Inputs[0].Name != "path" {
			t.Errorf("%s: first input is not the folder path: %+v", id, c.Inputs)
		}
		for _, in := range c.Inputs {
			if in.Name == "project" || in.Name == "dataset" {
				t.Errorf("%s still asks for %q, which no reader uses", id, in.Name)
			}
		}
	}
	// A freshly built connector must not demand its optional settings before
	// Test will run it.
	st := openFocusStore(t)
	cfSave(t, st.DB(), "gcp-billing-export", copyIntoDir(t, gcpFocusFixture), nil)
	res, ok, err := Test(st.DB(), "gcp-billing-export", func(string) string { return "" })
	if err != nil || !ok {
		t.Errorf("Test with only the folder set: ok=%v err=%v %s", ok, err, res)
	}
}

// ------------------------------------------------------- charge categories

// TestEveryChargeCategoryLandsAndAPurchaseIsNeverUsage: the five FOCUS
// ChargeCategory values each land in charges under their own name, so the
// category='Usage' sums the budget, coverage and eligibility code read can
// never include a commitment purchase, tax, credit or adjustment.
func TestEveryChargeCategoryLandsAndAPurchaseIsNeverUsage(t *testing.T) {
	dir := t.TempDir()
	cfWrite(t, dir, "cats.csv", cfCSV(cfCols,
		cfRow(map[string]string{"ChargeCategory": "Usage", "BilledCost": "10.00"}),
		cfRow(map[string]string{"ChargeCategory": "Purchase", "BilledCost": "1000.00"}),
		cfRow(map[string]string{"ChargeCategory": "Tax", "BilledCost": "2.00"}),
		cfRow(map[string]string{"ChargeCategory": "Credit", "BilledCost": "-3.00"}),
		cfRow(map[string]string{"ChargeCategory": "Adjustment", "BilledCost": "0.50"}),
	))
	_, db, err := cfImportFresh(t, "aws-data-exports", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"Usage": 1000, "Purchase": 100000, "Tax": 200, "Credit": -300, "Adjustment": 50}
	for cat, cents := range want {
		var got int
		if err := db.QueryRow(`SELECT COALESCE(SUM(billed_cents),0) FROM charges WHERE category=? AND provenance='aws-data-exports'`, cat).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != cents {
			t.Errorf("category %s: %d cents, want %d", cat, got, cents)
		}
	}
	var usage int
	db.QueryRow(`SELECT COALESCE(SUM(billed_cents),0) FROM charges WHERE category='Usage'`).Scan(&usage)
	if usage != 1000 {
		t.Errorf("the Usage sum is %d cents; a 1000.00 Purchase leaked into it", usage)
	}
}

// TestANegativeBilledCostIsKeptNotRefused: FOCUS lets BilledCost go negative
// (a credit, a refund) and does not forbid it on Usage either. The gateway
// reader refuses negatives; this one must keep them, and say it did.
func TestANegativeBilledCostIsKeptNotRefused(t *testing.T) {
	dir := t.TempDir()
	cfWrite(t, dir, "neg.csv", cfCSV(cfCols,
		cfRow(map[string]string{"ChargeCategory": "Credit", "BilledCost": "-12.345678"}),
		cfRow(map[string]string{"ChargeCategory": "Usage", "BilledCost": "-0.50", "ServiceName": "Refunded service"}),
	))
	msg, db, err := cfImportFresh(t, "aws-data-exports", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg, "refused") {
		t.Errorf("a negative cost was refused: %s", msg)
	}
	if !strings.Contains(msg, "2 rows have a negative BilledCost") {
		t.Errorf("the sentence does not say the negatives were kept: %s", msg)
	}
	var credit int
	db.QueryRow(`SELECT billed_cents FROM charges WHERE category='Credit'`).Scan(&credit)
	if credit != -1235 {
		t.Errorf("credit = %d cents, want -1235 (-12.345678 rounded half away from zero)", credit)
	}
	var refund int
	db.QueryRow(`SELECT billed_cents FROM charges WHERE service='Refunded service'`).Scan(&refund)
	if refund != -50 {
		t.Errorf("refund = %d cents, want -50", refund)
	}
}

// TestAFocus10FileIsReadAsWell: the shape of a real AWS FOCUS_1_0_AWS export
// (48 columns, no InvoiceId, timestamps with milliseconds, BilledCost to ten
// decimals, Tags always {}), rebuilt with invented values. Usage, Credit and
// Tax land on separate lines and the credits are not netted away; a month's
// Tax row, which AWS writes for the whole billing period, is kept whole on
// the day its period starts and counted, not refused.
func TestAFocus10FileIsReadAsWell(t *testing.T) {
	dir := copyIntoDir(t, "testdata/aws-data-exports-focus-1-0-sample.csv")
	msg, db, err := cfImportFresh(t, "aws-data-exports", dir, nil)
	if err != nil {
		t.Fatalf("%v (%s)", err, msg)
	}
	cfWant(t, cfCharges(t, db, "aws-data-exports"), []string{
		"aws|2026-10-01|Amazon Simple Storage Service|-|Usage|0|-",
		"aws|2026-10-01|Tax|-|Tax|20|-",
		"aws|2026-10-02|Amazon Elastic Compute Cloud|-|Credit|-100|-",
		"aws|2026-10-02|Amazon Elastic Compute Cloud|-|Usage|150|-",
		"aws|2026-10-03|Amazon Elastic Compute Cloud|-|Credit|-50|-",
	})
	for _, want := range []string{"7 rows", "1 sub account,", "0 of 7 rows carry the team tag",
		"2 rows cover more than one day and land whole on the day it starts",
		"2 rows have a negative BilledCost", "0.20 net BilledCost",
		"Usage 1.50", "Tax 0.20", "Credit -1.50"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the sentence does not say %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "refused") {
		t.Errorf("a row was refused: %s", msg)
	}
	var micros int64
	db.QueryRow(`SELECT billed_microusd FROM cloud_focus_rows WHERE service='Amazon Simple Storage Service'`).Scan(&micros)
	if micros != 12 {
		t.Errorf("0.0000123456 is %d micros, want 12 (rounded at the seventh decimal)", micros)
	}
}

// TestARowLongerThanADayLandsWholeOnItsFirstDay: AWS writes a month's Tax as
// one row for the whole billing period. It lands once, on the day the period
// starts, with every cent of it; spreading it evenly over thirty days would
// lose ten of them to rounding.
func TestARowLongerThanADayLandsWholeOnItsFirstDay(t *testing.T) {
	dir := t.TempDir()
	cfWrite(t, dir, "tax.csv", cfCSV(cfCols, cfRow(map[string]string{
		"ChargeCategory": "Tax", "BilledCost": "10.00", "ServiceName": "Tax",
		"ChargePeriodStart": "2026-09-01T00:00:00Z", "ChargePeriodEnd": "2026-10-01T00:00:00Z"})))
	msg, db, err := cfImportFresh(t, "aws-data-exports", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfWant(t, cfCharges(t, db, "aws-data-exports"), []string{"aws|2026-09-01|Tax|payments|Tax|1000|INV-1"})
	if !strings.Contains(msg, "1 row covers more than one day and lands whole on the day it starts") {
		t.Errorf("the sentence does not say the row was long: %s", msg)
	}
}

// ------------------------------------------------------------------- money

// TestCloudFocusMoneyIsNeverFloatAndRoundsOnce: ten 0.0035 rows are
// 3.5 cents of one group, rounded once to 4, where rounding each row on its
// own gives 0; and an amount a float64 round trip gets wrong stays exact, in
// E notation too, which the FOCUS numeric format allows.
func TestCloudFocusMoneyIsNeverFloatAndRoundsOnce(t *testing.T) {
	dir := t.TempDir()
	var rows []map[string]string
	for i := 0; i < 10; i++ {
		rows = append(rows, cfRow(map[string]string{"BilledCost": "0.0035"}))
	}
	// 0.000249 -> a float64 parse-and-scale gives 248 micros; E notation too.
	rows = append(rows,
		cfRow(map[string]string{"BilledCost": "0.000249", "ServiceName": "exact"}),
		cfRow(map[string]string{"BilledCost": "2.49E-4", "ServiceName": "exact-e"}),
		cfRow(map[string]string{"BilledCost": "35.2E-7", "ServiceName": "tiny-e"}),
		cfRow(map[string]string{"BilledCost": "1E2", "ServiceName": "hundred-e"}),
	)
	cfWrite(t, dir, "m.csv", cfCSV(cfCols, rows...))
	_, db, err := cfImportFresh(t, "aws-data-exports", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var cents int
	db.QueryRow(`SELECT billed_cents FROM charges WHERE service='Amazon Elastic Compute Cloud'`).Scan(&cents)
	if cents != 4 {
		t.Errorf("ten 0.0035 rows landed as %d cents, want 4 (35000 micros rounded once)", cents)
	}
	micros := func(service string) int64 {
		var m int64
		if err := db.QueryRow(`SELECT billed_microusd FROM cloud_focus_rows WHERE service=?`, service).Scan(&m); err != nil {
			t.Fatalf("%s: %v", service, err)
		}
		return m
	}
	if m := micros("exact"); m != 249 {
		t.Errorf("0.000249 is %d micros, want 249", m)
	}
	if m := micros("exact-e"); m != 249 {
		t.Errorf("2.49E-4 is %d micros, want 249", m)
	}
	if m := micros("tiny-e"); m != 4 {
		t.Errorf("35.2E-7 (0.00000352) is %d micros, want 4", m)
	}
	if m := micros("hundred-e"); m != 100_000_000 {
		t.Errorf("1E2 is %d micros, want 100000000", m)
	}
}

// ------------------------------------------------------------ team mapping

// TestTheTeamComesFromAConfigurableTagKey: the default key is "team"; the
// setting names another (AWS user tags are commonly prefixed), several keys
// are tried in order, a missing or valueless tag leaves the row teamless and
// the sentence counts them.
func TestTheTeamComesFromAConfigurableTagKey(t *testing.T) {
	dir := t.TempDir()
	cfWrite(t, dir, "t.csv", cfCSV(cfCols,
		cfRow(map[string]string{"ServiceName": "a", "Tags": `{"user_cost_centre":"cc-7","team":"ignored"}`}),
		cfRow(map[string]string{"ServiceName": "b", "Tags": `{"Owner":"ops"}`}),
		cfRow(map[string]string{"ServiceName": "c", "Tags": `{"user_cost_centre":true}`}),
		cfRow(map[string]string{"ServiceName": "d", "Tags": `{"user_cost_centre":null}`}),
		cfRow(map[string]string{"ServiceName": "e", "Tags": ``}),
	))
	msg, db, err := cfImportFresh(t, "aws-data-exports", dir, map[string]string{"team_tag": "user_cost_centre, owner"})
	if err != nil {
		t.Fatal(err)
	}
	team := func(svc string) string {
		var s sql.NullString
		db.QueryRow(`SELECT team FROM charges WHERE service=?`, svc).Scan(&s)
		return s.String
	}
	if got := team("a"); got != "cc-7" {
		t.Errorf("a: team %q, want cc-7", got)
	}
	if got := team("b"); got != "ops" {
		t.Errorf("b: team %q, want ops (second key, case-insensitive)", got)
	}
	for _, svc := range []string{"c", "d", "e"} {
		if got := team(svc); got != "" {
			t.Errorf("%s: team %q, want none", svc, got)
		}
	}
	if !strings.Contains(msg, "2 of 5 rows carry the team tag") || !strings.Contains(msg, "3 have none") {
		t.Errorf("the sentence does not count the tagged and untagged rows: %s", msg)
	}
	// And the default key does not see those tags at all.
	dir2 := t.TempDir()
	cfWrite(t, dir2, "t.csv", cfCSV(cfCols, cfRow(map[string]string{"Tags": `{"user_cost_centre":"cc-7"}`})))
	_, db2, err := cfImportFresh(t, "aws-data-exports", dir2, nil)
	if err != nil {
		t.Fatal(err)
	}
	var s sql.NullString
	db2.QueryRow(`SELECT team FROM charges`).Scan(&s)
	if s.Valid {
		t.Errorf("the default key read team %q from a tag it is not named", s.String)
	}
}

// ------------------------------------------------------------- idempotency

// TestCloudFocusReimportConverges: the same folder imported twice leaves the
// same rows, keyed by (file sha256, row number), and the same charges.
func TestCloudFocusReimportConverges(t *testing.T) {
	dir := copyIntoDir(t, awsFocusFixture)
	st := openFocusStore(t)
	db := st.DB()
	cfSave(t, db, "aws-data-exports", dir, nil)
	if _, err := Import(db, "aws-data-exports", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	first := cfCharges(t, db, "aws-data-exports")
	rows1 := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`)
	if _, err := Import(db, "aws-data-exports", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	cfWant(t, cfCharges(t, db, "aws-data-exports"), first)
	if rows2 := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`); rows2 != rows1 || rows1 != 10 {
		t.Errorf("raw rows %d then %d, want 10 both times", rows1, rows2)
	}
}

// TestARevisedFileReplacesItsOwnEarlierVersion: AWS revises a period for
// about two weeks by overwriting the export file in place. The new content
// has a new sha256; the old rows for that path must go, or the period is
// counted twice.
func TestARevisedFileReplacesItsOwnEarlierVersion(t *testing.T) {
	dir := t.TempDir()
	v1 := cfCSV(cfCols,
		cfRow(map[string]string{"BilledCost": "10.00"}),
		cfRow(map[string]string{"BilledCost": "20.00", "ChargePeriodStart": "2026-09-02T00:00:00Z", "ChargePeriodEnd": "2026-09-03T00:00:00Z"}),
	)
	cfWrite(t, dir, "data/export-00001.csv", v1)
	st := openFocusStore(t)
	db := st.DB()
	cfSave(t, db, "aws-data-exports", dir, nil)
	if _, err := Import(db, "aws-data-exports", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	// The revision: day one corrected, day two gone from this file.
	cfWrite(t, dir, "data/export-00001.csv", cfCSV(cfCols, cfRow(map[string]string{"BilledCost": "11.50"})))
	if _, err := Import(db, "aws-data-exports", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	cfWant(t, cfCharges(t, db, "aws-data-exports"), []string{
		"aws|2026-09-01|Amazon Elastic Compute Cloud|payments|Usage|1150|INV-1",
	})
	if n := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`); n != 1 {
		t.Errorf("%d raw rows after the revision, want 1: the old version's rows survived", n)
	}
}

// TestARefusedRevisionKeepsTheEarlierVersion: a revision that is truncated
// (a half-synced file) is refused by name and must not erase what the
// earlier, good version of the same path contributed.
func TestARefusedRevisionKeepsTheEarlierVersion(t *testing.T) {
	dir := t.TempDir()
	cfGzip(t, dir, "export-00001.csv.gz", cfCSV(cfCols, cfRow(nil)))
	st := openFocusStore(t)
	db := st.DB()
	cfSave(t, db, "aws-data-exports", dir, nil)
	if _, err := Import(db, "aws-data-exports", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write([]byte(cfCSV(cfCols, cfRow(map[string]string{"BilledCost": "99.00"}))))
	gz.Close()
	cfWrite(t, dir, "export-00001.csv.gz", buf.String()[:buf.Len()-6])
	msg, err := Import(db, "aws-data-exports", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !strings.Contains(msg, "export-00001.csv.gz") || !strings.Contains(msg, "not read") {
		t.Errorf("the half-synced file is not named as unread: %s", msg)
	}
	cfWant(t, cfCharges(t, db, "aws-data-exports"), []string{
		"aws|2026-09-01|Amazon Elastic Compute Cloud|payments|Usage|100|INV-1",
	})
}

// --------------------------------------------------- the generated estate

// TestCloudFocusRefusesToMixWithTheGeneratedEstate holds invariant 24 for
// the new readers: a store that still carries the generated estate is not
// given real cloud rows beside it unless the operator says so, and with the
// flag the generated rows go and the real ones arrive, journaled.
func TestCloudFocusRefusesToMixWithTheGeneratedEstate(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	if _, err := estate.Seed(db); err != nil {
		t.Fatal(err)
	}
	generated := cfCount(db, `SELECT COUNT(*) FROM charges`)
	if generated == 0 {
		t.Fatal("sanity: no generated charges")
	}
	dir := copyIntoDir(t, awsFocusFixture)
	cfSave(t, db, "aws-data-exports", dir, nil)

	_, err := Import(db, "aws-data-exports", false, ImportOptions{})
	if err == nil {
		t.Fatal("Import mixed real cloud rows into the generated estate")
	}
	if !strings.Contains(err.Error(), "-replace-generated") {
		t.Errorf("the refusal does not name the flag: %v", err)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM charges`); n != generated {
		t.Errorf("charges changed %d -> %d although Import refused", generated, n)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`); n > 0 {
		t.Errorf("%d raw rows written although Import refused", n)
	}

	if _, err := Import(db, "aws-data-exports", false,
		ImportOptions{ReplaceGenerated: true, Actor: "boss", Rec: st.AsRecorder()}); err != nil {
		t.Fatalf("Import with the flag: %v", err)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM charges WHERE provenance IS NULL`); n != 0 {
		t.Errorf("%d generated charges survived the flag", n)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM charges WHERE provenance='aws-data-exports'`); n != 9 {
		t.Errorf("%d real charges after the flag, want 9", n)
	}
	tail, _ := st.JournalTail(20)
	found := false
	for _, rec := range tail {
		if rec.Event == "generated_estate_replaced" {
			found = true
		}
	}
	if !found {
		t.Error("no generated_estate_replaced entry in the journal")
	}

	// A second connector then lands beside the first: nothing generated is
	// left to mix with, and the first connector's rows are not touched.
	gdir := copyIntoDir(t, gcpFocusFixture)
	cfSave(t, db, "gcp-billing-export", gdir, nil)
	if _, err := Import(db, "gcp-billing-export", false, ImportOptions{}); err != nil {
		t.Fatalf("the second connector: %v", err)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM charges WHERE provenance='aws-data-exports'`); n != 9 {
		t.Errorf("importing GCP disturbed the AWS rows: %d", n)
	}
}

// TestCloudFocusTestWritesNothing: Test describes exactly what Import would
// do, in the same sentence, and leaves the store alone.
func TestCloudFocusTestWritesNothing(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	cfSave(t, db, "aws-data-exports", copyIntoDir(t, awsFocusFixture), nil)
	res, ok, err := Test(db, "aws-data-exports", func(string) string { return "" })
	if err != nil || !ok {
		t.Fatalf("Test: ok=%v err=%v %s", ok, err, res)
	}
	for _, want := range []string{"Would read 1 file", "10 rows", "3 sub accounts", "545.71 net BilledCost"} {
		if !strings.Contains(res, want) {
			t.Errorf("Test does not say %q: %s", want, res)
		}
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM charges`); n > 0 {
		t.Errorf("Test wrote %d charges", n)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`); n > 0 {
		t.Errorf("Test wrote %d raw rows", n)
	}
	// Import says the same thing, in the past tense.
	imp, err := Import(db, "aws-data-exports", false, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Replace(res, "Would read", "Read", 1) != imp {
		t.Errorf("Test and Import describe different things.\n Test:   %s\n Import: %s", res, imp)
	}
}

// --------------------------------------------------------- the folder itself

// TestCloudFocusReadsANestedSyncedFolderAndIgnoresLinks: an S3 sync leaves
// partition folders, so the folder is walked; a symlink is never followed
// (one pointed at a device would never end), and the files it cannot read
// (Parquet, zip) are counted out loud rather than silently skipped.
func TestCloudFocusReadsANestedSyncedFolderAndIgnoresLinks(t *testing.T) {
	dir := t.TempDir()
	cfGzip(t, dir, "export/data/BILLING_PERIOD=2026-09/export-00001.csv.gz", cfCSV(cfCols, cfRow(nil)))
	cfWrite(t, dir, "export/data/BILLING_PERIOD=2026-09/export-00002.parquet", "PAR1")
	cfWrite(t, dir, "export/metadata/export-Manifest.json", "{}")
	outside := t.TempDir()
	target := cfWrite(t, outside, "elsewhere.csv", cfCSV(cfCols, cfRow(map[string]string{"BilledCost": "500.00"})))
	if err := os.Symlink(target, filepath.Join(dir, "link.csv")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	msg, db, err := cfImportFresh(t, "aws-data-exports", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`); n != 1 {
		t.Errorf("%d rows read, want 1: the symlinked file was followed or the nested one missed", n)
	}
	if !strings.Contains(msg, "2 files ignored") {
		t.Errorf("the sentence does not count the Parquet file and the link: %s", msg)
	}
}

// TestCloudFocusWithOnlyParquetSaysSo: a folder holding the export in a
// format this reader does not read is refused by name, not read as empty.
func TestCloudFocusWithOnlyParquetSaysSo(t *testing.T) {
	dir := t.TempDir()
	cfWrite(t, dir, "export-00001.snappy.parquet", "PAR1")
	_, _, err := cfImportFresh(t, "aws-data-exports", dir, nil)
	if err == nil || !strings.Contains(err.Error(), "parquet") || !strings.Contains(err.Error(), "CSV") {
		t.Errorf("want a refusal naming parquet and CSV, got %v", err)
	}
}

// ------------------------------------------------------------ hostile input

func cfHostile(t *testing.T, name, content, want string) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		dir := t.TempDir()
		cfWrite(t, dir, "bad.csv", content)
		msg, db, err := cfImportFresh(t, "aws-data-exports", dir, nil)
		if err != nil {
			t.Fatalf("Import returned a hard error rather than naming the problem: %v", err)
		}
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not say %q: %s", want, msg)
		}
		if n := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`); n != 0 {
			t.Errorf("%d raw rows written despite the refusal (count %d)", n, n)
		}
		if n := cfCount(db, `SELECT COUNT(*) FROM charges`); n != 0 {
			t.Errorf("%d charges written despite the refusal", n)
		}
	})
}

func TestCloudFocusHostileInput(t *testing.T) {
	good := cfRow(nil)
	without := func(drop string) []string {
		var out []string
		for _, c := range cfCols {
			if c != drop {
				out = append(out, c)
			}
		}
		return out
	}
	for _, col := range []string{"BilledCost", "BillingCurrency", "ChargeCategory", "ChargePeriodStart",
		"ChargePeriodEnd", "ProviderName", "ServiceName", "SubAccountId"} {
		cfHostile(t, "header missing "+col, cfCSV(without(col), good), col)
	}
	cfHostile(t, "duplicate header", "BilledCost,BilledCost,BillingCurrency\n1,2,USD\n", "twice")
	cfHostile(t, "currency EUR", cfCSV(cfCols, cfRow(map[string]string{"BillingCurrency": "EUR"})), `"EUR"`)
	cfHostile(t, "currency empty", cfCSV(cfCols, cfRow(map[string]string{"BillingCurrency": ""})), "USD only")
	cfHostile(t, "timestamp yesterday", cfCSV(cfCols, cfRow(map[string]string{"ChargePeriodStart": "yesterday"})), `"yesterday"`)
	cfHostile(t, "end before start", cfCSV(cfCols, cfRow(map[string]string{"ChargePeriodEnd": "2026-08-31T00:00:00Z"})), "not after")
	cfHostile(t, "a row spanning five years", cfCSV(cfCols, cfRow(map[string]string{"ChargePeriodEnd": "2031-09-01T00:00:00Z"})), "366 day")
	cfHostile(t, "a year nobody billed", cfCSV(cfCols, cfRow(map[string]string{
		"ChargePeriodStart": "0001-01-01T00:00:00Z", "ChargePeriodEnd": "0001-01-02T00:00:00Z"})), "outside")
	cfHostile(t, "cost abc", cfCSV(cfCols, cfRow(map[string]string{"BilledCost": "0.0035abc"})), `"0.0035abc"`)
	cfHostile(t, "cost NaN", cfCSV(cfCols, cfRow(map[string]string{"BilledCost": "NaN"})), `"NaN"`)
	cfHostile(t, "cost with a thousands comma", cfCSV(cfCols, cfRow(map[string]string{"BilledCost": "1,000.00"})), `"1,000.00"`)
	cfHostile(t, "cost over a billion", cfCSV(cfCols, cfRow(map[string]string{"BilledCost": "9223372036854.775807"})), "over")
	cfHostile(t, "cost huge exponent", cfCSV(cfCols, cfRow(map[string]string{"BilledCost": "1E400"})), `"1E400"`)
	cfHostile(t, "category outside the five", cfCSV(cfCols, cfRow(map[string]string{"ChargeCategory": "Freebie"})), `"Freebie"`)
	cfHostile(t, "category in the wrong case", cfCSV(cfCols, cfRow(map[string]string{"ChargeCategory": "usage"})), `"usage"`)
	cfHostile(t, "charge class other than Correction", cfCSV(cfCols, cfRow(map[string]string{"ChargeClass": "Refund"})), `"Refund"`)
	cfHostile(t, "another provider's export", cfCSV(cfCols, cfRow(map[string]string{"ProviderName": "Microsoft"})), `"Microsoft"`)
	cfHostile(t, "empty service", cfCSV(cfCols, cfRow(map[string]string{"ServiceName": ""})), "ServiceName")
	cfHostile(t, "tags that are not JSON", cfCSV(cfCols, cfRow(map[string]string{"Tags": "team=payments"})), "Tags")
	cfHostile(t, "tags that are a JSON string", cfCSV(cfCols, cfRow(map[string]string{"Tags": `"payments"`})), "Tags")
	cfHostile(t, "a 70 KB tags cell", cfCSV(cfCols, cfRow(map[string]string{"Tags": `{"team":"` + strings.Repeat("x", 70_000) + `"}`})), "Tags")
	cfHostile(t, "a 2 KB service name", cfCSV(cfCols, cfRow(map[string]string{"ServiceName": strings.Repeat("s", 2048)})), "ServiceName")
	cfHostile(t, "a row with too many fields", cfCSV(cfCols)+strings.Repeat("x,", 30)+"\n", "field(s), header has")
	cfHostile(t, "a row with three fields", cfCSV(cfCols)+"a,b,c\n", "field(s), header has")
	cfHostile(t, "an unterminated quote", "\"unterminated quote and no matching close at all", "not read")
	cfHostile(t, "a file of no columns", "\n\n\n", "not read")

	t.Run("a UTF-8 byte order mark on the header is not a missing column", func(t *testing.T) {
		dir := t.TempDir()
		cfWrite(t, dir, "bom.csv", "\ufeff"+cfCSV(cfCols, cfRow(nil)))
		_, db, err := cfImportFresh(t, "aws-data-exports", dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		if n := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`); n != 1 {
			t.Errorf("%d rows, want 1", n)
		}
	})

	t.Run("CRLF line endings and a quoted comma and newline are kept intact", func(t *testing.T) {
		dir := t.TempDir()
		content := cfCSV(cfCols, cfRow(map[string]string{"ChargeDescription": "a comma, and a\nnewline"}))
		cfWrite(t, dir, "q.csv", strings.ReplaceAll(content, "\n", "\r\n"))
		msg, db, err := cfImportFresh(t, "aws-data-exports", dir, nil)
		if err != nil {
			t.Fatalf("%v (%s)", err, msg)
		}
		if n := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`); n != 1 || strings.Contains(msg, "refused") {
			t.Errorf("the quoted row was refused or lost (%d rows): %s", n, msg)
		}
	})

	t.Run("truncated gzip", func(t *testing.T) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		gz.Write([]byte(cfCSV(cfCols, good)))
		gz.Close()
		dir := t.TempDir()
		cfWrite(t, dir, "bad.csv.gz", buf.String()[:buf.Len()-4])
		msg, db, err := cfImportFresh(t, "aws-data-exports", dir, nil)
		if err != nil {
			t.Fatalf("hard error: %v", err)
		}
		if !strings.Contains(msg, "bad.csv.gz") || !strings.Contains(msg, "not read") {
			t.Errorf("the truncated file is not named: %s", msg)
		}
		if n := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`); n != 0 {
			t.Errorf("%d rows written from a truncated gzip", n)
		}
	})

	t.Run("not a gzip at all", func(t *testing.T) {
		dir := t.TempDir()
		cfWrite(t, dir, "bad.csv.gz", "this is plain text, not gzip")
		msg, _, err := cfImportFresh(t, "aws-data-exports", dir, nil)
		if err != nil || !strings.Contains(msg, "not read") {
			t.Errorf("err=%v msg=%s", err, msg)
		}
	})

	t.Run("an empty folder", func(t *testing.T) {
		dir := t.TempDir()
		_, _, err := cfImportFresh(t, "aws-data-exports", dir, nil)
		if err == nil || !strings.Contains(err.Error(), dir) {
			t.Errorf("want a refusal naming the folder, got %v", err)
		}
	})

	t.Run("a folder that does not exist", func(t *testing.T) {
		_, _, err := cfImportFresh(t, "aws-data-exports", filepath.Join(t.TempDir(), "nope"), nil)
		if err == nil {
			t.Error("a missing folder was accepted")
		}
	})

	t.Run("a good file beside a bad one", func(t *testing.T) {
		dir := t.TempDir()
		cfWrite(t, dir, "a-good.csv", cfCSV(cfCols, good))
		cfWrite(t, dir, "b-bad.csv", "not,csv,shaped,at,all\nx,y,z,w,v\n")
		msg, db, err := cfImportFresh(t, "aws-data-exports", dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(msg, "b-bad.csv") {
			t.Errorf("the bad file is not named: %s", msg)
		}
		if n := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`); n != 1 {
			t.Errorf("%d rows from the good file, want 1", n)
		}
	})

	t.Run("a good row beside a bad row in one file", func(t *testing.T) {
		dir := t.TempDir()
		cfWrite(t, dir, "mix.csv", cfCSV(cfCols, good, cfRow(map[string]string{"BillingCurrency": "GBP"})))
		msg, db, err := cfImportFresh(t, "aws-data-exports", dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(msg, `"GBP"`) || !strings.Contains(msg, "1 row refused") {
			t.Errorf("the bad row is not named: %s", msg)
		}
		if n := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`); n != 1 {
			t.Errorf("%d rows, want the 1 good one", n)
		}
	})

	t.Run("a file that fails part way keeps none of its rows", func(t *testing.T) {
		// One good row, then a record that cannot be parsed at all: the good
		// row's INSERT was already executed and must be rolled back with it.
		dir := t.TempDir()
		cfWrite(t, dir, "half.csv", cfCSV(cfCols, good)+"\"never closed,1,2,3\n")
		msg, db, err := cfImportFresh(t, "aws-data-exports", dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(msg, "half.csv") || !strings.Contains(msg, "not read") {
			t.Errorf("the file is not named as unread: %s", msg)
		}
		if n := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`); n != 0 {
			t.Errorf("%d rows of a refused file survived", n)
		}
		if n := cfCount(db, `SELECT COUNT(*) FROM charges`); n != 0 {
			t.Errorf("%d charges of a refused file survived", n)
		}
	})
}

// TestARefusalListIsBounded: a hostile file of a hundred thousand bad rows
// is counted in full and shown in part, so the sentence (and the memory
// behind it) does not grow with the file.
func TestARefusalListIsBounded(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString(cfCSV(cfCols))
	bad := cfCSV(cfCols, cfRow(map[string]string{"BillingCurrency": "EUR"}))
	bad = bad[strings.Index(bad, "\n")+1:]
	for i := 0; i < 100_000; i++ {
		b.WriteString(bad)
	}
	cfWrite(t, dir, "bad.csv", b.String())
	msg, _, err := cfImportFresh(t, "aws-data-exports", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "100000 rows refused") {
		t.Errorf("the count is missing: %.300s", msg)
	}
	if !strings.Contains(msg, "first 20") {
		t.Errorf("the sentence does not say it shows only the first 20: %.300s", msg)
	}
	if len(msg) > 8_000 {
		t.Errorf("the sentence is %d bytes for a file of identical refusals", len(msg))
	}
}

// ------------------------------------------------------------------ bounds

// TestAGzipBombIsRefusedByName: a few kilobytes of gzip that inflate past
// the unpacked-size cap are refused as that file, named, with nothing kept,
// and the cap is the setting the operator can change.
func TestAGzipBombIsRefusedByName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bomb.csv.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	gz.Write([]byte(cfCSV(cfCols)))
	line := []byte(strings.TrimPrefix(cfCSV(cfCols, cfRow(nil)), cfCSV(cfCols)))
	for i := 0; i < 100_000; i++ { // ~15 MB unpacked
		gz.Write(line)
	}
	gz.Close()
	f.Close()
	info, _ := os.Stat(path)
	if info.Size() > 2_000_000 {
		t.Fatalf("the bomb is %d bytes packed; it is not a bomb", info.Size())
	}
	t.Logf("bomb: %d bytes packed, ~15 MB unpacked", info.Size())

	msg, db, err := cfImportFresh(t, "aws-data-exports", dir, map[string]string{"max_unpacked_mb": "1"})
	if err != nil {
		t.Fatalf("hard error rather than a named refusal: %v", err)
	}
	if !strings.Contains(msg, "bomb.csv.gz") || !strings.Contains(msg, "max_unpacked_mb") {
		t.Errorf("the refusal does not name the file and the setting: %s", msg)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`); n != 0 {
		t.Errorf("%d rows kept from a file refused for its unpacked size", n)
	}

	// The same file under a cap that fits it is read: the cap is a setting,
	// not a constant that happens to be small.
	msg, db, err = cfImportFresh(t, "aws-data-exports", dir, map[string]string{"max_unpacked_mb": "64"})
	if err != nil {
		t.Fatal(err)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`); n != 100_000 {
		t.Errorf("%d rows under a 64 MB cap, want 100000: %.300s", n, msg)
	}
}

// TestAFileOverTheByteCapIsRefusedBeforeItIsOpened: the on-disk cap, named.
func TestAFileOverTheByteCapIsRefusedBeforeItIsOpened(t *testing.T) {
	dir := t.TempDir()
	cfWrite(t, dir, "big.csv", cfCSV(cfCols, cfRow(map[string]string{"ChargeDescription": strings.Repeat("d", 900_000)}),
		cfRow(map[string]string{"ChargeDescription": strings.Repeat("d", 900_000)})))
	cfWrite(t, dir, "small.csv", cfCSV(cfCols, cfRow(nil)))
	msg, db, err := cfImportFresh(t, "aws-data-exports", dir, map[string]string{"max_file_mb": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "big.csv") || !strings.Contains(msg, "max_file_mb") {
		t.Errorf("the refusal does not name the file and the setting: %s", msg)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`); n != 1 {
		t.Errorf("%d rows, want the 1 from the small file", n)
	}
}

// TestACapSettingThatIsNotANumberIsRefused: a typo in a limit must not
// silently fall back to no limit.
func TestACapSettingThatIsNotANumberIsRefused(t *testing.T) {
	dir := copyIntoDir(t, awsFocusFixture)
	for _, bad := range []string{"lots", "0", "-5", "1.5", "99999999999"} {
		_, _, err := cfImportFresh(t, "aws-data-exports", dir, map[string]string{"max_file_mb": bad})
		if err == nil || !strings.Contains(err.Error(), "max_file_mb") {
			t.Errorf("max_file_mb=%q: want a refusal naming the setting, got %v", bad, err)
		}
	}
}

// TestARecordWithNoEndIsRefusedBeforeItFillsMemory: an opening quote that is
// never closed would make a CSV parser keep reading, and keeping, until the
// end of the file. The reader refuses the file once one record passes 1 MiB.
func TestARecordWithNoEndIsRefusedBeforeItFillsMemory(t *testing.T) {
	dir := t.TempDir()
	cfWrite(t, dir, "open.csv", cfCSV(cfCols)+"1.00,USD,\""+strings.Repeat("a\n", 3_000_000))
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	msg, _, err := cfImportFresh(t, "aws-data-exports", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if !strings.Contains(msg, "open.csv") || !strings.Contains(msg, "single record") {
		t.Errorf("the refusal does not say a record is too long: %s", msg)
	}
	if d := int64(after.HeapAlloc) - int64(before.HeapAlloc); d > 20_000_000 {
		t.Errorf("heap grew by %d bytes on a 6 MB file with one open quote", d)
	}
}

// TestACloudFocusFileOverAHundredMegabytesStaysBounded is the streaming
// property: a 120 MB file (400 rows, one unmapped column padded) is read
// with the live heap measured, not assumed.
func TestACloudFocusFileOverAHundredMegabytesStaysBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	const rows = 400
	pad := strings.Repeat("x", 300_000)
	dir := t.TempDir()
	path := filepath.Join(dir, "big.csv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(cfCSV(cfCols))
	line := strings.TrimPrefix(cfCSV(cfCols, cfRow(map[string]string{"ChargeDescription": pad})), cfCSV(cfCols))
	for i := 0; i < rows; i++ {
		f.WriteString(line)
	}
	f.Close()
	info, _ := os.Stat(path)
	if info.Size() < 100_000_000 {
		t.Fatalf("the fixture is %d bytes, not over 100 MB", info.Size())
	}

	st := openFocusStore(t)
	db := st.DB()
	cfSave(t, db, "aws-data-exports", dir, nil)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	msg, err := Import(db, "aws-data-exports", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	delta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("live heap delta after a %.1f MB file: %+d bytes", float64(info.Size())/1e6, delta)
	if delta > 50_000_000 {
		t.Errorf("heap grew by %d bytes reading a %d-byte file: it was buffered, not streamed", delta, info.Size())
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`); n != rows {
		t.Errorf("%d rows, want %d", n, rows)
	}
	var cents int
	db.QueryRow(`SELECT billed_cents FROM charges`).Scan(&cents)
	if cents != 40000 {
		t.Errorf("400 rows of 1.00 landed as %d cents, want 40000", cents)
	}
}

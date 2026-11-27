package connectors

// Each reader's row parser, given one bad field at a time: a currency that is
// not USD, an amount that is not a decimal, a count that is not a count, a
// required name left empty. Each refusal names the field and the value, which
// is what a person fixing the export needs. And a gzip that is not one, read
// through the same file walker the readers share.

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func colIndex(header string) map[string]int {
	col := map[string]int{}
	for i, h := range strings.Split(header, ",") {
		col[h] = i
	}
	return col
}

func withField(fields []string, header, name, value string) []string {
	out := append([]string(nil), fields...)
	out[colIndex(header)[name]] = value
	return out
}

func TestRightsizingRowsRefuseByName(t *testing.T) {
	type parser func([]string, map[string]int) (recRow, error)
	cases := []struct {
		reader, header string
		row            func() []string
		parse          parser
		field, value   string
		want           string
	}{
		{"aws", awsHeader, awsRowFields, parseAWSRightsizingRow, "CurrencyCode", "EUR", `currency "EUR", this reader is USD only`},
		{"gcp", gcpHeader, gcpRowFields, parseGCPRecommenderRow, "currency_code", "GBP", `currency "GBP", this reader is USD only`},
		{"gcp", gcpHeader, gcpRowFields, parseGCPRecommenderRow, "monthly_cost_savings", "lots", `monthly_cost_savings "lots" does not parse`},
		{"gcp", gcpHeader, gcpRowFields, parseGCPRecommenderRow, "resource", " ", "no resource"},
		{"gcp", gcpHeader, gcpRowFields, parseGCPRecommenderRow, "observation_period_days", "-1", `observation_period_days "-1" is not a non-negative integer`},
		{"azure", azureHeader, azureRowFields, parseAzureAdvisorRow, "Currency", "", `currency "", this reader is USD only`},
		{"azure", azureHeader, azureRowFields, parseAzureAdvisorRow, "PotentialAnnualCostSavings", "1,200", `PotentialAnnualCostSavings "1,200" does not parse`},
		{"azure", azureHeader, azureRowFields, parseAzureAdvisorRow, "ImpactedResource", "", "no ImpactedResource"},
		{"azure", azureHeader, azureRowFields, parseAzureAdvisorRow, "LookbackDays", "a week", `LookbackDays "a week" is not a non-negative integer`},
	}
	for _, c := range cases {
		t.Run(c.reader+" "+c.field, func(t *testing.T) {
			// The unedited row parses: the refusal is the one field's.
			if _, err := c.parse(c.row(), colIndex(c.header)); err != nil {
				t.Fatalf("the valid row was refused: %v", err)
			}
			_, err := c.parse(withField(c.row(), c.header, c.field, c.value), colIndex(c.header))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%s=%q: %v, want %q", c.field, c.value, err, c.want)
			}
		})
	}
}

func TestDivRoundHalfAwayFromZero(t *testing.T) {
	for _, c := range []struct{ n, d, want int64 }{
		{18, 12, 2}, {17, 12, 1}, {-18, 12, -2}, {-17, 12, -1}, {5, 0, 0}, {0, 12, 0},
	} {
		if got := divRoundHalfAwayFromZero(c.n, c.d); got != c.want {
			t.Errorf("%d/%d = %d, want %d", c.n, c.d, got, c.want)
		}
	}
}

func TestSaasSeatsRowsRefuseByName(t *testing.T) {
	header := strings.Join(saasSeatsHeaderFields(), ",")
	col, err := saasSeatsHeaderIndex(saasSeatsHeaderFields())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseSaasSeatsRow(saasSeatsRowFields(), col); err != nil {
		t.Fatalf("the valid row was refused: %v", err)
	}
	for _, c := range []struct{ field, value, want string }{
		{"Vendor", " ", "no vendor"},
		{"Product", "", "no product"},
		{"SeatsActive", "-3", `SeatsActive "-3" is not a whole number of seats`},
		{"ActiveWindowDays", "0", `ActiveWindowDays "0" is not a positive number of days`},
		{"MonthlyCents", "115.00", `MonthlyCents "115.00" is not a whole number of cents`},
	} {
		_, err := parseSaasSeatsRow(withField(saasSeatsRowFields(), header, c.field, c.value), col)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s=%q: %v, want %q", c.field, c.value, err, c.want)
		}
	}
	// A row shorter than the header reads its missing fields as empty, and
	// is refused for the first one that is required.
	if _, err := parseSaasSeatsRow([]string{"Zendesk"}, col); err == nil || !strings.Contains(err.Error(), "no product") {
		t.Errorf("a one-field row: %v", err)
	}
}

// A .gz in the folder is read through gzip, and one that is not gzip at all
// is refused for that, by name, without stopping the rest of the import.
func TestARightsizingExportIsReadGzippedAndABrokenGzipIsNamed(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "good.csv.gz"))
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	if _, err := zw.Write([]byte(awsHeader + "\n" + strings.Join(awsRowFields(), ",") + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.WriteFile(filepath.Join(dir, "broken.csv.gz"), []byte("this is not gzip"), 0o600); err != nil {
		t.Fatal(err)
	}

	msg, db, err := csvReaderImport(t, "aws-rightsizing", dir)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n := csvReaderRows(t, db, "recommendations"); n != 1 {
		t.Errorf("%d recommendations, want the one row in the gzipped file", n)
	}
	if !strings.Contains(msg, "broken.csv.gz") || !strings.Contains(msg, "not a valid gzip file") {
		t.Errorf("the import said %q; it does not name the broken gzip", msg)
	}
}

func TestBudgetRecommendationRowsRefuseByName(t *testing.T) {
	type parser func([]string, map[string]int) (budgetRecRow, error)
	cases := []struct {
		reader, header string
		row            func() []string
		parse          parser
		field, value   string
		want           string
	}{
		{"aws", budgetRecAWSHeader, budgetRecAWSRowFields, parseAWSBudgetsRecommendedRow, "CurrencyCode", "EUR", `currency "EUR", this reader is USD only`},
		{"gcp", budgetRecGCPHeader, budgetRecGCPRowFields, parseGCPCostRecommenderBudgetRow, "currency_code", "JPY", `currency "JPY", this reader is USD only`},
		{"gcp", budgetRecGCPHeader, budgetRecGCPRowFields, parseGCPCostRecommenderBudgetRow, "recommended_budget_amount", "two grand", `recommended_budget_amount "two grand" does not parse`},
		{"gcp", budgetRecGCPHeader, budgetRecGCPRowFields, parseGCPCostRecommenderBudgetRow, "month", "2026-9x", `month "2026-9x" is not a month`},
		{"azure", budgetRecAzureHeader, budgetRecAzureRowFields, parseAzureAdvisorBudgetRow, "Currency", "GBP", `currency "GBP", this reader is USD only`},
		{"azure", budgetRecAzureHeader, budgetRecAzureRowFields, parseAzureAdvisorBudgetRow, "RecommendedBudgetAmount", "$780", `RecommendedBudgetAmount "$780" does not parse`},
		{"azure", budgetRecAzureHeader, budgetRecAzureRowFields, parseAzureAdvisorBudgetRow, "Month", "20x6-09", `the Month column "20x6-09" is not a month`},
	}
	for _, c := range cases {
		t.Run(c.reader+" "+c.field, func(t *testing.T) {
			if _, err := c.parse(c.row(), colIndex(c.header)); err != nil {
				t.Fatalf("the valid row was refused: %v", err)
			}
			_, err := c.parse(withField(c.row(), c.header, c.field, c.value), colIndex(c.header))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%s=%q: %v, want %q", c.field, c.value, err, c.want)
			}
		})
	}
}

func TestABudgetMonthIsYearDashMonth(t *testing.T) {
	for s, want := range map[string]bool{
		"2026-09": true, "2026-12": true, "2026-10": true, "2026-01": true,
		"2026-00": false, "2026-13": false, "2026-9": false, "2026/09": false,
		"2O26-09": false, "September": false, "": false,
	} {
		if got := validBudgetRecMonth(s); got != want {
			t.Errorf("validBudgetRecMonth(%q) = %v, want %v", s, got, want)
		}
	}
}

// A gzipped budget export is read; a ragged row in it is refused by name and
// counted, and the rows around it still land.
func TestABudgetExportIsReadGzippedAndARaggedRowIsNamed(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "budgets.csv.gz"))
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	body := budgetRecAWSHeader + "\n" +
		strings.Join(budgetRecAWSRowFields(), ",") + "\n" +
		"111111111111,research\n"
	if _, err := zw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	msg, db, err := csvReaderImport(t, "aws-budgets-recommended", dir)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n := csvReaderRows(t, db, "budget_recommendations"); n != 1 {
		t.Errorf("%d budget recommendations, want the one whole row", n)
	}
	if !strings.Contains(msg, "budgets.csv.gz row 2: 2 field(s), header has 5") {
		t.Errorf("the import said %q; it does not name the ragged row", msg)
	}
}

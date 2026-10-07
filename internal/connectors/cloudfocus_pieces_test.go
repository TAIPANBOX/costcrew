package connectors

// The pieces of cloudfocus.go one by one, and the edges the folder-level
// tests in cloudfocus_test.go do not reach.

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/estate"
)

// TestTheTagCellShapes: the team is read from the shapes a Tags cell takes
// and from nothing else, and a value that cannot be a team name leaves the
// row teamless instead of failing it.
func TestTheTagCellShapes(t *testing.T) {
	long := strings.Repeat("t", 129)
	cases := []struct {
		name, raw  string
		team       string
		found, bad bool
		wantErr    bool
	}{
		{"object", `{"team":"payments"}`, "payments", true, false, false},
		{"padded value", `{"team":"  payments  "}`, "payments", true, false, false},
		{"key in another case", `{"TEAM":"payments"}`, "payments", true, false, false},
		{"google list", `[{"Key":"team","Value":"search","x_Inherited":false}]`, "search", true, false, false},
		{"list, first duplicate wins", `[{"Key":"team","Value":"a"},{"Key":"team","Value":"b"}]`, "a", true, false, false},
		{"list value not text", `[{"Key":"team","Value":true}]`, "", false, false, false},
		{"empty cell", ``, "", false, false, false},
		{"null", `null`, "", false, false, false},
		{"empty object", `{}`, "", false, false, false},
		{"empty value", `{"team":""}`, "", false, false, false},
		{"number value", `{"team":7}`, "", false, false, false},
		{"value too long", `{"team":"` + long + `"}`, "", false, true, false},
		{"value with a control character", "{\"team\":\"a\\u0007b\"}", "", false, true, false},
		{"not json", `team=payments`, "", false, false, true},
		{"a bare string", `"payments"`, "", false, false, true},
		{"broken object", `{"team":`, "", false, false, true},
		{"broken list", `[{"Key":`, "", false, false, true},
	}
	for _, c := range cases {
		team, found, bad, err := teamFromTags(c.raw, []string{"team"})
		if (err != nil) != c.wantErr || team != c.team || found != c.found || bad != c.bad {
			t.Errorf("%s: got (%q, found=%v, unusable=%v, err=%v), want (%q, %v, %v, err=%v)",
				c.name, team, found, bad, err, c.team, c.found, c.bad, c.wantErr)
		}
	}
}

// TestARefusalListIsBoundedAtItsSource: the per-file summary keeps 20
// reasons however many rows fail, so a hostile file cannot grow memory with
// its own refusals before they are merged into the sentence.
func TestARefusalListIsBoundedAtItsSource(t *testing.T) {
	s := newCloudSummary(cloudConfig{})
	for i := 0; i < 100_000; i++ {
		s.refuse(fmt.Sprintf("row %d: bad", i))
	}
	if len(s.Refusals) != 20 || s.RefusedRows != 100_000 {
		t.Errorf("kept %d reasons and counted %d rows, want 20 and 100000", len(s.Refusals), s.RefusedRows)
	}
}

// TestRefusalsAcrossManyFilesAreBoundedToo: thirty files each refusing one
// row are thirty refusals, of which the sentence names twenty.
func TestRefusalsAcrossManyFilesAreBoundedToo(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 30; i++ {
		cfWrite(t, dir, fmt.Sprintf("f%02d.csv", i), cfCSV(cfCols,
			cfRow(map[string]string{"BillingCurrency": "EUR", "BilledCost": fmt.Sprintf("%d.00", i+1)})))
	}
	msg, _, err := cfImportFresh(t, "aws-data-exports", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "30 rows refused (first 20 shown)") {
		t.Errorf("the sentence does not count thirty and show twenty: %.300s", msg)
	}
	if n := strings.Count(msg, "this reader is USD only"); n != 20 {
		t.Errorf("the sentence names %d refusals across the folder, want 20", n)
	}
}

// TestFocusDecimalsInENotation holds the FOCUS Numeric Format against the
// parser: E notation is legal and is applied to the digits as text, so it is
// exact; what is not a decimal, or is out of any real range, is refused.
func TestFocusDecimalsInENotation(t *testing.T) {
	good := map[string]int64{
		"35.2E-7": 4, "-1.5E3": -1_500_000_000, ".5E1": 5_000_000, "1E+3": 1_000_000_000,
		"0E5": 0, "12": 12_000_000, "-0.000001": -1, "0.0000005": 1, "0.0000004": 0,
		"123456789.123456": 123_456_789_123_456, "00000000000012": 12_000_000,
	}
	for in, want := range good {
		got, err := parseFocusDecimal(in)
		if err != nil || int64(got) != want {
			t.Errorf("%q: got %d err %v, want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "E5", "5E", "5E+", "1.2.3E1", "1E41", "1E-41", "abc", "1,5", "NaN",
		"Infinity", "0x10", "1_000", "--5", "9E39", "1" + strings.Repeat("0", 70)} {
		if _, err := parseFocusDecimal(in); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
	if _, err := parseFocusDecimal("1000000000"); err == nil {
		t.Error("a billion in one row was accepted")
	}
	if _, err := parseFocusDecimal("999999999.999999"); err != nil {
		t.Errorf("the largest accepted amount was refused: %v", err)
	}
}

// TestAnAmountThatOverflowsTheSumIsRefusedWhole: ten thousand rows each just
// under the per-row limit add up past what an int64 of micro-dollars holds.
// A wrapped sum would put a negative or tiny number into charges; the file
// is refused by name instead.
func TestAnAmountThatOverflowsTheSumIsRefusedWhole(t *testing.T) {
	dir := t.TempDir()
	var rows []map[string]string
	for i := 0; i < 10_000; i++ {
		rows = append(rows, cfRow(map[string]string{"BilledCost": "999999999.00"}))
	}
	cfWrite(t, dir, "huge.csv", cfCSV(cfCols, rows...))
	msg, db, err := cfImportFresh(t, "aws-data-exports", dir, nil)
	if err != nil {
		t.Fatalf("hard error: %v", err)
	}
	if !strings.Contains(msg, "huge.csv") || !strings.Contains(msg, "overflows") {
		t.Errorf("the file is not refused for its sum: %.300s", msg)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM charges`); n != 0 {
		t.Errorf("%d charges written from a file whose sum overflows", n)
	}
}

// TestAFolderWhoseFilesTogetherOverflowRefusesTheLaterFile: the same
// overflow spread over two files, each fine alone. The sentence's own total
// must not wrap, so the file that would tip it is refused by name.
func TestAFolderWhoseFilesTogetherOverflowRefusesTheLaterFile(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		var rows []map[string]string
		for j := 0; j < 6_000; j++ {
			rows = append(rows, cfRow(map[string]string{
				"BilledCost": fmt.Sprintf("999999999.%02d", i), "ServiceName": fmt.Sprintf("svc-%d", i)}))
		}
		cfWrite(t, dir, fmt.Sprintf("part-%d.csv", i), cfCSV(cfCols, rows...))
	}
	msg, _, err := cfImportFresh(t, "aws-data-exports", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "part-1.csv") || !strings.Contains(msg, "past what a 64-bit count") {
		t.Errorf("the second file is not refused for the folder's sum: %.400s", msg)
	}
	if !strings.Contains(msg, "Read 1 file") {
		t.Errorf("the first file should still be read: %.200s", msg)
	}
}

// TestCloudFocusSettings: the folder is required, the lists are bounded, a
// provider the default does not accept can be accepted by name, and a file
// where a folder belongs is said so.
func TestCloudFocusSettings(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	if err := Save(db, "aws-data-exports", map[string]string{"path": "  "}); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(db, "aws-data-exports", false, ImportOptions{}); err == nil || !strings.Contains(err.Error(), "no folder") {
		t.Errorf("want a refusal for the missing folder, got %v", err)
	}

	dir := t.TempDir()
	cfWrite(t, dir, "x.csv", cfCSV(cfCols, cfRow(nil)))
	many := "a,b,c,d,e,f,g,h,i"
	if _, _, err := cfImportFresh(t, "aws-data-exports", dir, map[string]string{"team_tag": many}); err == nil ||
		!strings.Contains(err.Error(), "team_tag") {
		t.Errorf("nine tag keys: want a refusal naming team_tag, got %v", err)
	}
	if _, _, err := cfImportFresh(t, "aws-data-exports", dir, map[string]string{"provider_names": strings.Repeat("p", 200)}); err == nil ||
		!strings.Contains(err.Error(), "provider_names") {
		t.Errorf("a 200-byte provider name: want a refusal naming provider_names, got %v", err)
	}
	// Only separators: the default stands.
	if _, db2, err := cfImportFresh(t, "aws-data-exports", dir, map[string]string{"team_tag": " , ,"}); err != nil {
		t.Errorf("separators only: %v", err)
	} else if n := cfCount(db2, `SELECT COUNT(*) FROM charges WHERE team='payments'`); n != 1 {
		t.Errorf("the default key was not used after an empty setting: %d", n)
	}

	other := t.TempDir()
	cfWrite(t, other, "contoso.csv", cfCSV(cfCols, cfRow(map[string]string{"ProviderName": "Contoso Cloud"})))
	if msg, _, err := cfImportFresh(t, "aws-data-exports", other, nil); err != nil || !strings.Contains(msg, "provider_names") {
		t.Errorf("a refused provider must name the setting that accepts it: err=%v %s", err, msg)
	}
	if _, db3, err := cfImportFresh(t, "aws-data-exports", other, map[string]string{"provider_names": "contoso cloud"}); err != nil {
		t.Fatal(err)
	} else if n := cfCount(db3, `SELECT COUNT(*) FROM charges`); n != 1 {
		t.Errorf("provider_names did not accept the provider: %d charges", n)
	}

	if _, _, err := cfImportFresh(t, "aws-data-exports", filepath.Join(dir, "x.csv"), nil); err == nil ||
		!strings.Contains(err.Error(), "folder") {
		t.Errorf("a file in place of a folder: want a refusal saying folder, got %v", err)
	}
}

// TestTheSameBytesTwiceAreOneFileAndTwoFilesOnOneDayAdd: a file copied
// beside itself is counted once and said so; two different files that touch
// the same day add up in one charges row.
func TestTheSameBytesTwiceAreOneFileAndTwoFilesOnOneDayAdd(t *testing.T) {
	dir := t.TempDir()
	one := cfCSV(cfCols, cfRow(map[string]string{"BilledCost": "10.00"}))
	cfWrite(t, dir, "a.csv", one)
	cfWrite(t, dir, "b-copy.csv", one)
	cfWrite(t, dir, "c.csv", cfCSV(cfCols, cfRow(map[string]string{"BilledCost": "5.25"})))
	msg, db, err := cfImportFresh(t, "aws-data-exports", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "1 file repeated an earlier file's bytes and was counted once") {
		t.Errorf("the copy is not named: %s", msg)
	}
	cfWant(t, cfCharges(t, db, "aws-data-exports"), []string{
		"aws|2026-09-01|Amazon Elastic Compute Cloud|payments|Usage|1525|INV-1",
	})
}

// TestADryRunNamesWhatWouldBeRefused: Test says "would be refused" and
// writes nothing.
func TestADryRunNamesWhatWouldBeRefused(t *testing.T) {
	dir := t.TempDir()
	cfWrite(t, dir, "mix.csv", cfCSV(cfCols, cfRow(nil), cfRow(map[string]string{"BillingCurrency": "GBP"})))
	st := openFocusStore(t)
	db := st.DB()
	cfSave(t, db, "aws-data-exports", dir, nil)
	res, ok, err := Test(db, "aws-data-exports", func(string) string { return "" })
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v %s", ok, err, res)
	}
	if !strings.Contains(res, "1 row would be refused") || !strings.Contains(res, `"GBP"`) {
		t.Errorf("Test does not name the row it would refuse: %s", res)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`); n > 0 {
		t.Errorf("Test wrote %d rows", n)
	}
}

type failingRecorder struct{}

func (failingRecorder) Emit(string, string, string, map[string]any, []string) error {
	return fmt.Errorf("the bus refused the line")
}

// TestAFailedJournalLineRollsTheWholeCloudImportBack: replacing the generated
// estate is journaled inside the import's transaction, so a refused journal
// line (invariant 50's failure, on a console teed onto the bus) leaves the
// generated estate standing and no real row behind.
func TestAFailedJournalLineRollsTheWholeCloudImportBack(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	if _, err := estate.Seed(db); err != nil {
		t.Fatal(err)
	}
	generated := cfCount(db, `SELECT COUNT(*) FROM charges`)
	cfSave(t, db, "aws-data-exports", copyIntoDir(t, awsFocusFixture), nil)
	_, err := Import(db, "aws-data-exports", false, ImportOptions{ReplaceGenerated: true, Actor: "boss", Rec: failingRecorder{}})
	if err == nil || !strings.Contains(err.Error(), "journaling") {
		t.Fatalf("want the journal failure returned, got %v", err)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM charges`); n != generated {
		t.Errorf("charges changed %d -> %d although the import failed", generated, n)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows`); n != 0 {
		t.Errorf("%d raw rows survived a failed import", n)
	}
}

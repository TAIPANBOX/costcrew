package connectors

// Invariant 103: TokenFuse adds x_price_basis to its FOCUS export, "book" for
// a call priced from its price book and "fallback" for one the book had no
// price for, priced at the gateway's fallback rate (measured on forge
// 2026-10-14: openai/gpt-4.1-mini at 15/75 per million, 0.00036 for 16
// tokens, thirty times or more its list price). A fallback amount is the
// gateway's estimate: it stays in ai_calls, and the daily ledger, every spend
// figure and the attribution leave it out.
//
// The fixture is seven calls: book-priced Haiku and Opus, two fallback-priced
// gpt-4.1-mini calls (one in a run that also spent at the book, one in a run
// that spent nothing else), a blocked call with no basis, and on the next day
// a book call and an admitted call whose basis is empty.

import (
	"encoding/csv"
	"os"
	"strings"
	"testing"
)

const exportPriceBasis = "testdata/tokenfuse-focus-price-basis-2026-10-14.csv"

func TestThePriceBasisIsKeptAndAFallbackIsLeftOutOfTheLedger(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	configureFocus(t, db, copyIntoDir(t, exportPriceBasis))
	msg, err := Import(db, "tokenfuse-focus", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	for basis, want := range map[string]int{"known": 3, "fallback": 2, "": 2} {
		if n := countWhere(t, db, `price_basis=?`, basis); n != want {
			t.Errorf("%d row(s) with price_basis %q, want %d", n, basis, want)
		}
	}
	if n := countWhere(t, db, FallbackPricedSQL); n != 2 {
		t.Errorf("FallbackPricedSQL counts %d calls, want the 2 fallback-priced admitted calls", n)
	}
	var cents, rows int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(billed_cents),0), COUNT(*) FROM charges
		WHERE provenance='tokenfuse-focus'`).Scan(&cents, &rows); err != nil {
		t.Fatal(err)
	}
	// 2026-10-14: Haiku 1 cent, Opus 4 cents; 2026-10-15: Haiku 0.005 + 0.001
	// (an empty basis is spend) = 1 cent. The gpt-4.1-mini estimates are not
	// in the ledger at all.
	if cents != 6 || rows != 3 {
		t.Errorf("the ledger holds %d cents over %d rows, want 6 over 3: a fallback estimate reached it", cents, rows)
	}
	var mini int
	if err := db.QueryRow(`SELECT COUNT(*) FROM charges WHERE model='openai/gpt-4.1-mini'`).Scan(&mini); err != nil {
		t.Fatal(err)
	}
	if mini != 0 {
		t.Errorf("%d charges row(s) carry the fallback-priced model", mini)
	}
	if !strings.Contains(msg, "2 calls (0.0024) were priced at a basis other than the gateway's price book") {
		t.Errorf("the import's sentence does not count the fallback-priced calls:\n%s", msg)
	}
}

func TestAnExportWithoutThePriceBasisKeepsEveryCallInTheLedger(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	configureFocus(t, db, copyIntoDir(t, "testdata/tokenfuse-focus-1.8.0-2026-10-14.csv"))
	msg, err := Import(db, "tokenfuse-focus", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	if n := countWhere(t, db, `price_basis IS NOT NULL`); n != 0 {
		t.Errorf("%d row(s) of an export without x_price_basis claim a basis", n)
	}
	var cents int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(billed_cents),0) FROM charges WHERE provenance='tokenfuse-focus'`).Scan(&cents); err != nil {
		t.Fatal(err)
	}
	if cents != 21 {
		t.Errorf("the ledger holds %d cents, want every admitted call's 21 as before", cents)
	}
	if strings.Contains(msg, "price book") {
		t.Errorf("an import without the column speaks of a price basis:\n%s", msg)
	}
}

// readCSV and writeCSV keep a quoted x_pii_findings ("pii_card=1,pii_email=1")
// one field while a test edits a row.
func readCSV(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func writeCSV(t *testing.T, recs [][]string) string {
	t.Helper()
	var b strings.Builder
	w := csv.NewWriter(&b)
	if err := w.WriteAll(recs); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestAPriceBasisExportIsKeptOverAnOlderCopyOfTheSameCalls(t *testing.T) {
	recs := readCSV(t, exportPriceBasis)
	// The same calls exported before x_price_basis and x_pii_findings existed.
	var older [][]string
	for _, r := range recs {
		older = append(older, r[:len(r)-2])
	}
	dir := t.TempDir()
	writeFocusFile(t, dir, "a-with-basis.csv", writeCSV(t, recs))
	writeFocusFile(t, dir, "z-without-basis.csv", writeCSV(t, older))
	st := openFocusStore(t)
	db := st.DB()
	configureFocus(t, db, dir)
	if _, err := Import(db, "tokenfuse-focus", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, db, `1=1`); n != 7 {
		t.Fatalf("the same seven calls in two exports are held as %d rows", n)
	}
	if n := countWhere(t, db, FallbackPricedSQL); n != 2 {
		t.Errorf("after both exports %d call(s) read as fallback-priced, want 2: the older copy displaced the one that says so", n)
	}
	if n := countWhere(t, db, PIILetThroughSQL); n != 2 {
		t.Errorf("after both exports %d call(s) carry PII findings, want 2", n)
	}
}

func TestHostilePriceBasisAndPIIFindingsAreRefusedByName(t *testing.T) {
	recs := readCSV(t, exportPriceBasis)
	header, call := recs[0], recs[2] // row 2: an admitted, book-priced call with no findings
	basisAt, piiAt := len(header)-2, len(header)-1
	for name, c := range map[string]struct {
		at    int
		value string
	}{
		"a basis with a control character": {basisAt, "fall\x07back"},
		"a basis that is a formula":        {basisAt, "=1+1"},
		"a basis over 32 bytes":            {basisAt, strings.Repeat("b", 33)},
		"a basis with a zero-width space":  {basisAt, "fall\u200bback"},
		"findings that carry a value":      {piiAt, "pii_email=jane@example.com"},
		"findings with no count":           {piiAt, "pii_card"},
		"findings with a zero count":       {piiAt, "pii_card=0"},
		"findings with a padded count":     {piiAt, "pii_card=01"},
		"findings with a negative count":   {piiAt, "pii_card=-1"},
		"findings naming a kind twice":     {piiAt, "pii_card=1,pii_card=2"},
		"findings with a capital kind":     {piiAt, "PII_card=1"},
		"findings with a kind of spaces":   {piiAt, "pii card=1"},
		"findings ending in a comma":       {piiAt, "pii_card=1,"},
		"findings that are a formula":      {piiAt, "=pii_card=1"},
		"findings over 256 bytes":          {piiAt, strings.Repeat("a", 250) + "=1,b=1"},
		"findings with a kind over 32":     {piiAt, strings.Repeat("k", 33) + "=1"},
	} {
		t.Run(name, func(t *testing.T) {
			row := append([]string(nil), call...)
			row[c.at] = c.value
			dir := t.TempDir()
			writeFocusFile(t, dir, "focus.csv", writeCSV(t, [][]string{header, row}))
			msg, db, err := importFrom(t, dir)
			if err != nil {
				t.Fatal(err)
			}
			if countWhere(t, db, `1=1`) != 0 || !strings.Contains(msg, header[c.at]) {
				t.Errorf("a row whose %s is %q was imported, or not refused by name:\n%s", header[c.at], c.value, msg)
			}
			if c.at == piiAt && len(c.value) > 6 && strings.Contains(msg, c.value) {
				t.Errorf("the refusal of x_pii_findings prints the value it refused:\n%s", msg)
			}
		})
	}
	// Two kinds, large counts: fine.
	row := append([]string(nil), call...)
	row[piiAt] = "pii_card=12,pii_phone_number=3"
	dir := t.TempDir()
	writeFocusFile(t, dir, "focus.csv", writeCSV(t, [][]string{header, row}))
	if _, db, err := importFrom(t, dir); err != nil || countWhere(t, db, PIILetThroughSQL) != 1 {
		t.Errorf("well-formed findings were refused (%v)", err)
	}
}

func TestThePIIFindingsAreKeptWhenTheExportCarriesThem(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	configureFocus(t, db, copyIntoDir(t, exportPriceBasis))
	if _, err := Import(db, "tokenfuse-focus", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, db, `pii_findings='pii_card=1,pii_email=1'`); n != 1 {
		t.Errorf("%d row(s) keep the findings pii_card=1,pii_email=1, want 1", n)
	}
	if n := countWhere(t, db, `pii_findings IS NULL`); n != 0 {
		t.Errorf("%d row(s) of an export with x_pii_findings stored as not reporting it", n)
	}
	if n := countWhere(t, db, PIILetThroughSQL); n != 2 {
		t.Errorf("PIILetThroughSQL counts %d calls, want 2", n)
	}
}

func TestTheReconciliationMarksTheFallbackPricedPartOfTheGatewaySum(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	configureFocus(t, db, copyIntoDir(t, exportPriceBasis))
	if _, err := Import(db, "tokenfuse-focus", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	rec, err := Reconcile(db, "openai-usage", "2026-10-14", "2026-10-14")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range rec.Rows {
		if r.Model == "openai/gpt-4.1-mini" {
			found = true
			if r.GatewayMicros != 2_360 || r.GatewayFallbackMicros != 2_360 {
				t.Errorf("gpt-4.1-mini: gateway %d, of which fallback %d; want 2360 and 2360", r.GatewayMicros, r.GatewayFallbackMicros)
			}
		}
	}
	if !found {
		t.Fatalf("no reconciliation row for the fallback-priced model: %+v", rec.Rows)
	}
}

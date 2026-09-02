package connectors

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// fixtureCSV is internal/connectors/testdata/tokenfuse-focus-2026-09-02.csv,
// copied byte for byte (sha256 a604c03f...2f70c5b on both sides) from
// ~/Development/go-to-market-2026-09/fixtures/tokenfuse-focus-2026-09-02.csv.
//
// @measured 2026-09-02 by tokenfuse-focus-2026-09-02.sh beside it there: the
// image ghcr.io/taipanbox/tokenfuse:v0.4.1, TOKENFUSE_ALLOW_STUB=1, five
// calls through /v1/messages from three agents in three runs (one
// case_resolved, one escalated, one blocked by a one-microdollar budget),
// then `tokenfuse focus-export`. What is real: the 26-column shape, the
// agent id, the run id, the outcome tags, the blocked row. What is not:
// every amount, because the stub provider meters a fixed 1000/500 tokens.
// See fixtures/README.md there for the rest of the provenance.
const fixtureCSV = "testdata/tokenfuse-focus-2026-09-02.csv"

func openFocusStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// copyIntoDir copies src into a fresh temp folder under its own basename and
// returns the folder, so each test gets an isolated directory to point the
// connector's path input at.
func copyIntoDir(t *testing.T, src string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, filepath.Base(src)), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func configureFocus(t *testing.T, db *sql.DB, dir string) {
	t.Helper()
	if err := Save(db, "tokenfuse-focus", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
}

// -------------------------------------------------------- the golden import

// TestTokenFuseFocusIsRead is red first: run against the code before this
// step (readers is empty, Import refuses every connector with "no live
// account is connected... The estate you are looking at is generated"), this
// fails immediately because there is nothing to import against.
func TestTokenFuseFocusIsRead(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := copyIntoDir(t, fixtureCSV)
	configureFocus(t, db, dir)

	msg, err := Import(db, "tokenfuse-focus", false, ImportOptions{Actor: "t.tester", Rec: st.AsRecorder()})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	t.Logf("Import said: %s", msg)

	var total, blocked int
	if err := db.QueryRow(`SELECT COUNT(*), SUM(blocked) FROM ai_calls`).Scan(&total, &blocked); err != nil {
		t.Fatal(err)
	}
	if total != 5 {
		t.Errorf("ai_calls has %d rows, want 5", total)
	}
	if blocked != 1 {
		t.Errorf("%d blocked rows, want 1", blocked)
	}
	if total-blocked != 4 {
		t.Errorf("%d non-blocked rows, want 4", total-blocked)
	}

	var blockedCents int64
	var blockedBasis string
	if err := db.QueryRow(`SELECT billed_cents, basis FROM ai_calls WHERE blocked=1`).
		Scan(&blockedCents, &blockedBasis); err != nil {
		t.Fatal(err)
	}
	if blockedCents != 0 {
		t.Errorf("the blocked row's billed_cents = %d, want 0", blockedCents)
	}
	if blockedBasis != "blocked" {
		t.Errorf("the blocked row's basis = %q, want \"blocked\"", blockedBasis)
	}

	// Three charges rows: the two haiku calls (each $0.003500, which
	// money.Parse rounds to 0 cents on its own third-decimal digit '3') sum
	// to 0; sonnet's single $0.010500 rounds to 1; opus's single NON-blocked
	// $0.017500 rounds to 2 (third digit '7' rounds up). The blocked opus
	// call is excluded from charges entirely, by model and by row.
	type gotRow struct {
		cents, qty int64
	}
	want := map[string]gotRow{
		"claude-haiku-4-5":  {0, 3000}, // 2 calls x (1000in+500out)
		"claude-sonnet-4-5": {1, 1500},
		"claude-opus-4-5":   {2, 1500}, // the blocked opus call is not here
	}
	rows, err := db.Query(`SELECT model, billed_cents, quantity FROM charges
		WHERE provenance='tokenfuse-focus' ORDER BY model`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]gotRow{}
	for rows.Next() {
		var model string
		var g gotRow
		var qty float64
		if err := rows.Scan(&model, &g.cents, &qty); err != nil {
			t.Fatal(err)
		}
		g.qty = int64(qty)
		got[model] = g
	}
	rows.Close()
	if len(got) != 3 {
		t.Fatalf("charges has %d distinct models with provenance tokenfuse-focus, want 3: %+v", len(got), got)
	}
	for model, w := range want {
		g, ok := got[model]
		if !ok {
			t.Errorf("no charges row for %s", model)
			continue
		}
		if g != w {
			t.Errorf("%s charges row = %+v, want %+v", model, g, w)
		}
	}

	// The forecaster made two calls that day (sonnet 0.0105 + opus 0.0175 =
	// 0.028) against triage-aws's two haiku calls (0.007), so the forecaster
	// holds the larger billed share and wins the attribution row.
	agent, confidence, err := estate.AgentFor(db,
		estate.SeriesKey{Source: "ai", Team: "", Service: "Anthropic API"}, "2026-09-02")
	if err != nil {
		t.Fatal(err)
	}
	if agent != "agent://taipanbox.dev/costcrew/forecaster" {
		t.Errorf("AgentFor = %q, want the forecaster", agent)
	}
	if confidence != "gateway-header" {
		t.Errorf("confidence = %q, want gateway-header", confidence)
	}
}

// TestBlockedCallsDoNotReachCharges guards a case the golden fixture cannot:
// its own blocked row happens to carry zero tokens as well as zero cost, so
// a deriveCharges that forgot to filter blocked rows out of the daily sum
// would still pass TestTokenFuseFocusIsRead untouched. A blocked call can
// have non-zero tokens (the guard can refuse after the prompt was already
// counted) while BilledCost must still be zero by contract, and this proves
// those tokens do not inflate the day's quantity either.
func TestBlockedCallsDoNotReachCharges(t *testing.T) {
	settled := focusRowFields() // 100+50 = 150 tokens, $0.05 = 5 cents
	blocked := focusRowFields()
	blocked[0] = "0.000000" // BilledCost: must be zero for a blocked row
	blocked[1] = "0.000000" // EffectiveCost
	blocked[21] = "true"    // x_blocked
	blocked[22] = "blocked" // x_cost_basis
	blocked[19] = "500"     // x_tokens_in: non-zero even though blocked
	blocked[20] = "250"     // x_tokens_out

	dir := t.TempDir()
	writeFocusFile(t, dir, "good.csv", focusHeader+"\n"+
		strings.Join(settled, ",")+"\n"+strings.Join(blocked, ","))
	msg, err, db := importFrom(t, dir)
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	var qty float64
	var cents int64
	if err := db.QueryRow(`SELECT quantity, billed_cents FROM charges
		WHERE provenance='tokenfuse-focus'`).Scan(&qty, &cents); err != nil {
		t.Fatal(err)
	}
	if qty != 150 {
		t.Errorf("quantity = %v, want 150: the blocked call's 750 tokens must not be counted", qty)
	}
	if cents != 5 {
		t.Errorf("billed_cents = %d, want 5", cents)
	}
}

func TestImportIsIdempotent(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := copyIntoDir(t, fixtureCSV)
	configureFocus(t, db, dir)

	if _, err := Import(db, "tokenfuse-focus", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	var calls1, charges1 int
	var cents1 int64
	db.QueryRow(`SELECT COUNT(*) FROM ai_calls`).Scan(&calls1)
	db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(billed_cents),0) FROM charges
		WHERE provenance='tokenfuse-focus'`).Scan(&charges1, &cents1)

	if _, err := Import(db, "tokenfuse-focus", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	var calls2, charges2 int
	var cents2 int64
	db.QueryRow(`SELECT COUNT(*) FROM ai_calls`).Scan(&calls2)
	db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(billed_cents),0) FROM charges
		WHERE provenance='tokenfuse-focus'`).Scan(&charges2, &cents2)

	if calls1 != calls2 {
		t.Errorf("ai_calls count changed on re-import: %d -> %d", calls1, calls2)
	}
	if charges1 != charges2 {
		t.Errorf("charges row count changed on re-import: %d -> %d", charges1, charges2)
	}
	if cents1 != cents2 {
		t.Errorf("charges billed_cents sum changed on re-import: %d -> %d", cents1, cents2)
	}
	if calls1 != 5 || charges1 != 3 {
		t.Fatalf("sanity: first import gave %d calls, %d charges rows, want 5 and 3", calls1, charges1)
	}
}

// TestGeneratedEstateIsNotMixed is refusal 1: without -replace-generated a
// store holding the generated world refuses Import outright, and nothing is
// written; with it, the generated rows are gone, the real ones are present,
// and the replacement is journaled.
func TestGeneratedEstateIsNotMixed(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()

	if _, err := estate.Seed(db); err != nil {
		t.Fatal(err)
	}
	var generatedCharges int
	db.QueryRow(`SELECT COUNT(*) FROM charges`).Scan(&generatedCharges)
	if generatedCharges == 0 {
		t.Fatal("sanity: estate.Seed wrote no charges")
	}
	// Two more of the seeded tables refusal 1 must also empty, given a
	// minimal schema this package does not otherwise depend on: proving the
	// wipe reaches beyond charges without importing anomaly/crew/history
	// just to seed them for real.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS anomalies(id TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO anomalies VALUES ('a1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS tasks(id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tasks VALUES (1)`); err != nil {
		t.Fatal(err)
	}

	dir := copyIntoDir(t, fixtureCSV)
	configureFocus(t, db, dir)

	// Without the flag: refused, nothing written.
	_, err := Import(db, "tokenfuse-focus", false, ImportOptions{})
	if err == nil {
		t.Fatal("Import ran against a generated estate without -replace-generated")
	}
	if !strings.Contains(err.Error(), "-replace-generated") {
		t.Errorf("the refusal does not name the flag: %v", err)
	}
	var calls int
	db.QueryRow(`SELECT COUNT(*) FROM ai_calls`).Scan(&calls)
	if calls != 0 {
		t.Errorf("%d ai_calls rows were written despite the refusal", calls)
	}
	var stillGenerated int
	db.QueryRow(`SELECT COUNT(*) FROM charges WHERE provenance IS NULL`).Scan(&stillGenerated)
	if stillGenerated != generatedCharges {
		t.Errorf("the generated charges changed even though Import refused: %d -> %d",
			generatedCharges, stillGenerated)
	}

	// With the flag: the generated rows are gone, the real ones are there.
	msg, err := Import(db, "tokenfuse-focus", false,
		ImportOptions{ReplaceGenerated: true, Actor: "boss", Rec: st.AsRecorder()})
	if err != nil {
		t.Fatalf("Import with -replace-generated: %v (%s)", err, msg)
	}
	var afterGenerated, afterReal, afterAnomalies, afterTasks int
	db.QueryRow(`SELECT COUNT(*) FROM charges WHERE provenance IS NULL`).Scan(&afterGenerated)
	db.QueryRow(`SELECT COUNT(*) FROM charges WHERE provenance='tokenfuse-focus'`).Scan(&afterReal)
	db.QueryRow(`SELECT COUNT(*) FROM anomalies`).Scan(&afterAnomalies)
	db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&afterTasks)
	if afterGenerated != 0 {
		t.Errorf("%d generated charges rows survived -replace-generated", afterGenerated)
	}
	if afterReal != 3 {
		t.Errorf("%d real charges rows after import, want 3", afterReal)
	}
	if afterAnomalies != 0 {
		t.Errorf("%d rows left in anomalies after -replace-generated", afterAnomalies)
	}
	if afterTasks != 0 {
		t.Errorf("%d rows left in tasks after -replace-generated", afterTasks)
	}

	tail, err := st.JournalTail(20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rec := range tail {
		if rec.Event == "generated_estate_replaced" {
			found = true
		}
	}
	if !found {
		t.Error("no generated_estate_replaced entry in the journal")
	}
}

// ----------------------------------------------------------- hostile input

const focusHeader = "BilledCost,EffectiveCost,BillingCurrency,ChargePeriodStart,ChargePeriodEnd," +
	"ChargeDescription,ProviderName,PublisherName,InvoiceIssuerName,ServiceName,ServiceCategory," +
	"ResourceId,ResourceName,SubAccountId,SubAccountName,x_run_id,x_parent_run_id,x_agent_id," +
	"x_model,x_tokens_in,x_tokens_out,x_blocked,x_cost_basis,x_outcome,x_unit,x_tool_calls"

// focusRowFields is one otherwise-valid record, split so a subtest can
// replace exactly the field it wants to make hostile and rejoin it.
func focusRowFields() []string {
	return strings.Split("0.050000,0.050000,USD,2026-09-02T10:00:00Z,2026-09-02T10:00:00Z,desc,"+
		"Anthropic,Anthropic,Anthropic,LLM inference,AI,agent://a/b/c,agent://a/b/c,run-1,run-1,"+
		"run-1,,agent://a/b/c,claude-haiku-4-5,100,50,false,settled,,,0", ",")
}

func writeFocusFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// importFrom is the hostile-input tests' shared shape: configure the
// connector at dir and Import it against a fresh store, and hand back the
// message and error rather than asserting anything itself, because "refused
// by name" and "skipped, the rest imported" want different assertions.
func importFrom(t *testing.T, dir string) (string, error, *sql.DB) {
	t.Helper()
	st := openFocusStore(t)
	db := st.DB()
	configureFocus(t, db, dir)
	msg, err := Import(db, "tokenfuse-focus", false, ImportOptions{})
	return msg, err, db
}

func TestHostileInput(t *testing.T) {
	t.Run("header missing BilledCost", func(t *testing.T) {
		fields := strings.Split(focusHeader, ",")
		var kept []string
		for _, f := range fields {
			if f != "BilledCost" {
				kept = append(kept, f)
			}
		}
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", strings.Join(kept, ",")+"\n"+strings.Join(focusRowFields(), ","))
		msg, err, db := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error rather than naming the file: %v", err)
		}
		if !strings.Contains(msg, "BilledCost") {
			t.Errorf("the refusal does not name the missing column: %s", msg)
		}
		assertNoRows(t, db)
	})

	t.Run("currency EUR", func(t *testing.T) {
		f := focusRowFields()
		f[2] = "EUR" // BillingCurrency
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", focusHeader+"\n"+strings.Join(f, ","))
		msg, err, db := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "EUR") {
			t.Errorf("the refusal does not name the currency: %s", msg)
		}
		assertNoRows(t, db)
	})

	t.Run("negative cost", func(t *testing.T) {
		f := focusRowFields()
		f[0] = "-0.050000" // BilledCost
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", focusHeader+"\n"+strings.Join(f, ","))
		msg, err, db := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "negative") {
			t.Errorf("the refusal does not say negative: %s", msg)
		}
		assertNoRows(t, db)
	})

	t.Run("cost 0.0035abc", func(t *testing.T) {
		f := focusRowFields()
		f[0] = "0.0035abc"
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", focusHeader+"\n"+strings.Join(f, ","))
		msg, err, db := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "0.0035abc") {
			t.Errorf("the refusal does not name the bad amount: %s", msg)
		}
		assertNoRows(t, db)
	})

	t.Run("timestamp yesterday", func(t *testing.T) {
		f := focusRowFields()
		f[3] = "yesterday" // ChargePeriodStart
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", focusHeader+"\n"+strings.Join(f, ","))
		msg, err, db := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "yesterday") {
			t.Errorf("the refusal does not name the bad timestamp: %s", msg)
		}
		assertNoRows(t, db)
	})

	t.Run("empty x_agent_id and empty ResourceId", func(t *testing.T) {
		f := focusRowFields()
		f[11] = "" // ResourceId
		f[17] = "" // x_agent_id
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", focusHeader+"\n"+strings.Join(f, ","))
		msg, err, db := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "no agent") {
			t.Errorf("the refusal does not say no agent: %s", msg)
		}
		assertNoRows(t, db)
	})

	t.Run("blocked row with a non-zero cost", func(t *testing.T) {
		f := focusRowFields()
		f[21] = "true" // x_blocked
		f[22] = "blocked"
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", focusHeader+"\n"+strings.Join(f, ",")) // BilledCost stays 0.05
		msg, err, db := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "blocked but") {
			t.Errorf("the refusal does not name the contract break: %s", msg)
		}
		assertNoRows(t, db)
	})

	t.Run("truncated gzip", func(t *testing.T) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		gz.Write([]byte(focusHeader + "\n" + strings.Join(focusRowFields(), ",") + "\n"))
		gz.Close()
		full := buf.Bytes()
		truncated := full[:len(full)-4] // cut the last few bytes: no valid trailer
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "bad.csv.gz"), truncated, 0o644); err != nil {
			t.Fatal(err)
		}
		msg, err, db := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error rather than naming the file: %v", err)
		}
		if !strings.Contains(msg, "not read") {
			t.Errorf("the truncated file is not named as unread: %s", msg)
		}
		assertNoRows(t, db)
	})

	t.Run("a row with 40 columns", func(t *testing.T) {
		f := focusRowFields()
		for i := 0; i < 14; i++ {
			f = append(f, "extra")
		}
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", focusHeader+"\n"+strings.Join(f, ","))
		msg, err, db := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "field(s), header has") {
			t.Errorf("the refusal does not name the field count: %s", msg)
		}
		assertNoRows(t, db)
	})

	t.Run("a row with 3 columns", func(t *testing.T) {
		dir := t.TempDir()
		writeFocusFile(t, dir, "bad.csv", focusHeader+"\na,b,c")
		msg, err, db := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "field(s), header has") {
			t.Errorf("the refusal does not name the field count: %s", msg)
		}
		assertNoRows(t, db)
	})

	t.Run("a field containing a quoted comma and a newline", func(t *testing.T) {
		// This must NOT be refused: RFC 4180 quoting is what
		// focusexport.rs actually writes, and encoding/csv is the library
		// doing the parsing, so this proves it round-trips rather than
		// being split into extra columns or losing the embedded newline.
		f := focusRowFields()
		f[23] = `"a comma, and a` + "\n" + `newline"` // x_outcome, quoted
		dir := t.TempDir()
		writeFocusFile(t, dir, "good.csv", focusHeader+"\n"+strings.Join(f, ","))
		msg, err, db := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import: %v (%s)", err, msg)
		}
		var outcome string
		if err := db.QueryRow(`SELECT outcome FROM ai_calls`).Scan(&outcome); err != nil {
			t.Fatalf("the quoted row was not imported: %v (%s)", err, msg)
		}
		if outcome != "a comma, and a\nnewline" {
			t.Errorf("outcome = %q, want the comma and newline preserved", outcome)
		}
	})

	t.Run("a file that is not CSV at all", func(t *testing.T) {
		dir := t.TempDir()
		// An opening quote with no closing quote: encoding/csv fails to
		// parse even the header, which is a stronger proof than plain prose
		// (technically valid, if useless, CSV).
		writeFocusFile(t, dir, "bad.csv", "\"unterminated quote and no matching close at all")
		msg, err, db := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error rather than naming the file: %v", err)
		}
		if !strings.Contains(msg, "not read") {
			t.Errorf("the unparseable file is not named as unread: %s", msg)
		}
		assertNoRows(t, db)
	})

	t.Run("an empty folder", func(t *testing.T) {
		dir := t.TempDir()
		_, err, _ := importFrom(t, dir)
		if err == nil {
			t.Fatal("Import accepted a folder with no CSV files")
		}
		if !strings.Contains(err.Error(), dir) {
			t.Errorf("the refusal does not name the folder: %v", err)
		}
	})

	t.Run("a folder with one good and one bad file", func(t *testing.T) {
		dir := t.TempDir()
		writeFocusFile(t, dir, "a-good.csv", focusHeader+"\n"+strings.Join(focusRowFields(), ","))
		writeFocusFile(t, dir, "b-bad.csv", "not,csv,shaped,at,all\nx,y,z,w,v")
		msg, err, db := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "b-bad.csv") {
			t.Errorf("the bad file is not named: %s", msg)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ai_calls`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%d rows imported from the good file, want 1", n)
		}
	})
}

func assertNoRows(t *testing.T, db *sql.DB) {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_calls`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d rows were written despite the row being refused", n)
	}
}

// TestA200MegabyteFileStaysBounded is the streaming property itself: one
// field (ChargeDescription, which nothing in this reader maps) is padded so
// the file reaches roughly 200 MB with a small, fast row count, and the live
// heap after the import is measured rather than assumed. Padding one column
// keeps the row count (and so the SQLite work and the wall-clock cost of
// this test) small while still exercising genuine 200 MB-scale file I/O:
// what the "never ReadAll" property is about is BYTES held at once, not how
// many rows happen to produce them.
func TestA200MegabyteFileStaysBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	const rows = 400
	const padBytes = 500_000 // 400 * 500_000 ~= 200 000 000 bytes of padding alone
	pad := strings.Repeat("x", padBytes)

	dir := t.TempDir()
	path := filepath.Join(dir, "big.csv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(focusHeader + "\n"); err != nil {
		t.Fatal(err)
	}
	fields := focusRowFields()
	fields[5] = pad // ChargeDescription: not required, not mapped anywhere
	line := strings.Join(fields, ",") + "\n"
	for i := 0; i < rows; i++ {
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fixture file: %d bytes (%.1f MB)", info.Size(), float64(info.Size())/1e6)
	if info.Size() < 150_000_000 {
		t.Fatalf("the generated fixture is only %d bytes; the test does not exercise 200 MB scale", info.Size())
	}

	st := openFocusStore(t)
	db := st.DB()
	configureFocus(t, db, dir)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	msg, err := Import(db, "tokenfuse-focus", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}

	runtime.GC()
	runtime.ReadMemStats(&after)
	delta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("live heap delta after importing a %.1f MB file: %+d bytes (%.1f MB)",
		float64(info.Size())/1e6, delta, float64(delta)/1e6)
	// Comfortably under the 200 MB file: proof this never held the whole
	// file, or even the whole 500 KB padded field set, in memory at once.
	const bound = 50_000_000
	if delta > bound {
		t.Errorf("live heap grew by %d bytes importing a %d-byte file; want under %d, "+
			"which would mean the file was buffered wholesale rather than streamed",
			delta, info.Size(), bound)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_calls`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != rows {
		t.Errorf("%d rows imported, want %d", n, rows)
	}
}

// ------------------------------------------------------------------- Test()

// TestTestDescribesTheFocusFolderWithoutWriting is refusal 2: Test() walks
// the folder and describes it exactly as Import would, and writes nothing.
func TestTestDescribesTheFocusFolderWithoutWriting(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := copyIntoDir(t, fixtureCSV)
	configureFocus(t, db, dir)

	result, ok, err := Test(db, "tokenfuse-focus", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("Test() reported not ok: %s", result)
	}
	for _, want := range []string{"1 file", "5 rows", "3 distinct agents", "2026-09-02"} {
		if !strings.Contains(result, want) {
			t.Errorf("Test() result does not mention %q: %s", want, result)
		}
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_calls`).Scan(&n); err != nil {
		// ai_calls may not exist at all if Test() truly wrote nothing and
		// nothing else created it; either reading 0 or the table being
		// absent proves the same thing, so only a genuine row is a failure.
		if !strings.Contains(err.Error(), "no such table") {
			t.Fatal(err)
		}
		return
	}
	if n != 0 {
		t.Errorf("Test() wrote %d rows to ai_calls", n)
	}
	var charges int
	db.QueryRow(`SELECT COUNT(*) FROM charges`).Scan(&charges)
	if charges != 0 {
		t.Errorf("Test() wrote %d rows to charges", charges)
	}
}

// TestEnsureFocusSchemaRunsTwice is invariant 11 for this reader's own
// migration: starting the console twice must not fail the second time.
func TestEnsureFocusSchemaRunsTwice(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	if err := EnsureFocusSchema(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := EnsureFocusSchema(db); err != nil {
		t.Fatalf("second run: %v (ALTER TABLE ADD COLUMN on a column that "+
			"already exists must be tolerated, not fail the start)", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_calls`).Scan(&n); err != nil {
		t.Fatal(err)
	}
}

func TestParseFocusTokensRejectsGarbageAndNegative(t *testing.T) {
	t.Run("garbage, not blocked", func(t *testing.T) {
		if _, err := parseFocusTokens("many", false); err == nil {
			t.Error("accepted a non-integer token count")
		}
	})
	t.Run("negative, not blocked", func(t *testing.T) {
		if _, err := parseFocusTokens("-5", false); err == nil {
			t.Error("accepted a negative token count")
		}
	})
	t.Run("empty, not blocked, is refused", func(t *testing.T) {
		if _, err := parseFocusTokens("", false); err == nil {
			t.Error("accepted an empty token count on a non-blocked row")
		}
	})
	t.Run("empty, blocked, reads as zero", func(t *testing.T) {
		n, err := parseFocusTokens("", true)
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("n = %d, want 0", n)
		}
	})
}

func TestTestNamesRowsItWouldRefuse(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	f := focusRowFields()
	f[2] = "EUR"
	dir := t.TempDir()
	writeFocusFile(t, dir, "bad.csv", focusHeader+"\n"+strings.Join(f, ","))
	configureFocus(t, db, dir)

	result, ok, err := Test(db, "tokenfuse-focus", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("Test() reported not ok on a file it can otherwise describe: %s", result)
	}
	if !strings.Contains(result, "would be refused") || !strings.Contains(result, "EUR") {
		t.Errorf("Test() does not name the row it would refuse: %s", result)
	}
}

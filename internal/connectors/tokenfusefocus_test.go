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
	"github.com/TAIPANBOX/costcrew/internal/money"
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

	var blockedMicros int64
	var blockedBasis string
	if err := db.QueryRow(`SELECT billed_microusd, basis FROM ai_calls WHERE blocked=1`).
		Scan(&blockedMicros, &blockedBasis); err != nil {
		t.Fatal(err)
	}
	if blockedMicros != 0 {
		t.Errorf("the blocked row's billed_microusd = %d, want 0", blockedMicros)
	}
	if blockedBasis != "blocked" {
		t.Errorf("the blocked row's basis = %q, want \"blocked\"", blockedBasis)
	}

	// Three charges rows, each the SUM of that day's Micros for the model,
	// rounded to cents ONCE: the two haiku calls are $0.003500 each, 3500
	// micros apiece, summing to 7000 micros -- seven tenths of a cent,
	// which rounds up (half away from zero) to 1 cent, not down to 0.
	// Sonnet's single $0.010500 (10500 micros) rounds to 1. Opus's single
	// NON-blocked $0.017500 (17500 micros) rounds to 2. The blocked opus
	// call is excluded from charges entirely, by model and by row.
	type gotRow struct {
		cents, qty int64
	}
	want := map[string]gotRow{
		"claude-haiku-4-5":  {1, 3000}, // 2 calls x (1000in+500out); 7000 micros -> 1 cent
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

// TestSubCentCallsRoundHalfAwayFromZeroOnceSummed is the property this
// reader exists to hold, exercised through a real import rather than only
// through money.Micros directly (internal/money's own tests cover the
// arithmetic in isolation; this proves deriveCharges actually calls it the
// way the arithmetic assumes). Ten haiku calls of $0.0035 on one day are
// $0.035, three and a half cents on the nose -- a TIE -- and half away from
// zero, the same convention money.Parse and money.Bps already use, rounds a
// tie up: four cents, not three.
func TestSubCentCallsRoundHalfAwayFromZeroOnceSummed(t *testing.T) {
	row := func() []string {
		f := focusRowFields()
		f[0] = "0.003500" // BilledCost
		f[1] = "0.003500" // EffectiveCost
		return f
	}

	t.Run("two calls sum to seven tenths of a cent, rounds up to one", func(t *testing.T) {
		dir := t.TempDir()
		lines := focusHeader + "\n" + strings.Join(row(), ",") + "\n" + strings.Join(row(), ",")
		writeFocusFile(t, dir, "good.csv", lines)
		msg, db, err := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import: %v (%s)", err, msg)
		}
		var cents int64
		if err := db.QueryRow(`SELECT billed_cents FROM charges
			WHERE provenance='tokenfuse-focus'`).Scan(&cents); err != nil {
			t.Fatalf("no charges row was derived: %v (%s)", err, msg)
		}
		if cents != 1 {
			t.Errorf("two $0.0035 calls produced a charges row of %d cents, want 1: "+
				"$0.007 rounded half away from zero", cents)
		}
	})

	t.Run("ten calls sum to a tie at three and a half cents, rounds up to four", func(t *testing.T) {
		dir := t.TempDir()
		var b strings.Builder
		b.WriteString(focusHeader + "\n")
		for i := 0; i < 10; i++ {
			b.WriteString(strings.Join(row(), ","))
			b.WriteString("\n")
		}
		writeFocusFile(t, dir, "good.csv", b.String())
		msg, db, err := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import: %v (%s)", err, msg)
		}
		var cents int64
		if err := db.QueryRow(`SELECT billed_cents FROM charges
			WHERE provenance='tokenfuse-focus'`).Scan(&cents); err != nil {
			t.Fatalf("no charges row was derived: %v (%s)", err, msg)
		}
		if cents != 4 {
			t.Errorf("ten $0.0035 calls produced a charges row of %d cents, want 4: "+
				"$0.035 is an exact tie at 3.5 cents, and half away from zero rounds up", cents)
		}
	})
}

// TestCostIsNeverParsedThroughFloat64 is mutant (b): parsing BilledCost as a
// float64 and multiplying by 1e6 gives the wrong answer for real values, not
// just contrived ones. $0.000249 is exactly 249 micros -- six decimal
// digits, nothing to round -- but float64(0.000249) is stored as
// 0.00024899999999999998, and truncating that back to an integer after
// multiplying by 1e6 gives 248. money.ParseMicros never goes through a
// float, so this must come back exact.
func TestCostIsNeverParsedThroughFloat64(t *testing.T) {
	f := focusRowFields()
	f[0] = "0.000249" // BilledCost
	f[1] = "0.000249" // EffectiveCost
	dir := t.TempDir()
	writeFocusFile(t, dir, "good.csv", focusHeader+"\n"+strings.Join(f, ","))
	msg, db, err := importFrom(t, dir)
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	var micros int64
	if err := db.QueryRow(`SELECT billed_microusd FROM ai_calls`).Scan(&micros); err != nil {
		t.Fatal(err)
	}
	if micros != 249 {
		t.Errorf("billed_microusd = %d, want 249 (a float64 round trip of $0.000249 gives 248)", micros)
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
	msg, db, err := importFrom(t, dir)
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
func importFrom(t *testing.T, dir string) (string, *sql.DB, error) {
	t.Helper()
	st := openFocusStore(t)
	db := st.DB()
	configureFocus(t, db, dir)
	msg, err := Import(db, "tokenfuse-focus", false, ImportOptions{})
	return msg, db, err
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
		msg, db, err := importFrom(t, dir)
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
		msg, db, err := importFrom(t, dir)
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
		msg, db, err := importFrom(t, dir)
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
		msg, db, err := importFrom(t, dir)
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
		msg, db, err := importFrom(t, dir)
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
		msg, db, err := importFrom(t, dir)
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
		msg, db, err := importFrom(t, dir)
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
		msg, db, err := importFrom(t, dir)
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
		msg, db, err := importFrom(t, dir)
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
		msg, db, err := importFrom(t, dir)
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
		msg, db, err := importFrom(t, dir)
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
		msg, db, err := importFrom(t, dir)
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
		_, _, err := importFrom(t, dir)
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
		msg, db, err := importFrom(t, dir)
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

// ------------------------------------------------------------ commitments
//
// C4-SPEC.md section 2. Red first, against main before this step: the six
// columns below did not exist in requiredFocusColumns or anywhere else in
// this file, so every row -- Purchase included -- went through
// parseFocusRow and either landed in ai_calls or was refused for missing an
// agent; there was no "commitments" table for sqlite to have opinions about,
// so TestCommitmentColumnsFillTheCommitmentsTable failed immediately with
// "no such table: commitments" and TestPurchaseRowsAreNeverCountedAsUsage
// failed because the $999 Purchase row landed in ai_calls and inflated the
// desk's Usage charges by exactly that amount -- both quoted verbatim in
// the PR body.

// commitmentHeader is focusHeader with the six optional columns this step
// reads appended: five FOCUS 1.2 CommitmentDiscount* columns and
// ChargeCategory, the routing column. A file without them (fixtureCSV, and
// every focusHeader-only test above) never has ChargeCategory equal
// "Purchase" -- focusField returns "" for a column absent from the header --
// so it takes the ai_calls path unchanged; that absence-is-safe property is
// exactly what TestAbsentCommitmentColumnsLeaveTheCommitmentsTableAlone
// checks directly, on fixtureCSV itself.
const commitmentHeader = focusHeader +
	",ChargeCategory,CommitmentDiscountId,CommitmentDiscountType," +
	"CommitmentDiscountStatus,CommitmentDiscountQuantity,CommitmentDiscountUnit"

// commitmentUsageRowFields is one ordinary usage row under commitmentHeader:
// focusRowFields' 26 fields plus ChargeCategory=Usage and five empty
// CommitmentDiscount* fields, so a file that DOES carry the six columns
// still reads its non-Purchase rows exactly like one that never heard of
// them.
func commitmentUsageRowFields() []string {
	return append(focusRowFields(), "Usage", "", "", "", "", "")
}

// commitmentRowFields is one ChargeCategory=Purchase row: the AI-call-shaped
// columns are left blank (parseCommitmentRow never reads x_agent_id or the
// token columns; only parseFocusRow does, and a Purchase row never reaches
// it), and the six columns commitmentHeader adds carry the commitment's own
// state.
func commitmentRowFields(id, kind, status, qty, unit, billedCost, start, end string) []string {
	return []string{
		billedCost, billedCost, "USD", start, end, "commitment purchase",
		"Anthropic", "Anthropic", "Anthropic", "Reserved Capacity", "AI",
		"", "", "sub-1", "", "", "", "", "", "", "", "false", "", "", "", "0",
		"Purchase", id, kind, status, qty, unit,
	}
}

func writeCommitmentFile(t *testing.T, dir, name string, rows ...[]string) {
	t.Helper()
	var b strings.Builder
	b.WriteString(commitmentHeader + "\n")
	for _, r := range rows {
		b.WriteString(strings.Join(r, ","))
		b.WriteString("\n")
	}
	writeFocusFile(t, dir, name, b.String())
}

func assertNoCommitmentRows(t *testing.T, db *sql.DB) {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM commitments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d commitment row(s) were written despite the row being refused", n)
	}
}

// TestCommitmentColumnsFillTheCommitmentsTable is C4-SPEC.md section 4's
// primary case: "importing a FOCUS file with commitment columns fills
// commitments and the summary says so".
func TestCommitmentColumnsFillTheCommitmentsTable(t *testing.T) {
	dir := t.TempDir()
	writeCommitmentFile(t, dir, "good.csv",
		commitmentUsageRowFields(),
		commitmentRowFields("cud-gcp-1", "cud", "Used", "700", "normalized-hours",
			"460.000000", "2026-01-01T00:00:00Z", "2027-01-01T00:00:00Z"),
	)
	msg, db, err := importFrom(t, dir)
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	if !strings.Contains(msg, "1 commitment row") {
		t.Errorf("the summary does not say how many commitment rows carried the columns: %s", msg)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM commitments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("commitments has %d row(s), want 1", n)
	}
	var kind, status, unit, source, start, end string
	var qty float64
	var cents int64
	if err := db.QueryRow(`SELECT kind, status, quantity, unit, source, date_start, date_end,
			monthly_cents FROM commitments WHERE id='cud-gcp-1'`).
		Scan(&kind, &status, &qty, &unit, &source, &start, &end, &cents); err != nil {
		t.Fatal(err)
	}
	if kind != "cud" || status != "Used" || qty != 700 || unit != "normalized-hours" {
		t.Errorf("got (%q,%q,%v,%q), want (cud,Used,700,normalized-hours)", kind, status, qty, unit)
	}
	if source != "ai" {
		t.Errorf("source = %q, want ai", source)
	}
	if start != "2026-01-01" || end != "2027-01-01" {
		t.Errorf("got dates (%s,%s), want (2026-01-01,2027-01-01)", start, end)
	}
	if cents != 46000 {
		t.Errorf("monthly_cents = %d, want 46000 ($460.00)", cents)
	}

	// The ordinary usage row beside it still went through the normal path.
	var calls int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_calls`).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("ai_calls has %d row(s), want 1: the usage row beside the Purchase row", calls)
	}
}

// TestAbsentCommitmentColumnsLeaveTheCommitmentsTableAlone imports the
// existing golden fixture, which has never heard of ChargeCategory or any
// CommitmentDiscount* column, and requires commitments to stay empty:
// "absent columns leave the generated fixture's table alone".
func TestAbsentCommitmentColumnsLeaveTheCommitmentsTableAlone(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := copyIntoDir(t, fixtureCSV)
	configureFocus(t, db, dir)
	if _, err := Import(db, "tokenfuse-focus", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	assertNoCommitmentRows(t, db)
}

// TestPurchaseRowsAreNeverCountedAsUsage is C4-SPEC.md section 4's mutant
// (g): a Purchase row counted as usage. The commitment row's own BilledCost
// ($999) dwarfs the one usage row's ($0.05, 5 cents); if the routing in
// processFocusFile were dropped, the Purchase row would fall through to
// parseFocusRow (refused there for carrying no agent and no tokens, in
// which case ai_calls would still be 1 row and this test would already
// catch the regression) or, were parseCommitmentRow's own row simply left
// unrouted and ai_calls-shaped fields backfilled, would land in the derived
// Usage charges and multiply the desk's total by nearly 20 000x. Either way
// this test is sensitive to the mutant gates-have-teeth.sh plants:
// commenting out the `== "Purchase"` routing check.
func TestPurchaseRowsAreNeverCountedAsUsage(t *testing.T) {
	dir := t.TempDir()
	writeCommitmentFile(t, dir, "good.csv",
		commitmentUsageRowFields(), // $0.05, 5 cents
		commitmentRowFields("aws-sp-1", "savings-plan", "Used", "1", "hours",
			"999.000000", "2026-09-02T00:00:00Z", "2027-09-02T00:00:00Z"),
	)
	msg, db, err := importFrom(t, dir)
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}

	var calls int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_calls`).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("ai_calls has %d row(s), want 1: the Purchase row must never reach it", calls)
	}
	var usageCents int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(billed_cents),0) FROM charges
		WHERE provenance='tokenfuse-focus' AND category='Usage'`).Scan(&usageCents); err != nil {
		t.Fatal(err)
	}
	if usageCents != 5 {
		t.Errorf("Usage charges sum to %d cents, want 5: the $999 Purchase row (%s) leaked into usage",
			money.Cents(usageCents), msg)
	}
	var commitmentN int
	if err := db.QueryRow(`SELECT COUNT(*) FROM commitments`).Scan(&commitmentN); err != nil {
		t.Fatal(err)
	}
	if commitmentN != 1 {
		t.Errorf("commitments has %d row(s), want 1", commitmentN)
	}
}

// TestCommitmentBoundaries is C4-SPEC.md section 4's boundary list for this
// reader: a commitment with zero quantity, and an expiry today (accepted
// exactly like any other date -- ExpiringWithin's own <= comparison, tested
// in internal/finops, is what makes "today" the edge that must count).
func TestCommitmentBoundaries(t *testing.T) {
	t.Run("zero quantity is accepted, not refused", func(t *testing.T) {
		dir := t.TempDir()
		writeCommitmentFile(t, dir, "good.csv",
			commitmentRowFields("new-1", "cud", "Unused", "0", "normalized-hours",
				"120.000000", "2026-09-01T00:00:00Z", "2027-09-01T00:00:00Z"),
		)
		msg, db, err := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import: %v (%s)", err, msg)
		}
		var n int
		var qty float64
		if err := db.QueryRow(`SELECT COUNT(*), COALESCE(MIN(quantity),-1) FROM commitments
			WHERE id='new-1'`).Scan(&n, &qty); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("a zero-quantity commitment was refused rather than kept: %s", msg)
		}
		if qty != 0 {
			t.Errorf("quantity = %v, want 0", qty)
		}
	})

	t.Run("an expiry today is kept as today, not shifted", func(t *testing.T) {
		dir := t.TempDir()
		writeCommitmentFile(t, dir, "good.csv",
			commitmentRowFields("today-1", "reserved", "Used", "1", "hours",
				"50.000000", "2026-01-01T00:00:00Z", "2026-09-02T00:00:00Z"),
		)
		_, db, err := importFrom(t, dir)
		if err != nil {
			t.Fatal(err)
		}
		var end string
		if err := db.QueryRow(`SELECT date_end FROM commitments WHERE id='today-1'`).Scan(&end); err != nil {
			t.Fatal(err)
		}
		if end != "2026-09-02" {
			t.Errorf("date_end = %q, want 2026-09-02", end)
		}
	})
}

// TestCommitmentHostileInputs is C4-SPEC.md section 4's hostile list for
// this reader: a negative quantity, a status string outside the FOCUS
// enumeration, and a 1 MB id.
func TestCommitmentHostileInputs(t *testing.T) {
	t.Run("negative quantity is refused", func(t *testing.T) {
		dir := t.TempDir()
		writeCommitmentFile(t, dir, "bad.csv",
			commitmentRowFields("neg-1", "cud", "Used", "-4", "normalized-hours",
				"120.000000", "2026-09-01T00:00:00Z", "2027-09-01T00:00:00Z"),
		)
		msg, db, err := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "negative") {
			t.Errorf("the refusal does not say negative: %s", msg)
		}
		assertNoCommitmentRows(t, db)
	})

	t.Run("a status outside the FOCUS enumeration is kept and flagged", func(t *testing.T) {
		dir := t.TempDir()
		writeCommitmentFile(t, dir, "good.csv",
			commitmentRowFields("weird-1", "cud", "PartiallyUsed", "10", "normalized-hours",
				"200.000000", "2026-09-01T00:00:00Z", "2027-09-01T00:00:00Z"),
		)
		msg, db, err := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import: %v (%s)", err, msg)
		}
		var n int
		var status string
		if err := db.QueryRow(`SELECT COUNT(*), MIN(status) FROM commitments WHERE id='weird-1'`).
			Scan(&n, &status); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("an unrecognised status was dropped rather than kept: %s", msg)
		}
		if status != "PartiallyUsed" {
			t.Errorf("status = %q, want the literal unrecognised value kept, not normalised", status)
		}
		if !strings.Contains(msg, "outside the FOCUS enumeration") {
			t.Errorf("the summary does not flag the unrecognised status: %s", msg)
		}
	})

	t.Run("a 1 MB id is refused", func(t *testing.T) {
		hugeID := strings.Repeat("x", 1_100_000)
		dir := t.TempDir()
		writeCommitmentFile(t, dir, "bad.csv",
			commitmentRowFields(hugeID, "cud", "Used", "10", "normalized-hours",
				"200.000000", "2026-09-01T00:00:00Z", "2027-09-01T00:00:00Z"),
		)
		msg, db, err := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "byte limit") {
			t.Errorf("the refusal does not name the byte limit: %s", msg)
		}
		assertNoCommitmentRows(t, db)
	})

	t.Run("ChargeCategory Purchase with no CommitmentDiscountId is refused, not silently dropped", func(t *testing.T) {
		dir := t.TempDir()
		writeCommitmentFile(t, dir, "bad.csv",
			commitmentRowFields("", "cud", "Used", "10", "normalized-hours",
				"200.000000", "2026-09-01T00:00:00Z", "2027-09-01T00:00:00Z"),
		)
		msg, db, err := importFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "CommitmentDiscountId is empty") {
			t.Errorf("the refusal does not name the missing id: %s", msg)
		}
		assertNoCommitmentRows(t, db)
		var calls int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ai_calls`).Scan(&calls); err != nil {
			t.Fatal(err)
		}
		if calls != 0 {
			t.Errorf("ai_calls has %d row(s); an id-less Purchase row must not fall through to it", calls)
		}
	})
}

// TestCommitmentCostIsNeverParsedThroughFloat64 mirrors
// TestCostIsNeverParsedThroughFloat64 for parseCommitmentRow's own BilledCost
// parse.
func TestCommitmentCostIsNeverParsedThroughFloat64(t *testing.T) {
	dir := t.TempDir()
	writeCommitmentFile(t, dir, "good.csv",
		commitmentRowFields("precise-1", "cud", "Used", "1", "hours",
			"0.000249", "2026-09-01T00:00:00Z", "2027-09-01T00:00:00Z"),
	)
	_, db, err := importFrom(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	var cents int64
	if err := db.QueryRow(`SELECT monthly_cents FROM commitments WHERE id='precise-1'`).
		Scan(&cents); err != nil {
		t.Fatal(err)
	}
	if cents != 0 {
		t.Errorf("monthly_cents = %d, want 0 ($0.000249 rounds to zero cents on its own row, "+
			"the same half-away-from-zero rule money.ParseMicros.Cents() applies everywhere else)", cents)
	}
}

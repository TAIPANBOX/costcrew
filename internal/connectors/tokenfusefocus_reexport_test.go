package connectors

// Invariant 89: a second export of the same calls replaces the first rather
// than being counted beside it, and an export from TokenFuse 1.7.0 keeps the
// credential and the block reason it carries.
//
// The shape is the one TokenFuse 1.7.0's own release describes (its invariant
// 81): before 1.7.0 a call refused for identity was exported under the agent
// id it claimed, its victim; from 1.7.0 the same call is exported under
// key:<key_id> with x_agent_id empty, and two columns are appended, x_key_id
// and x_block_reason. Re-exporting the same trace with 1.7.0 writes a new file
// with a new hash, and ai_calls was keyed by file hash and row number.

import (
	"database/sql"
	"strings"
	"testing"
)

const focusHeader170 = focusHeader + ",x_key_id,x_block_reason"

// call is one gateway call as both exports see it; only where it is filed,
// and the two appended columns, differ between them.
type reexportCall struct {
	ts, run, model, tin, tout, cost string
	blocked                         bool
	agent                           string // what a pre-1.7.0 export files it under
	resource170, agent170           string // what a 1.7.0 export files it under
	key, reason                     string
}

func (c reexportCall) fields(resource, agent string) []string {
	f := focusRowFields()
	f[0], f[1] = c.cost, c.cost
	f[3], f[4] = c.ts, c.ts
	f[11], f[12] = resource, resource
	f[15], f[16] = c.run, ""
	f[17] = agent
	f[18] = c.model
	f[19], f[20] = c.tin, c.tout
	f[21], f[22] = "false", "settled"
	if c.blocked {
		f[21], f[22] = "true", "blocked"
	}
	return f
}

func (c reexportCall) old() string {
	return strings.Join(c.fields(c.agent, c.agent), ",")
}

func (c reexportCall) new170() string {
	return strings.Join(append(c.fields(c.resource170, c.agent170), c.key, c.reason), ",")
}

// theTrace is three calls: two honest ones from flint and one call an
// imposter made claiming to be the victim, which the gateway refused.
var theTrace = []reexportCall{
	{ts: "2026-10-07T10:00:00.120Z", run: "run-1", model: "claude-haiku-4-5", tin: "100", tout: "50",
		cost: "0.050000", agent: "agent://acme/flint",
		resource170: "agent://acme/flint", agent170: "agent://acme/flint", key: "flint-key"},
	{ts: "2026-10-07T10:00:01.480Z", run: "run-1", model: "claude-haiku-4-5", tin: "200", tout: "80",
		cost: "0.090000", agent: "agent://acme/flint",
		resource170: "agent://acme/flint", agent170: "agent://acme/flint", key: "flint-key"},
	{ts: "2026-10-07T10:05:00.000Z", run: "run-2", model: "claude-haiku-4-5", tin: "0", tout: "0",
		cost: "0", blocked: true, agent: "agent://acme/victim",
		resource170: "key:imposter", agent170: "", key: "imposter", reason: "identity_mismatch"},
}

func oldExport() string {
	var b strings.Builder
	b.WriteString(focusHeader + "\n")
	for _, c := range theTrace {
		b.WriteString(c.old() + "\n")
	}
	return b.String()
}

func export170() string {
	var b strings.Builder
	b.WriteString(focusHeader170 + "\n")
	for _, c := range theTrace {
		b.WriteString(c.new170() + "\n")
	}
	return b.String()
}

type aiTotals struct {
	calls, blocked int
	micros, cents  int64
	charges        int
}

func readAITotals(t *testing.T, db *sql.DB) aiTotals {
	t.Helper()
	var a aiTotals
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(blocked),0), COALESCE(SUM(billed_microusd),0)
		FROM ai_calls`).Scan(&a.calls, &a.blocked, &a.micros); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(billed_cents),0) FROM charges
		WHERE provenance='tokenfuse-focus'`).Scan(&a.charges, &a.cents); err != nil {
		t.Fatal(err)
	}
	return a
}

func countWhere(t *testing.T, db *sql.DB, where string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_calls WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatalf("counting ai_calls where %s: %v", where, err)
	}
	return n
}

// TestAReExportOfTheSameCallsReplacesTheEarlierRows is the defect as an
// operator meets it: the folder already holds a pre-1.7.0 export, TokenFuse is
// upgraded, the same trace is exported again, and the import is run again.
func TestAReExportOfTheSameCallsReplacesTheEarlierRows(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := t.TempDir()
	writeFocusFile(t, dir, "focus-2026-10-07.csv", oldExport())
	configureFocus(t, db, dir)
	if _, err := Import(db, "tokenfuse-focus", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	before := readAITotals(t, db)
	if before.calls != 3 || before.blocked != 1 || before.micros != 140000 {
		t.Fatalf("sanity: the first export gave %+v, want 3 calls, 1 blocked, 140000 micros", before)
	}
	if n := countWhere(t, db, `agent='agent://acme/victim' AND blocked=1`); n != 1 {
		t.Fatalf("sanity: the pre-1.7.0 export files the refusal under its victim, got %d rows", n)
	}

	writeFocusFile(t, dir, "focus-2026-10-07-v170.csv", export170())
	msg, err := Import(db, "tokenfuse-focus", false, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	after := readAITotals(t, db)
	if after != before {
		t.Errorf("the same three calls exported twice are counted twice: %+v before the re-export, %+v after", before, after)
	}
	if n := countWhere(t, db, `agent='agent://acme/victim'`); n != 0 {
		t.Errorf("the victim still carries %d row(s) after the 1.7.0 export filed the refusal under the key", n)
	}
	if n := countWhere(t, db, `agent='key:imposter' AND blocked=1 AND key_id='imposter' AND block_reason='identity_mismatch'`); n != 1 {
		t.Errorf("the refusal under key:imposter with its key and reason: %d row(s), want 1", n)
	}
	if !strings.Contains(msg, "3 calls appeared in more than one export of the same trace and were counted once") {
		t.Errorf("the import does not say it replaced the earlier export's rows: %s", msg)
	}
	if !strings.Contains(msg, "3 rows, 2 distinct agents, 0.14 total BilledCost") {
		t.Errorf("the import's figures count the calls twice: %s", msg)
	}

	// And it stays converged: every import re-reads the whole folder.
	if _, err := Import(db, "tokenfuse-focus", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if again := readAITotals(t, db); again != before {
		t.Errorf("a third import moved the totals: %+v, want %+v", again, before)
	}
}

// TestAnOlderExportNeverDisplacesANewerOne holds the order the folder is read
// in out of the answer: the 1.7.0 export sorts FIRST here, so a rule of "the
// last file read wins" would put the victim's refusal back.
func TestAnOlderExportNeverDisplacesANewerOne(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := t.TempDir()
	writeFocusFile(t, dir, "a-exported-by-1.7.0.csv", export170())
	writeFocusFile(t, dir, "b-exported-by-1.6.1.csv", oldExport())
	configureFocus(t, db, dir)
	for i := 0; i < 2; i++ {
		if _, err := Import(db, "tokenfuse-focus", false, ImportOptions{}); err != nil {
			t.Fatal(err)
		}
		if n := countWhere(t, db, `agent='agent://acme/victim'`); n != 0 {
			t.Errorf("import %d: an export from before 1.7.0 displaced the newer one; the victim carries %d row(s)", i+1, n)
		}
		if n := countWhere(t, db, `1=1`); n != 3 {
			t.Errorf("import %d: %d rows, want the 3 calls once each", i+1, n)
		}
		if n := countWhere(t, db, `key_id IS NULL`); n != 0 {
			t.Errorf("import %d: %d row(s) kept from the export without the key column", i+1, n)
		}
	}
}

// TestTwoIdenticalCallsInOneExportStayTwo holds the other side of the
// identity: it leaves the agent out, so two calls that agree on everything
// else could look like one. A file never supersedes its own rows, and a second
// export carrying both keeps both.
func TestTwoIdenticalCallsInOneExportStayTwo(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := t.TempDir()
	c := theTrace[0]
	twin := c
	twin.agent, twin.resource170, twin.agent170 = "agent://acme/flint-2", "agent://acme/flint-2", "agent://acme/flint-2"
	writeFocusFile(t, dir, "one.csv", focusHeader+"\n"+c.old()+"\n"+twin.old()+"\n")
	configureFocus(t, db, dir)
	if _, err := Import(db, "tokenfuse-focus", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	writeFocusFile(t, dir, "two.csv", focusHeader170+"\n"+c.new170()+"\n"+twin.new170()+"\n")
	if _, err := Import(db, "tokenfuse-focus", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, db, `1=1`); n != 2 {
		t.Errorf("two calls that agree on run, instant, model, tokens and amount: %d row(s), want 2", n)
	}
}

// TestTheKeyAndTheBlockReasonAreKeptWhenTheExportCarriesThem: NULL when the
// file's header has no such column, the value (or "") when it has, so a row
// says which export wrote it.
func TestTheKeyAndTheBlockReasonAreKeptWhenTheExportCarriesThem(t *testing.T) {
	_, db, err := importFrom(t, dirWith(t, "old.csv", oldExport()))
	if err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, db, `key_id IS NULL AND block_reason IS NULL`); n != 3 {
		t.Errorf("an export from before 1.7.0: %d of 3 rows with no key and no reason recorded", n)
	}

	_, db, err = importFrom(t, dirWith(t, "new.csv", export170()))
	if err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, db, `key_id='flint-key' AND block_reason=''`); n != 2 {
		t.Errorf("the two honest calls with their key and an empty reason: %d row(s), want 2", n)
	}
	if n := countWhere(t, db, `key_id='imposter' AND block_reason='identity_mismatch'`); n != 1 {
		t.Errorf("the refusal with its key and its reason: %d row(s), want 1", n)
	}
}

// TestAGatewayRefusalWithClientKeysOffIsNamedAsSuchNotAsBadData: with client
// keys off a 1.7.0 export files an identity refusal under no agent and no key.
// The import says what it is instead of listing it as a broken row.
func TestAGatewayRefusalWithClientKeysOffIsNamedAsSuchNotAsBadData(t *testing.T) {
	refused := theTrace[2]
	refused.resource170, refused.agent170, refused.key = "", "", ""
	honest := theTrace[0]
	honest.key = ""
	msg, db, err := importFrom(t, dirWith(t, "keys-off.csv",
		focusHeader170+"\n"+honest.new170()+"\n"+refused.new170()+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "1 call the gateway refused (1 identity_mismatch) names no agent and no key") ||
		!strings.Contains(msg, "not bad data") {
		t.Errorf("the import does not say the row is the gateway's refusal: %s", msg)
	}
	if strings.Contains(msg, "no agent: x_agent_id and ResourceId are both empty") || strings.Contains(msg, "refused:") {
		t.Errorf("the gateway's refusal is listed as a broken row: %s", msg)
	}
	if n := countWhere(t, db, `1=1`); n != 1 {
		t.Errorf("%d row(s) kept, want the honest call only", n)
	}

	// A row with no agent that the gateway did NOT block is still bad data.
	bad := theTrace[0]
	bad.resource170, bad.agent170, bad.key = "", "", ""
	msg, _, err = importFrom(t, dirWith(t, "bad.csv", focusHeader170+"\n"+bad.new170()+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "no agent: x_agent_id and ResourceId are both empty") {
		t.Errorf("an unblocked row with no agent is no longer refused as one: %s", msg)
	}
}

// TestHostileKeyAndBlockReasonAreRefusedByName: both columns are printed on
// /ai, so both are held to the printed-name rule.
func TestHostileKeyAndBlockReasonAreRefusedByName(t *testing.T) {
	for name, edit := range map[string]func(c *reexportCall){
		"a key with a line break":    func(c *reexportCall) { c.key = "flint\u2028key" },
		"a key over the byte limit":  func(c *reexportCall) { c.key = strings.Repeat("k", 257) },
		"a reason with a direction":  func(c *reexportCall) { c.reason = "identity\u202emismatch" },
		"a reason read as a formula": func(c *reexportCall) { c.reason = "=HYPERLINK(1)" },
	} {
		t.Run(name, func(t *testing.T) {
			c := theTrace[2]
			edit(&c)
			msg, db, err := importFrom(t, dirWith(t, "x.csv", focusHeader170+"\n"+c.new170()+"\n"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(msg, "1 row refused") {
				t.Errorf("not refused: %s", msg)
			}
			if n := countWhere(t, db, `1=1`); n != 0 {
				t.Errorf("%d row(s) kept", n)
			}
		})
	}
}

func dirWith(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	writeFocusFile(t, dir, name, content)
	return dir
}

package connectors

// Invariant 100: TokenFuse 1.8.0 appended x_identity_reason to its FOCUS
// export (its invariant 87). On a call TOKENFUSE_IDENTITY_STRICT=warn let
// through although the identity check failed, it names the check; the row is
// otherwise an ordinary spent call filed under the agent id it claimed, so
// without the column a forwarded call reads exactly like an honest one.
//
// The fixture is six calls in the shape TokenFuse's own focusexport tests
// write: one honest call, three admitted under warn mode (two teams, two
// runs), one refusal for identity, and one warned call the Breaker then
// blocked for budget.

import (
	"os"
	"strings"
	"testing"
)

const export180 = "testdata/tokenfuse-focus-1.8.0-2026-10-14.csv"

func TestTheIdentityReasonIsKeptWhenTheExportCarriesIt(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	configureFocus(t, db, copyIntoDir(t, export180))
	msg, err := Import(db, "tokenfuse-focus", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	if n := countWhere(t, db, `identity_reason IS NULL`); n != 0 {
		t.Errorf("%d row(s) of a 1.8.0 export were stored with no identity_reason, as though the column were absent", n)
	}
	if n := countWhere(t, db, `identity_reason='agent_id_not_allowed' AND blocked=0`); n != 2 {
		t.Errorf("the warn-mode calls' finding: %d admitted rows carry agent_id_not_allowed, want 2", n)
	}
	if n := countWhere(t, db, `identity_reason='' AND agent='agent://acme.example/finops/flint' AND run_id='run-1'`); n != 1 {
		t.Errorf("the honest call: %d rows with an empty identity_reason, want 1", n)
	}
	if n := countWhere(t, db, `agent='key:imposter' AND identity_reason='agent_id_not_allowed'`); n != 1 {
		t.Errorf("the refusal for identity does not keep the check that refused it (%d rows)", n)
	}
	if n := countWhere(t, db, IdentityWarnedSQL); n != 3 {
		t.Errorf("IdentityWarnedSQL counts %d forwarded calls, want 3: a refusal and a Breaker block are not forwarded", n)
	}
	for _, want := range []string{"3 calls (0.16) were forwarded under identity warn mode",
		"counted as spend under the agent id each claimed"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the import's sentence does not say %q:\n%s", want, msg)
		}
	}
}

func TestAnExportWithoutTheColumnStoresNoIdentityReason(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	configureFocus(t, db, copyIntoDir(t, "testdata/tokenfuse-focus-1.7.0-2026-10-07.csv"))
	msg, err := Import(db, "tokenfuse-focus", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	if n := countWhere(t, db, `identity_reason IS NOT NULL`); n != 0 {
		t.Errorf("%d row(s) of a 1.7.0 export claim to report identity warn; the export has no such column", n)
	}
	if strings.Contains(msg, "identity warn") {
		t.Errorf("a 1.7.0 import's sentence speaks of identity warn mode: %s", msg)
	}
}

// TestAWarnMarkedExportIsKeptOverAnOlderCopyOfTheSameCalls is invariant 89's
// supersede rule one generation on: the same trace exported by 1.7.0 and by
// 1.8.0 is one set of calls, and the copy kept is the one that says which of
// them warn mode forwarded, whichever file the folder lists last.
func TestAWarnMarkedExportIsKeptOverAnOlderCopyOfTheSameCalls(t *testing.T) {
	data, err := os.ReadFile(export180)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	// The 1.7.0 export of the same six calls: the same columns less the last.
	var older strings.Builder
	for _, l := range lines {
		older.WriteString(l[:strings.LastIndex(l, ",")] + "\n")
	}
	dir := t.TempDir()
	// "a-" sorts first and "z-" last: read in that order, the 1.7.0 copy is
	// the later file of what used to be one generation, and won.
	writeFocusFile(t, dir, "a-focus-1.8.0.csv", string(data))
	writeFocusFile(t, dir, "z-focus-1.7.0.csv", older.String())

	st := openFocusStore(t)
	db := st.DB()
	configureFocus(t, db, dir)
	msg, err := Import(db, "tokenfuse-focus", false, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	if n := countWhere(t, db, `1=1`); n != 6 {
		t.Fatalf("the same six calls in two exports are held as %d rows, want 6", n)
	}
	if n := countWhere(t, db, IdentityWarnedSQL); n != 3 {
		t.Errorf("after both exports %d call(s) read as forwarded under warn mode, want 3: "+
			"the 1.7.0 copy displaced the 1.8.0 one and the marker was lost", n)
	}
	if !strings.Contains(msg, "3 calls (0.16) were forwarded under identity warn mode") {
		t.Errorf("the sentence does not count the forwarded calls once, from the store:\n%s", msg)
	}

	// And a third import, the folder unchanged, changes nothing.
	if _, err := Import(db, "tokenfuse-focus", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, db, IdentityWarnedSQL); n != 3 {
		t.Errorf("a re-import moved the forwarded count to %d", n)
	}
}

func TestHostileIdentityReasonIsRefusedByName(t *testing.T) {
	data, err := os.ReadFile(export180)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	header, warned := lines[0], lines[2] // row 2 is a call admitted under warn mode
	base := warned[:strings.LastIndex(warned, ",")+1]
	for name, value := range map[string]string{
		"a control character":   "agent_id\x07not_allowed",
		"a line separator":      "agent_id\u2028not_allowed",
		"a formula":             "=HYPERLINK(1)",
		"over 64 bytes":         strings.Repeat("a", 65),
		"a zero-width joiner":   "agent_id\u200dnot_allowed",
		"text direction":        "agent_id\u202enot_allowed",
		"invalid UTF-8":         "agent_id\xffnot_allowed",
		"a leading at sign":     "@agent_id_not_allowed",
		"a leading plus":        "+agent_id_not_allowed",
		"a leading minus":       "-agent_id_not_allowed",
		"a carriage return":     "agent_id\rnot_allowed",
		"a quoted line feed":    "\"agent_id\nnot_allowed\"",
		"a byte order mark":     "\ufeffagent_id_not_allowed",
		"a soft hyphen":         "agent_id\u00adnot_allowed",
		"a paragraph separator": "agent_id\u2029not_allowed",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFocusFile(t, dir, "focus.csv", header+"\n"+base+value+"\n")
			msg, db, err := importFrom(t, dir)
			if err != nil {
				t.Fatalf("one hostile row refused the whole import: %v", err)
			}
			if n := countWhere(t, db, `1=1`); n != 0 {
				t.Errorf("a row whose x_identity_reason is %s was imported", name)
			}
			if !strings.Contains(msg, "1 row refused") || !strings.Contains(msg, "x_identity_reason") {
				t.Errorf("the refusal does not name the row and the column:\n%s", msg)
			}
		})
	}
	// The bound is 64 bytes, inclusive.
	dir := t.TempDir()
	writeFocusFile(t, dir, "focus.csv", header+"\n"+base+strings.Repeat("a", 64)+"\n")
	if _, db, err := importFrom(t, dir); err != nil || countWhere(t, db, IdentityWarnedSQL) != 1 {
		t.Errorf("a 64-byte finding is refused (%v); the bound is inclusive", err)
	}
}

func TestTheIdentityReasonColumnIsAddedToAStoreThatPredatesIt(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	if err := EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE ai_calls DROP COLUMN identity_reason`); err != nil {
		t.Fatalf("simulating a store from before the column: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := EnsureFocusSchema(db); err != nil {
			t.Fatalf("EnsureFocusSchema on a store without identity_reason, pass %d: %v", i+1, err)
		}
	}
	if n := countWhere(t, db, `identity_reason IS NULL`); n != 0 {
		t.Fatal("sanity: an empty table counts nothing")
	}
	// The column is back, and an import writes it.
	configureFocus(t, db, copyIntoDir(t, export180))
	if _, err := Import(db, "tokenfuse-focus", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, db, IdentityWarnedSQL); n != 3 {
		t.Errorf("after the column was added back, %d forwarded calls were read, want 3", n)
	}
}

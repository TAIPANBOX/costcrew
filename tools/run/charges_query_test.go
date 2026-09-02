package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// Every hostile input B2-SPEC.md section 3.3 names, each refused by name
// (never merely "an error"), and never touching a row.
//
// Red first, against the code before this step: charges_query.go was a
// stub returning "charges_query is not implemented yet" for everything, so
// every one of these failed -- including the two that must be ALLOWED,
// which is exactly why a stub that only ever refuses is not a substitute
// for the real checks: it would pass every "must refuse" case for the
// wrong reason and fail every "must allow" case.
//
// Extended after review of PR #20 with the inputs that review named --
// FROM (SELECT 1) x, analysts and friends, main./temp., every quoting
// form, pragma_table_info, sqlite_schema, and a CTE reading a disallowed
// table -- and plain WITH moved from the "must allow" list to "must
// refuse": a CTE is a derived table by another name and this tool's three
// allowed tables need none of it.
func TestChargesQueryHostileInputs(t *testing.T) {
	cases := []struct {
		name   string
		sql    string
		refuse bool
		needle string // substring the refusal must name, when refuse is true
	}{
		{"a write statement outright", "DROP TABLE charges", true, "SELECT"},
		{"a second statement after a semicolon", "SELECT 1; DROP TABLE charges", true, ";"},
		{"a line comment", "SELECT * FROM charges -- x", true, "--"},
		{"a block comment", "SELECT * FROM charges /* */", true, "/*"},
		{"PRAGMA", "PRAGMA table_info(analysts)", true, "SELECT"},
		{"a disallowed table", "SELECT * FROM analysts", true, "analysts"},
		{"a disallowed table reached through UNION", "SELECT * FROM charges UNION SELECT name,1,1,1,1,1,1,1,1,1,1 FROM analysts", true, "analysts"},
		{"a disallowed table reached through a subquery", "SELECT (SELECT password_hash FROM accounts) FROM charges", true, "accounts"},
		{"sqlite_master", "SELECT * FROM sqlite_master", true, "sqlite_master"},
		{"a recursive CTE", "WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM r) SELECT n FROM r", true, "SELECT"},
		{"a statement far past the size cap", "SELECT * FROM charges WHERE service = '" + strings.Repeat("x", 1_100_000) + "'", true, "bytes"},
		{"a non-ASCII table name, not the allow-list under any alphabet",
			"SELECT * FROM таблиця", true, "таблиця"},
		{"select in lower case", "select * from charges", false, ""},
		{"a LIMIT past the cap, lowered rather than refused", "SELECT * FROM charges LIMIT 100000", false, ""},

		// PR #20 review: a derived table followed by a comma-continued
		// disallowed table.
		{"a derived table, then a disallowed table in the same comma list",
			"SELECT * FROM (SELECT 1) x, analysts", true, "analysts"},
		{"charges, a derived table, then a disallowed table",
			"SELECT 1 FROM charges, (SELECT 1) x, accounts", true, "accounts"},
		// A main./temp. schema qualification, refused as a bare prefix.
		{"a main. schema qualification", "SELECT * FROM main.accounts", true, "schema-qualified"},
		// Every quoting form SQLite accepts for an identifier.
		{"a double-quoted disallowed table", `SELECT * FROM "accounts"`, true, "accounts"},
		{"a backtick-quoted disallowed table", "SELECT * FROM `accounts`", true, "accounts"},
		{"a bracket-quoted disallowed table", "SELECT * FROM [accounts]", true, "accounts"},
		// pragma_*() table-valued functions: a FROM target exactly like a
		// real table, reading a table's own schema without ever writing
		// its name after FROM.
		{"a pragma_ table-valued function", "SELECT name FROM pragma_table_info('accounts')", true, "pragma_"},
		// sqlite_schema is sqlite_master's own alias in modern SQLite.
		{"sqlite_schema, sqlite_master's alias", "SELECT * FROM sqlite_schema", true, "sqlite_"},
		// A CTE reading a disallowed table, and a CTE naming only allowed
		// ones: both refused now, purely because WITH appears at all.
		{"a CTE reading a disallowed table", "WITH a AS (SELECT * FROM accounts) SELECT * FROM a", true, "SELECT"},
		{"a plain, non-recursive WITH reading only allowed tables",
			"WITH unused_cte AS (SELECT 1) SELECT * FROM charges", true, "SELECT"},
		// WITH nested inside a derived table, past where the "must start
		// with SELECT" check ever looks, AND naming its CTE "charges" so
		// it shadows the real table: tablesInSQL sees only the allowed
		// name "charges" (both the outer FROM and the CTE's own inner
		// FROM read as that literal word) and finds nothing to refuse --
		// the only thing standing between this statement and a model
		// reading fabricated numbers under the real table's name is the
		// whole-statement WITH check, proven directly by
		// TestATableNamedCTEPassesTablesInSQLButNotTheWithBan below.
		{"a WITH nested in a derived table, its CTE shadowing charges",
			`SELECT * FROM (WITH charges AS (SELECT 'fake' AS service) SELECT * FROM charges) y`,
			true, "WITH"},
	}

	roDB := chargesQueryTestDB(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			safe, err := checkChargesSQL(roDB, c.sql)
			if c.refuse {
				if err == nil {
					t.Fatalf("accepted, want refused; rewritten as: %s", safe)
				}
				if !strings.Contains(strings.ToUpper(err.Error()), strings.ToUpper(c.needle)) {
					t.Errorf("refused, but not by name: got %q, want it to name %q", err.Error(), c.needle)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused, want accepted: %v", err)
			}

			// And it actually runs, through the real read-only connection,
			// never panicking and never returning more than the cap.
			args, _ := json.Marshal(struct {
				SQL string `json:"sql"`
			}{c.sql})
			out, err := runChargesQueryTool(context.Background(), nil, roDB, args)
			if err != nil {
				t.Fatalf("an allowed statement failed to run: %v", err)
			}
			if out == "" {
				t.Error("an allowed statement returned nothing")
			}
		})
	}
}

// None of the refused inputs above ever reaches a row: run through the
// FULL tool (not only the static checker) against a store carrying a
// canary row in analysts, and require that row unchanged and its marker
// value absent from every result.
func TestChargesQueryHostileInputsNeverTouchARow(t *testing.T) {
	dir := t.TempDir()
	rwDB := chargesQueryTestDBAt(t, dir)
	const canaryMarker = "CANARY-SECRET-1a2b3c"
	if _, err := rwDB.Exec(`INSERT INTO analysts(name, role, state, reason)
		VALUES ('canary-analyst', 'canary', 'active', ?)`, canaryMarker); err != nil {
		t.Fatal(err)
	}
	roDB := roConnAt(t, dir)

	hostile := []string{
		"DROP TABLE charges",
		"SELECT 1; DROP TABLE charges",
		"SELECT * FROM charges -- x",
		"SELECT * FROM charges /* */",
		"PRAGMA table_info(analysts)",
		"SELECT * FROM analysts",
		"SELECT * FROM charges UNION SELECT name,1,1,1,1,1,1,1,1,1,1 FROM analysts",
		"SELECT (SELECT password_hash FROM accounts) FROM charges",
		"SELECT * FROM sqlite_master",
		"WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM r) SELECT n FROM r",
		"SELECT * FROM (SELECT 1) x, analysts",
		"SELECT 1 FROM charges, (SELECT 1) x, accounts",
		"SELECT * FROM main.accounts",
		`SELECT * FROM "accounts"`,
		"SELECT * FROM `accounts`",
		"SELECT * FROM [accounts]",
		"SELECT name FROM pragma_table_info('accounts')",
		"SELECT * FROM sqlite_schema",
		"WITH a AS (SELECT * FROM accounts) SELECT * FROM a",
		`SELECT * FROM (WITH charges AS (SELECT 'fake' AS service) SELECT * FROM charges) y`,
	}
	for _, sql := range hostile {
		args, _ := json.Marshal(struct {
			SQL string `json:"sql"`
		}{sql})
		out, err := runChargesQueryTool(context.Background(), nil, roDB, args)
		if err == nil {
			t.Errorf("%q was not refused, returned: %s", sql, out)
		}
		if strings.Contains(out, canaryMarker) {
			t.Errorf("%q leaked the canary marker into its result: %s", sql, out)
		}
	}

	var reason string
	var n int
	if err := rwDB.QueryRow(`SELECT COUNT(*), COALESCE(MAX(reason),'') FROM analysts
		WHERE name='canary-analyst'`).Scan(&n, &reason); err != nil {
		t.Fatal(err)
	}
	if n != 1 || reason != canaryMarker {
		t.Errorf("the canary row changed: count=%d reason=%q, want 1 and %q", n, reason, canaryMarker)
	}
}

// tablesInSQL's own comment used to claim a comma-separated table list
// stops being tracked once a derived table in front of it closes. Tested
// directly here, that claim is false: listOpenAt is keyed by paren depth,
// so closing the derived table's own depth leaves the outer FROM's list
// open for the comma that follows. Kept as its own test, separate from
// the table-driven one above, because it is what that comment now points
// readers at by name.
func TestADerivedTableDoesNotEndTheCommaList(t *testing.T) {
	got, err := tablesInSQL("SELECT * FROM (SELECT 1) x, analysts")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tb := range got {
		if tb == "analysts" {
			found = true
		}
	}
	if !found {
		t.Errorf("tablesInSQL(%q) = %v, want it to include analysts: the comma "+
			"after a derived table's close paren must still open the list for it",
			"SELECT * FROM (SELECT 1) x, analysts", got)
	}
}

// A CTE named "charges" shadows the real table, and tablesInSQL cannot
// tell the difference: its inner "FROM charges" (the CTE's own body) and
// the outer reference to the CTE both read as the literal word "charges",
// which is allowed. Proven directly, in isolation, exactly like
// TestWrapWithLimitCapsTheStatement and TestRefuseUnknownTablesCatchesARealDisallowedTable
// isolate their own layers: tablesInSQL alone finds nothing to refuse
// here, so the whole-statement WITH check is the ONLY thing standing
// between this statement and a model reading fabricated numbers under the
// real table's name.
func TestATableNamedCTEPassesTablesInSQLButNotTheWithBan(t *testing.T) {
	sql := `SELECT * FROM (WITH charges AS (SELECT 'fake' AS service) SELECT * FROM charges) y`

	tables, err := tablesInSQL(sql)
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range tables {
		if tb != "charges" {
			t.Fatalf("tablesInSQL(%q) = %v, this test's premise is that it finds "+
				"nothing but the allowed name \"charges\" -- it found something else, "+
				"which means this case no longer isolates the WITH check", sql, tables)
		}
	}

	roDB := chargesQueryTestDB(t)
	if _, err := checkChargesSQL(roDB, sql); err == nil {
		t.Fatal("checkChargesSQL accepted a CTE that shadows the real table charges")
	}
}

// refuseUnknownTables catches a real, disallowed table on its own,
// independent of tablesInSQL: called directly (not through
// checkChargesSQL's full pipeline), the way TestWrapWithLimitCapsTheStatement
// tests wrapWithLimit directly, because an end-to-end test cannot tell
// "this layer works" from "a different, redundant layer already caught
// it" -- exactly the shape of gap TestChargesQueryResultIsCappedAt200Rows
// had for wrapWithLimit, found by hand rather than by a red test.
// tablesInSQL turned out to already catch every hostile input this file
// constructs (see TestADerivedTableDoesNotEndTheCommaList above); this is
// the test that proves refuseUnknownTables is doing real work regardless.
func TestRefuseUnknownTablesCatchesARealDisallowedTable(t *testing.T) {
	roDB := chargesQueryTestDB(t)
	if err := refuseUnknownTables(roDB, "SELECT * FROM analysts"); err == nil {
		t.Fatal("refuseUnknownTables did not refuse a real, disallowed table")
	}
	// And it lets allowed tables, and text that is not the name of
	// anything real, straight through.
	if err := refuseUnknownTables(roDB, "SELECT service, billed_cents AS total FROM charges"); err != nil {
		t.Errorf("refuseUnknownTables refused allowed tables and ordinary column names: %v", err)
	}
}

// A connection opened through store.OpenReadOnly refuses a write, which is
// the mechanism the whole tool leans on: even a parser miss cannot write
// through it.
func TestOpenReadOnlyRefusesAWrite(t *testing.T) {
	dir := t.TempDir()
	chargesQueryTestDBAt(t, dir) // creates app.db read-write first
	roDB := roConnAt(t, dir)

	if _, err := roDB.Exec(`INSERT INTO charges(source, day, service, category, billed_cents) VALUES ('x','2026-01-01','y','Usage',100)`); err == nil {
		t.Fatal("a write through the read-only connection succeeded")
	}
}

// A statement that names only allow-listed tables and stays under the size
// cap has its result capped at 200 rows and says so.
func TestChargesQueryResultIsCappedAt200Rows(t *testing.T) {
	dir := t.TempDir()
	rwDB := chargesQueryTestDBAt(t, dir)
	for i := 0; i < 250; i++ {
		if _, err := rwDB.Exec(`INSERT INTO charges(source, day, service, category, billed_cents)
			VALUES ('aws', '2026-01-01', ?, 'Usage', 100)`, "svc"+itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	roDB := roConnAt(t, dir)
	args, _ := json.Marshal(struct {
		SQL string `json:"sql"`
	}{"SELECT service FROM charges"})
	out, err := runChargesQueryTool(context.Background(), nil, roDB, args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cut at 200 rows") {
		t.Errorf("250 rows queried with no LIMIT does not say it was cut:\n%s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// header + 200 rows + the count line
	if len(lines) != 202 {
		t.Errorf("got %d lines, want 202 (header, 200 rows, count line):\n%s", len(lines), out)
	}
}

// wrapWithLimit itself caps the statement it is given, independent of the
// in-memory truncation runChargesQueryTool also applies once the rows come
// back: that second truncation is a real, load-bearing safety net (it is
// what makes a 201st row impossible to leak even if the SQL-level cap were
// ever bypassed), but it also means an end-to-end test alone cannot tell
// "wrapWithLimit works" from "wrapWithLimit does nothing and the in-memory
// cut saved it" -- the mutant that drops wrapWithLimit passed
// TestChargesQueryResultIsCappedAt200Rows outright the first time this was
// tried, caught by hand rather than by a red test. This is the test that
// actually distinguishes the two layers.
func TestWrapWithLimitCapsTheStatement(t *testing.T) {
	got := wrapWithLimit("SELECT * FROM charges")
	if !strings.Contains(got, "LIMIT 201") {
		t.Fatalf("wrapWithLimit does not cap the statement at all: %q", got)
	}
	// And a statement that already asks for far more is still capped by the
	// OUTER limit, not left to whatever the inner one says.
	got2 := wrapWithLimit("SELECT * FROM charges LIMIT 100000")
	if !strings.Contains(got2, "LIMIT 201") {
		t.Fatalf("the outer cap is missing when the inner statement has its own LIMIT: %q", got2)
	}
}

// identifierTokens itself, across every quoting form, so a failure here
// points straight at the tokenizer rather than at whichever check happens
// to call it.
func TestIdentifierTokensReadsEveryQuotingForm(t *testing.T) {
	cases := []struct {
		sql  string
		want string
	}{
		{"SELECT * FROM accounts", "accounts"},
		{`SELECT * FROM "accounts"`, "accounts"},
		{"SELECT * FROM `accounts`", "accounts"},
		{"SELECT * FROM [accounts]", "accounts"},
		{"SELECT * FROM main.accounts", "accounts"},
	}
	for _, c := range cases {
		toks := identifierTokens(c.sql)
		found := false
		for _, tok := range toks {
			if strings.EqualFold(tok, c.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("identifierTokens(%q) = %v, want it to include %q", c.sql, toks, c.want)
		}
	}
	// A single-quoted string is never an identifier, whatever it contains.
	toks := identifierTokens("SELECT * FROM charges WHERE service = 'accounts'")
	for _, tok := range toks {
		if strings.EqualFold(tok, "accounts") {
			t.Errorf("identifierTokens read inside a string literal: %v", toks)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func chargesQueryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	chargesQueryTestDBAt(t, dir)
	return roConnAt(t, dir)
}

func chargesQueryTestDBAt(t *testing.T, dir string) *sql.DB {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.DB()
	for _, schema := range []string{crew.Schema, crew.RosterSchema, estate.SeedSchema} {
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO charges(source, day, service, category, billed_cents)
		VALUES ('aws','2026-01-01','Amazon EC2','Usage',12345)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func roConnAt(t *testing.T, dir string) *sql.DB {
	t.Helper()
	roDB, err := store.OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { roDB.Close() })
	return roDB
}

package main

// ai_calls_query: C7-SPEC.md section 4, "the charges_query hostile suite
// reused verbatim against the new tool". Red first, against the code
// before this step: ai_calls_query did not exist in the catalogue at all,
// checkAICallsSQL and runAICallsQueryTool were undefined, so every test
// below failed to compile.

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// TestAICallsQueryHostileInputs is TestChargesQueryHostileInputs' own list,
// reused verbatim (C7-SPEC.md section 4's own words) with two adjustments
// the different allow-list requires: every case naming "charges" now names
// "ai_calls" instead (ai_calls_query's one allowed table, where
// charges_query's hostile suite used charges as the ALLOWED table to prove
// a hostile statement around it), and "a disallowed table" now names
// charges itself, which charges_query allows and ai_calls_query must not.
func TestAICallsQueryHostileInputs(t *testing.T) {
	cases := []struct {
		name   string
		sql    string
		refuse bool
		needle string
	}{
		{"a write statement outright", "DROP TABLE ai_calls", true, "SELECT"},
		{"a second statement after a semicolon", "SELECT 1; DROP TABLE ai_calls", true, ";"},
		{"a line comment", "SELECT * FROM ai_calls -- x", true, "--"},
		{"a block comment", "SELECT * FROM ai_calls /* */", true, "/*"},
		{"PRAGMA", "PRAGMA table_info(analysts)", true, "SELECT"},
		{"a disallowed table", "SELECT * FROM analysts", true, "analysts"},
		// The one case charges_query's own suite could not name: charges
		// itself is real, is charges_query's own allowed table, and must
		// still be refused here, by name, because it is not ai_calls.
		{"charges, allowed for the other tool but not this one", "SELECT * FROM charges", true, "charges"},
		{"a disallowed table reached through UNION",
			"SELECT * FROM ai_calls UNION SELECT name,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1 FROM analysts",
			true, "analysts"},
		{"a disallowed table reached through a subquery",
			"SELECT (SELECT password_hash FROM accounts) FROM ai_calls", true, "accounts"},
		{"sqlite_master", "SELECT * FROM sqlite_master", true, "sqlite_master"},
		{"a recursive CTE", "WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM r) SELECT n FROM r", true, "SELECT"},
		{"a statement far past the size cap", "SELECT * FROM ai_calls WHERE model = '" + strings.Repeat("x", 1_100_000) + "'", true, "bytes"},
		{"a non-ASCII table name, not the allow-list under any alphabet",
			"SELECT * FROM таблиця", true, "таблиця"},
		{"select in lower case", "select * from ai_calls", false, ""},
		{"a LIMIT past the cap, lowered rather than refused", "SELECT * FROM ai_calls LIMIT 100000", false, ""},
		{"a derived table, then a disallowed table in the same comma list",
			"SELECT * FROM (SELECT 1) x, analysts", true, "analysts"},
		{"ai_calls, a derived table, then a disallowed table",
			"SELECT 1 FROM ai_calls, (SELECT 1) x, accounts", true, "accounts"},
		{"a main. schema qualification", "SELECT * FROM main.accounts", true, "schema-qualified"},
		{"a double-quoted disallowed table", `SELECT * FROM "accounts"`, true, "accounts"},
		{"a backtick-quoted disallowed table", "SELECT * FROM `accounts`", true, "accounts"},
		{"a bracket-quoted disallowed table", "SELECT * FROM [accounts]", true, "accounts"},
		{"a pragma_ table-valued function", "SELECT name FROM pragma_table_info('accounts')", true, "pragma_"},
		{"sqlite_schema, sqlite_master's alias", "SELECT * FROM sqlite_schema", true, "sqlite_"},
		{"a CTE reading a disallowed table", "WITH a AS (SELECT * FROM accounts) SELECT * FROM a", true, "SELECT"},
		{"a plain, non-recursive WITH reading only allowed tables",
			"WITH unused_cte AS (SELECT 1) SELECT * FROM ai_calls", true, "SELECT"},
		{"a WITH nested in a derived table, its CTE shadowing ai_calls",
			`SELECT * FROM (WITH ai_calls AS (SELECT 'fake' AS agent) SELECT * FROM ai_calls) y`,
			true, "WITH"},
		// C7-SPEC.md section 4's own hostile cases, named for this tool.
		{"a ResourceId with a quote in it, as a string literal, not a table reference",
			"SELECT agent FROM ai_calls WHERE agent = 'O''Brien''s Agent'", false, ""},
		{"a 1 MB x_outcome value, as a string literal",
			"SELECT agent FROM ai_calls WHERE outcome = '" + strings.Repeat("y", 1_000_000) + "'", true, "bytes"},
	}

	roDB := aiCallsQueryTestDB(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			safe, err := checkAICallsSQL(roDB, c.sql)
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
			args, _ := json.Marshal(struct {
				SQL string `json:"sql"`
			}{c.sql})
			out, err := runAICallsQueryTool(context.Background(), nil, roDB, args)
			if err != nil {
				t.Fatalf("an allowed statement failed to run: %v", err)
			}
			if out == "" {
				t.Error("an allowed statement returned nothing")
			}
		})
	}
}

// TestAICallsQueryAnswersOverAICallsAndRefusesCharges is C7-SPEC.md section
// 4's own red-first sentence for this tool, end to end through the
// catalogue and dispatcher, not only the static checker: "ai_calls_query
// refuses a query naming charges or accounts and answers one over
// ai_calls".
func TestAICallsQueryAnswersOverAICallsAndRefusesCharges(t *testing.T) {
	dir := t.TempDir()
	rwDB := aiCallsQueryTestDBAt(t, dir)
	roDB := roConnAt(t, dir)

	a := crew.Analyst{Name: "ai-spend", State: "active", Skills: []string{"ai-spend-analysis"}}

	goodArgs, _ := json.Marshal(struct {
		SQL string `json:"sql"`
	}{"SELECT agent, model FROM ai_calls"})
	res := dispatch(context.Background(), rwDB, roDB, a, "ai_calls_query", goodArgs, bus{})
	if res.Outcome != outcomeOK {
		t.Fatalf("a query over ai_calls was refused: outcome=%s text=%s", res.Outcome, res.Text)
	}
	if !strings.Contains(res.Text, "seed-agent") {
		t.Errorf("the answer does not carry the row this store holds: %s", res.Text)
	}

	badArgs, _ := json.Marshal(struct {
		SQL string `json:"sql"`
	}{"SELECT * FROM charges"})
	res = dispatch(context.Background(), rwDB, roDB, a, "ai_calls_query", badArgs, bus{})
	if res.Outcome != outcomeError {
		t.Fatalf("a query naming charges was not refused: outcome=%s text=%s", res.Outcome, res.Text)
	}
	if !strings.Contains(res.Text, "charges") {
		t.Errorf("the refusal does not name charges: %s", res.Text)
	}

	badArgs2, _ := json.Marshal(struct {
		SQL string `json:"sql"`
	}{"SELECT * FROM accounts"})
	res = dispatch(context.Background(), rwDB, roDB, a, "ai_calls_query", badArgs2, bus{})
	if res.Outcome != outcomeError {
		t.Fatalf("a query naming accounts was not refused: outcome=%s text=%s", res.Outcome, res.Text)
	}
}

// TestAICallsQueryIsGrantedByFiguresReadNotSQLReadonly is C7-SPEC.md
// section 2's own line: "by the figures-read right". A skill that grants
// only sql-readonly, with no figures-read alongside it, must still be
// refused this tool; every skill in mandate.go's own table that grants
// sql-readonly also grants figures-read, so this proves it directly against
// the catalogue definition rather than trusting that no skill will ever
// separate the two.
func TestAICallsQueryIsGrantedByFiguresReadNotSQLReadonly(t *testing.T) {
	def, ok := toolByName("ai_calls_query")
	if !ok {
		t.Fatal("ai_calls_query is not in the catalogue")
	}
	if def.Right != "figures-read" {
		t.Errorf("ai_calls_query.Right = %q, want \"figures-read\"", def.Right)
	}
}

// TestAICallsQueryRefusesAnAnalystWithNoRights is the dispatcher's own
// refusal path (dispatch.go, already proven generically for charges_query):
// a suspended analyst holds no rights at all, and ai_calls_query must
// refuse it by name rather than running the query.
func TestAICallsQueryRefusesAnAnalystWithNoRights(t *testing.T) {
	dir := t.TempDir()
	rwDB := aiCallsQueryTestDBAt(t, dir)
	roDB := roConnAt(t, dir)

	a := crew.Analyst{Name: "ai-spend", State: "suspended", Skills: []string{"ai-spend-analysis"}}
	args, _ := json.Marshal(struct {
		SQL string `json:"sql"`
	}{"SELECT agent FROM ai_calls"})
	res := dispatch(context.Background(), rwDB, roDB, a, "ai_calls_query", args, bus{})
	if res.Outcome != outcomeRefused {
		t.Fatalf("a suspended analyst's call was not refused: outcome=%s text=%s", res.Outcome, res.Text)
	}
}

// ------------------------------------------------------------------ helpers

// aiCallsQueryTestDB is chargesQueryTestDB's own shape, seeded with ai_calls
// (via connectors.EnsureFocusSchema) and one row, instead of charges.
func aiCallsQueryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	aiCallsQueryTestDBAt(t, dir)
	return roConnAt(t, dir)
}

func aiCallsQueryTestDBAt(t *testing.T, dir string) *sql.DB {
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
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ai_calls
		(file_sha256, row_no, ts, day, team, agent, run_id, parent_run_id,
		 provider, model, tokens_in, tokens_out, billed_microusd, blocked, basis,
		 outcome, tool_calls)
		VALUES ('seed',1,'2026-09-02T00:00:00Z','2026-09-02',NULL,'seed-agent',NULL,NULL,
		 'Anthropic','claude-haiku-4-5',100,50,3500,0,'settled','done',NULL)`); err != nil {
		t.Fatal(err)
	}
	return db
}

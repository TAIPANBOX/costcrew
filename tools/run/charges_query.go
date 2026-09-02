package main

// charges_query: the one tool that takes the model's own text into SQL.
//
// B2-SPEC.md section 3.3, tier T3 on its own. Everything below exists
// because the model's argument is not trusted the way every other tool's
// arguments are: a service name or a period string can only select which
// ALREADY-WRITTEN query runs, but "sql" IS the query, so this is the one
// place a wrong answer is silent in a way none of the other ten tools can
// produce.
//
// Five independent layers, and each is load-bearing on its own:
//
//  1. checkChargesSQL refuses anything that is not a single, well-formed
//     SELECT naming only charges, drivers or attribution -- text checks,
//     before the statement is ever prepared.
//  2. wrapWithLimit puts the checked statement inside
//     `SELECT * FROM (<it>) LIMIT 201`, so the row cap holds regardless of
//     what LIMIT clause (if any, however deep in a subquery) the statement
//     itself carries, rather than trying to find and rewrite one occurrence
//     of the word LIMIT in arbitrary text. See wrapWithLimit's own comment
//     for why this is not what B2-SPEC.md section 3.3 literally describes.
//  3. The statement runs on store.OpenReadOnly's connection, which the
//     database driver itself keeps in SQLite's query_only mode on every
//     physical connection it opens (internal/store/readonly.go), so even a
//     miss in (1) cannot write.
//  4. A context deadline, applied here even though the dispatcher already
//     applies its own generic one, so this tool's own timeout requirement
//     is provable without depending on being called through dispatch().
//  5. The result itself is capped at 200 rows on the way out, independent
//     of whatever the query returned.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// chargesQueryLimit is the row cap: 3.3's own number.
	chargesQueryLimit = 200
	// maxChargesQueryBytes refuses a statement long before it is anywhere
	// near SQLite's own limits: the hostile-input list's "a 1 MB statement"
	// is three orders of magnitude past this.
	maxChargesQueryBytes = 8 * 1024
	chargesQueryTimeout  = 5 * time.Second
)

// chargesAllowedTables is the whole allow-list. Nothing else -- not
// sqlite_master, not analysts, not accounts, not tasks, not artifacts -- is
// ever a valid FROM or JOIN target for this tool.
var chargesAllowedTables = map[string]bool{
	"charges":     true,
	"drivers":     true,
	"attribution": true,
}

// chargesBannedKeywords is PRAGMA, ATTACH/DETACH, RECURSIVE (which is what
// makes WITH RECURSIVE recursive; plain WITH is not in this list and stays
// allowed), and every write keyword SQLite has a statement form for.
// Checked as whole words, case-insensitively, anywhere in the statement --
// not only at its start -- because a write keyword hiding inside a
// subquery or after a UNION is exactly as dangerous as one at the front.
var chargesBannedKeywords = []string{
	"pragma", "attach", "detach", "recursive",
	"insert", "update", "delete", "drop", "alter", "create", "replace",
	"truncate", "vacuum", "reindex", "grant", "revoke", "savepoint",
	"release", "begin", "commit", "rollback", "trigger",
}

var chargesBannedRE = mustWordREs(chargesBannedKeywords)

func mustWordREs(words []string) map[string]*regexp.Regexp {
	out := make(map[string]*regexp.Regexp, len(words))
	for _, w := range words {
		out[w] = regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(w) + `\b`)
	}
	return out
}

// checkChargesSQL is every text rule 3.3 lists, applied in the order that
// fails cheapest first. It returns the TRIMMED statement, unchanged: this
// function decides whether the statement runs at all, and wrapWithLimit
// (called separately, only once this has approved something) is what
// bounds its result.
func checkChargesSQL(raw string) (string, error) {
	if len(raw) > maxChargesQueryBytes {
		return "", fmt.Errorf("the statement is %d bytes, refused: at most %d",
			len(raw), maxChargesQueryBytes)
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("an empty statement is refused")
	}
	if strings.Contains(trimmed, ";") {
		return "", fmt.Errorf("refused: a statement may not contain ; (one statement only)")
	}
	if strings.Contains(trimmed, "--") {
		return "", fmt.Errorf("refused: a statement may not contain a -- comment")
	}
	if strings.Contains(trimmed, "/*") {
		return "", fmt.Errorf("refused: a statement may not contain a /* comment")
	}
	if !startsWithSelectOrWith(trimmed) {
		return "", fmt.Errorf("refused: a statement must start with SELECT")
	}
	for _, kw := range chargesBannedKeywords {
		if chargesBannedRE[kw].MatchString(trimmed) {
			return "", fmt.Errorf("refused: %s is not allowed", strings.ToUpper(kw))
		}
	}

	tables, err := tablesInSQL(trimmed)
	if err != nil {
		return "", err
	}
	if len(tables) == 0 {
		return "", fmt.Errorf("refused: no table is named in the statement")
	}
	for _, tb := range tables {
		if !chargesAllowedTables[tb] {
			return "", fmt.Errorf(
				"refused: table %q is not one of charges, drivers, attribution", tb)
		}
	}
	return trimmed, nil
}

// startsWithSelectOrWith is 3.3's "the text must start with SELECT", plus
// the one carve-out the same rule names in the same breath: "plain WITH
// allowed". A statement opening with WITH still has to resolve to a SELECT
// eventually (SQLite has no other statement a WITH clause can front), and
// WITH RECURSIVE specifically is refused separately, by name, through
// chargesBannedKeywords -- not by rejecting every WITH here, which would
// refuse the ordinary, non-recursive CTE the same rule says is fine.
func startsWithSelectOrWith(trimmed string) bool {
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return false
	}
	return strings.EqualFold(fields[0], "select") || strings.EqualFold(fields[0], "with")
}

// -------------------------------------------------------------- table names

// tablesInSQL walks the statement's tokens and returns every distinct
// lower-cased identifier that follows a FROM or a JOIN, wherever it occurs
// -- including inside a subquery, since the scan is a single flat pass over
// every token rather than a recursive walk that has to find the subquery
// first. A derived table (FROM (SELECT ...)) is not itself a name, so it is
// skipped rather than guessed at; the SUBQUERY's own FROM/JOIN is still
// caught because the scan keeps going through the same parenthesised
// tokens, not because this case is special-cased.
//
// Known, accepted gap: a comma-separated table list that continues AFTER a
// derived table ("FROM (SELECT ...) x, drivers") stops being tracked once
// the derived table opens, so a further name after that comma is missed.
// None of 3.3's hostile inputs is shaped like that, and getting it exactly
// right needs a real parser; this is a conservative, stated limit rather
// than a silent one.
func tablesInSQL(sql string) ([]string, error) {
	toks := tokenizeSQL(sql)
	seen := map[string]bool{}
	var out []string

	depth := 0
	listOpenAt := map[int]bool{} // depth -> "a FROM/JOIN list is open here"
	expectName := false

	for _, tk := range toks {
		switch {
		case tk == "(":
			depth++
			expectName = false
		case tk == ")":
			delete(listOpenAt, depth)
			depth--
			expectName = false
		case tk == ",":
			if listOpenAt[depth] {
				expectName = true
			}
		case strings.EqualFold(tk, "FROM") || strings.EqualFold(tk, "JOIN"):
			listOpenAt[depth] = true
			expectName = true
		case expectName && isSQLIdent(tk):
			name := strings.ToLower(tk)
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
			expectName = false
		case isSQLKeywordBoundary(tk):
			listOpenAt[depth] = false
			expectName = false
		default:
			expectName = false
		}
	}
	return out, nil
}

// isSQLKeywordBoundary is every keyword that ends a table list without
// opening a new one: the alias right after a table name is NOT one of
// these, so "FROM charges c, drivers d" still finds both.
var sqlBoundaryWords = map[string]bool{
	"where": true, "group": true, "order": true, "limit": true,
	"having": true, "union": true, "on": true, "using": true,
	"select": true, "as": true, "and": true, "or": true,
}

func isSQLKeywordBoundary(tok string) bool {
	return sqlBoundaryWords[strings.ToLower(tok)]
}

func isSQLIdent(tok string) bool {
	if tok == "" {
		return false
	}
	for i := 0; i < len(tok); i++ {
		if !isIdentByte(tok[i]) {
			return false
		}
	}
	return true
}

func isIdentByte(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
		c >= 0x80 // any UTF-8 lead or continuation byte: an identifier is
	// captured as one opaque token rather than silently dropped, so a
	// non-ASCII table name is refused BY NAME rather than by accident.
}

// tokenizeSQL splits into identifiers/keywords and the single-character
// punctuation this scan cares about, skipping whitespace and single-quoted
// string literals (doubled ” is the escaped quote inside one) so a literal
// cannot smuggle a fake "FROM x" past the scan above. It never panics on
// any input, including invalid UTF-8: everything is indexed by byte, which
// Go allows unconditionally.
func tokenizeSQL(s string) []string {
	var out []string
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '\'':
			j := i + 1
			for j < len(s) {
				if s[j] == '\'' {
					if j+1 < len(s) && s[j+1] == '\'' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			i = j
		case c == ',' || c == '(' || c == ')':
			out = append(out, string(c))
			i++
		case isIdentByte(c):
			j := i
			for j < len(s) && isIdentByte(s[j]) {
				j++
			}
			out = append(out, s[i:j])
			i = j
		default:
			i++
		}
	}
	return out
}

// ----------------------------------------------------------------- the cap

// wrapWithLimit puts the checked statement inside an outer
// `SELECT * FROM (...) LIMIT n`, which caps the FINAL result at n rows
// regardless of what the inner statement asked for: no LIMIT at all, a
// LIMIT past the cap, or a LIMIT buried inside a subquery a text rewrite
// would never find. n is chargesQueryLimit+1 so the caller can tell "there
// were more" from "there were exactly the cap" and say "cut" only when true.
//
// B2-SPEC.md section 3.3 literally describes finding and rewriting one
// LIMIT clause in the statement's own text ("if it has a larger one, it is
// lowered"). That does not hold once the clause can be arbitrarily deep in
// a subquery -- a text search only ever finds the FIRST "LIMIT n" in the
// string, which can belong to an inner subquery while the outer query
// stays unbounded. Wrapping bounds the actual number of rows that come
// back regardless of nesting, which is the property the row cap exists
// for; the deviation is deliberate and this is where it is written down.
func wrapWithLimit(checked string) string {
	return "SELECT * FROM (\n" + checked + "\n) AS q LIMIT " + strconv.Itoa(chargesQueryLimit+1)
}

// ------------------------------------------------------------------- run it

func runChargesQueryTool(ctx context.Context, _, roDB *sql.DB, args json.RawMessage) (string, error) {
	var in struct {
		SQL string `json:"sql"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if roDB == nil {
		return "", fmt.Errorf("no read-only connection is configured for this run")
	}
	checked, err := checkChargesSQL(in.SQL)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, chargesQueryTimeout)
	defer cancel()

	rows, err := roDB.QueryContext(ctx, wrapWithLimit(checked))
	if err != nil {
		return "", fmt.Errorf("the query failed: %v", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	var out [][]string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return "", err
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			row[i] = chargesCellString(v)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	cut := len(out) > chargesQueryLimit
	if cut {
		out = out[:chargesQueryLimit]
	}
	return renderChargesTable(cols, out, cut), nil
}

func chargesCellString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		return x
	default:
		return fmt.Sprintf("%v", x)
	}
}

// renderChargesTable is the fixed-width table 3.3 asks for: column names,
// every row padded to its column's widest value, the row count, and "cut
// at 200 rows" exactly when the query actually had more.
func renderChargesTable(cols []string, rows [][]string, cut bool) string {
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = len(c)
	}
	for _, r := range rows {
		for i, v := range r {
			if len(v) > widths[i] {
				widths[i] = len(v)
			}
		}
	}
	var b strings.Builder
	writeRow := func(vals []string) {
		for i, v := range vals {
			fmt.Fprintf(&b, "%-*s  ", widths[i], v)
		}
		b.WriteString("\n")
	}
	writeRow(cols)
	for _, r := range rows {
		writeRow(r)
	}
	fmt.Fprintf(&b, "%d row(s)", len(rows))
	if cut {
		b.WriteString(", cut at 200 rows")
	}
	b.WriteString("\n")
	return b.String()
}

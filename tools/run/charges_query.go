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
// Six independent layers, and each is load-bearing on its own:
//
//  1. checkChargesSQL's own text checks refuse anything that is not a
//     single, well-formed SELECT: no PRAGMA/ATTACH/DETACH, no write
//     keyword, no ;, no comment, no WITH anywhere in the statement (a CTE
//     is a derived table by another name and this tool's three allowed
//     tables need none), no main./temp. schema qualification, and no
//     identifier starting with sqlite_ or pragma_ (SQLite's own system
//     tables and the pragma_*() table-valued functions, both callable
//     from a FROM clause exactly like a real table). None of these needs
//     a database round trip to decide, so all of them run first.
//  2. tablesInSQL, the FROM/JOIN walk: a first, cheap structural pass,
//     refusing anything it can already tell is not charges, drivers or
//     attribution.
//  3. refuseUnknownTables, the whole-statement identifier scan added
//     after review of PR #20: (2) tracks SQL structure (FROM, JOIN,
//     comma-lists, nesting depth) to decide which tokens are table
//     references, and structure-tracking code can be wrong about a
//     construct nobody thought to test against it. This layer does not
//     try to track structure at all: it tokenizes EVERY identifier the
//     statement contains, in every quoting form SQLite accepts (bare,
//     "double", `backtick`, [bracket]), and refuses the statement if any
//     one of them is the name of a real table or view this database
//     currently has (read fresh from sqlite_master on every call, so a
//     table added next month is covered with no code change) that is not
//     charges, drivers or attribution. A column or alias that happens not
//     to be a real table name passes through untouched; the only thing
//     this layer can ever refuse is text that names something real -- it
//     is also the one check here that needs the database, which is why it
//     runs last, after every cheaper check already had its say.
//  4. wrapWithLimit puts the checked statement inside
//     `SELECT * FROM (<it>) LIMIT 201`, so the row cap holds regardless of
//     what LIMIT clause (if any, however deep in a subquery) the statement
//     itself carries, rather than trying to find and rewrite one occurrence
//     of the word LIMIT in arbitrary text. See wrapWithLimit's own comment
//     for why this is not what B2-SPEC.md section 3.3 literally describes.
//  5. The statement runs on store.OpenReadOnly's connection, which the
//     database driver itself keeps in SQLite's query_only mode on every
//     physical connection it opens (internal/store/readonly.go), so even a
//     miss in (1)-(3) cannot write.
//  6. A context deadline, applied here even though the dispatcher already
//     applies its own generic one, so this tool's own timeout requirement
//     is provable without depending on being called through dispatch();
//     and the result itself is capped at 200 rows on the way out,
//     independent of whatever the query returned.

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
// ever a valid reference for this tool, in any position or any quoting.
var chargesAllowedTables = map[string]bool{
	"charges":     true,
	"drivers":     true,
	"attribution": true,
}

// chargesBannedKeywords is PRAGMA, ATTACH/DETACH, RECURSIVE, and every
// write keyword SQLite has a statement form for. Checked as whole words,
// case-insensitively, anywhere in the statement -- not only at its start --
// because a write keyword hiding inside a subquery or after a UNION is
// exactly as dangerous as one at the front.
//
// WITH is not in this list: it is refused unconditionally, everywhere in
// the statement, by checkChargesSQL directly, alongside every other
// whole-statement, no-legitimate-use-for-this-tool refusal that needs no
// database round trip (sqlite_*, pragma_*, main./temp.). A CTE is a
// derived table by another name and this tool's three allowed tables need
// none.
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

// withAnywhereRE catches WITH wherever it sits in the statement, not only
// at the start: `SELECT * FROM charges, (WITH x AS (SELECT * FROM
// accounts) SELECT * FROM x) y` opens its CTE inside a derived table, past
// where a plain "does the statement start with WITH" check would ever
// look.
var withAnywhereRE = regexp.MustCompile(`(?i)\bWITH\b`)

// mainTempSchemaRE catches a main. or temp. schema qualification wherever
// it sits, independent of whatever name follows the dot: the qualifier
// itself is refused, not only the cases where what follows happens to
// match a real, disallowed table.
var mainTempSchemaRE = regexp.MustCompile(`(?i)\b(main|temp)\s*\.`)

// checkChargesSQL is every text rule 3.3 lists, plus the checks review of
// PR #20 added, applied in the order that fails cheapest first:
// everything that needs no database round trip runs before
// refuseUnknownTables, the one check that does. It returns
// the TRIMMED statement, unchanged: this function decides whether the
// statement runs at all, and wrapWithLimit (called separately, only once
// this has approved something) is what bounds its result.
func checkChargesSQL(db *sql.DB, raw string) (string, error) {
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
	if !startsWithSelect(trimmed) {
		return "", fmt.Errorf("refused: a statement must start with SELECT")
	}
	for _, kw := range chargesBannedKeywords {
		if chargesBannedRE[kw].MatchString(trimmed) {
			return "", fmt.Errorf("refused: %s is not allowed", strings.ToUpper(kw))
		}
	}
	// WITH, and a main./temp. schema qualification, are refused by their
	// own shape alone -- neither needs the database round trip below, so
	// both run here, with the other checks that fail cheapest first.
	if withAnywhereRE.MatchString(trimmed) {
		return "", fmt.Errorf("refused: WITH is not allowed")
	}
	if loc := mainTempSchemaRE.FindString(trimmed); loc != "" {
		return "", fmt.Errorf("refused: schema-qualified names (%s) are not allowed",
			strings.TrimRight(loc, ". \t"))
	}
	// sqlite_* and pragma_* are refused by their own prefix alone too, in
	// any quoting form: no database round trip needed to know a name
	// starts with a reserved prefix.
	for _, tok := range identifierTokens(trimmed) {
		low := strings.ToLower(tok)
		if strings.HasPrefix(low, "sqlite_") {
			return "", fmt.Errorf("refused: %q is not allowed (sqlite_ is reserved)", tok)
		}
		if strings.HasPrefix(low, "pragma_") {
			return "", fmt.Errorf("refused: %q is not allowed (pragma_ is reserved)", tok)
		}
	}

	// tablesInSQL: the first, cheap pass over the statement's own FROM/JOIN
	// structure.
	tables, err := tablesInSQL(trimmed)
	if err != nil {
		return "", err
	}
	for _, tb := range tables {
		if !chargesAllowedTables[tb] {
			return "", fmt.Errorf(
				"refused: table %q is not one of charges, drivers, attribution", tb)
		}
	}
	if len(tables) == 0 {
		return "", fmt.Errorf("refused: no table is named in the statement")
	}

	// refuseUnknownTables: the one check that needs the database, run
	// last -- see its own comment for why a comprehensive, structure-independent
	// scan against the REAL schema still matters even though tablesInSQL
	// above already turned out to catch every hostile input this file's
	// own tests construct.
	if err := refuseUnknownTables(db, trimmed); err != nil {
		return "", err
	}
	return trimmed, nil
}

// startsWithSelect is 3.3's "the text must start with SELECT". Unlike the
// version this replaced, WITH is not accepted here: checkChargesSQL
// refuses WITH unconditionally now, so accepting it here only to refuse it
// a few lines later was the same rule stated twice, once wrong.
func startsWithSelect(trimmed string) bool {
	fields := strings.Fields(trimmed)
	return len(fields) > 0 && strings.EqualFold(fields[0], "select")
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
// This is the FIRST, cheap pass (checkChargesSQL runs it before the
// database round trip refuseUnknownTables needs). A comma list that
// continues past a derived table close paren -- "FROM (SELECT ...) x,
// drivers" -- IS still tracked correctly: listOpenAt is keyed by paren
// depth, and closing the derived table's depth only clears that depth's
// own flag, leaving the outer FROM's list open for the comma that follows
// (TestADerivedTableDoesNotEndTheCommaList proves it directly). A
// previous version of this comment claimed the opposite, un-tested; it was
// wrong about its own state machine. What this pass does NOT attempt,
// deliberately, is every quoting form or a dynamic notion of what a real
// table is -- that is refuseUnknownTables's job, run second and
// independently, so a mistake in this walk's structural tracking (found or
// not yet found) is not the only thing standing between the model's text
// and a table this tool does not allow.
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
//
// It does NOT understand double/backtick/bracket identifier quoting --
// those bytes fall into the default case below and are silently dropped,
// which happens to still surface the identifier inside them as a bare
// token (tablesInSQL benefits from that by accident, for the same reason
// checkChargesSQL does not rely on it alone: an accident is not a
// guarantee). identifierTokens, used by checkChargesSQL's own sqlite_/pragma_
// check and by refuseUnknownTables, is the one that understands all four
// forms on purpose.
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

// ------------------------------------------------ the whole-statement scan

// realTableNames reads every table and view this database currently has,
// straight from sqlite_master, lower-cased. Read fresh on every call
// rather than cached: a query that runs a handful of times per task, at
// most six times a round, does not need caching, and a cache is one more
// thing that could go stale the moment a table is added or dropped.
func realTableNames(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type IN ('table','view')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[strings.ToLower(name)] = true
	}
	return out, rows.Err()
}

// refuseUnknownTables is the whole-statement pass PR #20's review asked
// for: independent of tablesInSQL's FROM/JOIN structural walk above, it
// does not try to work out which clause an identifier sits in at all. It
// tokenizes EVERY identifier the statement contains, in every quoting
// form (identifierTokens), and refuses the statement if any one of them
// matches a real table or view this database CURRENTLY has (realTableNames,
// read fresh every call, so a table added next month is covered with no
// code change here) that is not charges, drivers or attribution.
//
// This is deliberately about REAL tables, not an enumerated deny-list: a
// column or alias that is not the name of anything in the schema passes
// straight through, whatever it is called, and the only database round
// trip this tool's checks make is the one this function needs -- which is
// why it runs last, after every check above has already had its cheaper
// say. WITH, sqlite_* and pragma_* are refused earlier, in checkChargesSQL
// itself, by their own shape alone: none of those three needs to know
// what tables exist to be refused.
func refuseUnknownTables(db *sql.DB, sql string) error {
	real, err := realTableNames(db)
	if err != nil {
		return fmt.Errorf("checking the schema: %v", err)
	}
	for _, tok := range identifierTokens(sql) {
		low := strings.ToLower(tok)
		if real[low] && !chargesAllowedTables[low] {
			return fmt.Errorf(
				"refused: table %q is not one of charges, drivers, attribution", tok)
		}
	}
	return nil
}

// identifierTokens extracts every identifier-shaped token from sql, in
// every form SQLite accepts one: bare (charges), "double quoted",
// `backtick quoted`, and [bracket quoted]. A single-quoted string is
// skipped whole, opaque, and never an identifier -- SQLite's own rule,
// and the reason 'accounts' inside a string literal is not a table
// reference while "accounts" is. It never panics on any input, including
// an unterminated quote: the reader functions below stop at end of string
// rather than index past it.
func identifierTokens(sql string) []string {
	var out []string
	i := 0
	for i < len(sql) {
		c := sql[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',' || c == '(' || c == ')' || c == '.':
			i++
		case c == '\'':
			i = skipQuoted(sql, i, '\'')
		case c == '"':
			tok, next := readQuoted(sql, i, '"')
			out = append(out, tok)
			i = next
		case c == '`':
			tok, next := readQuoted(sql, i, '`')
			out = append(out, tok)
			i = next
		case c == '[':
			tok, next := readBracketQuoted(sql, i)
			out = append(out, tok)
			i = next
		case isIdentByte(c):
			j := i
			for j < len(sql) && isIdentByte(sql[j]) {
				j++
			}
			out = append(out, sql[i:j])
			i = j
		default:
			i++
		}
	}
	return out
}

// readQuoted reads a "-quoted or `-quoted identifier starting at s[start],
// where doubling the quote character is the escape for a literal one
// inside it (SQLite's rule for both forms), and returns its unescaped
// content plus the index just past the closing quote.
func readQuoted(s string, start int, q byte) (content string, next int) {
	var b strings.Builder
	j := start + 1
	for j < len(s) {
		if s[j] == q {
			if j+1 < len(s) && s[j+1] == q {
				b.WriteByte(q)
				j += 2
				continue
			}
			j++
			break
		}
		b.WriteByte(s[j])
		j++
	}
	return b.String(), j
}

// readBracketQuoted reads a [bracket quoted] identifier starting at
// s[start]: SQL Server's form, which SQLite also accepts, with no
// doubling escape -- it simply reads to the first ].
func readBracketQuoted(s string, start int) (content string, next int) {
	j := start + 1
	k := j
	for k < len(s) && s[k] != ']' {
		k++
	}
	content = s[j:k]
	if k < len(s) {
		k++
	}
	return content, k
}

// skipQuoted advances past a '-quoted string literal starting at s[start],
// the same doubling-escape rule as readQuoted, without keeping its
// content: a string literal is never an identifier.
func skipQuoted(s string, start int, q byte) int {
	j := start + 1
	for j < len(s) {
		if s[j] == q {
			if j+1 < len(s) && s[j+1] == q {
				j += 2
				continue
			}
			j++
			break
		}
		j++
	}
	return j
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
	checked, err := checkChargesSQL(roDB, in.SQL)
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

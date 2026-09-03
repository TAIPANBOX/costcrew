package main

// ai_calls_query: the charges_query shape (C7-SPEC.md section 2), scoped to
// ai_calls only, and gated by figures-read rather than sql-readonly --
// ai-spend and unit-econ-ai already hold figures-read to read ai_calls at
// all (roles.yaml's own "reads" line for both families), and sql-readonly
// is charges_query's own broader right, over charges, drivers and
// attribution, unchanged by this file.
//
// Every layer charges_query.go's own header names is REUSED here, not
// copied: tokenizeSQL, identifierTokens, tablesInSQL, realTableNames,
// wrapWithLimit, chargesCellString, renderChargesTable, startsWithSelect,
// chargesBannedKeywords/chargesBannedRE, withAnywhereRE, mainTempSchemaRE
// and the three size/row/timeout constants are none of them specific to
// charges, drivers and attribution, and this file calls every one of them
// as-is. charges_query.go itself is left byte-for-byte untouched: three of
// its own gates-have-teeth.sh cases (lines naming
// "if !chargesAllowedTables[tb] {", "if real[low] && !chargesAllowedTables[low] {"
// and "if withAnywhereRE.MatchString(trimmed) {") plant their mutant by an
// exact literal match against that file's own text, and a refactor that
// pulled the allow-list check out into a shared, parameterised function
// would silently break all three (their python-driven apply_edits finds
// nothing to replace and reports BROKEN, not a caught fault). So the two
// checks that actually depend on WHICH tables are allowed --
// checkAICallsSQL's own table-name loop and refuseUnknownAICallsTables --
// are written out here again, short, and this file gets its own teeth case
// in gates-have-teeth.sh rather than sharing charges_query's three.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// aiCallsAllowedTables is the whole allow-list for this tool: ai_calls and
// nothing else, in any position, any quoting.
var aiCallsAllowedTables = map[string]bool{"ai_calls": true}

// checkAICallsSQL is checkChargesSQL's own sequence, table name for table
// name, scoped to ai_calls. See that function's comment for why each check
// runs in this order (cheapest, no-database-round-trip checks first).
func checkAICallsSQL(db *sql.DB, raw string) (string, error) {
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
	if withAnywhereRE.MatchString(trimmed) {
		return "", fmt.Errorf("refused: WITH is not allowed")
	}
	if loc := mainTempSchemaRE.FindString(trimmed); loc != "" {
		return "", fmt.Errorf("refused: schema-qualified names (%s) are not allowed",
			strings.TrimRight(loc, ". \t"))
	}
	for _, tok := range identifierTokens(trimmed) {
		low := strings.ToLower(tok)
		if strings.HasPrefix(low, "sqlite_") {
			return "", fmt.Errorf("refused: %q is not allowed (sqlite_ is reserved)", tok)
		}
		if strings.HasPrefix(low, "pragma_") {
			return "", fmt.Errorf("refused: %q is not allowed (pragma_ is reserved)", tok)
		}
	}

	tables, err := tablesInSQL(trimmed)
	if err != nil {
		return "", err
	}
	for _, tb := range tables {
		if !aiCallsAllowedTables[tb] {
			return "", fmt.Errorf("refused: table %q is not ai_calls", tb)
		}
	}
	if len(tables) == 0 {
		return "", fmt.Errorf("refused: no table is named in the statement")
	}

	if err := refuseUnknownAICallsTables(db, trimmed); err != nil {
		return "", err
	}
	return trimmed, nil
}

// refuseUnknownAICallsTables is refuseUnknownTables's own whole-statement
// scan, scoped to ai_calls: every identifier the statement contains, in
// every quoting form, checked against the live table list from
// sqlite_master, read fresh on every call.
func refuseUnknownAICallsTables(db *sql.DB, sql string) error {
	real, err := realTableNames(db)
	if err != nil {
		return fmt.Errorf("checking the schema: %v", err)
	}
	for _, tok := range identifierTokens(sql) {
		low := strings.ToLower(tok)
		if real[low] && !aiCallsAllowedTables[low] {
			return fmt.Errorf("refused: table %q is not ai_calls", tok)
		}
	}
	return nil
}

// runAICallsQueryTool is runChargesQueryTool's own body, scoped to ai_calls
// and reusing the same wrapWithLimit/chargesCellString/renderChargesTable
// this file's header names.
func runAICallsQueryTool(ctx context.Context, _, roDB *sql.DB, args json.RawMessage) (string, error) {
	var in struct {
		SQL string `json:"sql"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if roDB == nil {
		return "", fmt.Errorf("no read-only connection is configured for this run")
	}
	checked, err := checkAICallsSQL(roDB, in.SQL)
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

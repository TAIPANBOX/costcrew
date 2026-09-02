package main

// charges_query's own implementation is the next commit (B2-SPEC.md
// section 3.3, tier T3 on its own): this stub only registers the name and
// the right it needs, so the catalogue in tools.go compiles and the
// dispatcher's right-check tests (dispatch_test.go) can use charges_query
// as their example of a right-gated tool without depending on the tool's
// own body existing yet.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

func runChargesQueryTool(_ context.Context, _, _ *sql.DB, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("charges_query is not implemented yet")
}

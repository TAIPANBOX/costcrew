package store

// A second, read-only pool onto the same file.
//
// B2-SPEC.md section 3.3 asks for `charges_query`'s connection to be opened
// with `?mode=ro` in the DSN, plus `PRAGMA query_only = 1` on that
// connection. Checked against the vendored driver rather than assumed:
// modernc.org/sqlite v1.57.0's DSN parser (sqlite.go, applyQueryParams) has
// no "mode" key at all, only mattn-compatible shorthands it names one by
// one, so a literal `?mode=ro` would parse without error and do nothing.
// What it DOES carry is `_query_only`, applied inside newConn with
// `pragma query_only = <value>` -- which is the real mechanism SQLite
// itself offers here, so that is what this uses.
//
// It is set in the DSN rather than run as a statement after Open for a
// second reason beyond the name: *sql.DB is a POOL, not one connection.
// Go's database/sql can open more physical connections under concurrent
// load, and a `db.Exec("PRAGMA query_only = 1")` run once only binds
// whichever single connection served that call -- a later query on a
// connection the pool opens afterward would not be read-only at all. The
// DSN parameter is read inside the driver's own newConn, so the pragma is
// re-applied on every physical connection this pool ever opens, not just
// the first.
//
// TestOpenReadOnlyRefusesAWrite proves the one property that actually
// matters: a write attempted through this pool fails, on the file store.Open
// already has open read-write in the same process.

import (
	"database/sql"
	"path/filepath"
)

// OpenReadOnly opens a second connection pool onto the SAME app.db file
// store.Open uses, forced into SQLite's query_only mode on every physical
// connection it ever opens. It does not create the file or run migrate: a
// caller that opens this before store.Open has created app.db gets a
// missing-file error rather than a fresh empty database, which is the right
// failure for a read path to have.
func OpenReadOnly(dir string) (*sql.DB, error) {
	dsn := filepath.Join(dir, "app.db") +
		"?_pragma=busy_timeout(5000)&_query_only=1"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

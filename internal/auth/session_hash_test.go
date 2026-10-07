package auth_test

// Invariant 61: a session token is never stored. The cookie carries a random
// token; the database carries only its SHA-256, so a copy of app.db (a backup,
// a stray volume snapshot, a file read through some other fault) is not a set
// of live logins.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// open returns an auth over a store in dir, plus the store, so a test can look
// at the table behind it. The store is closed on cleanup.
func open(t *testing.T, dir string) (*auth.Auth, *store.Store) {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a, err := auth.New(st, dir)
	if err != nil {
		t.Fatal(err)
	}
	return a, st
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// everyByteOnDisk is app.db and its write-ahead log as a reader of the data
// directory would see them. The -wal is the part a test that only looked at
// app.db would miss: a row written a moment ago lives there until a checkpoint.
func everyByteOnDisk(t *testing.T, dir string) []byte {
	t.Helper()
	var all []byte
	for _, name := range []string{"app.db", "app.db-wal"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		all = append(all, b...)
	}
	if len(all) == 0 {
		t.Fatal("neither app.db nor its -wal holds a byte; this measured nothing")
	}
	return all
}

// storedRow returns every column of the one sessions row as text, whatever the
// columns are called, so a test about "what the database holds" does not pass
// or fail on a column's name.
func storedRow(t *testing.T, st *store.Store) map[string]string {
	t.Helper()
	rows, err := st.DB().Query(`SELECT * FROM sessions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	n := 0
	for rows.Next() {
		n++
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for i, c := range cols {
			switch v := vals[i].(type) {
			case string:
				out[c] = v
			case []byte:
				out[c] = string(v)
			}
		}
	}
	if n != 1 {
		t.Fatalf("sessions rows: %d, want 1", n)
	}
	return out
}

func signedIn(t *testing.T, a *auth.Auth) string {
	t.Helper()
	if ok, err := a.Create("alice", "alice-password-2026", "operator"); err != nil || !ok {
		t.Fatalf("creating alice: %v %v", ok, err)
	}
	token, err := a.StartSession("alice")
	if err != nil || token == "" {
		t.Fatalf("starting a session: %q %v", token, err)
	}
	return token
}

// The session table holds a hash and the file holds no copy of the cookie.
func TestTheDatabaseHoldsNoSessionTokenInTheClear(t *testing.T) {
	dir := t.TempDir()
	a, st := open(t, dir)
	token := signedIn(t, a)

	for col, v := range storedRow(t, st) {
		if v == token {
			t.Fatalf("the sessions table stores the cookie value itself, in column %q", col)
		}
	}
	var stored string
	if err := st.DB().QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatalf("the sessions table has no token_hash column: %v", err)
	}
	// Pinned to SHA-256 on purpose: a 256-bit random token has no dictionary
	// to attack, so a slow hash would cost every request and buy nothing, and
	// a reader of this test should see which function the claim is about.
	if want := sha256Hex(token); stored != want {
		t.Errorf("stored value %q, want the SHA-256 of the cookie %q", stored, want)
	}
	if bytes.Contains(everyByteOnDisk(t, dir), []byte(token)) {
		t.Errorf("the cookie value %q is readable in app.db or its -wal", token)
	}
}

// A hash read out of the database is not a login. This is the property the
// whole change exists for: before it, the stored value WAS the cookie.
func TestAHashReadFromTheDatabaseIsNotACookie(t *testing.T) {
	a, st := open(t, t.TempDir())
	token := signedIn(t, a)

	for col, v := range storedRow(t, st) {
		if u, err := a.SessionUser(v); err != nil || u != nil {
			who := "nobody"
			if u != nil {
				who = u.Username
			}
			t.Errorf("presenting column %q of the stored row as a cookie signed in as %s (err %v)", col, who, err)
		}
	}
	// And the real cookie still does, or the test above proves a broken login.
	if u, err := a.SessionUser(token); err != nil || u == nil || u.Username != "alice" {
		t.Fatalf("the cookie no longer resolves to its account: found=%v %v", u != nil, err)
	}
}

// Behaviour at the edges: ending a session ends it, an expired one is gone,
// and a stranger's string resolves to nobody without an error.
func TestSessionLifecycleByTheCookieValue(t *testing.T) {
	a, st := open(t, t.TempDir())
	token := signedIn(t, a)

	if err := a.EndSession(token); err != nil {
		t.Fatal(err)
	}
	if u, _ := a.SessionUser(token); u != nil {
		t.Error("the session still resolves after EndSession")
	}
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("rows left after EndSession: %d %v", n, err)
	}

	// Expired: moved into the past by hand, the clock is not injectable.
	token2, err := a.StartSession("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE sessions SET expires = ?`,
		float64(time.Now().Add(-time.Minute).UnixNano())/1e9); err != nil {
		t.Fatal(err)
	}
	if u, _ := a.SessionUser(token2); u != nil {
		t.Error("an expired session still resolves")
	}
	// Ending a session nobody holds is not an error (a second logout click).
	if err := a.EndSession("never-issued"); err != nil {
		t.Errorf("ending a session that never existed: %v", err)
	}
}

// Hostile cookie values. None may resolve, none may error, none may panic.
func TestSessionUserSurvivesHostileCookies(t *testing.T) {
	a, _ := open(t, t.TempDir())
	token := signedIn(t, a)

	cases := map[string]string{
		"empty":                "",
		"sql tail":             "' OR '1'='1",
		"sql comment":          token + "'--",
		"nul byte":             "abc\x00def",
		"one megabyte":         strings.Repeat("A", 1<<20),
		"the hash of a cookie": sha256Hex(token),
		"upper-cased cookie":   strings.ToUpper(token),
		"cookie plus space":    token + " ",
		"unicode":              "сесія-🙂",
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			if u, err := a.SessionUser(v); err != nil || u != nil {
				t.Errorf("SessionUser(%q...) = found=%v, %v; want nobody and no error", head(v), u != nil, err)
			}
		})
	}
}

func head(s string) string {
	if len(s) > 24 {
		return s[:24]
	}
	return s
}

// The CSRF token stays what it was: an HMAC of the cookie value under the
// installation's key. It is bound to what the browser holds, not to the
// storage key, so a page rendered before this change still verifies after it.
func TestCSRFStaysBoundToTheCookieValue(t *testing.T) {
	dir := t.TempDir()
	a, _ := open(t, dir)
	token := signedIn(t, a)

	key, err := os.ReadFile(filepath.Join(dir, ".session-secret"))
	if err != nil {
		t.Fatal(err)
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(token))
	want := hex.EncodeToString(m.Sum(nil))[:32]

	if got := a.CSRFToken(token); got != want {
		t.Errorf("CSRFToken = %s, want HMAC-SHA256(.session-secret, cookie)[:32] = %s", got, want)
	}
	if !a.CSRFOK(token, want) {
		t.Error("the token an old page carries no longer verifies")
	}
	if a.CSRFToken(sha256Hex(token)) == want {
		t.Error("the CSRF token is the same for the cookie and for its storage hash")
	}
	other, err := a.StartSession("alice")
	if err != nil {
		t.Fatal(err)
	}
	if a.CSRFOK(other, want) {
		t.Error("a CSRF token from one session verifies in another")
	}
}

// ---------------------------------------------------------------- migration

// legacyStore builds app.db exactly as a console before this change left it:
// the clear-text sessions table, one live session, WAL mode, then closed
// cleanly. It uses the driver directly because the point is a file the new
// code did not write.
func legacyStore(t *testing.T, dir, marker string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "app.db")+
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE users(username TEXT PRIMARY KEY, pw_hash TEXT, role TEXT DEFAULT 'viewer',
		  created REAL, last_login REAL, failed INTEGER DEFAULT 0, locked_until REAL DEFAULT 0)`,
		`CREATE TABLE sessions(token TEXT PRIMARY KEY, username TEXT, created REAL, expires REAL)`,
		`INSERT INTO users(username, pw_hash, role, created) VALUES ('alice','x','admin',1)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	future := float64(time.Now().Add(time.Hour).UnixNano()) / 1e9
	if _, err := db.Exec(`INSERT INTO sessions(token, username, created, expires) VALUES (?,?,?,?)`,
		marker, "alice", 1.0, future); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// Everybody signs in again once, and the old tokens leave no trace behind.
func TestOldClearTextSessionsAreEndedAndErasedByTheMigration(t *testing.T) {
	dir := t.TempDir()
	marker := "LEGACY-CLEARTEXT-SESSION-" + strings.Repeat("Zq9", 12)
	legacyStore(t, dir, marker)
	if !bytes.Contains(everyByteOnDisk(t, dir), []byte(marker)) {
		t.Fatal("the legacy fixture does not hold its own marker; the test would pass on anything")
	}

	a, st := open(t, dir)

	if u, err := a.SessionUser(marker); err != nil || u != nil {
		t.Errorf("a clear-text session from before the change still signs in (user found: %v, err %v)", u != nil, err)
	}
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("sessions rows after the migration: %d %v, want 0", n, err)
	}
	if bytes.Contains(everyByteOnDisk(t, dir), []byte(marker)) {
		t.Error("the old session token is still readable in app.db or its -wal after the migration")
	}
	// The accounts are not touched: this ends logins, it does not end people.
	if u, err := a.Get("alice"); err != nil || u == nil || u.Role != "admin" {
		t.Errorf("alice after the migration: found=%v %v", u != nil, err)
	}
	// And the migration says so in the audit chain.
	recs, err := st.JournalTail(5)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range recs {
		if r.Event == "sessions_reset" {
			found = true
		}
	}
	if !found {
		t.Errorf("no sessions_reset entry in the journal; the sign-out of every user left no record: %+v", recs)
	}
}

// The second start is the real test: a migration that runs on every open would
// sign everybody out on every restart.
func TestTheMigrationRunsOnceAndKeepsTheSessionsItDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	legacyStore(t, dir, "legacy-token-for-the-second-run-test-0123456789")

	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(st, dir)
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.StartSession("alice")
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	for i := 0; i < 2; i++ {
		a2, st2 := open(t, dir)
		if u, err := a2.SessionUser(token); err != nil || u == nil {
			t.Fatalf("restart %d: the session started after the migration no longer resolves: found=%v %v", i+1, u != nil, err)
		}
		var col string
		if err := st2.DB().QueryRow(`SELECT token_hash FROM sessions LIMIT 1`).Scan(&col); err != nil {
			t.Fatalf("restart %d: %v", i+1, err)
		}
		st2.Close()
	}
}

package store_test

// Invariant 61's migration: a database that still holds the clear-text
// sessions table is moved to the hashed one exactly once, every session in it
// is ended, and the sign-out of everybody is journaled. And the second,
// read-only door onto the same file refuses a write.

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/store"
)

func sessionColumns(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info('sessions') ORDER BY cid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	return cols
}

func resets(t *testing.T, s *store.Store) []store.Record {
	t.Helper()
	tail, err := s.JournalTail(100)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Record
	for _, r := range tail {
		if r.Event == "sessions_reset" {
			out = append(out, r)
		}
	}
	return out
}

func TestTheClearTextSessionsTableIsEndedOnceAndJournaled(t *testing.T) {
	dir := t.TempDir()

	// An installation from before invariant 61: the cookie itself is stored.
	old, err := sql.Open("sqlite", filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE sessions(token TEXT PRIMARY KEY, username TEXT, created REAL, expires REAL)`,
		`INSERT INTO sessions VALUES ('cookie-one', 'a.person', 1, 9e9)`,
		`INSERT INTO sessions VALUES ('cookie-two', 'b.person', 1, 9e9)`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sessionColumns(t, s.DB()), ","); got != "token_hash,username,created,expires" {
		t.Errorf("sessions columns after Open: %s, want the hashed table", got)
	}
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d sessions survived the migration: a clear-text cookie cannot be carried over", n)
	}
	rs := resets(t, s)
	if len(rs) != 1 {
		t.Fatalf("%d sessions_reset entries, want 1", len(rs))
	}
	if ended, _ := rs[0].Data["ended"].(float64); ended != 2 {
		t.Errorf("journal says %v sessions ended, want 2", rs[0].Data["ended"])
	}
	raw, err := os.ReadFile(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "cookie-one") {
		t.Error("a dropped clear-text cookie is still readable in app.db")
	}
	s.Close()

	// A restart signs nobody out a second time.
	s2, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if rs := resets(t, s2); len(rs) != 1 {
		t.Errorf("after a restart: %d sessions_reset entries, want still 1", len(rs))
	}
}

func TestOpenFailsWhenTheDatabasePathIsNotAFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "app.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	if s, err := store.Open(dir); err == nil {
		s.Close()
		t.Fatal("opened a store whose app.db is a directory")
	}
}

func TestOpenReadOnlyReadsAndRefusesAWrite(t *testing.T) {
	dir := t.TempDir()
	if _, err := store.OpenReadOnly(filepath.Join(dir, "nowhere")); err == nil {
		t.Error("a read-only open of a store that does not exist succeeded")
	}

	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(`INSERT INTO users(username, role) VALUES ('a.person', 'viewer')`); err != nil {
		t.Fatal(err)
	}

	ro, err := store.OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	var who string
	if err := ro.QueryRow(`SELECT username FROM users`).Scan(&who); err != nil || who != "a.person" {
		t.Fatalf("read through the read-only door: %q, %v", who, err)
	}
	if _, err := ro.Exec(`DELETE FROM users`); err == nil {
		t.Fatal("a write through the read-only door succeeded")
	}
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil || n != 1 {
		t.Errorf("users after the refused write: %d, %v", n, err)
	}
}

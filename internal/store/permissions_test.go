package store_test

// Invariant 63: the files this console owns are private to the account that
// runs it. The data directory is created 0700, the journal 0600, and
// app.db with its -wal and -shm 0600. They were 0755, 0644 and whatever the
// process umask gave, which on an ordinary host meant any local user could
// read the journal (usernames, every decision) and the database.
//
// The passport files and the -stack-events file are NOT in this list: other
// services read them, by design, and tightening them would break the
// integration. TestSharedFilesStayReadable... in internal/stack holds that.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/store"
)

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return fi.Mode().Perm()
}

// touchEveryFile writes through the database and the journal so that every file
// the store ever creates exists: app.db, and in WAL mode its -wal and -shm,
// and the journal.
func touchEveryFile(t *testing.T, st *store.Store) {
	t.Helper()
	if _, err := st.DB().Exec(
		`INSERT OR IGNORE INTO users(username, pw_hash, role, created) VALUES (?,?,?,?)`,
		"probe", "x", "viewer", 1.0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Journal("probe", 0, map[string]any{"n": 1}); err != nil {
		t.Fatal(err)
	}
}

func TestTheDataDirectoryIsPrivateWhenTheStoreCreatesIt(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "var", "costcrew")
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	for _, d := range []string{dir, filepath.Dir(dir)} {
		if got := mode(t, d); got != 0o700 {
			t.Errorf("%s created with mode %04o, want 0700", d, got)
		}
	}
}

// The default -data is ".", the operator's working directory. A directory that
// already exists is theirs, not this program's, and is left exactly as found.
func TestAnExistingDataDirectoryKeepsItsMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got := mode(t, dir); got != 0o755 {
		t.Errorf("an existing directory was changed to %04o; it was 0755 and is not this program's to change", got)
	}
}

// Measured on the files actually created, the -wal and -shm included, and
// measured twice: the second Open starts with the -wal and -shm gone (the
// first Close removed them) and creates them again.
func TestEveryFileTheStoreCreatesIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	for run := 1; run <= 2; run++ {
		st, err := store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		touchEveryFile(t, st)
		for _, name := range []string{"app.db", "app.db-wal", "app.db-shm", "events.ndjson"} {
			if got := mode(t, filepath.Join(dir, name)); got != 0o600 {
				t.Errorf("run %d: %s has mode %04o, want 0600", run, name, got)
			}
		}
		st.Close()
	}
}

// An installation from before this change has world-readable files. Opening it
// closes them; it does not wait for them to be recreated.
func TestFilesFromBeforeTheChangeAreTightenedOnOpen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	touchEveryFile(t, st)
	st.Close()
	for _, name := range []string{"app.db", "events.ndjson"} {
		if err := os.Chmod(filepath.Join(dir, name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	touchEveryFile(t, st)
	for _, name := range []string{"app.db", "app.db-wal", "app.db-shm", "events.ndjson"} {
		if got := mode(t, filepath.Join(dir, name)); got != 0o600 {
			t.Errorf("%s still has mode %04o after Open, want 0600", name, got)
		}
	}
}

// The journal does not exist until the first append, so Open cannot be what
// makes it private: the append that creates it has to.
func TestTheJournalIsCreatedPrivateByTheFirstAppend(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := os.Stat(st.JournalPath()); err == nil {
		t.Fatal("the journal exists before any append; the test would measure Open, not Journal")
	}
	if _, err := st.Journal("probe", 0, nil); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, st.JournalPath()); got != 0o600 {
		t.Errorf("the journal was created with mode %04o, want 0600", got)
	}
}

// .session-secret signs every session and every CSRF token. It is written 0600
// already; the remaining way for it to leak is `git add`.
func TestTheSessionSecretAndTheDatabaseFilesCannotBeCommitted(t *testing.T) {
	raw, err := os.ReadFile("../../.gitignore")
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(raw), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	for _, want := range []string{".session-secret", "*.db", "*.db-wal", "*.db-shm", "events.ndjson"} {
		if !have[want] {
			t.Errorf(".gitignore has no %q line", want)
		}
	}
}

package auth_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// openAuthIn is what the console does at start: open the store, then load or
// create the session key beside it.
func openAuthIn(t *testing.T, dir string) (*auth.Auth, error) {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return auth.New(st, dir)
}

// The key signs every session and seeds every CSRF token. A key that another
// account on the machine can read is a key nobody should sign with, so the
// console refuses to start on one instead of quietly using it.
func TestTheSessionSecretIsCreatedOwnerOnlyWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	if _, err := openAuthIn(t, dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, ".session-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("created with mode %o, want 600", got)
	}
}

func TestASessionSecretReadableByItsGroupOrOthersStopsTheStart(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o644, 0o604, 0o660, 0o666} {
		dir := t.TempDir()
		path := filepath.Join(dir, ".session-secret")
		before := bytes.Repeat([]byte{7}, 32)
		if err := os.WriteFile(path, before, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		_, err := openAuthIn(t, dir)
		if err == nil {
			t.Errorf("mode %o: the console started on a key others can read, want a refusal", mode)
			continue
		}
		if !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("mode %o: refusal %q does not name chmod 600", mode, err)
		}
		if got, _ := os.ReadFile(path); !bytes.Equal(got, before) {
			t.Errorf("mode %o: the refused key was replaced", mode)
		}
	}
}

func TestAnOwnerOnlySessionSecretIsReadAsItWas(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".session-secret")
	before := bytes.Repeat([]byte{9}, 32)
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openAuthIn(t, dir); err != nil {
		t.Fatalf("mode 600 refused: %v", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, before) {
		t.Errorf("an existing owner-only key was rewritten at start")
	}
}

// A link is not a key file: a link to a file that happens to be 0600 would
// pass a mode check on the target while the path names something else.
func TestASessionSecretThatIsALinkStopsTheStart(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	before := bytes.Repeat([]byte{5}, 32)
	if err := os.WriteFile(target, before, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".session-secret")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	_, err := openAuthIn(t, dir)
	if err == nil {
		t.Fatalf("a symbolic link was read as the session key, want a refusal")
	}
	if !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("refusal %q does not name chmod 600", err)
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, before) {
		t.Errorf("the file the link names was changed")
	}
}

func TestASessionSecretThatIsNotAFileStopsTheStart(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".session-secret"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := openAuthIn(t, dir)
	if err == nil {
		t.Fatalf("a directory was read as the session key, want a refusal")
	}
	if !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("refusal %q does not name chmod 600", err)
	}
}

// Before this change a read that failed for any reason, a permission denied
// included, was read as "no key yet" and the file was overwritten with a new
// one, which signs out everybody. Only a missing file may be created.
func TestAnUnreadableSessionSecretIsNotReplaced(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads a file whatever its mode")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, ".session-secret")
	before := bytes.Repeat([]byte{3}, 32)
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	// Write-only: the read fails for this account, and the write the old code
	// tried next would succeed and replace the key. Mode 000 would not show it,
	// since that write fails too.
	if err := os.Chmod(path, 0o200); err != nil {
		t.Fatal(err)
	}
	_, err := openAuthIn(t, dir)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err == nil {
		t.Fatalf("an unreadable key was accepted, want a refusal")
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, before) {
		t.Errorf("an unreadable key was replaced with a new one")
	}
}

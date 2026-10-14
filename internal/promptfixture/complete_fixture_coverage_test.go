package promptfixture

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/store"
)

// The complete fixture is the slowest thing this package builds, and several
// tests need the same one. It is built once per test binary and a test that
// changes it works on its own copy of the database file. Building it per test
// took the package from seconds to minutes under -race.
var completeFixture struct {
	once sync.Once
	dir  string // a closed store holding the complete fixture, made by buildCompleteFixture
	err  error
}

// TestMain removes the shared fixture after the run. Nothing else in the
// package needs a hook, so this is the only one.
func TestMain(m *testing.M) {
	code := m.Run()
	if completeFixture.dir != "" {
		os.RemoveAll(completeFixture.dir)
	}
	os.Exit(code)
}

func buildCompleteFixture() {
	dir, err := os.MkdirTemp("", "promptfixture-complete-")
	if err != nil {
		completeFixture.err = err
		return
	}
	st, err := Build(dir)
	if err != nil {
		os.RemoveAll(dir)
		completeFixture.err = err
		return
	}
	if err := st.Close(); err != nil {
		os.RemoveAll(dir)
		completeFixture.err = err
		return
	}
	completeFixture.dir = dir
}

// fixtureCopy opens a store that holds a copy of the complete fixture. The
// caller owns it and closes it; changes made to it reach no other test.
func fixtureCopy(t *testing.T) *store.Store {
	t.Helper()
	completeFixture.once.Do(buildCompleteFixture)
	if completeFixture.err != nil {
		t.Fatalf("building the complete fixture: %v", completeFixture.err)
	}
	dst := t.TempDir()
	if err := copyFile(filepath.Join(completeFixture.dir, "app.db"), filepath.Join(dst, "app.db")); err != nil {
		t.Fatalf("copying the complete fixture: %v", err)
	}
	st, err := store.Open(dst)
	if err != nil {
		t.Fatalf("opening the copy: %v", err)
	}
	return st
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

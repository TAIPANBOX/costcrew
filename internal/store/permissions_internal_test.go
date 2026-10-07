package store

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// Open does not die of a chmod the filesystem refuses (a mount that does not
// allow it, a file somebody else owns): refusing to start would turn a
// hardening step into an outage. It does not stay quiet either; the refusal is
// a warning the caller prints. A file that is merely not there yet is neither.
func TestAChmodTheFilesystemRefusesIsAWarningNotSilenceNotAnOutage(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if w := st.Warnings(); len(w) != 0 {
		t.Fatalf("a fresh store already carries warnings: %v", w)
	}

	refuse := func(p string, m os.FileMode) error {
		if strings.HasSuffix(p, "app.db") {
			return &fs.PathError{Op: "chmod", Path: p, Err: errors.New("operation not permitted")}
		}
		if strings.HasSuffix(p, "events.ndjson") {
			return &fs.PathError{Op: "chmod", Path: p, Err: fs.ErrNotExist}
		}
		return nil
	}
	st.tighten(refuse)

	w := st.Warnings()
	if len(w) != 1 || !strings.Contains(w[0], "app.db") || !strings.Contains(w[0], "operation not permitted") {
		t.Errorf("warnings = %q, want exactly one, naming app.db and the refusal "+
			"(a missing journal is not a warning)", w)
	}
}

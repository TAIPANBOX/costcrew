package deliver

// The pseudonym key lives in the data directory. A data directory that
// cannot be made (a file is in its place) refuses to start a restricting
// mode, rather than run with no key.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestARestrictingModeRefusesADataDirectoryItCannotMake(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(blocker, []byte("a file, not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := NewPolicy(PromptMasked, filepath.Join(blocker, "inside"))
	if err == nil || p != nil {
		t.Fatalf("a data directory under a file: policy %v, error %v", p, err)
	}
	if !strings.Contains(err.Error(), "mkdir") || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("the refusal %q does not say what is in the way", err)
	}
	// A nil policy binds to nothing and does not panic.
	var none *Policy
	none.Bind(nil)
}

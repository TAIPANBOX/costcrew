package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDemoModeRefusesALiveRun: in demo mode nobody spends the owner's model
// budget, so costcrew-run -live (and -due -live) is refused before the store
// is even opened.
func TestDemoModeRefusesALiveRun(t *testing.T) {
	t.Setenv("COSTCREW_DEMO", "1")
	dir := filepath.Join(t.TempDir(), "data")
	err := run(dir, "1.00", 0, 0, true, false, false, 0, "", "", "", "", "", localOptions{}, "")
	if err == nil || !strings.Contains(err.Error(), "demo mode") {
		t.Fatalf("a live run in demo mode was not refused by name: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "app.db")); serr == nil {
		t.Error("the store was opened before the refusal")
	}
}

package web_test

import (
	"os/exec"
	"strings"
	"testing"
)

// The features/ bindings hold, checked from the test suite as well as from
// scripts/features-are-bound.sh.
//
// The script is the gate a person runs. This is the same check reachable from
// `go test ./...`, so a binding that breaks is caught by the ordinary suite
// rather than only by somebody remembering to run a shell script, and so that
// gates-have-teeth.sh has a Go test to plant faults against.
func TestFeatureBindingsHold(t *testing.T) {
	out, err := exec.Command("../../scripts/features-are-bound.sh").CombinedOutput()
	text := string(out)
	if err != nil {
		t.Errorf("features/ bindings are broken:\n%s", strings.TrimSpace(text))
		return
	}
	if !strings.Contains(text, "0 broken") {
		t.Errorf("the gate reported success without saying nothing is broken:\n%s", text)
	}
	t.Log("  " + strings.TrimSpace(text))
}

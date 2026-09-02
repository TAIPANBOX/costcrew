package crew_test

// The roles.yaml bindings hold, checked from the test suite as well as from
// scripts/roles-are-bound.sh, the same way internal/web/features_test.go's
// TestFeatureBindingsHold wraps scripts/features-are-bound.sh: the script is
// the gate a person runs, and this is the same check reachable from
// `go test ./...`, so scripts/gates-have-teeth.sh has a Go test to plant
// faults against.

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestRolesAreBound(t *testing.T) {
	cmd := exec.Command("../../scripts/roles-are-bound.sh")
	// ROLES_YAML lets scripts/gates-have-teeth.sh plant "the file is gone"
	// without touching the real embed (every package in the module builds
	// against internal/crew/roles.yaml) or deleting a tracked file this test
	// would then have to restore itself. Nothing else should ever set it.
	if v := os.Getenv("ROLES_YAML"); v != "" {
		cmd.Env = append(os.Environ(), "ROLES_YAML="+v)
	}
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		t.Errorf("roles.yaml bindings are broken:\n%s", strings.TrimSpace(text))
		return
	}
	if !strings.Contains(text, "0 broken") {
		t.Errorf("the gate reported success without saying nothing is broken:\n%s", text)
	}
	t.Log("  " + strings.TrimSpace(text))
}

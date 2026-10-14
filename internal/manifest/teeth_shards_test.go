package manifest_test

import (
	"os/exec"
	"strings"
	"testing"
)

// The teeth shards partition the teeth cases, checked from the test suite as
// well as from scripts/teeth-shards-cover-every-case.sh.
//
// The script is the gate a person runs. This is the same check reachable from
// `go test ./...`, so a split that drops or repeats a case is caught by the
// ordinary suite, and so that gates-have-teeth.sh has a Go test to plant a
// broken filter against.
func TestTeethShardsCoverEveryCase(t *testing.T) {
	out, err := exec.Command("../../scripts/teeth-shards-cover-every-case.sh").CombinedOutput()
	text := string(out)
	if err != nil {
		t.Errorf("the teeth shards do not cover every case:\n%s", strings.TrimSpace(text))
		return
	}
	if !strings.Contains(text, "every case in exactly one shard") {
		t.Errorf("the gate reported success without saying the shards partition the cases:\n%s", text)
	}
	t.Log("  " + strings.TrimSpace(text))
}

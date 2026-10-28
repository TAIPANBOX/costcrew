package manifest_test

import (
	"os/exec"
	"strings"
	"testing"
)

// The two checks branch protection requires, `gate` and `teeth`, are jobs that
// wait for the jobs doing the work. Split that way, a job nobody waits for is
// required by nothing, and an aggregator that is skipped instead of red when a
// job fails lets the merge through. scripts/ci-required-checks-wait-for-every-job.sh
// reads the workflow for both; this puts it in the ordinary suite, and gives
// gates-have-teeth.sh a test to plant a broken workflow against.
func TestTheRequiredChecksWaitForEveryJob(t *testing.T) {
	out, err := exec.Command("../../scripts/ci-required-checks-wait-for-every-job.sh").CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		t.Fatalf("a required check does not wait for every job:\n%s", text)
	}
	if !strings.Contains(text, "both are red unless every one passed") {
		t.Errorf("the gate reported success without saying what the two checks wait for:\n%s", text)
	}
	t.Log("  " + text)
}

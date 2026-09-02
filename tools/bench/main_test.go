package main

import (
	"bytes"
	"strings"
	"testing"
)

// This test package DOES pass -live in two places below
// (TestLiveWithEitherMockIsHostile), and that is the only shape it is ever
// allowed to take: -live paired with a mock engine, refused by main.go's
// own flag validation before selectKnownCases or any caller runs at all.
// No test anywhere in this package pairs -live with a real engine name,
// which is the one combination that would reach liveCall. See live_test.go
// for how that function's own request and response handling is proven
// without ever setting anthropicAPIBase to the real vendor.
func runArgs(t *testing.T, args ...string) (code int, out, errOut string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	c, err := run(args, &stdout, &stderr)
	if err != nil {
		stderr.WriteString(err.Error())
	}
	return c, stdout.String(), stderr.String()
}

func TestNIsHostile(t *testing.T) {
	for _, n := range []string{"0", "-1"} {
		code, _, errOut := runArgs(t, "-dir", t.TempDir(), "-n", n)
		if code == 0 {
			t.Errorf("-n %s was accepted", n)
		}
		if !strings.Contains(errOut, "-n") {
			t.Errorf("-n %s: refusal does not mention -n: %q", n, errOut)
		}
	}
}

func TestUnknownEngineIsHostile(t *testing.T) {
	code, _, errOut := runArgs(t, "-dir", t.TempDir(), "-engine", "not-a-real-engine")
	if code == 0 {
		t.Error("an unknown -engine was accepted")
	}
	if !strings.Contains(errOut, "not-a-real-engine") {
		t.Errorf("refusal does not name the bad engine: %q", errOut)
	}
}

func TestUnknownSkillIsHostile(t *testing.T) {
	code, _, errOut := runArgs(t, "-dir", t.TempDir(), "-skill", "optimize")
	if code == 0 {
		t.Error("an unknown -skill was accepted")
	}
	if !strings.Contains(errOut, "-skill") {
		t.Errorf("refusal does not mention -skill: %q", errOut)
	}
}

// B7-SPEC.md section 4: "Neither mock is selectable with -live."
func TestLiveWithEitherMockIsHostile(t *testing.T) {
	for _, engine := range []string{"mock", "mock-oracle"} {
		code, _, errOut := runArgs(t, "-dir", t.TempDir(), "-live", "-engine", engine)
		if code == 0 {
			t.Errorf("-live with -engine %s was accepted", engine)
		}
		if !strings.Contains(errOut, "-live") {
			t.Errorf("-live with -engine %s: refusal does not mention -live: %q", engine, errOut)
		}
	}
}

// -seed garbage: the flag package's own ContinueOnError refusal, not a
// custom check, and it must not exit 0 or panic.
func TestSeedGarbageIsHostile(t *testing.T) {
	code, _, _ := runArgs(t, "-dir", t.TempDir(), "-seed", "garbage")
	if code == 0 {
		t.Error("-seed garbage was accepted")
	}
}

// B7-SPEC.md section 2: without -live, any engine but mock/mock-oracle is
// priced and refused, exit 2. This IS run: pricing never calls anything,
// which is exactly why it is the one real-engine path this agent may
// exercise at all.
func TestRealEngineWithoutLivePricesAndExits2(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := runArgs(t, "-dir", dir, "-skill", "investigate", "-engine", "anthropic")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(out, "-live") {
		t.Errorf("the refusal does not mention -live:\n%s", out)
	}
	if !strings.Contains(out, "USD") {
		t.Errorf("the refusal does not print a price:\n%s", out)
	}
}

// -n asked for more than the two-case fixture holds: the price is for
// what was ACTUALLY priced, and the refusal says so, the same clamp-and-say
// rule -n's own boundary applies to a real scoring run.
func TestRealEngineWithoutLivePricesSayWhenNClamped(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := runArgs(t, "-dir", dir, "-skill", "investigate", "-engine", "anthropic", "-n", "20")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(out, "requested 20") {
		t.Errorf("asking for 20 cases against a two-case fixture did not say it was clamped:\n%s", out)
	}
	if !strings.Contains(out, "Worst case for 2 case(s)") {
		t.Errorf("the priced count does not match the clamped case count:\n%s", out)
	}
}

// A normal mock run against a freshly-seeded -dir: this is the one thing
// this agent actually runs, in every test and in the report's own "Ran it".
func TestANormalMockRunSucceeds(t *testing.T) {
	dir := t.TempDir()
	code, out, errOut := runArgs(t, "-dir", dir, "-skill", "triage", "-engine", "mock", "-seed", "7")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "BENCH  fixture, seed 7") {
		t.Errorf("output does not carry the fixed header:\n%s", out)
	}
}

func TestAMockOracleRunSucceeds(t *testing.T) {
	dir := t.TempDir()
	code, out, errOut := runArgs(t, "-dir", dir, "-skill", "investigate", "-engine", "mock-oracle")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "accuracy (cause) 100%") {
		t.Errorf("mock-oracle did not report 100%% accuracy:\n%s", out)
	}
}

// A store is seeded exactly once: running the bench twice against the same
// -dir must not change the estate, the roster, or which cases exist (no
// re-detection surprises, no duplicate roster rows).
func TestRunningTwiceAgainstTheSameDirIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	_, out1, _ := runArgs(t, "-dir", dir, "-skill", "investigate", "-engine", "mock", "-seed", "9")
	_, out2, _ := runArgs(t, "-dir", dir, "-skill", "investigate", "-engine", "mock", "-seed", "9")
	if out1 != out2 {
		t.Errorf("two runs against the same -dir with the same seed produced different output:\n"+
			"--- first ---\n%s\n--- second ---\n%s", out1, out2)
	}
}

// The default -dir is a fresh install: main() itself (not run()) wires
// os.Args, os.Stdout and os.Stderr, and this is what proves it is wired at
// all without actually invoking main (which would call os.Exit and end the
// test binary).
func TestRunAcceptsAnEmptyArgumentListWithoutPanicking(t *testing.T) {
	dir := t.TempDir()
	if code, _, _ := runArgs(t, "-dir", dir); code != 0 {
		t.Errorf("the default flags (triage, mock, seed 1, n 20) did not succeed: exit %d", code)
	}
}

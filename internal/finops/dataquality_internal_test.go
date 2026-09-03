package finops

// Hostile input, C9-SPEC.md section 4: "a threshold row missing from the
// roles data (refuse to measure, say so)". roles.yaml's own T.stale and
// T.untagged are fixed at compile time (go:embed), so the only honest way
// to exercise "missing" is to ask the SAME lookup DataQuality itself uses
// for a name roles.yaml genuinely does not carry -- crew.ThresholdFor
// really does return ok=false for any such name, so this is a real exercise
// of the refusal path, not a simulation of one.

import (
	"errors"
	"testing"
)

func TestWholeNumberThresholdRefusesAMissingThreshold(t *testing.T) {
	_, err := wholeNumberThreshold("T.does-not-exist", false)
	if err == nil {
		t.Fatal("wholeNumberThreshold accepted a threshold name roles.yaml does not define")
	}
	if !errors.Is(err, ErrThresholdMissing) {
		t.Errorf("error %v does not wrap ErrThresholdMissing", err)
	}
}

// daysBetween is defensive against a date it cannot parse: zero, never a
// panic and never a negative count that would read as "in the future".
func TestDaysBetweenIsZeroOnAnUnparseableDate(t *testing.T) {
	if got := daysBetween("not-a-date", "2026-09-10"); got != 0 {
		t.Errorf("daysBetween with an unparseable from = %d, want 0", got)
	}
	if got := daysBetween("2026-09-01", "also-not-a-date"); got != 0 {
		t.Errorf("daysBetween with an unparseable to = %d, want 0", got)
	}
}

// The real thresholds this measurement depends on must both parse today,
// against the roles data actually embedded in this build.
func TestWholeNumberThresholdParsesTStaleAndTUntagged(t *testing.T) {
	days, err := wholeNumberThreshold("T.stale", false)
	if err != nil {
		t.Fatalf("T.stale: %v", err)
	}
	if days <= 0 {
		t.Errorf("T.stale parsed to %d, want a positive number of days", days)
	}
	pct, err := wholeNumberThreshold("T.untagged", true)
	if err != nil {
		t.Fatalf("T.untagged: %v", err)
	}
	if pct <= 0 || pct > 100 {
		t.Errorf("T.untagged parsed to %d, want a percentage between 1 and 100", pct)
	}
}

package finops

// worseMiss is unexported: LargestMiss's own test (forecast_test.go, package
// finops_test) can only observe it through Forecasts' own SQL order (period
// DESC, source ASC), which happens to already agree with worseMiss's later-
// period and lower-source tie-breaks -- so a test built on top of Forecasts
// would still pass with the tie-break logic deleted outright, keeping
// whichever row the query returned first. Tested directly here instead, on
// values this file constructs by hand, so each level of the comparator is
// what decides the answer, not a query's own incidental order.

import "testing"

func TestWorseMissOrdersByErrorPctFirst(t *testing.T) {
	a := Forecast{Period: "2026-01", Source: "aws", ErrorPct: 20}
	b := Forecast{Period: "2026-01", Source: "gcp", ErrorPct: 5}
	if !worseMiss(a, b) {
		t.Error("20% error should be a worse miss than 5%, regardless of anything else")
	}
	if worseMiss(b, a) {
		t.Error("5% error should not be a worse miss than 20%")
	}
}

// Tied on ErrorPct: the larger absolute cents gap wins, even when its own
// ErrorPct denominator (Forecast) differs from the other's.
func TestWorseMissBreaksATiedErrorOnTheAbsoluteGap(t *testing.T) {
	a := Forecast{Period: "2026-01", Source: "aws", ErrorPct: 10, Forecast: 10000, Actual: 11000} // gap 1000
	b := Forecast{Period: "2026-01", Source: "gcp", ErrorPct: 10, Forecast: 20000, Actual: 22000} // gap 2000
	if !worseMiss(b, a) {
		t.Error("a 2000-cent gap should be a worse miss than a 1000-cent gap at the same error rate")
	}
	if worseMiss(a, b) {
		t.Error("a 1000-cent gap should not be a worse miss than a 2000-cent gap at the same error rate")
	}
}

// Tied on both ErrorPct and the absolute gap: the LATER period wins -- an
// older miss that was never corrected is less urgent than a fresh one with
// an identical shape.
func TestWorseMissBreaksATiedGapOnThePeriod(t *testing.T) {
	older := Forecast{Period: "2025-12", Source: "aws", ErrorPct: 10, Forecast: 10000, Actual: 11000}
	newer := Forecast{Period: "2026-01", Source: "aws", ErrorPct: 10, Forecast: 10000, Actual: 11000}
	if !worseMiss(newer, older) {
		t.Error("2026-01 should be a worse miss than 2025-12 when error and gap are tied")
	}
	if worseMiss(older, newer) {
		t.Error("2025-12 should not be a worse miss than 2026-01 when error and gap are tied")
	}
}

// Tied on ErrorPct, the gap and the period: the source breaks the tie, so
// the same data always names the same "largest miss" regardless of
// whatever order it arrived in.
func TestWorseMissBreaksAFullTieOnTheSource(t *testing.T) {
	a := Forecast{Period: "2026-01", Source: "aws", ErrorPct: 10, Forecast: 10000, Actual: 11000}
	z := Forecast{Period: "2026-01", Source: "zzz-desk", ErrorPct: 10, Forecast: 10000, Actual: 11000}
	if !worseMiss(a, z) {
		t.Error("aws should be a worse miss than zzz-desk when everything else is tied")
	}
	if worseMiss(z, a) {
		t.Error("zzz-desk should not be a worse miss than aws when everything else is tied")
	}
}

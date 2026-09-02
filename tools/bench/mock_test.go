package main

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

func gkeAnomaly() anomaly.Anomaly {
	return anomaly.Anomaly{
		ID: "A-e02", Source: "gcp", Team: "research", Service: "GKE", Day: "2026-06-22",
		Direction: "up", Amount: money.Cents(96_500), Baseline: money.Cents(20_000),
		Excess: money.Cents(76_500), Z: 4.1,
		Driver: "Quarterly model refresh, planned",
	}
}

// B7-SPEC.md section 4: mock scores 100% on service and day, 0% on cause
// -- and, this file's own design, 0% on kind too, since a mock that never
// mentions the kind at all is a cleaner, equally deterministic baseline
// than one that has to know the true kind just to contradict it.
func TestMockScoresRightServiceAndDayWrongCauseAndKind(t *testing.T) {
	an := gkeAnomaly()
	body := mockDeliverable(an, "one-time", false)
	got := scoreDeliverable(an, "one-time", body, 0)

	if !got.ServiceNamed {
		t.Error("mock did not name the service")
	}
	if !got.DayNamed {
		t.Error("mock did not name the day")
	}
	if got.CauseMatched {
		t.Errorf("mock scored a cause match; it must always be the fixed wrong string: %q", got.NamedCause)
	}
	if got.KindRight {
		t.Error("mock scored the kind right; it is supposed to name the OPPOSITE kind")
	}
	if got.NamedCause != mockWrongCause {
		t.Errorf("mock named %q, want the fixed wrong string %q", got.NamedCause, mockWrongCause)
	}
}

// The fixed string really is fixed: two different anomalies get the exact
// same wrong cause, not a per-case one.
func TestMockWrongCauseIsFixedAcrossCases(t *testing.T) {
	a1 := gkeAnomaly()
	a2 := gkeAnomaly()
	a2.ID, a2.Service, a2.Driver = "A-e04", "Batch cluster", "Batch cluster decommission, tranche 1"

	b1 := mockDeliverable(a1, "one-time", false)
	b2 := mockDeliverable(a2, "one-time", false)
	if namedCause(b1) != namedCause(b2) {
		t.Errorf("mock's wrong cause differs per case: %q vs %q", namedCause(b1), namedCause(b2))
	}
}

// B7-SPEC.md section 4: mock-oracle is handed the truth and names it,
// scoring 100% on cause -- "it exists to prove the scorer can say yes as
// well as no."
func TestMockOracleScores100PercentAcrossTheBoard(t *testing.T) {
	an := gkeAnomaly()
	body := mockDeliverable(an, "one-time", true)
	got := scoreDeliverable(an, "one-time", body, 0)

	if !got.ServiceNamed || !got.DayNamed || !got.KindRight || !got.CauseMatched {
		t.Errorf("mock-oracle did not score 100%%: %+v", got)
	}
	if got.NamedCause != an.Driver {
		t.Errorf("mock-oracle named %q, want the true label %q", got.NamedCause, an.Driver)
	}
}

// Both mocks cost nothing: neither calls anything, so neither can have
// spent anything.
func TestNeitherMockCostsAnything(t *testing.T) {
	an := gkeAnomaly()
	for _, oracle := range []bool{false, true} {
		body := mockDeliverable(an, "one-time", oracle)
		got := scoreDeliverable(an, "one-time", body, 0)
		if got.CostMicros != 0 {
			t.Errorf("oracle=%v: cost is %d micros, want 0", oracle, got.CostMicros)
		}
	}
}

func TestIsMockEngineRecognisesBothAndNothingElse(t *testing.T) {
	for _, name := range []string{"mock", "mock-oracle"} {
		if !isMockEngine(name) {
			t.Errorf("isMockEngine(%q) = false", name)
		}
	}
	for _, name := range []string{"anthropic", "openrouter", "bedrock", "", "Mock"} {
		if isMockEngine(name) {
			t.Errorf("isMockEngine(%q) = true", name)
		}
	}
}

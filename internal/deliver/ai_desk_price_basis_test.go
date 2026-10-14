package deliver

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// TestAISpendSectionKeepsAFallbackEstimateOutOfTheTotalAndNamesPII (invariant 103): the
// ai-spend analyst's total leaves out what the gateway priced at its fallback
// rate, and the packet says how much that was, desk-wide even under
// aggregates, and per agent and model otherwise.
func TestAISpendSectionKeepsAFallbackEstimateOutOfTheTotalAndNamesPII(t *testing.T) {
	db := importInto(t, "tokenfuse-focus-price-basis-2026-10-14.csv")
	a := crew.Analyst{Name: "ai-spend", State: "active", Skills: []string{"ai-spend-analysis", "token-economics"}}
	p := Packet(db, crew.Task{ID: 1, Desk: "ai"}, a, false)
	for _, want := range []string{
		"total: 0.06, 7 calls",
		"priced at the gateway's fallback rate (its price book had no price for the model): 2 calls, 0.0024, an estimate and not a bill, left out of the total above",
		"[+0.0024 fallback-priced over 2 calls, an estimate left out of the cost]",
		"[+0.0024 fallback-priced, an estimate left out of the cost]",
		"personal data the gateway's scanner found and let through: 2 calls; 3 pii_email, 1 pii_card (kinds and counts only, never the values)",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the ai-spend packet does not carry %q:\n%s", want, p)
		}
	}

	pol, err := NewPolicy(PromptAggregates, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pol.Bind(db)
	t.Cleanup(SetActivePolicy(pol))
	if agg := Packet(db, crew.Task{ID: 1, Desk: "ai"}, a, false); !strings.Contains(agg, "2 calls, 0.0024, an estimate and not a bill") ||
		!strings.Contains(agg, "found and let through: 2 calls") {
		t.Errorf("the desk-wide fallback line is withheld under aggregates:\n%s", agg)
	}

	quiet := importInto(t, "tokenfuse-focus-1.8.0-2026-10-14.csv")
	if q := Packet(quiet, crew.Task{ID: 1, Desk: "ai"}, a, false); strings.Contains(q, "fallback") || strings.Contains(q, "let through") {
		t.Errorf("a packet from an export without a price basis speaks of a fallback:\n%s", q)
	}
}

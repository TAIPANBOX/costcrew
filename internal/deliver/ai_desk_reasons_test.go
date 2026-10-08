package deliver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// TestAISpendSectionCarriesTheBlockReason (invariant 90): the ai-spend
// analyst is told why each call was blocked, and that a key: row is a
// credential, so it cannot read an identity refusal as an agent's overspend.
func TestAISpendSectionCarriesTheBlockReason(t *testing.T) {
	db := deliverTestDB(t)
	data, err := os.ReadFile(filepath.Join("..", "connectors", "testdata", "tokenfuse-focus-1.7.0-2026-10-07.csv"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "focus.csv"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := connectors.Save(db, "tokenfuse-focus", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := connectors.Import(db, "tokenfuse-focus", false, connectors.ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	a := crew.Analyst{Name: "ai-spend", State: "active", Skills: []string{"ai-spend-analysis", "token-economics"}}
	p := Packet(db, crew.Task{ID: 1, Desk: "ai"}, a, false)
	for _, want := range []string{
		"blocked by the gateway's reason: 1 budget_exceeded, 1 identity_mismatch",
		"filed under the credential (key:...), not the agent it claimed",
		"1 calls, 1 blocked (1 identity_mismatch) [a credential, not an agent]",
		"1 blocked (1 budget_exceeded)",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the ai-spend packet does not carry %q:\n%s", want, p)
		}
	}
}

package deliver

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// importInto reads one fixture from internal/connectors/testdata into db.
func importInto(t *testing.T, name string) *sql.DB {
	t.Helper()
	db := deliverTestDB(t)
	data, err := os.ReadFile(filepath.Join("..", "connectors", "testdata", name))
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
	return db
}

// TestAISpendSectionNamesTheCallsForwardedUnderIdentityWarn (invariant 100):
// the ai-spend analyst is told which spend was forwarded although the
// identity check failed, desk-wide even under aggregates, and per agent
// otherwise, so it cannot read that spend as the claimed agent's own.
func TestAISpendSectionNamesTheCallsForwardedUnderIdentityWarn(t *testing.T) {
	db := importInto(t, "tokenfuse-focus-1.8.0-2026-10-14.csv")
	a := crew.Analyst{Name: "ai-spend", State: "active", Skills: []string{"ai-spend-analysis", "token-economics"}}
	p := Packet(db, crew.Task{ID: 1, Desk: "ai"}, a, false)
	for _, want := range []string{
		"forwarded under identity warn mode: 3 calls, 0.16 (agent_id_not_allowed, unit_chosen_by_agent_id)",
		"counted under the agent id each claimed, which nobody proved",
		"[2 forwarded under identity warn, 0.15: the agent id is claimed, not proved]",
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
	agg := Packet(db, crew.Task{ID: 1, Desk: "ai"}, a, false)
	if !strings.Contains(agg, "forwarded under identity warn mode: 3 calls, 0.16") {
		t.Errorf("the desk-wide count is withheld under aggregates:\n%s", agg)
	}
	if strings.Contains(agg, "forwarded under identity warn, 0.15") {
		t.Errorf("a per-agent line leaks under aggregates:\n%s", agg)
	}

	// And a month in which nothing was forwarded says nothing of it.
	quiet := importInto(t, "tokenfuse-focus-1.7.0-2026-10-07.csv")
	if p := Packet(quiet, crew.Task{ID: 1, Desk: "ai"}, a, false); strings.Contains(p, "identity warn") {
		t.Errorf("a packet with nothing forwarded under warn mode speaks of it:\n%s", p)
	}
}

package anomaly_test

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

func hintStore(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.DB().Exec(anomaly.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO anomalies (id, source, service, day, direction, amount_cents,
		baseline_cents, excess_cents, z, rule_version, state, detected_at)
		VALUES ('A-h', 'aws', 'Amazon EC2', '2026-07-14', 'up', 900, 100, 800, 5, 'v1', 'open', '2026-07-15T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return st.DB()
}

// Without -typryx-url nothing is migrated, and every reader treats the store
// as one with no hint.
func TestAStoreWithoutTheHintColumnsHasNoHintAndRefusesAWrite(t *testing.T) {
	db := hintStore(t)
	if _, ok, err := anomaly.HintOf(db, "A-h"); ok || err != nil {
		t.Errorf("HintOf on a store with no hint columns = %v, %v", ok, err)
	}
	if err := anomaly.SaveHint(db, "A-h", anomaly.Hint{Class: "unknown", Backend: "off"}); !errors.Is(err, anomaly.ErrNoHintColumns) {
		t.Errorf("SaveHint without the columns = %v, want ErrNoHintColumns", err)
	}
	if ids, err := anomaly.NeedingHint(db, 10); err == nil || len(ids) != 0 {
		t.Errorf("NeedingHint without the columns = %v, %v", ids, err)
	}
}

func TestEnsureHintColumnsIsSafeToRunTwice(t *testing.T) {
	db := hintStore(t)
	for i := 0; i < 2; i++ {
		if err := anomaly.EnsureHintColumns(db); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	if err := anomaly.SaveHint(db, "A-h", anomaly.Hint{Class: "price_change", Probability: 0.5,
		Backend: anomaly.BackendOff, AnswerID: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := anomaly.EnsureHintColumns(db); err != nil {
		t.Fatal(err)
	}
	if h, ok, _ := anomaly.HintOf(db, "A-h"); !ok || h.Class != "price_change" {
		t.Errorf("a third migration lost the hint: %+v", h)
	}
}

// SaveHint touches the hint columns and nothing else on the row.
func TestSaveHintNeverMovesTheAnomaly(t *testing.T) {
	db := hintStore(t)
	if err := anomaly.EnsureHintColumns(db); err != nil {
		t.Fatal(err)
	}
	if err := anomaly.Assign(db, "A-h", "triage-aws", nil); err != nil {
		t.Fatal(err)
	}
	before, _ := anomaly.Get(db, "A-h")
	if err := anomaly.SaveHint(db, "A-h", anomaly.Hint{Class: "runaway_agent", Probability: 0.99,
		Backend: anomaly.BackendJev, AnswerID: "a"}); err != nil {
		t.Fatal(err)
	}
	after, _ := anomaly.Get(db, "A-h")
	if before != after {
		t.Errorf("a hint moved the anomaly:\nbefore %+v\nafter  %+v", before, after)
	}
	if err := anomaly.SaveHint(db, "A-nope", anomaly.Hint{Class: "unknown", Backend: "off"}); !errors.Is(err, anomaly.ErrNotFound) {
		t.Errorf("a hint for no anomaly = %v", err)
	}
}

func TestHintSourceNamesEachDataMode(t *testing.T) {
	for b, want := range map[string]string{
		anomaly.BackendJev:      "Jev, hosted by TypeSafe AI",
		anomaly.BackendOwnModel: "the operator's own model",
		anomaly.BackendOff:      "not a model's judgement",
		"":                      "no backend answering",
	} {
		if got := (anomaly.Hint{Backend: b}).Source(); len(got) < len(want) || !contains(got, want) {
			t.Errorf("Source(%q) = %q, want it to say %q", b, got, want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

package store_test

import (
	"os"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/store"
)

// A whole number in a journal entry must not break the chain.
//
// The verifier re-derives each hash from the line it reads back, and JSON has
// turned every number in it into a float64 by then. An int64 written as 34805
// returns as 34805.0, the two canonical forms differ, and the chain reports
// itself broken at an entry nobody touched.
//
// It hid for the whole life of this console because only sign-ins were
// journalled and those carry nothing but strings. It fired the day the console
// started recording what it had decided, which is the day the chain first
// mattered.
func TestAWholeNumberInAnEntryDoesNotBreakTheChain(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	for _, data := range []map[string]any{
		{"who": "owner"},
		{"excess_cents": int64(34805), "anomaly": "A-1234"},
		{"seats": 60, "team": "growth"},
		{"rate": 1.5, "share": 0.0},
		{"nested": map[string]any{"cents": int64(1200)}},
		{"list": []any{int64(1), int64(2)}},
	} {
		if _, err := st.Journal("test_event", 0, data); err != nil {
			t.Fatalf("journalling %v: %v", data, err)
		}
	}

	ok, n, breakAt, err := st.VerifyChain()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("the chain broke at %s after %d entries, none of which was edited", breakAt, n)
	}
	if n != 6 {
		t.Errorf("wrote 6 entries, the chain has %d", n)
	}
}

// And an edited entry still breaks it. A chain that accepts everything is not
// a chain, and the fix above must not have bought its pass that way.
func TestAnEditedEntryStillBreaksTheChain(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []map[string]any{{"seats": 60}, {"seats": 61}, {"seats": 62}} {
		if _, err := st.Journal("test_event", 0, d); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	path := dir + "/events.ndjson"
	raw, err := readFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := replaceFirst(raw, `"seats": 61`, `"seats": 99`)
	if edited == raw {
		t.Fatalf("the entry to edit was not found in the journal")
	}
	if err := writeFile(path, edited); err != nil {
		t.Fatal(err)
	}

	st2, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	ok, _, breakAt, err := st2.VerifyChain()
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("a journal entry was edited and the chain still verified")
	}
	if breakAt == "" {
		t.Error("the chain broke and did not say where")
	}
}

func readFile(p string) (string, error) {
	b, err := os.ReadFile(p)
	return string(b), err
}

func writeFile(p, s string) error { return os.WriteFile(p, []byte(s), 0o644) }

func replaceFirst(s, old, new string) string { return strings.Replace(s, old, new, 1) }

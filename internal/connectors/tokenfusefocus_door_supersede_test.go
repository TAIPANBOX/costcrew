package connectors

// Invariant 106: two doors are two traces. A call_key match between the main
// folder and the OpenAI-wire folder is two calls, not one re-export of one
// call, so supersede removes a row only when it was read through the same door
// as the file being imported.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// doorTwinCSV is an export holding one blocked call (a refused call costs
// nothing): the same run, instant, model, tokens, amount and block as the other
// door's copy. Only the
// description differs, so the two files are two files with two hashes.
func doorTwinCSV(t *testing.T, description string) string {
	t.Helper()
	src, err := os.ReadFile("testdata/tokenfuse-focus-openai-door-2026-10-14.csv")
	if err != nil {
		t.Fatal(err)
	}
	header := strings.SplitN(string(src), "\n", 2)[0]
	row := "0.000000,0.000000,USD,2026-10-14T10:05:00.000Z,2026-10-14T10:05:00.000Z," + description +
		",OpenAI,OpenAI,OpenAI,LLM inference,AI and Machine Learning," +
		"agent://home.example/w2x/cross,agent://home.example/w2x/cross," +
		"w2-b12-A,w2-b12-A,w2-b12-A,,agent://home.example/w2x/cross,openai/gpt-4.1-mini," +
		"200,60,true,settled,,w2x,0,cross-openai-key,breaker,"
	dir := t.TempDir()
	path := filepath.Join(dir, "export.csv")
	if err := os.WriteFile(path, []byte(header+"\n"+row+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestOneBlockedCallOnEachDoorIsTwoCalls: the same blocked call, exported by
// the main gateway and by the OpenAI-wire gateway, is one row on each door,
// whichever folder the import reads first. Before invariant 106 the second
// file read deleted the first door's row, because the call_key matched.
func TestOneBlockedCallOnEachDoorIsTwoCalls(t *testing.T) {
	for _, order := range []struct {
		name            string
		first, second   string
		firstDescribed  string
		secondDescribed string
	}{
		{"main read first", "main", "openai", "LLM call", "LLM call via the OpenAI wire"},
		{"OpenAI-wire read first", "openai", "main", "LLM call via the OpenAI wire", "LLM call"},
	} {
		t.Run(order.name, func(t *testing.T) {
			st := openFocusStore(t)
			db := st.DB()
			firstDir := doorTwinCSV(t, order.firstDescribed)
			secondDir := doorTwinCSV(t, order.secondDescribed)
			mainDir, openaiDir := firstDir, secondDir
			if order.first == "openai" {
				mainDir, openaiDir = secondDir, firstDir
			}
			if err := Save(db, "tokenfuse-focus", map[string]string{"path": mainDir, OpenAIPathInput: openaiDir}); err != nil {
				t.Fatal(err)
			}
			msg, err := Import(db, "tokenfuse-focus", false, ImportOptions{})
			if err != nil {
				t.Fatalf("Import: %v (%s)", err, msg)
			}
			if n := countWhere(t, db, `1=1`); n != 2 {
				t.Errorf("%d row(s) for one blocked call on each door, want 2: a call_key match across doors removed one", n)
			}
			if n := countWhere(t, db, `door=?`, DoorMain); n != 1 {
				t.Errorf("%d row(s) on the main gateway's door, want 1", n)
			}
			if n := countWhere(t, db, `door=?`, DoorOpenAI); n != 1 {
				t.Errorf("%d row(s) on the OpenAI-wire door, want 1", n)
			}
			if !strings.Contains(msg, "By door: 1 row through the main gateway, 1 through the OpenAI-wire door.") {
				t.Errorf("the import's sentence does not say one row per door:\n%s", msg)
			}
			if _, err := Import(db, "tokenfuse-focus", false, ImportOptions{}); err != nil {
				t.Fatal(err)
			}
			if n := countWhere(t, db, `1=1`); n != 2 {
				t.Errorf("a second import left %d row(s), want 2", n)
			}
		})
	}
}

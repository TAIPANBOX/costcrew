package connectors

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/estate"
)

// The gateway export reader on a launcher's machine, where a folder exists
// before any export has been written to it.

// TestAnEmptyOpenAIDoorFolderDoesNotBlockTheMainDoor: a launcher that wires
// -focus-dir-openai on a machine with no OpenAI-wire gateway gives the reader
// an existing, empty folder. An empty folder is zero files, so the main
// door's calls still land.
func TestAnEmptyOpenAIDoorFolderDoesNotBlockTheMainDoor(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	main := copyIntoDir(t, fixtureCSV)
	empty := t.TempDir()
	if err := Save(db, "tokenfuse-focus", map[string]string{"path": main, OpenAIPathInput: empty}); err != nil {
		t.Fatal(err)
	}
	msg, err := Import(db, "tokenfuse-focus", false, ImportOptions{})
	if err != nil {
		t.Fatalf("an empty OpenAI-wire folder refused the whole import: %v", err)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM ai_calls WHERE door=?`, DoorMain); n <= 0 {
		t.Errorf("the main door's calls did not land: %d rows (%s)", n, msg)
	}
}

// TestAnEmptyMainFolderReadsAsNoExportYet: the main folder exists and holds
// no export yet. That is zero files, said in the sentence, not an error, and
// not the generated-estate refusal either: nothing was going to be read.
func TestAnEmptyMainFolderReadsAsNoExportYet(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	if _, err := estate.Seed(db); err != nil {
		t.Fatal(err)
	}
	configureFocus(t, db, t.TempDir())
	msg, err := Import(db, "tokenfuse-focus", false, ImportOptions{})
	if err != nil {
		t.Fatalf("an empty folder is an error: %v", err)
	}
	if !strings.Contains(msg, "Read 0 files") || !strings.Contains(msg, "no export") {
		t.Errorf("the sentence does not say the folder holds no export yet: %s", msg)
	}
}

// TestAMissingDoorFolderIsStillAnError: a folder that is not there is a
// misconfiguration, named, and nothing is read.
func TestAMissingDoorFolderIsStillAnError(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	main := copyIntoDir(t, fixtureCSV)
	gone := filepath.Join(t.TempDir(), "not-there")
	if err := Save(db, "tokenfuse-focus", map[string]string{"path": main, OpenAIPathInput: gone}); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(db, "tokenfuse-focus", false, ImportOptions{}); err == nil ||
		!strings.Contains(err.Error(), "OpenAI-wire door") {
		t.Fatalf("a missing door folder was not refused by name: %v", err)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM ai_calls`); n > 0 {
		t.Errorf("%d rows landed although the configuration was refused", n)
	}
}

// blockedOnlyExport is the fixture's header and first row with the call
// blocked: a file whose every call the gateway refused, so none is spend.
func blockedOnlyExport(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(fixtureCSV)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitN(string(raw), "\n", 3)
	row := strings.Replace(lines[1], ",false,settled,", ",true,blocked,", 1)
	if row == lines[1] || !strings.HasPrefix(row, "0.003500,0.003500,") {
		t.Fatal("sanity: the fixture row is not the shape this helper rewrites")
	}
	// A blocked call bills nothing: the gateway refused it before it was sent.
	row = "0,0," + strings.TrimPrefix(row, "0.003500,0.003500,")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blocked.csv"), []byte(lines[0]+"\n"+row+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestReplaceGeneratedKeepsTheEstateWhenNoCallIsSpend: the launcher's yes to
// replacing the generated estate is spent only on real money. An export whose
// every call was blocked lands its calls and wipes nothing: the generated
// charges, tasks and anomalies stay, and nothing is journaled as replaced.
// Wiping here left charges empty, so the next start re-seeded the estate and
// the next import wiped it again.
func TestReplaceGeneratedKeepsTheEstateWhenNoCallIsSpend(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	if _, err := estate.Seed(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS tasks(id INTEGER); INSERT INTO tasks VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	before := cfCount(db, `SELECT COUNT(*) FROM charges WHERE provenance IS NULL`)
	configureFocus(t, db, blockedOnlyExport(t))
	rec := &recordingRecorder{}
	msg, err := Import(db, "tokenfuse-focus", false, ImportOptions{ReplaceGenerated: true, Rec: rec, Actor: "test"})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := cfCount(db, `SELECT COUNT(*) FROM charges WHERE provenance IS NULL`); got != before {
		t.Errorf("the generated charges went from %d to %d for an export with no spend in it", before, got)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM tasks`); n != 1 {
		t.Errorf("the seeded tasks were wiped for an export with no spend in it: %d left", n)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM ai_calls WHERE blocked=1`); n != 1 {
		t.Errorf("the blocked call did not land: %d", n)
	}
	for _, k := range rec.kinds {
		if k == "generated_estate_replaced" {
			t.Errorf("the replacement was journaled although nothing was replaced")
		}
	}
	if !strings.Contains(msg, "generated estate was kept") {
		t.Errorf("the sentence does not say the generated estate was kept: %s", msg)
	}
}

// TestReplaceGeneratedStillReplacesOnceSpendLands: the same yes with an
// export that carries real spend replaces the estate, as before.
func TestReplaceGeneratedStillReplacesOnceSpendLands(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	if _, err := estate.Seed(db); err != nil {
		t.Fatal(err)
	}
	configureFocus(t, db, copyIntoDir(t, fixtureCSV))
	if _, err := Import(db, "tokenfuse-focus", false, ImportOptions{ReplaceGenerated: true}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM charges WHERE provenance IS NULL`); n != 0 {
		t.Errorf("%d generated charges survived an import with real spend in it", n)
	}
}

type recordingRecorder struct{ kinds []string }

func (r *recordingRecorder) Emit(kind, actor, severity string, data map[string]any, onBehalfOf []string) error {
	r.kinds = append(r.kinds, kind)
	return nil
}

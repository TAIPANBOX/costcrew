package connectors

// LoadTokenFuseFocus is the tokenfuse-focus reader handed to a caller that is
// not the console (costcrew-cloudability). It must be that reader and not a
// second one: the same sentence, the same rows, and the figures in its result
// the ones the sentence states.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTokenFuseFocusIsTheConnectorsOwnReader(t *testing.T) {
	dir := t.TempDir()
	good := strings.Join(focusRowFields(), ",")
	writeFocusFile(t, dir, "a.csv", focusHeader+"\n"+good+"\n"+good+"\n0.1,0.1,USD\n")
	target := filepath.Join(t.TempDir(), "elsewhere.csv")
	if err := os.WriteFile(target, []byte(focusHeader+"\n"+good+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := true
	if err := os.Symlink(target, filepath.Join(dir, "b.csv")); err != nil {
		linked = false
	}

	loaded := openFocusStore(t).DB()
	got, err := LoadTokenFuseFocus(loaded, dir)
	if err != nil {
		t.Fatal(err)
	}
	msg, imported, err := importFrom(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sentence != msg {
		t.Errorf("LoadTokenFuseFocus says\n  %s\nand the connector's Import says\n  %s", got.Sentence, msg)
	}
	if got.FilesRead != 1 || got.Calls != 2 || got.RefusedRows != 1 {
		t.Errorf("read %+v, want 1 file, 2 calls and 1 refused row, the figures its own sentence states: %s",
			got, got.Sentence)
	}
	if linked && got.FilesNotRead != 1 {
		t.Errorf("FilesNotRead = %d, want the link it passed over counted: %s", got.FilesNotRead, got.Sentence)
	}
	var a, b int
	_ = loaded.QueryRow(`SELECT COUNT(*) FROM ai_calls`).Scan(&a)
	_ = imported.QueryRow(`SELECT COUNT(*) FROM ai_calls`).Scan(&b)
	if a != 2 || a != b {
		t.Errorf("ai_calls holds %d rows after LoadTokenFuseFocus and %d after Import, want 2 both ways", a, b)
	}
}

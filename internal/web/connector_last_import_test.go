package web_test

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheConnectorPageShowsWhatTheLastImportRead (invariant 89): the import's
// own sentence reaches the person who clicked Import. Before it, the web
// console discarded that sentence and redirected with no message, so a
// refused row, a call counted once from two exports, or a gateway refusal with
// nobody to file it under could be read only by calling Test afterwards.
func TestTheConnectorPageShowsWhatTheLastImportRead(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")

	data, err := os.ReadFile(filepath.Join("..", "connectors", "testdata", "tokenfuse-focus-2026-09-02.csv"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "focus.csv"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	code, loc := admin.post(t, "/connectors/tokenfuse-focus/save", url.Values{
		"path": {dir}, "csrf": {admin.csrf(t, "/connectors/tokenfuse-focus")},
	})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("saving the path: %d %s", code, loc)
	}
	_, before, _ := admin.get(t, "/connectors/tokenfuse-focus")
	if strings.Contains(before, "Last import") {
		t.Fatal("the page names a last import before any import ran")
	}
	code, loc = admin.post(t, "/connectors/tokenfuse-focus/import", url.Values{
		"csrf": {admin.csrf(t, "/connectors/tokenfuse-focus")}, "replace-generated": {"yes"},
	})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("importing: %d %s", code, loc)
	}
	_, body, _ := admin.get(t, "/connectors/tokenfuse-focus")
	if !strings.Contains(body, "Last import") || !strings.Contains(body, "Read 1 file") ||
		!strings.Contains(body, "5 rows, 3 distinct agents") {
		t.Errorf("the connector page does not show what the import read")
	}
}

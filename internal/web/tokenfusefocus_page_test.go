package web_test

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheAIPageReadsARealImport is the end-to-end proof that section 6's
// wiring holds through the actual HTTP surface, not only through the
// functions underneath it: an operator saves a folder, imports it, and the
// AI page it reaches afterwards reads the store rather than the generated
// world, with a per-agent table and a label saying Actions came from tagged
// outcomes.
func TestTheAIPageReadsARealImport(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")

	src := filepath.Join("..", "connectors", "testdata", "tokenfuse-focus-2026-09-02.csv")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "focus.csv"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	saveCSRF := admin.csrf(t, "/connectors/tokenfuse-focus")
	code, loc := admin.post(t, "/connectors/tokenfuse-focus/save", url.Values{
		"path": {dir}, "csrf": {saveCSRF},
	})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("saving the path: %d %s", code, loc)
	}

	// start(t) seeds the generated world (estate.Seed), so refusal 1 applies
	// here regardless of which month the fixture lands on: the check is
	// whether ANY row in charges has no provenance, not whether this
	// month's rows do.
	importCSRF := admin.csrf(t, "/connectors/tokenfuse-focus")
	code, loc = admin.post(t, "/connectors/tokenfuse-focus/import", url.Values{
		"csrf": {importCSRF}, "replace-generated": {"yes"},
	})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("importing with -replace-generated: %d %s", code, loc)
	}

	status, body, _ := admin.get(t, "/ai")
	if status != 200 {
		t.Fatalf("GET /ai: %d", status)
	}
	for _, want := range []string{
		"By agent", // the per-agent panel, shown only when Real
		"agent://taipanbox.dev/costcrew/forecaster", // an agent name from the fixture
		"outcomes tagged by the calling agents",     // hasOutcomes label
		"read from a connector",                     // the This month tile's sub-label when Real
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /ai does not contain %q", want)
		}
	}
	if strings.Contains(body, "What this cannot tell you") {
		t.Error("GET /ai still shows the generated-path panel after a real import")
	}
}

package web_test

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAnAWSExportReachesTheConsoleThroughTheConnectorPage is the end-to-end
// proof for the plain-FOCUS cloud reader, through the actual HTTP surface: an
// operator saves a folder and a tag key on the connector page, imports with
// the replace-generated box (the page must offer it for this connector, or
// the import can never run against the seeded estate), and the aws desk then
// shows the export's own services instead of the generated ones.
func TestAnAWSExportReachesTheConsoleThroughTheConnectorPage(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")

	data, err := os.ReadFile(filepath.Join("..", "connectors", "testdata", "aws-data-exports-focus-1-2-sample.csv"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "export-00001.csv"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	status, page, _ := admin.get(t, "/connectors/aws-data-exports")
	if status != 200 {
		t.Fatalf("GET the connector page: %d", status)
	}
	for _, want := range []string{"Tag key that names the team (optional)", "Folder the export was synced to",
		"replace-generated", "built"} {
		if !strings.Contains(page, want) {
			t.Errorf("the connector page does not contain %q", want)
		}
	}
	if strings.Contains(page, "documented, not built") || strings.Contains(page, "the reader is not written") {
		t.Error("the connector page still says its reader is not written")
	}

	csrf := admin.csrf(t, "/connectors/aws-data-exports")
	code, loc := admin.post(t, "/connectors/aws-data-exports/save", url.Values{
		"path": {dir}, "team_tag": {"team"}, "csrf": {csrf},
	})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("saving: %d %s", code, loc)
	}
	// Test needs only the folder; the four optional settings are not demanded.
	code, loc = admin.post(t, "/connectors/aws-data-exports/test", url.Values{"csrf": {csrf}})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("testing: %d %s", code, loc)
	}
	_, page, _ = admin.get(t, "/connectors/aws-data-exports")
	if !strings.Contains(page, "Would read 1 file") {
		t.Errorf("the test result does not describe the folder: %.600s", page)
	}

	code, loc = admin.post(t, "/connectors/aws-data-exports/import", url.Values{
		"csrf": {csrf}, "replace-generated": {"yes"},
	})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("importing: %d %s", code, loc)
	}

	status, body, _ := admin.get(t, "/desk/aws?period=2026-09")
	if status != 200 {
		t.Fatalf("GET /desk/aws?period=2026-09: %d", status)
	}
	if !strings.Contains(body, "Amazon Elastic Compute Cloud") {
		t.Errorf("the aws desk does not show the export's own service")
	}
}

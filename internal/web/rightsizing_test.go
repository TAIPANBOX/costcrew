package web_test

// C5-SPEC.md section 2's page bullet: a recommendations list per desk with
// the import's file and date, and "none imported" when empty. Red first
// against main: there is no /rightsizing route, so GET returns 404.

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheRightsizingPageReadsARealImport is the end-to-end proof through
// the actual HTTP surface, the same shape TestTheAIPageReadsARealImport
// already holds for tokenfuse-focus: an operator saves a folder, imports
// it, and the page reads the store rather than staying silent.
func TestTheRightsizingPageReadsARealImport(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")

	src := filepath.Join("..", "connectors", "testdata", "aws-rightsizing-2026-09-02.csv")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "aws-rightsizing-2026-09-02.csv"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	saveCSRF := admin.csrf(t, "/connectors/aws-rightsizing")
	code, loc := admin.post(t, "/connectors/aws-rightsizing/save", url.Values{
		"path": {dir}, "csrf": {saveCSRF},
	})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("saving the path: %d %s", code, loc)
	}
	importCSRF := admin.csrf(t, "/connectors/aws-rightsizing")
	code, loc = admin.post(t, "/connectors/aws-rightsizing/import", url.Values{"csrf": {importCSRF}})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("importing: %d %s", code, loc)
	}

	status, body, _ := admin.get(t, "/rightsizing")
	if status != 200 {
		t.Fatalf("GET /rightsizing: %d", status)
	}
	for _, want := range []string{
		"aws",
		"i-0a1b2c3d4e5f60789",
		"184.20",
		"m5.2xlarge",
		"m5.large",
		"aws-rightsizing-2026-09-02.csv", // the import's own file
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /rightsizing does not contain %q", want)
		}
	}
	// A desk nothing was ever imported for says so, rather than showing an
	// empty table with no explanation.
	if !strings.Contains(body, "none imported") {
		t.Error("GET /rightsizing does not say \"none imported\" for a desk with nothing imported")
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "</html>") {
		t.Error("GET /rightsizing stopped mid-document: it does not end with </html>")
	}
}

// TestTheThreeConnectorDetailPagesLinkToTheRightsizingPage is the crawl
// path a person (and the parity capture, which follows only href links)
// actually reaches /rightsizing by: each of the three new catalogue
// entries' own detail page names it.
func TestTheThreeConnectorDetailPagesLinkToTheRightsizingPage(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")

	for _, id := range []string{"aws-rightsizing", "gcp-recommender", "azure-advisor"} {
		status, body, _ := admin.get(t, "/connectors/"+id)
		if status != 200 {
			t.Fatalf("GET /connectors/%s: %d", id, status)
		}
		if !strings.Contains(body, `href="/rightsizing"`) {
			t.Errorf("/connectors/%s does not link to /rightsizing", id)
		}
	}
}

// TestTheRightsizingPageStartsWithNoneImported: on a fresh install, before
// any of the three readers has ever been pointed at a folder, every desk
// says "none imported" and the page still renders (200, not 500).
func TestTheRightsizingPageStartsWithNoneImported(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")

	status, body, _ := admin.get(t, "/rightsizing")
	if status != 200 {
		t.Fatalf("GET /rightsizing: %d", status)
	}
	if !strings.Contains(body, "none imported") {
		t.Error("GET /rightsizing on a fresh install does not say \"none imported\"")
	}
}

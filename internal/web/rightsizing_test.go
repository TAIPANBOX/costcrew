package web_test

// C5-SPEC.md section 2's page bullet: a recommendations list per desk with
// the import's file and date, and "none imported" when empty. Red first
// against main: there is no /rightsizing route, so GET returns 404.

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
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

// plantRightsizingRow inserts one recommendation row directly, the same
// shape internal/deliver's own plantRecommendation uses: saving_cents =
// 100*i and an identical Current/Recommended pair across every row on the
// desk, so a comparator that (wrongly) ranks by Current instead of saving
// degenerates entirely to the resource-name tie-break -- which, for these
// resource names, produces the exact REVERSE of the correct order, not an
// order that happens to agree with it by coincidence the way the AWS
// fixture's own varied Current strings can (internal/deliver/rightsizing_test.go's
// own comment on TestRecommendationsSectionRanksBySavingFromAFixtureImport).
func plantRightsizingRow(t *testing.T, h *harness, desk string, i int) {
	t.Helper()
	db := h.st.DB()
	if err := connectors.EnsureRecommendationsSchema(db); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("%s:res-%d", desk, i)
	if _, err := db.Exec(`INSERT INTO recommendations
		(id, provider, desk, resource, action, current, recommended,
		 monthly_saving_cents, lookback_days, source_file, imported_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		id, desk, desk, fmt.Sprintf("res-%d", i), "resize", "same-size", "same-size",
		100*i, 30, "planted.csv", "2026-09-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
}

// TestTheRightsizingPageOrdersRowsBySavingNotBySize is the row-order proof
// this file did not carry before: coordinator review of PR #34, 2026-09-03,
// found that every existing test here checked substring presence only, so a
// "rank by current cost" mutant planted directly in the page's own (then
// separate) copy of the ranking comparator compiled clean and passed this
// whole package. The page now calls connectors.RankBySaving, the one shared
// comparator deliver.recommendationsSection also uses, but this test proves
// the ORDER the page actually renders rather than trusting the refactor by
// construction.
func TestTheRightsizingPageOrdersRowsBySavingNotBySize(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")

	for i := 0; i < 4; i++ {
		plantRightsizingRow(t, h, "gcp", i)
	}

	status, body, _ := admin.get(t, "/rightsizing")
	if status != 200 {
		t.Fatalf("GET /rightsizing: %d", status)
	}

	// Saving-descending: res-3 (300), res-2 (200), res-1 (100), res-0 (0).
	wantOrder := []string{"res-3", "res-2", "res-1", "res-0"}
	last := -1
	for _, resource := range wantOrder {
		at := strings.Index(body, resource)
		if at < 0 {
			t.Fatalf("%s is missing from /rightsizing:\n%s", resource, body)
		}
		if at < last {
			t.Errorf("the rows are not in saving-descending order (%s appears out of "+
				"place): %v", resource, wantOrder)
		}
		last = at
	}
}

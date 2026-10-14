package web_test

// The AI desk reads real rows through five queries, and a read that fails in
// any of them must say so rather than render a page from whatever was read
// before it. Each fault below drops one column that only one of those reads
// uses, after a real TokenFuse export has been imported, so the page takes the
// real-data path and the failing read is the one under test.

import (
	"net/http"
	"strings"
	"testing"
)

// covDropColumn removes one column from the store. A column the store cannot
// drop (an index or a generated column names it) fails the test, because a
// fault that did not happen proves nothing about the page.
func covDropColumn(t *testing.T, h *harness, table, column string) {
	t.Helper()
	if _, err := h.st.DB().Exec(`ALTER TABLE ` + table + ` DROP COLUMN ` + column); err != nil {
		t.Fatalf("dropping %s.%s to break one read: %v", table, column, err)
	}
}

func TestCovAIPageRefusesToRenderWhenOneOfItsReadsFails(t *testing.T) {
	cases := []struct {
		name          string
		table, column string
	}{
		{"the latest real month", "charges", "provenance"},
		{"the units read", "charges", "model"},
		{"the agents read, by block reason", "ai_calls", "block_reason"},
		{"the export mix", "ai_calls", "key_id"},
		{"the identity warnings", "ai_calls", "identity_reason"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := start(t)
			h.signUp(t, "owner", "owner-password-2026")
			importExportsWeb(t, h, nil, "tokenfuse-focus-1.7.0-2026-10-07.csv")
			covDropColumn(t, h, c.table, c.column)

			code, body, _ := h.get(t, "/ai")
			if code != http.StatusInternalServerError || !strings.Contains(body, "store unavailable") {
				t.Errorf("with %s.%s gone the AI page answered %d, want 500 saying the store is unavailable",
					c.table, c.column, code)
			}
		})
	}
}

// The same import with nothing broken renders the real data, so the failures
// above are the read that broke and not the setup.
func TestCovAIPageRendersTheImportedRealMonthWhenEveryReadWorks(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	importExportsWeb(t, h, nil, "tokenfuse-focus-1.7.0-2026-10-07.csv")
	code, body, _ := h.get(t, "/ai")
	if code != http.StatusOK {
		t.Fatalf("GET /ai with the import in place: %d", code)
	}
	if !strings.Contains(body, "key:imposter") {
		t.Error("the imported export's agents are not on the AI page")
	}
}

// The runs page reads the latest real month, then the runs of that month. A
// failed read in either is a server fault, not an empty list of runs.
func TestCovAIRunsPageRefusesWhenOneOfItsReadsFails(t *testing.T) {
	cases := []struct {
		name          string
		table, column string
	}{
		{"the latest real month", "charges", "provenance"},
		{"the runs of the month", "ai_calls", "identity_reason"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := start(t)
			h.signUp(t, "owner", "owner-password-2026")
			importExportsWeb(t, h, nil, "tokenfuse-focus-runs-2026-10-14.csv")
			covDropColumn(t, h, c.table, c.column)

			code, body, _ := h.get(t, "/ai/runs")
			if code != http.StatusInternalServerError || !strings.Contains(body, "store unavailable") {
				t.Errorf("with %s.%s gone the runs page answered %d, want 500 saying the store is unavailable",
					c.table, c.column, code)
			}
		})
	}
}

package web_test

// C6-SPEC.md section 2, "the page": the SaaS page shows imported figures
// when loaded and says "generated" otherwise. Red first, run against main
// before practice.go's saas() read finops.Licences at all: every test below
// that checks for "Imported from a vendor's own seat export" or for the
// imported figures themselves fails, because main's /saas always shows
// world.Licences and always says nothing about its own source.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
)

// importSaasSeatsFixtureIntoHarness imports the connectors package's own
// fixture (internal/connectors/testdata/saas-seats-2026-09-03.csv) directly
// into the harness's store, the same file internal/finops/licences_test.go
// and internal/deliver/renewals_test.go already read. Going through
// connectors.Save/Import rather than a raw INSERT proves the whole chain --
// reader to computation to page -- rather than only the page's own SQL.
func importSaasSeatsFixtureIntoHarness(t *testing.T, h *harness) {
	t.Helper()
	src := filepath.Join("..", "connectors", "testdata", "saas-seats-2026-09-03.csv")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seats.csv"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	db := h.st.DB()
	if err := connectors.Save(db, "saas-seats", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := connectors.Import(db, "saas-seats", false, connectors.ImportOptions{}); err != nil {
		t.Fatal(err)
	}
}

// Nothing imported: the page says so, and it still shows the generated
// fixture -- this is the "otherwise" half of C6-SPEC.md's own sentence, and
// it is what every install had before this step, so it must not have moved.
func TestSaasPageSaysGeneratedWithNothingImported(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	code, body, _ := h.get(t, "/saas")
	if code != 200 {
		t.Fatalf("GET /saas: %d", code)
	}
	if !strings.Contains(body, "This is the generated fixture") {
		t.Errorf("/saas does not say it is showing the generated fixture:\n%s", body)
	}
	if strings.Contains(body, "Imported from a vendor") {
		t.Errorf("/saas claims an import that never happened:\n%s", body)
	}
	if !strings.Contains(body, "Zendesk") {
		t.Errorf("/saas with nothing imported does not show the generated fixture's own vendors:\n%s", body)
	}
}

// Imported: the page says so, and shows the imported figures rather than
// the generated fixture's.
func TestSaasPageShowsImportedFiguresWhenLoaded(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	importSaasSeatsFixtureIntoHarness(t, h)

	code, body, _ := h.get(t, "/saas")
	if code != 200 {
		t.Fatalf("GET /saas: %d", code)
	}
	if !strings.Contains(body, "Imported from a vendor's own seat export") {
		t.Errorf("/saas does not say the figures were imported:\n%s", body)
	}
	if strings.Contains(body, "This is the generated fixture") {
		t.Errorf("/saas still claims to be the generated fixture after an import:\n%s", body)
	}
	for _, want := range []string{"Zendesk", "Suite Professional", "Figma", "NetSuite"} {
		if !strings.Contains(body, want) {
			t.Errorf("/saas does not show the imported vendor/product %q:\n%s", want, body)
		}
	}
	// The generated fixture's own vendors (Datadog, GitHub's generated
	// price point aside) must not still be on the page: showing both would
	// be exactly the "mixed" figure invariant 20 exists to flag, and this
	// page has no such note because the two never actually mix here (the
	// generated licences never reach the store at all).
	if strings.Contains(body, "Datadog") {
		t.Errorf("/saas still shows a generated-only vendor after an import replaced the table:\n%s", body)
	}
	// The idle-seat tile: 18+41+0+1 = 60, from the fixture's own numbers,
	// not the generated fixture's.
	if !strings.Contains(body, `<div class="v">60</div>`) {
		t.Errorf("/saas's own Idle tile does not read 60 (the imported total):\n%s", body)
	}
}

// A row with no team (every imported row: the documented header carries no
// team column at all) renders without an empty, broken /team/ link.
func TestSaasPageGuardsTheTeamLinkOnAnImportedRow(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	importSaasSeatsFixtureIntoHarness(t, h)

	_, body, _ := h.get(t, "/saas")
	if strings.Contains(body, `href="/team/"`) {
		t.Errorf("/saas renders an empty /team/ link for an imported row:\n%s", body)
	}
	if !strings.Contains(body, "not tagged") {
		t.Errorf("/saas does not say \"not tagged\" for an imported row's empty team:\n%s", body)
	}
}

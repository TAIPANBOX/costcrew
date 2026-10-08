package web_test

// Invariant 95: four small sentences that said something untrue.

import (
	"strings"
	"testing"
)

func TestTheReconciliationPageStatesTheExactModelNameLimit(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	importAnthropicUsage(t, h)
	gatewayRow(t, h, 1, "2026-10-01", "claude-haiku-4-5", 1_055_001)
	_, body, _ := h.get(t, "/reconciliation?connector=anthropic-usage&from=2026-10-01&to=2026-10-03")
	for _, want := range []string{"matched by its exact name", "one gateway over on the alias and one gateway under on the dated id"} {
		if !strings.Contains(body, want) {
			t.Errorf("the reconciliation page does not say %q", want)
		}
	}
}

func TestTheUsageConnectorsAreListedAsFoldersNotCalls(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	_, body, _ := h.get(t, "/connectors")
	for _, id := range []string{"anthropic-usage", "openai-usage"} {
		i := strings.Index(body, `href="/connectors/`+id+`"`)
		if i < 0 {
			t.Fatalf("/connectors does not list %s", id)
		}
		row := body[i : i+strings.Index(body[i:], "</td>")]
		if strings.Contains(row, "· api") || !strings.Contains(row, "· export-drop") {
			t.Errorf("%s is listed as an API call although the console reads a folder: %s", id, row)
		}
	}
}

func TestTheFocusConnectorPageOffersNoDropAndLinksToTheAIPage(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	_, body, _ := h.get(t, "/connectors/tokenfuse-focus")
	if strings.Contains(body, "drop the folder") {
		t.Error("the connector page offers to take a dropped folder, which it cannot")
	}
	if !strings.Contains(body, `<a href="/ai">`) {
		t.Error("the TokenFuse connector page does not link to the AI spend page")
	}
}

package web_test

// Invariant 81 through the console's own read routes: the provider's figure
// beside the gateway's sum, the gap as its own column and its own line, a
// status per row, and a CSV a spreadsheet cannot run.

import (
	"encoding/csv"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
)

// importAnthropicUsage saves and imports the connectors package's own
// fixture through the console, the way an operator would.
func importAnthropicUsage(t *testing.T, admin *harness) {
	t.Helper()
	src := filepath.Join("..", "connectors", "testdata", "provider-usage", "anthropic")
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	code, loc := admin.post(t, "/connectors/anthropic-usage/save", url.Values{
		"path": {dir}, "csrf": {admin.csrf(t, "/connectors/anthropic-usage")},
	})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("saving the path: %d %s", code, loc)
	}
	code, loc = admin.post(t, "/connectors/anthropic-usage/import", url.Values{
		"csrf": {admin.csrf(t, "/connectors/anthropic-usage")},
	})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("importing: %d %s", code, loc)
	}
}

func gatewayRow(t *testing.T, h *harness, row int, day, model string, micros int64) {
	t.Helper()
	db := h.st.DB()
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ai_calls(file_sha256, row_no, ts, day, agent, provider, model,
		tokens_in, tokens_out, billed_microusd, blocked, basis) VALUES ('w',?,?,?,'agent://x/a','Anthropic',?,0,0,?,0,'settled')`,
		row, day+"T00:00:00Z", day, model, micros); err != nil {
		t.Fatal(err)
	}
}

func TestTheReconciliationPageShowsTheGapAndTheStatus(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	importAnthropicUsage(t, h)
	gatewayRow(t, h, 1, "2026-10-01", "claude-haiku-4-5", 1_055_001)
	gatewayRow(t, h, 2, "2026-10-01", "claude-sonnet-4-5", 237_891)
	gatewayRow(t, h, 3, "2026-10-03", "claude-haiku-4-5", 5_000)

	code, body, _ := h.get(t, "/reconciliation?connector=anthropic-usage&from=2026-10-01&to=2026-10-03")
	if code != 200 {
		t.Fatalf("GET /reconciliation: %d\n%s", code, body)
	}
	for _, want := range []string{
		"1.237891", "0.237891", "-1.000000", // sonnet: provider, gateway, gap
		"matched", "gateway under", "provider missing",
		// The window's whole gap: gateway 1.297892 against provider 12.392892.
		"-11.095000",
		"Download as CSV", "Provider rows kept",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not show %q", want)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "</html>") {
		t.Error("the page stopped mid-document")
	}
}

func TestTheReconciliationCSVCarriesEveryRowAndTheGapLine(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	importAnthropicUsage(t, h)
	gatewayRow(t, h, 1, "2026-10-01", "claude-haiku-4-5", 1_055_000)
	gatewayRow(t, h, 2, "2026-10-01", "=HYPERLINK(\"http://evil\")", 7)

	code, body, _ := h.get(t, "/export/reconciliation.csv?connector=anthropic-usage&from=2026-10-01&to=2026-10-02")
	if code != 200 {
		t.Fatalf("GET the CSV: %d %s", code, body)
	}
	recs, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("the CSV does not parse: %v", err)
	}
	if strings.Join(recs[0], ",") != "day,model,provider_usd,gateway_usd,gap_usd,status,"+
		"provider_tokens_in,gateway_tokens_in,provider_tokens_out,gateway_tokens_out" {
		t.Errorf("header %v", recs[0])
	}
	found := false
	for _, r := range recs[1 : len(recs)-1] {
		if r[0] == "2026-10-01" && r[1] == "claude-haiku-4-5" {
			found = true
			if r[2] != "1.055001" || r[3] != "1.055000" || r[4] != "-0.000001" || r[5] != "matched" {
				t.Errorf("haiku row %v: want the one-micro gap printed beside a matched status", r)
			}
		}
		if strings.HasPrefix(r[1], "=") {
			t.Errorf("a model name reaches the CSV as a formula: %q", r[1])
		}
	}
	if !found {
		t.Error("no haiku row in the CSV")
	}
	last := recs[len(recs)-1]
	// provider 12.392892 (haiku, sonnet and web search on 10-01, haiku on
	// 10-02), gateway 1.055007, gap -11.337885.
	if last[0] != "total" || last[2] != "12.392892" || last[3] != "1.055007" || last[4] != "-11.337885" {
		t.Errorf("the last line %v, want the total with the window's gap -11.337885", last)
	}
}

func TestTheReconciliationPageSaysWhenThereIsNothingToReconcile(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	code, body, _ := h.get(t, "/reconciliation?connector=openai-usage")
	if code != 200 || !strings.Contains(body, "Nothing to reconcile") {
		t.Errorf("an empty reconciliation: %d, does not say there is nothing to reconcile", code)
	}
}

func TestTheReconciliationRefusesAnUnknownConnectorOrABadWindow(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	for _, p := range []string{"/reconciliation?connector=opencost", "/export/reconciliation.csv?connector=../etc"} {
		if code, _, _ := h.get(t, p); code != 404 {
			t.Errorf("GET %s: %d, want 404", p, code)
		}
	}
	for _, p := range []string{"/reconciliation?from=yesterday", "/export/reconciliation.csv?from=2026-10-02&to=2026-10-01"} {
		if code, _, _ := h.get(t, p); code != 400 {
			t.Errorf("GET %s: %d, want 400", p, code)
		}
	}
}

func TestAViewerReadsTheReconciliation(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	if ok, err := h.au.Create("watcher", "watcher-password-2026", "viewer"); err != nil || !ok {
		t.Fatalf("creating a viewer: %v %v", ok, err)
	}
	viewer := h.as(t, "watcher", "watcher-password-2026")
	for _, p := range []string{"/reconciliation", "/export/reconciliation.csv"} {
		if code, _, _ := viewer.get(t, p); code != 200 {
			t.Errorf("a viewer GET %s: %d, want 200", p, code)
		}
	}
}

func TestTheAIPageAndTheConnectorLinkToTheReconciliation(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	for p, want := range map[string]string{
		"/ai":                         `href="/reconciliation"`,
		"/connectors/anthropic-usage": `href="/reconciliation?connector=anthropic-usage"`,
		"/connectors/openai-usage":    `href="/reconciliation?connector=openai-usage"`,
	} {
		if _, body, _ := h.get(t, p); !strings.Contains(body, want) {
			t.Errorf("%s does not link %s", p, want)
		}
	}
}

package web_test

// Invariant 102: each gateway door is named as its own source when the input
// tells them apart, on /engines and on /ai/runs, and both pages say "one
// source" when it does not.

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// importTwoDoorsWeb saves the main folder and the OpenAI-wire door's folder on
// the connector through its own form, and imports both.
func importTwoDoorsWeb(t *testing.T, admin *harness) {
	t.Helper()
	dirs := map[string]string{}
	for in, name := range map[string]string{"path": "tokenfuse-focus-runs-2026-10-14.csv",
		"path_openai": "tokenfuse-focus-openai-door-2026-10-14.csv"} {
		data, err := os.ReadFile(filepath.Join("..", "connectors", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		dirs[in] = dir
	}
	code, loc := admin.post(t, "/connectors/tokenfuse-focus/save", url.Values{
		"path": {dirs["path"]}, "path_openai": {dirs["path_openai"]},
		"csrf": {admin.csrf(t, "/connectors/tokenfuse-focus")},
	})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("saving the folders: %d %s", code, loc)
	}
	code, loc = admin.post(t, "/connectors/tokenfuse-focus/import", url.Values{
		"csrf": {admin.csrf(t, "/connectors/tokenfuse-focus")}, "replace-generated": {"yes"},
	})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("importing: %d %s", code, loc)
	}
}

func TestTheRunsPageNamesTheDoorsWhenTheInputTellsThemApart(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")
	importTwoDoorsWeb(t, admin)

	_, body, _ := admin.get(t, "/ai/runs")
	a := runRow(t, body, "w2-b12-A")
	for _, want := range []string{"two doors", "main gateway<span class=\"sub\">5 calls, 0.05</span>",
		"OpenAI-wire door<span class=\"sub\">2 calls, 0.0060</span>", "<code>openai/gpt-4.1-mini</code> 10%"} {
		if !strings.Contains(a, want) {
			t.Errorf("w2-b12-A's row does not show %q", want)
		}
	}
	if d := runRow(t, body, "w2-b12-D"); strings.Contains(d, "two doors") || !strings.Contains(d, "OpenAI-wire door") {
		t.Error("a run through the OpenAI-wire door alone is not named as that door's, or is marked as crossing")
	}
	for _, want := range []string{"<th>Door</th>", "spent against two ledgers",
		"<div class=\"k\">Two doors</div><div class=\"v up\">1</div>"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /ai/runs does not contain %q", want)
		}
	}
	if strings.Contains(body, "read from one source") {
		t.Error("/ai/runs says one source while two doors' exports were read")
	}

	_, eng, _ := admin.get(t, "/engines")
	panel := section(t, eng, "gateway-doors")
	for _, want := range []string{"read per door from the gateways' own exports",
		"<li>main gateway: 15 calls, 0.08</li>", "<li>OpenAI-wire door: 3 calls, 0.0071</li>"} {
		if !strings.Contains(panel, want) {
			t.Errorf("/engines' gateway doors panel does not contain %q", want)
		}
	}
}

func TestTheEnginesPageNamesEachGatewayDoorThisConsoleCallsThrough(t *testing.T) {
	h := startWithGateways(t, "https://gw.example:4100/v1?k=never-printed", "http://door.example:4110")
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")
	_, body, _ := admin.get(t, "/engines")
	panel := section(t, body, "gateway-doors")
	anth := tableRow(t, panel, "Anthropic-wire gateway")
	for _, want := range []string{"<code>-gateway</code>", "<code>gw.example:4100</code>", "Anthropic API"} {
		if !strings.Contains(anth, want) {
			t.Errorf("the Anthropic-wire door's row does not contain %q", want)
		}
	}
	oai := tableRow(t, panel, "OpenAI-wire door")
	for _, want := range []string{"<code>-gateway-openai</code>", "<code>door.example:4110</code>", "OpenRouter", "A model you host yourself"} {
		if !strings.Contains(oai, want) {
			t.Errorf("the OpenAI-wire door's row does not contain %q", want)
		}
	}
	if strings.Contains(anth, "OpenRouter") || strings.Contains(oai, "Anthropic API") {
		t.Error("an engine is listed under the door that does not front its wire")
	}
	if !strings.Contains(panel, "Refused while a door is set, because no door fronts their wire: Amazon Bedrock.") {
		t.Error("/engines does not name the engine a configured gateway refuses")
	}
	if strings.Contains(body, "never-printed") || strings.Contains(body, "/v1?") {
		t.Error("/engines prints a gateway URL's path or query, not only its host")
	}
	if !strings.Contains(panel, "Nothing imported yet") {
		t.Error("/engines does not say nothing has been imported through any door")
	}
}

func TestTheEnginesPageSaysOneSourceWhenTheInputDoesNotTellDoorsApart(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")
	_, before, _ := admin.get(t, "/engines")
	if p := section(t, before, "gateway-doors"); !strings.Contains(p, "No door is set") ||
		!strings.Contains(tableRow(t, p, "OpenAI-wire door"), "not set") {
		t.Error("/engines with no gateway configured does not say no door is set")
	}

	importExportsWeb(t, admin, nil, "tokenfuse-focus-runs-2026-10-14.csv")
	_, body, _ := admin.get(t, "/engines")
	panel := section(t, body, "gateway-doors")
	for _, want := range []string{"One source.", "does not say which door a call came through",
		"15 calls (0.08) were all read from one folder, the main gateway's"} {
		if !strings.Contains(panel, want) {
			t.Errorf("/engines after one folder's import does not contain %q:\n%s", want, panel)
		}
	}
	if strings.Contains(panel, "read per door") {
		t.Error("/engines names doors that one folder cannot tell apart")
	}
}

// tableRow is the markup of the table row that names name, up to its </tr>.
func tableRow(t *testing.T, body, name string) string {
	t.Helper()
	i := strings.Index(body, "<td>"+name+"</td>")
	if i < 0 {
		t.Fatalf("no row for %s", name)
	}
	j := strings.Index(body[i:], "</tr>")
	return body[i : i+j]
}

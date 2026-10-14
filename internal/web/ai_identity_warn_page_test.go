package web_test

// Invariant 100: /ai marks the calls TOKENFUSE_IDENTITY_STRICT=warn forwarded
// although the identity check failed, with their spend, on the agent's row,
// on the team's row, and per team and per run in a panel of their own; and
// it says so when the month's export cannot tell.

import (
	"strings"
	"testing"
)

// section is the markup of the panel with the given id, up to the next panel,
// so an assertion about it cannot be met by another part of the page.
func section(t *testing.T, body, id string) string {
	t.Helper()
	i := strings.Index(body, `id="`+id+`"`)
	if i < 0 {
		t.Fatalf("no panel with id %q on the page", id)
	}
	rest := body[i:]
	if j := strings.Index(rest[1:], `<div class="panel"`); j >= 0 {
		return rest[:j+1]
	}
	return rest
}

func TestTheAIPageMarksCallsForwardedUnderIdentityWarn(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")
	importExportsWeb(t, admin, nil, "tokenfuse-focus-1.8.0-2026-10-14.csv")

	_, body, _ := admin.get(t, "/ai")
	if !strings.Contains(body, "<strong>3 calls (0.16)</strong> this month were forwarded under identity warn mode") {
		t.Error("/ai does not say, above the tables, how many calls and how much spend warn mode forwarded")
	}
	flint := agentRow(t, body, "agent://acme.example/finops/flint")
	if !strings.Contains(flint, "identity warn") || !strings.Contains(flint, "2 of these calls (0.15) forwarded although the identity check failed") {
		t.Error("flint's By agent row does not mark its two forwarded calls and their spend")
	}
	if edge := agentRow(t, body, "agent://acme.example/routers/edge"); !strings.Contains(edge, "1 of these calls (0.01)") {
		t.Error("edge's By agent row does not mark its forwarded call")
	}
	if key := agentRow(t, body, "key:imposter"); strings.Contains(key, "identity warn") {
		t.Error("a refusal for identity is marked as a call warn mode forwarded")
	}

	panel := section(t, body, "identity-warn")
	for _, want := range []string{"<h2>Forwarded under identity warn mode</h2>",
		"<strong>3 calls, 0.16</strong>",
		"<td>home-routers</td>", "<td>edge-lab</td>",
		"<code>run-7</code>", "<code>run-8</code>",
		"<code>2 agent_id_not_allowed</code>", "<code>1 unit_chosen_by_agent_id</code>"} {
		if !strings.Contains(panel, want) {
			t.Errorf("the identity warn panel does not contain %q", want)
		}
	}
	// Largest spend first, by team and by run.
	if strings.Index(panel, "<td>home-routers</td>") > strings.Index(panel, "<td>edge-lab</td>") ||
		strings.Index(panel, "<code>run-7</code>") > strings.Index(panel, "<code>run-8</code>") {
		t.Error("the panel does not list the largest forwarded spend first")
	}
	if strings.Contains(panel, "<td>finops</td>") || strings.Contains(panel, "<code>run-1</code>") {
		t.Error("an honest team or run is listed among the forwarded calls")
	}

	// The team rows of the price table carry the flag, and only those.
	price := body[:strings.Index(body, "<h2>By agent</h2>")]
	if !strings.Contains(price, "identity warn: 2") || !strings.Contains(price, "identity warn: 1") {
		t.Error("the team rows of the price and volume table carry no identity warn flag")
	}
	if strings.Count(price, "chip triaged") != 3 {
		t.Errorf("%d identity warn flags in the price table, want 3 (home-routers on two models, edge-lab on one)",
			strings.Count(price, "chip triaged"))
	}
}

func TestTheAIPageSaysAnOlderExportCannotTellAWarnedCall(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")
	importExportsWeb(t, admin, nil, "tokenfuse-focus-1.7.0-2026-10-07.csv")

	_, body, _ := admin.get(t, "/ai")
	panel := section(t, body, "identity-warn")
	if !strings.Contains(panel, "4 calls this month came from an export written before TokenFuse 1.8.0") {
		t.Error("/ai does not say that a 1.7.0 export cannot tell a warned call from an honest one")
	}
	if strings.Contains(body, "were forwarded under identity warn mode") || strings.Contains(panel, "No call this month") {
		t.Error("/ai claims to know what warn mode forwarded from an export that does not say")
	}
}

func TestTheAIPageSaysNothingWasForwardedWhenTheExportSaysSo(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")
	importExportsWeb(t, admin, func(_, body string) string {
		// The same export with every finding blanked: the column is there, and
		// nothing was forwarded.
		lines := strings.Split(strings.TrimSpace(body), "\n")
		for i := 1; i < len(lines); i++ {
			lines[i] = lines[i][:strings.LastIndex(lines[i], ",")+1]
		}
		return strings.Join(lines, "\n") + "\n"
	}, "tokenfuse-focus-1.8.0-2026-10-14.csv")

	_, body, _ := admin.get(t, "/ai")
	panel := section(t, body, "identity-warn")
	if !strings.Contains(panel, "No call this month was forwarded under identity warn mode.") {
		t.Error("/ai does not say that nothing was forwarded when the export reports every call")
	}
	if strings.Contains(body, "chip triaged") {
		t.Error("/ai flags a team or an agent when nothing was forwarded")
	}
}

package web_test

// Invariant 94: a customer unit's page names the business unit a stamped rule
// charges it back under, /teams lists the customer units and calls a team's
// own unit its business unit, and the chargeback page's units panel says which
// period its figures are, not "today".

import (
	"net/url"
	"strings"
	"testing"
)

// stampedUnits is startWithUnits with the aws unit charged back under
// "Customer One" by the owner's own stamp, through the route; gcp stays
// without a rule.
func stampedUnits(t *testing.T) *harness {
	t.Helper()
	h := startWithUnits(t)
	h.signUp(t, "boss", "boss-password-2026")
	if _, err := h.au.Create("owner1", "owner1-password-2026", "operator"); err != nil {
		t.Fatal(err)
	}
	owner := h.as(t, "owner1", "owner1-password-2026")
	art, ord := plantCarriedUnitRule(t, h, newUnitSprint(t, h), "owner1", "aws", "Customer One")
	if code, loc := owner.post(t, stampPath(art, ord), url.Values{"csrf": {owner.csrf(t, "/board")}}); code != 303 ||
		strings.Contains(loc, "msg=") {
		t.Fatalf("the owner's stamp: %d %s", code, loc)
	}
	return h
}

func TestAUnitsPageNamesTheBusinessUnitItsRuleChargesItUnder(t *testing.T) {
	h := stampedUnits(t)
	_, aws, _ := h.get(t, "/team/aws?period=2026-09")
	if !strings.Contains(aws, "charged back under business unit <strong>Customer One</strong> by a rule owner1 stamped on") {
		t.Errorf("a ruled unit's page does not name its business unit:\n%s", fragment(aws, "<header", 600))
	}
	if strings.Contains(aws, "it has no business unit") {
		t.Error("a ruled unit's page says it has no business unit")
	}
	_, gcp, _ := h.get(t, "/team/gcp?period=2026-09")
	if !strings.Contains(gcp, "No rule charges it back under a business unit yet") {
		t.Errorf("an unruled unit's page does not say no rule covers it yet:\n%s", fragment(gcp, "<header", 600))
	}
}

func TestTheTeamsPageListsTheCustomerUnitsAndNamesBusinessUnits(t *testing.T) {
	h := stampedUnits(t)
	_, body, _ := h.get(t, "/teams?period=2026-09")
	for _, want := range []string{`<h2>Customer units</h2>`, `<a href="/team/aws?period=2026-09">aws</a>`,
		`<td>Customer One</td>`, `<a href="/team/gcp?period=2026-09">gcp</a>`, `href="/chargeback?period=2026-09#units"`,
		`>Business unit</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("/teams does not contain %q", want)
		}
	}
	if strings.Contains(body, `>Unit</a>`) {
		t.Error("/teams still calls a team's business unit its Unit")
	}

	plain := start(t)
	plain.signUp(t, "boss", "boss-password-2026")
	_, team, _ := plain.get(t, "/team/data-eng")
	if !strings.Contains(team, "<p>Business unit ") {
		t.Errorf("a roster team's page does not say Business unit:\n%s", fragment(team, "<header", 400))
	}
	if _, teams, _ := plain.get(t, "/teams"); strings.Contains(teams, "<h2>Customer units</h2>") {
		t.Error("/teams shows a customer units panel on an estate with no units")
	}
}

func TestTheChargebackUnitsPanelNamesItsPeriodNotToday(t *testing.T) {
	h := stampedUnits(t)
	_, body, _ := h.get(t, "/chargeback?period=2026-09")
	if strings.Contains(body, "today&#39;s figures") || strings.Contains(body, "today's figures") ||
		strings.Contains(body, `<th class="num">Today</th>`) {
		t.Error("the chargeback units panel still says its figures are today's")
	}
	for _, want := range []string{"These are 2026-09&#39;s figures as they stand now", `<th class="num">2026-09, live</th>`,
		`id="units"`} {
		if !strings.Contains(body, want) && !strings.Contains(body, strings.ReplaceAll(want, "&#39;", "'")) {
			t.Errorf("the chargeback units panel does not contain %q", want)
		}
	}
}

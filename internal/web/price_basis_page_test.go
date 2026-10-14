package web_test

// Invariant 103: /ai, /ai/runs and /reconciliation show a fallback-priced
// amount apart, never inside spend, and /ai says when the export does not
// report a basis at all.

import (
	"strings"
	"testing"
)

func TestTheAIPageShowsFallbackPricedSpendApart(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")
	importExportsWeb(t, admin, nil, "tokenfuse-focus-price-basis-2026-10-14.csv")

	_, body, _ := admin.get(t, "/ai")
	for _, want := range []string{
		`<div class="k">This month</div><div class="v">0.06</div>`,
		`<div class="k">Fallback-priced</div><div class="v up">0.0024</div>`,
		"<strong>2 calls (0.0024)</strong> this month were priced at the gateway's fallback rate",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /ai does not contain %q", want)
		}
	}
	if !strings.Contains(agentRow(t, body, "agent://home.example/lab/pricer"),
		"0.06<span class=\"sub\">+ 0.0024 fallback-priced, 2 calls, not in this cost</span>") {
		t.Error("the agent's row does not keep its fallback estimate apart from its cost")
	}
	panel := section(t, body, "price-basis")
	for _, want := range []string{"Priced from the book: 3 calls, 0.06.",
		"<strong>Fallback-priced: 2 calls, 0.0024</strong>",
		"<td><code>openai/gpt-4.1-mini</code></td><td><code>fallback</code></td><td class=\"num\">2</td><td class=\"num\">0.0024</td>",
		"1 call (0.0010) came from an export with the column and no basis on the row"} {
		if !strings.Contains(panel, want) {
			t.Errorf("the price basis panel does not contain %q", want)
		}
	}
	if strings.Contains(body, "gpt-4.1-mini</td>\n        <td class=\"num\">") {
		t.Error("the fallback-priced model has a row of its own in the price and volume table")
	}

	_, runs, _ := admin.get(t, "/ai/runs")
	if p1 := runRow(t, runs, "run-p1"); !strings.Contains(p1, "+ 0.0004 fallback-priced, not in spend") ||
		!strings.Contains(p1, "fallback estimate only") {
		t.Error("run-p1 does not keep its fallback estimate apart")
	}

	_, rec, _ := admin.get(t, "/reconciliation?connector=openai-usage&from=2026-10-14&to=2026-10-14")
	if !strings.Contains(rec, "of which 0.002360 priced at the gateway's fallback rate") {
		t.Error("/reconciliation does not mark the fallback-priced part of the gateway's sum")
	}
}

func TestTheAIPageSaysThePriceBasisIsNotReportedByAnOlderGateway(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")
	importExportsWeb(t, admin, nil, "tokenfuse-focus-1.8.0-2026-10-14.csv")
	_, body, _ := admin.get(t, "/ai")
	panel := section(t, body, "price-basis")
	if !strings.Contains(panel, "Price basis not reported by this gateway version: 4 calls (0.21)") {
		t.Error("/ai does not say the price basis is not reported by an export without the column")
	}
	if strings.Contains(body, "Fallback-priced") || strings.Contains(panel, "Priced from the book") {
		t.Error("/ai claims a price basis an export without the column never gave")
	}
}

func TestTheAIPageNamesThePersonalDataLetThrough(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")
	importExportsWeb(t, admin, nil, "tokenfuse-focus-price-basis-2026-10-14.csv")
	_, body, _ := admin.get(t, "/ai")
	panel := section(t, body, "pii")
	for _, want := range []string{"<h2>Personal data found and let through</h2>",
		"<strong>2 calls</strong> this month carried personal data the gateway let through: 3 <code>pii_email</code>, 1 <code>pii_card</code>.",
		"<td>agent://home.example/lab/pricer</td><td class=\"num\">2</td>"} {
		if !strings.Contains(panel, want) {
			t.Errorf("the PII panel does not contain %q", want)
		}
	}
	_, runs, _ := admin.get(t, "/ai/runs")
	if p1 := runRow(t, runs, "run-p1"); !strings.Contains(p1, "PII let through</span> 2 calls: 3 pii_email, 1 pii_card") {
		t.Error("run-p1's row does not name the personal data let through")
	}

	h2 := start(t)
	h2.signUp(t, "boss", "boss-password-2026")
	admin2 := h2.as(t, "boss", "boss-password-2026")
	importExportsWeb(t, admin2, nil, "tokenfuse-focus-1.8.0-2026-10-14.csv")
	_, older, _ := admin2.get(t, "/ai")
	if p := section(t, older, "pii"); !strings.Contains(p, "PII findings not reported by this gateway version: 4 calls") ||
		strings.Contains(p, "No call this month") {
		t.Error("/ai does not say an export without x_pii_findings cannot tell")
	}
}

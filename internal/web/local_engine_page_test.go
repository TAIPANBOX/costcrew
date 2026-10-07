package web_test

import (
	"html"
	"net/http"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/engines"
)

// A supervisor on the local engine was refused with "an unknown or unmetered
// engine", which is neither: local is in the catalogue and is metered when the
// runner is given a price. What stops the plan-ask is that this console never
// configures the local engine, so it has no price to bound the call by and no
// token ceiling of its own. The refusal now says that, and where a local run
// can be bounded instead.
func TestAskPlanForALocalSupervisorSaysWhyItIsRefused(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	if _, err := h.st.DB().Exec(`UPDATE analysts SET engine = ? WHERE name = 'supervisor'`,
		engines.LocalID); err != nil {
		t.Fatal(err)
	}
	_, getBody, _ := h.get(t, "/sprint/plan")
	form := planFormFields(t, getBody)
	form.Set("csrf", h.csrf(t, "/sprint/plan"))
	code, body := postBody(t, h, "/sprint/plan/ask", form)
	if code != http.StatusOK {
		t.Fatalf("POST /sprint/plan/ask = %d, want a shown refusal", code)
	}
	text := html.UnescapeString(body)
	if strings.Contains(text, "unknown or unmetered") {
		t.Errorf("a local supervisor is still called an unknown or unmetered engine: %s",
			excerpt(text, "cannot be priced"))
	}
	for _, want := range []string{"local engine", "no price", "costcrew-run"} {
		if !strings.Contains(text, want) {
			t.Errorf("the refusal does not say %q: %s", want, excerpt(text, "refused"))
		}
	}
}

// /engines listed three of the catalogue's five families, so Amazon Bedrock
// (cloud-role) and a model the organisation hosts (self-hosted) were offered on
// the hire form and absent from the page that explains the engines.
func TestTheEnginesPageListsEveryFamilyInTheCatalogue(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	code, body, _ := h.get(t, "/engines")
	if code != http.StatusOK {
		t.Fatalf("GET /engines: %d", code)
	}
	text := html.UnescapeString(body)
	seen := map[engines.Family]bool{}
	for _, e := range engines.Catalogue {
		if seen[e.Family] {
			continue
		}
		seen[e.Family] = true
		if title := engines.FamilyTitle(e.Family); !strings.Contains(text, title) {
			t.Errorf("/engines does not list the %s family (%q), which %s belongs to",
				e.Family, title, e.ID)
		}
	}
	if len(seen) < 5 {
		t.Fatalf("the catalogue names %d families; the scan is broken", len(seen))
	}
}

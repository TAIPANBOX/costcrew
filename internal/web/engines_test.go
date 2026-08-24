package web_test

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/engines"
)

// Every analyst is hired with an engine this console knows.
//
// The hire form offers the engine catalogue: claude-cli, openrouter,
// anthropic, deepseek, local-cli. The thirty-nine seeded analysts carry
// claude-strong and kimi-standard, which are the FIXTURE's names for a model
// tier and are in no catalogue at all.
//
// Nothing noticed, because nothing ever asked. The Engines page reports on the
// catalogue and the agent card prints whatever string the roster holds, so an
// engine that means nothing renders exactly like one that means something.
// It surfaced the first time something tried to ACT on the value: the dry-run
// estimator could not price a single one of the seventy-seven open tasks.
//
// The direction of the failure is what makes it worth a gate. An unknown
// engine has no price, and the safe reading of "no price" is refuse. The first
// version of Metered returned a bare false and the estimator read all
// thirty-nine as "on a subscription, nothing new billed", which is the reading
// that spends money.
func TestEveryAnalystIsHiredWithAnEngineTheConsoleKnows(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "boss", "boss-password-2026")

	known := map[string]bool{}
	for _, e := range engines.Catalogue {
		known[e.ID] = true
	}
	roster, err := crew.Roster(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	bad := map[string]int{}
	for _, a := range roster {
		if a.Engine == "" {
			continue
		}
		if !known[a.Engine] {
			bad[a.Engine]++
		}
	}
	for name, n := range bad {
		t.Errorf("%d analysts are hired with engine %q, which is not in the "+
			"catalogue the hire form offers: nothing can price a call to it, "+
			"and nothing on any page says so", n, name)
	}
}

// And the catalogue is what the form offers, so the two cannot drift apart in
// the other direction either.
func TestTheHireFormOffersTheCatalogue(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "boss", "boss-password-2026")

	_, body, _ := h.get(t, "/staff/new")
	for _, e := range engines.Catalogue {
		if !strings.Contains(body, ">"+e.ID+"<") {
			t.Errorf("the hire form does not offer %q, which the engines page "+
				"documents and the estimator prices", e.ID)
		}
	}
}

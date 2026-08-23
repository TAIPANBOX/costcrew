package web_test

import (
	"regexp"
	"strings"
	"testing"
)

// Pages that read ?period, and therefore pages a link into must not send the
// reader to on a different month than the one they are looking at.
var periodAware = []string{"desk", "service", "team"}

var anyLink = regexp.MustCompile(`href="/(desk|service|team)/([^"]+)"`)

// A period the reader CHOSE survives the click.
//
// Choosing May on the desks list and clicking a desk landed on July, because
// the link carried no period and the desk page fell back to its own default.
// The figure the reader was looking at and the figure they land on then answer
// different questions, and nothing on either page says a month was discarded.
//
// This is sharper than the Overview case that led to it: there the two pages
// merely defaulted differently, here the console throws away an explicit
// choice.
func TestAChosenPeriodSurvivesTheClick(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "boss", "boss-password-2026")

	// A month that is not any page's default, so a link that carries nothing
	// is visibly wrong rather than accidentally right.
	const chosen = "2026-05"

	for _, from := range []string{"/desks", "/services", "/teams"} {
		code, body, _ := h.get(t, from+"?period="+chosen)
		if code != 200 {
			t.Errorf("GET %s?period=%s answered %d", from, chosen, code)
			continue
		}
		if !strings.Contains(body, chosen) {
			t.Errorf("%s does not show %s at all; the fixture may not have that "+
				"month and this test then proves nothing", from, chosen)
			continue
		}
		checked, bare := 0, 0
		for _, m := range anyLink.FindAllStringSubmatch(body, -1) {
			kind, rest := m[1], m[2]
			if !contains(periodAware, kind) {
				continue
			}
			checked++
			if !strings.Contains(rest, "period="+chosen) {
				bare++
				if bare == 1 {
					t.Errorf("%s at %s links to /%s/%s with no period: the "+
						"month the reader chose is dropped on the way",
						from, chosen, kind, rest)
				}
			}
		}
		if checked == 0 {
			t.Errorf("%s has no links into a period-aware page; the scan is broken", from)
		}
		if bare > 1 {
			t.Errorf("  and %d more links on %s do the same", bare-1, from)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

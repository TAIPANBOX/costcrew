package web_test

import (
	"strings"
	"testing"
)

// The row opens what its first cell names, without JavaScript.
//
// Measured on /services before this: the link was 87x17 in a row of 1180x43,
// so a reader had to hit 2.8% of the row, forty-eight times down the page.
//
// This is CSS only, so what a test can check is that the rule is served and
// that the markup it depends on is still shaped the way it expects: a first
// cell whose direct child is the link. A template that wraps the name in
// something else would leave the rule matching nothing, and the page would
// look identical while every row went back to a 2.8% target.
func TestTheRowClickRuleIsServedAndItsMarkupHolds(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "boss", "boss-password-2026")

	code, css, _ := h.get(t, "/static/app.css")
	if code != 200 {
		t.Fatalf("the stylesheet answered %d", code)
	}
	for _, rule := range []string{
		"tbody tr { position: relative; }",
		"tbody td:first-child > a::after",
	} {
		if !strings.Contains(css, rule) {
			t.Errorf("the stylesheet no longer carries %q, so no row opens "+
				"anything but its own short link", rule)
		}
	}
	// The figures must stay above the overlay, or a table of money stops
	// being selectable.
	if !strings.Contains(css, "tbody td.num") {
		t.Error("td.num is no longer lifted above the overlay: dragging across " +
			"a figure would select nothing")
	}

	// And the markup the rule depends on.
	for _, page := range []string{"/services", "/desks", "/teams", "/staff"} {
		_, body, _ := h.get(t, page)
		i := strings.Index(body, "<tbody>")
		if i < 0 {
			t.Errorf("%s has no tbody", page)
			continue
		}
		row := body[i:]
		if j := strings.Index(row, "</tr>"); j > 0 {
			row = row[:j]
		}
		if !strings.Contains(row, `<td><a href="/`) {
			t.Errorf("%s: the first cell's link is not a direct child of the "+
				"cell any more, so the row-click rule matches nothing:\n  %s",
				page, strings.TrimSpace(row)[:min(160, len(strings.TrimSpace(row)))])
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

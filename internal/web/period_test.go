package web_test

import (
	"regexp"
	"strings"
	"testing"
)

var deskHref = regexp.MustCompile(`href="/desk/([a-z]+)(\?period=([0-9-]+))?"`)

// A figure you click is the figure you land on.
//
// The Overview's desk tiles show THIS month to date. The desk page opens on
// the last month that FINISHED, because that is the right default for a page
// somebody navigates to directly. Clicking a tile therefore moved the reader
// between periods without saying so: aws read 32206.09 on the tile and
// 62841.79 on the page, both correct, and a reader holding the first number
// in their head has no way to know the second answers a different question.
func TestClickingADeskTileKeepsThePeriod(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "boss", "boss-password-2026")

	_, body, _ := h.get(t, "/")
	i := strings.Index(body, "Each desk")
	if i < 0 {
		t.Fatal("no desk tiles on the overview")
	}
	tiles := body[i:]
	if j := strings.Index(tiles, "What moved"); j > 0 {
		tiles = tiles[:j]
	}
	found := 0
	for _, m := range deskHref.FindAllStringSubmatch(tiles, -1) {
		found++
		if m[3] == "" {
			t.Errorf("the tile for %s links to /desk/%s with no period, so it "+
				"opens on a different month from the figure on the tile",
				m[1], m[1])
		}
	}
	if found < 4 {
		t.Fatalf("only %d desk tiles found; the scan is broken", found)
	}
}

// And the period it carries is one the desk page honours, not a string it
// silently drops back to its own default.
func TestTheCarriedPeriodIsTheOneTheDeskShows(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "boss", "boss-password-2026")

	_, body, _ := h.get(t, "/")
	m := deskHref.FindStringSubmatch(body[strings.Index(body, "Each desk"):])
	if m == nil || m[3] == "" {
		t.Fatal("no desk tile carries a period")
	}
	desk, period := m[1], m[3]

	code, page, _ := h.get(t, "/desk/"+desk+"?period="+period)
	if code != 200 {
		t.Fatalf("/desk/%s?period=%s answered %d", desk, period, code)
	}
	// The page states which month it is showing, and it has to be that one.
	if !strings.Contains(page, period) {
		t.Errorf("/desk/%s?period=%s does not name %s anywhere: the period was "+
			"dropped and the page fell back to its own default", desk, period, period)
	}
	// And the cost it shows is the tile's, not the previous month's.
	tile := regexp.MustCompile(
		`href="/desk/` + desk + `\?period=` + period + `">[a-z]+</a></div>\s*<div class="v">([\d,.]+)</div>`).
		FindStringSubmatch(body)
	if tile == nil {
		t.Fatalf("could not read the tile figure for %s", desk)
	}
	if !strings.Contains(page, tile[1]) {
		t.Errorf("the tile for %s says %s and the page it opens does not carry "+
			"that figure: the click still moves the reader between periods",
			desk, tile[1])
	}
}

// A zero on the audit page means the plane is off, and the page says so by
// not printing the tile at all.
//
// "On the stack: 0 agent-events emitted" sat next to "The chain verified: 36
// events" on an installation with no stack configured. A reader comparing 36
// with 0 concludes the console is failing to emit, when the truth is that
// nobody switched the governance plane on. The template already guards the
// tile with StackOn; the flag was wired to s.rec, the store's own journal
// recorder, which is present on every installation.
func TestTheAuditPageDoesNotShowAStackZero(t *testing.T) {
	h := startWith(t, true) // no events path: the stack is off
	h.signUp(t, "boss", "boss-password-2026")

	_, body, _ := h.get(t, "/audit")
	if strings.Contains(body, "On the stack") {
		t.Error("the audit page shows the stack tile on an installation with " +
			"no agent-event stream, so it prints a zero that reads as a fault")
	}
	// And the chain figure, which IS about this installation, is still there.
	if !strings.Contains(body, "The chain") {
		t.Error("the chain tile went missing with it")
	}
}

// And with a stream configured it comes back, or the fix was to delete the
// tile rather than to guard it.
func TestTheAuditPageShowsTheStackWhenItIsOn(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/events.ndjson"
	h := startStream(t, path)
	h.signUp(t, "boss", "boss-password-2026")

	_, body, _ := h.get(t, "/audit")
	if !strings.Contains(body, "On the stack") {
		t.Error("with an agent-event stream configured the audit page does " +
			"not report what has been emitted")
	}
}

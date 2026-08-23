package web_test

import (
	"strings"
	"testing"
)

// Every desk an agent's card links to has to answer.
//
// "Desk" means two things in this console: a spend source (aws, gcp, ai) and
// the desk an analyst sits at. Twelve analysts sit at "management", which is
// not a spend source at all, so every card of theirs linked to /desk/management
// and got a bare 404. The owners page for whoever holds them carried twelve of
// those links on one screen.
//
// Found by crawling every link reachable by clicking from the front page,
// which is the only way it shows: the desks page does not link there, so
// nothing an operator browses top-down ever reaches it.
func TestEveryDeskAnAgentSitsAtAnswers(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "boss", "boss-password-2026")

	rows, err := h.st.DB().Query(
		`SELECT DISTINCT desk FROM analysts WHERE COALESCE(desk,'') != ''`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var desk string
		if err := rows.Scan(&desk); err != nil {
			t.Fatal(err)
		}
		seen++
		code, _, loc := h.get(t, "/desk/"+desk)
		switch {
		case code == 200:
		case code == 303 && loc != "":
			// A desk with no spend behind it may send the reader somewhere
			// that can answer, as long as it lands on a real page.
			if c, _, _ := h.get(t, loc); c != 200 {
				t.Errorf("/desk/%s redirects to %s, which answers %d", desk, loc, c)
			}
		default:
			t.Errorf("/desk/%s answers %d, and agents' cards link to it", desk, code)
		}
	}
	if seen < 4 {
		t.Fatalf("only %d desks in the fixture; the scan is broken", seen)
	}
}

// And the reader lands on the agents, which is what they clicked for.
func TestACrewDeskShowsWhoSitsAtIt(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "boss", "boss-password-2026")

	var n int
	if err := h.st.DB().QueryRow(
		`SELECT COUNT(*) FROM analysts WHERE desk='management'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Skip("no management desk in this fixture")
	}
	_, _, loc := h.get(t, "/desk/management")
	if loc == "" {
		t.Fatal("/desk/management does not redirect")
	}
	_, body, _ := h.get(t, loc)
	// The filtered list shows those agents and not the whole roster.
	links := strings.Count(body, `href="/staff/`)
	if links == 0 {
		t.Fatalf("%s lists no agents", loc)
	}
	var total int
	if err := h.st.DB().QueryRow(`SELECT COUNT(*) FROM analysts`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if links >= total {
		t.Errorf("%s lists %d agent links with %d on the whole roster: it is "+
			"not filtered to the desk", loc, links, total)
	}
}

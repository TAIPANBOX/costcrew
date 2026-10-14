package web_test

// Answering an option of a decision request: the three reasons the console
// gives for not answering one. A number that names no option, an option that
// is not on the request, and an option already answered each say so, and none
// of them changes a stamp.

import (
	"net/url"
	"strconv"
	"testing"
)

func TestCovOptionAnswersSayWhyAnOptionCannotBeAnswered(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	// An option on a deliverable that does not exist is no option at all.
	code, loc := h.post(t, "/option/999999/1/apply", url.Values{"csrf": {h.csrf(t, "/board")}})
	if code != 303 || !covHasMsg(loc, "no such option") {
		t.Errorf("an option on a missing deliverable: %d %s, want no such option", code, loc)
	}

	var art int
	if err := h.st.DB().QueryRow(`SELECT id FROM artifacts ORDER BY id LIMIT 1`).Scan(&art); err != nil {
		t.Fatalf("the seeded console has no deliverable to answer an option on: %v", err)
	}
	artID := strconv.Itoa(art)
	// The option path only names a decision request for a task somebody owns.
	if _, err := h.st.DB().Exec(`UPDATE tasks SET owner='owner' WHERE id=(SELECT task FROM artifacts WHERE id=?)`, art); err != nil {
		t.Fatal(err)
	}

	// A real deliverable with no option at that number.
	code, loc = h.post(t, "/option/"+artID+"/99/apply", url.Values{"csrf": {h.csrf(t, "/board")}})
	if code != 303 || !covHasMsg(loc, "no such option") {
		t.Errorf("an option number the deliverable does not carry: %d %s", code, loc)
	}

	// An option that has already been answered is not carried any more, and
	// answering it again is refused with the state it is in.
	if _, err := h.st.DB().Exec(`INSERT INTO artifact_options(artifact, ordinal, class, summary, state)
		VALUES (?, 77, 'forecast.project', 'answered already', 'refused')`, art); err != nil {
		t.Fatal(err)
	}
	code, loc = h.post(t, "/option/"+artID+"/77/apply", url.Values{"csrf": {h.csrf(t, "/board")}})
	if code != 303 || !covHasMsg(loc, "this option is refused, not carried") {
		t.Errorf("answering an option already refused: %d %s", code, loc)
	}
}

package web_test

import (
	"fmt"
	"strings"
	"testing"
)

// A reader can tell a real deliverable from a generated one.
//
// Red first: with no marker in the template, the page rendered a live draft
// and a seeded one identically, and the only difference between them lived in
// a column nothing displayed. A marker in the database that no page shows is
// not a marker.
func TestTheTaskPageShowsWhichDeliverableWasWrittenLive(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	db := h.st.DB()

	var task int
	if err := db.QueryRow(`SELECT id FROM tasks ORDER BY id LIMIT 1`).
		Scan(&task); err != nil {
		t.Fatal(err)
	}
	// Two drafts on one task: one the estate generated, one a model wrote.
	for _, src := range []string{"fixture", "live"} {
		if _, err := db.Exec(`INSERT INTO artifacts
			(task, author, title, body, state, created, source)
			VALUES (?,?,?,'body','draft',datetime('now'),?)`,
			task, "a."+src, src, src); err != nil {
			t.Fatal(err)
		}
	}

	code, body, _ := h.get(t, fmt.Sprintf("/task/%d", task))
	if code != 200 {
		t.Fatalf("task page: %d", code)
	}
	if n := strings.Count(body, "written live"); n != 1 {
		t.Errorf("the page marks %d deliverables as live, want exactly 1: "+
			"two drafts, one generated and one a model wrote, must not read "+
			"the same", n)
	}
}

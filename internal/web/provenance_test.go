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

// The crew page says how much of its figure is real money.
//
// Everything else on that page is generated: 3871.35 across 310 tasks, none of
// it anybody's money. A live run adds real charges to the same column, and one
// figure covering both kinds is the fault this console spends its time catching
// in other people's data. Invariant 16 carried this as its open item until now.
//
// Red first: with the template silent, the page showed 3871.59 and nothing said
// that 0.24 of it was real.
func TestTheCrewPageSaysWhatOfItsFigureIsReal(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	db := h.st.DB()

	// Two tasks an agent actually worked, 0.0531 and 0.0219 of a dollar.
	var ids []int
	rows, err := db.Query(`SELECT id FROM tasks ORDER BY id LIMIT 2`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	for i, id := range ids {
		if _, err := db.Exec(`UPDATE tasks SET live_micros = ? WHERE id = ?`,
			[]int64{53_100, 21_900}[i], id); err != nil {
			t.Fatal(err)
		}
	}

	code, body, _ := h.get(t, "/staff")
	if code != 200 {
		t.Fatalf("crew page: %d", code)
	}
	// 75000 micros rounds up to 8 cents.
	if !strings.Contains(body, "0.08 of it is real money") {
		t.Error("the crew page does not say how much of its figure is real: a " +
			"reader sees one number covering generated and live spend together, " +
			"which is the fault this console exists to catch elsewhere")
	}
	if !strings.Contains(body, "2 tasks an agent actually wrote") {
		t.Error("the page does not say how many tasks the real money was spent on")
	}
}

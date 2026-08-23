package web_test

import (
	"database/sql"
	"strings"
	"testing"
)

// The panel has to be filled for most of the estate, not merely present.
//
// This is the failure it was built for and the one nothing was watching: the
// card's event panels read the agent-event stream, which held ten lines naming
// five agents, so thirty-four of thirty-nine cards said "nothing yet" while
// the board behind them held three hundred tasks and two hundred and
// seventy-nine artifacts. A panel that renders and is always empty looks like
// a broken feature and reads as an agent that has never done anything.
func TestMostCardsShowWhereTheAgentStopped(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "owner", "owner-password-2026")

	names := analystNames(t, h.st.DB())
	if len(names) < 10 {
		t.Fatalf("only %d analysts in the fixture", len(names))
	}
	filled := 0
	for _, n := range names {
		_, body, _ := h.get(t, "/staff/"+n)
		if !strings.Contains(body, "Where it stopped") {
			t.Fatalf("the card for %s has no stops panel at all", n)
		}
		if strings.Contains(body, "sent back and") {
			filled++
		}
	}
	// Two thirds is a floor with room in it, not a pin to today's fixture.
	if filled*3 < len(names)*2 {
		t.Errorf("only %d of %d cards show a stop; the panel is reading "+
			"something that is nearly always empty", filled, len(names))
	}
	t.Logf("%d of %d cards show at least one stop", filled, len(names))
}

// The panel's figures must equal the board's.
//
// A panel derived from the board that disagrees with the board is worse than
// no panel: both numbers look authoritative and only one is right.
func TestTheStopsPanelAgreesWithTheBoard(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "owner", "owner-password-2026")
	db := h.st.DB()

	name := anAgentWithStops(t, db)
	var returned, blocked int
	var spent int64
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM artifacts WHERE author=? AND state='returned'`,
		name).Scan(&returned); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM tasks WHERE assignee=? AND state='blocked'`,
		name).Scan(&blocked); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COALESCE(SUM(x),0) FROM (
			SELECT t.spent_cents x FROM artifacts a JOIN tasks t ON t.id=a.task
			 WHERE a.author=? AND a.state='returned'
			UNION ALL
			SELECT spent_cents FROM tasks WHERE assignee=? AND state='blocked')`,
		name, name).Scan(&spent); err != nil {
		t.Fatal(err)
	}

	_, body, _ := h.get(t, "/staff/"+name)
	for _, want := range []string{
		itoa(returned) + " artifact", itoa(blocked) + " task",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the card for %s does not say %q; the board does", name, want)
		}
	}
	if spent > 0 && !strings.Contains(body, centsString(spent)) {
		t.Errorf("the card for %s does not carry %s, which is what the stopped "+
			"work had cost", name, centsString(spent))
	}
}

// It reads the board, not the stream.
//
// This is the design decision the panel rests on and it is invisible from the
// outside: the harness writes no agent-event stream, so if the panel were
// reading one it would be empty here and full in production, which is the
// worst way round for a bug to be.
func TestTheStopsPanelDoesNotNeedTheEventStream(t *testing.T) {
	h := startWith(t, true) // no events path: the stream does not exist
	h.signUp(t, "owner", "owner-password-2026")

	name := anAgentWithStops(t, h.st.DB())
	_, body, _ := h.get(t, "/staff/"+name)
	if !strings.Contains(body, "sent back and") {
		t.Error("with no event stream at all the stops panel is empty, so it " +
			"is reading the stream rather than the board")
	}
	// And the stream panel is honestly empty, rather than borrowing the board's
	// rows and claiming the stack can see them.
	if !strings.Contains(body, "Nothing in the stream names this agent yet") {
		t.Error("the events panel does not say the stream is empty, so a reader " +
			"cannot tell which record each panel is showing")
	}
}

// A stop with no reason is named as such.
//
// Blank there reads as "no problem", and the whole value of the panel is that
// somebody wrote down why. An empty cell hides the one case worth chasing.
func TestAStopWithNoReasonSaysSo(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "owner", "owner-password-2026")
	db := h.st.DB()

	name := anAgentWithStops(t, db)
	if _, err := db.Exec(
		`UPDATE tasks SET reason='' WHERE assignee=? AND state='blocked'`,
		name); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE artifacts SET reason='' WHERE author=? AND state='returned'`,
		name); err != nil {
		t.Fatal(err)
	}
	_, body, _ := h.get(t, "/staff/"+name)
	if !strings.Contains(body, "no reason was recorded") {
		t.Error("a stop with no reason shows an empty cell, which reads as " +
			"nothing having gone wrong")
	}
}

func analystNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM analysts ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func anAgentWithStops(t *testing.T, db *sql.DB) string {
	t.Helper()
	var name string
	err := db.QueryRow(`
		SELECT a.author FROM artifacts a
		 WHERE a.state='returned'
		   AND EXISTS (SELECT 1 FROM tasks t WHERE t.assignee=a.author AND t.state='blocked')
		 LIMIT 1`).Scan(&name)
	if err != nil || name == "" {
		t.Skipf("no agent in this fixture has both a return and a block: %v", err)
	}
	return name
}

func centsString(v int64) string {
	neg := ""
	if v < 0 {
		neg, v = "-", -v
	}
	return neg + itoa(int(v/100)) + "." + pad2s(int(v%100))
}

func pad2s(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

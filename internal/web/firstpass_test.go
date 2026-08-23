package web_test

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// One label, one number, and compared BEFORE it is rounded.
//
// "First pass" appears on the crew page and on the results page, computed in
// two places. They agree today because both count TASK states, which was
// worth establishing rather than assuming: the comment about counting
// DELIVERABLES sits a few lines above in the same function and governs a
// different figure, the one about work waiting for a signature. Reading it as
// if it governed these two is how I came to believe they used two sources.
//
// What this pins is that they stay one number. Nothing else did: they are
// summed from different code, and a change to either side would not fail
// anything. Planting one extra return per analyst in Scoreboards is caught
// here and nowhere else.
//
// It compares the COMPUTED values. The rendered ones round to whole percent,
// and 232/265 and 246/279 both print 88%, so a comparison of what is on the
// screen would let the two drift a long way before it noticed.
func TestFirstPassIsOneNumberFromOneSource(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "boss", "boss-password-2026")
	db := h.st.DB()

	same := func(when string) {
		t.Helper()
		res, err := finops.Compute(db, world.LastDay[:7])
		if err != nil {
			t.Fatal(err)
		}
		want, ok := res.FirstPass()
		if !ok {
			t.Fatalf("%s: the results page has no first-pass rate at all", when)
		}
		boards, err := crew.Scoreboards(db)
		if err != nil {
			t.Fatal(err)
		}
		var posted, returned int
		for _, b := range boards {
			posted += b.Posted
			returned += b.Returned
		}
		if posted+returned == 0 {
			t.Fatalf("%s: the crew page judges nothing", when)
		}
		got := float64(posted) / float64(posted+returned) * 100
		if diff := got - want; diff > 0.0001 || diff < -0.0001 {
			t.Errorf("%s: the crew page computes %.4f%% and the results page "+
				"%.4f%%. One label, two tables, and the rendered figures round "+
				"to the same number until the gap is wide", when, got, want)
		}
	}

	same("as seeded")

	// The move a reviewer makes every day: stamp deliverables whose tasks are
	// not closed. This is what pulled the two apart.
	res, err := db.Exec(`UPDATE artifacts SET state='posted'
		 WHERE state='draft'
		   AND task IN (SELECT id FROM tasks WHERE state IN ('active','done'))`)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		t.Skip("no draft on an open task here, so the drift cannot be produced")
	}
	t.Logf("stamped %d deliverables whose tasks are not closed", n)
	same("after stamping them")

	// And the other direction, which is where the gap gets visible.
	if _, err := db.Exec(`UPDATE artifacts SET state='returned'
		 WHERE state='posted'
		   AND task IN (SELECT id FROM tasks WHERE state='active')`); err != nil {
		t.Fatal(err)
	}
	same("after sending them back instead")
}

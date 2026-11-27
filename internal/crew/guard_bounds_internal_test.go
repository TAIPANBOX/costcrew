package crew

// CheckGuards at its edges: an analyst exactly at its guard and one with no
// guard at all are not past it, nothing is checked without a recorder, and
// a recorder that refuses is reported rather than passed over.

import (
	"errors"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

type refusingRecorder struct{ calls int }

func (r *refusingRecorder) Emit(string, string, string, map[string]any, []string) error {
	r.calls++
	return errors.New("the bus is full")
}

func TestCheckGuardsAtTheEdges(t *testing.T) {
	db := ownershipDB(t)
	const month = "2026-08"
	mustExec(t, db, `INSERT INTO sprints(id, label, start, state) VALUES (1, 'seeded', ?, 'closed')`, month+"-01")
	seedAnalystWithSpend(t, db, "exactly-at", money.Cents(100), money.Cents(100))
	seedAnalystWithSpend(t, db, "no-guard", money.Cents(0), money.Cents(5_000))
	seedAnalystWithSpend(t, db, "one-cent-over", money.Cents(100), money.Cents(101))

	if past, by, err := CheckGuards(db, month, nil); past != 0 || by != 0 || err != nil {
		t.Errorf("with no recorder: %d %s %v, want nothing checked", past, by, err)
	}

	rec := &capturingRecorder{}
	past, by, err := CheckGuards(db, month, rec)
	if err != nil {
		t.Fatal(err)
	}
	if past != 1 || by != 1 {
		t.Errorf("past %d by %s, want only one-cent-over, by one cent", past, by)
	}
	if len(rec.events) != 1 || rec.events[0].actor != "one-cent-over" {
		t.Errorf("events %+v, want only one-cent-over's", rec.events)
	}

	ref := &refusingRecorder{}
	if _, _, err := CheckGuards(db, month, ref); err == nil || err.Error() != "the bus is full" {
		t.Errorf("a refusing recorder: %v, want its error", err)
	}
}

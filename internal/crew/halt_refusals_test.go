package crew_test

// The halt's refusals: a halt that names no desk or gives no reason, and a
// lift with no reason or on a desk that is not halted. None of them suspends
// or reactivates anybody.

import (
	"errors"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

func TestHaltAndLiftRefusals(t *testing.T) {
	db, _ := withRoster(t)
	countActive := func() int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM analysts WHERE desk='aws' AND state='active'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := countActive()

	if _, _, err := crew.ApplyHalt(db, "  ", "the feed is stale", "a-person", "owner", "2026-08-15", nil); err == nil ||
		!strings.Contains(err.Error(), "names no desk") {
		t.Errorf("no desk: %v", err)
	}
	if _, _, err := crew.ApplyHalt(db, "aws", " ", "a-person", "owner", "2026-08-15", nil); !errors.Is(err, crew.ErrNeedReason) {
		t.Errorf("no reason: %v, want ErrNeedReason", err)
	}
	if got := countActive(); got != before {
		t.Fatalf("a refused halt suspended %d analysts on aws", before-got)
	}

	if _, err := crew.LiftHalt(db, "aws", "fixed", "a-person", nil); err == nil ||
		!strings.Contains(err.Error(), "aws is not halted") {
		t.Errorf("lifting a desk that is not halted: %v", err)
	}
	suspended, _, err := crew.ApplyHalt(db, "aws", "the feed is stale", "a-person", "owner", "2026-08-15", nil)
	if err != nil || len(suspended) != before {
		t.Fatalf("halt: %v suspended %d, want %d", err, len(suspended), before)
	}
	if _, err := crew.LiftHalt(db, "aws", "", "a-person", nil); !errors.Is(err, crew.ErrNeedReason) {
		t.Errorf("a lift with no reason: %v, want ErrNeedReason", err)
	}
	if got := countActive(); got != 0 {
		t.Errorf("a refused lift reactivated %d analysts", got)
	}
}

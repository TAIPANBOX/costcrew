package crew

import (
	"database/sql"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// captured is one Emit call, kept so a test can read what went to the bus.
type captured struct {
	kind, actor, severity string
}

type capturingRecorder struct{ events []captured }

func (c *capturingRecorder) Emit(kind, actor, severity string, _ map[string]any, _ []string) error {
	c.events = append(c.events, captured{kind, actor, severity})
	return nil
}

// CheckGuards had no test at all, which is how it emitted an invalid severity
// from the day it was written and nothing anywhere said so.
//
// The severity it chose in the COMMON band, an analyst past its guard but under
// one and a half times it, was "warning". The shared envelope's severity is a
// closed enum (agent-passport SPEC 6.1: info, low, medium, high, critical) and
// "warning" is not in it, so every one of those events was a line any
// schema-validating consumer refuses whole.
//
// This pins all three bands AND their membership of that enum, so neither the
// banding nor the spelling can drift back on its own.
func TestEveryGuardBandEmitsASeverityTheEnvelopeAllows(t *testing.T) {
	allowed := map[string]bool{
		"info": true, "low": true, "medium": true, "high": true, "critical": true,
	}
	db := ownershipDB(t)
	const guardMonth = "2026-08"
	mustExec(t, db, `INSERT INTO sprints(id, label, start, state) VALUES (1, 'seeded', ?, 'closed')`, guardMonth+"-01")
	// Three analysts, one per band, against a guard of one pound.
	guard := money.Cents(100)
	bands := []struct {
		name  string
		spent money.Cents
		want  string
	}{
		{"barely-over", 101, "low"},   // over, under 1.5x
		{"half-again", 150, "medium"}, // at 1.5x
		{"twice", 200, "high"},        // at 2x
	}
	for _, b := range bands {
		seedAnalystWithSpend(t, db, b.name, guard, b.spent)
	}

	rec := &capturingRecorder{}
	past, _, err := CheckGuards(db, guardMonth, rec)
	if err != nil {
		t.Fatalf("CheckGuards: %v", err)
	}
	if past != len(bands) {
		t.Fatalf("%d analyst(s) past their guard, want %d", past, len(bands))
	}

	got := map[string]string{}
	for _, e := range rec.events {
		if e.kind != "guard_passed" {
			t.Fatalf("an unexpected kind reached the bus: %q", e.kind)
		}
		if !allowed[e.severity] {
			t.Fatalf("%s went out with severity %q, which the shared envelope's "+
				"closed enum does not carry; a consumer that validates refuses "+
				"the whole line", e.actor, e.severity)
		}
		got[e.actor] = e.severity
	}
	for _, b := range bands {
		if got[b.name] != b.want {
			t.Errorf("%s (spent %s against a guard of %s): severity %q, want %q",
				b.name, b.spent, guard, got[b.name], b.want)
		}
	}
}

// seedAnalystWithSpend puts one analyst on the roster with a monthly guard, and
// one task charged against it inside the seeded sprint.
func seedAnalystWithSpend(t *testing.T, db *sql.DB, name string, guard, spent money.Cents) {
	t.Helper()
	mustExec(t, db,
		`INSERT INTO analysts(name, state, monthly_cents, desk, owner)
		 VALUES (?, 'active', ?, 'ai', 'y.mercer')`, name, int64(guard))
	mustExec(t, db,
		`INSERT INTO tasks(sprint, assignee, spent_cents, title, state)
		 VALUES (1, ?, ?, 'seeded', 'done')`, name, int64(spent))
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

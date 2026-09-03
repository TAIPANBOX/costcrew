package crew_test

// B5-SPEC.md section 2 (the switch, in the store) and section 3 point 2 (the
// SAME function the plan uses, exported so the page, the plan and the runner
// cannot disagree about what is due).
//
// Red first, against main: crew.CadenceDue, crew.CadenceSettings and
// crew.SetCadence do not exist yet, so this file does not compile. That is
// the only kind of red a brand-new export can produce, the same shape
// gateway_test.go's own header already documents in tools/run.

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// CadenceDue is the SAME source Propose already has (source 3): exporting it
// must not change what a due analyst looks like.
func TestCadenceDueMatchesWhatProposeAlreadyProducesForCadence(t *testing.T) {
	db := planDB(t)
	hire(t, db, "weekly-writer", "aws", "openrouter", []string{"variance-commentary"}, money.Cents(1000), money.Cents(10000))
	if _, err := db.Exec(`UPDATE analysts SET cadence='weekly' WHERE name='weekly-writer'`); err != nil {
		t.Fatal(err)
	}
	tres, err := db.Exec(`INSERT INTO tasks (title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated)
		VALUES ('t', 'g', 'weekly-writer', 'aws', 'posted', 0, 0, datetime('now'), datetime('now'))`)
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := tres.LastInsertId()
	if _, err := db.Exec(`INSERT INTO artifacts (task, author, title, body, state, created, stamped, stamper)
		VALUES (?, 'weekly-writer', 'a', 'b', 'posted', '2026-08-31T00:00:00Z', '2026-08-31T00:00:00Z', 'owner')`, tid); err != nil {
		t.Fatal(err)
	}

	roster, err := crew.Roster(db)
	if err != nil {
		t.Fatal(err)
	}
	spent, err := crew.SpendInMonth(db, "2026-09")
	if err != nil {
		t.Fatal(err)
	}

	items, err := crew.CadenceDue(db, roster, "2026-09-08", spent)
	if err != nil {
		t.Fatal(err)
	}
	got := itemsWhy(items, "weekly cadence")
	if len(got) != 1 {
		t.Fatalf("CadenceDue directly: weekly-cadence items = %d, want 1 (%v)", len(got), items)
	}
	if got[0].Assignee != "weekly-writer" {
		t.Errorf("assignee = %q, want weekly-writer", got[0].Assignee)
	}

	// And Propose, which is the page's own path, must report the identical
	// item: the plan and a direct CadenceDue call must never disagree about
	// what is due.
	p, err := crew.Propose(db, "2026-W99", "2026-09-08", "2026-09-14", "")
	if err != nil {
		t.Fatal(err)
	}
	viaPropose := itemsWhy(p.Items, "weekly cadence")
	if len(viaPropose) != 1 || viaPropose[0].Assignee != got[0].Assignee || viaPropose[0].Why != got[0].Why {
		t.Errorf("Propose's own cadence item disagrees with CadenceDue's: %+v vs %+v", viaPropose, got)
	}
}

// An on-request analyst is never in the list, direct or through Propose.
func TestCadenceDueNeverListsAnOnRequestAnalyst(t *testing.T) {
	db := planDB(t)
	hire(t, db, "never-due", "aws", "openrouter", []string{"variance-commentary"}, money.Cents(1000), money.Cents(10000))
	roster, err := crew.Roster(db)
	if err != nil {
		t.Fatal(err)
	}
	items, err := crew.CadenceDue(db, roster, "2026-09-08", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Assignee == "never-due" {
			t.Errorf("on-request analyst never-due appeared in the due list: %+v", it)
		}
	}
}

// ------------------------------------------------------------- the switch

// The default is off, and a ceiling of zero, which is off by another name:
// nothing about a fresh store's absence of a settings row must read as "on".
func TestCadenceSettingsDefaultsToOff(t *testing.T) {
	db := planDB(t)
	enabled, ceiling, by, at, err := crew.CadenceSettings(db)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Error("a fresh store reads cadence.enabled as on; the default must be off")
	}
	if ceiling != 0 {
		t.Errorf("ceiling = %s, want 0 on a fresh store", ceiling)
	}
	if by != "" || at != "" {
		t.Errorf("changed_by=%q changed_at=%q on a fresh store, want both empty", by, at)
	}
}

// SetCadence and CadenceSettings round-trip, and the actor is recorded.
func TestSetCadenceThenCadenceSettingsRoundTrips(t *testing.T) {
	db := planDB(t)
	if err := crew.SetCadence(db, true, money.Cents(2500), "yurii"); err != nil {
		t.Fatal(err)
	}
	enabled, ceiling, by, at, err := crew.CadenceSettings(db)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Error("enabled = false after SetCadence(true, ...)")
	}
	if ceiling != money.Cents(2500) {
		t.Errorf("ceiling = %s, want 25.00", ceiling)
	}
	if by != "yurii" {
		t.Errorf("changed_by = %q, want yurii", by)
	}
	if strings.TrimSpace(at) == "" {
		t.Error("changed_at is empty after a change: nobody could say when this was switched on")
	}

	// And flipping it off is recorded too, with a new actor.
	if err := crew.SetCadence(db, false, 0, "second-operator"); err != nil {
		t.Fatal(err)
	}
	enabled2, ceiling2, by2, _, err := crew.CadenceSettings(db)
	if err != nil {
		t.Fatal(err)
	}
	if enabled2 {
		t.Error("enabled = true after SetCadence(false, ...)")
	}
	if ceiling2 != 0 {
		t.Errorf("ceiling after switching off = %s, want 0", ceiling2)
	}
	if by2 != "second-operator" {
		t.Errorf("changed_by = %q, want second-operator", by2)
	}
}

// Hostile: a ceiling of -1 is refused, and refused BEFORE anything is
// written, not merely clamped to zero on the way in.
func TestSetCadenceRefusesANegativeCeiling(t *testing.T) {
	db := planDB(t)
	// A baseline so a bad write's non-effect can be told apart from a fresh
	// store's own default.
	if err := crew.SetCadence(db, true, money.Cents(100), "first"); err != nil {
		t.Fatal(err)
	}
	err := crew.SetCadence(db, true, money.Cents(-1), "attacker")
	if err == nil {
		t.Fatal("SetCadence accepted a ceiling of -1")
	}
	enabled, ceiling, by, _, rerr := crew.CadenceSettings(db)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if ceiling != money.Cents(100) || by != "first" || !enabled {
		t.Errorf("a refused negative ceiling changed the stored settings: enabled=%v ceiling=%s by=%q",
			enabled, ceiling, by)
	}
}

// ---------------------------------------------------------- C9: desk halts

// CadenceDue skips a halted desk's cadence work and says why (C9-SPEC.md
// section 2). This is the mutant scripts/gates-have-teeth.sh plants: "skip
// the -due check for a halted desk".
//
// ApplyHalt already suspends every active analyst it finds on the desk, and
// CadenceDue's own "state != active" rule silently skips a suspended one on
// its own -- so this hires a SECOND analyst directly onto the already-halted
// desk, active, to prove the halt check itself fires rather than merely
// happening to agree with the ordinary suspension rule.
func TestCadenceDueSkipsAHaltedDeskAndSaysWhy(t *testing.T) {
	db := planDB(t)
	hire(t, db, "daily-writer", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(1000), money.Cents(10000))
	if _, err := db.Exec(`UPDATE analysts SET cadence='daily' WHERE name='daily-writer'`); err != nil {
		t.Fatal(err)
	}
	rec := &spyRecorder{}
	if _, _, err := crew.ApplyHalt(db, "aws", "tagging feed stale for 5 days", "data-quality", "yurii", "2026-09-01", rec); err != nil {
		t.Fatal(err)
	}

	// Hired AFTER the halt, so it is genuinely active on an already-halted
	// desk: Hire itself does not check for a halt.
	hire(t, db, "late-hire", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(1000), money.Cents(10000))
	if _, err := db.Exec(`UPDATE analysts SET cadence='daily' WHERE name='late-hire'`); err != nil {
		t.Fatal(err)
	}

	roster, err := crew.Roster(db)
	if err != nil {
		t.Fatal(err)
	}
	items, err := crew.CadenceDue(db, roster, "2026-09-05", nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, it := range items {
		if it.Assignee == "late-hire" {
			t.Errorf("CadenceDue proposed daily cadence work for late-hire on the HALTED "+
				"aws desk: %+v", it)
		}
	}
	var sawWhy bool
	for _, it := range items {
		if strings.Contains(it.Why, "aws") && strings.Contains(it.Why, "halt") {
			sawWhy = true
		}
	}
	if !sawWhy {
		t.Errorf("CadenceDue skipped the halted aws desk with no explanation naming it in "+
			"Why: %+v", items)
	}
}

// The SAME skip reaches Propose, since proposeCadenceDue wraps CadenceDue:
// C9-SPEC.md section 2, "-due and Propose skip a halted desk".
func TestProposeSkipsAHaltedDeskThroughCadenceDue(t *testing.T) {
	db := planDB(t)
	hire(t, db, "daily-writer", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(1000), money.Cents(10000))
	if _, err := db.Exec(`UPDATE analysts SET cadence='daily' WHERE name='daily-writer'`); err != nil {
		t.Fatal(err)
	}
	rec := &spyRecorder{}
	if _, _, err := crew.ApplyHalt(db, "aws", "tagging feed stale", "data-quality", "yurii", "2026-09-01", rec); err != nil {
		t.Fatal(err)
	}
	hire(t, db, "late-hire", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(1000), money.Cents(10000))
	if _, err := db.Exec(`UPDATE analysts SET cadence='daily' WHERE name='late-hire'`); err != nil {
		t.Fatal(err)
	}

	p, err := crew.Propose(db, "2026-W99", "2026-09-05", "2026-09-11", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range p.Items {
		if it.Assignee == "late-hire" {
			t.Errorf("Propose proposed work for late-hire on the halted aws desk: %+v", it)
		}
	}
}

// Hostile: a settings row holding garbage reads as off, the safe direction,
// rather than panicking or being treated as a number nobody wrote.
func TestCadenceSettingsOnGarbageReadsAsOff(t *testing.T) {
	db := planDB(t)
	// ensureSettings is unexported; SetCadence(false,...) is what creates the
	// table honestly, then this overwrites it with garbage a person never
	// typed through the console (a hand-edited database, or a migration gone
	// wrong).
	if err := crew.SetCadence(db, false, 0, "nobody"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE settings SET value='banana' WHERE key='cadence.enabled'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE settings SET value='not-a-number' WHERE key='cadence.ceiling_cents'`); err != nil {
		t.Fatal(err)
	}
	enabled, ceiling, _, _, err := crew.CadenceSettings(db)
	if err != nil {
		t.Fatalf("a garbage row must read as off, not error the whole page: %v", err)
	}
	if enabled {
		t.Error("cadence.enabled='banana' read as on: garbage must fail closed")
	}
	if ceiling != 0 {
		t.Errorf("cadence.ceiling_cents='not-a-number' read as %s, want 0", ceiling)
	}
}

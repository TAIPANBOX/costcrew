package crew_test

// C9-SPEC.md section 2, "data.halt applied": the desk-level suspension a
// data-quality finding can put on a desk's whole crew, and a person's lift
// that takes it off again.
//
// Red first, against main: crew.DeskHalt, crew.ApplyHalt, crew.LiftHalt,
// crew.ActiveHalt and crew.Halts do not exist yet, so this file does not
// compile -- the same shape cadence_test.go's own header already documents
// for a feature built from nothing.

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// spyRecorder is crew_test's own Recorder double: guard_internal_test.go's
// capturingRecorder is unexported and lives in the internal (package crew)
// test binary, invisible from here.
type spyRecorder struct {
	events []spyEvent
}

type spyEvent struct {
	kind, actor, severity string
	data                  map[string]any
}

func (r *spyRecorder) Emit(kind, actor, severity string, data map[string]any, _ []string) error {
	r.events = append(r.events, spyEvent{kind, actor, severity, data})
	return nil
}

func (r *spyRecorder) count(kind string) int {
	n := 0
	for _, e := range r.events {
		if e.kind == kind {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------- red first

func TestApplyHaltSuspendsEveryActiveAnalystOnTheDeskWithTheReason(t *testing.T) {
	db := planDB(t)
	hire(t, db, "triage-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(1000), money.Cents(10000))
	hire(t, db, "investigator-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(1000), money.Cents(10000))
	hire(t, db, "triage-gcp", "gcp", "openrouter", []string{"anomaly-triage"}, money.Cents(1000), money.Cents(10000))
	rec := &spyRecorder{}

	reason := "tagging feed from the aws desk has been stale for 5 days"
	suspended, already, err := crew.ApplyHalt(db, "aws", reason, "data-quality", "yurii", "2026-09-01", rec)
	if err != nil {
		t.Fatalf("ApplyHalt: %v", err)
	}
	if already {
		t.Error("already = true on the FIRST halt of this desk")
	}
	if len(suspended) != 2 {
		t.Fatalf("suspended = %v, want both aws analysts", suspended)
	}

	for _, name := range []string{"triage-aws", "investigator-aws"} {
		a, err := crew.GetAnalyst(db, name)
		if err != nil {
			t.Fatal(err)
		}
		if a.State != "suspended" {
			t.Errorf("%s.State = %q, want suspended", name, a.State)
		}
		if a.Reason != reason {
			t.Errorf("%s.Reason = %q, want %q", name, a.Reason, reason)
		}
	}
	// The gcp analyst is untouched: a halt on aws must not suspend a
	// different desk's crew.
	if a, err := crew.GetAnalyst(db, "triage-gcp"); err != nil || a.State != "active" {
		t.Errorf("triage-gcp.State = %v (err %v), want active: a halt on aws touched gcp", a.State, err)
	}

	if got := rec.count("agent_state_changed"); got != 2 {
		t.Errorf("agent_state_changed events = %d, want 2 (one per suspended analyst): %+v", got, rec.events)
	}

	h, found, err := crew.ActiveHalt(db, "aws")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("ActiveHalt(aws) found nothing after ApplyHalt")
	}
	if h.Reason != reason || h.Started != "2026-09-01" || h.Owner != "yurii" || h.AppliedBy != "data-quality" {
		t.Errorf("halt = %+v, want reason=%q started=2026-09-01 owner=yurii applied_by=data-quality", h, reason)
	}
}

// Boundary: a desk with no analysts at all still records the halt.
func TestApplyHaltOnADeskWithNoAnalystsStillRecordsTheHalt(t *testing.T) {
	db := planDB(t)
	rec := &spyRecorder{}
	suspended, already, err := crew.ApplyHalt(db, "saas", "no charge on record", "data-quality", "yurii", "2026-09-01", rec)
	if err != nil {
		t.Fatalf("ApplyHalt on an empty desk: %v", err)
	}
	if already {
		t.Error("already = true on the first halt")
	}
	if len(suspended) != 0 {
		t.Errorf("suspended = %v, want none: this desk has no analysts", suspended)
	}
	if got := rec.count("agent_state_changed"); got != 0 {
		t.Errorf("agent_state_changed events = %d, want 0", got)
	}
	if _, found, err := crew.ActiveHalt(db, "saas"); err != nil || !found {
		t.Errorf("ActiveHalt(saas) found=%v err=%v, want the halt recorded regardless", found, err)
	}
}

// Boundary: a second halt on an already-halted desk does not double-suspend,
// and does not move the started date.
func TestASecondHaltOnAHaltedDeskDoesNotDoubleSuspend(t *testing.T) {
	db := planDB(t)
	hire(t, db, "triage-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(1000), money.Cents(10000))
	rec := &spyRecorder{}

	if _, _, err := crew.ApplyHalt(db, "aws", "first report", "data-quality", "yurii", "2026-09-01", rec); err != nil {
		t.Fatal(err)
	}
	firstEvents := len(rec.events)

	suspended, already, err := crew.ApplyHalt(db, "aws", "second report, worse", "data-quality", "yurii", "2026-09-05", rec)
	if err != nil {
		t.Fatalf("ApplyHalt a second time: %v", err)
	}
	if !already {
		t.Error("already = false on the SECOND halt of an already-halted desk")
	}
	if len(suspended) != 0 {
		t.Errorf("the second ApplyHalt suspended %v; nothing should be touched twice", suspended)
	}
	if len(rec.events) != firstEvents {
		t.Errorf("the second ApplyHalt journaled %d more event(s); a desk already halted must journal nothing new",
			len(rec.events)-firstEvents)
	}

	h, found, err := crew.ActiveHalt(db, "aws")
	if err != nil || !found {
		t.Fatalf("ActiveHalt(aws) after a second halt: found=%v err=%v", found, err)
	}
	if h.Started != "2026-09-01" {
		t.Errorf("started = %q after a second halt, want 2026-09-01 (the FIRST day this desk stopped): "+
			"T.stale_days is measured from when the crew actually stopped, not from whichever "+
			"day happened to re-report the same still-open problem", h.Started)
	}
	if h.Reason != "first report" {
		t.Errorf("reason = %q after a second halt, want the first report kept: a no-op must change nothing", h.Reason)
	}
}

// ---------------------------------------------------------------- lifting

func TestLiftHaltReturnsTheAnalystsToActiveWithTheReasonJournaled(t *testing.T) {
	db := planDB(t)
	hire(t, db, "triage-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(1000), money.Cents(10000))
	hire(t, db, "investigator-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(1000), money.Cents(10000))
	rec := &spyRecorder{}
	if _, _, err := crew.ApplyHalt(db, "aws", "stale feed", "data-quality", "yurii", "2026-09-01", rec); err != nil {
		t.Fatal(err)
	}
	before := len(rec.events)

	liftReason := "the tagging feed is current again, confirmed on the connector page"
	reactivated, err := crew.LiftHalt(db, "aws", liftReason, "yurii", rec)
	if err != nil {
		t.Fatalf("LiftHalt: %v", err)
	}
	if len(reactivated) != 2 {
		t.Fatalf("reactivated = %v, want both aws analysts", reactivated)
	}
	for _, name := range []string{"triage-aws", "investigator-aws"} {
		a, err := crew.GetAnalyst(db, name)
		if err != nil {
			t.Fatal(err)
		}
		if a.State != "active" {
			t.Errorf("%s.State = %q after lifting, want active", name, a.State)
		}
	}
	if got := len(rec.events) - before; got != 2 {
		t.Errorf("lifting journaled %d event(s), want 2 (one per reactivated analyst)", got)
	}
	var sawReason bool
	for _, e := range rec.events[before:] {
		if e.kind != "agent_state_changed" {
			t.Errorf("lift journaled a %q event; C9-SPEC.md section 3 says no new wire type, "+
				"agent_state_changed already carries a suspension AND its lift", e.kind)
		}
		if r, _ := e.data["reason"].(string); r == liftReason {
			sawReason = true
		}
	}
	if !sawReason {
		t.Errorf("no lift event carried the reason %q: %+v", liftReason, rec.events[before:])
	}

	if _, found, err := crew.ActiveHalt(db, "aws"); err != nil || found {
		t.Errorf("ActiveHalt(aws) after lifting: found=%v err=%v, want the halt gone", found, err)
	}
}

// Mutant this catches: lift without a reason. A reason-less lift is
// indistinguishable from nobody having checked, the same argument every
// other reversal in this console already makes (crew.Return, MarkOptionRefused).
func TestLiftHaltRefusesWithNoReason(t *testing.T) {
	db := planDB(t)
	hire(t, db, "triage-aws", "aws", "openrouter", []string{"anomaly-triage"}, money.Cents(1000), money.Cents(10000))
	rec := &spyRecorder{}
	if _, _, err := crew.ApplyHalt(db, "aws", "stale feed", "data-quality", "yurii", "2026-09-01", rec); err != nil {
		t.Fatal(err)
	}
	before := len(rec.events)

	if _, err := crew.LiftHalt(db, "aws", "", "yurii", rec); err == nil {
		t.Fatal("LiftHalt accepted an empty reason")
	}
	if a, err := crew.GetAnalyst(db, "triage-aws"); err != nil || a.State != "suspended" {
		t.Errorf("triage-aws.State = %v (err %v) after a refused lift, want still suspended", a.State, err)
	}
	if _, found, err := crew.ActiveHalt(db, "aws"); err != nil || !found {
		t.Errorf("ActiveHalt(aws) after a refused lift: found=%v err=%v, want the halt still there", found, err)
	}
	if len(rec.events) != before {
		t.Errorf("a refused lift journaled %d event(s); it must journal nothing", len(rec.events)-before)
	}
}

// LiftHalt on a desk that was never halted is refused, not a silent no-op.
func TestLiftHaltOnADeskNotHaltedIsRefused(t *testing.T) {
	db := planDB(t)
	if _, err := crew.LiftHalt(db, "aws", "a reason", "yurii", nil); err == nil {
		t.Fatal("LiftHalt on a desk that was never halted was accepted")
	}
}

// ------------------------------------------------------------------ Halts()

func TestHaltsListsEveryHaltedDesk(t *testing.T) {
	db := planDB(t)
	rec := &spyRecorder{}
	if _, _, err := crew.ApplyHalt(db, "aws", "r1", "data-quality", "yurii", "2026-09-01", rec); err != nil {
		t.Fatal(err)
	}
	if _, _, err := crew.ApplyHalt(db, "gcp", "r2", "data-quality", "yurii", "2026-09-02", rec); err != nil {
		t.Fatal(err)
	}
	halts, err := crew.Halts(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(halts) != 2 {
		t.Fatalf("Halts() = %v, want exactly 2", halts)
	}
	seen := map[string]bool{}
	for _, h := range halts {
		seen[h.Desk] = true
	}
	if !seen["aws"] || !seen["gcp"] {
		t.Errorf("Halts() = %+v, want both aws and gcp", halts)
	}
}

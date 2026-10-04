package crew_test

// A person's answer to a carried option and who it was given for: the reason
// an admin must give for answering an owner's request, the marks the option
// and the journal carry, and the migration for an installation from before.

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// Red first (by compile: crew.ValidBehalfReason does not exist before this
// change). Required, capped, plain text; markup is allowed and stored as is.
func TestBehalfReasonIsRequiredCappedAndPlain(t *testing.T) {
	good := map[string]string{
		"a sentence":                "owner1 is on leave until the 20th",
		"surrounding space trimmed": "  owner1 is on leave  ",
		"a newline and a tab":       "owner1 is on leave.\n\tBack on the 20th.",
		"markup is text":            `<b>on leave</b> & "away"`,
		"non-latin text":            "власник у відпустці",
		"exactly the cap":           strings.Repeat("a", crew.BehalfReasonMaxBytes),
	}
	for name, in := range good {
		got, err := crew.ValidBehalfReason(in)
		if err != nil {
			t.Errorf("%s: refused: %v", name, err)
			continue
		}
		if got != strings.TrimSpace(in) {
			t.Errorf("%s: stored %q, want the trimmed input", name, got)
		}
	}
	bad := map[string]string{
		"empty":                   "",
		"spaces only":             "    ",
		"newlines only":           "\n\n",
		"zero-width only":         "\u200b\ufeff",
		"one byte over the cap":   strings.Repeat("a", crew.BehalfReasonMaxBytes+1),
		"a megabyte":              strings.Repeat("x", 1<<20),
		"NUL":                     "a\x00b",
		"escape":                  "a\x1b[31mred",
		"carriage return":         "a\rb",
		"line separator":          "a\u2028b",
		"paragraph separator":     "a\u2029b",
		"right-to-left override":  "a\u202eb",
		"isolate":                 "a\u2066b",
		"invalid UTF-8":           "a\xffb",
		"a lone surrogate (utf8)": "a\xed\xa0\x80b",
	}
	for name, in := range bad {
		if got, err := crew.ValidBehalfReason(in); err == nil {
			t.Errorf("%s: accepted as %.40q", name, got)
		}
	}
}

func TestAnAnswerValidatesItsOwnShape(t *testing.T) {
	if err := crew.OwnerAnswer("owner1").Validate(); err != nil {
		t.Errorf("an owner's own answer is refused: %v", err)
	}
	if err := (crew.Answer{Actor: "supervisor"}).Validate(); err != nil {
		t.Errorf("the supervisor's own act is refused: %v", err)
	}
	if err := crew.AdminAnswerFor("boss", "owner1", "on leave").Validate(); err != nil {
		t.Errorf("an admin's answer with a reason is refused: %v", err)
	}
	if err := crew.AdminAnswerFor("boss", "owner1", "").Validate(); err == nil {
		t.Error("an answer on behalf of an owner with no reason was accepted")
	}
	// A reason with nobody to be on behalf of, and an on-behalf answer
	// marked as the owner's, are both shapes no handler should produce.
	if err := (crew.Answer{Actor: "owner1", As: crew.AnsweredAsOwner, Reason: "why"}).Validate(); err == nil {
		t.Error("a reason on an answer that is on behalf of nobody was accepted")
	}
	if err := (crew.Answer{Actor: "boss", As: crew.AnsweredAsOwner, OnBehalfOf: "owner1", Reason: "why"}).Validate(); err == nil {
		t.Error("an answer on behalf of an owner but marked as the owner's own was accepted")
	}
}

// RefuseOption records the refusal, marks who answered and for whom, and
// journals option_refused; an answer on behalf of an owner with no reason
// changes nothing and journals nothing.
func TestRefuseOptionMarksAndJournalsWhoAnsweredAndForWhom(t *testing.T) {
	for _, c := range []struct {
		name      string
		ans       crew.Answer
		wantAs    string
		wantOwner string
		wantWhy   string
	}{
		{"the owner's own", crew.OwnerAnswer("owner1"), "owner", "", ""},
		{"an admin for the owner", crew.AdminAnswerFor("boss", "owner1", "  on leave "), "admin_on_behalf_of_owner", "owner1", "on leave"},
	} {
		t.Run(c.name, func(t *testing.T) {
			db := optionsTestDB(t)
			taskID := plantPlainTask(t, db)
			artID := plantDraftArtifact(t, db, taskID, "body")
			if _, err := db.Exec(`INSERT INTO artifact_options
				(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state)
				VALUES (?, 1, 'budget.set', 'raise it', 1000, 0, 'low', 'the owner', '[]', 'carried')`, artID); err != nil {
				t.Fatal(err)
			}
			rec := &spyRecorder{}
			if err := crew.RefuseOption(db, artID, 1, c.ans, "not this month", rec); err != nil {
				t.Fatal(err)
			}
			o, err := crew.GetOption(db, artID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if o.State != crew.OptionRefused || o.Reason != "not this month" || o.DecidedBy != c.ans.Actor ||
				o.OnBehalfOf != c.wantOwner || o.BehalfReason != c.wantWhy {
				t.Errorf("option reads %+v", o)
			}
			if rec.count("option_refused") != 1 {
				t.Fatalf("%d option_refused events, want 1", rec.count("option_refused"))
			}
			e := rec.events[0]
			if e.severity != "low" || e.actor != c.ans.Actor {
				t.Errorf("event severity %q actor %q, want low and %q", e.severity, e.actor, c.ans.Actor)
			}
			if e.data["answered_as"] != c.wantAs || e.data["answered_by"] != c.ans.Actor || e.data["reason"] != "not this month" {
				t.Errorf("event data %v", e.data)
			}
			if got, has := e.data["on_behalf_of"]; has != (c.wantOwner != "") || (has && got != c.wantOwner) {
				t.Errorf("event on_behalf_of = %v (present %v), want %q", got, has, c.wantOwner)
			}
			if got := e.data["on_behalf_reason"]; c.wantWhy != "" && got != c.wantWhy {
				t.Errorf("event on_behalf_reason = %v, want %q", got, c.wantWhy)
			}
		})
	}
}

func TestRefuseOptionOnBehalfWithNoReasonChangesNothing(t *testing.T) {
	db := optionsTestDB(t)
	taskID := plantPlainTask(t, db)
	artID := plantDraftArtifact(t, db, taskID, "body")
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state)
		VALUES (?, 1, 'budget.set', 'raise it', 1000, 0, 'low', 'the owner', '[]', 'carried')`, artID); err != nil {
		t.Fatal(err)
	}
	rec := &spyRecorder{}
	if err := crew.RefuseOption(db, artID, 1, crew.AdminAnswerFor("boss", "owner1", " "), "no", rec); err == nil {
		t.Fatal("a refusal on behalf of an owner with no reason was accepted")
	}
	o, _ := crew.GetOption(db, artID, 1)
	if o.State != crew.OptionCarried || len(rec.events) != 0 {
		t.Errorf("state %q with %d events: nothing should have changed", o.State, len(rec.events))
	}
	// And the refusal's own reason is still required, as it always was.
	if err := crew.RefuseOption(db, artID, 1, crew.OwnerAnswer("owner1"), "  ", rec); err != crew.ErrNeedReason {
		t.Errorf("a reasonless refusal returned %v, want ErrNeedReason", err)
	}
}

// AnswerData adds nothing for the supervisor's own act.
func TestAnswerDataAddsNothingForTheSupervisor(t *testing.T) {
	d := map[string]any{"artifact": 1}
	crew.AnswerData(d, crew.Answer{Actor: "supervisor"})
	if len(d) != 1 {
		t.Errorf("the supervisor's own act gained fields: %v", d)
	}
}

// The migration: an installation whose artifact_options predates the two
// columns gains them, safely twice, and reads back every old row as answered
// by nobody on behalf of anybody.
func TestEnsureOptionBehalfAddsTheColumnsSafelyTwice(t *testing.T) {
	db := optionsTestDB(t)
	if _, err := db.Exec(`DROP TABLE artifact_options`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE artifact_options(
		artifact INTEGER NOT NULL, ordinal INTEGER NOT NULL, class TEXT NOT NULL,
		summary TEXT, figure_cents INTEGER NOT NULL DEFAULT 0,
		saving_cents INTEGER NOT NULL DEFAULT 0, risk TEXT, needs TEXT,
		evidence TEXT, target TEXT, state TEXT NOT NULL, decided_by TEXT, decided_at TEXT,
		reason TEXT, PRIMARY KEY (artifact, ordinal))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_options (artifact, ordinal, class, state, decided_by)
		VALUES (7, 1, 'budget.set', 'applied', 'owner1')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := crew.EnsureOptionBehalf(db); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	o, err := crew.GetOption(db, 7, 1)
	if err != nil {
		t.Fatalf("an option from before the columns cannot be read: %v", err)
	}
	if o.DecidedBy != "owner1" || o.OnBehalfOf != "" || o.BehalfReason != "" {
		t.Errorf("an old answer reads %+v, want owner1 and no on-behalf mark", o)
	}
}

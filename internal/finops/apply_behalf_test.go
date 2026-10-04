package finops_test

// ApplyAs: an answer a person gave through the console, which says whose
// stamp it is. An answer on behalf of an owner needs a reason and is refused
// before any side effect when it has none; with one, the option and the
// journaled event name the owner, the admin and the reason.

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

type behalfEvent struct {
	kind, actor, severity string
	data                  map[string]any
}

type behalfSpy struct{ events []behalfEvent }

func (r *behalfSpy) Emit(kind, actor, severity string, data map[string]any, _ []string) error {
	r.events = append(r.events, behalfEvent{kind, actor, severity, data})
	return nil
}

// Red first (by compile: finops.ApplyAs does not exist before this change).
// The class has a real side effect, so "refused before anything changes" is
// something the test can see: the anomaly stays open.
func TestApplyAsRefusesAnAnswerOnBehalfWithNoReasonBeforeAnySideEffect(t *testing.T) {
	db := applyTestDB(t)
	plantAnomalyRow(t, db, "A-behalf")
	opt := plantOption(t, db, "aws", "A-behalf", "anomaly.dismiss", "a planned load test")
	rec := &behalfSpy{}

	err := finops.ApplyAs(db, opt, crew.AdminAnswerFor("boss", "owner1", "   "), rec)
	if err == nil {
		t.Fatal("an answer on behalf of an owner with no reason was applied")
	}
	if st := anomalyState(t, db, "A-behalf"); st != anomaly.Open {
		t.Errorf("the anomaly is %q: the side effect ran before the reason was checked", st)
	}
	got, _ := crew.GetOption(db, opt.Artifact, opt.Ordinal)
	if got.State != crew.OptionOpen {
		t.Errorf("the option is %q after a refused answer", got.State)
	}
	if len(rec.events) != 0 {
		t.Errorf("%d events were emitted for an answer that was refused", len(rec.events))
	}
}

func TestApplyAsOnBehalfOfAnOwnerMarksTheOptionAndTheEvent(t *testing.T) {
	db := applyTestDB(t)
	plantAnomalyRow(t, db, "A-behalf2")
	opt := plantOption(t, db, "aws", "A-behalf2", "anomaly.dismiss", "a planned load test")
	rec := &behalfSpy{}

	if err := finops.ApplyAs(db, opt, crew.AdminAnswerFor("boss", "owner1", " owner1 is on leave "), rec); err != nil {
		t.Fatal(err)
	}
	if st := anomalyState(t, db, "A-behalf2"); st != anomaly.Dismissed {
		t.Errorf("the anomaly is %q, want dismissed: the answer should apply", st)
	}
	got, _ := crew.GetOption(db, opt.Artifact, opt.Ordinal)
	if got.State != crew.OptionApplied || got.DecidedBy != "boss" || got.OnBehalfOf != "owner1" || got.BehalfReason != "owner1 is on leave" {
		t.Errorf("option reads %+v", got)
	}
	var applied *behalfEvent
	for i := range rec.events {
		if rec.events[i].kind == "option_applied" {
			applied = &rec.events[i]
		}
	}
	if applied == nil {
		t.Fatal("no option_applied event")
	}
	d := applied.data
	if applied.actor != "boss" || applied.severity != "info" || d["answered_by"] != "boss" ||
		d["answered_as"] != "admin_on_behalf_of_owner" || d["on_behalf_of"] != "owner1" ||
		d["on_behalf_reason"] != "owner1 is on leave" {
		t.Errorf("event %+v", *applied)
	}
}

func TestApplyAsAnOwnersOwnAnswerCarriesNoOnBehalfOf(t *testing.T) {
	db := applyTestDB(t)
	plantAnomalyRow(t, db, "A-behalf3")
	opt := plantOption(t, db, "aws", "A-behalf3", "anomaly.dismiss", "a planned load test")
	rec := &behalfSpy{}
	if err := finops.ApplyAs(db, opt, crew.OwnerAnswer("owner1"), rec); err != nil {
		t.Fatal(err)
	}
	got, _ := crew.GetOption(db, opt.Artifact, opt.Ordinal)
	if got.DecidedBy != "owner1" || got.OnBehalfOf != "" || got.BehalfReason != "" {
		t.Errorf("option reads %+v", got)
	}
	for _, e := range rec.events {
		if e.kind != "option_applied" {
			continue
		}
		if _, has := e.data["on_behalf_of"]; has {
			t.Errorf("the owner's own answer journals on_behalf_of: %v", e.data)
		}
		if e.data["answered_as"] != "owner" {
			t.Errorf("answered_as = %v, want owner", e.data["answered_as"])
		}
	}
}

// Apply, the supervisor's own act, adds none of it.
func TestApplyForTheSupervisorAddsNoAnswerFields(t *testing.T) {
	db := applyTestDB(t)
	plantAnomalyRow(t, db, "A-behalf4")
	opt := plantOption(t, db, "aws", "A-behalf4", "anomaly.dismiss", "a planned load test")
	rec := &behalfSpy{}
	if err := finops.Apply(db, opt, "supervisor", rec); err != nil {
		t.Fatal(err)
	}
	for _, e := range rec.events {
		if e.kind != "option_applied" {
			continue
		}
		for _, k := range []string{"answered_as", "answered_by", "on_behalf_of", "on_behalf_reason"} {
			if _, has := e.data[k]; has {
				t.Errorf("the supervisor's own act journals %s: %v", k, e.data)
			}
		}
	}
}

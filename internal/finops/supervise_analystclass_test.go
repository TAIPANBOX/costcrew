package finops_test

// The supervisor's pass and the classes the ANALYST link owns.
//
// roles.yaml gives the supervisor option.select in its decides_alone list and
// says what it means: "option.select for options inside the analysts' own
// classes". Supervise used to ask crew.MayDecide("supervisor", class), which
// for the literal role "supervisor" only checks that the class is owned by
// the supervisor, so every option of an analyst-owned class
// (recommendation.rightsizing, anomaly.dismiss, driver.one-time, ...) was
// carried to the owner as a decision request, whatever its figure. The
// owner was asked about every analyst option, which is the opposite of the
// decided rule that the owner is asked rarely.

import (
	"database/sql"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

func tAnomalyCents(t *testing.T) int64 {
	t.Helper()
	th, ok := crew.ThresholdFor("T.anomaly")
	if !ok {
		t.Fatal("T.anomaly is missing from roles.yaml")
	}
	return th.ValueCents
}

// requestOnFile is whether the owner has a decision request for this sprint.
func requestOnFile(t *testing.T, db *sql.DB, sprintID int, owner string) bool {
	t.Helper()
	_, found, err := crew.DecisionRequestFor(db, sprintID, owner)
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func anomalyState(t *testing.T, db *sql.DB, id string) anomaly.State {
	t.Helper()
	a, err := anomaly.Get(db, id)
	if err != nil {
		t.Fatal(err)
	}
	return a.State
}

// The class is owned by the analyst link, the figure is inside T.anomaly:
// the supervisor selects the option itself, as itself, and nobody is asked.
func TestTheSupervisorSelectsAnAnalystClassOptionWithinTAnomaly(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	artID, ords := plantDeliverable(t, db, sprintID, "aws", "y.mercer", "",
		optSpec{"recommendation.rightsizing", "move the batch fleet to smaller nodes", "low", 50000, 20000},
	)

	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Applied) != 1 || pass.Applied[0].Class != "recommendation.rightsizing" {
		t.Fatalf("applied %v, want exactly one recommendation.rightsizing: the class is the "+
			"analyst's own and option.select is the supervisor's", pass.Applied)
	}
	if len(pass.Carried) != 0 {
		t.Fatalf("carried %v, want none", pass.Carried)
	}
	got := mustGetOption(t, db, artID, ords[0])
	if got.State != crew.OptionApplied {
		t.Errorf("state %q, want applied", got.State)
	}
	if got.DecidedBy != "supervisor" {
		t.Errorf("decided_by %q, want supervisor", got.DecidedBy)
	}
	if len(pass.Requests) != 0 || requestOnFile(t, db, sprintID, "y.mercer") {
		t.Errorf("a decision request was written for an option the supervisor decided itself: %v", pass.Requests)
	}
}

// The same holds for an analyst class whose side effect changes the estate's
// record: the dismissal lands, journaled as the supervisor's act.
func TestTheSupervisorsSelectionOfAnAnalystClassCarriesItsSideEffect(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	plantAnomalyRow(t, db, "A-dismiss")
	artID, ords := plantDeliverable(t, db, sprintID, "aws", "y.mercer", "A-dismiss",
		optSpec{"anomaly.dismiss", "a planned load test, ticket on file", "low", 10000, 0},
	)

	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Applied) != 1 || len(pass.Requests) != 0 {
		t.Fatalf("applied %d, requests %d, want 1 and 0", len(pass.Applied), len(pass.Requests))
	}
	if st := anomalyState(t, db, "A-dismiss"); st != anomaly.Dismissed {
		t.Errorf("anomaly state %q, want dismissed", st)
	}
	if by := mustGetOption(t, db, artID, ords[0]).DecidedBy; by != "supervisor" {
		t.Errorf("decided_by %q, want supervisor", by)
	}
}

// A figure over T.anomaly is a key decision whoever owns the class.
func TestAnAnalystClassOptionOverTAnomalyIsCarried(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	over := tAnomalyCents(t) + 1
	artID, ords := plantDeliverable(t, db, sprintID, "aws", "y.mercer", "",
		optSpec{"recommendation.rightsizing", "retire the whole analytics cluster", "medium", over, 90000},
	)

	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Applied) != 0 || len(pass.Carried) != 1 {
		t.Fatalf("applied %d, carried %d, want 0 and 1: %d is over T.anomaly (%d)",
			len(pass.Applied), len(pass.Carried), over, tAnomalyCents(t))
	}
	if st := mustGetOption(t, db, artID, ords[0]).State; st != crew.OptionCarried {
		t.Errorf("state %q, want carried", st)
	}
	if !requestOnFile(t, db, sprintID, "y.mercer") {
		t.Errorf("no decision request for y.mercer: a carried option must reach its owner")
	}
}

// Exactly at the threshold is inside it, the same boundary a
// supervisor-owned class already has (figure <= T.anomaly).
func TestAnAnalystClassOptionExactlyAtTAnomalyIsApplied(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	plantDeliverable(t, db, sprintID, "aws", "y.mercer", "",
		optSpec{"recommendation.rightsizing", "resize one cluster", "low", tAnomalyCents(t), 1000},
	)
	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Applied) != 1 || len(pass.Carried) != 0 {
		t.Fatalf("applied %d, carried %d, want 1 and 0 at exactly T.anomaly", len(pass.Applied), len(pass.Carried))
	}
}

// What the supervisor never decides stays carried, inside the threshold or
// not: a class the owner holds, and a class nobody in the crew decides.
func TestOwnerAndNobodyClassOptionsStayCarriedWithinTAnomaly(t *testing.T) {
	for _, class := range []string{"period.close", "budget.set", "purchase", "infra.change", "vendor.negotiate"} {
		t.Run(class, func(t *testing.T) {
			db, sprintID := superviseTestDB(t)
			artID, ords := plantDeliverable(t, db, sprintID, "aws", "t.langley", "",
				optSpec{class, "a small one", "low", 10000, 1000},
			)
			pass, err := finops.Supervise(db, sprintID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(pass.Applied) != 0 || len(pass.Carried) != 1 {
				t.Fatalf("applied %d, carried %d, want 0 and 1 for %s", len(pass.Applied), len(pass.Carried), class)
			}
			if st := mustGetOption(t, db, artID, ords[0]).State; st != crew.OptionCarried {
				t.Errorf("state %q, want carried", st)
			}
			if !requestOnFile(t, db, sprintID, "t.langley") {
				t.Errorf("no decision request for t.langley")
			}
		})
	}
}

// One deliverable's choice is decided together. When its top-ranked option is
// an analyst class the supervisor selects, the lower-ranked alternative that
// the supervisor could not have decided (a purchase) is resolved with it as
// not_chosen, never applied and never asked about separately.
func TestSelectingAnAnalystOptionResolvesItsHandsUpAlternative(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	artID, ords := plantDeliverable(t, db, sprintID, "aws", "y.mercer", "",
		optSpec{"recommendation.rightsizing", "resize the fleet", "low", 10000, 40000}, // ranks first
		optSpec{"purchase", "buy a three year commitment", "high", 10000, 10000},
	)
	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Applied) != 1 || len(pass.Carried) != 0 || len(pass.Requests) != 0 {
		t.Fatalf("applied %d, carried %d, requests %d, want 1, 0, 0",
			len(pass.Applied), len(pass.Carried), len(pass.Requests))
	}
	if st := mustGetOption(t, db, artID, ords[1]).State; st != crew.OptionNotChosen {
		t.Errorf("the purchase alternative is %q, want not_chosen", st)
	}
}

// Two analysts who disagree on the same anomaly are still ONE question for
// the owner (roles.yaml's hands_to_owner_conditions), even though
// anomaly.explain is an analyst class the supervisor now selects within
// T.anomaly: a contradiction is carried, never settled by the ranking.
func TestAContradictedAnalystOptionIsStillCarriedToTheOwner(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	plantAnomalyRow(t, db, "A-split")
	one := plantPostedOptionOnAnomaly(t, db, sprintID, "aws", "y.mercer",
		"A-split", "anomaly.explain", "a scheduled batch job")
	two := plantPostedOptionOnAnomaly(t, db, sprintID, "aws", "t.langley",
		"A-split", "anomaly.explain", "a runaway process")

	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Applied) != 0 || len(pass.Carried) != 2 || len(pass.Requests) != 1 {
		t.Fatalf("applied %d, carried %d, requests %d, want 0, 2, 1: a disagreement between "+
			"two analysts is the owner's one question", len(pass.Applied), len(pass.Carried), len(pass.Requests))
	}
	if st := anomalyState(t, db, "A-split"); st != anomaly.Open {
		t.Errorf("anomaly state %q, want it left open while the owner has not answered", st)
	}
	for _, o := range []crew.Option{one, two} {
		if st := mustGetOption(t, db, o.Artifact, o.Ordinal).State; st != crew.OptionCarried {
			t.Errorf("option %d:%d state %q, want carried", o.Artifact, o.Ordinal, st)
		}
	}
}

// Selecting for an anomaly settles that anomaly once. A second analyst class
// option on an anomaly this pass has already decided is not applied on top
// of it (a dismissal followed by an explanation would be refused by the
// anomaly's own state machine and abort the whole pass); it is carried, since
// two analysts answering one anomaly is the owner's question.
func TestASecondOptionOnAnAnomalyTheSupervisorAlreadyDecidedIsCarried(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	plantAnomalyRow(t, db, "A-twice")
	first := plantPostedOptionOnAnomaly(t, db, sprintID, "aws", "y.mercer",
		"A-twice", "anomaly.dismiss", "a planned load test")
	second := plantPostedOptionOnAnomaly(t, db, sprintID, "aws", "t.langley",
		"A-twice", "anomaly.explain", "a scheduled batch job")

	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatalf("the pass aborted on the second option: %v", err)
	}
	if len(pass.Applied) != 1 || pass.Applied[0].Class != "anomaly.dismiss" {
		t.Fatalf("applied %v, want exactly the anomaly.dismiss", pass.Applied)
	}
	if st := mustGetOption(t, db, first.Artifact, first.Ordinal).State; st != crew.OptionApplied {
		t.Errorf("dismiss state %q, want applied", st)
	}
	if st := mustGetOption(t, db, second.Artifact, second.Ordinal).State; st != crew.OptionCarried {
		t.Errorf("explain state %q, want carried", st)
	}
	if st := anomalyState(t, db, "A-twice"); st != anomaly.Dismissed {
		t.Errorf("anomaly state %q, want dismissed", st)
	}
}

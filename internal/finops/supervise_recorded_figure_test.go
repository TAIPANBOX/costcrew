package finops_test

// F-008 (security review 2026-10-17): the supervisor's threshold read the
// figure the ANALYST wrote into its options block. An injected analyst can
// propose anomaly.dismiss with figure_cents 1000 for a USD 1,040.00 anomaly,
// and the pass applied it as within T.anomaly, closing the anomaly on a
// number nothing recorded. For an option tied to an anomaly (its deliverable's
// task came from one) the threshold is judged on the anomaly's own recorded
// excess, in exact cents, and an option whose figure disagrees with it is
// carried to the owner with the reason written on the decision.
//
// Every test here is red against the code before the fix: each asserts an
// outcome the old comparison produced the other way.

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// plantAnomalyExcess is plantAnomalyRow with the excess given: a signed
// excess (negative is a fall). The baseline is fixed and the amount is made
// consistent with it, so the row reads as the detector would write it.
func plantAnomalyExcess(t *testing.T, db *sql.DB, id string, excess int64) {
	t.Helper()
	if _, err := db.Exec(anomaly.Schema); err != nil {
		t.Fatal(err)
	}
	const baseline = int64(200000)
	direction := "up"
	if excess < 0 {
		direction = "down"
	}
	if _, err := db.Exec(`INSERT INTO anomalies
		(id, source, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule_version, state, detected_at)
		VALUES (?,'aws','Amazon EC2','2026-07-14',?,?,?,?,4.2,?,?,?)`,
		id, direction, baseline+excess, baseline, excess, anomaly.RuleVersion,
		string(anomaly.Open), "2026-07-15T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
}

// The injected-analyst case, in the shape the review named: a dismissal on a
// figure of 10.00 for an anomaly whose recorded excess is 1,040.00.
func TestAnOptionFiguredDifferentlyFromItsAnomalyIsCarriedNotApplied(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	plantAnomalyExcess(t, db, "A-understated", 104000)
	artID, ords := plantDeliverable(t, db, sprintID, "aws", "y.mercer", "A-understated",
		optSpec{"anomaly.dismiss", "a planned load test", "low", 1000, 0})

	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Applied) != 0 {
		t.Fatalf("applied %v: a dismissal on figure %s was applied to an anomaly whose recorded excess is %s",
			pass.Applied, money.Cents(1000), money.Cents(104000))
	}
	if len(pass.Carried) != 1 {
		t.Fatalf("carried %d, want 1: the figure disagrees with the anomaly, so the owner decides", len(pass.Carried))
	}
	if st := anomalyState(t, db, "A-understated"); st != anomaly.Open {
		t.Errorf("anomaly state %q, want open: nothing was applied", st)
	}
	if got := mustGetOption(t, db, artID, ords[0]); got.State != crew.OptionCarried {
		t.Errorf("option state %q, want carried", got.State)
	}
}

// The threshold reads the anomaly's excess, never the option's own figure.
// Row (a) is the defect itself: the option's 1,000.00 is under T.anomaly, the
// anomaly's 4,000.00 is over it, and the old code applied the dismissal.
func TestTheThresholdReadsTheAnomaliesRecordedExcessNotTheOption(t *testing.T) {
	cases := []struct {
		name           string
		excess, figure int64
		wantApplied    bool
	}{
		{"figure under T.anomaly, recorded excess over it, figures disagree", 400000, 100000, false},
		{"figure and recorded excess agree and are over T.anomaly", 400000, 400000, false},
		{"figure and recorded excess agree and are under T.anomaly", 240000, 240000, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, sprintID := superviseTestDB(t)
			plantAnomalyExcess(t, db, "A-threshold", c.excess)
			plantDeliverable(t, db, sprintID, "aws", "y.mercer", "A-threshold",
				optSpec{"anomaly.explain", "a scheduled batch job", "low", c.figure, 0})

			pass, err := finops.Supervise(db, sprintID, nil)
			if err != nil {
				t.Fatal(err)
			}
			applied := len(pass.Applied) == 1
			if applied != c.wantApplied {
				t.Errorf("applied %v, want %v: recorded excess %s, option figure %s, T.anomaly %s",
					applied, c.wantApplied, money.Cents(c.excess), money.Cents(c.figure),
					money.Cents(tAnomalyCents(t)))
			}
		})
	}
}

// The recorded figure is the magnitude of the excess. A fall is judged by how
// far it fell, so a figure equal to that magnitude agrees, and a figure that
// does not is carried.
func TestTheRecordedFigureIsTheMagnitudeOfAnExcessThatFell(t *testing.T) {
	cases := []struct {
		name        string
		figure      int64
		wantApplied bool
	}{
		{"figure equals the fall's magnitude", 104000, true},
		{"figure is the signed fall, not its magnitude, read as a disagreement", -104000, false},
		{"figure understates the fall", 1000, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, sprintID := superviseTestDB(t)
			plantAnomalyExcess(t, db, "A-fall", -104000)
			plantDeliverable(t, db, sprintID, "aws", "y.mercer", "A-fall",
				optSpec{"anomaly.dismiss", "a planned load test", "low", c.figure, 0})

			pass, err := finops.Supervise(db, sprintID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if applied := len(pass.Applied) == 1; applied != c.wantApplied {
				t.Errorf("applied %v, want %v for figure %s against a fall of %s",
					applied, c.wantApplied, money.Cents(c.figure), money.Cents(104000))
			}
		})
	}
}

// Exact cents, no tolerance: the figure and the anomaly's excess come from the
// same integer, the packet prints it and the analyst copies it, so a one-cent
// difference is a different number and goes to the owner.
func TestAgreementIsExactToTheCent(t *testing.T) {
	cases := []struct {
		name        string
		figure      int64
		wantApplied bool
	}{
		{"exactly the recorded excess", 104000, true},
		{"one cent over", 104001, false},
		{"one cent under", 103999, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, sprintID := superviseTestDB(t)
			plantAnomalyExcess(t, db, "A-cent", 104000)
			plantDeliverable(t, db, sprintID, "aws", "y.mercer", "A-cent",
				optSpec{"anomaly.dismiss", "a planned load test", "low", c.figure, 0})

			pass, err := finops.Supervise(db, sprintID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if applied := len(pass.Applied) == 1; applied != c.wantApplied {
				t.Errorf("applied %v, want %v for figure %d cents against 104000", applied, c.wantApplied, c.figure)
			}
		})
	}
}

// An analyst that wrote no figure (the zero value a missing key decodes to)
// does not get a free pass under the threshold.
func TestAnOptionWithNoFigureOnAnAnomalyTaskIsCarried(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	plantAnomalyExcess(t, db, "A-nofigure", 104000)
	plantDeliverable(t, db, sprintID, "aws", "y.mercer", "A-nofigure",
		optSpec{"anomaly.dismiss", "a planned load test", "low", 0, 0})

	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Applied) != 0 || len(pass.Carried) != 1 {
		t.Fatalf("applied %d, carried %d, want 0 and 1 for an option with no figure on a 1,040.00 anomaly",
			len(pass.Applied), len(pass.Carried))
	}
}

// The reason is recorded on the decision, where the pass already writes a
// contradiction's reason (decisionRequestBody renders a note under its option).
func TestTheCarriedDecisionSaysWhyItsFigureWasNotApplied(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	plantAnomalyExcess(t, db, "A-reason", 104000)
	plantDeliverable(t, db, sprintID, "aws", "y.mercer", "A-reason",
		optSpec{"anomaly.dismiss", "a planned load test", "low", 1000, 0})

	if _, err := finops.Supervise(db, sprintID, nil); err != nil {
		t.Fatal(err)
	}
	artifactID, found, err := crew.DecisionRequestFor(db, sprintID, "y.mercer")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("no decision request for the owner: the carried option has nowhere to be asked about")
	}
	var body string
	if err := db.QueryRow(`SELECT body FROM artifacts WHERE id=?`, artifactID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"does not match the anomaly's recorded excess",
		money.Cents(104000).String(),
		money.Cents(1000).String(),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("decision request does not contain %q:\n%s", want, body)
		}
	}
}

package finops_test

// T.anomaly was halved on 2026-10-04, USD 5,000 to USD 2,500. The supervisor's
// pass compares an option's figure against it, so the same option is a
// different decision before and after: USD 3,000 was inside the draft and is
// over the decided value. These tests use the literal cents on purpose. The
// older boundary tests read the threshold from roles.yaml and so follow it
// wherever it goes, which is right for them and says nothing about WHERE it is.

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

// Red first: under the draft T.anomaly (500000) a USD 3,000 option was applied
// by the supervisor itself and nobody was asked.
func TestAnOptionOfThreeThousandDollarsIsCarriedToTheOwner(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	artID, ords := plantDeliverable(t, db, sprintID, "aws", "y.mercer", "",
		optSpec{"recommendation.rightsizing", "retire one analytics cluster", "medium", 300000, 90000},
	)

	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Applied) != 0 || len(pass.Carried) != 1 {
		t.Fatalf("applied %d, carried %d, want 0 and 1: USD 3,000 is over the decided T.anomaly of USD 2,500",
			len(pass.Applied), len(pass.Carried))
	}
	if st := mustGetOption(t, db, artID, ords[0]).State; st != crew.OptionCarried {
		t.Errorf("state %q, want carried", st)
	}
	if !requestOnFile(t, db, sprintID, "y.mercer") {
		t.Errorf("no decision request for y.mercer: a carried option must reach its owner")
	}
}

// Red first on the one-cent side: under the draft, 250001 was inside.
func TestTAnomalyBoundaryIsTwoThousandFiveHundredDollarsToTheCent(t *testing.T) {
	t.Run("exactly USD 2,500 is inside", func(t *testing.T) {
		db, sprintID := superviseTestDB(t)
		plantDeliverable(t, db, sprintID, "aws", "y.mercer", "",
			optSpec{"recommendation.rightsizing", "resize one cluster", "low", 250000, 1000},
		)
		pass, err := finops.Supervise(db, sprintID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(pass.Applied) != 1 || len(pass.Carried) != 0 {
			t.Fatalf("applied %d, carried %d, want 1 and 0 at exactly USD 2,500", len(pass.Applied), len(pass.Carried))
		}
	})
	t.Run("one cent over is the owner's", func(t *testing.T) {
		db, sprintID := superviseTestDB(t)
		plantDeliverable(t, db, sprintID, "aws", "y.mercer", "",
			optSpec{"recommendation.rightsizing", "resize one cluster", "low", 250001, 1000},
		)
		pass, err := finops.Supervise(db, sprintID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(pass.Applied) != 0 || len(pass.Carried) != 1 {
			t.Fatalf("applied %d, carried %d, want 0 and 1 one cent over USD 2,500", len(pass.Applied), len(pass.Carried))
		}
	})
}

// The same decision for a class the supervisor owns outright: a figure of USD
// 3,000 on a driver.recurring is carried, where under the draft it was applied.
func TestASupervisorOwnedClassOverTheDecidedTAnomalyIsCarried(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	artID, ords := plantDeliverableWithTargets(t, db, sprintID, "aws", "y.mercer", "",
		map[int]string{1: `{"start": "2026-08-01", "end": "2026-08-30"}`},
		optSpec{"driver.recurring", "a scheduled batch job", "low", 300000, 0},
	)
	pass, err := finops.Supervise(db, sprintID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Applied) != 0 || len(pass.Carried) != 1 {
		t.Fatalf("applied %d, carried %d, want 0 and 1: USD 3,000 is over USD 2,500", len(pass.Applied), len(pass.Carried))
	}
	if st := mustGetOption(t, db, artID, ords[0]).State; st != crew.OptionCarried {
		t.Errorf("state %q, want carried", st)
	}
}

package finops_test

// The anomaly and period classes' side effects beyond the explain and close
// cases apply_test.go already holds: dismissing and accepting, an anomaly
// class on a task that names no anomaly, and reopening a period, including
// the refusal when nothing is closed.

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

func plantOpenAnomaly(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO anomalies
		(id, source, team, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule_version, state, detected_at)
		VALUES (?, 'aws', 'ml-platform', 'Amazon EC2', '2026-07-14', 'up', 184000, 80000,
		        104000, 4.2, ?, 'open', '2026-07-15T00:00:00Z')`,
		id, anomaly.RuleVersion); err != nil {
		t.Fatal(err)
	}
}

func TestApplyAnomalyDismissAndAcceptMoveTheAnomaly(t *testing.T) {
	db := applyTestDB(t)

	plantOpenAnomaly(t, db, "A-dismiss")
	opt := plantOption(t, db, "aws", "A-dismiss", "anomaly.dismiss", "below the money floor")
	if err := finops.Apply(db, opt, "supervisor", nil); err != nil {
		t.Fatal(err)
	}
	got, err := anomaly.Get(db, "A-dismiss")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != anomaly.Dismissed || got.Reason != "below the money floor" {
		t.Errorf("after anomaly.dismiss: state %q reason %q", got.State, got.Reason)
	}

	// Accepting needs an answer first: an open anomaly refuses it, and the
	// option is left exactly as it was.
	plantOpenAnomaly(t, db, "A-accept")
	acc := plantOption(t, db, "aws", "A-accept", "anomaly.accept", "the cause is understood")
	if err := finops.Apply(db, acc, "supervisor", nil); err == nil {
		t.Fatal("accepting an anomaly nobody has explained was applied")
	}
	if o := mustGetOption(t, db, acc.Artifact, acc.Ordinal); o.State != crew.OptionOpen {
		t.Errorf("a refused accept left the option %q, want open", o.State)
	}
	if err := anomaly.Explain(db, "A-accept", "a batch job", nil); err != nil {
		t.Fatal(err)
	}
	if err := finops.Apply(db, acc, "supervisor", nil); err != nil {
		t.Fatal(err)
	}
	got, err = anomaly.Get(db, "A-accept")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != anomaly.Accepted {
		t.Errorf("after anomaly.accept: state %q, want accepted", got.State)
	}
}

// A task that names no anomaly has nothing for these classes to move: the
// option is recorded as applied and nothing else changes.
func TestApplyAnAnomalyClassWithNoAnomalyIsRecordedOnly(t *testing.T) {
	for _, class := range []string{"anomaly.explain", "anomaly.dismiss", "anomaly.accept"} {
		t.Run(class, func(t *testing.T) {
			db := applyTestDB(t)
			plantOpenAnomaly(t, db, "A-untouched")
			opt := plantOption(t, db, "aws", "", class, "a reason")
			if err := finops.Apply(db, opt, "supervisor", nil); err != nil {
				t.Fatal(err)
			}
			if o := mustGetOption(t, db, opt.Artifact, opt.Ordinal); o.State != crew.OptionApplied {
				t.Errorf("option state %q, want applied", o.State)
			}
			got, err := anomaly.Get(db, "A-untouched")
			if err != nil {
				t.Fatal(err)
			}
			if got.State != anomaly.Open {
				t.Errorf("an option on a task with no anomaly moved another anomaly to %q", got.State)
			}
		})
	}
}

func TestApplyPeriodReopen(t *testing.T) {
	db := applyTestDB(t)

	// Nothing closed: refused, and the option stays open.
	none := plantOption(t, db, "aws", "", "period.reopen", "")
	err := finops.Apply(db, none, "y.mercer", nil)
	if err == nil || !strings.Contains(err.Error(), "no period is closed") {
		t.Fatalf("reopen with nothing closed: %v", err)
	}
	if o := mustGetOption(t, db, none.Artifact, none.Ordinal); o.State != crew.OptionOpen {
		t.Errorf("a refused reopen left the option %q, want open", o.State)
	}

	period, err := finops.OpenPeriod(db)
	if err != nil || period == "" {
		t.Fatalf("no open period: %v", err)
	}
	if err := finops.Close(db, period, "y.mercer"); err != nil {
		t.Fatal(err)
	}
	// No summary of its own: the actor's name stands in as the reason
	// Reopen insists on.
	reopen := plantOption(t, db, "aws", "", "period.reopen", "")
	if err := finops.Apply(db, reopen, "y.mercer", nil); err != nil {
		t.Fatal(err)
	}
	closed, err := finops.IsClosed(db, period)
	if err != nil {
		t.Fatal(err)
	}
	if closed {
		t.Errorf("%s is still closed after applying period.reopen", period)
	}
}

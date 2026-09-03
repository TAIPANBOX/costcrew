package finops_test

// C9-SPEC.md section 2: "a halt older than T.stale_days is handed to the
// owner (the supervisor's hands_to_owner_conditions already say so)."
// roles.yaml's own words for the supervisor: "a data.halt that has lasted
// past T.stale_days" is one of exactly two named hands_to_owner_conditions,
// the same mechanism contradictionRouting already uses for the other one
// ("any question two analysts answer differently on the same evidence").
//
// Red first, against main: Supervise reads no halt at all, so a halt on file
// changes nothing about what a sprint's pass carries.

import (
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

// mustStaleDaysThreshold reads T.stale_days the same way DataQuality reads
// T.stale and T.untagged: from the roles data, never a literal, so this
// test survives roles.yaml's own number changing.
func mustStaleDaysThreshold(t *testing.T) int {
	t.Helper()
	th, ok := crew.ThresholdFor("T.stale_days")
	if !ok {
		t.Fatal("roles.yaml carries no T.stale_days threshold")
	}
	n := 0
	for _, r := range th.Value {
		if r < '0' || r > '9' {
			t.Fatalf("T.stale_days's value %q is not a plain integer", th.Value)
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func TestAHaltOlderThanTStaleDaysIsCarriedToTheOwnerBySupervise(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	staleDays := mustStaleDaysThreshold(t)

	reason := "onprem tagging feed has been stale for 11 days"
	started := time.Now().UTC().AddDate(0, 0, -staleDays).Format("2006-01-02")
	if _, _, err := crew.ApplyHalt(db, "onprem", reason, "data-quality", "t.langley", started, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := finops.Supervise(db, sprintID, nil); err != nil {
		t.Fatal(err)
	}

	_, found, err := crew.DecisionRequestFor(db, sprintID, "t.langley")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("no decision request on file for t.langley after a %d-day-old halt "+
			"on onprem (T.stale_days is %d)", staleDays, staleDays)
	}
	body := decisionRequestBodyFor(t, db, sprintID, "t.langley")
	if !strings.Contains(body, "onprem") {
		t.Errorf("the decision request does not name the halted desk:\n%s", body)
	}
	if !strings.Contains(body, reason) {
		t.Errorf("the decision request does not carry the halt's own reason:\n%s", body)
	}
}

// A halt that has NOT yet reached T.stale_days is not carried: an owner
// should not be asked about something the supervisor's own job description
// still gets to sit with.
func TestAHaltYoungerThanTStaleDaysIsNotCarried(t *testing.T) {
	db, sprintID := superviseTestDB(t)
	staleDays := mustStaleDaysThreshold(t)
	if staleDays <= 1 {
		t.Skip("T.stale_days is too small to construct a not-yet-stale case")
	}

	started := time.Now().UTC().AddDate(0, 0, -(staleDays - 1)).Format("2006-01-02")
	if _, _, err := crew.ApplyHalt(db, "onprem", "just applied", "data-quality", "t.langley", started, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := finops.Supervise(db, sprintID, nil); err != nil {
		t.Fatal(err)
	}

	if _, found, err := crew.DecisionRequestFor(db, sprintID, "t.langley"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Error("a halt one day short of T.stale_days was already carried to the owner")
	}
}

package anomaly_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/detect"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

func seeded(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := estate.Seed(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func run(t *testing.T, db *sql.DB) (found, added int) {
	t.Helper()
	f, a, err := anomaly.Run(db, time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC), detect.Default(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return f, a
}

// The identity is the whole design, so it is the first thing tested.
func TestIDIsDerivedAndStable(t *testing.T) {
	a := anomaly.ID("aws", "ml-platform", "Amazon EC2", "2026-07-14", "v1")
	b := anomaly.ID("aws", "ml-platform", "Amazon EC2", "2026-07-14", "v1")
	if a != b {
		t.Fatalf("the same event produced two ids: %s and %s", a, b)
	}
	for _, other := range []string{
		anomaly.ID("gcp", "ml-platform", "Amazon EC2", "2026-07-14", "v1"),
		anomaly.ID("aws", "data-eng", "Amazon EC2", "2026-07-14", "v1"),
		anomaly.ID("aws", "ml-platform", "Amazon S3", "2026-07-14", "v1"),
		anomaly.ID("aws", "ml-platform", "Amazon EC2", "2026-07-15", "v1"),
		// A retuned rule must produce a NEW anomaly, not silently change the
		// numbers under a decision already made about the old one.
		anomaly.ID("aws", "ml-platform", "Amazon EC2", "2026-07-14", "v2"),
	} {
		if other == a {
			t.Errorf("a different event collided with %s", a)
		}
	}
}

func TestRunFindsThePlantedEvents(t *testing.T) {
	db := seeded(t)
	found, added := run(t, db)
	if found == 0 {
		t.Fatal("the detector found nothing in an estate with nine planted events")
	}
	if added != found {
		t.Errorf("first run: %d found but only %d stored", found, added)
	}

	list, err := anomaly.List(db, anomaly.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < len(world.MustDetect()) {
		t.Errorf("%d anomalies stored, but %d events were planted to be found",
			len(list), len(world.MustDetect()))
	}
	for _, a := range list {
		if a.State != anomaly.Open {
			t.Errorf("%s arrived in state %q; a fresh detection is open", a.ID, a.State)
		}
		if a.RuleVer == "" || a.Rule == "" {
			t.Errorf("%s has no rule recorded, so the page cannot print the test it failed", a.ID)
		}
	}
}

// The property that makes state possible at all.
func TestASecondRunAddsNothingAndDisturbsNothing(t *testing.T) {
	db := seeded(t)
	run(t, db)

	before, err := anomaly.List(db, anomaly.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 {
		t.Fatal("nothing to re-run against")
	}
	if err := anomaly.Assign(db, before[0].ID, "triage-aws", nil); err != nil {
		t.Fatal(err)
	}
	if err := anomaly.Dismiss(db, before[1].ID, "Known load test, agreed with the team on the 15th", nil); err != nil {
		t.Fatal(err)
	}

	_, added := run(t, db)
	if added != 0 {
		t.Errorf("a second run over unchanged data added %d anomalies", added)
	}

	after, err := anomaly.Get(db, before[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != anomaly.Triaged || after.HandledBy != "triage-aws" {
		t.Errorf("re-running the detector took the owner off %s: state %q, handled by %q",
			after.ID, after.State, after.HandledBy)
	}
	dis, err := anomaly.Get(db, before[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if dis.State != anomaly.Dismissed {
		t.Errorf("a dismissed anomaly came back as %q on the next run; that is the "+
			"defect this whole package exists to prevent", dis.State)
	}
	if dis.Reason == "" {
		t.Error("the dismissal lost its reason")
	}
	if dis.ClosedAt == "" {
		t.Error("a closed anomaly has no closing time, so response time cannot be measured")
	}
}

// A decision without a reason is indistinguishable from nobody having looked.
func TestClosingWithoutAReasonIsRefused(t *testing.T) {
	db := seeded(t)
	run(t, db)
	list, _ := anomaly.List(db, anomaly.Filter{})
	if len(list) < 3 {
		t.Fatal("not enough anomalies for this test")
	}

	for _, blank := range []string{"", "   ", "\t\n"} {
		if err := anomaly.Dismiss(db, list[0].ID, blank, nil); !errors.Is(err, anomaly.ErrNeedReason) {
			t.Errorf("dismiss with %q was accepted (err=%v)", blank, err)
		}
		if err := anomaly.Explain(db, list[0].ID, blank, nil); !errors.Is(err, anomaly.ErrNeedReason) {
			t.Errorf("explain with %q was accepted (err=%v)", blank, err)
		}
	}
	// Assigning needs no reason: taking something on is not a decision anybody
	// has to justify afterwards.
	if err := anomaly.Assign(db, list[0].ID, "investigator-aws", nil); err != nil {
		t.Errorf("assign was refused: %v", err)
	}
}

func TestAClosedAnomalyCannotBeReopenedByAnotherDecision(t *testing.T) {
	db := seeded(t)
	run(t, db)
	list, _ := anomaly.List(db, anomaly.Filter{})
	id := list[0].ID

	if err := anomaly.Dismiss(db, id, "Duplicate of the incident on the 12th", nil); err != nil {
		t.Fatal(err)
	}
	if err := anomaly.Assign(db, id, "someone-else", nil); !errors.Is(err, anomaly.ErrClosed) {
		t.Errorf("a closed anomaly accepted a new owner: %v", err)
	}
	if err := anomaly.Explain(db, id, "second thoughts", nil); !errors.Is(err, anomaly.ErrClosed) {
		t.Errorf("a closed anomaly accepted a new explanation: %v", err)
	}
}

func TestUnknownAnomalyIsReported(t *testing.T) {
	db := seeded(t)
	run(t, db)
	if err := anomaly.Dismiss(db, "A-deadbeefdead", "nope", nil); !errors.Is(err, anomaly.ErrNotFound) {
		t.Errorf("dismissing a non-existent anomaly gave %v", err)
	}
}

// Two different questions, two different columns, and the grain is always
// stated rather than implied.
func TestCausedByCarriesItsGrain(t *testing.T) {
	db := seeded(t)
	run(t, db)
	list, err := anomaly.List(db, anomaly.Filter{})
	if err != nil {
		t.Fatal(err)
	}

	var agentGrain, teamGrain int
	for _, a := range list {
		switch a.CausedByKind {
		case "agent":
			agentGrain++
			if a.CausedBy == "" {
				t.Errorf("%s claims agent grain with no agent named", a.ID)
			}
		case "team":
			teamGrain++
			if a.CausedBy != a.Team {
				t.Errorf("%s claims team grain but names %q, not %q", a.ID, a.CausedBy, a.Team)
			}
		case "unknown":
			if a.CausedBy != "" {
				t.Errorf("%s claims unknown grain but names %q", a.ID, a.CausedBy)
			}
		default:
			t.Errorf("%s has grain %q, which is not one of the three", a.ID, a.CausedByKind)
		}
		// handled_by is a different question and must not be filled in by
		// detection.
		if a.State == anomaly.Open && a.HandledBy != "" {
			t.Errorf("%s is open but already has a handler", a.ID)
		}
	}
	if agentGrain == 0 {
		t.Error("no anomaly reached agent grain, so the column the governance stack " +
			"exists for is never exercised")
	}
	if teamGrain == 0 {
		t.Error("no anomaly sat at team grain; the honest-about-grain path is untested")
	}
}

func TestListRanksByMoney(t *testing.T) {
	db := seeded(t)
	run(t, db)
	list, _ := anomaly.List(db, anomaly.Filter{})
	for i := 1; i < len(list); i++ {
		if list[i-1].Excess.Abs() < list[i].Excess.Abs() {
			t.Fatalf("%s (%s) ranks above %s (%s)",
				list[i-1].ID, list[i-1].Excess, list[i].ID, list[i].Excess)
		}
	}
}

func TestFiltersNarrow(t *testing.T) {
	db := seeded(t)
	run(t, db)
	all, _ := anomaly.List(db, anomaly.Filter{})
	open, _ := anomaly.List(db, anomaly.Filter{State: anomaly.Open})
	if len(open) != len(all) {
		t.Errorf("everything should be open on a first run: %d of %d", len(open), len(all))
	}
	down, _ := anomaly.List(db, anomaly.Filter{Direction: "down"})
	if len(down) == 0 {
		t.Error("no downward anomalies; a one-sided detector would look complete here")
	}
	for _, a := range down {
		if a.Direction != "down" {
			t.Errorf("%s is %q under a down filter", a.ID, a.Direction)
		}
	}
}

// The strongest statement the fixture can make, and the one the per-series
// tests cannot: across the WHOLE estate, 42 series and nineteen thousand rows,
// the detector finds the planted events and nothing else.
//
// The per-series tests check the controls at the points where a control was
// planted. This one checks everywhere else too, which is where a false
// positive would actually come from.
func TestTheWholeEstateProducesExactlyThePlantedEvents(t *testing.T) {
	db := seeded(t)
	run(t, db)
	list, err := anomaly.List(db, anomaly.Filter{})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]world.Event{}
	for _, e := range world.MustDetect() {
		want[e.Source+"|"+e.Team+"|"+e.Service] = e
	}

	got := map[string]bool{}
	for _, a := range list {
		k := a.Source + "|" + a.Team + "|" + a.Service
		e, ok := want[k]
		if !ok {
			t.Errorf("false positive: %s on %s, %s, worth %s, where nothing was planted",
				a.ID, k, a.Day, a.Excess)
			continue
		}
		if got[k] {
			t.Errorf("%s reported twice; one incident should be one anomaly", k)
		}
		got[k] = true
		if a.Day != e.Day {
			// A day either side is fine: an incident that starts late in the
			// evening lands on the next date.
			if diff := dayGap(t, a.Day, e.Day); diff > 1 {
				t.Errorf("%s: reported on %s, planted on %s", e.ID, a.Day, e.Day)
			}
		}
	}
	for k, e := range want {
		if !got[k] {
			t.Errorf("missed %s: %s", e.ID, e.Why)
		}
	}
	if len(list) != len(world.MustDetect()) {
		t.Errorf("%d anomalies for %d planted events", len(list), len(world.MustDetect()))
	}
}

func dayGap(t *testing.T, a, b string) int {
	t.Helper()
	ta, err := time.Parse("2006-01-02", a)
	if err != nil {
		t.Fatal(err)
	}
	tb, err := time.Parse("2006-01-02", b)
	if err != nil {
		t.Fatal(err)
	}
	d := int(ta.Sub(tb).Hours() / 24)
	if d < 0 {
		return -d
	}
	return d
}

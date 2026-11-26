package history

// Seed gives a fresh installation a past, through the same functions a
// person's decisions go through, and it runs on every start: so it must
// write the past once and never move a decision on the second run.

import (
	"database/sql"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/detect"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// seededStore is what main.go seeds before history.Seed: the estate, its
// anomalies, the board and the roster.
func seededStore(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.DB()
	if _, err := estate.Seed(db); err != nil {
		t.Fatal(err)
	}
	if _, _, err := anomaly.Run(db, time.Now(), detect.Default(), nil); err != nil {
		t.Fatal(err)
	}
	list, err := anomaly.List(db, anomaly.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var seeds []crew.AnomalySeed
	for _, a := range list {
		seeds = append(seeds, crew.AnomalySeed{ID: a.ID, Source: a.Source, Service: a.Service,
			Day: a.Day, Direction: a.Direction, Excess: a.Excess})
	}
	if _, _, _, err := crew.Seed(db, seeds); err != nil {
		t.Fatal(err)
	}
	if err := finops.SeedRules(db); err != nil {
		t.Fatal(err)
	}
	if _, err := crew.SeedRoster(db, "owner"); err != nil {
		t.Fatal(err)
	}
	return db
}

func anomalyStates(t *testing.T, db *sql.DB) map[anomaly.State]int {
	t.Helper()
	all, err := anomaly.List(db, anomaly.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[anomaly.State]int{}
	for _, a := range all {
		out[a.State]++
	}
	return out
}

func TestSeedWritesThePastOnceAndOnlyOnce(t *testing.T) {
	db := seededStore(t)
	before := anomalyStates(t, db)

	c, err := Seed(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Triaged == 0 || c.Explained == 0 || c.Accepted+c.Dismissed == 0 {
		t.Errorf("no anomaly was worked: %+v", c)
	}
	if c.Explained > c.Triaged || c.Accepted+c.Dismissed > c.Explained {
		t.Errorf("a later state outnumbers the one before it: %+v", c)
	}
	if c.Forecasts == 0 || c.Explainers != len(explainerTopics) || c.Comments == 0 {
		t.Errorf("counts %+v: want forecasts, %d explainers and comments", c, len(explainerTopics))
	}
	after := anomalyStates(t, db)
	if after[anomaly.Open] == 0 {
		t.Error("every anomaly was worked: a queue nobody is behind on is not a queue")
	}
	if after[anomaly.Open] >= before[anomaly.Open] {
		t.Errorf("open %d -> %d: nothing was decided", before[anomaly.Open], after[anomaly.Open])
	}
	exps, err := crew.Explainers(db)
	if err != nil {
		t.Fatal(err)
	}
	published := 0
	for _, e := range exps {
		if e.Publisher != "" {
			published++
		}
	}
	if published != 2 {
		t.Errorf("%d explainers published, want two (every third, from the first)", published)
	}

	// The second start changes nothing.
	again, err := Seed(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again != (Counts{}) {
		t.Errorf("a second Seed did %+v; it must find the past already written", again)
	}
	if got := anomalyStates(t, db); len(got) != len(after) || got[anomaly.Open] != after[anomaly.Open] ||
		got[anomaly.Accepted] != after[anomaly.Accepted] || got[anomaly.Dismissed] != after[anomaly.Dismissed] {
		t.Errorf("a second Seed moved decisions: %v -> %v", after, got)
	}
}

// With nobody active there is nobody to decide anything or write anything:
// the past is left empty rather than invented under a name that cannot act.
func TestSeedWithNobodyActiveDecidesNothing(t *testing.T) {
	db := seededStore(t)
	if _, err := db.Exec(`UPDATE analysts SET state='suspended'`); err != nil {
		t.Fatal(err)
	}
	c, err := Seed(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Triaged != 0 || c.Explainers != 0 {
		t.Errorf("with nobody active: %+v, want no triage and no explainers", c)
	}
	if st := anomalyStates(t, db); len(st) != 1 || st[anomaly.Open] == 0 {
		t.Errorf("anomaly states %v, want all still open", st)
	}
}

// Nobody holds the narration skill: the explainers still get written, by
// whoever is active, rather than by nobody.
func TestExplainersFallBackToAnyActiveAuthor(t *testing.T) {
	db := seededStore(t)
	if _, err := db.Exec(`UPDATE analysts SET skills=replace(skills, 'showback-narration', 'x-narration')`); err != nil {
		t.Fatal(err)
	}
	var c Counts
	if err := seedExplainers(db, &c); err != nil {
		t.Fatal(err)
	}
	if c.Explainers != len(explainerTopics) {
		t.Fatalf("%d explainers, want %d", c.Explainers, len(explainerTopics))
	}
	exps, err := crew.Explainers(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range exps {
		if e.Author == "" {
			t.Errorf("explainer %q has no author", e.Topic)
		}
	}
}

func TestPickIsDeterministicAndInRange(t *testing.T) {
	if pick("anything", 0) != 0 || pick("anything", -3) != 0 {
		t.Error("pick over nothing must be 0, not a panic or a negative index")
	}
	for _, s := range []string{"a", "b", "A-1|who", "A-2|open"} {
		got := pick(s, 7)
		if got < 0 || got >= 7 || got != pick(s, 7) {
			t.Errorf("pick(%q, 7) = %d, not a stable index under 7", s, got)
		}
	}
}

func TestNowIsTodayInUTC(t *testing.T) {
	if got, want := Now(), time.Now().UTC().Format("2006-01-02"); got != want {
		t.Errorf("Now() = %q, want %q", got, want)
	}
}

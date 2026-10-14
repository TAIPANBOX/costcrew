package finops_test

// Invariant 102: the calls and spend read through each gateway door, per month
// and per run. The main folder holds the runs fixture; the OpenAI-wire door's
// folder holds three calls, two of them in w2-b12-A, which therefore came
// through both doors.

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

func importTwoDoors(t *testing.T) *sql.DB {
	t.Helper()
	db := bareDB(t)
	dirs := map[string]string{}
	for in, name := range map[string]string{"path": exportRuns,
		connectors.OpenAIPathInput: "tokenfuse-focus-openai-door-2026-10-14.csv"} {
		data, err := os.ReadFile(filepath.Join("..", "connectors", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		dirs[in] = dir
	}
	if err := connectors.Save(db, "tokenfuse-focus", dirs); err != nil {
		t.Fatal(err)
	}
	if _, err := connectors.Import(db, "tokenfuse-focus", false, connectors.ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestARunThroughTwoDoorsNamesBoth(t *testing.T) {
	db := importTwoDoors(t)
	runs, err := finops.AIRuns(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]finops.RunRow{}
	for _, r := range runs {
		by[r.RunID] = r
	}
	a := by["w2-b12-A"]
	type d struct {
		label string
		calls int
		cost  money.Micros
	}
	var got []d
	for _, x := range a.Doors {
		got = append(got, d{x.Label(), x.Calls, x.Cost})
	}
	want := []d{{"main gateway", 5, 50_000}, {"OpenAI-wire door", 2, 6_000}}
	if !reflect.DeepEqual(got, want) || !a.CrossedDoors() {
		t.Errorf("w2-b12-A's doors:\n got %+v\nwant %+v", got, want)
	}
	if !reflect.DeepEqual(func() []string {
		var m []string
		for _, x := range a.Models {
			m = append(m, x.Model+" "+x.Share())
		}
		return m
	}(), []string{"claude-opus-5-5 85%", "claude-haiku-5-5 3.5%", "openai/gpt-4.1-mini 10%"}) {
		t.Errorf("w2-b12-A's models across both doors: %+v", a.Models)
	}
	if one := by["w2-b12-D"]; one.CrossedDoors() || len(one.Doors) != 1 || one.Doors[0].Label() != "OpenAI-wire door" {
		t.Errorf("a run through one door reads as %+v", one.Doors)
	}
	if s := finops.SummarizeRuns(runs); s.Crossed != 1 {
		t.Errorf("%d runs counted as crossing doors, want 1", s.Crossed)
	}

	doors, err := finops.AIDoors(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	var all []d
	for _, x := range doors {
		all = append(all, d{x.Label(), x.Calls, x.Cost})
	}
	if want := []d{{"main gateway", 15, 78_000}, {"OpenAI-wire door", 3, 7_084}}; !reflect.DeepEqual(all, want) {
		t.Errorf("the month by door:\n got %+v\nwant %+v", all, want)
	}
}

func TestARowFromBeforeTheDoorColumnIsReadAsTheMainFolders(t *testing.T) {
	db := bareDB(t)
	importExports(t, db, exportRuns)
	if _, err := db.Exec(`UPDATE ai_calls SET door=NULL`); err != nil {
		t.Fatal(err)
	}
	doors, err := finops.AIDoors(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(doors) != 1 || doors[0].Label() != "main gateway" || doors[0].Calls != 15 {
		t.Errorf("rows with no door read as %+v, want one source, the main gateway's 15", doors)
	}
	if got := finops.DoorLabel("some-other-door"); got != "some-other-door" {
		t.Errorf("an unknown door is relabelled %q", got)
	}
	db.Close()
	if _, err := finops.AIDoors(db, "2026-10"); err == nil {
		t.Error("AIDoors read a closed store without an error")
	}
	if _, err := finops.AIRuns(db, "2026-10"); err == nil {
		t.Error("AIRuns read a closed store without an error")
	}
}

package crew_test

import (
	"testing"

	"database/sql"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/detect"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// A suspended analyst must not be assignable.
//
// The dropdown on the task page is built from ActiveNames, so a person
// clicking through the console never sees a suspended name. That is the UI.
// This asks the separate question of whether the code behind it refuses, which
// is what decides whether suspension is a control or a filter.
func seededBoard(t *testing.T) (*sql.DB, string) {
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
		seeds = append(seeds, crew.AnomalySeed{
			ID: a.ID, Source: a.Source, Service: a.Service,
			Day: a.Day, Direction: a.Direction, Excess: a.Excess,
		})
	}
	if _, _, _, err := crew.Seed(db, seeds); err != nil {
		t.Fatal(err)
	}
	if _, err := crew.SeedRoster(db, "yurii"); err != nil {
		t.Fatal(err)
	}
	names, err := crew.ActiveNames(db)
	if err != nil || len(names) == 0 {
		t.Fatalf("no roster to test with: %v", err)
	}
	return db, names[0]
}

func TestAssignRefusesASuspendedAnalyst(t *testing.T) {
	db, victim := seededBoard(t)
	if err := crew.SetState(db, victim, "suspended", "testing the seam"); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	tasks, err3 := crew.Tasks(db, crew.TaskFilter{OpenOnly: true})
	if err3 != nil || len(tasks) == 0 {
		t.Fatalf("no open task: %v", err3)
	}
	id := tasks[0].ID
	if err := crew.Assign(db, id, victim); err == nil {
		got, _ := crew.GetTask(db, id)
		t.Fatalf("Assign accepted a SUSPENDED analyst %q; task %d now assignee=%q state=%q",
			victim, id, got.Assignee, got.State)
	}

	// An analyst on probation is narrower authority, not withdrawn authority,
	// and one that could be given no work could never come off probation.
	if err := crew.SetState(db, victim, "probation", "first-pass rate under the bar"); err != nil {
		t.Fatalf("probation: %v", err)
	}
	if err := crew.Assign(db, id, victim); err != nil {
		t.Fatalf("Assign refused an analyst on PROBATION: %v", err)
	}
}

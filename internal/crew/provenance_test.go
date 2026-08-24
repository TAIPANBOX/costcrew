package crew_test

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// A generated draft and a real one must not read the same.
//
// Red first: with the runner inserting no source, a live draft came back
// "fixture", which is the whole fault. The column defaults to fixture on
// purpose, so the only thing that can make this test pass is the writer
// SAYING what it wrote.
func TestALiveDeliverableSaysItIsLive(t *testing.T) {
	db, _ := withRoster(t)
	if err := crew.EnsureArtifactProvenance(db); err != nil {
		t.Fatal(err)
	}
	if err := seedSomeWork(db); err != nil {
		t.Fatal(err)
	}
	var task int
	if err := db.QueryRow(`SELECT id FROM tasks LIMIT 1`).Scan(&task); err != nil {
		t.Fatal(err)
	}

	// Written the way the seed writes: no source named at all.
	if _, err := db.Exec(`INSERT INTO artifacts (task, author, title, body, state)
		VALUES (?,?,?,?,'draft')`, task, "a.seeded", "generated", "body"); err != nil {
		t.Fatal(err)
	}
	// Written the way tools/run writes after a real call.
	if _, err := db.Exec(`INSERT INTO artifacts (task, author, title, body, state, source)
		VALUES (?,?,?,?,'draft','live')`, task, "a.live", "real", "body"); err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	as, err := crew.Artifacts(db, task)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range as {
		got[a.Author] = a.Source
	}
	if got["a.seeded"] != "fixture" {
		t.Errorf("a generated draft reads %q, want fixture", got["a.seeded"])
	}
	if got["a.live"] != "live" {
		t.Errorf("a draft a model actually wrote reads %q, want live: "+
			"a real deliverable is indistinguishable from a generated one",
			got["a.live"])
	}
}

// Running twice must not fail on the column already being there.
func TestArtifactProvenanceIsSafeToRunAgain(t *testing.T) {
	db, _ := withRoster(t)
	for i := 0; i < 3; i++ {
		if err := crew.EnsureArtifactProvenance(db); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
}

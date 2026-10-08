package deliver

// Invariant 77: with typryx unset, the packet an analyst reads is byte for
// byte the packet it read before typryx existed. The golden file below was
// written by THIS test against the code on main before the hint was added
// (COSTCREW_UPDATE_GOLDEN=1), so it is a record of the old bytes, not a
// description of the new ones. Re-writing it is a decision to change what
// every analyst reads, and the diff of testdata/ is where that decision shows.

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/detect"
	"github.com/TAIPANBOX/costcrew/internal/estate"
)

const triagePacketsGolden = "testdata/triage_packets.golden"

// goldenTriagePackets builds the seeded estate the console builds on a first
// start (charges, detection, board), and renders the packet of the first
// eight anomaly tasks, in task-id order, for the analyst each is assigned to.
func goldenTriagePackets(t *testing.T, db *sql.DB) string {
	t.Helper()
	if _, err := estate.Seed(db); err != nil {
		t.Fatal(err)
	}
	// A fixed clock: detection stamps detected_at with it, and nothing in a
	// packet may depend on the day the test happened to run.
	if _, _, err := anomaly.Run(db, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), detect.Default(), nil); err != nil {
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
	if _, err := crew.SeedRoster(db, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := crew.Seed(db, seeds); err != nil {
		t.Fatal(err)
	}
	return renderTriagePackets(t, db)
}

// renderTriagePackets is the rendering half of goldenTriagePackets, over a
// store that is already seeded.
func renderTriagePackets(t *testing.T, db *sql.DB) string {
	t.Helper()
	roster, err := crew.Roster(db)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]crew.Analyst{}
	for _, a := range roster {
		by[a.Name] = a
	}
	tasks, err := crew.Tasks(db, crew.TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	n := 0
	for _, task := range tasks {
		if task.Anomaly == "" || by[task.Assignee].Name == "" {
			continue
		}
		fmt.Fprintf(&b, "=== task %d, %s, anomaly %s\n", task.ID, task.Assignee, task.Anomaly)
		b.WriteString(Packet(db, task, by[task.Assignee], false))
		b.WriteString("\n")
		n++
		if n == 8 {
			break
		}
	}
	if n < 3 {
		t.Fatalf("only %d anomaly tasks with an assignee on the seeded board: the golden would measure nothing", n)
	}
	return b.String()
}

func TestWithTypryxUnsetTheTriagePacketIsByteIdentical(t *testing.T) {
	got := goldenTriagePackets(t, deliverTestDB(t))
	if os.Getenv("COSTCREW_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(triagePacketsGolden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(triagePacketsGolden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", triagePacketsGolden, len(got))
		return
	}
	want, err := os.ReadFile(triagePacketsGolden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) || i < len(wl); i++ {
			var g, w string
			if i < len(gl) {
				g = gl[i]
			}
			if i < len(wl) {
				w = wl[i]
			}
			if g != w {
				t.Fatalf("the packet differs from the golden at line %d:\n got:  %q\n want: %q", i+1, g, w)
			}
		}
	}
}

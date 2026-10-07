package deliver

// The golden packet: what `-prompt-data full` (the default) sends, frozen
// BEFORE the policy existed. The mode is a switch on what leaves the process,
// and a switch that quietly changes its own default is the one regression no
// other test here would see, because every other test reads a section it
// planted itself. This one reads a whole packet built from the generated
// estate and compares it byte for byte with a file that was written from the
// code on main before the policy landed.
//
// To regenerate it on purpose (a section really did change): set
// COSTCREW_UPDATE_GOLDEN=1 for one run and read the diff in the commit.

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

const goldenPacketPath = "testdata/packet-full.golden"

// goldenPacketInputs builds the one packet the golden file holds: an anomaly
// on a real series of the generated estate, with a driver, a last posted
// explanation on the same service, the analyst's own history with a fate of
// each kind, the desk's month and the forecast. Nothing in it reads the
// clock, so it is the same bytes on any day.
func goldenPacketInputs(t *testing.T) (*sql.DB, crew.Task, crew.Analyst) {
	t.Helper()
	db := deliverTestDB(t)
	if _, err := estate.Seed(db); err != nil {
		t.Fatal(err)
	}
	if err := estate.SeedBudgets(db); err != nil {
		t.Fatal(err)
	}
	an := anomaly.Anomaly{
		ID: "A-gold01", Source: "gcp", Team: "research", Service: "GKE",
		Day: "2026-06-22", Direction: "up",
		Amount: money.Cents(1_200_00), Baseline: money.Cents(300_00),
		Excess: money.Cents(900_00), Z: 4.1, Rule: "z-score over 3.5",
		RuleVer: anomaly.RuleVersion, State: anomaly.Open, DetectedAt: "2026-06-23T00:00:00Z",
		Driver: "Quarterly model refresh, planned",
	}
	plantAnomaly(t, db, an)
	plantDriver(t, db, world.Driver{
		Start: an.Day, End: an.Day, Scope: an.Service,
		Label: an.Driver, Kind: "one-time", Source: an.Source,
	})
	taskID := plantFixtureDriverTask(t, db, an.ID, "gcp")

	// The last posted explanation on this service, by somebody else.
	plantPostedArtifact(t, db, taskID, "triage-gcp",
		"GKE rose because the research team ran the quarterly refresh on 40 nodes.",
		"2026-06-24T09:00:00Z")

	// The analyst's own history on the desk: three posted deliverables, one
	// option each, in three different fates.
	mem := plantMemoryTask(t, db, "gcp", "Explain the GKE move in May")
	a1 := plantPostedArtifact(t, db, mem, "investigator-gcp", "May was a batch backfill.", "2026-06-01T10:00:00Z")
	plantOption(t, db, a1, 1, "anomaly.explain", "batch backfill", crew.OptionApplied, "owner1", "")
	a2 := plantPostedArtifact(t, db, mem, "investigator-gcp", "April was a node pool resize.", "2026-05-02T10:00:00Z")
	plantOption(t, db, a2, 1, "anomaly.dismiss", "noise", crew.OptionRefused, "owner2", "it was not noise")
	a3 := plantPostedArtifact(t, db, mem, "investigator-gcp", "March was a one-off.", "2026-04-02T10:00:00Z")
	plantOption(t, db, a3, 1, "driver.one-time", "register it", crew.OptionNotChosen, "", "a sibling was chosen")

	task, err := crew.GetTask(db, taskID)
	if err != nil {
		t.Fatal(err)
	}
	a := crew.Analyst{Name: "investigator-gcp", Role: "Investigator (gcp desk)", Desk: "gcp",
		State: "active", Skills: []string{
			"driver-classification", "variance-commentary", "forecasting-commentary"}}
	return db, task, a
}

// TestFullModeBuildsTheSamePacketItAlwaysDid is the scenario "full is today's
// behaviour, unchanged": the default policy's packet equals, byte for byte,
// the file frozen from main before the policy existed.
func TestFullModeBuildsTheSamePacketItAlwaysDid(t *testing.T) {
	db, task, a := goldenPacketInputs(t)
	got := Packet(db, task, a, false)

	if os.Getenv("COSTCREW_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPacketPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPacketPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", goldenPacketPath, len(got))
		return
	}
	want, err := os.ReadFile(goldenPacketPath)
	if err != nil {
		t.Fatalf("the golden packet is missing (%v); it is the proof that full mode is unchanged", err)
	}
	if got != string(want) {
		t.Errorf("the full-mode packet is no longer the one main built.\n--- want (%d bytes)\n%s\n--- got (%d bytes)\n%s",
			len(want), want, len(got), got)
	}
	for _, header := range []string{
		"The anomaly", "The series", "Drivers on this service and desk",
		"The last posted explanation on this service", "The desk's month",
		"Forecasting", "What you posted on this desk before",
	} {
		if !strings.Contains(got, header) {
			t.Errorf("the golden packet does not exercise the %q section, so it would not notice that section changing", header)
		}
	}
}

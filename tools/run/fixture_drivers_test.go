package main

// The seeded fixture's drivers must reach the packet. Until 2026-09-02
// world.Drivers() filled Driver.Source with a provenance string ("planted
// fixture, event E02") while driversSection filters on Source == desk, so
// the "Drivers on this service and desk" section was empty for every
// generated-estate anomaly in every live run, silently. Nothing read the
// provenance text; the desk is what Source means everywhere else (the live
// apply path, internal/finops.applyDriver, writes t.Desk or an.Source).
// Found by Yurii reading the code, not by any test, which is why these two
// exist.

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// Every fixture driver's Source is one of the fixture's own desks: the same
// field the live apply path fills with a desk, read by the same filter.
func TestEveryFixtureDriverCarriesItsDesk(t *testing.T) {
	desks := map[string]bool{}
	for _, e := range world.Planted {
		desks[e.Source] = true
	}
	drivers := world.Drivers()
	if len(drivers) == 0 {
		t.Fatal("the fixture plants no drivers at all; this test measures nothing")
	}
	for _, d := range drivers {
		if !desks[d.Source] {
			t.Errorf("fixture driver %q on %s carries Source %q, which is no desk of the fixture (%v)",
				d.Label, d.Scope, d.Source, keys(desks))
		}
	}
}

// E02 is the fixture's own example of a real, large, explained move: GKE on
// the gcp desk on 2026-06-22, driver "Quarterly model refresh, planned". An
// investigator's packet for that anomaly must carry that driver, through the
// same driversSection and packet() the live runner uses.
func TestTheSeededFixtureDriversReachThePacket(t *testing.T) {
	db := packetTestDB(t)
	if _, err := estate.Seed(db); err != nil {
		t.Fatal(err)
	}
	an := anomaly.Anomaly{
		ID: "A-e02", Source: "gcp", Team: "research", Service: "GKE",
		Day: "2026-06-22", Direction: "up",
		Amount: money.Cents(1_200_00), Baseline: money.Cents(300_00),
		Excess: money.Cents(96_500), Z: 4.1, Rule: "z-score over 3.5",
		RuleVer: anomaly.RuleVersion, State: anomaly.Open, DetectedAt: "2026-06-23T00:00:00Z",
	}
	plantAnomaly(t, db, an)

	section := driversSection(db, an, "gcp")
	if !strings.Contains(section, "Quarterly model refresh, planned") {
		t.Fatalf("driversSection for E02 on the gcp desk does not name the planted driver; got %q", section)
	}

	task, err := crew.GetTask(db, plantAnomalyTask(t, db, an.ID, "gcp"))
	if err != nil {
		t.Fatal(err)
	}
	a := crew.Analyst{Name: "investigator-gcp", Desk: "gcp", State: "active",
		Skills: []string{"driver-classification"}}
	got := packet(db, task, a)
	for _, want := range []string{"Drivers on this service and desk", "Quarterly model refresh, planned"} {
		if !strings.Contains(got, want) {
			t.Errorf("the packet for E02 is missing %q", want)
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

package deliver

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

func hintPacketFixture(t *testing.T) (crew.Task, crew.Analyst, func(hide bool) string, func(anomaly.Hint)) {
	t.Helper()
	db := deliverTestDB(t)
	an := anomaly.Anomaly{ID: "A-hint", Source: "aws", Team: "payments", Service: "Amazon EC2",
		Day: "2026-07-14", Direction: "up", Amount: money.Cents(1000_00), Baseline: money.Cents(300_00),
		Excess: money.Cents(700_00), Z: 6.2, RuleVer: anomaly.RuleVersion, State: anomaly.Open,
		DetectedAt: "2026-07-15T00:00:00Z"}
	plantAnomaly(t, db, an)
	task, err := crew.GetTask(db, plantFixtureDriverTask(t, db, an.ID, "aws"))
	if err != nil {
		t.Fatal(err)
	}
	a := crew.Analyst{Name: "triage-aws", Desk: "aws", State: "active",
		Skills: []string{"anomaly-triage", "driver-classification"}}
	save := func(h anomaly.Hint) {
		if err := anomaly.EnsureHintColumns(db); err != nil {
			t.Fatal(err)
		}
		if err := anomaly.SaveHint(db, an.ID, h); err != nil {
			t.Fatal(err)
		}
	}
	return task, a, func(hide bool) string { return Packet(db, task, a, hide) }, save
}

// The analyst reads the hint, labelled as a suggestion it may disagree with,
// with the backend that produced it.
func TestTheTriagePacketCarriesTheHintAsASuggestion(t *testing.T) {
	_, _, packet, save := hintPacketFixture(t)
	if strings.Contains(packet(false), "typed hint") {
		t.Fatal("a packet carries a hint before one was ever recorded")
	}
	save(anomaly.Hint{Class: "runaway_agent", Probability: 0.874, Backend: anomaly.BackendJev,
		Model: "jev-1.13.0", AnswerID: "ans-1"})
	got := packet(false)
	for _, want := range []string{
		"A typed hint (a suggestion, not a finding)",
		"typryx suggests: runaway_agent, probability 0.87",
		"source: Jev, hosted by TypeSafe AI", "model jev-1.13.0",
		"You may disagree with it", "Nothing was decided because of it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the packet is missing %q:\n%s", want, got)
		}
	}
	if i, j := strings.Index(got, "The anomaly\n"), strings.Index(got, "A typed hint"); i < 0 || j < i {
		t.Errorf("the hint comes before the anomaly it is about")
	}
}

func TestANoHintReasonReachesThePacket(t *testing.T) {
	_, _, packet, save := hintPacketFixture(t)
	save(anomaly.Hint{Reason: "typryx did not answer within 5s"})
	got := packet(false)
	if !strings.Contains(got, "typryx gave no hint: typryx did not answer within 5s") {
		t.Errorf("the packet does not say why there is no hint:\n%s", got)
	}
	if strings.Contains(got, "suggests") {
		t.Error("a failure reads as a suggestion")
	}
}

// The bench hides the driver; the hint was asked with the change registry in
// its state, so it is hidden too.
func TestTheBenchHidingPacketCarriesNoHint(t *testing.T) {
	_, _, packet, save := hintPacketFixture(t)
	save(anomaly.Hint{Class: "misconfiguration", Probability: 0.7, Backend: anomaly.BackendOwnModel, AnswerID: "a"})
	if got := packet(true); strings.Contains(got, "typed hint") || strings.Contains(got, "misconfiguration") {
		t.Errorf("a hiding packet carries the hint:\n%s", got)
	}
	if got := packet(false); !strings.Contains(got, "misconfiguration") {
		t.Error("the ordinary packet lost the hint; this test measures nothing")
	}
}

// The migration alone, with no hint recorded, changes no byte of a packet.
func TestTheMigrationAloneChangesNoPacket(t *testing.T) {
	db := deliverTestDB(t)
	before := goldenTriagePackets(t, db)
	if err := anomaly.EnsureHintColumns(db); err != nil {
		t.Fatal(err)
	}
	if after := renderTriagePackets(t, db); after != before {
		t.Error("adding the hint columns, with no hint recorded, changed a packet")
	}
}

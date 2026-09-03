package deliver

// The bench's one property, tested where Packet now lives.
//
// Red first, against the code on main before B7: packet() in
// tools/run/packet.go took no hiding argument at all and printed the
// driver's label and its kind unconditionally, so this test does not even
// compile against that builder ("too many arguments in call to packet").
// Verified logically red too: with hideDriver forced to false inside
// Packet (simulating exactly what "no such parameter" amounts to),
// TestBenchPacketHidesTheDriverLabelAndItsKind fails with "a bench packet
// still carries the word of the kind" and prints the unredacted "Drivers on
// this service and desk... (one-time)" section -- see the PR body for the
// verbatim output.

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// B7-SPEC.md section 5's named property: "the bench packet contains neither
// the label nor the word of the kind".
func TestBenchPacketHidesTheDriverLabelAndItsKind(t *testing.T) {
	db := deliverTestDB(t)
	an := anomaly.Anomaly{
		ID: "A-hide1", Source: "gcp", Team: "research", Service: "GKE",
		Day: "2026-06-22", Direction: "up",
		Amount: money.Cents(96_500), Baseline: money.Cents(20_000),
		Excess: money.Cents(76_500), Z: 4.1, Rule: "z-score over 3.5",
		RuleVer: anomaly.RuleVersion, State: anomaly.Open, DetectedAt: "2026-06-23T00:00:00Z",
		Driver: "Quarterly model refresh, planned",
	}
	plantAnomaly(t, db, an)
	// Source is the DESK here, not a provenance string: driversSection's own
	// filter is `d.Source != desk`, the convention internal/finops/apply.go's
	// applyDriver already follows for a live-applied driver. world.Drivers()
	// itself gets this wrong for the SEEDED fixture (a separate, pre-existing
	// bug, out of scope here and flagged separately), which is exactly why
	// this test builds its own driver row the way the filter actually reads,
	// rather than the way the seeder happens to write it today.
	plantDriver(t, db, world.Driver{
		Start: an.Day, End: an.Day, Scope: an.Service,
		Label: an.Driver, Kind: "one-time", Source: an.Source,
	})
	task := crew.Task{ID: 1, Anomaly: an.ID, Desk: an.Source}
	a := crew.Analyst{Name: "triage-gcp", State: "active", Skills: []string{"anomaly-triage"}}

	hidden := Packet(db, task, a, true)
	if strings.Contains(hidden, an.Driver) {
		t.Errorf("a bench packet (hideDriver=true) still names the driver label %q:\n%s",
			an.Driver, hidden)
	}
	for _, word := range []string{"one-time", "recurring"} {
		if strings.Contains(hidden, word) {
			t.Errorf("a bench packet still carries the word of the kind (%q):\n%s", word, hidden)
		}
	}
	if strings.Contains(hidden, "Drivers on this service and desk") {
		t.Errorf("a bench packet still carries the whole drivers section:\n%s", hidden)
	}

	// And a bench packet is not simply broken: everything else the
	// production packet shows is still there.
	if !strings.Contains(hidden, an.Excess.String()) {
		t.Errorf("hiding the driver also hid the excess, which it must not:\n%s", hidden)
	}
	if !strings.Contains(hidden, an.Service) {
		t.Errorf("hiding the driver also hid the service, which it must not:\n%s", hidden)
	}

	// Production, hideDriver=false, is untouched: the boolean actually
	// toggles the behaviour rather than the section having quietly broken.
	shown := Packet(db, task, a, false)
	if !strings.Contains(shown, an.Driver) {
		t.Fatalf("production's own packet (hideDriver=false) no longer names the driver: %q", shown)
	}
	if !strings.Contains(shown, "one-time") {
		t.Fatalf("production's own packet (hideDriver=false) no longer names the kind: %q", shown)
	}
}

// The other real known case (E04: onprem, Batch cluster, a fall rather than
// a rise), so the property is proven on both anomalies the fixture can
// actually produce a driver label for, not only the gcp one above.
func TestBenchPacketHidesTheDriverOnTheOtherKnownCase(t *testing.T) {
	db := deliverTestDB(t)
	an := anomaly.Anomaly{
		ID: "A-hide2", Source: "onprem", Team: "data-eng", Service: "Batch cluster",
		Day: "2026-07-02", Direction: "down",
		Amount: money.Cents(4_000), Baseline: money.Cents(12_000),
		Excess: money.Cents(-8_000), Z: -4.0, Rule: "z-score over 3.5",
		RuleVer: anomaly.RuleVersion, State: anomaly.Open, DetectedAt: "2026-07-03T00:00:00Z",
		Driver: "Batch cluster decommission, tranche 1",
	}
	plantAnomaly(t, db, an)
	plantDriver(t, db, world.Driver{
		Start: an.Day, End: an.Day, Scope: an.Service,
		Label: an.Driver, Kind: "one-time", Source: an.Source,
	})
	task := crew.Task{ID: 2, Anomaly: an.ID, Desk: an.Source}
	a := crew.Analyst{Name: "investigator-onprem", State: "active", Skills: []string{"anomaly-triage"}}

	// The unhidden form really does carry the driver, or this test would prove
	// nothing by hiding it.
	if always := AnomalySection(an, false); !strings.Contains(always, an.Driver) {
		t.Fatalf("this test's own fixture does not carry the driver in its "+
			"unhidden form, so it cannot prove anything: %q", always)
	}
	hidden := Packet(db, task, a, true)
	if hidden == "" {
		t.Fatal("the fixture produced no packet at all; this test's own setup is broken")
	}
	if strings.Contains(hidden, an.Driver) {
		t.Errorf("Packet(hideDriver=true) still names the driver: %q", an.Driver)
	}
	if strings.Contains(hidden, "one-time") {
		t.Errorf("Packet(hideDriver=true) still names the kind:\n%s", hidden)
	}
}

func deliverTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.DB()
	for _, schema := range []string{crew.Schema, estate.SeedSchema, anomaly.Schema} {
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := crew.EnsureArtifactProvenance(db); err != nil {
		t.Fatal(err)
	}
	if err := crew.EnsureLiveSpendLedger(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func plantAnomaly(t *testing.T, db *sql.DB, an anomaly.Anomaly) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO anomalies
		(id, source, team, service, day, direction, amount_cents, baseline_cents,
		 excess_cents, z, rule, rule_version, driver, caused_by, caused_by_kind,
		 handled_by, state, reason, detected_at, closed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		an.ID, an.Source, an.Team, an.Service, an.Day, an.Direction,
		int64(an.Amount), int64(an.Baseline), int64(an.Excess), an.Z,
		an.Rule, an.RuleVer, an.Driver, "", "", "", string(an.State), "", an.DetectedAt, nil); err != nil {
		t.Fatal(err)
	}
}

func plantDriver(t *testing.T, db *sql.DB, d world.Driver) {
	t.Helper()
	if err := estate.InsertDriver(db, d); err != nil {
		t.Fatal(err)
	}
}

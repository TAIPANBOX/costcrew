package deliver

// C4-SPEC.md section 2's packet section: commitmentsSection, for the
// commitments role, with coverage, utilisation, the expiry calendar and
// break-even, top ten candidates with "and N more".
//
// Red first, against main before this step: Packet() had no path that ever
// mentioned a commitment at all, so an analyst with commitment-modelling
// or waterline-tracking (world.go's own "commitments" agent) always got an
// empty packet on a task with no anomaly -- TestCommitmentsSectionAppears
// failed with "the packet has no Commitments section at all" before this
// function existed, quoted verbatim in the PR body.

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/crew"
)

func commitmentsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := deliverTestDB(t)
	if err := connectors.EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func plantRealCharge(t *testing.T, db *sql.DB, source, day, service string, cents int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO charges
		(source, day, service, category, billed_cents, provenance)
		VALUES (?,?,?,'Usage',?,'tokenfuse-focus')`, source, day, service, cents); err != nil {
		t.Fatal(err)
	}
}

func plantCommitment(t *testing.T, db *sql.DB, id, kind, status, source, start, end string, qty float64, monthlyCents int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO commitments
		(id, kind, status, quantity, unit, source, date_start, date_end, monthly_cents)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		id, kind, status, qty, "hours", source, start, end, monthlyCents); err != nil {
		t.Fatal(err)
	}
}

func commitmentsAnalyst() crew.Analyst {
	return crew.Analyst{Name: "commitments", State: "active",
		Skills: []string{"commitment-modelling", "waterline-tracking"}}
}

// TestCommitmentsSectionAppears is the primary case: coverage, utilisation
// and break-even for a real commitment all reach the packet.
func TestCommitmentsSectionAppears(t *testing.T) {
	db := commitmentsTestDB(t)
	plantRealCharge(t, db, "ai", "2026-09-01", "Anthropic API", 120000)
	plantRealCharge(t, db, "ai", "2026-09-15", "Anthropic API", 80000)
	plantCommitment(t, db, "cud-1", "cud", "Used", "ai", "2026-01-01", "2027-01-01", 700, 150000)

	task := crew.Task{ID: 1, Desk: "management"}
	got := Packet(db, task, commitmentsAnalyst(), false)

	if !strings.Contains(got, "Commitments") {
		t.Fatalf("the packet has no Commitments section at all:\n%s", got)
	}
	if !strings.Contains(got, "ai") {
		t.Errorf("the packet does not name the ai desk:\n%s", got)
	}
	if !strings.Contains(got, "75.0%") {
		t.Errorf("the packet does not carry the desk's own coverage (75.0%%):\n%s", got)
	}
	if !strings.Contains(got, "cud-1") {
		t.Errorf("the packet does not name the commitment:\n%s", got)
	}
	if !strings.Contains(got, "3 month") {
		t.Errorf("the packet does not carry the break-even in months (3):\n%s", got)
	}
}

// TestCommitmentsSectionListsAnExpiryWithinNinetyDays exercises the
// calendar half of the section.
func TestCommitmentsSectionListsAnExpiryWithinNinetyDays(t *testing.T) {
	db := commitmentsTestDB(t)
	plantRealCharge(t, db, "ai", "2026-09-02", "Anthropic API", 50000)
	plantCommitment(t, db, "expiring-soon", "cud", "Used", "ai",
		"2026-01-01", "2026-10-01", 700, 40000) // ~29 days after AsOfDay
	plantCommitment(t, db, "expiring-later", "cud", "Used", "ai",
		"2026-01-01", "2028-01-01", 700, 40000)

	task := crew.Task{ID: 1, Desk: "management"}
	got := Packet(db, task, commitmentsAnalyst(), false)

	if !strings.Contains(got, "Expiring") {
		t.Fatalf("the packet has no expiry calendar section:\n%s", got)
	}
	calendar := calendarBlock(t, got)
	if !strings.Contains(calendar, "expiring-soon") {
		t.Errorf("the packet's calendar does not name the commitment expiring within 90 days:\n%s", calendar)
	}
	if strings.Contains(calendar, "expiring-later") {
		t.Errorf("the packet's calendar names a commitment expiring far beyond 90 days:\n%s", calendar)
	}
}

// calendarBlock isolates the "Expiring in the next 90 days" section from
// the rest of the packet: every other section (utilisation, buy-or-wait)
// legitimately names every real commitment regardless of when it expires,
// so a check for "is this id named anywhere" would fail on a healthy
// packet, not just a broken one.
func calendarBlock(t *testing.T, packet string) string {
	t.Helper()
	i := strings.Index(packet, "Expiring in the next 90 days")
	if i < 0 {
		t.Fatalf("no calendar heading in packet:\n%s", packet)
	}
	rest := packet[i:]
	if j := strings.Index(rest, "\n\n"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// TestCommitmentsSectionCapsCandidatesAtTenWithAndNMore is C4-SPEC.md
// section 2's own words: "top ten candidates with 'and N more'".
func TestCommitmentsSectionCapsCandidatesAtTenWithAndNMore(t *testing.T) {
	db := commitmentsTestDB(t)
	plantRealCharge(t, db, "ai", "2026-09-01", "Anthropic API", 100000)
	// Twelve commitments, each with a distinct, decreasing monthly price so
	// BreakEvens' own ranking (largest saving first) is unambiguous, and all
	// twelve save money against the shared 100000-cent eligible spend.
	for i := 0; i < 12; i++ {
		id := "c-" + string(rune('a'+i))
		monthly := int64(90000 - i*1000)
		plantCommitment(t, db, id, "cud", "Used", "ai", "2026-01-01", "2027-01-01", 700, monthly)
	}

	task := crew.Task{ID: 1, Desk: "management"}
	got := Packet(db, task, commitmentsAnalyst(), false)

	buyOrWait := buyOrWaitBlock(t, got)
	if !strings.Contains(buyOrWait, "and 2 more") {
		t.Errorf("the buy-or-wait list does not cap at ten with an \"and N more\" line "+
			"for the twelfth:\n%s", buyOrWait)
	}
	count := strings.Count(buyOrWait, "c-")
	// Each shown candidate line names its own id once; the truncation note
	// itself never says "c-", so this is exactly the shown-candidate count.
	if count != commitmentsCandidateCap {
		t.Errorf("the buy-or-wait list names %d candidate(s), want exactly %d shown",
			count, commitmentsCandidateCap)
	}
}

// buyOrWaitBlock isolates the "Buy or wait" section: utilisation lists
// every real commitment too, so a bare id search across the whole packet
// would count both.
func buyOrWaitBlock(t *testing.T, packet string) string {
	t.Helper()
	i := strings.Index(packet, "Buy or wait")
	if i < 0 {
		t.Fatalf("no buy-or-wait heading in packet:\n%s", packet)
	}
	return packet[i:]
}

// TestCommitmentsSectionIsAbsentWithNoRealData: a fresh store, nothing
// imported, produces no Commitments section at all -- additive, never
// misleading, the same rule every other section in packet.go already
// holds for a name no role family matches or a table with nothing in it.
func TestCommitmentsSectionIsAbsentWithNoRealData(t *testing.T) {
	db := commitmentsTestDB(t)
	task := crew.Task{ID: 1, Desk: "management"}
	got := Packet(db, task, commitmentsAnalyst(), false)
	if got != "" {
		t.Errorf("a commitments analyst's packet is not empty on a store with no real commitments "+
			"and no anomaly task:\n%s", got)
	}
}

// TestCommitmentsSectionIsSkippedForAnalystsWithoutTheSkill: an unrelated
// role never sees this section, even when real commitments exist.
func TestCommitmentsSectionIsSkippedForAnalystsWithoutTheSkill(t *testing.T) {
	db := commitmentsTestDB(t)
	plantRealCharge(t, db, "ai", "2026-09-01", "Anthropic API", 100000)
	plantCommitment(t, db, "cud-1", "cud", "Used", "ai", "2026-01-01", "2027-01-01", 700, 50000)

	task := crew.Task{ID: 1, Desk: "aws"}
	a := crew.Analyst{Name: "investigator-aws", State: "active", Skills: []string{"variance-commentary"}}
	got := Packet(db, task, a, false)
	if strings.Contains(got, "Commitments") {
		t.Errorf("an investigator's packet carries the commitments section it has no skill for:\n%s", got)
	}
}

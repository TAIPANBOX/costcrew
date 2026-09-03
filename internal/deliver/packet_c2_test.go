package deliver

// closePackSection: C2-SPEC.md section 2. "closePackSection for the
// chargeback role on a task whose title names a period: allocation by
// method and team (cents), coverage, unallocated with the rule ids that
// produced it, true-up since the last close, invoice reconciliation ... Yields
// before memory, after the anomaly." Red against unchanged code: today no
// such section exists at all, so a chargeback task's packet carries none of
// this.

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// closePackTestDB is a fully seeded estate (unlike deliverTestDB's bare
// schema in packet_test.go): closePackSection reads finops.Allocate,
// finops.Rules and friends, which need real charges and rules to say
// anything, the same way a live console's own estate would.
func closePackTestDB(t *testing.T) *sql.DB {
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
	if err := finops.SeedRules(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(crew.Schema); err != nil {
		t.Fatal(err)
	}
	return db
}

func aClosePackMonth(t *testing.T, db *sql.DB) string {
	t.Helper()
	ms, err := finops.Months(db)
	if err != nil || len(ms) < 2 {
		t.Fatalf("months: %v %v", ms, err)
	}
	return ms[1] // the newest is partial; the second newest is a whole month
}

var chargebackAnalyst = crew.Analyst{
	Name: "chargeback", Desk: "management", State: "active",
	Skills: []string{"allocation-rules", "period-close", "true-up"},
}

func TestClosePackSectionAppearsOnAChargebackTaskNamingAPeriod(t *testing.T) {
	db := closePackTestDB(t)
	period := aClosePackMonth(t, db)
	task := crew.Task{Title: "Close the books, " + period, Desk: "management"}

	got := Packet(db, task, chargebackAnalyst, false)
	if got == "" {
		t.Fatal("no packet at all for a chargeback task naming a period")
	}
	alloc, err := finops.Allocate(db, period)
	if err != nil {
		t.Fatal(err)
	}
	if len(alloc.Teams) == 0 {
		t.Fatal("the seeded month has no teams; this test cannot see the property")
	}
	first := alloc.Teams[0]
	if !strings.Contains(got, first.Team) {
		t.Errorf("packet does not name team %q:\n%s", first.Team, got)
	}
	if !strings.Contains(got, first.Loaded().String()) {
		t.Errorf("packet does not carry %s's loaded figure %s:\n%s", first.Team, first.Loaded(), got)
	}
	if !strings.Contains(got, alloc.Unallocated.String()) {
		t.Errorf("packet does not carry the unallocated total %s:\n%s", alloc.Unallocated, got)
	}
	// Coverage, to one place, is already in the Allocation and must appear
	// somewhere legible rather than only as a raw float.
	if !strings.Contains(got, "coverage") {
		t.Errorf("packet does not mention coverage:\n%s", got)
	}
}

func TestClosePackSectionAbsentWithoutAPeriodInTheTitle(t *testing.T) {
	db := closePackTestDB(t)
	task := crew.Task{Title: "Review the shared cost rules", Desk: "management"}

	got := closePackSection(db, chargebackAnalyst, task)
	if got != "" {
		t.Errorf("a title naming no period produced a close pack section:\n%s", got)
	}
}

func TestClosePackSectionAbsentForANonChargebackRole(t *testing.T) {
	db := closePackTestDB(t)
	period := aClosePackMonth(t, db)
	task := crew.Task{Title: "Investigate " + period, Desk: "aws"}
	other := crew.Analyst{Name: "investigator-aws", Desk: "aws", State: "active",
		Skills: []string{"anomaly-triage"}}

	got := closePackSection(db, other, task)
	if got != "" {
		t.Errorf("a non-chargeback role got a close pack section:\n%s", got)
	}
}

// A period nobody has ever closed: the true-up has nothing to compare
// against, and the section must say so honestly rather than print an empty
// table or, worse, nothing at all about why.
func TestClosePackSectionNamesNoPreviousClose(t *testing.T) {
	db := closePackTestDB(t)
	period := aClosePackMonth(t, db)
	if closed, _ := finops.IsClosed(db, period); closed {
		t.Fatal("this test needs a period nobody has closed yet")
	}
	task := crew.Task{Title: "Close the books, " + period, Desk: "management"}

	got := closePackSection(db, chargebackAnalyst, task)
	if !strings.Contains(got, "no previous close") {
		t.Errorf("a never-closed period's close pack does not say so:\n%s", got)
	}
}

func TestClosePackSectionSaysNoInvoiceColumnIsLoaded(t *testing.T) {
	db := closePackTestDB(t)
	period := aClosePackMonth(t, db)
	task := crew.Task{Title: "Close the books, " + period, Desk: "management"}

	got := closePackSection(db, chargebackAnalyst, task)
	if !strings.Contains(got, "no invoice column is loaded") {
		t.Errorf("the generated estate never sets invoice_id, but the close pack "+
			"does not say no invoice column is loaded:\n%s", got)
	}
}

func TestClosePackSectionReconcilesInvoicesWhenPresent(t *testing.T) {
	db := closePackTestDB(t)
	period := aClosePackMonth(t, db)
	day := period + "-08"
	if _, err := db.Exec(`INSERT INTO charges (source, day, service, category, billed_cents, invoice_id)
		VALUES ('aws', ?, 'EC2', 'Usage', 4321, 'INV-C2-1')`, day); err != nil {
		t.Fatal(err)
	}
	task := crew.Task{Title: "Close the books, " + period, Desk: "management"}

	got := closePackSection(db, chargebackAnalyst, task)
	if !strings.Contains(got, "INV-C2-1") {
		t.Errorf("close pack does not name the loaded invoice INV-C2-1:\n%s", got)
	}
	if strings.Contains(got, "no invoice column is loaded") {
		t.Errorf("close pack still says no invoice column is loaded once one is:\n%s", got)
	}
}

// The close pack is appended after the anomaly-related sections and before
// memory (ownHistorySection), so under the 12 KiB cap memory yields first,
// never the close pack.
func TestClosePackSectionComesBeforeMemoryInThePacket(t *testing.T) {
	db := closePackTestDB(t)
	period := aClosePackMonth(t, db)

	pastTaskID := plantMemoryTask(t, db, "management", "an earlier task")
	plantPostedArtifact(t, db, pastTaskID, "chargeback",
		"An earlier close pack.\n\n```options\n{\"options\": [{\"class\": \"commentary.showback\", "+
			"\"summary\": \"x\", \"figure_cents\": 0}]}\n```\n",
		"2026-08-01T00:00:00Z")

	task := crew.Task{Title: "Close the books, " + period, Desk: "management"}
	got := Packet(db, task, chargebackAnalyst, false)
	if got == "" {
		t.Fatal("empty packet")
	}
	closeIdx := strings.Index(got, "The close pack")
	memIdx := strings.Index(got, "What you posted on this desk before")
	if closeIdx < 0 {
		t.Fatalf("no close pack section in the packet:\n%s", got)
	}
	if memIdx < 0 {
		t.Fatalf("no memory section in the packet, so the ordering cannot be checked:\n%s", got)
	}
	if closeIdx > memIdx {
		t.Errorf("the close pack (at %d) comes AFTER memory (at %d): memory must yield first",
			closeIdx, memIdx)
	}
}

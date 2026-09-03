package deliver

// B8-SPEC.md: memory, in the store first. A person on this job remembers two
// things between tasks -- what they said last time, and what happened to it
// -- and today an analyst's packet remembers neither: only the service's
// last explanation by ANYBODY, and 90 days of drivers. This file's tests
// hold ownHistorySection (the analyst's own last three posted deliverables
// on the desk, each with the fate of its options) and driversSection's
// widened window (90 -> 180 days, capped at 24, "and N more").
//
// `@yurii 2026-09-02`, the ask this step serves: analysts that "більш
// повною мірою замінити людей на цих посадах."

import (
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// -------------------------------------------------------------- fixtures

// plantMemoryTask is a plain task with a desk and no anomaly: what an
// analyst's OWN past deliverables hang off, independent of any specific
// incident.
func plantMemoryTask(t *testing.T, db *sql.DB, desk, title string) int {
	t.Helper()
	res, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated)
		VALUES (?, 'explain it', '', ?, 'posted', 0, 0, datetime('now'), datetime('now'))`,
		title, desk)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return int(id)
}

// plantPostedArtifact plants a POSTED deliverable, stamped at a caller-given
// timestamp so ownHistorySection's "newest first" ordering is deterministic
// across several artifacts rather than a tie broken only by row id.
func plantPostedArtifact(t *testing.T, db *sql.DB, taskID int, author, body, stampedAt string) int {
	t.Helper()
	res, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created, stamped, stamper)
		VALUES (?, ?, 'a deliverable', ?, 'posted', ?, ?, 'owner1')`,
		taskID, author, body, stampedAt, stampedAt)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return int(id)
}

// plantOption writes one artifact_options row directly, in whatever state
// the test needs: ownHistorySection's fate rendering is exercised against
// artifact_options.state and decided_by/reason directly, not against the
// Mark* transition functions those columns' OWN tests already hold (B3).
func plantOption(t *testing.T, db *sql.DB, artifactID, ordinal int, class, summary string, state crew.OptionState, decidedBy, reason string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state, decided_by, reason)
		VALUES (?,?,?,?,0,0,'','','[]',?,?,?)`,
		artifactID, ordinal, class, summary, string(state), decidedBy, reason); err != nil {
		t.Fatal(err)
	}
}

// memoryAnalyst is any analyst with figures-read (every non-suspended
// analyst has it -- crew.RightsFor) and no anomaly-specific skill, so
// Packet() exercises ownHistorySection without an anomaly in play unless a
// test's own task carries one.
func memoryAnalyst(name string) crew.Analyst {
	return crew.Analyst{Name: name, State: "active", Skills: []string{"anomaly-triage"}}
}

// ------------------------------------------------------- the feature itself

// B8-SPEC.md section 4's first red-first property: an analyst's own last
// posted deliverable appears in its packet. Red on main before this step --
// Packet() had no ownHistorySection at all, and the packet for a plain task
// with no anomaly and no reporting/forecasting skill was simply "".
func TestOwnHistoryShowsTheAnalystsOwnLastPostedDeliverable(t *testing.T) {
	db := deliverTestDB(t)
	taskID := plantMemoryTask(t, db, "aws", "Explain the EC2 move")
	plantPostedArtifact(t, db, taskID, "investigator-aws",
		"The EC2 spend rose because a batch job ran long.", "2026-08-20T10:00:00Z")

	a := memoryAnalyst("investigator-aws")
	got := Packet(db, crew.Task{ID: 999, Desk: "aws"}, a, false)

	if !strings.Contains(got, "What you posted on this desk before, and what happened to it") {
		t.Fatalf("the packet does not carry the own-history header at all:\n%s", got)
	}
	if !strings.Contains(got, "Explain the EC2 move") {
		t.Errorf("the packet does not name the task of the analyst's own last posted deliverable:\n%s", got)
	}
	if !strings.Contains(got, "The EC2 spend rose because a batch job ran long.") {
		t.Errorf("the packet does not carry the body of the analyst's own last posted deliverable:\n%s", got)
	}
}

// The section is absent, not a header over nothing, when the analyst has
// never posted on this desk (B8-SPEC.md section 2's own words).
func TestOwnHistoryIsAbsentWhenTheAnalystHasNeverPostedHere(t *testing.T) {
	db := deliverTestDB(t)
	a := memoryAnalyst("investigator-aws")
	got := Packet(db, crew.Task{ID: 1, Desk: "aws"}, a, false)
	if strings.Contains(got, "What you posted on this desk before") {
		t.Errorf("a header appeared over nothing, for an analyst who has never posted here:\n%q", got)
	}
}

// B8-SPEC.md section 4's boundary: exactly three, newest first, and a
// fourth is not shown.
func TestOwnHistoryShowsExactlyThreeNewestFirst(t *testing.T) {
	db := deliverTestDB(t)
	taskID := plantMemoryTask(t, db, "aws", "Explain the move")
	stamps := []string{
		"2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z",
		"2026-08-03T00:00:00Z", "2026-08-04T00:00:00Z",
	}
	for i, ts := range stamps {
		plantPostedArtifact(t, db, taskID, "investigator-aws",
			"body of the "+ordinalWord(i)+" deliverable", ts)
	}

	a := memoryAnalyst("investigator-aws")
	got := Packet(db, crew.Task{ID: 1, Desk: "aws"}, a, false)

	for _, want := range []string{"second deliverable", "third deliverable", "fourth deliverable"} {
		if !strings.Contains(got, "body of the "+want) {
			t.Errorf("missing %q, one of the three newest:\n%s", want, got)
		}
	}
	if strings.Contains(got, "body of the first deliverable") {
		t.Errorf("a fourth (oldest, over the cap of three) deliverable was shown:\n%s", got)
	}
	// Newest first: the fourth (2026-08-04) precedes the third (2026-08-03)
	// precedes the second (2026-08-02) in the rendered text.
	idx4 := strings.Index(got, "fourth deliverable")
	idx3 := strings.Index(got, "third deliverable")
	idx2 := strings.Index(got, "second deliverable")
	if !(idx4 < idx3 && idx3 < idx2) {
		t.Errorf("history is not newest first: fourth@%d third@%d second@%d\n%s", idx4, idx3, idx2, got)
	}
}

func ordinalWord(i int) string {
	return [...]string{"first", "second", "third", "fourth"}[i]
}

// Mutant (a), B8-SPEC.md section 4: "show any author's deliverables."
// Another analyst's deliverable on the SAME desk must not appear.
func TestOwnHistoryHidesAnotherAnalystsDeliverableOnTheSameDesk(t *testing.T) {
	db := deliverTestDB(t)
	taskID := plantMemoryTask(t, db, "aws", "Explain the other move")
	plantPostedArtifact(t, db, taskID, "triage-aws",
		"a colleague's own explanation, never this analyst's", "2026-08-20T10:00:00Z")

	a := memoryAnalyst("investigator-aws")
	got := Packet(db, crew.Task{ID: 1, Desk: "aws"}, a, false)
	if strings.Contains(got, "a colleague's own explanation") {
		t.Errorf("another analyst's deliverable on the same desk was shown:\n%s", got)
	}
}

// The same analyst's deliverable on ANOTHER desk must not appear either:
// this is desk-scoped memory, not estate-wide memory.
func TestOwnHistoryHidesTheSameAnalystsDeliverableOnAnotherDesk(t *testing.T) {
	db := deliverTestDB(t)
	taskID := plantMemoryTask(t, db, "gcp", "Explain a gcp move")
	plantPostedArtifact(t, db, taskID, "investigator-aws",
		"an explanation this analyst wrote, but on the gcp desk", "2026-08-20T10:00:00Z")

	a := memoryAnalyst("investigator-aws")
	got := Packet(db, crew.Task{ID: 1, Desk: "aws"}, a, false)
	if strings.Contains(got, "but on the gcp desk") {
		t.Errorf("the same analyst's deliverable on a DIFFERENT desk was shown on the aws packet:\n%s", got)
	}
}

// -------------------------------------------------------------- the fates

// B8-SPEC.md section 4's other two named red-first properties ("applied by
// owner1", "not chosen"), plus the remaining three states (refused, carried,
// open) for full boundary coverage, and mutant (b)'s catcher: "drop the fate
// line" breaks every one of these five subtests at once.
func TestOwnHistoryShowsTheFateOfEveryOptionState(t *testing.T) {
	cases := []struct {
		name  string
		state crew.OptionState
		by    string
		want  string
		setup func(t *testing.T, db *sql.DB, artifactID int)
	}{
		{"open", crew.OptionOpen, "", "open", nil},
		{"applied", crew.OptionApplied, "owner1", "applied by owner1", nil},
		{"refused", crew.OptionRefused, "owner1", "refused by owner1: too risky this quarter", nil},
		{"not_chosen", crew.OptionNotChosen, "owner1", "not chosen (lost to the top-ranked option)", nil},
		{"carried", crew.OptionCarried, "supervisor", "still waiting on t.langley", func(t *testing.T, db *sql.DB, artifactID int) {
			taskID, err := crew.TaskOfArtifact(db, artifactID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE tasks SET owner=? WHERE id=?`, "t.langley", taskID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := deliverTestDB(t)
			taskID := plantMemoryTask(t, db, "aws", "Explain the "+c.name+" move")
			artID := plantPostedArtifact(t, db, taskID, "investigator-aws",
				"the explanation body", "2026-08-20T10:00:00Z")
			reason := ""
			switch c.state {
			case crew.OptionRefused:
				reason = "too risky this quarter"
			case crew.OptionNotChosen:
				reason = "lost to the top-ranked option"
			}
			plantOption(t, db, artID, 1, "anomaly.explain", "a scheduled batch job", c.state, c.by, reason)
			if c.setup != nil {
				c.setup(t, db, artID)
			}

			a := memoryAnalyst("investigator-aws")
			got := Packet(db, crew.Task{ID: 1, Desk: "aws"}, a, false)
			if !strings.Contains(got, c.want) {
				t.Errorf("fate %q not found for state %q:\n%s", c.want, c.state, got)
			}
		})
	}
}

// -------------------------------------------------------------- the drivers

// B8-SPEC.md section 4's second red-first property: a driver 120 days
// before the anomaly appears (today, at 90 days, it does not). Mutant (c)'s
// catcher: "keep 90 days."
func TestDriversSectionReachesOneHundredTwentyDays(t *testing.T) {
	db := deliverTestDB(t)
	an := anomaly.Anomaly{
		ID: "A-mem1", Source: "aws", Team: "ml-platform", Service: "EC2",
		Day: "2026-08-20", Direction: "up",
		Amount: money.Cents(50_000), Baseline: money.Cents(10_000),
		Excess: money.Cents(40_000), Z: 4.0, Rule: "z-score over 3.5",
		RuleVer: anomaly.RuleVersion, State: anomaly.Open, DetectedAt: "2026-08-21T00:00:00Z",
	}
	plantAnomaly(t, db, an)
	// 120 days before 2026-08-20 is 2026-04-22: outside the old 90-day
	// window, inside the new 180-day one.
	plantDriver(t, db, world.Driver{
		Start: "2026-04-20", End: "2026-04-22", Scope: an.Service,
		Label: "GPU fleet reserved instance purchase", Kind: "one-time", Source: an.Source,
	})

	got := driversSection(db, an, an.Source)
	if !strings.Contains(got, "GPU fleet reserved instance purchase") {
		t.Errorf("a driver 120 days before the anomaly is missing from driversSection:\n%q", got)
	}
	if !strings.Contains(got, "last six months") {
		t.Errorf("driversSection's header does not say the widened window:\n%q", got)
	}
}

// B8-SPEC.md section 4's boundary: the drivers cap at 24 with "and N more".
func TestDriversSectionCapsAtTwentyFourWithAndNMore(t *testing.T) {
	db := deliverTestDB(t)
	an := anomaly.Anomaly{
		ID: "A-mem2", Source: "aws", Team: "ml-platform", Service: "EC2",
		Day: "2026-08-20", Direction: "up",
		Amount: money.Cents(50_000), Baseline: money.Cents(10_000),
		Excess: money.Cents(40_000), Z: 4.0, Rule: "z-score over 3.5",
		RuleVer: anomaly.RuleVersion, State: anomaly.Open, DetectedAt: "2026-08-21T00:00:00Z",
	}
	plantAnomaly(t, db, an)
	// 30 drivers inside the window: only 24 (newest) are printed, plus a
	// trailer naming the other 6.
	days := []string{
		"07-01", "07-02", "07-03", "07-04", "07-05", "07-06", "07-07", "07-08",
		"07-09", "07-10", "07-11", "07-12", "07-13", "07-14", "07-15", "07-16",
		"07-17", "07-18", "07-19", "07-20", "07-21", "07-22", "07-23", "07-24",
		"07-25", "07-26", "07-27", "07-28", "07-29", "07-30",
	}
	for i, md := range days {
		day := "2026-" + md
		plantDriver(t, db, world.Driver{
			Start: day, End: day, Scope: an.Service,
			Label: "driver number " + strconv.Itoa(i), Kind: "one-time", Source: an.Source,
		})
	}

	got := driversSection(db, an, an.Source)
	if !strings.Contains(got, "and 6 more") {
		t.Errorf("the drivers section does not say 'and 6 more' for 30 matches capped at 24:\n%q", got)
	}
	shown := strings.Count(got, "driver number")
	if shown != 24 {
		t.Errorf("got %d driver rows printed, want exactly 24 (the cap)", shown)
	}
	// Newest first: the last-planted (07-30) must appear before the
	// 25th-newest (07-06), which is itself over the cap and absent.
	if !strings.Contains(got, "driver number 29") { // 0-indexed: day 07-30 is index 29
		t.Errorf("the newest driver (07-30) is missing, so the cap kept the wrong end:\n%q", got)
	}
	if strings.Contains(got, "driver number 0 ") { // 07-01 (i=0), the oldest, is over the cap
		t.Errorf("the oldest driver (07-01) was kept instead of cut:\n%q", got)
	}
}

// ------------------------------------------------ memory never crowds out evidence

// B8-SPEC.md section 4's fifth red-first property, and mutant (d)'s
// catcher ("trim the anomaly section instead of the history section"):
// under load, the packet stays at or under 12 KiB, the anomaly section is
// intact, and the history section -- appended last -- is the one trimmed.
//
// Task titles are not byte-bounded by the spec (only the body, at 240, and
// each option's summary/reason, at 80, are named), so this test uses
// generous-but-not-exotic titles for the three own-history artifacts to
// reliably push the packet over the cap without needing an unrealistic
// number of postings: three real analysts' deliverables with full option
// blocks is a completely ordinary desk, not a hostile input.
func TestOwnHistoryNeverCrowdsOutTheAnomalyUnderTheCap(t *testing.T) {
	db := deliverTestDB(t)
	an := anomaly.Anomaly{
		ID: "A-mem3", Source: "aws", Team: "ml-platform", Service: "EC2",
		Day: "2026-08-20", Direction: "up",
		Amount: money.Cents(500_000), Baseline: money.Cents(100_000),
		Excess: money.Cents(400_000), Z: 5.2, Rule: "z-score over 3.5",
		RuleVer: anomaly.RuleVersion, State: anomaly.Open, DetectedAt: "2026-08-21T00:00:00Z",
		Driver: "Quarterly model refresh, planned",
	}
	plantAnomaly(t, db, an)
	plantDriver(t, db, world.Driver{
		Start: an.Day, End: an.Day, Scope: an.Service,
		Label: an.Driver, Kind: "one-time", Source: an.Source,
	})

	taskID := plantMemoryTask(t, db, "aws", "Explain the EC2 move")
	longTitle := strings.Repeat("The very long, verbose, generated task title that a busy desk accumulates over a quarter of triage work ", 40)
	// The marker goes FIRST: trimBytes cuts each body to its first 240
	// bytes, so a marker at the END of a 2 KB body would never survive the
	// trim regardless of which artifact it belonged to, and could not tell
	// the test which one actually made it into the packet.
	markers := []string{"OLDEST-OF-THE-THREE", "MIDDLE-OF-THE-THREE", "NEWEST-OF-THE-THREE"}
	stamps := []string{"2026-08-17T00:00:00Z", "2026-08-18T00:00:00Z", "2026-08-19T00:00:00Z"}
	for i, ts := range stamps {
		body := markers[i] + " " + strings.Repeat("x", 2_048)
		artID := plantPostedArtifact(t, db, taskID, "investigator-aws", body, ts)
		for ord := 1; ord <= 3; ord++ {
			plantOption(t, db, artID, ord, "anomaly.explain",
				strings.Repeat("s", 80), crew.OptionRefused, "owner1", strings.Repeat("r", 80))
		}
	}
	// The task title is shared by the join (t.title): set it long on the
	// task row directly, once, rather than per artifact.
	if _, err := db.Exec(`UPDATE tasks SET title=? WHERE id=?`, longTitle, taskID); err != nil {
		t.Fatal(err)
	}

	task := crew.Task{ID: 1, Anomaly: an.ID, Desk: an.Source}
	a := crew.Analyst{Name: "investigator-aws", State: "active", Skills: []string{"anomaly-triage"}}
	got := Packet(db, task, a, false)

	if len(got) > packetMaxBytes {
		t.Fatalf("packet is %d bytes, over the %d byte cap", len(got), packetMaxBytes)
	}
	if len(got) < 8192 {
		t.Fatalf("this test's own fixture did not push the packet anywhere near the cap (got only %d bytes); it proves nothing about trimming", len(got))
	}
	// The anomaly section is intact: every field AnomalySection prints is
	// still present, whole.
	for _, want := range []string{"The anomaly", an.Service, an.Day, an.Excess.String(), an.Driver} {
		if !strings.Contains(got, want) {
			t.Errorf("the anomaly section is not intact, missing %q (%d bytes total)", want, len(got))
		}
	}
	// The history section is the one trimmed: newest first means the newest
	// artifact renders closest to the section's own start and survives, but
	// the oldest of the three (rendered last, furthest from the front of
	// the whole packet) is cut, and the packet ends in the truncation note
	// rather than a clean close.
	if !strings.Contains(got, "What you posted on this desk before") {
		t.Errorf("the history section did not even start (%d bytes total)", len(got))
	}
	if !strings.Contains(got, "NEWEST-OF-THE-THREE") {
		t.Errorf("the newest of the three history entries did not survive, though it renders first (%d bytes total)", len(got))
	}
	if strings.Contains(got, "OLDEST-OF-THE-THREE") {
		t.Errorf("the oldest of the three history entries survived intact, so nothing was actually trimmed from history (%d bytes total)", len(got))
	}
	if !strings.HasSuffix(got, truncatedNote) {
		t.Errorf("the packet does not end in the truncation note, so nothing was actually cut:\n...%s", got[len(got)-min(200, len(got)):])
	}
}

// ----------------------------------------------------------------- hostile

// B8-SPEC.md section 4's first hostile input: a 1 MB body, trimmed to 240.
func TestOwnHistoryTrimsAOneMegabyteBody(t *testing.T) {
	db := deliverTestDB(t)
	taskID := plantMemoryTask(t, db, "aws", "Explain the move")
	huge := strings.Repeat("z", 1_100_000)
	plantPostedArtifact(t, db, taskID, "investigator-aws", huge, "2026-08-20T10:00:00Z")

	a := memoryAnalyst("investigator-aws")
	got := Packet(db, crew.Task{ID: 1, Desk: "aws"}, a, false)
	if strings.Contains(got, strings.Repeat("z", 300)) {
		t.Errorf("a 1 MB body was not trimmed: 300 consecutive z's survived into the packet")
	}
	if !strings.Contains(got, strings.Repeat("z", 240)+"…") {
		t.Errorf("the body was not trimmed to exactly 240 bytes with the trimBytes ellipsis marker")
	}
}

// B8-SPEC.md section 4's second hostile input: an option summary with a
// script tag. The packet is plain text sent to a model, not HTML, so it must
// carry the tag as literal text -- unlike internal/web's rendering test for
// the SAME hostile input (TestAScriptTagInAnOptionSummaryRendersAsText),
// which is about escaping for a browser. This is that test's neighbour for
// the packet path.
func TestOwnHistoryCarriesAScriptTagAsPlainText(t *testing.T) {
	db := deliverTestDB(t)
	taskID := plantMemoryTask(t, db, "aws", "Explain the move")
	artID := plantPostedArtifact(t, db, taskID, "investigator-aws", "the body", "2026-08-20T10:00:00Z")
	tag := `<script>alert(1)</script>`
	plantOption(t, db, artID, 1, "anomaly.explain", tag, crew.OptionApplied, "owner1", "")

	a := memoryAnalyst("investigator-aws")
	got := Packet(db, crew.Task{ID: 1, Desk: "aws"}, a, false)
	if !strings.Contains(got, tag) {
		t.Errorf("the script tag was not carried as plain text into the packet:\n%s", got)
	}
	if strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("the packet HTML-escaped the option summary, which a plain-text prompt must never do:\n%s", got)
	}
	if !strings.Contains(got, "applied by owner1") {
		t.Errorf("the rest of the option's fate did not survive alongside the script tag:\n%s", got)
	}
}

// B8-SPEC.md section 4's third hostile input: an analyst name with a quote
// in it, proving the query is parameterised (not string-built SQL).
func TestOwnHistoryQueryIsParameterisedAgainstAQuoteInTheName(t *testing.T) {
	db := deliverTestDB(t)
	name := `investigator's-aws`
	taskID := plantMemoryTask(t, db, "aws", "Explain the move")
	plantPostedArtifact(t, db, taskID, name, "an explanation from an analyst whose name has a quote", "2026-08-20T10:00:00Z")

	a := memoryAnalyst(name)
	got := Packet(db, crew.Task{ID: 1, Desk: "aws"}, a, false)
	if !strings.Contains(got, "an explanation from an analyst whose name has a quote") {
		t.Errorf("a name with a quote in it broke the query or the match, rather than being bound as a parameter:\n%s", got)
	}
}

// -------------------------------------------------------- bench hiding mode

// Coordinator review of PR #27: ownHistorySection was appended regardless of
// hideDriver, so a store holding a posted deliverable by the SAME analyst on
// the SAME desk, whose option named the CURRENT anomaly's own driver as its
// cause (exactly what a real analyst writes for a RECURRING event), handed a
// bench run the answer through its own history rather than through the
// anomaly's driver: line. Memory of past answers on the same desk IS an
// answer key, so hiding mode must omit the whole section, not merely filter
// it -- proven two ways: the leak itself is gone, and production
// (hideDriver=false) still carries the section whole, so the toggle is
// doing real work rather than the section having quietly broken.
func TestBenchHidingModeOmitsOwnHistoryEntirely(t *testing.T) {
	db := deliverTestDB(t)
	an := anomaly.Anomaly{
		ID: "A-mem4", Source: "gcp", Team: "research", Service: "GKE",
		Day: "2026-06-22", Direction: "up",
		Amount: money.Cents(96_500), Baseline: money.Cents(20_000),
		Excess: money.Cents(76_500), Z: 4.1, Rule: "z-score over 3.5",
		RuleVer: anomaly.RuleVersion, State: anomaly.Open, DetectedAt: "2026-06-23T00:00:00Z",
		Driver: "Quarterly model refresh, planned",
	}
	plantAnomaly(t, db, an)
	plantDriver(t, db, world.Driver{
		Start: an.Day, End: an.Day, Scope: an.Service,
		Label: an.Driver, Kind: "one-time", Source: an.Source,
	})

	// The analyst's OWN past history on this desk: a prior deliverable that
	// named THIS SAME driver as its cause, the recurring-event case that
	// makes memory an answer key if it survives hiding mode.
	pastTask := plantMemoryTask(t, db, "gcp", "Explain last quarter's move")
	pastArtID := plantPostedArtifact(t, db, pastTask, "investigator-gcp",
		"The quarterly refresh drove usage up again.", "2026-03-15T00:00:00Z")
	plantOption(t, db, pastArtID, 1, "anomaly.explain", an.Driver, crew.OptionApplied, "owner1", "")

	task := crew.Task{ID: 1, Anomaly: an.ID, Desk: an.Source}
	a := crew.Analyst{Name: "investigator-gcp", State: "active", Skills: []string{"anomaly-triage"}}

	hidden := Packet(db, task, a, true)
	if strings.Contains(hidden, "What you posted on this desk before") {
		t.Errorf("a bench packet (hideDriver=true) still carries the history section at all:\n%s", hidden)
	}
	if strings.Contains(hidden, an.Driver) {
		t.Errorf("a bench packet (hideDriver=true) names the driver label via the history section:\n%s", hidden)
	}

	shown := Packet(db, task, a, false)
	if !strings.Contains(shown, "What you posted on this desk before") {
		t.Fatalf("production (hideDriver=false) lost the history section entirely:\n%s", shown)
	}
	if !strings.Contains(shown, an.Driver) {
		t.Errorf("production's own history section no longer carries the option that named the driver:\n%s", shown)
	}
}

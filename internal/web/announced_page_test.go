package web_test

// ANNOUNCED-DRIVER-SPEC.md section 5: the decision page shows an applied
// driver.announced decision's announced amount beside what its month measured,
// and how many days before its start the owner answered it.
//
// Red against the code before this step: the answered card carries no
// announced figures, so neither the measured amount nor the answer line is on
// the page.

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// spendRun writes one usage charge a day for days days from first.
func spendRun(t *testing.T, h *harness, source, service, first string, days int, cents int64) {
	t.Helper()
	db := h.st.DB()
	for i := 0; i < days; i++ {
		day := daysAfter(t, first, i)
		if _, err := db.Exec(`INSERT INTO charges
			(source, day, service, team, category, billed_cents) VALUES (?,?,?,NULL,'Usage',?)`,
			source, day, service, cents); err != nil {
			t.Fatal(err)
		}
	}
}

// plantAppliedAnnounced is a sprint, a posted deliverable and an announced
// option already APPLIED by the owner, with its driver registered as the
// owner's answer would register it. The answer is the first of October, four
// days before the driver starts on the fifth.
func plantAppliedAnnounced(t *testing.T, h *harness, owner, label string) int {
	t.Helper()
	db := h.st.DB()
	sres, err := db.Exec(`INSERT INTO sprints (label, start, finish, state, goal)
		VALUES ('2026-W41', '2026-10-01', '2026-10-07', 'active', 'a goal')`)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := sres.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	tres, err := db.Exec(`INSERT INTO tasks
		(sprint, title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated, owner)
		VALUES (?, 'a task', 'a goal', 'optimizer-aws', 'aws', 'active', 0, 0,
		        datetime('now'), datetime('now'), ?)`, sid, owner)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := tres.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	ares, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created)
		VALUES (?, 'optimizer-aws', 'a deliverable', 'body', 'posted', datetime('now'))`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	artID, err := ares.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state, decided_by, decided_at)
		VALUES (?, 1, 'driver.announced', ?, 0, 0, 'low', 'nothing', '[]', 'applied', ?, '2026-10-01T09:00:00Z')`,
		artID, label, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO decision_requests (artifact, sprint, owner, lapses, created)
		VALUES (?,?,?,?,datetime('now'))`, artID, sid, owner, "2026-10-08"); err != nil {
		t.Fatal(err)
	}
	if err := estate.InsertDriver(db, world.Driver{Start: "2026-10-05", End: "2026-10-31",
		Scope: "svc-test-a", Label: label, Kind: "announced", Source: "aws",
		Expected: int64Ptr(-300_00)}); err != nil {
		t.Fatal(err)
	}
	return int(sid)
}

func int64Ptr(v int64) *int64 { return &v }

func daysAfter(t *testing.T, first string, n int) string {
	t.Helper()
	d, err := time.Parse("2006-01-02", first)
	if err != nil {
		t.Fatal(err)
	}
	return d.AddDate(0, 0, n).Format("2006-01-02")
}

func TestTheDecisionPageSaysWhatAnAnnouncedDecisionMeasured(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026") // first account: admin
	if _, err := h.au.Create("owner1", "owner1-password-2026", "operator"); err != nil {
		t.Fatal(err)
	}
	// September before the start, and the start's own month: 1000.00 a day
	// before the step, 880.00 a day from the fifth, so the measured effect is
	// -120.00 a day against the announced -300.00.
	spendRun(t, h, "aws", "svc-test-a", "2026-09-07", 24, 1000_00)
	spendRun(t, h, "aws", "svc-test-a", "2026-10-01", 4, 1000_00)
	spendRun(t, h, "aws", "svc-test-a", "2026-10-05", 27, 880_00)
	sid := plantAppliedAnnounced(t, h, "owner1", "swap A")

	boss := h.as(t, "boss", "boss-password-2026")
	code, body, _ := boss.get(t, "/sprint/"+strconv.Itoa(sid)+"/decisions/owner1")
	if code != 200 {
		t.Fatalf("the decision page answered %d, want 200", code)
	}
	for _, want := range []string{
		"swap A",
		"-300.00/day, measured -120.00/day over 27 covered days in 2026-10",
		"answered 4 days before the start",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the decision page does not say %q", want)
		}
	}
}

// ANNOUNCED-DRIVER-SPEC.md section 11, findings (c) and F4: the answer line reads
// once, with its label and no repeated word, and a decision that names no team
// shows how its step splits across the teams on its desk, each team's share of
// the spend and the expected amount a day.
//
// Red against the code before this step: the line says "announced" twice, and
// no split is shown.
func TestTheDecisionPageStatesTheAnnouncedLineOnceAndShowsTheSplit(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	if _, err := h.au.Create("owner1", "owner1-password-2026", "operator"); err != nil {
		t.Fatal(err)
	}
	db := h.st.DB()
	// 28 days before the 5th of October, the window the split reads: data-eng
	// spends 100.00 a day and product-web 300.00 a day, so the -300.00 a day
	// decision splits 25% and 75%: -75.00 and -225.00 a day.
	for i := 0; i < 28; i++ {
		day := daysAfter(t, "2026-09-07", i)
		for _, tc := range []struct {
			team  string
			cents int64
		}{{"data-eng", 100_00}, {"product-web", 300_00}} {
			if _, err := db.Exec(`INSERT INTO charges
				(source, day, service, team, category, billed_cents) VALUES ('aws', ?, 'svc-test-a', ?, 'Usage', ?)`,
				day, tc.team, tc.cents); err != nil {
				t.Fatal(err)
			}
		}
	}
	sid := plantAppliedAnnounced(t, h, "owner1", "swap A")

	boss := h.as(t, "boss", "boss-password-2026")
	code, body, _ := boss.get(t, "/sprint/"+strconv.Itoa(sid)+"/decisions/owner1")
	if code != 200 {
		t.Fatalf("the decision page answered %d, want 200", code)
	}
	if strings.Contains(body, "<dd>announced ") {
		t.Errorf("the announced line repeats the label's word at its start")
	}
	if !strings.Contains(body, "<dd>-300.00/day, measured ") {
		t.Errorf("the announced line does not open with its figure once, under its label")
	}
	for _, want := range []string{"data-eng", "25.0%", "-75.00/day", "product-web", "75.0%", "-225.00/day"} {
		if !strings.Contains(body, want) {
			t.Errorf("the split does not show %q", want)
		}
	}
}

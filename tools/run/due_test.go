package main

// -due: B5-SPEC.md section 7, in the order the testing rule names them.
//
// Red first, against main: -due, runDue, runDueOn, duePreflight, dueExecute,
// errCadenceOff and crew.CadenceDue/CadenceSettings/SetCadence do not exist
// yet, so this file does not compile -- the same shape gateway_test.go's own
// header already documents for a feature built from nothing.

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// dueTestDB is a fresh store carrying every schema -due's own path touches.
// Production relies on the console having already run these migrations once
// against the same data directory before a person could ever reach
// /cadence to flip the switch; this stands them up directly, the same way
// runnerTasks (provenance_test.go) already does for the ordinary -live path.
func dueTestDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.DB()
	if _, err := db.Exec(crew.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(crew.RosterSchema); err != nil {
		t.Fatal(err)
	}
	if err := crew.EnsureArtifactProvenance(db); err != nil {
		t.Fatal(err)
	}
	if err := crew.EnsureLiveSpendLedger(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// hireDue is a minimal, isolated analyst that is cadence-due from the
// moment it exists: cadence "daily", never posted. "anomaly-triage" keeps
// Packet() from touching any figures table this fixture does not carry
// (internal/deliver/estimate_test.go picks the same skill for the same
// reason).
func hireDue(t *testing.T, db *sql.DB, name, engine string, perTask, monthly money.Cents) {
	t.Helper()
	if err := crew.Hire(db, crew.Analyst{
		Name: name, Role: "test analyst", Desk: "aws", Engine: engine,
		Skills: []string{"anomaly-triage"}, Rights: []string{"figures-read"},
		PerTask: perTask, Monthly: monthly, Cadence: "daily",
		Audience: "the desk", Owner: "yurii", Parent: "supervisor",
		Attestation: "none",
	}); err != nil {
		t.Fatal(err)
	}
}

// fakeEngineServer is the ONLY thing -live is ever pointed at in this
// package's tests: a local httptest server standing in for the gateway,
// exactly as gateway_test.go's own tests already do. It answers every
// request with one text block and a small, fixed token count.
func fakeEngineServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"content":[{"type":"text","text":"the deliverable"}],`+
			`"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sprintCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sprints`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// ------------------------------------------------------------- red first (1)

// -due with the switch off exits 2 (errCadenceOff) and creates nothing.
func TestDueWithTheSwitchOffExitsTwoAndCreatesNothing(t *testing.T) {
	db := dueTestDB(t)
	hireDue(t, db, "daily-writer", "openrouter", money.Cents(100), money.Cents(10000))
	// A generous, nonzero cadence.ceiling_cents with the switch off: off is
	// the reason this must refuse, not an incidental zero ceiling (which
	// defaults to off "by another name" and would refuse on its own,
	// proving nothing about the switch check specifically).
	if err := crew.SetCadence(db, false, money.Cents(500_00), "nobody"); err != nil {
		t.Fatal(err)
	}

	err := runDueOn(db, nil, money.Cents(500_00), true, 2000, true, bus{}, gatewayConfig{}, "2026-09-03")
	if err == nil {
		t.Fatal("-due with the switch off was accepted")
	}
	if !errors.Is(err, errCadenceOff) {
		t.Errorf("error %v does not wrap errCadenceOff", err)
	}
	if dueExitCode(err) != 2 {
		t.Errorf("dueExitCode(%v) = %d, want 2: a cron wrapper must be able to "+
			"tell this apart from an ordinary failure", err, dueExitCode(err))
	}
	if got := sprintCount(t, db); got != 0 {
		t.Errorf("%d sprint(s) exist after a refused run; the switch being off "+
			"must mean nothing else is touched", got)
	}
}

// ------------------------------------------------------------- red first (2)

// A worst case above the ceiling refuses before any call: no sprint, no task.
func TestDueRefusesBeforeAnyCallWhenWorstExceedsTheCeiling(t *testing.T) {
	db := dueTestDB(t)
	// A generous per-task guard, so the ITEM's own guard is never what
	// refuses it: this test is about the RUN's ceiling, not the per-task
	// one price() already covers elsewhere.
	hireDue(t, db, "expensive", "anthropic", money.Cents(1000_00), money.Cents(100000_00))
	if err := crew.SetCadence(db, true, money.Cents(100_00), "yurii"); err != nil {
		t.Fatal(err)
	}
	srv := fakeEngineServer(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	gw := gatewayConfig{URL: srv.URL, Host: "test.local", CeilingUSD: money.Cents(1)}

	// -ceiling of one cent: the packet's own text alone worst-cases above it,
	// and the per-task guard above is far too generous to be the reason.
	err := runDueOn(db, nil, money.Cents(1), true, 2000, true, bus{}, gw, "2026-09-03")
	if err == nil {
		t.Fatal("a worst case above the ceiling was accepted")
	}
	if !strings.Contains(err.Error(), "refused before any call") {
		t.Errorf("refused for the wrong reason: %v", err)
	}
	if got := sprintCount(t, db); got != 0 {
		t.Errorf("%d sprint(s) exist after a ceiling refusal", got)
	}
}

// TestDueRefusesBeforeAnyCallWhenTheMultipliedWorstExceedsTheCeilingButNotTheSingleCallWorst
// is PRICE-DISPLAY-SPEC.md's own gap, found reading due.go rather than named
// there by name: dueWorstMicros summed the raw, single-call e.WorstMicros
// the same way report() and price()'s Verdict did, so a ceiling that could
// never cover six rounds on the tool loop still passed THIS boundary check,
// let crew.Approve create the sprint and the task, and only failed once
// spend() reached execute()'s own reserve() -- which spend() swallows into a
// printed line and a nil return (live.go's own spend, "A refusal stops the
// run"), so dueExecute saw success and left a sprint and a never-run task on
// the board.
//
// TestDueRunsWhenTheCeilingExactlyEqualsTheWorstCase, above in this file,
// already sits at exactly this boundary and never noticed: its own comment
// already names execute()'s reservation as "a different, larger number" than
// this boundary's own worst case and deliberately asserts only on
// sprintCount and dueExecute's own refusal wording, never on whether the
// task actually ran. This test is the assertion that was missing.
//
// single is learned from each estimate's own e.WorstMicros directly, NOT
// from dueWorstMicros(ests): after this fix dueWorstMicros returns the
// RESERVED (already multiplied) figure, so calling it to learn the
// single-call baseline would be circular -- it would return the very number
// this test exists to check.
func TestDueRefusesBeforeAnyCallWhenTheMultipliedWorstExceedsTheCeilingButNotTheSingleCallWorst(t *testing.T) {
	db := dueTestDB(t)
	hireDue(t, db, "expensive", "anthropic", money.Cents(100_000_00), money.Cents(100_000_00))
	if err := crew.SetCadence(db, true, money.Cents(100_000_00), "yurii"); err != nil {
		t.Fatal(err)
	}

	// Learn this fixture's own single-call worst case first, under a
	// deliberately generous ceiling that cannot itself be the reason for
	// anything below.
	_, ests, _, _, _, _, _, err := duePreflight(db, money.Cents(100_000_00), true, 2000, "2026-09-03")
	if err != nil {
		t.Fatal(err)
	}
	var single int64
	var wouldRun int
	for _, e := range ests {
		if !e.Refused && e.Priced {
			wouldRun++
			single += e.WorstMicros
		}
	}
	if wouldRun == 0 || single <= 0 {
		t.Fatal("the fixture priced nothing to bound this test against")
	}
	multiplied := single * int64(loopsFor("anthropic"))
	if multiplied <= single {
		t.Fatal(`loopsFor("anthropic") is 1: this test needs a looping engine to mean anything`)
	}
	ceilingMicros := single + (multiplied-single)/2
	ceiling := money.Cents((ceilingMicros + 9_999) / 10_000)
	if int64(ceiling)*10_000 <= single || int64(ceiling)*10_000 >= multiplied {
		t.Fatalf("the ceiling %s does not sit strictly between %s and %s",
			ceiling, usd(single), usd(multiplied))
	}
	if err := crew.SetCadence(db, true, ceiling, "yurii"); err != nil {
		t.Fatal(err)
	}

	// A fake server IS set up, the same as this file's other -due -live
	// tests, even though a correctly-refusing run never dials one: this
	// keeps the test meaningful (rather than accidentally passing because
	// nothing could be reached) whichever state of the fix it runs against.
	srv := fakeEngineServer(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	gw := gatewayConfig{URL: srv.URL, Host: "test.local", CeilingUSD: ceiling}

	err = runDueOn(db, nil, ceiling, true, 2000, true, bus{}, gw, "2026-09-03")
	if err == nil {
		t.Fatalf("-due -live accepted a ceiling (%s) between the single-call worst "+
			"case (%s) and the multiplied one (%s): a run that cannot possibly cover "+
			"its own worst case must refuse before any call, not create a sprint and "+
			"then silently fail to run it", ceiling, usd(single), usd(multiplied))
	}
	if !strings.Contains(err.Error(), "refused before any call") {
		t.Errorf("refused for the wrong reason: %v", err)
	}
	if got := sprintCount(t, db); got != 0 {
		t.Errorf("%d sprint(s) exist after a refusal that should have run before any "+
			"call: -due's own boundary must refuse before crew.Approve, not after", got)
	}
}

// ------------------------------------------------------------- red first (3)

// Without -live, -due prints and writes nothing.
func TestDueDryRunPrintsAndWritesNothing(t *testing.T) {
	db := dueTestDB(t)
	hireDue(t, db, "daily-writer", "openrouter", money.Cents(500), money.Cents(10000))
	if err := crew.SetCadence(db, true, money.Cents(500_00), "yurii"); err != nil {
		t.Fatal(err)
	}

	if err := runDueOn(db, nil, money.Cents(500_00), true, 2000, false, bus{}, gatewayConfig{}, "2026-09-03"); err != nil {
		t.Fatalf("a dry run under the ceiling must not error: %v", err)
	}
	if got := sprintCount(t, db); got != 0 {
		t.Errorf("a dry run created %d sprint(s); -due without -live must write nothing", got)
	}
	var tasks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if tasks != 0 {
		t.Errorf("a dry run created %d task(s); -due without -live must write nothing", tasks)
	}
}

// ------------------------------------------------------------- red first (4)

// -due -live against a fake engine server creates the tasks under
// cadence-<date>, runs them, and emits one crew_ran with the summed cost.
func TestDueLiveCreatesRunsAndEmitsCrewRan(t *testing.T) {
	db := dueTestDB(t)
	hireDue(t, db, "daily-writer", "anthropic", money.Cents(500), money.Cents(10000))
	if err := crew.SetCadence(db, true, money.Cents(500_00), "yurii"); err != nil {
		t.Fatal(err)
	}
	srv := fakeEngineServer(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	gw := gatewayConfig{URL: srv.URL, Host: "test.local", CeilingUSD: money.Cents(500_00)}
	b, path := testBus(t, "test.local", "crew-due-1")

	if err := runDueOn(db, nil, money.Cents(500_00), true, 2000, true, b, gw, "2026-09-03"); err != nil {
		t.Fatalf("a live due run under the ceiling errored: %v", err)
	}

	var label string
	if err := db.QueryRow(`SELECT label FROM sprints`).Scan(&label); err != nil {
		t.Fatalf("no sprint was created: %v", err)
	}
	if label != "cadence-2026-09-03" {
		t.Errorf("sprint label = %q, want cadence-2026-09-03", label)
	}

	var artifacts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM artifacts`).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if artifacts != 1 {
		t.Fatalf("%d artifact(s) written, want 1: the task must have actually run", artifacts)
	}

	evs := allEvents(t, path)
	var ran []map[string]any
	for _, ev := range evs {
		if ev["type"] == "crew_ran" {
			ran = append(ran, ev)
		}
	}
	if len(ran) != 1 {
		t.Fatalf("crew_ran events = %d, want exactly 1 (%v)", len(ran), evs)
	}
	data, _ := ran[0]["data"].(map[string]any)
	if data == nil {
		t.Fatal("crew_ran carries no data")
	}
	if got := data["sprint"]; got != "cadence-2026-09-03" {
		t.Errorf("crew_ran sprint = %v, want cadence-2026-09-03", got)
	}
	costMicros, _ := data["cost_micros"].(float64)
	if costMicros <= 0 {
		t.Errorf("crew_ran cost_micros = %v, want > 0: the run actually spent something", data["cost_micros"])
	}
	if got := data["switched_on_by"]; got != "yurii" {
		t.Errorf("crew_ran switched_on_by = %v, want yurii", got)
	}
}

// crew_ran's cost is summed in micros BEFORE any rounding, never rounded
// per task first. [[finest-unit-per-row-round-once-at-the-aggregate]], and
// CLAUDE.md invariant 18's own history: a test with only one task cannot
// tell "summed then rounded" from "rounded then summed" apart, because for
// one row they agree. Two tasks, each costing 105 micros (the fake
// server's fixed usage, 10 in + 5 out tokens at anthropic's published
// rate) -- a fifth of a hundredth of a cent, far under the 10,000-micron
// cent -- make the two diverge: rounding each to the nearest cent FIRST
// floors both to zero, and the true sum, 210 micros, is what
// emitCrewRan's SQL must report.
func TestCrewRanCostSumsMicrosBeforeAnyRounding(t *testing.T) {
	db := dueTestDB(t)
	hireDue(t, db, "daily-writer-a", "anthropic", money.Cents(500), money.Cents(10000))
	hireDue(t, db, "daily-writer-b", "anthropic", money.Cents(500), money.Cents(10000))
	if err := crew.SetCadence(db, true, money.Cents(500_00), "yurii"); err != nil {
		t.Fatal(err)
	}
	srv := fakeEngineServer(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	gw := gatewayConfig{URL: srv.URL, Host: "test.local", CeilingUSD: money.Cents(500_00)}
	b, path := testBus(t, "test.local", "crew-due-round")

	if err := runDueOn(db, nil, money.Cents(500_00), true, 2000, true, b, gw, "2026-09-03"); err != nil {
		t.Fatalf("a live due run under the ceiling errored: %v", err)
	}

	var summedMicros int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(live_micros),0) FROM tasks`).Scan(&summedMicros); err != nil {
		t.Fatal(err)
	}
	if summedMicros <= 0 {
		t.Fatalf("the board's own live_micros summed to %d, want > 0: the fixture "+
			"priced nothing to bound this test against", summedMicros)
	}
	// Each task individually is under one cent (10,000 micros): if either
	// task alone would round to zero, rounding-then-summing and
	// summing-then-rounding could accidentally agree, which would make this
	// test unable to tell them apart. Both must be true for the test to
	// mean anything.
	var perTask []int64
	rows, err := db.Query(`SELECT live_micros FROM tasks`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var m int64
		if err := rows.Scan(&m); err != nil {
			t.Fatal(err)
		}
		perTask = append(perTask, m)
	}
	rows.Close()
	for _, m := range perTask {
		if m >= 10_000 {
			t.Fatalf("a task cost %d micros, which is a whole cent or more: this "+
				"test needs every task under a cent to isolate the rounding order", m)
		}
	}

	// Two tasks also each write a tool_call event, so the bus carries three
	// lines here, not one: this filters for crew_ran rather than assuming
	// there is only one event, unlike the single-task test above.
	var ran []map[string]any
	for _, e := range allEvents(t, path) {
		if e["type"] == "crew_ran" {
			ran = append(ran, e)
		}
	}
	if len(ran) != 1 {
		t.Fatalf("crew_ran events = %d, want exactly 1", len(ran))
	}
	data, _ := ran[0]["data"].(map[string]any)
	got, _ := data["cost_micros"].(float64)
	if int64(got) != summedMicros {
		t.Errorf("crew_ran cost_micros = %v, want %d (the board's own true sum, "+
			"summed in micros before any rounding)", data["cost_micros"], summedMicros)
	}
}

// ------------------------------------------------------------- red first (6)

// A second -due -live the same day creates no second sprint and no
// duplicate task.
func TestASecondDueLiveTheSameDayIsIdempotent(t *testing.T) {
	db := dueTestDB(t)
	hireDue(t, db, "daily-writer", "anthropic", money.Cents(500), money.Cents(10000))
	if err := crew.SetCadence(db, true, money.Cents(500_00), "yurii"); err != nil {
		t.Fatal(err)
	}
	srv := fakeEngineServer(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	gw := gatewayConfig{URL: srv.URL, Host: "test.local", CeilingUSD: money.Cents(500_00)}
	b, _ := testBus(t, "test.local", "crew-due-1")

	if err := runDueOn(db, nil, money.Cents(500_00), true, 2000, true, b, gw, "2026-09-03"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if got := sprintCount(t, db); got != 1 {
		t.Fatalf("after the first run, sprints = %d, want 1", got)
	}
	var tasksAfterFirst int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&tasksAfterFirst); err != nil {
		t.Fatal(err)
	}

	b2, _ := testBus(t, "test.local", "crew-due-2")
	if err := runDueOn(db, nil, money.Cents(500_00), true, 2000, true, b2, gw, "2026-09-03"); err != nil {
		t.Fatalf("second run on the same day: %v", err)
	}
	if got := sprintCount(t, db); got != 1 {
		t.Errorf("after a second same-day run, sprints = %d, want 1 (no duplicate)", got)
	}
	var tasksAfterSecond int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&tasksAfterSecond); err != nil {
		t.Fatal(err)
	}
	if tasksAfterSecond != tasksAfterFirst {
		t.Errorf("tasks went %d -> %d on a same-day rerun, want unchanged", tasksAfterFirst, tasksAfterSecond)
	}
}

// ------------------------------------------------------------ boundaries (7)

// A ceiling exactly equal to the worst case runs -due's own preflight
// check, rather than being refused on a strict greater-than-or-equal
// comparison: worst > ceiling refuses, worst == ceiling does not.
//
// This isolates -due's OWN boundary (dueExecute's "refused before any
// call") from execute()'s separate, already-tested reservation math
// (CLAUDE.md invariant 17: it reserves loopsFor(engine) calls' worth up
// front for an engine on the tool loop, which is a different, larger
// number than the single-call worst case this boundary is about) by
// asserting on dueExecute's own refusal message and on whether Approve
// ever ran, not on whether the deeper call itself completed.
func TestDueRunsWhenTheCeilingExactlyEqualsTheWorstCase(t *testing.T) {
	db := dueTestDB(t)
	hireDue(t, db, "daily-writer", "anthropic", money.Cents(500), money.Cents(10000))
	if err := crew.SetCadence(db, true, money.Cents(500_00), "yurii"); err != nil {
		t.Fatal(err)
	}
	srv := fakeEngineServer(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")

	// Preflight-price to learn the exact worst case, then set BOTH ceilings
	// to precisely that.
	_, ests, _, _, _, _, _, err := duePreflight(db, money.Cents(500_00), true, 2000, "2026-09-03")
	if err != nil {
		t.Fatal(err)
	}
	worst, wouldRun, _ := dueWorstMicros(ests)
	if wouldRun == 0 {
		t.Fatal("nothing would run; the fixture priced nothing to bound the ceiling against")
	}
	exact := money.Cents((worst + 9_999) / 10_000) // ceiling is cents; round up to cover the worst case exactly
	if err := crew.SetCadence(db, true, exact, "yurii"); err != nil {
		t.Fatal(err)
	}
	gw := gatewayConfig{URL: srv.URL, Host: "test.local", CeilingUSD: exact}
	b, _ := testBus(t, "test.local", "crew-due-exact")

	err = runDueOn(db, nil, exact, true, 2000, true, b, gw, "2026-09-03")
	if err != nil && strings.Contains(err.Error(), "refused before any call") {
		t.Errorf("a ceiling exactly at the worst case was refused at -due's own "+
			"boundary check: %v", err)
	}
	if got := sprintCount(t, db); got != 1 {
		t.Errorf("sprints = %d, want 1: -due's own boundary check must let a ceiling "+
			"exactly at the worst case through to Approve", got)
	}
}

// cadence.ceiling_cents of 0 refuses, which is "off" by another name, even
// with -ceiling itself generous.
func TestDueRefusesWhenCadenceCeilingCentsIsZero(t *testing.T) {
	db := dueTestDB(t)
	hireDue(t, db, "daily-writer", "anthropic", money.Cents(500), money.Cents(10000))
	if err := crew.SetCadence(db, true, 0, "yurii"); err != nil {
		t.Fatal(err)
	}
	srv := fakeEngineServer(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	gw := gatewayConfig{URL: srv.URL, Host: "test.local", CeilingUSD: money.Cents(500_00)}

	err := runDueOn(db, nil, money.Cents(500_00), true, 2000, true, bus{}, gw, "2026-09-03")
	if err == nil {
		t.Fatal("cadence.ceiling_cents=0 was accepted despite a generous -ceiling")
	}
	if got := sprintCount(t, db); got != 0 {
		t.Errorf("%d sprint(s) exist after a zero-ceiling refusal", got)
	}
}

// --------------------------------------------------------------- hostile (10)

// A ceiling of -1 is refused, not silently clamped or treated as "no cap".
func TestDueRefusesANegativeCeilingFlag(t *testing.T) {
	db := dueTestDB(t)
	hireDue(t, db, "daily-writer", "openrouter", money.Cents(500), money.Cents(10000))
	if err := crew.SetCadence(db, true, money.Cents(500_00), "yurii"); err != nil {
		t.Fatal(err)
	}
	err := runDueOn(db, nil, money.Cents(-1), true, 2000, false, bus{}, gatewayConfig{}, "2026-09-03")
	if err == nil {
		t.Fatal("-ceiling -1 (a cent, negative) was accepted")
	}
	if got := sprintCount(t, db); got != 0 {
		t.Errorf("%d sprint(s) exist after a negative-ceiling refusal", got)
	}
}

// --------------------------------------------------------------- hostile (11)

// A settings row holding garbage reads as off end to end, through -due, not
// only through crew.CadenceSettings in isolation.
func TestDueOnGarbageSettingsRefusesAsOff(t *testing.T) {
	db := dueTestDB(t)
	hireDue(t, db, "daily-writer", "openrouter", money.Cents(500), money.Cents(10000))
	if err := crew.SetCadence(db, false, 0, "nobody"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE settings SET value='banana' WHERE key='cadence.enabled'`); err != nil {
		t.Fatal(err)
	}

	err := runDueOn(db, nil, money.Cents(500_00), true, 2000, true, bus{}, gatewayConfig{}, "2026-09-03")
	if !errors.Is(err, errCadenceOff) {
		t.Errorf("garbage in cadence.enabled did not read as off: %v", err)
	}
	if got := sprintCount(t, db); got != 0 {
		t.Errorf("%d sprint(s) exist after a garbage-settings refusal", got)
	}
}

// --------------------------------------------------------------- hostile (12)

// A run with the switch flipped off between preflight and execution refuses,
// re-read right before the first call, and creates nothing.
func TestDueRefusesWhenTheSwitchIsFlippedOffBetweenPreflightAndExecution(t *testing.T) {
	db := dueTestDB(t)
	hireDue(t, db, "daily-writer", "anthropic", money.Cents(500), money.Cents(10000))
	if err := crew.SetCadence(db, true, money.Cents(500_00), "yurii"); err != nil {
		t.Fatal(err)
	}

	items, ests, label, cadenceCeiling, effCeil, changedBy, changedAt, err := duePreflight(
		db, money.Cents(500_00), true, 2000, "2026-09-03")
	if err != nil {
		t.Fatalf("preflight while the switch was on: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("nothing was due; the fixture priced nothing for this test to flip under")
	}

	// The switch is turned off between preflight and execution -- the exact
	// race section 7 names.
	if err := crew.SetCadence(db, false, 0, "second-person"); err != nil {
		t.Fatal(err)
	}

	srv := fakeEngineServer(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	gw := gatewayConfig{URL: srv.URL, Host: "test.local", CeilingUSD: effCeil}

	err = dueExecute(db, nil, items, ests, label, "2026-09-03",
		money.Cents(500_00), cadenceCeiling, effCeil, changedBy, changedAt, 2000, bus{}, gw)
	if !errors.Is(err, errCadenceOff) {
		t.Errorf("execution proceeded after the switch was flipped off: %v", err)
	}
	if got := sprintCount(t, db); got != 0 {
		t.Errorf("%d sprint(s) exist after a mid-run refusal: nothing should have been created", got)
	}
}

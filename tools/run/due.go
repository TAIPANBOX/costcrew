package main

// -due: B5-SPEC.md section 3. Only cadence-due work, only under the ceiling,
// and only while a person has switched the cadence on in the console.
//
// This composes the existing run rather than reimplementing it: CadenceDue is
// the SAME function the plan and the console read (internal/crew/plan.go),
// price() and report() are the SAME estimator and printer the ordinary
// sprint dry run uses, crew.Approve is the SAME path that materialises a
// plan onto the board, and spend() is the SAME executor that reserves,
// calls, settles and journals. Nothing here bypasses -ceiling, -gateway or
// the per-task worst case.

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// errCadenceOff is what -due returns when the console's switch is off, or
// was turned off between the preflight check and execution. main() maps it
// to exit code 2, distinct from an ordinary failure (1), so a cron wrapper
// can tell "nothing to do, by design" from "broke".
var errCadenceOff = errors.New("the console's cadence switch is off; a person turns it on at /cadence")

// runDue is -due's entry point from run().
func runDue(db, roDB *sql.DB, cap money.Cents, hasCap bool, maxTok int, live bool, b bus, gw gatewayConfig) error {
	return runDueOn(db, roDB, cap, hasCap, maxTok, live, b, gw, time.Now().Format("2006-01-02"))
}

// runDueOn is runDue with today held fixed, so a test can pin the date the
// same way price()'s own estimate pins "0000-00-00".
func runDueOn(db, roDB *sql.DB, cap money.Cents, hasCap bool, maxTok int, live bool, b bus, gw gatewayConfig, today string) error {
	items, ests, label, cadenceCeiling, effCeil, changedBy, changedAt, err := duePreflight(db, cap, hasCap, maxTok, today)
	if err != nil {
		return err
	}
	if !live {
		// The same shape -live's own preflight prints (report(), reused
		// unchanged below): a dry run never hard-refuses over the ceiling,
		// it says so and exits 0, because previewing an over-budget day is
		// the whole point of running without -live.
		fmt.Printf("-due, %s. -ceiling is %s; the console's cadence.ceiling_cents is %s; "+
			"the smaller of the two, %s, is what a live run below would be held to.\n\n",
			today, cap, cadenceCeiling, effCeil)
		report(db, ests, maxTok, effCeil, true)
		return nil
	}
	return dueExecute(db, roDB, items, ests, label, today, cap, cadenceCeiling, effCeil,
		changedBy, changedAt, maxTok, b, gw)
}

// duePreflight is section 3 points 1 to 3: refuse unless the switch is on,
// build the due list through crew.CadenceDue, and price it the way -live
// prices a sprint. It never creates or runs anything, and it never hard
// refuses on the ceiling alone -- see dueExecute for that check, repeated
// there because a dry run must still be able to preview an over-budget day.
func duePreflight(db *sql.DB, cap money.Cents, hasCap bool, maxTok int, today string) (
	items []crew.PlanItem, ests []estimate, label string,
	cadenceCeiling, effectiveCeiling money.Cents, changedBy, changedAt string, err error) {

	if !hasCap {
		return nil, nil, "", 0, 0, "", "", fmt.Errorf(
			"-due needs -ceiling: the smaller of it and the console's own " +
				"cadence.ceiling_cents is what a due run may spend")
	}
	if cap < 0 {
		return nil, nil, "", 0, 0, "", "", fmt.Errorf("-ceiling cannot be negative: %s", cap)
	}

	enabled, cadenceCeiling, changedBy, changedAt, serr := crew.CadenceSettings(db)
	if serr != nil {
		return nil, nil, "", 0, 0, "", "", serr
	}
	if !enabled {
		return nil, nil, "", 0, 0, "", "", fmt.Errorf("%w", errCadenceOff)
	}

	items, ests, derr := dueItemsAndEstimates(db, maxTok, today)
	if derr != nil {
		return nil, nil, "", 0, 0, "", "", derr
	}

	label = "cadence-" + today
	effectiveCeiling = cap
	if cadenceCeiling < effectiveCeiling {
		effectiveCeiling = cadenceCeiling
	}
	return items, ests, label, cadenceCeiling, effectiveCeiling, changedBy, changedAt, nil
}

// dueItemsAndEstimates builds today's cadence-due list (crew.CadenceDue,
// the SAME source Propose reads) and prices each item the way price() prices
// an ordinary task: a synthetic, not-yet-created crew.Task carrying the
// item's own Budget (the analyst's PerTask), so price()'s own per-task-guard
// comparison applies exactly as it would to a real one.
func dueItemsAndEstimates(db *sql.DB, maxTok int, today string) ([]crew.PlanItem, []estimate, error) {
	roster, err := crew.Roster(db)
	if err != nil {
		return nil, nil, err
	}
	month := ""
	if len(today) >= 7 {
		month = today[:7]
	}
	spent, err := crew.SpendInMonth(db, month)
	if err != nil {
		return nil, nil, err
	}
	items, err := crew.CadenceDue(db, roster, today, spent)
	if err != nil {
		return nil, nil, err
	}
	return items, priceItems(db, roster, items, maxTok), nil
}

// priceItems is dueItemsAndEstimates' pricing step, for a due list that has
// not been created on the board yet: one synthetic, ID-less crew.Task per
// item, carrying the item's own budget so the per-task guard applies exactly
// as it would to a real one.
func priceItems(db *sql.DB, roster []crew.Analyst, items []crew.PlanItem, maxTok int) []estimate {
	tasks := make([]crew.Task, len(items))
	for i, it := range items {
		tasks[i] = crew.Task{Title: it.Title, Goal: it.Goal, Assignee: it.Assignee,
			Desk: it.Desk, Budget: it.Budget}
	}
	return priceTasks(db, roster, tasks, maxTok)
}

// priceTasks is dueExecute's pricing step once the board carries the real
// rows: the same price() call, against the real crew.Task (with a real ID),
// which is what saveDraft() needs to write a deliverable against.
func priceTasks(db *sql.DB, roster []crew.Analyst, tasks []crew.Task, maxTok int) []estimate {
	by := map[string]crew.Analyst{}
	for _, a := range roster {
		by[a.Name] = a
	}
	ests := make([]estimate, 0, len(tasks))
	for _, t := range tasks {
		ests = append(ests, price(db, t, by[t.Assignee], maxTok))
	}
	return ests
}

// dueWorstMicros sums the worst case of every item this run would actually
// attempt: priced and not refused, the same population spend()'s own todo
// list holds. Summed in micros before anything is rounded, per
// [[finest-unit-per-row-round-once-at-the-aggregate]].
//
// reservedWorstCase(e), not e.WorstMicros: dueExecute's own "refused before
// any call" check (below) and the "cadence-due: N task(s) created... worst
// case X" line it prints both read this sum, and a cadence-due task on
// anthropic or openrouter runs through the SAME execute() and tool loop an
// ordinary sprint task does, so this must be what that run would actually
// reserve -- not one call's own bound. Before this fix a ceiling that could
// never cover a looped task's real reservation still passed here, let
// crew.Approve create the sprint and the task, and only failed once spend()
// reached execute()'s own (already-multiplied) reserve() -- which spend()
// swallows into a printed line and a nil return, so dueExecute saw success
// and left a sprint and a never-run task on the board. Found reading this
// file while confirming PRICE-DISPLAY-SPEC.md's own fix for report() and
// price()'s Verdict; not named there by name, but the identical gap.
func dueWorstMicros(ests []estimate) (worst int64, wouldRun, refused int) {
	for _, e := range ests {
		if e.Refused || !e.Priced {
			refused++
			continue
		}
		wouldRun++
		worst += reservedWorstCase(e)
	}
	return worst, wouldRun, refused
}

// dueExecute is section 3 point 5: create the due tasks (a sprint labelled
// cadence-<date>, created if absent through crew.Approve's own path), run
// them through the existing executor, and emit crew_ran once at the end.
//
// The switch is re-read here, fresh, right before anything is created or
// called: a person flipping it off between duePreflight and here must still
// stop this run, and creating nothing is the same "nothing else touched"
// contract the initial refusal already keeps. ests is duePreflight's own
// pricing of items, reused rather than re-derived, for the same reason
// estimate.Packet is captured once and carried into execute() unchanged
// (main.go): a run must call what it priced, not something read again.
func dueExecute(db, roDB *sql.DB, items []crew.PlanItem, ests []estimate, label, today string,
	cap, cadenceCeiling, effectiveCeiling money.Cents, changedBy, changedAt string,
	maxTok int, b bus, gw gatewayConfig) error {

	enabled, _, _, _, err := crew.CadenceSettings(db)
	if err != nil {
		return err
	}
	if !enabled {
		return fmt.Errorf("%w (turned off after this run started; nothing was created)", errCadenceOff)
	}

	// Same-day idempotency (section 7: "a second -due -live the same day
	// creates no second sprint and no duplicate task"). A draft this run
	// writes is never POSTED by itself, so the same analysts would still
	// read as due if CadenceDue ran again today; the guard is here, on the
	// sprint's own label, rather than hoping the due list changes.
	var existingID int
	switch serr := db.QueryRow(`SELECT id FROM sprints WHERE label=?`, label).Scan(&existingID); {
	case serr == nil:
		fmt.Printf("cadence already ran today: sprint %s is already on the board. "+
			"Nothing new was created.\n", label)
		return nil
	case !errors.Is(serr, sql.ErrNoRows):
		return serr
	}

	if len(items) == 0 {
		fmt.Println("nothing is cadence-due today. Nothing was created.")
		return nil
	}

	worst, wouldRun, _ := dueWorstMicros(ests)
	if wouldRun == 0 {
		fmt.Println("every cadence-due item today is refused before any call " +
			"(no headroom, or an unpriceable engine). Nothing was created.")
		return nil
	}
	if worst > int64(effectiveCeiling)*10_000 {
		return fmt.Errorf("the worst case is %s and the ceiling is %s "+
			"(the smaller of -ceiling %s and the console's cadence.ceiling_cents %s): "+
			"refused before any call", usd(worst), effectiveCeiling, cap, cadenceCeiling)
	}

	goalBy, goalAt := changedBy, changedAt
	if goalBy == "" {
		goalBy = "nobody recorded"
	}
	if goalAt == "" {
		goalAt = "an unknown date"
	}
	plan := crew.Plan{
		Label: label, Start: today, End: today,
		Goal:  fmt.Sprintf("cadence run switched on by %s on %s", goalBy, goalAt),
		Items: items,
	}
	// "owner": the person's act is the switch, already journaled with their
	// name; this call is what keeps "Approve creates the tasks and is a
	// person's act" honest, the same coarse link every other caller of
	// Approve passes (internal/web/planning.go's approvePlan).
	n, aerr := crew.Approve(db, plan, "owner")
	if aerr != nil {
		return aerr
	}

	var sid int
	if err := db.QueryRow(`SELECT id FROM sprints WHERE label=?`, label).Scan(&sid); err != nil {
		return err
	}
	liveTasks, err := crew.Tasks(db, crew.TaskFilter{Sprint: sid})
	if err != nil {
		return err
	}
	roster, err := crew.Roster(db)
	if err != nil {
		return err
	}
	liveEsts := priceTasks(db, roster, liveTasks, maxTok)

	fmt.Printf("cadence-due: %d task(s) created under %s (%d in this run, worst case %s).\n",
		n, label, wouldRun, usd(worst))
	if err := spend(db, roDB, liveEsts, maxTok, effectiveCeiling, 0, b, gw); err != nil {
		return err
	}
	return emitCrewRan(db, b, sid, label, liveEsts, effectiveCeiling, changedBy)
}

// emitCrewRan reads what this run actually cost from the board (summed once,
// in micros, per [[finest-unit-per-row-round-once-at-the-aggregate]] -- no
// further rounding here, since crew_ran's payload is micro-dollars, the same
// finest unit ai_calls and the runner's own bus already use) and journals it,
// section 6.
func emitCrewRan(db *sql.DB, b bus, sprintID int, label string, liveEsts []estimate, ceiling money.Cents, switchedOnBy string) error {
	var costMicros int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(t.live_micros),0) FROM tasks t WHERE t.sprint=?`,
		sprintID).Scan(&costMicros); err != nil {
		return err
	}
	_, ran, refused := dueWorstMicros(liveEsts)
	return b.crewRan(label, ran, refused, costMicros, ceiling, switchedOnBy)
}

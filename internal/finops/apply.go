package finops

// Apply is B3-SPEC.md section 3's table in code: one row per decision class
// this console can actually carry out, reusing the existing function that
// already does it rather than a second copy of the write. A class with no
// row here is "text only": applying it is recording the decision, with no
// side effect, which is applySideEffect's own default case below.
//
// Every application goes through the journal chain with the actor (the
// supervisor's name or the owner's account) and the option's id, so the
// audit page shows who decided what and on which evidence -- independently
// of whatever the inner domain function (anomaly.Explain and the rest)
// journals on its own, because that inner journalling names the anomaly's
// HANDLER, not whoever just applied this option, and the two are often
// different people.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// Recorder mirrors crew.Recorder and anomaly.Recorder's single method,
// restated here rather than imported: internal/crew already imports nothing
// of this package, so importing either of theirs back would risk a cycle the
// moment one of them adds a dependency here, and the method set is the whole
// contract.
type Recorder interface {
	Emit(kind, actor, severity string, data map[string]any, onBehalfOf []string) error
}

// Apply carries out one option, whether the actor is "supervisor" (the
// supervisor's own pass, for a class its job description lets it decide
// alone) or an owner's account username (the owner's stamp on a carried
// option). It does not check MayDecide/Escalates itself -- the caller
// already has, because THAT check is what decides whether Apply is reachable
// at all (internal/finops.Supervise for the supervisor's classes,
// internal/web's owner-answer routes for carried ones) -- so an
// analyst's Post, which never calls this function, still applies nothing,
// exactly as before this file existed.
//
// Applying an option always resolves its deliverable's whole choice:
// `@yurii 2026-09-02`, "давати на вибір якісь певні рішення" is offering a
// CHOICE, never independent actions, so every other still-live option
// crew.LiveRivalsOf finds -- the rest of THIS deliverable's own
// alternatives, and, for anomaly.explain, the other side of a "two analysts
// answered differently" question living on a different deliverable
// (roles.yaml's own hands_to_owner_conditions) -- is marked not_chosen in
// the SAME call. The choosing itself is roles.yaml's own option.select,
// "which of an analyst's options is carried forward"; it is journaled as
// part of the existing option_applied event (a not_chosen list in its own
// data) rather than as a fourth wire type, because a new type costs a
// registry change in every repository that reads the shared bus.
func Apply(db *sql.DB, opt crew.Option, actor string, rec Recorder) error {
	taskID, err := crew.TaskOfArtifact(db, opt.Artifact)
	if err != nil {
		return err
	}
	t, err := crew.GetTask(db, taskID)
	if err != nil {
		return err
	}

	if err := applySideEffect(db, opt, t, actor, rec); err != nil {
		return err
	}
	if err := crew.MarkOptionApplied(db, opt.Artifact, opt.Ordinal, actor); err != nil {
		return err
	}

	rivals, err := crew.LiveRivalsOf(db, opt)
	if err != nil {
		return err
	}
	notChosenReason := fmt.Sprintf("not chosen: option %d (%s) was applied", opt.Ordinal, opt.Class)
	for _, riv := range rivals {
		if err := crew.MarkOptionNotChosen(db, riv.Artifact, riv.Ordinal, actor, notChosenReason); err != nil {
			return err
		}
	}

	if rec != nil {
		data := map[string]any{
			"artifact":     opt.Artifact,
			"ordinal":      opt.Ordinal,
			"class":        opt.Class,
			"task":         taskID,
			"figure_cents": opt.FigureCents,
		}
		if len(rivals) > 0 {
			nc := make([]map[string]any, 0, len(rivals))
			for _, riv := range rivals {
				nc = append(nc, map[string]any{
					"artifact": riv.Artifact, "ordinal": riv.Ordinal, "class": riv.Class,
				})
			}
			data["not_chosen"] = nc
		}
		_ = rec.Emit("option_applied", actor, "info", data, nil)
	}
	return nil
}

// applySideEffect is the table itself. Every class named here also exists in
// internal/crew/roles.yaml (scripts/roles-are-bound.sh's property 1 already
// checks that direction for every "// class:" tag in internal/ and tools/),
// and the classes with no case below -- budget.set among them -- are "text
// only" deliberately: its existing function (the estate budgets intake)
// needs a structured target (a team and month) the generic options shape
// (class/summary/figure_cents/saving_cents/risk/needs/evidence) does not
// carry and this option's own task does not supply either. Inventing one
// would be exactly "invent a number it was not given", the rule every job
// description in ROLES-2026-09.md carries under "Never". Wiring it is
// follow-up work once a companion field names the target, the same way
// C2-SPEC.md's own "target" field did for allocation.rule below (invariant
// 32, CLAUDE.md): once a class carries a real target, applySideEffect's own
// case reads it rather than staying text-only forever.
//
// explainer.publish was a third class in that position until C8-SPEC.md: it
// needed an explainer id the generic shape does not carry either, but the
// target turned out to need no companion field at all -- the option's OWN
// artifact IS the pack, so applyExplainerPublish below reads its author and
// its whole body rather than inventing anything this option was never
// given, and publishes through crew.PublishArtifact, which itself finishes
// by calling crew.Publish, the SAME state transition Commission's own draft
// already goes through by a person's hand.
func applySideEffect(db *sql.DB, opt crew.Option, t crew.Task, actor string, rec Recorder) error {
	switch opt.Class {
	case "anomaly.explain": // class:anomaly.explain
		if t.Anomaly == "" {
			return nil
		}
		return anomaly.Explain(db, t.Anomaly, opt.Summary, rec)
	case "anomaly.dismiss": // class:anomaly.dismiss
		if t.Anomaly == "" {
			return nil
		}
		return anomaly.Dismiss(db, t.Anomaly, opt.Summary, rec)
	case "anomaly.accept": // class:anomaly.accept
		if t.Anomaly == "" {
			return nil
		}
		return anomaly.Accept(db, t.Anomaly, opt.Summary, rec)
	case "driver.one-time": // class:driver.one-time
		return applyDriver(db, opt, t, "one-time")
	case "driver.recurring": // class:driver.recurring
		return applyDriver(db, opt, t, "recurring")
	case "forecast.freeze": // class:forecast.freeze
		period, err := OpenPeriod(db)
		if err != nil || period == "" {
			return err
		}
		if err := Freeze(db, period, actor); err != nil {
			return err
		}
		// C3-SPEC.md section 2: "the option's summary becomes the freeze's
		// recorded basis" -- the forecaster's own written explanation of
		// which drivers moved the number, in place of ProjectWithDrivers's
		// own generated sentence. A no-op when the option carried no
		// summary of its own.
		return SetForecastBasis(db, period, opt.Summary)
	case "period.close": // class:period.close
		period, err := OpenPeriod(db)
		if err != nil || period == "" {
			return err
		}
		if err := Close(db, period, actor); err != nil {
			return err
		}
		return queueShowbackTasks(db, period, t.Sprint)
	case "period.reopen": // class:period.reopen
		periods, err := ClosedPeriods(db)
		if err != nil {
			return err
		}
		if len(periods) == 0 {
			return fmt.Errorf("period.reopen was applied and no period is closed to reopen")
		}
		reason := opt.Summary
		if reason == "" {
			reason = "reopened by " + actor
		}
		return Reopen(db, periods[0], reason)
	case "allocation.rule": // class:allocation.rule
		return applyAllocationRule(db, opt)
	case "explainer.publish": // class:explainer.publish
		return applyExplainerPublish(db, opt, actor)
	case "data.halt": // class:data.halt
		return applyHalt(db, opt, actor, rec)
	}
	// budget.set, and every other class not named above: recorded only,
	// per this function's own comment.
	return nil
}

// applyHalt is data.halt's side effect: C9-SPEC.md section 2. The desk it
// targets travels in the option's own Needs field -- the generic option
// shape (class/summary/figure_cents/saving_cents/risk/needs/evidence) has no
// dedicated "desk" column, the same gap this file's own header names for
// allocation.rule/budget.set/explainer.publish, and Needs is the one field
// already meant to carry "what a person would have to do"; here, which
// desk. Summary is the reason -- "a halt request naming the desk and the
// reason" is roles.yaml's own owes line for this role.
//
// The owner a stale halt is later carried to (finops.Supervise) is read the
// SAME way an ordinary carried option's owner already is: tasks.owner of
// the deliverable that named it, via ownerOfOption (supervise.go), so a
// data.halt decision request and an ordinary one can never disagree about
// whose it is.
func applyHalt(db *sql.DB, opt crew.Option, actor string, rec Recorder) error {
	desk := strings.TrimSpace(opt.Needs)
	if desk == "" {
		return fmt.Errorf("data.halt option %d names no desk in its needs field", opt.Ordinal)
	}
	reason := strings.TrimSpace(opt.Summary)
	if reason == "" {
		reason = fmt.Sprintf("data.halt applied on option %d, no reason given in the deliverable", opt.Ordinal)
	}
	owner, err := ownerOfOption(db, opt)
	if err != nil {
		return err
	}
	today := time.Now().UTC().Format("2006-01-02")
	_, _, err = crew.ApplyHalt(db, desk, reason, actor, owner, today, rec)
	return err
}

// applyExplainerPublish is C8-SPEC.md section 2: the deliverable's own
// artifact is the target explainer.publish was missing. topic is the
// option's own summary ("the pack's title"), falling back to the artifact's
// own title only for the option shapes ValidateAndSaveOptions already
// allows through with an empty summary (evidence-only options), so a stamp
// never publishes an explainer with no title at all. "leadership" is not a
// roster team -- world.Teams names ten real ones -- which is exactly why
// explainers.html only links a team's name to /team/{name} when a real one
// is there: this is the one row on that page that deliberately is not.
func applyExplainerPublish(db *sql.DB, opt crew.Option, actor string) error {
	art, err := crew.GetArtifact(db, opt.Artifact)
	if err != nil {
		return err
	}
	topic := strings.TrimSpace(opt.Summary)
	if topic == "" {
		topic = art.Title
	}
	_, err = crew.PublishArtifact(db, "leadership", topic, "leadership", art.Author,
		art.Body, money.Cents(opt.FigureCents), actor)
	return err
}

// allocationRuleTarget mirrors internal/crew's own generic decode of the
// same JSON (unallocationRuleTarget there, typed only enough to validate
// shape): this is the one place that actually reads it as a rule id and a
// Method, because internal/crew cannot import this package's Method type
// without the import cycling back (this package already imports crew).
type allocationRuleTarget struct {
	RuleID int64  `json:"rule_id"`
	Method Method `json:"method"`
}

// applyAllocationRule is C2-SPEC.md section 2's own wiring: "internal/finops.Apply
// wires allocation.rule to finops.SetRule with that target", the companion
// field this class was missing when apply.go's own comment first named it
// as recorded-only for lack of one. A target that fails to decode, or is
// simply absent (an option saved before this feature existed, or a caller
// bypassing crew.ValidateAndSaveOptions the way TestApplyAnUnwiredClassIsRecordedOnly
// does), is left exactly as "recorded only" always was: no error, nothing
// invented. Once a target IS present, SetRule's own checks -- a rule id
// this store does not have, a method string it does not define -- are what
// refuse it; duplicating either check here would only risk the two
// disagreeing.
func applyAllocationRule(db *sql.DB, opt crew.Option) error {
	if len(opt.Target) == 0 {
		return nil
	}
	var tgt allocationRuleTarget
	if err := json.Unmarshal(opt.Target, &tgt); err != nil {
		return nil
	}
	if tgt.RuleID <= 0 || tgt.Method == "" {
		return nil
	}
	return SetRule(db, int(tgt.RuleID), tgt.Method)
}

// queueShowbackTasks is period.close's own statement half (C2-SPEC.md
// section 2): "one showback narration artifact per team is queued as a
// task for the desk's reporter (the existing task creation path), never
// sent by this console." One task per (source, team) row of the period
// JUST frozen -- FrozenPeriod, not a fresh Allocate, because what is owed
// is a showback about the numbers actually closed, not whatever the estate
// has moved to by the time this runs -- assigned to "reporter-"+source
// only when that analyst is on the roster (reporter-aws, reporter-gcp,
// reporter-azure, reporter-onprem today; a desk with none, ai and saas
// today, is skipped rather than queued to nobody). sprintID is the sprint
// the period.close option's own task belongs to, so the showback tasks land
// beside the close itself rather than on whatever sprint happens to be
// "current" days or weeks later.
func queueShowbackTasks(db *sql.DB, period string, sprintID int) error {
	frozen, err := FrozenPeriod(db, period)
	if err != nil {
		return err
	}
	roster, err := crew.Roster(db)
	if err != nil {
		return err
	}
	onRoster := make(map[string]bool, len(roster))
	for _, a := range roster {
		onRoster[a.Name] = true
	}
	for _, row := range frozen.Teams {
		reporter := "reporter-" + row.Source
		if !onRoster[reporter] {
			continue
		}
		title := fmt.Sprintf("Showback narration: %s on %s, %s", row.Team, row.Source, period)
		goal := fmt.Sprintf("Write the showback narration for %s's %s spend in %s: "+
			"%s direct, %s allocated, %s total.",
			row.Team, row.Source, period, row.Direct, row.Allocated, row.Loaded())
		if _, err := crew.EnsureTask(db, sprintID, title, goal, reporter, row.Source, 0); err != nil {
			return err
		}
	}
	return nil
}

// applyDriver derives the drivers row's scope, source and window from the
// option's own task and, since DRIVER-WINDOW-SPEC.md, from the option's own
// target: the anomaly it came from when it has one (the service as scope,
// the desk as source), or the task's desk with a wide scope otherwise.
//
// The window itself never comes from the wall clock any more.
// internal/detect.Driver.Covers has no periodicity column anywhere -- the
// window IS the extent of the rhythm -- so a recurring driver with a
// one-day window is a contradiction the store cannot see: expected on one
// day, repeating nowhere (DRIVER-WINDOW-SPEC.md section 1, found by Yurii
// reading this function while C3 landed ProjectWithDrivers). For
// driver.one-time on a task WITH an anomaly, that anomaly's own day is the
// window's only source, target or none ("that day IS the driver, nothing to
// ask", section 2) -- crew.ValidateAndSaveOptions already refuses a target
// volunteered there at save time, and this is the same rule held again here
// for an option that reached Apply by bypassing it. Every other case reads
// the window from opt.Target; a driver.recurring option, or a
// driver.one-time one with no anomaly, that reaches here with no target (an
// option saved before this change, or a caller bypassing validation) writes
// no drivers row and returns a descriptive error instead -- the same
// "real error, no side effect, nothing invented" shape this function
// already held one line below for a task with no desk.
func applyDriver(db *sql.DB, opt crew.Option, t crew.Task, kind string) error {
	scope, source := "*", t.Desk
	var an anomaly.Anomaly
	var hasAnomaly bool
	if t.Anomaly != "" {
		var err error
		an, err = anomaly.Get(db, t.Anomaly)
		hasAnomaly = err == nil
	}
	if hasAnomaly {
		scope, source = an.Service, an.Source
	}
	if source == "" {
		return fmt.Errorf("driver.%s was applied on a task with no desk to write it against", kind)
	}

	var start, end string
	if kind == "one-time" && hasAnomaly {
		start, end = an.Day, an.Day
	} else {
		tgt, ok, err := decodeDriverTarget(opt.Target)
		if err != nil {
			return fmt.Errorf("driver.%s's target %w", kind, err)
		}
		if !ok {
			// The "and no anomaly" clause is appended as a plain string, not
			// folded into the Errorf format string itself: staticcheck (CI,
			// pinned 2026.2.1) refuses a dynamic format string in a
			// printf-style call, and building the sentence in two Errorf
			// verbs keeps this one a constant.
			why := ""
			if kind == "one-time" && !hasAnomaly {
				why = " (and no anomaly to take a day from)"
			}
			return fmt.Errorf("driver.%s was applied with no target naming its window%s: "+
				"recorded only, no drivers row written (option %d on artifact %d)",
				kind, why, opt.Ordinal, opt.Artifact)
		}
		start, end = tgt.Start, tgt.End
	}

	return estate.InsertDriver(db, world.Driver{
		Start: start, End: end, Scope: scope, Label: opt.Summary, Kind: kind, Source: source,
	})
}

// driverTarget mirrors internal/crew's own generic decode of the same JSON
// (crew.driverTarget there, unexported and checked only for shape at save
// time): this is the one place that turns it into the two date strings
// InsertDriver writes. A local, unexported type here rather than an import
// from internal/crew, the same reason this file's own allocationRuleTarget
// (once #31 merges) is local rather than shared: nothing about the decode
// needs internal/crew's own package, and a second four-line type costs less
// than an export neither side otherwise wants.
type driverTarget struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// decodeDriverTarget reads opt.Target as {start, end}. ok is false, with no
// error, for a target that is simply absent -- the caller's own "no target"
// path, not a fault. crew.ValidateAndSaveOptions already enforces the full
// shape (parses as dates, end not before start, at most 366 days) at save
// time, so a target that IS present here should already be well-formed; a
// malformed one still gets a real error rather than a silently empty
// window, because a window this function cannot read is not one it should
// guess at either -- the same argument that removed time.Now() from this
// file in the first place.
func decodeDriverTarget(raw json.RawMessage) (tgt driverTarget, ok bool, err error) {
	if len(raw) == 0 {
		return driverTarget{}, false, nil
	}
	if err := json.Unmarshal(raw, &tgt); err != nil {
		return driverTarget{}, false, fmt.Errorf("does not decode: %w", err)
	}
	if strings.TrimSpace(tgt.Start) == "" || strings.TrimSpace(tgt.End) == "" {
		return driverTarget{}, false, fmt.Errorf("carries an empty start or end")
	}
	return tgt, true, nil
}

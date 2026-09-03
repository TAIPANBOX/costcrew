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

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
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
// and the classes with no case below -- allocation.rule, budget.set,
// explainer.publish among them -- are "text only" deliberately: their
// existing functions (finops.SetRule, the estate budgets intake,
// crew.Publish) each need a structured target (a rule id, a team and month,
// an explainer id) the generic options shape
// (class/summary/figure_cents/saving_cents/risk/needs/evidence) does not
// carry and this option's own task does not supply either. Inventing one
// would be exactly "invent a number it was not given", the rule every job
// description in ROLES-2026-09.md carries under "Never". Wiring them is
// follow-up work once a companion field names the target; see this PR's
// body and the report's NOT PROVEN line.
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
		return Freeze(db, period, actor)
	case "period.close": // class:period.close
		period, err := OpenPeriod(db)
		if err != nil || period == "" {
			return err
		}
		return Close(db, period, actor)
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
	}
	// allocation.rule, budget.set, explainer.publish, and every class not
	// named above: recorded only, per this function's own comment.
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

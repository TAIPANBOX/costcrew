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
	"fmt"
	"time"

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
	if rec != nil {
		_ = rec.Emit("option_applied", actor, "info", map[string]any{
			"artifact":     opt.Artifact,
			"ordinal":      opt.Ordinal,
			"class":        opt.Class,
			"task":         taskID,
			"figure_cents": opt.FigureCents,
		}, nil)
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
// option's own task: the anomaly it came from when it has one (the service
// as scope, the desk as source, the anomaly's own day as both ends of the
// window), or the task's desk with a wide scope and today's date otherwise.
func applyDriver(db *sql.DB, opt crew.Option, t crew.Task, kind string) error {
	scope, source, day := "*", t.Desk, time.Now().UTC().Format("2006-01-02")
	if t.Anomaly != "" {
		if an, err := anomaly.Get(db, t.Anomaly); err == nil {
			scope, source, day = an.Service, an.Source, an.Day
		}
	}
	if source == "" {
		return fmt.Errorf("driver.%s was applied on a task with no desk to write it against", kind)
	}
	return estate.InsertDriver(db, world.Driver{
		Start: day, End: day, Scope: scope, Label: opt.Summary, Kind: kind, Source: source,
	})
}

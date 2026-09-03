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
		return Freeze(db, period, actor)
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
	}
	// budget.set, and every other class not named above: recorded only,
	// per this function's own comment.
	return nil
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

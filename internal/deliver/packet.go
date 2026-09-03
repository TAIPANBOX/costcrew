// Package deliver is what tools/run and tools/bench both send to a model:
// the TASK PACKET (the figures) and the prompt built around it, plus the
// single HTTP call that turns a prompt into a deliverable.
//
// It exists because Go refuses to import a "package main": tools/bench
// cannot call tools/run's unexported packet()/prompt(), and the two must
// build the IDENTICAL packet and prompt a live run would, or a bench that
// scores against them proves nothing about production. So this is the
// factoring B7-SPEC.md section 3 asks for when it says "factor the call and
// the parse out of it rather than copying them": Packet and Prompt moved
// here from tools/run/packet.go and tools/run/live.go (plus
// tools/run/mandate.go's jobDescriptionBlock, Prompt's own dependency), and
// tools/run keeps a one-line wrapper at each old call site so every existing
// caller and test is untouched. Call() -- the half that spends money -- did
// NOT move: it is entangled with tools/run's TokenFuse gateway headers
// (gatewayHeaders, gateway_test.go's 294 lines), which is a console-specific
// concern the bench has no flag for at all. Moving it would have risked that
// production path for a bench code path this agent is barred from ever
// running (B7-SPEC.md's own hard rule: -live is refused in every run this
// agent makes). tools/bench has its own small, separately-tested live caller
// instead; see tools/bench/live.go for why that is the honest trade-off.
package deliver

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// packetMaxBytes bounds the packet the same way every other fixed block in
// this prompt is bounded: a number somebody can check the actual output
// against, not a hope. 12 KiB is generous for the sections below (each is a
// handful of lines) and small next to the output cap this run already
// reserves at the model's per-token price.
const packetMaxBytes = 12 * 1024

const noFiguresSentence = "\nYou have not been given figures for this task.\n"

const truncatedNote = "\n... (cut: the packet is bounded at 12 KiB)\n"

// Packet builds the TASK PACKET, or the one sentence that stands in for it
// when an analyst holds no figures-read. Every section below is
// independently omitted when it has nothing to say, and the whole block is
// omitted (an empty string) when nothing came back at all -- additive,
// never misleading, the same rule jobDescriptionBlock already holds for a
// name no role family matches.
//
// hideDriver is B7-SPEC.md section 2's bench mode: true omits the anomaly's
// own driver: line and the whole "Drivers on this service and desk" section,
// so a bench scoring an analyst's named cause against the known driver label
// never hands that label to the analyst first. Every production caller
// passes false, which is the only value tools/run ever used before this
// flag existed and reproduces packet() byte for byte on a task with no
// driver at all.
func Packet(db *sql.DB, t crew.Task, a crew.Analyst, hideDriver bool) string {
	rights := crew.RightsFor(a.Skills, a.State)
	if !HasString(rights, "figures-read") {
		return noFiguresSentence
	}

	var sections []string

	if t.Anomaly != "" {
		if an, err := anomaly.Get(db, t.Anomaly); err == nil {
			sections = append(sections, AnomalySection(an, hideDriver))
			if s := seriesSection(db, an); s != "" {
				sections = append(sections, s)
			}
			if !hideDriver {
				if s := driversSection(db, an, t.Desk); s != "" {
					sections = append(sections, s)
				}
			}
			if s := teamMonthSection(db, an); s != "" {
				sections = append(sections, s)
			}
			if s := lastExplanationSection(db, an); s != "" {
				sections = append(sections, s)
			}
		}
	}

	if HasString(a.Skills, "exec-reporting", "showback-narration", "variance-commentary") {
		if s := reportingSection(db, t.Desk); s != "" {
			sections = append(sections, s)
		}
	}
	if HasString(a.Skills, "forecasting-commentary", "capacity-estimation", "forecast-accuracy") {
		if s := forecastingSection(db, t.Desk); s != "" {
			sections = append(sections, s)
		}
	}
	// "decision-framing" only, not "exec-reporting": exec-reporter carries
	// both (internal/world/world.go), and exec-reporting is ALSO the
	// desk reporters' own skill (reporter-aws and the rest), which is what
	// reportingSection just above already answers to. Gating executiveSection
	// on the skill the desk reporters do NOT have is what keeps the two
	// sections apart -- one desk's month against four estate-wide numbers
	// are different questions, and "management" (exec-reporter's own desk in
	// t.Desk) owns no team's spend for reportingSection to report on anyway.
	if HasString(a.Skills, "decision-framing") {
		if s := executiveSection(db); s != "" {
			sections = append(sections, s)
		}
	}

	// ownHistorySection is MEMORY (B8-SPEC.md section 2) and is appended
	// LAST, deliberately, and this is the one place that order is decided:
	// BoundBytes below trims from the END of the joined sections, so
	// whatever is appended last is what yields first once the 12 KiB cap is
	// reached. Every section above -- the anomaly, the series, the drivers,
	// the team's month, the last posted explanation, reporting, forecasting
	// -- is never trimmed to make room for memory, because memory is the
	// only thing ever appended after them. Skipped ENTIRELY when hideDriver
	// is true: a past posted deliverable's own option can name the very
	// driver a bench run is hiding (a recurring cause explained before is
	// exactly the case memory exists for), so memory of past answers on the
	// same desk is itself an answer key.
	if !hideDriver {
		if s := ownHistorySection(db, a, t.Desk); s != "" {
			sections = append(sections, s)
		}
	}

	if len(sections) == 0 {
		return ""
	}
	body := "\nTASK PACKET\n" + strings.Join(sections, "\n")
	return BoundBytes(body, packetMaxBytes)
}

// BoundBytes caps s at max bytes, never splitting a UTF-8 rune, and says so
// when it cut something: a truncated figure with no note reads as a whole
// one.
func BoundBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max - len(truncatedNote)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + truncatedNote
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

func HasString(list []string, want ...string) bool {
	for _, w := range want {
		for _, s := range list {
			if s == w {
				return true
			}
		}
	}
	return false
}

// -------------------------------------------------------------- the anomaly

func AnomalySection(an anomaly.Anomaly, hideDriver bool) string {
	var b strings.Builder
	b.WriteString("The anomaly\n")
	fmt.Fprintf(&b, "source:    %s\n", an.Source)
	if an.Team != "" {
		fmt.Fprintf(&b, "team:      %s\n", an.Team)
	}
	fmt.Fprintf(&b, "service:   %s\n", an.Service)
	fmt.Fprintf(&b, "day:       %s\n", an.Day)
	fmt.Fprintf(&b, "direction: %s\n", an.Direction)
	fmt.Fprintf(&b, "amount:    %s\n", an.Amount)
	fmt.Fprintf(&b, "baseline:  %s\n", an.Baseline)
	fmt.Fprintf(&b, "excess:    %s\n", an.Excess)
	fmt.Fprintf(&b, "z:         %.2f\n", an.Z)
	if an.Rule != "" {
		fmt.Fprintf(&b, "rule:      %s\n", an.Rule)
	}
	if an.Driver != "" && !hideDriver {
		fmt.Fprintf(&b, "driver:    %s\n", an.Driver)
	}
	if an.CausedBy != "" {
		fmt.Fprintf(&b, "caused by: %s (%s)\n", an.CausedBy, an.CausedByKind)
	}
	return b.String()
}

// -------------------------------------------------------------- the series

// seriesSection prints the 28 days before an.Day and the 7 after, marking
// each day's weekday and flagging the ones that share the anomaly's own
// weekday: detect.Find judges "a Sunday against Sundays" (internal/detect's
// own header), so that is the "day type" a reader needs to see repeated.
func seriesSection(db *sql.DB, an anomaly.Anomaly) string {
	days, vals, err := estate.SeriesDays(db, estate.SeriesKey{
		Source: an.Source, Team: an.Team, Service: an.Service})
	if err != nil || len(days) == 0 {
		return ""
	}
	idx := -1
	for i, d := range days {
		if d == an.Day {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ""
	}
	from := idx - 28
	if from < 0 {
		from = 0
	}
	to := idx + 7
	if to > len(days)-1 {
		to = len(days) - 1
	}
	anWeekday := weekdayOf(an.Day)

	var b strings.Builder
	b.WriteString("The series (28 days before, 7 after; * marks the same " +
		"weekday as the anomaly; -> marks the anomaly's own day)\n")
	for i := from; i <= to; i++ {
		wd := weekdayOf(days[i])
		mark := " "
		if wd == anWeekday {
			mark = "*"
		}
		arrow := ""
		if days[i] == an.Day {
			arrow = " ->"
		}
		fmt.Fprintf(&b, "%s %s%s  %s%s\n", days[i], wd, mark, vals[i], arrow)
	}
	return b.String()
}

func weekdayOf(day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return "???"
	}
	return t.Weekday().String()[:3]
}

// ------------------------------------------------------------- the drivers

// driversSectionWindowDays and driversSectionCap are B8-SPEC.md section 2's
// own numbers: the window that was 90 days is now 180 ("last six months"),
// and the list -- unbounded before -- is now capped, newest first, with a
// final "and N more" line when it is cut. Named constants because
// gates-have-teeth.sh's "keep 90 days" mutant case names this exact line.
const (
	driversSectionWindowDays = 180
	driversSectionCap        = 24
)

// driversSection lists registry rows on the anomaly's own service and desk,
// covering the six months ending on the anomaly's day: a row counts when its
// range reaches into that window and it started on or before the anomaly.
func driversSection(db *sql.DB, an anomaly.Anomaly, desk string) string {
	all, err := estate.Drivers(db)
	if err != nil || len(all) == 0 {
		return ""
	}
	since := dayBefore(an.Day, driversSectionWindowDays)
	matches := func(d world.Driver) bool {
		if d.Source != desk {
			return false
		}
		if d.Scope != "*" && d.Scope != an.Service {
			return false
		}
		return d.End >= since && d.Start <= an.Day
	}

	total := 0
	for _, d := range all {
		if matches(d) {
			total++
		}
	}
	if total == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("Drivers on this service and desk, last six months\n")
	shown := 0
	// estate.Drivers orders oldest first (date_start, label ASC), for the
	// drivers page's own sake; reversed here, rather than at the shared
	// reader, so this section alone reads newest first.
	for i := len(all) - 1; i >= 0 && shown < driversSectionCap; i-- {
		d := all[i]
		if !matches(d) {
			continue
		}
		fmt.Fprintf(&b, "%s to %s  %s (%s)\n", d.Start, d.End, d.Label, d.Kind)
		shown++
	}
	if total > shown {
		fmt.Fprintf(&b, "and %d more\n", total-shown)
	}
	return b.String()
}

func dayBefore(day string, n int) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, -n).Format("2006-01-02")
}

// --------------------------------------------------------- the team's month

func teamMonthSection(db *sql.DB, an anomaly.Anomaly) string {
	if an.Team == "" {
		return ""
	}
	rows, err := finops.BudgetsFor(db, an.Source, an.Day[:7])
	if err != nil {
		return ""
	}
	for _, r := range rows {
		if r.Team != an.Team {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "The team's month (%s, %s)\n", an.Team, an.Day[:7])
		fmt.Fprintf(&b, "budget:   %s\n", r.Budget)
		fmt.Fprintf(&b, "spend:    %s\n", r.Actual)
		fmt.Fprintf(&b, "variance: %s\n", r.Variance)
		return b.String()
	}
	return ""
}

// ------------------------------------------------- the last posted explanation

// lastExplanationSection finds the most recent POSTED artifact on a task
// that came from an anomaly on the same service, whichever anomaly it was:
// a written, human-approved answer for the same service is useful context
// regardless of which specific incident produced it.
func lastExplanationSection(db *sql.DB, an anomaly.Anomaly) string {
	var body, created, author string
	err := db.QueryRow(`
		SELECT ar.body, COALESCE(ar.stamped, ar.created, ''), COALESCE(ar.author,'')
		FROM artifacts ar
		JOIN tasks t ON t.id = ar.task
		JOIN anomalies an2 ON an2.id = t.anomaly
		WHERE an2.service = ? AND ar.state = 'posted'
		ORDER BY ar.stamped DESC, ar.id DESC LIMIT 1`, an.Service).
		Scan(&body, &created, &author)
	if err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("The last posted explanation on this service\n")
	fmt.Fprintf(&b, "by %s, %s\n", author, created)
	b.WriteString(trimBytes(body, 600))
	b.WriteString("\n")
	return b.String()
}

func trimBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// --------------------------------------------------- the analyst's own history

// A person on this job remembers two things between tasks: what they said
// last time, and what happened to it. `@yurii 2026-09-02`, the ask this
// section serves: analysts that "більш повною мірою замінити людей на цих
// посадах" -- B8-SPEC.md section 1.

// ownHistorySection is the last three artifacts THIS analyst posted on THIS
// desk, newest first, each with the task title, the date it was posted, the
// first 240 bytes of the body, and the fate of every option it ended in.
// Empty when the analyst has never posted here: the section is absent, not a
// header over nothing, the same rule every other section in this file
// already holds.
func ownHistorySection(db *sql.DB, a crew.Analyst, desk string) string {
	if a.Name == "" || desk == "" {
		return ""
	}
	rows, err := db.Query(`
		SELECT ar.id, t.title, COALESCE(ar.stamped, ar.created, ''), ar.body
		FROM artifacts ar
		JOIN tasks t ON t.id = ar.task
		WHERE ar.author = ? AND ar.state = 'posted' AND t.desk = ?
		ORDER BY ar.stamped DESC, ar.id DESC
		LIMIT 3`, a.Name, desk)
	if err != nil {
		return ""
	}
	defer rows.Close()

	type ownArtifact struct {
		id    int
		title string
		when  string
		body  string
	}
	var got []ownArtifact
	for rows.Next() {
		var r ownArtifact
		if err := rows.Scan(&r.id, &r.title, &r.when, &r.body); err != nil {
			return ""
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil || len(got) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("What you posted on this desk before, and what happened to it\n")
	for _, r := range got {
		fmt.Fprintf(&b, "\n%s, posted %s\n", r.title, r.when)
		b.WriteString(trimBytes(r.body, 240))
		b.WriteString("\n")
		opts, err := crew.Options(db, r.id)
		if err != nil {
			continue
		}
		for _, o := range opts {
			fmt.Fprintf(&b, "  - %s: %s (%s)\n", o.Class, trimBytes(o.Summary, 80), fateOf(db, o))
		}
	}
	return b.String()
}

// fateOf is one option's fate in words: what a person on this job would
// remember happened to what it proposed last time. The four fates
// artifact_options.state and decided_by can carry, plus "open" for one
// nobody has looked at yet.
func fateOf(db *sql.DB, o crew.Option) string {
	switch o.State {
	case crew.OptionApplied:
		return "applied by " + o.DecidedBy
	case crew.OptionRefused:
		return "refused by " + o.DecidedBy + ": " + trimBytes(o.Reason, 80)
	case crew.OptionNotChosen:
		return "not chosen (" + o.Reason + ")"
	case crew.OptionCarried:
		return "still waiting on " + waitingOwner(db, o.Artifact)
	default:
		return "open"
	}
}

// waitingOwner is who a carried option is waiting on: tasks.owner of the
// task the option's OWN deliverable answers (crew.TaskOwner via
// crew.TaskOfArtifact), the same lookup finops.ownerOfOption already uses
// for the same question from the supervisor's own side. decision_requests
// is keyed by the DECISION REQUEST's own artifact -- the supervisor's
// deliverable, crew.WriteDecisionRequest -- never by the artifact whose
// option was carried, so it is not what this reads.
func waitingOwner(db *sql.DB, artifactID int) string {
	taskID, err := crew.TaskOfArtifact(db, artifactID)
	if err != nil {
		return "unclaimed"
	}
	owner, err := crew.TaskOwner(db, taskID)
	if err != nil || owner == "" {
		return "unclaimed"
	}
	return owner
}

// ------------------------------------------------------ reporting and forecasting

// reportingSection is the desk's month for a skill whose job is to narrate
// it: totals, the practice's own allocation coverage, the top five movers
// on this desk, and how many anomalies are still open here. Shaped from
// desk() in internal/web/drill.go, the page the same data already renders
// for a person.
func reportingSection(db *sql.DB, desk string) string {
	if desk == "" {
		return ""
	}
	period, err := finops.OpenPeriod(db)
	if err != nil || period == "" {
		return ""
	}
	alloc, err := finops.Allocate(db, period)
	if err != nil {
		return ""
	}
	var total money.Cents
	var movers []finops.TeamCost
	for _, tc := range alloc.Teams {
		if tc.Source != desk {
			continue
		}
		total += tc.Loaded()
		movers = append(movers, tc)
	}
	openAnoms, _ := anomaly.List(db, anomaly.Filter{State: anomaly.Open, Source: desk})

	var b strings.Builder
	fmt.Fprintf(&b, "The desk's month (%s, %s)\n", desk, period)
	fmt.Fprintf(&b, "total:               %s\n", total)
	fmt.Fprintf(&b, "allocation coverage: %.1f%% (practice-wide)\n", alloc.Coverage)
	fmt.Fprintf(&b, "open anomalies:      %d\n", len(openAnoms))
	if len(movers) > 0 {
		b.WriteString("top movers:\n")
		for i, tc := range movers {
			if i >= 5 {
				break
			}
			fmt.Fprintf(&b, "  %-20s %s\n", tc.Team, tc.Loaded())
		}
	}
	return b.String()
}

// forecastingSection is the run-rate projection for the desk and the basis
// it was built from, plus the most recently frozen forecast and its grade
// when one has been scored.
func forecastingSection(db *sql.DB, desk string) string {
	if desk == "" {
		return ""
	}
	period, err := finops.OpenPeriod(db)
	if err != nil || period == "" {
		return ""
	}
	proj, basis, err := finops.Project(db, period)
	if err != nil {
		return ""
	}
	amt, ok := proj[desk]

	frozen, ferr := finops.Forecasts(db, period)
	var last finops.Forecast
	haveLast := false
	if ferr == nil {
		for _, f := range frozen {
			if f.Source != desk {
				continue
			}
			if !haveLast || f.Period > last.Period {
				last, haveLast = f, true
			}
		}
	}
	if !ok && !haveLast {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Forecasting (%s, %s)\n", desk, period)
	if ok {
		fmt.Fprintf(&b, "run-rate projection: %s\n", amt)
		fmt.Fprintf(&b, "basis: %s\n", basis)
	}
	if haveLast {
		fmt.Fprintf(&b, "last frozen forecast: %s for %s", last.Forecast, last.Period)
		if last.HasAct {
			fmt.Fprintf(&b, ", graded %s (%.1f%% error)\n", last.Grade, last.ErrorPct)
		} else {
			b.WriteString(", not yet scored\n")
		}
	}
	return b.String()
}

// -------------------------------------------------------- the executive pack

// executiveSection is C8-SPEC.md section 2's own words: the four KPI
// figures with last period's value and the delta, and the last three
// posted explanations on the desks whose spend moved most, so the pack's
// "why" comes from what a desk actually posted rather than from a
// template. Empty when the estate has no charges at all, the same
// "additive, never misleading" rule every other section here holds.
func executiveSection(db *sql.DB) string {
	figs, period, previous, err := finops.Executive(db)
	if err != nil || len(figs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The executive pack (%s)\n", period)
	for _, f := range figs {
		b.WriteString(executiveFigureLine(f))
	}
	if s := movedDesksSection(db, period, previous); s != "" {
		b.WriteString("\n")
		b.WriteString(s)
	}
	return b.String()
}

// executiveFigureLine is one KPI's own line. The Blocked check comes FIRST
// and returns on its own: C8-SPEC.md section 4, "a refused KPI appears as
// refused in the packet, not as zero", and gates-have-teeth.sh's "show a
// refused KPI as zero" case plants exactly this guard's removal, which
// falls through to the value branches below and prints f.Numeric's own Go
// zero value -- a real 0.0, not an absence, which is what makes the mutant
// catchable at all.
func executiveFigureLine(f finops.ExecutiveFigure) string {
	if f.Blocked != "" {
		return fmt.Sprintf("%s: refused, %s\n", f.Name, f.Blocked)
	}
	if !f.HasVal {
		return "" // neither a value nor a refusal: nothing here to say, never invented
	}
	if !f.HasPeriod {
		return fmt.Sprintf("%s: %.1f%s (no previous period)\n", f.Name, f.Numeric, f.Unit)
	}
	if !f.PrevHasVal {
		reason := f.PrevBlocked
		if reason == "" {
			reason = "no cost in that period"
		}
		return fmt.Sprintf("%s: %.1f%s (previous period: refused, %s)\n", f.Name, f.Numeric, f.Unit, reason)
	}
	return fmt.Sprintf("%s: %.1f%s (was %.1f%s, %+.1f)\n", f.Name, f.Numeric, f.Unit, f.PrevNumeric, f.Unit, f.Delta)
}

// movedDesksSection is the last three posted explanations -- any analyst's,
// any task's -- on the desk or desks whose total spend moved most between
// previous and period, ranked by the SIZE of the move either way (the
// overview page's own movers(), internal/web/pages.go, already reads "what
// changed" the same way, not "what went up"). Filled desk by desk, newest
// explanation first, stopping at three: a desk that moved the most but has
// nothing posted on it yet (the boundary C8-SPEC.md section 4 names)
// contributes nothing, and the next desk in the ranking fills the rest,
// which is why this reads "desks" and not "the desk".
func movedDesksSection(db *sql.DB, period, previous string) string {
	if previous == "" {
		return "" // nothing to compare a move against yet: the estate's first period
	}
	now, err := estate.Totals(db, period)
	if err != nil {
		return ""
	}
	was, err := estate.Totals(db, previous)
	if err != nil {
		return ""
	}

	type moved struct {
		desk  string
		delta money.Cents
	}
	var moves []moved
	for _, d := range world.Desks {
		delta := now[d.Name] - was[d.Name]
		if delta == 0 {
			continue
		}
		moves = append(moves, moved{d.Name, delta})
	}
	if len(moves) == 0 {
		return ""
	}
	// world.Desks is a fixed order, not a ranked one: sorted here, and
	// stably, so two desks tied on the size of their move keep world.Desks's
	// own order rather than whatever order the loop above happened to visit
	// them in -- the same nondeterminism invariant 7 (CLAUDE.md) already
	// guards every other ranked list in this console against.
	sort.SliceStable(moves, func(i, j int) bool {
		return moves[i].delta.Abs() > moves[j].delta.Abs()
	})

	var b strings.Builder
	shown := 0
	for _, m := range moves {
		if shown >= 3 {
			break
		}
		rows, err := lastPostedOnDesk(db, m.desk, 3-shown)
		if err != nil || len(rows) == 0 {
			continue
		}
		if shown == 0 {
			b.WriteString("The last posted explanations on the desks whose spend moved most\n")
		}
		for _, r := range rows {
			fmt.Fprintf(&b, "\n%s, %s, %s\n", r.title, r.desk, r.when)
			b.WriteString(trimBytes(r.body, 200))
			b.WriteString("\n")
		}
		shown += len(rows)
	}
	return b.String()
}

type postedRow struct {
	title, desk, when, body string
}

// lastPostedOnDesk is the last n posted deliverables on desk, any analyst,
// newest first -- the same notion of "posted explanation" this file already
// holds in lastExplanationSection and ownHistorySection, just not scoped to
// one service (lastExplanationSection) or one author (ownHistorySection):
// the executive pack cares about what the DESK posted, not who posted it.
func lastPostedOnDesk(db *sql.DB, desk string, n int) ([]postedRow, error) {
	if n <= 0 {
		return nil, nil
	}
	rows, err := db.Query(`SELECT t.title, COALESCE(ar.stamped, ar.created, ''), ar.body
		FROM artifacts ar JOIN tasks t ON t.id = ar.task
		WHERE t.desk = ? AND ar.state = 'posted'
		ORDER BY ar.stamped DESC, ar.id DESC LIMIT ?`, desk, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []postedRow
	for rows.Next() {
		var r postedRow
		if err := rows.Scan(&r.title, &r.when, &r.body); err != nil {
			return nil, err
		}
		r.desk = desk
		out = append(out, r)
	}
	return out, rows.Err()
}

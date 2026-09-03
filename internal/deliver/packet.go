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
	"regexp"
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
	// C7-SPEC.md section 2: the AI desk's own two sections, gated by the
	// skills only ai-spend and unit-econ-ai hold (world.go's roster), the
	// same convention every skill-gated section above already follows.
	// Neither takes a desk argument: ai_calls has no desk column at all --
	// it IS the AI desk's own table, by construction, the way charges.source
	// is what makes every section above this one desk-general.
	if HasString(a.Skills, "ai-spend-analysis", "token-economics", "model-routing-review") {
		if s := aiSpendSection(db); s != "" {
			sections = append(sections, s)
		}
	}
	if HasString(a.Skills, "unit-economics", "cost-per-outcome") {
		if s := unitEconomicsSection(db); s != "" {
			sections = append(sections, s)
		}
	}
	// closePackSection is C2-SPEC.md section 2's own section, appended here
	// -- after the anomaly-related sections above, before memory below --
	// deliberately: "yields before memory, after the anomaly" is that spec's
	// own words for this exact position, and BoundBytes below trims from the
	// END of the joined sections, so a section's place in this list IS its
	// place in line to be cut.
	if s := closePackSection(db, a, t); s != "" {
		sections = append(sections, s)
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

// ------------------------------------------------------------- the AI desk

// aiSpendSectionCap is C7-SPEC.md section 2's own number: top ten by cost,
// then "and N more" -- driversSection's own convention (driversSectionCap),
// reused here rather than invented anew.
const aiSpendSectionCap = 10

// aiSpendSection is ai-spend's own packet: this month's ai_calls grouped by
// agent (ResourceId) and by model, the blocked count named as the guard's
// saving rather than folded into cost (a blocked call always settles at
// zero -- the reader refuses any row that says otherwise), and the
// estimated share of x_cost_basis. Omitted entirely -- not a header over
// nothing, the convention every section in this file already holds --
// until real AI spend has landed on the desk.
func aiSpendSection(db *sql.DB) string {
	month, ok, err := finops.LatestRealAIMonth(db)
	if err != nil || !ok {
		return ""
	}
	byAgent, err := finops.AIByAgent(db, month)
	if err != nil || len(byAgent) == 0 {
		return ""
	}
	byModel, err := finops.AIByModel(db, month)
	if err != nil {
		return ""
	}
	settled, estimated, blockedBasis, err := finops.BasisCounts(db, month)
	if err != nil {
		return ""
	}

	var calls, blockedCalls int
	var total money.Micros
	for _, r := range byAgent {
		calls += r.Calls
		blockedCalls += r.BlockedCalls
		total += r.Cost
	}

	var b strings.Builder
	fmt.Fprintf(&b, "The AI desk's month (%s)\n", month)
	fmt.Fprintf(&b, "total: %s, %d calls, %d blocked (the guard's saving, never a cost: a "+
		"blocked call always settles at zero)\n", total, calls, blockedCalls)
	if n := settled + estimated; n > 0 {
		fmt.Fprintf(&b, "basis: %d settled, %d estimated (%.0f%% of the non-blocked calls), "+
			"%d blocked\n", settled, estimated, float64(estimated)/float64(n)*100, blockedBasis)
	}

	b.WriteString("\nBy agent, top ten by cost\n")
	shown := 0
	for _, r := range byAgent {
		if shown >= aiSpendSectionCap {
			break
		}
		fmt.Fprintf(&b, "  %-56s %8s  %d calls, %d blocked\n", r.Agent, r.Cost, r.Calls, r.BlockedCalls)
		shown++
	}
	if len(byAgent) > shown {
		fmt.Fprintf(&b, "and %d more\n", len(byAgent)-shown)
	}

	b.WriteString("\nBy model, top ten by cost\n")
	shown = 0
	for _, r := range byModel {
		if shown >= aiSpendSectionCap {
			break
		}
		fmt.Fprintf(&b, "  %-24s %8s  %d calls, %d blocked\n", r.Model, r.Cost, r.Calls, r.BlockedCalls)
		shown++
	}
	if len(byModel) > shown {
		fmt.Fprintf(&b, "and %d more\n", len(byModel)-shown)
	}

	return b.String()
}

// unitEconomicsSection is unit-econ-ai's own packet: cost per outcome per
// agent, for every agent with at least one x_outcome-tagged call this
// month, and one sentence naming every agent that spent and tagged none --
// said plainly rather than a cost per outcome invented for it. Omitted
// entirely until real AI spend has landed, the same convention as
// aiSpendSection above.
func unitEconomicsSection(db *sql.DB) string {
	month, ok, err := finops.LatestRealAIMonth(db)
	if err != nil || !ok {
		return ""
	}
	byAgent, err := finops.AIByAgent(db, month)
	if err != nil || len(byAgent) == 0 {
		return ""
	}
	counts, err := finops.OutcomeCountsByAgent(db, month)
	if err != nil {
		return ""
	}

	var withOutcome []string
	var noOutcome []string
	for _, r := range byAgent {
		if r.Calls-r.BlockedCalls == 0 {
			continue // nothing spent; not a gap, nothing to divide either
		}
		if n := counts[r.Agent]; n > 0 {
			perOutcome := money.Micros(int64(r.Cost) / int64(n))
			withOutcome = append(withOutcome, fmt.Sprintf("  %-56s %8s per outcome (n=%d, %s total)\n",
				r.Agent, perOutcome, n, r.Cost))
		} else {
			noOutcome = append(noOutcome, r.Agent)
		}
	}
	if len(withOutcome) == 0 && len(noOutcome) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Unit economics, cost per outcome (%s)\n", month)
	if len(withOutcome) == 0 {
		b.WriteString("  no agent's calls this month carry an outcome\n")
	}
	for _, l := range withOutcome {
		b.WriteString(l)
	}
	if len(noOutcome) > 0 {
		fmt.Fprintf(&b, "cost with no outcome header, said rather than invented, for: %s\n",
			strings.Join(noOutcome, ", "))
	}
	return b.String()
}

// ------------------------------------------------------------ the close pack

// C2-SPEC.md: a chargeback analyst's last three days of the month.
// `@yurii 2026-09-02`, the ask this section serves: "більш повною мірою
// замінити людей на цих посадах" -- a chargeback analyst's own words for the
// job, "reconcile, allocate, freeze, send the statements, answer the
// arguments."

// periodInTitle finds a YYYY-MM period inside a task's own title -- the
// same shape finops.Months and finops.Allocate already use as a period key.
// The chargeback-analyst family's single roles.yaml cadence line, "weekly,
// and the close pack monthly", covers two different kinds of task on the
// SAME analyst, and only the title tells this packet builder which one it
// is looking at: a period-naming title is the close pack, anything else is
// the family's own ordinary weekly work.
var periodInTitle = regexp.MustCompile(`\d{4}-\d{2}`)

// closePackSection is the chargeback-analyst family's own packet section
// (C2-SPEC.md section 2): allocation by method and team, coverage,
// unallocated cost with the rule ids that produced it, the true-up since
// the last close, and the invoice reconciliation once charges.invoice_id
// carries anything for the period -- one sentence saying it does not,
// otherwise. Empty, additively, when the role is not chargeback-analyst or
// the title names no period, the same rule every other section in this
// file already holds for a condition that does not apply.
func closePackSection(db *sql.DB, a crew.Analyst, t crew.Task) string {
	role, ok := crew.RoleForDesk(a.Name, a.Desk)
	if !ok || role.Family != "chargeback-analyst" {
		return ""
	}
	period := periodInTitle.FindString(t.Title)
	if period == "" {
		return ""
	}

	alloc, err := finops.Allocate(db, period)
	if err != nil {
		return ""
	}
	rules, err := finops.Rules(db)
	if err != nil {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "The close pack, %s\n", period)

	b.WriteString("\nAllocation rules in effect (method by desk and category)\n")
	for _, r := range rules {
		fmt.Fprintf(&b, "rule %d: %s / %s -> %s\n", r.ID, r.Source, r.Category, finops.MethodNote(r.Method))
	}

	b.WriteString("\nBy team: direct, allocated, loaded\n")
	for _, tc := range alloc.Teams {
		fmt.Fprintf(&b, "%s / %s: %s direct, %s allocated, %s loaded\n",
			tc.Source, tc.Team, tc.Direct, tc.Allocated, tc.Loaded())
	}
	fmt.Fprintf(&b, "coverage: %.1f%%\n", alloc.Coverage)

	fmt.Fprintf(&b, "\nUnallocated: %s\n", alloc.Unallocated)
	if pots, perr := finops.UnallocatedPots(db, period); perr == nil {
		for _, p := range pots {
			ruleName := "no rule"
			if p.RuleID != 0 {
				ruleName = fmt.Sprintf("rule %d", p.RuleID)
			}
			fmt.Fprintf(&b, "%s / %s: %s, %s (%s)\n", p.Source, p.Category, p.Amount, ruleName, p.Reason)
		}
	}

	b.WriteString("\nTrue-up since the last close\n")
	if trueUp, _, terr := finops.TrueUpFor(db, period); terr == nil {
		switch {
		case len(trueUp) > 0:
			for _, tu := range trueUp {
				fmt.Fprintf(&b, "%s / %s: %s then, %s now (%s)\n",
					tu.Source, tu.Team, tu.Frozen, tu.Now, tu.Delta)
			}
		default:
			if frozen, ferr := finops.FrozenPeriod(db, period); ferr == nil && frozen.Closed {
				b.WriteString("nothing has moved since the close.\n")
			} else {
				b.WriteString("no previous close to true up against.\n")
			}
		}
	}

	b.WriteString("\nInvoice reconciliation\n")
	if invoices, uncovered, has, ierr := finops.InvoiceReconciliation(db, period); ierr == nil {
		if !has {
			b.WriteString("no invoice column is loaded.\n")
		} else {
			for _, inv := range invoices {
				fmt.Fprintf(&b, "invoice %s: %s\n", inv.InvoiceID, inv.Amount)
			}
			if uncovered != 0 {
				fmt.Fprintf(&b, "not tied to any invoice: %s\n", uncovered)
			}
		}
	}

	return b.String()
}

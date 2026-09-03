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
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
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

// driversSection lists registry rows on the anomaly's own service and desk,
// covering the 90 days ending on the anomaly's day: a row counts when its
// range reaches into that window and it started on or before the anomaly.
func driversSection(db *sql.DB, an anomaly.Anomaly, desk string) string {
	all, err := estate.Drivers(db)
	if err != nil || len(all) == 0 {
		return ""
	}
	since := dayBefore(an.Day, 90)

	var b strings.Builder
	b.WriteString("Drivers on this service and desk, last 90 days\n")
	n := 0
	for _, d := range all {
		if d.Source != desk {
			continue
		}
		if d.Scope != "*" && d.Scope != an.Service {
			continue
		}
		if d.End < since || d.Start > an.Day {
			continue
		}
		fmt.Fprintf(&b, "%s to %s  %s (%s)\n", d.Start, d.End, d.Label, d.Kind)
		n++
	}
	if n == 0 {
		return ""
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

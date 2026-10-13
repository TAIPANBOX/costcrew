// Package detect finds irregularities in a daily cost series.
//
// Three properties decide whether an anomaly queue is used or ignored, and all
// three are choices rather than defaults:
//
//   - It is two-sided. A fall matters as much as a rise: a feed that stopped
//     delivering, a workload switched off without anyone noticing, and a
//     silent data-quality failure all look like a drop, and a one-sided
//     detector never sees any of them.
//   - It compares like with like. A Sunday is judged against Sundays. Without
//     that, a weekly rhythm is reported as an incident every weekend and the
//     queue becomes noise within a month.
//   - It ranks by money, not by z. A four-sigma deviation worth three dollars
//     is real, true, and not worth a person's morning.
//
// The baseline is a median and a median absolute deviation, not a mean and a
// standard deviation, so last month's spike does not raise the bar for this
// month's.
package detect

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

type Point struct {
	Day    string
	Amount money.Cents
}

// Config is deliberately explicit. Every value is a judgement somebody has to
// be able to argue with, so none of them is hidden in the code.
type Config struct {
	Window   int         // days of history the baseline is drawn from
	ZCut     float64     // how far out is far enough
	MinMove  money.Cents // the money floor: below this, nobody is woken up
	MinBase  money.Cents // a fall is only meaningful against a baseline worth losing
	Directio []Direction

	// CoolDays is how long one incident owns its series.
	//
	// Without it a STEP is reported over and over. A step raises the level
	// permanently, the rolling median needs the whole window to catch up, and
	// in the meantime the threshold is crossed again every few days. Measured
	// on the fixture: one step produced six entries across two weeks. Six rows
	// about one incident is how a queue stops being read.
	//
	// So a finding owns its series in that direction for this long, and the
	// day kept is the FIRST one, because when it started is the fact somebody
	// investigating actually needs.
	CoolDays int
}

type Direction string

const (
	Up   Direction = "up"
	Down Direction = "down"
)

func Default() Config {
	return Config{
		Window: 28,
		ZCut:   3.5,
		// Fifty dollars. The long tail of an estate is full of services that
		// double from eighty cents to three, and a queue full of those is a
		// queue nobody opens.
		MinMove:  money.Cents(50_00),
		MinBase:  money.Cents(5_00),
		Directio: []Direction{Up, Down},
		// Three weeks. Long enough to swallow a step's aftershocks, short
		// enough that a genuinely separate incident next month is its own.
		CoolDays: 21,
	}
}

// Driver is a registry entry that explains spend on a date range.
//
// Expected is set only for kind "announced": the signed change of the
// service's daily cost against its clean baseline (ANNOUNCED-DRIVER-SPEC.md
// section 4). nil for every other kind.
type Driver struct {
	Start, End, Scope, Label, Kind string
	Expected                       *money.Cents
}

// Covers reports whether this driver applies to a service on a day, and
// whether it makes the day EXPECTED rather than merely explained.
//
// The distinction is the useful one. A one-time driver annotates an anomaly
// and never hides it: somebody still has to confirm the migration cost what it
// was supposed to. A recurring driver describes a rhythm that is part of
// normal operation, and reporting it every week trains people to close the
// queue without reading it.
func (d Driver) Covers(service, day string) (applies, expected bool) {
	if d.Scope != "*" && d.Scope != service {
		return false, false
	}
	if day < d.Start || day > d.End {
		return false, false
	}
	return true, d.Kind == "recurring"
}

type Finding struct {
	Day       string
	Direction Direction
	Amount    money.Cents
	Baseline  money.Cents
	Excess    money.Cents // signed: what it added or removed
	Z         float64
	Driver    string // registry label, when one applies
	Rule      string // the test, in words, for the page to print
}

// Find returns the irregularities in one series, largest by money first.
//
// The series must be a contiguous run of days. Gaps are the caller's problem
// to fill with zeroes, because only the caller knows whether a missing day
// means nothing was spent or nothing was delivered, and those are different
// facts.
func Find(points []Point, service string, drivers []Driver, cfg Config) []Finding {
	if len(points) < cfg.Window+2 {
		return nil
	}
	wants := map[Direction]bool{}
	for _, d := range cfg.Directio {
		wants[d] = true
	}

	days := make([]time.Time, len(points))
	for i, p := range points {
		t, err := time.Parse("2006-01-02", p.Day)
		if err != nil {
			return nil
		}
		days[i] = t
	}

	var found []Finding
	for i := cfg.Window; i < len(points); i++ {
		w := cfg.Window
		if half := len(points) / 2; w > half {
			w = half
		}
		if w < 7 {
			w = 7
		}
		lo := i - w
		if lo < 0 {
			lo = 0
		}

		// An announced decision moves the history before its start by its
		// amount, so the baseline is at the new level from the first day it
		// lands (ANNOUNCED-DRIVER-SPEC.md section 4). The day is then judged by
		// the same rule as every other day.
		window, step, names, earliest := shiftForAnnounced(points[lo:i], service, points[i].Day, drivers)
		hist := sameDayType(window, days[lo:i], days[i])
		if len(hist) < 5 {
			// Not enough of the same kind of day yet, so fall back to the
			// plain window rather than reporting on a baseline of two.
			hist = amounts(window)
		}
		med := median(hist)
		mad := medianAbsDev(hist, med)
		if mad == 0 {
			// A perfectly flat history: any move is infinitely many
			// deviations, which is not a useful thing to say. One cent of
			// spread keeps the arithmetic finite and the ranking honest.
			mad = 1
		}

		amt := points[i].Amount
		excess := amt - med
		dir := Up
		if excess < 0 {
			dir = Down
		}
		if !wants[dir] {
			continue
		}
		z := float64(excess) / (1.4826 * float64(mad))
		if math.Abs(z) < cfg.ZCut {
			continue
		}
		if excess.Abs() < cfg.MinMove {
			continue
		}
		if dir == Down && med < cfg.MinBase {
			continue
		}

		label, expected := explain(drivers, service, points[i].Day)
		if expected {
			continue
		}
		if label == "" {
			// An announcement that starts a few days later is a hint for the
			// finding, never a reason to drop it.
			label = earlyHint(drivers, service, points[i].Day)
		}
		rule := ruleText(cfg)
		if step != 0 {
			// The facts the detector has: the announced step and where it
			// starts, the day's amount, and the expected level, which is the
			// shifted median on every day (a window already past the start is
			// not shifted, and its median is the new level already). No move is
			// derived from a level the step is then taken away from again.
			rule = "differs from announced decision " + names +
				" (expected " + step.String() + "/day from " + earliest + "): " +
				amt.String() + " against an expected " + med.String()
		}
		found = append(found, Finding{
			Day: points[i].Day, Direction: dir, Amount: amt, Baseline: med,
			Excess: excess, Z: z, Driver: label,
			Rule: rule,
		})
	}

	// An incident rarely respects midnight, so a run of consecutive days is
	// one event. The first day is kept because that is when it started, which
	// is the fact somebody investigating actually needs.
	found = coalesce(found, cfg.CoolDays)

	sort.SliceStable(found, func(a, b int) bool {
		return found[a].Excess.Abs() > found[b].Excess.Abs()
	})
	return found
}

func ruleText(cfg Config) string {
	return "daily spend more than " +
		trimFloat(cfg.ZCut) + " robust deviations from the median of the last " +
		itoa(cfg.Window) + " days of the same day type, and at least " +
		cfg.MinMove.String() + " away from it"
}

func explain(drivers []Driver, service, day string) (label string, expected bool) {
	for _, d := range drivers {
		applies, exp := d.Covers(service, day)
		if !applies {
			continue
		}
		if exp {
			return d.Label, true
		}
		label = d.Label
	}
	return label, false
}

// shiftForAnnounced returns the history with every day before an announced
// driver's start moved by that driver's amount, for each announced driver that
// covers day on service (ANNOUNCED-DRIVER-SPEC.md section 4), the summed step,
// the labels of those drivers, and the earliest of their starts. Two decisions
// on one service in one window add up: the step is their sum, not the largest
// of them. The input is not modified.
func shiftForAnnounced(hist []Point, service, day string, drivers []Driver) ([]Point, money.Cents, string, string) {
	out := append([]Point(nil), hist...)
	var step money.Cents
	var labels []string
	earliest := ""
	for _, d := range drivers {
		if d.Expected == nil {
			continue
		}
		if applies, _ := d.Covers(service, day); !applies {
			continue
		}
		step += *d.Expected
		labels = append(labels, d.Label)
		if earliest == "" || d.Start < earliest {
			earliest = d.Start
		}
		for k := range out {
			if out[k].Day < d.Start {
				out[k].Amount += *d.Expected
			}
		}
	}
	return out, step, strings.Join(labels, ", "), earliest
}

// earlyHintDays is how far before an announced driver's start a finding still
// carries that driver's label as a hint. It is a guess, and stays one until a
// month of real decisions has been read (ANNOUNCED-DRIVER-SPEC.md section 10).
// The hint never suppresses a finding.
const earlyHintDays = 7

// earlyHint names an announced driver on service that starts within
// earlyHintDays after day.
func earlyHint(drivers []Driver, service, day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return ""
	}
	limit := t.AddDate(0, 0, earlyHintDays).Format("2006-01-02")
	for _, d := range drivers {
		if d.Expected != nil && (d.Scope == service || d.Scope == "*") && d.Start > day && d.Start <= limit {
			return d.Label + " (announced from " + d.Start + ")"
		}
	}
	return ""
}

// sameDayType keeps only the history that is the same kind of day as the one
// being judged: weekend against weekend, weekday against weekday.
func sameDayType(points []Point, days []time.Time, target time.Time) []money.Cents {
	weekend := func(t time.Time) bool {
		return t.Weekday() == time.Saturday || t.Weekday() == time.Sunday
	}
	want := weekend(target)
	var out []money.Cents
	for i, p := range points {
		if weekend(days[i]) == want {
			out = append(out, p.Amount)
		}
	}
	return out
}

func amounts(points []Point) []money.Cents {
	out := make([]money.Cents, len(points))
	for i, p := range points {
		out[i] = p.Amount
	}
	return out
}

func median(v []money.Cents) money.Cents {
	if len(v) == 0 {
		return 0
	}
	s := append([]money.Cents(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	// Integer mean of the two middle values, rounded down, which keeps the
	// median in cents rather than reintroducing a fraction.
	return (s[n/2-1] + s[n/2]) / 2
}

func medianAbsDev(v []money.Cents, med money.Cents) money.Cents {
	d := make([]money.Cents, len(v))
	for i, x := range v {
		d[i] = (x - med).Abs()
	}
	return median(d)
}

// coalesce collapses one incident into one finding.
//
// An incident rarely respects midnight, and a step does not respect the month.
// Findings in the same direction within the cooling period are one event: the
// earliest day, because that is when it started, and the largest excess,
// because that is what it was worth at its peak.
func coalesce(f []Finding, coolDays int) []Finding {
	if len(f) < 2 {
		return f
	}
	if coolDays < 1 {
		coolDays = 1
	}
	sort.Slice(f, func(a, b int) bool { return f[a].Day < f[b].Day })

	var out []Finding
	// The head of each open incident, per direction, so a fall during a
	// sustained rise is still its own event rather than being swallowed.
	head := map[Direction]int{}
	for _, cur := range f {
		i, open := head[cur.Direction]
		if open {
			p, _ := time.Parse("2006-01-02", out[i].Day)
			c, _ := time.Parse("2006-01-02", cur.Day)
			if c.Sub(p) <= time.Duration(coolDays)*24*time.Hour {
				if cur.Excess.Abs() > out[i].Excess.Abs() {
					day := out[i].Day
					out[i] = cur
					out[i].Day = day
				}
				continue
			}
		}
		out = append(out, cur)
		head[cur.Direction] = len(out) - 1
	}
	return out
}

func trimFloat(f float64) string {
	s := []byte(nil)
	s = appendFloat(s, f)
	return string(s)
}

func appendFloat(b []byte, f float64) []byte {
	i := int64(f)
	frac := int64(math.Round((f - float64(i)) * 10))
	b = appendInt(b, i)
	if frac != 0 {
		b = append(b, '.')
		b = appendInt(b, frac)
	}
	return b
}

func itoa(i int) string { return string(appendInt(nil, int64(i))) }

func appendInt(b []byte, i int64) []byte {
	if i < 0 {
		b = append(b, '-')
		i = -i
	}
	if i >= 10 {
		b = appendInt(b, i/10)
	}
	return append(b, byte('0'+i%10))
}

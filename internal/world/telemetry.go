package world

import (
	"fmt"
	"sort"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// Utilisation is what a machine actually did, as opposed to what it cost.
//
// Cost alone cannot tell you whether something is the wrong size. A box at
// four percent CPU and a box at eighty percent bill identically, and only one
// of them is a finding. This is the evidence half of a rightsizing case, and
// without it a recommendation is a guess with a dollar sign on it.
type Utilisation struct {
	Source   string
	Service  string
	Team     string
	Resource string
	Kind     string // compute, database, accelerator
	P95CPU   float64
	P95Mem   float64
	Days     int
	Monthly  money.Cents // what it costs now
	Advice   string      // the smaller size, when there is one
	Saving   money.Cents // what the smaller size would cost less
	Why      string
}

type Licence struct {
	Vendor   string
	Product  string
	Team     string
	Issued   int
	Active30 int // signed in at least once in the last thirty days
	PerSeat  money.Cents
	Renews   string
	Term     string
	Note     string

	// Imported only (internal/finops.Licences, reading what the saas-seats
	// connector in internal/connectors wrote): zero on every generated row,
	// because the fixture invents no vendor contract to read a notice
	// period, a term length or an active-window size off of. Active30 is
	// still what an imported row's own "seats active" count is kept in --
	// one field either source can be read through, rather than a second
	// Idle()/Waste() pair -- even though the window an import measured
	// against is not always literally thirty days; ActiveWindowDays says
	// what it actually was.
	ActiveWindowDays int
	TermMonths       int
	NoticeDays       int
}

func (l Licence) Idle() int { return l.Issued - l.Active30 }

// Waste is money in seats nobody signed into. Monthly, at the per-seat price
// actually being paid.
func (l Licence) Waste() money.Cents { return l.PerSeat * money.Cents(l.Idle()) }

// NoticeDeadline is the last day to act before the renewal takes effect:
// NoticeDays before Renews. Meaningful only on an imported row -- a
// generated one carries NoticeDays zero, which reads as "notice due on the
// renewal date itself" rather than as "no notice period exists", so a
// caller with a generated row has no business asking this at all. An
// unparseable or empty Renews returns "" rather than a wrong date.
func (l Licence) NoticeDeadline() string {
	d, err := time.Parse("2006-01-02", l.Renews)
	if err != nil {
		return ""
	}
	return d.AddDate(0, 0, -l.NoticeDays).Format("2006-01-02")
}

// Commitment is a discount bought in advance: cheaper per hour, and only if
// you use it.
type Commitment struct {
	Source  string
	Name    string
	Kind    string // savings-plan, reserved, cud
	Hourly  money.Cents
	Used    float64 // percentage of what was committed
	Expires string
	Term    string
	Note    string
}

// Waterline is the eighty percent line: below it, the discount is costing
// more than it saves.
const Waterline = 80.0

func (c Commitment) BelowWaterline() bool { return c.Used < Waterline }

// Wasted is what is being paid for and not used, per month.
func (c Commitment) Wasted() money.Cents {
	if c.Used >= 100 {
		return 0
	}
	idle := (100 - c.Used) / 100
	return money.Cents(float64(c.Hourly) * 730 * idle)
}

// ExpiringWithin is the calendar question: what has to be decided soon.
func ExpiringWithin(days int, from string) []Commitment {
	ref, err := time.Parse("2006-01-02", from)
	if err != nil {
		return nil
	}
	var out []Commitment
	for _, c := range Commitments {
		e, err := time.Parse("2006-01-02", c.Expires)
		if err != nil {
			continue
		}
		if d := e.Sub(ref).Hours() / 24; d >= 0 && d <= float64(days) {
			out = append(out, c)
		}
	}
	return out
}

// AIUnit is the AI desk's economics: price separated from volume.
//
// A bill that doubled because the price went up and a bill that doubled
// because twice as much was done are different facts with different answers,
// and a console that reports only dollars cannot tell them apart.
type AIUnit struct {
	Month     string
	Team      string
	Model     string
	Tokens    int64
	Cost      money.Cents
	Actions   int
	Deflected int
}

func (a AIUnit) PerMillion() money.Cents {
	if a.Tokens == 0 {
		return 0
	}
	return money.Cents(int64(a.Cost) * 1_000_000 / a.Tokens)
}

func (a AIUnit) PerAction() money.Cents {
	if a.Actions == 0 {
		return 0
	}
	return money.Cents(int64(a.Cost) / int64(a.Actions))
}

// AIUnits is derived from the same generated series the charges come from, so
// the two agree by construction rather than by a reconciliation nobody runs.
func AIUnits() []AIUnit {
	byKey := map[string]*AIUnit{}
	for _, r := range Generate() {
		if r.Source != "ai" || r.Unit != "tokens" {
			continue
		}
		k := r.Day[:7] + "|" + r.Team + "|" + r.Model
		u, ok := byKey[k]
		if !ok {
			u = &AIUnit{Month: r.Day[:7], Team: r.Team, Model: r.Model}
			byKey[k] = u
		}
		u.Tokens += int64(r.Quantity)
		u.Cost += r.Billed
	}
	// The keys, in order, rather than the map. Ranging a Go map returns a
	// different order every time by design, so a slice built that way is a
	// different slice on every call: two installs of this binary listed the AI
	// desk differently, and a sort with ties broke differently per request.
	// The key is month|team|model, which is unique, so this is a total order
	// and not a partial one that leaves ties to chance again.
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]AIUnit, 0, len(keys))
	for _, k := range keys {
		u := byKey[k]
		// Actions are the business metric the cost is judged against. Derived
		// from tokens at a fixed ratio, and the page says so: a per-action
		// figure invented from a dollar amount is not a measurement.
		u.Actions = int(u.Tokens / 4_800)
		u.Deflected = u.Actions * 38 / 100
		out = append(out, *u)
	}
	return out
}

func (a AIUnit) String() string {
	return fmt.Sprintf("%s %s %s: %d tokens, %s", a.Month, a.Team, a.Model, a.Tokens, a.Cost)
}

package world

import (
	"fmt"
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

// UtilisationRows is written out rather than derived, so a recommendation can
// be argued with by reading it. Each carries the coverage it was measured over
// AND its reason, because "downsize this" with no evidence is a sentence
// nobody acts on.
var UtilisationRows = []Utilisation{
	{"aws", "Amazon EC2", "ml-platform", "i-0a3f training-worker-04", "compute",
		6.2, 11.0, 30, money.Cents(84_000), "m6i.2xlarge from m6i.8xlarge",
		money.Cents(63_000),
		"Six percent p95 CPU over thirty days. Left running after the June training run finished."},
	{"aws", "Amazon RDS", "data-eng", "warehouse-replica-02", "database",
		9.4, 31.0, 30, money.Cents(61_000), "db.r6g.xlarge from db.r6g.4xlarge",
		money.Cents(42_000),
		"A read replica nothing reads. Connection count has been zero for 26 of 30 days."},
	{"gcp", "Compute Engine", "research", "gke-pool-burst-3", "compute",
		14.8, 22.0, 30, money.Cents(47_000), "n2-standard-8 from n2-standard-32",
		money.Cents(31_000),
		"Sized for a burst that has not happened since April."},
	{"azure", "Virtual Machines", "support-tools", "vm-tools-prod-01", "compute",
		38.5, 64.0, 30, money.Cents(29_000), "",
		0,
		"Comfortably used. Named here so the page shows what NOT downsizing looks like."},
	{"aws", "Amazon EC2", "sre-platform", "i-07bd build-runner-01", "compute",
		71.2, 58.0, 30, money.Cents(38_000), "",
		0,
		"Busy. A rightsizing page that only lists candidates hides its own false-negative rate."},
	{"ai", "GPU training cluster", "research", "gpu-node-a100-02", "accelerator",
		18.9, 44.0, 14, money.Cents(120_000), "two A100s from four",
		money.Cents(58_000),
		"Fourteen days of evidence only, because the node is new. Stated rather than " +
			"rounded up to thirty, since a shorter window is a weaker case."},
}

// Licence is a SaaS subscription: what was bought against what is used.
//
// The gap is the finding, and it is the one nobody looks at because a licence
// renews quietly and a cloud bill does not.
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
}

func (l Licence) Idle() int { return l.Issued - l.Active30 }

// Waste is money in seats nobody signed into. Monthly, at the per-seat price
// actually being paid.
func (l Licence) Waste() money.Cents { return l.PerSeat * money.Cents(l.Idle()) }

var Licences = []Licence{
	{"Zendesk", "Suite Professional", "support-tools", 60, 41, money.Cents(11_500),
		"2026-11-01", "annual", "Bought for a support team that grew by nine, not nineteen."},
	{"Datadog", "Pro", "sre-platform", 120, 113, money.Cents(2_300),
		"2027-02-01", "annual", "Close to fully used."},
	{"Figma", "Organization", "product-web", 45, 22, money.Cents(4_500),
		"2026-10-15", "annual", "Half the seats were issued to engineers who use the viewer."},
	{"GitHub", "Enterprise", "data-eng", 140, 131, money.Cents(2_100),
		"2027-01-01", "annual", ""},
	{"Salesforce", "Sales Cloud", "growth", 30, 12, money.Cents(16_500),
		"2026-09-30", "annual", "Renews in weeks. The largest single decision on this page."},
	{"NetSuite", "ERP", "finance-systems", 25, 24, money.Cents(13_200),
		"2027-03-01", "annual", ""},
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

var Commitments = []Commitment{
	{"aws", "Compute Savings Plan, 1yr", "savings-plan", money.Cents(580), 94.2,
		"2026-11-30", "1 year", "Healthy."},
	{"aws", "RDS Reserved, 3yr", "reserved", money.Cents(240), 61.5,
		"2028-04-15", "3 years", "Bought for a workload that moved to Aurora. Two years left to run."},
	{"gcp", "Committed Use Discount, 1yr", "cud", money.Cents(360), 88.1,
		"2026-09-14", "1 year", "Expires inside 30 days. Renew, resize or let it lapse."},
	{"azure", "Reserved VM Instances, 1yr", "reserved", money.Cents(210), 76.4,
		"2027-01-20", "1 year", "Just under the line and drifting down."},
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
	var out []AIUnit
	for _, u := range byKey {
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

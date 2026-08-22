package world

import (
	"hash/fnv"
	"math"
	"sort"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// The estate spans fourteen and a half months, which is what makes a
// year-on-year comparison possible and a twelve-month commitment expire
// inside the window.
const (
	FirstDay = "2025-06-01"
	LastDay  = "2026-08-15"
)

// Series is one daily cost line: a team's use of one service on one desk.
type Series struct {
	Source  string
	Team    string
	Service string
	Base    money.Cents // typical weekday spend
	Weekend float64     // weekend level as a fraction of a weekday
	Growth  float64     // annual growth, 0.20 being twenty percent a year
	Noise   float64     // day-to-day variation as a fraction of the base
	Meter   string      // AI desks carry consumption alongside cost
	Unit    string
	Model   string
}

// Catalogue is the whole estate, written out rather than generated, so that
// what the console shows can be read here and argued with.
//
// The shape of it matters as much as the size: a few large series and a long
// tail of small ones, because that is what a real account looks like and it is
// what makes a money floor necessary rather than fussy.
var Catalogue = buildCatalogue()

func buildCatalogue() []Series {
	s := []Series{
		// aws, the biggest desk
		{"aws", "ml-platform", "Amazon EC2", money.Cents(34_000), 0.72, 0.24, 0.11, "", "", ""},
		{"aws", "ml-platform", "Amazon S3", money.Cents(7_400), 0.95, 0.31, 0.05, "", "", ""},
		{"aws", "data-eng", "Amazon EC2", money.Cents(21_500), 0.68, 0.14, 0.10, "", "", ""},
		{"aws", "data-eng", "Amazon RDS", money.Cents(16_200), 0.90, 0.09, 0.06, "", "", ""},
		{"aws", "sre-platform", "Amazon EC2", money.Cents(28_800), 0.64, 0.07, 0.09, "", "", ""},
		{"aws", "sre-platform", "Amazon EKS", money.Cents(12_300), 0.81, 0.18, 0.08, "", "", ""},
		{"aws", "sre-platform", "Data Transfer", money.Cents(4_100), 0.70, 0.11, 0.14, "", "", ""},
		{"aws", "product-web", "Amazon EC2", money.Cents(15_600), 0.76, 0.16, 0.10, "", "", ""},
		{"aws", "product-mobile", "Amazon S3", money.Cents(5_900), 0.93, 0.22, 0.07, "", "", ""},
		{"aws", "growth", "CloudWatch", money.Cents(2_200), 0.88, 0.26, 0.12, "", "", ""},
		{"aws", "security", "Amazon GuardDuty", money.Cents(3_400), 0.97, 0.05, 0.04, "", "", ""},

		// gcp
		{"gcp", "research", "GKE", money.Cents(19_400), 0.66, 0.29, 0.13, "", "", ""},
		{"gcp", "research", "Compute Engine", money.Cents(11_800), 0.70, 0.21, 0.11, "", "", ""},
		{"gcp", "growth", "BigQuery", money.Cents(9_700), 0.58, 0.34, 0.19, "", "", ""},
		{"gcp", "product-web", "Cloud Run", money.Cents(6_300), 0.74, 0.44, 0.09, "", "", ""},
		{"gcp", "data-eng", "Cloud Storage", money.Cents(5_100), 0.96, 0.17, 0.04, "", "", ""},
		{"gcp", "ml-platform", "Compute Engine", money.Cents(13_900), 0.69, 0.27, 0.12, "", "", ""},

		// azure
		{"azure", "security", "Microsoft Sentinel", money.Cents(14_600), 0.94, 0.12, 0.06, "", "", ""},
		{"azure", "finance-systems", "Azure SQL", money.Cents(8_800), 0.86, 0.06, 0.05, "", "", ""},
		{"azure", "finance-systems", "Azure Functions", money.Cents(80), 0.90, 0.10, 0.22, "", "", ""},
		{"azure", "support-tools", "Virtual Machines", money.Cents(6_400), 0.78, 0.08, 0.07, "", "", ""},
		{"azure", "product-mobile", "Blob Storage", money.Cents(3_700), 0.95, 0.19, 0.05, "", "", ""},

		// on-premises, steady and slow
		{"onprem", "data-eng", "Batch cluster", money.Cents(12_900), 0.85, 0.03, 0.05, "", "", ""},
		{"onprem", "sre-platform", "Storage array", money.Cents(9_600), 0.99, 0.04, 0.03, "", "", ""},
		{"onprem", "sre-platform", "Virtualisation", money.Cents(11_200), 0.98, 0.02, 0.03, "", "", ""},
		{"onprem", "security", "Network", money.Cents(4_800), 0.99, 0.01, 0.02, "", "", ""},

		// the AI desk: the organisation's own agents
		{"ai", "product-web", "Anthropic API", money.Cents(4_900), 0.55, 0.62, 0.18,
			"output-tokens", "tokens", "claude-strong"},
		{"ai", "ml-platform", "OpenRouter", money.Cents(3_100), 0.60, 0.71, 0.21,
			"input-tokens", "tokens", "kimi-standard"},
		{"ai", "research", "GPU training cluster", money.Cents(8_700), 0.88, 0.35, 0.16,
			"gpu-hours", "hours", ""},
		{"ai", "growth", "OpenAI API", money.Cents(1_800), 0.52, 0.48, 0.24,
			"output-tokens", "tokens", "gpt-mini"},

		// saas, which barely moves until somebody buys seats
		{"saas", "support-tools", "Zendesk", money.Cents(24_000), 1.0, 0.05, 0.01, "", "", ""},
		{"saas", "sre-platform", "Datadog", money.Cents(5_300), 1.0, 0.15, 0.02, "", "", ""},
		{"saas", "product-web", "Figma", money.Cents(900), 1.0, 0.08, 0.01, "", "", ""},
		{"saas", "data-eng", "GitHub", money.Cents(1_600), 1.0, 0.06, 0.01, "", "", ""},
		{"saas", "growth", "Salesforce", money.Cents(4_100), 1.0, 0.11, 0.01, "", "", ""},
		{"saas", "finance-systems", "NetSuite", money.Cents(3_300), 1.0, 0.04, 0.01, "", "", ""},

		// The long tail. Individually trivial, collectively not, and the
		// reason a money floor has to exist: without one these fill the
		// anomaly queue and nothing else ever gets looked at.
		{"aws", "research", "Amazon SageMaker", money.Cents(940), 0.61, 0.39, 0.28, "", "", ""},
		{"aws", "finance-systems", "Amazon SES", money.Cents(120), 0.83, 0.07, 0.31, "", "", ""},
		{"gcp", "support-tools", "Cloud Logging", money.Cents(410), 0.92, 0.13, 0.19, "", "", ""},
		{"gcp", "security", "Secret Manager", money.Cents(60), 0.99, 0.03, 0.26, "", "", ""},
		{"azure", "growth", "Azure CDN", money.Cents(1_450), 0.71, 0.23, 0.17, "", "", ""},
		{"azure", "research", "Azure ML", money.Cents(5_200), 0.63, 0.41, 0.20, "", "", ""},
		{"onprem", "finance-systems", "Backup and DR", money.Cents(2_700), 0.99, 0.02, 0.03, "", "", ""},
		{"ai", "security", "Anthropic API", money.Cents(760), 0.74, 0.55, 0.23,
			"output-tokens", "tokens", "claude-strong"},
	}
	sort.Slice(s, func(i, j int) bool {
		if s[i].Source != s[j].Source {
			return s[i].Source < s[j].Source
		}
		if s[i].Team != s[j].Team {
			return s[i].Team < s[j].Team
		}
		return s[i].Service < s[j].Service
	})
	return s
}

// Row is one charge as the store holds it.
type Row struct {
	Source   string
	Day      string
	Service  string
	Team     string
	Category string
	Billed   money.Cents
	Quantity float64
	Unit     string
	Meter    string
	Model    string
}

// noise is deterministic per series and per day, so the estate is identical on
// every machine and does not depend on the order the generator walks it. A
// sequential PRNG would tie the value to iteration order, which is exactly the
// defect found in the original's budget seeding.
func noise(key string, day time.Time) float64 {
	h := fnv.New64a()
	h.Write([]byte(key))
	h.Write([]byte(day.Format("2006-01-02")))
	x := h.Sum64()
	// splitmix64, to spread the hash's low bits before they become a value
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	// A triangular distribution centred on zero: milder tails than uniform,
	// so an ordinary day does not look like an incident.
	a := float64(x&0xffffffff) / float64(1<<32)
	b := float64((x>>32)&0xffffffff) / float64(1<<32)
	return a + b - 1
}

// Shared is a cost with no team of its own: a commitment bought for a desk,
// tax on the whole bill, a credit applied to the account.
//
// The estate needs these or allocation is a page with nothing to do. They are
// also the honest part of a real bill: on most accounts somewhere between a
// tenth and a quarter of the money arrives with nobody's name on it, and what
// a FinOps team is actually asked to do is give it one.
type Shared struct {
	Source   string
	Service  string
	Category string // Purchase, Tax, Credit
	Monthly  money.Cents
	Day      int // day of the month it lands on
}

var SharedCosts = []Shared{
	{"aws", "Savings Plan", "Purchase", money.Cents(420_000), 1},
	{"aws", "Reserved Instances", "Purchase", money.Cents(180_000), 1},
	{"aws", "Tax", "Tax", money.Cents(96_000), 28},
	{"aws", "Enterprise Discount", "Credit", money.Cents(-140_000), 28},
	{"gcp", "Committed Use Discount", "Purchase", money.Cents(260_000), 1},
	{"gcp", "Tax", "Tax", money.Cents(52_000), 28},
	{"azure", "Reservations", "Purchase", money.Cents(150_000), 1},
	{"azure", "Tax", "Tax", money.Cents(38_000), 28},
	{"onprem", "Depreciation", "Purchase", money.Cents(310_000), 1},
	{"saas", "Annual platform fee", "Purchase", money.Cents(88_000), 15},
}

// Generate builds the whole estate: every series, every day, with the planted
// events applied on top, plus the shared costs that belong to nobody.
func Generate() []Row {
	first, _ := time.Parse("2006-01-02", FirstDay)
	last, _ := time.Parse("2006-01-02", LastDay)

	byKey := map[string][]Event{}
	for _, e := range Planted {
		byKey[e.Source+"|"+e.Team+"|"+e.Service] = append(
			byKey[e.Source+"|"+e.Team+"|"+e.Service], e)
	}

	var out []Row
	for _, s := range Catalogue {
		key := s.Source + "|" + s.Team + "|" + s.Service
		events := byKey[key]
		for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
			out = append(out, s.day(d, first, events, key))
		}
	}

	// Shared cost lands once a month, with no team, which is exactly what
	// makes it shared.
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		for _, sc := range SharedCosts {
			if d.Day() != sc.Day {
				continue
			}
			out = append(out, Row{
				Source: sc.Source, Day: d.Format("2006-01-02"),
				Service: sc.Service, Team: "", Category: sc.Category,
				Billed: sc.Monthly,
			})
		}
	}
	return out
}

func (s Series) day(d, first time.Time, events []Event, key string) Row {
	level := float64(s.Base)

	// Growth, compounded over the elapsed fraction of a year.
	years := d.Sub(first).Hours() / (24 * 365.25)
	level *= math.Pow(1+s.Growth, years)

	// Weekends, which is the seasonality that makes a naive detector cry wolf.
	if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
		level *= s.Weekend
	}

	// Month-end batch on the on-premises storage array: recurring, in the
	// baseline, and planted as something the detector must NOT report.
	if s.Service == "Storage array" && isMonthEnd(d) {
		level *= 1.35
	}

	level *= 1 + s.Noise*noise(key, d)

	iso := d.Format("2006-01-02")
	for _, e := range events {
		level *= e.factorOn(iso, d)
	}

	billed := money.Cents(math.Round(level))
	if billed < 0 {
		billed = 0
	}
	r := Row{
		Source: s.Source, Day: iso, Service: s.Service, Team: s.Team,
		Category: "Usage", Billed: billed,
		Unit: s.Unit, Meter: s.Meter, Model: s.Model,
	}
	if s.Unit != "" {
		// Consumption is generated from the same level rather than divided out
		// of the cost afterwards: a token count derived from a dollar amount
		// is not a measurement.
		//
		// The rate is per MODEL, not per unit. It was per unit, so every model
		// on the AI page priced at exactly 15.62 per million tokens, which
		// makes nonsense of the one thing that page is for: telling a bill
		// that rose because the price rose from one that rose because twice as
		// much was done. With one price there is only volume.
		r.Quantity = math.Round(float64(billed) * unitRate(s.Unit, s.Model))
	}
	return r
}

// unitRate is how much of a unit one cent buys.
//
// Tokens per cent, so a CHEAPER model gives more of them. The three rates put
// the models roughly an order of magnitude apart, which is where real ones
// sit, and it is what lets the AI page separate a price move from a volume
// move at all.
func unitRate(unit, model string) float64 {
	switch unit {
	case "tokens":
		switch model {
		case "claude-strong":
			return 180 // about 55.50 per million
		case "kimi-standard":
			return 640 // about 15.60 per million
		case "gpt-mini":
			return 2000 // about 5.00 per million
		}
		return 640
	case "hours":
		return 0.0004
	}
	return 0
}

// factorOn is how much this event multiplies a given day.
func (e Event) factorOn(iso string, d time.Time) float64 {
	start, err := time.Parse("2006-01-02", e.Day)
	if err != nil {
		return 1
	}
	switch e.Shape {
	case Natural:
		return 1
	case Spike, Drop:
		if iso == e.Day {
			return e.Factor
		}
		// A real incident rarely lands inside one calendar day. The next day
		// carries a third of it, which is what makes merging adjacent days
		// necessary rather than decorative.
		if d.Sub(start) == 24*time.Hour {
			return 1 + (e.Factor-1)/3
		}
	case Step:
		if !d.Before(start) {
			return e.Factor
		}
	case Ramp:
		// Reaches its factor over ninety days and stays there.
		if !d.Before(start) {
			p := d.Sub(start).Hours() / (24 * 90)
			if p > 1 {
				p = 1
			}
			return 1 + (e.Factor-1)*p
		}
	}
	return 1
}

func isMonthEnd(d time.Time) bool {
	return d.AddDate(0, 0, 1).Day() == 1
}

// RecurringDrivers are the registry entries that cover the events which are
// expected rather than merely explained.
type Driver struct {
	Start, End, Scope, Label, Kind, Source string
}

func Drivers() []Driver {
	var out []Driver
	for _, e := range Planted {
		if e.Driver == "" {
			continue
		}
		kind := "one-time"
		start, end := e.Day, e.Day
		// The weekly training window is the case that distinguishes "expected"
		// from "explained": it covers a range and repeats.
		if e.ID == "N05" || e.ID == "N04" {
			kind, start, end = "recurring", FirstDay, LastDay
		}
		out = append(out, Driver{start, end, e.Service, e.Driver, kind,
			"planted fixture, event " + e.ID})
	}
	return out
}

// DayBefore is n days before a "2006-01-02" date, as the same string.
//
// It exists so a fixture can stagger dates without every caller re-deriving
// the parse and the format, and so an unparseable input comes back unchanged
// rather than as the zero time, which would render as year one.
func DayBefore(day string, n int) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, -n).Format("2006-01-02")
}

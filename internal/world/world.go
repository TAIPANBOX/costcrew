// Package world defines the estate the console opens onto: the teams that
// spend, the desks that watch them, the analysts hired to each desk, and the
// events planted in the cost series.
//
// The planted events are the part worth explaining. The original generated
// plausible noise and then detected whatever the detector happened to find,
// which means nothing could say whether the detector was right: a missed
// anomaly and an estate with no anomalies look identical from the outside.
//
// Here every irregularity is placed on purpose and carries the answer with it,
// including the ones that must NOT be reported. A generator that only plants
// true positives cannot catch a detector that flags everything.
package world

import "github.com/TAIPANBOX/costcrew/internal/money"

// ---------------------------------------------------------------- the org

// Team is a spending team. Ten of them, because a FinOps console with three
// teams never shows the thing that makes allocation hard, which is a shared
// cost nobody owns and two teams that both think they are the small one.
type Team struct {
	Name    string
	Unit    string // the business unit chargeback rolls up to
	Cadence string // how often they want to hear from FinOps
}

var Teams = []Team{
	{"ml-platform", "engineering", "weekly"},
	{"data-eng", "engineering", "weekly"},
	{"product-web", "product", "weekly"},
	{"product-mobile", "product", "fortnightly"},
	{"sre-platform", "engineering", "weekly"},
	{"security", "risk", "monthly"},
	{"growth", "commercial", "fortnightly"},
	{"finance-systems", "corporate", "monthly"},
	{"research", "engineering", "monthly"},
	{"support-tools", "corporate", "monthly"},
}

// Desk is a place spend comes from and an analyst watches.
type Desk struct {
	Name     string
	Kind     string // cloud, on-premises, ai, saas
	Currency string
}

var Desks = []Desk{
	{"aws", "cloud", "USD"},
	{"gcp", "cloud", "USD"},
	{"azure", "cloud", "USD"},
	{"onprem", "on-premises", "USD"},
	{"ai", "ai", "USD"},
	{"saas", "saas", "USD"},
}

// ---------------------------------------------------------------- the crew

// AgentState is what an analyst is doing right now, and every value here
// exists because some screen has to render it honestly.
type AgentState string

const (
	Active     AgentState = "active"     // on the rota
	Suspended  AgentState = "suspended"  // off it, with a reason, work untouched
	Onboarding AgentState = "onboarding" // hired, nothing finished yet
	Restricted AgentState = "restricted" // may propose, may not act
	OverGuard  AgentState = "over-guard" // this month's spend passed its ceiling
	Probation  AgentState = "probation"  // first-pass rate under the bar
)

type Agent struct {
	Name       string
	Role       string
	Desk       string
	State      AgentState
	Reason     string // required whenever State is not Active
	Engine     string
	PerTaskUSD string
	MonthlyUSD string
	Skills     []string
}

// Crew is thirty-six analysts. The variety is the point: a console that only
// ever shows healthy agents never gets its unhappy paths looked at, and the
// unhappy paths are where a governance product earns its place.
var Crew = buildCrew()

func buildCrew() []Agent {
	cheap, strong := "kimi-standard", "claude-strong"
	var c []Agent

	add := func(a Agent) { c = append(c, a) }

	// One supervisor over everything. It plans, it does not execute.
	add(Agent{"supervisor", "Crew supervisor", "management", Active, "", strong,
		"5.00", "200.00", []string{"sprint-planning", "routing", "escalation"}})

	// The three cloud desks get a full crew each.
	for _, d := range []string{"aws", "gcp", "azure"} {
		add(Agent{"investigator-" + d, "Investigator (" + d + " desk)", d, Active, "", cheap,
			"15.00", "100.00", []string{"variance-commentary", "anomaly-triage", "sql-readonly"}})
		add(Agent{"optimizer-" + d, "Optimizer (" + d + " desk)", d, Active, "", cheap,
			"18.00", "90.00", []string{"rightsizing-analysis", "commitment-modelling"}})
		add(Agent{"reporter-" + d, "Reporter (" + d + " desk)", d, Active, "", cheap,
			"20.00", "100.00", []string{"exec-reporting", "showback-narration"}})
		add(Agent{"capacity-" + d, "Capacity analyst (" + d + " desk)", d, Active, "", cheap,
			"15.00", "75.00", []string{"capacity-estimation", "forecast-accuracy"}})
		add(Agent{"triage-" + d, "Anomaly triage (" + d + " desk)", d, Active, "", cheap,
			"12.00", "120.00", []string{"anomaly-triage", "driver-classification"}})
		add(Agent{"partner-" + d, "FinOps partner (" + d + " desk)", d, Active, "", strong,
			"25.00", "80.00", []string{"stakeholder-briefing", "unit-economics"}})
	}

	// On-premises is smaller and slower moving, so it gets three.
	add(Agent{"investigator-onprem", "Investigator (on-prem desk)", "onprem", Active, "", cheap,
		"15.00", "50.00", []string{"variance-commentary", "capacity-estimation"}})
	add(Agent{"optimizer-onprem", "Optimizer (on-prem desk)", "onprem", Active, "", cheap,
		"18.00", "50.00", []string{"rightsizing-analysis", "depreciation-modelling"}})
	add(Agent{"reporter-onprem", "Reporter (on-prem desk)", "onprem", Active, "", cheap,
		"20.00", "60.00", []string{"exec-reporting", "showback-narration"}})

	// The AI desk watches what the organisation's own agents cost, which
	// includes this crew.
	add(Agent{"ai-spend", "AI spend analyst", "ai", Active, "", strong,
		"22.00", "150.00", []string{"ai-spend-analysis", "token-economics"}})
	add(Agent{"unit-econ-ai", "Unit economics (AI)", "ai", Active, "", strong,
		"22.00", "120.00", []string{"unit-economics", "cost-per-outcome"}})
	add(Agent{"triage-ai", "Anomaly triage (AI desk)", "ai", Active, "", cheap,
		"12.00", "100.00", []string{"anomaly-triage", "model-routing-review"}})

	add(Agent{"saas-manager", "SaaS portfolio manager", "saas", Active, "", cheap,
		"14.00", "70.00", []string{"licence-reconciliation", "renewal-calendar"}})
	add(Agent{"renewals", "Renewals analyst", "saas", Active, "", cheap,
		"14.00", "60.00", []string{"renewal-negotiation-prep", "vendor-benchmarking"}})

	// Org-wide analysts, not tied to one desk.
	add(Agent{"chargeback", "Chargeback analyst", "management", Active, "", cheap,
		"16.00", "90.00", []string{"allocation-rules", "period-close", "true-up"}})
	add(Agent{"commitments", "Commitment analyst", "management", Active, "", strong,
		"24.00", "110.00", []string{"commitment-modelling", "waterline-tracking"}})
	add(Agent{"forecaster", "Forecaster", "management", Active, "", strong,
		"24.00", "130.00", []string{"forecasting-commentary", "forecast-accuracy"}})
	add(Agent{"kpi-steward", "KPI steward", "management", Active, "", cheap,
		"12.00", "60.00", []string{"kpi-benchmarking", "maturity-assessment"}})
	add(Agent{"exec-reporter", "Executive reporter", "management", Active, "", strong,
		"26.00", "140.00", []string{"exec-reporting", "decision-framing"}})
	add(Agent{"governance", "Governance analyst", "management", Active, "", cheap,
		"12.00", "50.00", []string{"policy-review", "evidence-assembly"}})

	// And the ones that are not simply fine. Each carries the reason, because
	// a state without one is a state nobody can act on.
	add(Agent{"data-quality", "Data quality analyst", "management", Suspended,
		"Tagging feed from the azure desk has been stale for 9 days; paused until it returns",
		cheap, "12.00", "60.00", []string{"data-quality-checks", "tag-coverage"}})
	add(Agent{"benchmarking", "Benchmarking analyst", "management", Onboarding,
		"Hired this sprint; first deliverable not yet written", cheap,
		"12.00", "60.00", []string{"vendor-benchmarking", "peer-comparison"}})
	add(Agent{"sustainability", "Sustainability analyst", "management", Restricted,
		"May propose only: carbon data source is not yet trusted for published figures",
		cheap, "12.00", "40.00", []string{"carbon-reporting", "efficiency-metrics"}})
	add(Agent{"deep-analysis", "Deep analysis (on request)", "management", OverGuard,
		"Passed its 180.00 monthly ceiling on the 19th; further runs refused until the month turns",
		strong, "40.00", "180.00", []string{"root-cause-analysis", "scenario-modelling"}})
	add(Agent{"intake-triage", "Intake triage", "management", Probation,
		"First-pass acceptance 54% over the last two sprints, against a bar of 80%",
		cheap, "10.00", "50.00", []string{"intake-reading", "request-classification"}})
	add(Agent{"migration-watch", "Migration watch", "aws", Suspended,
		"The migration it was hired for finished on 2026-07-31; kept for its history",
		cheap, "15.00", "70.00", []string{"migration-tracking", "step-detection"}})

	return c
}

// ------------------------------------------------------------- the events

// Shape is how an irregularity looks in the series.
type Shape string

const (
	Spike Shape = "spike" // one day well above the baseline
	Drop  Shape = "drop"  // one day well below it
	Step  Shape = "step"  // a sustained change in level
	Ramp  Shape = "ramp"  // a gradual climb, which is NOT an anomaly

	// Natural changes nothing at all. The control is the series behaving
	// normally on a day a careless detector reports anyway: a Sunday, a
	// month-end batch. Planting a synthetic dip and then calling it normal
	// would test the opposite of what it claims.
	Natural Shape = "natural"
)

// Event is one planted irregularity and the answer that goes with it.
//
// Detect is the ground truth. A false entry is as valuable as a true one:
// weekends, slow growth and amounts below the money floor are exactly what a
// naive detector reports, and without them in the fixture nothing measures
// that.
type Event struct {
	ID       string
	Source   string
	Team     string
	Service  string
	Day      string // ISO date; for Step and Ramp, the day it starts
	Shape    Shape
	Factor   float64     // multiple of baseline at the peak, or the step's new level
	Excess   money.Cents // roughly what it adds or removes, for ranking
	Detect   bool        // must the detector report this?
	Driver   string      // registry label that explains it, if any
	CausedBy string      // an agent, when the spend is an agent's own
	Why      string      // why it is or is not an anomaly, in a person's words
}

// Planted is the fixture's ground truth. Fourteen events: nine that a correct
// detector must find and five it must leave alone.
var Planted = []Event{
	{
		ID: "E01", Source: "aws", Team: "ml-platform", Service: "Amazon EC2",
		Day: "2026-07-14", Shape: Spike, Factor: 5.4, Excess: money.Cents(184_000),
		Detect: true,
		Why:    "A training run left forty instances up over a weekend. Nothing in the registry explains it.",
	},
	{
		ID: "E02", Source: "gcp", Team: "research", Service: "GKE",
		Day: "2026-06-22", Shape: Spike, Factor: 4.1, Excess: money.Cents(96_500),
		Detect: true, Driver: "Quarterly model refresh, planned",
		Why: "Real and large, and the registry explains it. It is reported and annotated, never hidden.",
	},
	{
		ID: "E03", Source: "azure", Team: "security", Service: "Microsoft Sentinel",
		Day: "2026-08-03", Shape: Drop, Factor: 0.08, Excess: money.Cents(-71_200),
		Detect: true,
		Why:    "The log feed stopped delivering. A fall this steep is a data-quality incident, not a saving.",
	},
	{
		ID: "E04", Source: "onprem", Team: "data-eng", Service: "Batch cluster",
		Day: "2026-07-02", Shape: Drop, Factor: 0.35, Excess: money.Cents(-42_800),
		Detect: true, Driver: "Batch cluster decommission, tranche 1",
		Why: "A deliberate switch-off. Reported so somebody confirms the saving was intended and stuck.",
	},
	{
		ID: "E05", Source: "ai", Team: "product-web", Service: "Anthropic API",
		Day: "2026-07-28", Shape: Step, Factor: 2.6, Excess: money.Cents(238_400),
		Detect: true, CausedBy: "deep-analysis",
		Why: "An agent was moved to the strong model and left there. A step, not a spike: it does not come back down.",
	},
	{
		ID: "E06", Source: "ai", Team: "ml-platform", Service: "OpenRouter",
		Day: "2026-08-11", Shape: Spike, Factor: 6.8, Excess: money.Cents(151_900),
		Detect: true, CausedBy: "unit-econ-ai",
		Why: "A retry loop. The clearest case for a runtime kill-switch in the whole fixture.",
	},
	{
		ID: "E07", Source: "aws", Team: "product-mobile", Service: "Amazon S3",
		Day: "2026-06-09", Shape: Step, Factor: 1.9, Excess: money.Cents(88_300),
		Detect: true,
		Why:    "Lifecycle rules were removed, so nothing ages out any more. Steps are the ones people miss.",
	},
	{
		ID: "E08", Source: "gcp", Team: "growth", Service: "BigQuery",
		Day: "2026-08-05", Shape: Spike, Factor: 3.7, Excess: money.Cents(64_100),
		Detect: true,
		Why:    "One unpartitioned query scanned the whole table, repeatedly.",
	},
	{
		ID: "E09", Source: "saas", Team: "support-tools", Service: "Zendesk",
		Day: "2026-07-01", Shape: Step, Factor: 1.45, Excess: money.Cents(10_800),
		Detect: true,
		Why:    "Seats bought for a hiring plan that slipped. Money in unused licences.",
	},

	// The five that must NOT be reported. These are the fixture's real value.
	{
		ID: "N01", Source: "aws", Team: "sre-platform", Service: "Amazon EC2",
		Day: "2026-07-19", Shape: Natural, Factor: 1, Detect: false,
		Why: "A Sunday, with nothing done to it. This desk runs at 64 percent of a " +
			"weekday at the weekend, and a detector without a same-day-type baseline " +
			"reports that rhythm as an incident every single week.",
	},
	{
		ID: "N02", Source: "gcp", Team: "product-web", Service: "Cloud Run",
		Day: "2026-06-01", Shape: Ramp, Factor: 1.8, Detect: false,
		Why: "Eighty percent growth spread over three months. Real, worth a forecast, " +
			"and not an incident on any single day.",
	},
	{
		ID: "N03", Source: "azure", Team: "finance-systems", Service: "Azure Functions",
		Day: "2026-08-07", Shape: Spike, Factor: 4.2, Excess: money.Cents(310),
		Detect: false,
		Why: "Four times a baseline of eighty cents. Statistically loud, worth three dollars. " +
			"Below the money floor, and a queue full of these teaches people to ignore the queue.",
	},
	{
		ID: "N04", Source: "onprem", Team: "sre-platform", Service: "Storage array",
		Day: "2026-06-30", Shape: Natural, Factor: 1, Detect: false,
		Driver: "Month-end batch on the storage array",
		Why: "The month-end batch runs every month and the generator puts it in the " +
			"series itself. A 28-day window holds only one month-end, so the median " +
			"never learns it: the registry is what makes a monthly rhythm expected.",
	},
	{
		ID: "N05", Source: "ai", Team: "research", Service: "GPU training cluster",
		Day: "2026-07-08", Shape: Spike, Factor: 3.1, Excess: money.Cents(58_000),
		Detect: false, Driver: "Scheduled weekly training window",
		Why: "Large, and it happens every Wednesday on a published schedule. The registry " +
			"covers it as recurring, so it is expected rather than merely explained.",
	},
}

// MustDetect is the ground truth a detector is scored against.
func MustDetect() []Event {
	var out []Event
	for _, e := range Planted {
		if e.Detect {
			out = append(out, e)
		}
	}
	return out
}

// MustIgnore is the other half, and the half that catches a detector which
// simply flags everything loud.
func MustIgnore() []Event {
	var out []Event
	for _, e := range Planted {
		if !e.Detect {
			out = append(out, e)
		}
	}
	return out
}

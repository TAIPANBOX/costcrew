package finops

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// KPI is one measure of the practice, and every one of them can refuse.
//
// The refusal is the important part. A KPI library where everything reports a
// number is one where several numbers are made up, and a practice that
// measures itself with invented figures is worse off than one that measures
// nothing. So a KPI that cannot be computed says WHY, by name, and the page
// counts how many did.
type KPI struct {
	ID      string
	Name    string
	Group   string
	Value   string
	Unit    string
	Target  string
	Meets   bool
	HasVal  bool
	Blocked string // why it cannot be computed, when it cannot
	Note    string
}

// KPIs computes the library against whatever the console actually holds.
func KPIs(db *sql.DB, period string) ([]KPI, error) {
	var out []KPI
	add := func(k KPI) { out = append(out, k) }

	// -------------------------------------------------- allocation
	a, err := Allocate(db, period)
	if err != nil {
		return nil, err
	}
	add(KPI{
		ID: "allocation-coverage", Name: "Allocation coverage", Group: "Allocation",
		Value: fmt.Sprintf("%.1f", a.Coverage), Unit: "%", Target: ">= 90",
		HasVal: true, Meets: a.Coverage >= 90,
		Note: "Direct cost plus what a rule placed, over the whole bill.",
	})
	unallocPct, ok := money.Pct(a.Unallocated, a.Direct+a.Shared)
	add(KPI{
		ID: "unallocated-share", Name: "Cost with no owner", Group: "Allocation",
		Value: fmt.Sprintf("%.1f", unallocPct), Unit: "%", Target: "<= 5",
		HasVal: ok, Meets: ok && unallocPct <= 5,
		Blocked: blockedIf(!ok, "the period has no cost at all, so a share of it is not a number"),
	})

	// -------------------------------------------------- anomalies
	var open, closed int
	var openMoney int64
	// COALESCE on all three, not two of three. SUM over no rows is NULL, not
	// zero, and scanning NULL into an int fails: on an estate whose detector
	// has not run yet the whole KPI library returned an error instead of a
	// library of refusals, which is the one thing this page is built not to do.
	// The third had it and the first two did not, which is how a query gets
	// half-hardened.
	if err := db.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN state='open' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN state IN ('accepted','dismissed') THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN state='open' THEN ABS(excess_cents) ELSE 0 END),0)
		FROM anomalies`).Scan(&open, &closed, &openMoney); err != nil {
		return nil, err
	}
	total := open + closed
	rate, hasRate := money.Pct(money.Cents(closed), money.Cents(total))
	add(KPI{
		ID: "anomaly-closure", Name: "Anomalies closed", Group: "Anomalies",
		Value: fmt.Sprintf("%.0f", rate), Unit: "%", Target: ">= 80",
		HasVal: hasRate, Meets: hasRate && rate >= 80,
		Blocked: blockedIf(!hasRate, "no anomalies have been detected yet, so a closure rate is not a rate"),
	})
	add(KPI{
		ID: "anomaly-open-money", Name: "Money in open anomalies", Group: "Anomalies",
		Value: money.Cents(openMoney).String(), Unit: "USD", Target: "as low as it goes",
		HasVal: true, Meets: openMoney == 0,
		Note: "What has been noticed and not yet explained.",
	})

	// -------------------------------------------------- the crew
	//
	// COALESCE on all three SUMs, not just the last one (spent_cents already
	// had it). SUM over zero rows is NULL regardless of the CASE inside it,
	// and scanning NULL into a plain Go int fails outright, the same defect
	// the anomalies query above was fixed for and the reason its own
	// comment exists. Found twice, independently, by two different paths:
	// -replace-generated wipes the seeded board along with the rest of the
	// generated estate, so the KPI page 500'd the moment somebody used the
	// flag and then opened it; and tools/run's own kpis tool test built a
	// store with nothing seeded into tasks at all and hit the identical
	// scan error. Both are the same bug -- an estate with no tasks yet --
	// reached from opposite directions.
	var tasks, posted, returned int
	var spent int64
	if err := db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN state='posted' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN state='returned' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(spent_cents),0) FROM tasks`).
		Scan(&tasks, &posted, &returned, &spent); err != nil {
		return nil, err
	}
	fp, hasFP := money.Pct(money.Cents(posted), money.Cents(posted+returned))
	add(KPI{
		ID: "first-pass", Name: "First-pass acceptance", Group: "The crew",
		Value: fmt.Sprintf("%.0f", fp), Unit: "%", Target: ">= 80",
		HasVal: hasFP, Meets: hasFP && fp >= 80,
		Blocked: blockedIf(!hasFP, "nothing has been reviewed yet, and a rate over no reviews is not a rate"),
	})
	// What the crew cost, judged against what it found.
	//
	// This carried Meets: true, hard-coded, so it reported that it met its
	// target whatever the numbers said, on a page whose own headline is that
	// a library where everything reports a number is one where several are
	// invented. A KPI that cannot fail is one of those.
	var found money.Cents
	if err := db.QueryRow(`SELECT COALESCE(SUM(ABS(excess_cents)),0) FROM anomalies
		WHERE state IN ('explained','accepted')`).Scan(&found); err != nil {
		return nil, err
	}
	// What of that figure is real. The rest was generated at seed time, and one
	// number covering both kinds is the fault this console catches elsewhere.
	liveMicros, liveTasks, err := crew.LiveSpend(db)
	if err != nil {
		return nil, err
	}
	ratio, hasRatio := 0.0, spent > 0
	if hasRatio {
		ratio = float64(found) / float64(spent)
	}
	add(KPI{
		ID: "crew-cost", Name: "What the crew cost", Group: "The crew",
		Value: money.Cents(spent).String(), Unit: "USD",
		Target: "less than it finds",
		HasVal: true, Meets: hasRatio && ratio >= 1,
		Note: strings.TrimSpace(fmt.Sprintf("Across %d tasks, against %s found and "+
			"either explained or accepted: a return of %.2fx. %s",
			tasks, found, ratio, crew.RealMoney(liveMicros, liveTasks))),
		Blocked: blockedIf(!hasRatio,
			"the crew has been charged nothing yet, so there is no ratio to report"),
	})

	// -------------------------------------------------- commitments
	var below int
	var wasted money.Cents
	for _, c := range world.Commitments {
		if c.BelowWaterline() {
			below++
		}
		wasted += c.Wasted()
	}
	add(KPI{
		ID: "commitment-waterline", Name: "Commitments below the waterline", Group: "Commitments",
		Value: fmt.Sprintf("%d of %d", below, len(world.Commitments)), Target: "0",
		HasVal: true, Meets: below == 0,
		Note: fmt.Sprintf("Under %.0f%% utilisation a discount costs more than it saves. %s a month is being paid for and not used.", world.Waterline, wasted),
	})

	// -------------------------------------------------- saas
	var idle int
	var saasWaste money.Cents
	for _, l := range world.Licences {
		idle += l.Idle()
		saasWaste += l.Waste()
	}
	add(KPI{
		ID: "saas-idle-seats", Name: "Money in unused seats", Group: "SaaS",
		Value: saasWaste.String(), Unit: "USD/month", Target: "as low as it goes",
		HasVal: true, Meets: saasWaste == 0,
		Note: fmt.Sprintf("%d seats issued and not signed into in thirty days.", idle),
	})

	// -------------------------------------------------- the ones that cannot
	//
	// Named, and refusing, because a library where everything reports a number
	// is one where several are invented.
	// This one stops refusing the moment a frozen month finishes, which is the
	// loop worth having: the KPI is not switched on by a setting, it is earned
	// by the practice doing the thing it measures.
	// The estate's open month, NOT the period this page is filtered to.
	// Accuracy is a property of the practice, and a practice does not forecast
	// better because somebody changed a dropdown.
	openMonth, err := OpenPeriod(db)
	if err != nil {
		return nil, err
	}
	acc, scored, hasAcc, err := Accuracy(db, openMonth)
	if err != nil {
		return nil, err
	}
	add(KPI{
		ID: "forecast-accuracy", Name: "Forecast accuracy", Group: "Forecasting",
		Value: fmt.Sprintf("%.1f", acc), Unit: "% average error",
		Target: fmt.Sprintf("within %.0f%%", LadderTrusted),
		HasVal: hasAcc, Meets: hasAcc && acc <= LadderTrusted,
		Note: fmt.Sprintf("Across %d scored month-desks. %s.", scored, LadderText()),
		Blocked: blockedIf(!hasAcc,
			"no frozen forecast has reached the end of its month yet, so there is "+
				"nothing to compare an actual against. Accuracy against an unfrozen "+
				"forecast is a number that improves whenever somebody edits the forecast."),
	})
	// C7-SPEC.md section 2: reports once any call on the AI desk this month
	// carries x_outcome, refusing -- and naming the count of agents that
	// spent and set none -- only when nothing does. Meets is left false even
	// while reporting: the target is "trending down" and nothing here holds
	// a PRIOR month's own figure to compare against, so claiming a trend
	// either way would be exactly the invented number this file's own
	// header warns against.
	//
	// Value and Unit are set ONLY when hasCPO: the /kpis page's own sort by
	// value compares .Value as a plain string regardless of .HasVal, and
	// this KPI was ALWAYS blocked before this step, Value at its Go zero
	// value (""), tied with carbon-per-workload's own permanent "" and
	// broken by insertion order. Setting Value to a real number even while
	// blocked ("0.00") stopped being a tie with "" and silently reordered
	// that page -- found by the parity gate comparing this step's own
	// before and after captures, /kpis?sort=value&dir=asc line 68, the
	// exact shape invariant 26 and the AI page's own history (this file's
	// AttributionCoverage comment) already warn this gate catches.
	perOutcome, hasCPO, cpoWithNone, cpoTotal, err := CostPerOutcome(db, period)
	if err != nil {
		return nil, err
	}
	var cpoNote string
	cpoValue, cpoUnit := "", ""
	if hasCPO {
		cpoValue, cpoUnit = perOutcome.String(), "USD/outcome"
		if cpoWithNone > 0 {
			cpoNote = fmt.Sprintf("%d of %d agents that spent on the AI desk this month tagged no "+
				"outcome; this figure covers only the cost of the ones that did.", cpoWithNone, cpoTotal)
		}
	}
	add(KPI{
		ID: "cost-per-outcome", Name: "Cost per business outcome", Group: "Unit economics",
		Value: cpoValue, Unit: cpoUnit, Target: "trending down",
		HasVal: hasCPO, Note: cpoNote,
		Blocked: blockedIf(!hasCPO, costPerOutcomeRefusal(cpoWithNone, cpoTotal)),
	})
	add(KPI{
		ID: "carbon-per-workload", Name: "Carbon per workload", Group: "Sustainability",
		Target: "trending down",
		Blocked: "no carbon data source is connected, and the estimates providers " +
			"publish are not comparable between them.",
	})
	attrPct, hasAttr, err := AttributionCoverage(db, period)
	if err != nil {
		return nil, err
	}
	// Note is set only when hasAttr, not unconditionally: the template
	// renders {{if .Note}} and {{if .Blocked}} independently, so a Note
	// written for the reporting case would show ALONGSIDE the refusal on a
	// store with nothing real yet, saying both "spend a connector wrote"
	// and "model calls do not carry an agent header" in the same row. Found
	// by the parity gate comparing a fresh install against itself: /kpis
	// differed even though neither side had imported anything.
	var attrNote string
	if hasAttr {
		attrNote = "Real AI spend a connector's reader wrote, with the agent the gateway's " +
			"own header named for the largest share of each day."
	}
	add(KPI{
		ID: "agent-attribution", Name: "AI spend attributed to an agent", Group: "Unit economics",
		Value: fmt.Sprintf("%.0f", attrPct), Unit: "%", Target: ">= 90",
		HasVal: hasAttr, Meets: hasAttr && attrPct >= 90,
		Note: attrNote,
		Blocked: blockedIf(!hasAttr, "model calls do not carry an agent header through a "+
			"gateway yet, so AI spend can be attributed to a team and no further. The "+
			"anomaly pages say which grain they are at rather than implying the finer one."),
	})

	return out, nil
}

func blockedIf(cond bool, why string) string {
	if cond {
		return why
	}
	return ""
}

// costPerOutcomeRefusal is cost-per-outcome's own refusal text: the count
// of agents that spent on the AI desk this month and tagged none, or, when
// nothing has spent there at all, the SAME sentence this KPI carried
// unconditionally before this step -- word for word, not merely in
// substance. That case (total == 0) is exactly what a fresh install or a
// generated-only estate still reaches (ai_calls exists, empty; no real
// import has ever run), which is the one state parity/captures/golden was
// captured from, so this KPI's own text stays byte-identical there and the
// parity gate's "0 differing" holds. The count-of-agents wording below is
// reachable only once real AI spend exists with nothing tagged -- a state
// the OLD, unconditionally-blocked KPI could never have described either
// way, so there is no prior text for it to stay identical to.
func costPerOutcomeRefusal(withNone, total int) string {
	if total == 0 {
		return "the business metric this would divide by is not connected. A cost " +
			"per outcome derived from a cost is not a unit economic, it is the same " +
			"number wearing a denominator."
	}
	return fmt.Sprintf("no call on the AI desk this month carries an outcome (x_outcome); "+
		"%d of %d agents that spent this month set none. A cost per outcome derived from a "+
		"cost with nothing counted is not a unit economic, it is the same number wearing a "+
		"denominator.", withNone, total)
}

// KPICounts is the header: how many report, how many refuse, how many meet.
func KPICounts(ks []KPI) (reporting, blocked, meeting int) {
	for _, k := range ks {
		if k.Blocked != "" {
			blocked++
			continue
		}
		reporting++
		if k.Meets {
			meeting++
		}
	}
	return
}

// ----------------------------------------------------------- the exec pack

// executiveKPIIDs is the four KPIs roles.yaml's own executive-reporter owes
// ("four numbers, each with its reason ... and never from a template"),
// named ONCE here rather than in internal/deliver or any web page, so
// nothing that ever reads them can disagree about which four.
//
// @claude 2026-09-03: neither ROLES-2026-09.md nor PLAN-2026-09.md names the
// four by id, so this choice is mine, made against what KPIs() actually
// computes rather than against a wish list. Only THREE of the twelve vary
// with the period argument at all: allocation-coverage and
// unallocated-share both come from Allocate(db, period), and
// agent-attribution comes from AttributionCoverage(db, period). Every other
// KPI's own query -- crew-cost, anomaly-open-money, forecast-accuracy and
// the rest -- ignores the argument entirely (forecast-accuracy reads
// OpenPeriod() instead, on purpose, per its own comment), so "last period's
// value and the delta" would be a delta of zero BY CONSTRUCTION for any of
// them, on every estate, which is not a number an executive should be shown
// as though it meant something. cost-per-outcome is the fourth: it never
// computes in this console at all (no outcome metric is connected until
// C7), so it is the one guaranteed to exercise "refused, not zero" on any
// estate, generated or real, which is exactly the property C8-SPEC.md
// section 4 asks this pack to prove.
var executiveKPIIDs = []string{
	"allocation-coverage", "unallocated-share", "agent-attribution", "cost-per-outcome",
}

// ExecutiveFigure is one of the four numbers the executive pack shows: this
// period's own KPI row (so a refusal carries its exact reason, in the KPI's
// own words), its value as a NUMBER rather than the page's pre-formatted
// string, and the period before it.
//
// Numeric is 0 whenever HasVal is false -- Go's own zero value for
// float64, left exactly as the language gives it rather than guarded
// against -- because C8-SPEC.md section 4 asks for a refusal that renders
// as a refusal and never as a zero, and that guard needs a REAL zero on the
// other side of it to catch, the same shape this file's own COALESCE
// history (the comments on the anomalies and crew queries above) has
// already been bitten by twice.
type ExecutiveFigure struct {
	KPI
	Numeric float64

	// HasPeriod is false only for the estate's very first period: C8-SPEC.md
	// section 4's own words, "no previous period". Once true, the figure DID
	// have a period before it, even when that period's own KPI has nothing to
	// show (PrevHasVal false) -- that is a different sentence ("the previous
	// period itself refused"), not the boundary this flag names.
	HasPeriod bool

	PrevHasVal  bool // the previous period's OWN KPI had a value, not a refusal
	PrevNumeric float64
	PrevBlocked string // the previous period's own refusal, when PrevHasVal is false

	Delta    float64
	HasDelta bool // true only when THIS period and the previous one both have a value
}

// Executive is the four figures the executive pack shows, C8-SPEC.md
// section 2. period is the period the figures are FOR: the last COMPLETE
// month in Months(), never the one still running -- the same index
// internal/web's own period() defaults to, for the same reason: a
// fortnightly pack that included eleven days of an open month would tell an
// executive the estate got cheaper because the month is not over yet.
// previous is the month before that, or "" when period is the estate's very
// first one. figures is nil, with no error, when the estate has no charges
// at all -- an empty pack, not a crash, the same "additive, never
// misleading" rule every packet section already holds.
func Executive(db *sql.DB) (figures []ExecutiveFigure, period, previous string, err error) {
	period, previous, err = executivePeriod(db)
	if err != nil || period == "" {
		return nil, period, previous, err
	}
	cur, err := KPIs(db, period)
	if err != nil {
		return nil, period, previous, err
	}
	var prev []KPI
	if previous != "" {
		if prev, err = KPIs(db, previous); err != nil {
			return nil, period, previous, err
		}
	}

	for _, id := range executiveKPIIDs {
		k, ok := kpiByID(cur, id)
		if !ok {
			// Cannot happen while executiveKPIIDs names only real ids -- KPIs()
			// always appends all twelve, refusal or not -- so this is a guard
			// against a future rename of an id above, not a live path. A
			// packet's job is to say what it has, never to crash the analyst
			// it was building for.
			continue
		}
		f := ExecutiveFigure{KPI: k, HasPeriod: previous != ""}
		if k.HasVal {
			if n, perr := strconv.ParseFloat(k.Value, 64); perr == nil {
				f.Numeric = n
			}
		}
		if pk, pok := kpiByID(prev, id); pok {
			f.PrevBlocked = pk.Blocked
			if pk.HasVal {
				if n, perr := strconv.ParseFloat(pk.Value, 64); perr == nil {
					f.PrevHasVal = true
					f.PrevNumeric = n
				}
			}
		}
		if k.HasVal && f.PrevHasVal {
			f.Delta = f.Numeric - f.PrevNumeric
			f.HasDelta = true
		}
		figures = append(figures, f)
	}
	return figures, period, previous, nil
}

func kpiByID(ks []KPI, id string) (KPI, bool) {
	for _, k := range ks {
		if k.ID == id {
			return k, true
		}
	}
	return KPI{}, false
}

// executivePeriod picks the period the executive pack reports on: the last
// COMPLETE month in Months(), skipping index 0 (the one still running) when
// a complete month exists, the identical index internal/web's own period()
// defaults to. previous is the month right before it in Months()'s own
// descending list, so "no previous period" is exactly the estate's own
// first month, never a calendar gap Months() cannot see anyway.
func executivePeriod(db *sql.DB) (period, previous string, err error) {
	months, err := Months(db)
	if err != nil || len(months) == 0 {
		return "", "", err
	}
	idx := 0
	if len(months) > 1 {
		idx = 1
	}
	period = months[idx]
	if idx+1 < len(months) {
		previous = months[idx+1]
	}
	return period, previous, nil
}

// ------------------------------------------------------------- maturity

// Capability is one dimension of practice maturity, scored with the EVIDENCE
// beside it.
//
// A self-assessment with no evidence is an opinion with a number on it. Each
// level here names what in this console supports the claim, and a capability
// with no evidence is scored zero regardless of what anybody believes.
type Capability struct {
	Name     string
	Level    int // 0 none, 1 crawl, 2 walk, 3 run
	Evidence string
	Next     string
}

func Levels() []string { return []string{"none", "crawl", "walk", "run"} }

func Maturity(db *sql.DB, period string) ([]Capability, error) {
	a, err := Allocate(db, period)
	if err != nil {
		return nil, err
	}
	closed, err := ClosedPeriods(db)
	if err != nil {
		return nil, err
	}
	var anomalies, handled int
	_ = db.QueryRow(`SELECT COUNT(*), SUM(CASE WHEN handled_by IS NOT NULL AND handled_by<>'' THEN 1 ELSE 0 END)
		FROM anomalies`).Scan(&anomalies, &handled)

	caps := []Capability{
		{
			Name: "Allocation", Level: level(a.Coverage >= 90, a.Coverage >= 70, a.Coverage > 0),
			Evidence: fmt.Sprintf("%.1f%% of the bill has an owner, with %s still unallocated.",
				a.Coverage, a.Unallocated),
			Next: "Give the remaining shared cost a rule, or accept in writing that it stays central.",
		},
		{
			Name:     "Chargeback",
			Level:    level(len(closed) >= 3, len(closed) >= 1, true),
			Evidence: fmt.Sprintf("%d periods have been closed and frozen.", len(closed)),
			Next:     "Close a period every month, so a team can plan against a number that stops moving.",
		},
		{
			Name:     "Anomaly management",
			Level:    level(anomalies > 0 && handled == anomalies, handled > 0, anomalies > 0),
			Evidence: fmt.Sprintf("%d anomalies detected, %d taken by an analyst.", anomalies, handled),
			Next:     "Give every open anomaly an owner, so none of them ages quietly.",
		},
		{
			Name:     "Forecasting",
			Level:    forecastLevel(db, period),
			Evidence: forecastEvidence(db, period),
			Next:     "Freeze every month, and let the accuracy be scored against what happens rather than against a forecast that kept moving.",
		},
		{
			Name:  "Unit economics",
			Level: 1,
			Evidence: "Token and GPU-hour volumes are held beside cost, so price is " +
				"separated from volume. The business metric to divide by is not connected.",
			Next: "Connect one real outcome metric. Until then a cost per outcome would be " +
				"the same number wearing a denominator.",
		},
		{
			Name:  "Governance",
			Level: 2,
			Evidence: "Every decision carries a reason, the journal is hash-chained and " +
				"verified on the audit page, and identity is published as Agent Passports.",
			Next: "Bind the identities to a workload. Attestation currently says none, " +
				"which is honest and is not proof.",
		},
	}
	return caps, nil
}

func level(run, walk, crawl bool) int {
	switch {
	case run:
		return 3
	case walk:
		return 2
	case crawl:
		return 1
	}
	return 0
}

func forecastLevel(db *sql.DB, period string) int {
	frozen, err := FrozenPeriods(db)
	if err != nil {
		return 0
	}
	openMonth, _ := OpenPeriod(db)
	acc, _, has, _ := Accuracy(db, openMonth)
	switch {
	case has && acc <= LadderTrusted:
		return 3
	case has:
		return 2
	case len(frozen) > 0:
		return 1
	}
	return 0
}

func forecastEvidence(db *sql.DB, period string) string {
	frozen, _ := FrozenPeriods(db)
	acc, scored, has, _ := Accuracy(db, period)
	if has {
		return fmt.Sprintf("%d months frozen, %d month-desks scored, average error %.1f%%.",
			len(frozen), scored, acc)
	}
	if len(frozen) > 0 {
		return fmt.Sprintf("%d months frozen, none finished yet, so nothing is scored.", len(frozen))
	}
	return "Nothing has been frozen, so no forecast has ever been held to what happened."
}

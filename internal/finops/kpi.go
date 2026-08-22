package finops

import (
	"database/sql"
	"fmt"

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
	if err := db.QueryRow(`SELECT
		SUM(CASE WHEN state='open' THEN 1 ELSE 0 END),
		SUM(CASE WHEN state IN ('accepted','dismissed') THEN 1 ELSE 0 END),
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
	var tasks, posted, returned int
	var spent int64
	if err := db.QueryRow(`SELECT COUNT(*),
		SUM(CASE WHEN state='posted' THEN 1 ELSE 0 END),
		SUM(CASE WHEN state='returned' THEN 1 ELSE 0 END),
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
	add(KPI{
		ID: "crew-cost", Name: "What the crew cost", Group: "The crew",
		Value: money.Cents(spent).String(), Unit: "USD", Target: "against what it found",
		HasVal: true, Meets: true,
		Note: fmt.Sprintf("Across %d tasks.", tasks),
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
	add(KPI{
		ID: "cost-per-outcome", Name: "Cost per business outcome", Group: "Unit economics",
		Target: "trending down",
		Blocked: "the business metric this would divide by is not connected. A cost " +
			"per outcome derived from a cost is not a unit economic, it is the same " +
			"number wearing a denominator.",
	})
	add(KPI{
		ID: "carbon-per-workload", Name: "Carbon per workload", Group: "Sustainability",
		Target: "trending down",
		Blocked: "no carbon data source is connected, and the estimates providers " +
			"publish are not comparable between them.",
	})
	add(KPI{
		ID: "agent-attribution", Name: "AI spend attributed to an agent", Group: "Unit economics",
		Target: ">= 90%",
		Blocked: "model calls do not carry an agent header through a gateway, so AI " +
			"spend can be attributed to a team and no further. The anomaly pages say " +
			"which grain they are at rather than implying the finer one.",
	})

	return out, nil
}

func blockedIf(cond bool, why string) string {
	if cond {
		return why
	}
	return ""
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

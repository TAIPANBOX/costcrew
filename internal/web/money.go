package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

var (
	tplAllocation = page("allocation.html")
	tplChargeback = page("chargeback.html")
	tplResults    = page("results.html")
)

// period reads the month off the query, defaulting to the last CLOSED one.
//
// Not the newest. The newest is partial, and a partial month against a whole
// month's budget makes every team look thrifty, which is the single most
// common way a cost report misleads the person reading it.
func (s *Server) period(r *http.Request) (string, []string) {
	months, _ := finops.Months(s.db)
	if len(months) == 0 {
		return "", nil
	}
	def := months[0]
	if len(months) > 1 {
		def = months[1]
	}
	if p := r.URL.Query().Get("period"); p != "" {
		for _, m := range months {
			if m == p {
				return p, months
			}
		}
	}
	return def, months
}

// ------------------------------------------------------------- allocation

func (s *Server) allocation(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	p, months := s.period(r)
	a, err := finops.Allocate(s.db, p)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	rules, err := finops.Rules(s.db)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	sp := readSort(r, "loaded", true)
	applySort(a.Teams, sp, map[string]func(x, y finops.TeamCost) int{
		"desk":      func(x, y finops.TeamCost) int { return cmpString(x.Source, y.Source) },
		"team":      func(x, y finops.TeamCost) int { return cmpString(x.Team, y.Team) },
		"direct":    func(x, y finops.TeamCost) int { return cmpInt64(int64(x.Direct), int64(y.Direct)) },
		"allocated": func(x, y finops.TeamCost) int { return cmpInt64(int64(x.Allocated), int64(y.Allocated)) },
		"loaded":    func(x, y finops.TeamCost) int { return cmpInt64(int64(x.Loaded()), int64(y.Loaded())) },
	}, "loaded")
	s.render(w, tplAllocation, struct {
		shell
		A       finops.Allocation
		Rules   []finops.Rule
		Methods []finops.Method
		Months  []string
		Period  string
		CanAct  bool
		Sort    sortSpec
	}{s.shellFor(r, "Allocation", "allocation"), a, rules,
		finops.ValidMethods(), months, p, u.May("operator"), sp})
}

func (s *Server) setRule(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	back := "/allocation"
	if !s.checked(w, r, back, u) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		redirectMsg(w, r, back, "no such rule")
		return
	}
	m, err := finops.MethodFrom(r.PostFormValue("method"))
	if err != nil {
		redirectMsg(w, r, back, err.Error())
		return
	}
	s.done(w, r, back, finops.SetRule(s.db, id, m))
}

// ------------------------------------------------------------- chargeback

func (s *Server) chargeback(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	p, months := s.period(r)
	frozen, err := finops.FrozenPeriod(s.db, p)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	live, err := finops.Allocate(s.db, p)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	trueUp, _, _ := finops.TrueUpFor(s.db, p)
	// Customer units (costcrew#74): read off the allocation already in hand,
	// so this panel and the table below it cannot disagree.
	unitRules, err := finops.UnitRules(s.db)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	units := finops.UnitsOf(live, unitRules)

	// C2-SPEC.md section 2: "the chargeback page shows the last close pack's
	// figures beside the live ones." The last CLOSE overall, not necessarily
	// of the period being viewed -- viewing an open month is exactly when
	// there is nothing of its own to freeze yet, and the most recent close is
	// the only figure a reader can compare it against.
	var lastClose finops.Period
	var haveLastClose bool
	if closedPeriods, cerr := finops.ClosedPeriods(s.db); cerr == nil && len(closedPeriods) > 0 {
		if lc, lerr := finops.FrozenPeriod(s.db, closedPeriods[0]); lerr == nil {
			lastClose, haveLastClose = lc, true
		}
	}
	liveTotal := live.Direct + live.Shared

	sp := readSort(r, "loaded", true)
	applySort(live.Teams, sp, map[string]func(x, y finops.TeamCost) int{
		"desk":   func(x, y finops.TeamCost) int { return cmpString(x.Source, y.Source) },
		"team":   func(x, y finops.TeamCost) int { return cmpString(x.Team, y.Team) },
		"loaded": func(x, y finops.TeamCost) int { return cmpInt64(int64(x.Loaded()), int64(y.Loaded())) },
	}, "loaded")
	// The true-up table sorts on its own parameter. Its interesting column is
	// the DELTA and the interesting end of it is the largest move in either
	// direction, so it opens on the size of the change, not its sign.
	tp := readSortNamed(r, "tsort", "delta", true)
	applySort(trueUp, tp, map[string]func(x, y finops.TrueUp) int{
		"desk":   func(x, y finops.TrueUp) int { return cmpString(x.Source, y.Source) },
		"team":   func(x, y finops.TrueUp) int { return cmpString(x.Team, y.Team) },
		"frozen": func(x, y finops.TrueUp) int { return cmpInt64(int64(x.Frozen), int64(y.Frozen)) },
		"now":    func(x, y finops.TrueUp) int { return cmpInt64(int64(x.Now), int64(y.Now)) },
		"delta":  func(x, y finops.TrueUp) int { return cmpInt64(abs64(int64(x.Delta)), abs64(int64(y.Delta))) },
	}, "delta")

	s.render(w, tplChargeback, struct {
		shell
		P             finops.Period
		A             finops.Allocation
		TrueUp        []finops.TrueUp
		Months        []string
		Period        string
		CanAct        bool
		Sort          sortSpec
		SortTrueUp    sortSpec
		LastClose     finops.Period
		HaveLastClose bool
		LiveTotal     money.Cents
		Units         []finops.UnitLine
	}{s.shellFor(r, "Chargeback", "chargeback"), frozen, live, trueUp,
		months, p, u.May("operator"), sp, tp, lastClose, haveLastClose, liveTotal, units})
}

func (s *Server) closePeriod(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	if !s.checked(w, r, "/chargeback", u) {
		return
	}
	p := r.PostFormValue("period")
	back := "/chargeback?period=" + p
	s.done(w, r, back, finops.Close(s.db, p, u.Username))
}

func (s *Server) reopenPeriod(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	if !s.checked(w, r, "/chargeback", u) {
		return
	}
	p := r.PostFormValue("period")
	back := "/chargeback?period=" + p
	s.done(w, r, back, finops.Reopen(s.db, p, r.PostFormValue("reason")))
}

// ---------------------------------------------------------------- results

func (s *Server) results(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	p, _ := s.period(r)
	res, err := finops.Compute(s.db, p)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	ret, hasRet := res.Return()
	fp, hasFP := res.FirstPass()
	s.render(w, tplResults, struct {
		shell
		R            finops.Results
		Return       float64
		HasReturn    bool
		FirstPass    float64
		HasFirstPass bool
	}{s.shellFor(r, "Results", "results"), res, ret, hasRet, fp, hasFP})
}

// ---------------------------------------------------------------- exports

func (s *Server) exportAllocation(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	p, _ := s.period(r)
	a, err := finops.Allocate(s.db, p)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	rows := make([][]string, 0, len(a.Teams)+1)
	for _, t := range a.Teams {
		rows = append(rows, []string{p, t.Source, t.Team,
			t.Direct.String(), t.Allocated.String(), t.Loaded().String()})
	}
	// The unowned remainder is a ROW, not a footnote. A file whose lines do
	// not add up to the invoice is one somebody reconciles by hand.
	if a.Unallocated != 0 {
		rows = append(rows, []string{p, "*", "(unallocated)",
			"0.00", a.Unallocated.String(), a.Unallocated.String()})
	}
	writeCSV(w, "allocation-"+p+".csv", []csvCol{
		textCol("period"), textCol("source"), textCol("team"), numberCol("direct_usd"),
		numberCol("allocated_usd"), numberCol("fully_loaded_usd")}, rows)
}

func (s *Server) exportGL(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	p, _ := s.period(r)
	frozen, err := finops.FrozenPeriod(s.db, p)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	// Only a CLOSED period goes to the ledger. Exporting a live allocation as
	// a journal entry is how finance ends up with a number that changed after
	// they posted it.
	if !frozen.Closed {
		writeCSV(w, "gl-"+p+".csv",
			[]csvCol{textCol("period"), textCol("status")},
			[][]string{{p, "not closed: nothing to post to a ledger"}})
		return
	}
	rows := make([][]string, 0, len(frozen.Teams))
	for _, t := range frozen.Teams {
		rows = append(rows, []string{p, t.Source, t.Team, t.Loaded().String(),
			frozen.FrozenAt, frozen.ClosedBy})
	}
	writeCSV(w, "gl-"+p+".csv", []csvCol{
		textCol("period"), textCol("source"), textCol("cost_centre"), numberCol("amount_usd"),
		textCol("frozen_at"), textCol("closed_by")}, rows)
}

func (s *Server) exportShowback(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	p, _ := s.period(r)
	sb, err := finops.Showback(s.db, p)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	// The rows come from finops.Showback: the roster's teams as the file
	// always carried them, one row per customer unit a stamped rule covers,
	// and, if any unit has none, one "(unruled units)" row, so the file
	// accounts for the whole bill (costcrew#74).
	rows := make([][]string, 0, len(sb))
	for _, r := range sb {
		rows = append(rows, []string{p, r.Team, r.BusinessUnit,
			r.Direct.String(), r.Allocated.String(), r.Loaded().String()})
	}
	writeCSV(w, "showback-"+p+".csv", []csvCol{
		textCol("period"), textCol("team"), textCol("business_unit"), numberCol("direct_usd"),
		numberCol("allocated_usd"), numberCol("fully_loaded_usd")}, rows)
}

func (s *Server) exportResultsCSV(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	list, err := anomaly.List(s.db, anomaly.Filter{})
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	rows := make([][]string, 0, len(list))
	for _, a := range list {
		rows = append(rows, []string{
			a.ID, a.Source, a.Team, a.Service, a.Day, a.Direction,
			a.Excess.String(), a.CausedBy, a.CausedByKind, a.HandledBy,
			string(a.State), a.Reason,
		})
	}
	writeCSV(w, "findings.csv", []csvCol{
		textCol("id"), textCol("source"), textCol("team"), textCol("service"), textCol("day"),
		textCol("direction"), numberCol("excess_usd"), textCol("caused_by"),
		textCol("caused_by_grain"), textCol("handled_by"), textCol("state"), textCol("reason")}, rows)
}

func (s *Server) exportCrewCSV(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	scores, err := crew.Scoreboards(s.db)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	var rows [][]string
	for _, a := range world.Crew {
		sc := scores[a.Name]
		// Blank, not "100", where nothing has been reviewed. A rate over no
		// reviews is not a rate, and a spreadsheet will happily average it.
		rate := ""
		if sc.HasRate {
			rate = strconv.FormatFloat(sc.FirstPass, 'f', 0, 64)
		}
		rows = append(rows, []string{
			a.Name, a.Role, a.Desk, string(a.State), a.Reason,
			strconv.Itoa(sc.Tasks), strconv.Itoa(sc.Open), strconv.Itoa(sc.Posted),
			strconv.Itoa(sc.Returned), rate, sc.Spent.String(),
			strconv.Itoa(sc.Anomalies),
		})
	}
	writeCSV(w, "crew.csv", []csvCol{
		textCol("analyst"), textCol("role"), textCol("desk"), textCol("state"),
		textCol("state_reason"), numberCol("tasks"), numberCol("open"), numberCol("posted"),
		numberCol("returned"), numberCol("first_pass_pct"), numberCol("spent_usd"),
		numberCol("anomalies_handled")}, rows)
}

func (s *Server) exportResultsMD(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	p, _ := s.period(r)
	res, err := finops.Compute(s.db, p)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# CostCrew results, %s\n\n", mdText(p))
	fmt.Fprintf(&b, "Money is **found**, never saved. Nothing is saved until somebody acts.\n\n")
	fmt.Fprintf(&b, "- Found this period: **%s** a month, %s annualised\n", res.FoundMonthly, res.FoundAnnual)
	fmt.Fprintf(&b, "- The crew cost **%s** across %d tasks\n", res.CrewSpend, res.Tasks)
	if ret, ok := res.Return(); ok {
		fmt.Fprintf(&b, "- Return: **%.0fx** what it cost to run\n", ret)
	}
	fmt.Fprintf(&b, "- The estate: %s\n\n", res.Estate)
	b.WriteString("## Still open\n\n")
	fmt.Fprintf(&b, "%d anomalies worth %s have not been looked at", res.OpenAnomalies, res.OpenMoney)
	if res.OldestOpen != "" {
		fmt.Fprintf(&b, ", the oldest from %s", mdText(res.OldestOpen))
	}
	b.WriteString(".\n\nAn anomaly nobody has looked at says more about a practice than one that closed.\n\n")
	b.WriteString("## Decisions needed\n\n")
	fmt.Fprintf(&b, "- %d deliverables written and awaiting a stamp\n", res.AwaitingStamp)
	fmt.Fprintf(&b, "- %d anomalies have an answer and need accepting or rejecting\n", res.AwaitingDecision)

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	setDisposition(w, "attachment", "results-"+p+".md")
	fmt.Fprint(w, b.String())
}

func (s *Server) exportExecPacket(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	p, _ := s.period(r)
	res, _ := finops.Compute(s.db, p)
	a, _ := finops.Allocate(s.db, p)
	open, _ := anomaly.List(s.db, anomaly.Filter{State: anomaly.Open})

	var b strings.Builder
	fmt.Fprintf(&b, "# Executive packet, %s\n\n", mdText(p))
	fmt.Fprintf(&b, "The estate cost **%s** this period. %s of it arrived without a team; "+
		"%s of that has been given one by a rule, and **%s** still has no owner.\n\n",
		res.Estate, a.Shared, a.Placed, a.Unallocated)

	b.WriteString("## What the crew found\n\n")
	fmt.Fprintf(&b, "**%s a month**, found rather than saved: nothing moves until "+
		"somebody acts on it. Finding it cost %s.\n\n", res.FoundMonthly, res.CrewSpend)

	b.WriteString("## What is still open\n\n")
	if len(open) == 0 {
		b.WriteString("Nothing.\n\n")
	} else {
		b.WriteString("| Money | Where | Day | Whose spend |\n|---|---|---|---|\n")
		for i, x := range open {
			if i >= 8 {
				fmt.Fprintf(&b, "\nand %d more.\n", len(open)-8)
				break
			}
			// Every one of these but the amount can come from an imported
			// file, and each lands in a table cell (download.go, mdText).
			fmt.Fprintf(&b, "| %s | %s, %s | %s | %s (%s) |\n",
				x.Excess, mdText(x.Source), mdText(x.Service), mdText(x.Day),
				mdText(x.CausedBy), mdText(x.CausedByKind))
		}
		b.WriteString("\n")
	}

	b.WriteString("## Decisions needed\n\n")
	fmt.Fprintf(&b, "1. **%d anomalies** have an answer and need accepting or rejecting.\n", res.AwaitingDecision)
	fmt.Fprintf(&b, "2. **%d deliverables** are written and awaiting a stamp.\n", res.AwaitingStamp)
	if a.Unallocated != 0 {
		fmt.Fprintf(&b, "3. **%s** of shared cost has no allocation rule that will take it. "+
			"Either a rule changes or somebody accepts it stays central.\n", a.Unallocated)
	}
	b.WriteString("\nNo figure here is an estimate. Every one is a sum over rows this " +
		"console holds, and each is reproducible from the exports beside it.\n")

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	setDisposition(w, "attachment", "exec-packet-"+p+".md")
	fmt.Fprint(w, b.String())
}

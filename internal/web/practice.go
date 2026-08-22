package web

import (
	"fmt"
	"net/http"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

var (
	tplKPIs        = page("kpis.html")
	tplUtilisation = page("utilisation.html")
	tplSaaS        = page("saas.html")
	tplAI          = page("ai.html")
)

func (s *Server) kpis(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	p, _ := s.period(r)
	ks, err := finops.KPIs(s.db, p)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	caps, err := finops.Maturity(s.db, p)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	reporting, blocked, meeting := finops.KPICounts(ks)
	// Default: grouped, which is how the library is meant to be read. Sorting
	// by verdict is the other thing somebody wants, and it is one click.
	srt := readSort(r, "group", false)
	applySort(ks, srt, map[string]func(a, b finops.KPI) int{
		"group": func(a, b finops.KPI) int { return cmpString(a.Group, b.Group) },
		"kpi":   func(a, b finops.KPI) int { return cmpString(a.Name, b.Name) },
		"value": func(a, b finops.KPI) int { return cmpString(a.Value, b.Value) },
		"verdict": func(a, b finops.KPI) int {
			// Refusals first, then below target, then met: the page's own
			// argument is that the refusals are the part worth reading.
			rank := func(k finops.KPI) int {
				switch {
				case k.Blocked != "":
					return 0
				case !k.Meets:
					return 1
				}
				return 2
			}
			return cmpInt(rank(a), rank(b))
		},
	}, "group")
	s.render(w, tplKPIs, struct {
		shell
		KPIs                        []finops.KPI
		Caps                        []finops.Capability
		Levels                      []string
		Reporting, Blocked, Meeting int
		Sort                        sortSpec
	}{s.shellFor(r, "KPIs", "kpis"), ks, caps, finops.Levels(),
		reporting, blocked, meeting, srt})
}

func (s *Server) utilisation(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	var candidates, fine int
	var saving money.Cents
	for _, u := range world.UtilisationRows {
		if u.Advice != "" {
			candidates++
			saving += u.Saving
		} else {
			fine++
		}
	}
	rows := copyOf(world.UtilisationRows)
	sp := readSort(r, "saving", true)
	applySort(rows, sp, map[string]func(x, y world.Utilisation) int{
		"desk":     func(x, y world.Utilisation) int { return cmpString(x.Source, y.Source) },
		"service":  func(x, y world.Utilisation) int { return cmpString(x.Service, y.Service) },
		"team":     func(x, y world.Utilisation) int { return cmpString(x.Team, y.Team) },
		"resource": func(x, y world.Utilisation) int { return cmpString(x.Resource, y.Resource) },
		"kind":     func(x, y world.Utilisation) int { return cmpString(x.Kind, y.Kind) },
		"cpu":      func(x, y world.Utilisation) int { return cmpFloat(x.P95CPU, y.P95CPU) },
		"mem":      func(x, y world.Utilisation) int { return cmpFloat(x.P95Mem, y.P95Mem) },
		"monthly":  func(x, y world.Utilisation) int { return cmpInt64(int64(x.Monthly), int64(y.Monthly)) },
		"saving":   func(x, y world.Utilisation) int { return cmpInt64(int64(x.Saving), int64(y.Saving)) },
	}, "saving")
	s.render(w, tplUtilisation, struct {
		shell
		Rows             []world.Utilisation
		Candidates, Fine int
		Saving           money.Cents
		Sort             sortSpec
	}{s.shellFor(r, "Utilisation", "utilisation"), rows,
		candidates, fine, saving, sp})
}

func (s *Server) saas(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	var idle, issued int
	var waste money.Cents
	for _, l := range world.Licences {
		idle += l.Idle()
		issued += l.Issued
		waste += l.Waste()
	}
	// Ninety days from the estate's own last day, not from today: the fixture
	// is dated, and a renewal calendar measured against the wall clock would
	// quietly empty as time passed.
	soon := len(world.ExpiringWithin(90, world.LastDay))
	rows := copyOf(world.Licences)
	sp := readSort(r, "waste", true)
	applySort(rows, sp, map[string]func(x, y world.Licence) int{
		"vendor":  func(x, y world.Licence) int { return cmpString(x.Vendor, y.Vendor) },
		"product": func(x, y world.Licence) int { return cmpString(x.Product, y.Product) },
		"team":    func(x, y world.Licence) int { return cmpString(x.Team, y.Team) },
		"issued":  func(x, y world.Licence) int { return cmpInt(x.Issued, y.Issued) },
		"active":  func(x, y world.Licence) int { return cmpInt(x.Active30, y.Active30) },
		"idle":    func(x, y world.Licence) int { return cmpInt(x.Idle(), y.Idle()) },
		"seat":    func(x, y world.Licence) int { return cmpInt64(int64(x.PerSeat), int64(y.PerSeat)) },
		"waste":   func(x, y world.Licence) int { return cmpInt64(int64(x.Waste()), int64(y.Waste())) },
		"renews":  func(x, y world.Licence) int { return cmpString(x.Renews, y.Renews) },
	}, "waste")
	comms := copyOf(world.Commitments)
	cp := readSortNamed(r, "csort", "used", false)
	applySort(comms, cp, map[string]func(x, y world.Commitment) int{
		"desk":    func(x, y world.Commitment) int { return cmpString(x.Source, y.Source) },
		"name":    func(x, y world.Commitment) int { return cmpString(x.Name, y.Name) },
		"kind":    func(x, y world.Commitment) int { return cmpString(x.Kind, y.Kind) },
		"hourly":  func(x, y world.Commitment) int { return cmpInt64(int64(x.Hourly), int64(y.Hourly)) },
		"used":    func(x, y world.Commitment) int { return cmpFloat(x.Used, y.Used) },
		"expires": func(x, y world.Commitment) int { return cmpString(x.Expires, y.Expires) },
	}, "used")
	s.render(w, tplSaaS, struct {
		shell
		Rows            []world.Licence
		Commitments     []world.Commitment
		Waterline       float64
		Waste           money.Cents
		Idle            int
		Issued          int
		Soon            int
		Sort            sortSpec
		SortCommitments sortSpec
	}{s.shellFor(r, "SaaS", "saas"), rows, comms,
		world.Waterline, waste, idle, issued, soon, sp, cp})
}

func (s *Server) ai(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	month := world.LastDay[:7]
	units := world.AIUnits()
	var rows []world.AIUnit
	var total money.Cents
	var tokens int64
	for _, u := range units {
		if u.Month != month {
			continue
		}
		rows = append(rows, u)
		total += u.Cost
		tokens += u.Tokens
	}
	list, _ := anomaly.List(s.db, anomaly.Filter{Source: "ai"})
	// What this console's OWN agents cost, from the board rather than from the
	// invoices. It is a separate number and it is said to be a separate one:
	// the crew runs on models the estate does not bill for through these
	// meters, so folding it into the desk total would double nothing and
	// explain less. The crew page reports the same figure.
	var crewCost money.Cents
	var crewTasks int
	if scores, err := crew.Scoreboards(s.db); err == nil {
		for _, sc := range scores {
			crewCost += sc.Spent
			crewTasks += sc.Tasks
		}
	}
	sp := readSort(r, "cost", true)
	applySort(rows, sp, map[string]func(x, y world.AIUnit) int{
		"team":      func(x, y world.AIUnit) int { return cmpString(x.Team, y.Team) },
		"model":     func(x, y world.AIUnit) int { return cmpString(x.Model, y.Model) },
		"tokens":    func(x, y world.AIUnit) int { return cmpInt64(x.Tokens, y.Tokens) },
		"cost":      func(x, y world.AIUnit) int { return cmpInt64(int64(x.Cost), int64(y.Cost)) },
		"permil":    func(x, y world.AIUnit) int { return cmpInt64(int64(x.PerMillion()), int64(y.PerMillion())) },
		"actions":   func(x, y world.AIUnit) int { return cmpInt(x.Actions, y.Actions) },
		"deflected": func(x, y world.AIUnit) int { return cmpInt(x.Deflected, y.Deflected) },
	}, "cost")
	s.render(w, tplAI, struct {
		shell
		Rows      []world.AIUnit
		Total     money.Cents
		Tokens    string
		Anomalies int
		Sort      sortSpec
		CrewCost  money.Cents
		CrewTasks int
		Month     string
	}{s.shellFor(r, "AI spend", "ai"), rows, total, thousands(tokens), len(list), sp,
		crewCost, crewTasks, month})
}

// thousands groups a large count so a reader can tell a million from ten.
func thousands(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ' ')
		}
		out = append(out, c)
	}
	return string(out)
}

// resultsHTML is the whole answer as one self-contained file.
//
// Self-contained matters: a report that needs a running server to be read is
// one that cannot be attached to an email, and the person who most needs it is
// the one least likely to have the console open.
func (s *Server) exportResultsHTML(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	p, _ := s.period(r)
	res, err := finops.Compute(s.db, p)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	a, _ := finops.Allocate(s.db, p)
	open, _ := anomaly.List(s.db, anomaly.Filter{State: anomaly.Open})
	totals, _ := estate.Totals(s.db, p)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=costcrew-results-"+p+".html")

	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>CostCrew results, %s</title><style>
:root{--ink:#1a2029;--ink2:#4d5762;--ink3:#78828d;--line:#d8dde2;--bg:#fff;--up:#a4472c}
@media(prefers-color-scheme:dark){:root{--ink:#e1e7ed;--ink2:#a6b2be;--ink3:#7b8794;--line:#2c3742;--bg:#11161c;--up:#e08a6b}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);
font:16px/1.6 system-ui,-apple-system,"Segoe UI",sans-serif;padding:0 20px 60px}
main{max-width:70ch;margin:0 auto}h1{font-size:30px;letter-spacing:-.02em;margin:44px 0 6px}
h2{font-size:19px;margin:34px 0 10px}p{margin:0 0 14px;color:var(--ink2)}
.lede{font-size:18px}table{border-collapse:collapse;width:100%%;font-size:14px;margin:0 0 18px}
th{text-align:left;font-size:11px;letter-spacing:.08em;text-transform:uppercase;
color:var(--ink3);padding:8px 10px;border-bottom:1px solid var(--line)}
td{padding:8px 10px;border-bottom:1px solid var(--line);color:var(--ink2)}
td.n{text-align:right;font-variant-numeric:tabular-nums;font-family:ui-monospace,monospace}
strong{color:var(--ink)}footer{margin-top:40px;padding-top:16px;border-top:1px solid var(--line);
color:var(--ink3);font-size:13px}
</style></head><body><main>
<h1>CostCrew results</h1>
<p class="lede">%s. Money below is <strong>found</strong>, never saved: nothing is saved until somebody acts on it.</p>
`, p, p)

	fmt.Fprintf(w, `<h2>The headline</h2>
<table><tbody>
<tr><td>Found this period</td><td class="n"><strong>%s</strong></td></tr>
<tr><td>What the crew cost to run</td><td class="n">%s</td></tr>
<tr><td>The estate</td><td class="n">%s</td></tr>
<tr><td>Cost with no owner</td><td class="n">%s</td></tr>
</tbody></table>`, res.FoundMonthly, res.CrewSpend, res.Estate, a.Unallocated)

	fmt.Fprintf(w, `<h2>Still unexplained</h2>
<p>%d anomalies worth %s have not been looked at`, res.OpenAnomalies, res.OpenMoney)
	if res.OldestOpen != "" {
		fmt.Fprintf(w, `, the oldest from %s`, res.OldestOpen)
	}
	fmt.Fprint(w, `. An anomaly nobody has looked at says more about a practice than one that closed.</p>`)

	if len(open) > 0 {
		fmt.Fprint(w, `<table><thead><tr><th>Money</th><th>Where</th><th>Day</th><th>Whose spend</th></tr></thead><tbody>`)
		for i, x := range open {
			if i >= 10 {
				break
			}
			fmt.Fprintf(w, `<tr><td class="n">%s</td><td>%s, %s</td><td>%s</td><td>%s (%s)</td></tr>`,
				x.Excess, x.Source, x.Service, x.Day, x.CausedBy, x.CausedByKind)
		}
		fmt.Fprint(w, `</tbody></table>`)
	}

	fmt.Fprint(w, `<h2>By desk</h2><table><thead><tr><th>Desk</th><th>Cost</th></tr></thead><tbody>`)
	for _, d := range world.Desks {
		fmt.Fprintf(w, `<tr><td>%s</td><td class="n">%s</td></tr>`, d.Name, totals[d.Name])
	}
	fmt.Fprint(w, `</tbody></table>`)

	fmt.Fprintf(w, `<h2>Decisions needed</h2>
<p><strong>%d</strong> anomalies have an answer and need accepting or rejecting.
<strong>%d</strong> deliverables are written and awaiting a stamp.</p>
<footer>Generated by CostCrew on %s. No figure here is an estimate: each is a sum
over rows the console holds, and each is reproducible from the exports beside it.</footer>
</main></body></html>`, res.AwaitingDecision, res.AwaitingStamp,
		time.Now().UTC().Format("2006-01-02"))
}

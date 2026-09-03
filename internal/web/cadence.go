package web

// /cadence: B5-SPEC.md section 4. The console page that says what is due,
// what it would cost at worst, and the switch a person flips. No button on
// this page runs anything; the routine runs on the platform's clock
// (stack-k8s's CronJob, stack-single's routine), still suspended -- flipping
// that is a platform act and a separate decision (section 5). This page's
// own switch is the inner one `tools/run -due` reads before it will spend a
// cent.

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

var tplCadence = page("cadence.html")

// dueEstimateMaxTokens mirrors tools/run's own -max-tokens default (main.go:
// `flag.Int("max-tokens", 2000, ...)`). This page previews what a `-due` run
// would cost if invoked with that default; an operator who runs the CronJob
// with a different -max-tokens sees a different actual worst case, which is
// no different from any other estimate in this console being a bound rather
// than a promise.
const dueEstimateMaxTokens = 2000

// cadenceDueRow is one due item, priced for display. Worst is pre-formatted
// (usdMicros) rather than left to the template: a quarter of a cent renders
// as 0.00 at two decimals, the same trap tools/run's own usd() exists to
// avoid, so this reuses four decimal places for the same reason.
type cadenceDueRow struct {
	Analyst    string
	Title      string
	Cadence    string // parsed from the item's own Why; see parseCadenceWhy
	LastPosted string
	Why        string
	Worst      string
	Priced     bool
}

// cadenceRanRow is one crew_ran entry from the journal, for the "last three
// runs" panel.
type cadenceRanRow struct {
	When         string
	Sprint       string
	TasksRun     int
	TasksRefused int
	Cost         string
	SwitchedOnBy string
}

// usdMicros renders micro-dollars at four decimal places, the same
// convention tools/run's own usd() uses and for the same reason: a call on
// the cheap route costs a fraction of a cent, and two places would print
// every one of them as 0.00.
func usdMicros(micros int64) string {
	return fmt.Sprintf("%.4f", float64(micros)/1e6)
}

func (s *Server) cadencePage(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}

	enabled, ceilingCents, changedBy, changedAt, err := crew.CadenceSettings(s.db)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}

	today := time.Now().Format("2006-01-02")
	roster, err := crew.Roster(s.db)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	by := map[string]crew.Analyst{}
	for _, a := range roster {
		by[a.Name] = a
	}
	spent, err := crew.SpendInMonth(s.db, today[:7])
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	items, err := crew.CadenceDue(s.db, roster, today, spent)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}

	rows := make([]cadenceDueRow, 0, len(items))
	// Summed in micros, once, before it is ever formatted: rounding each
	// row first and adding the rounded strings is exactly the fault
	// [[finest-unit-per-row-round-once-at-the-aggregate]] names.
	var totalWorstMicros int64
	for _, it := range items {
		word, lastPosted := parseCadenceWhy(it.Why)
		task := crewTaskForPreview(it)
		worst, _, priced := deliver.EstimateWorstCase(s.db, task, by[it.Assignee], dueEstimateMaxTokens)
		if priced {
			totalWorstMicros += worst
		}
		rows = append(rows, cadenceDueRow{
			Analyst: it.Assignee, Title: it.Title, Cadence: word, LastPosted: lastPosted,
			Why: it.Why, Worst: usdMicros(worst), Priced: priced,
		})
	}

	// The last three crew_ran events, newest first. A generous tail is read
	// and filtered rather than reading everything: JournalTail already reads
	// newest-first, and 500 is far more than a day's worth of entries this
	// console otherwise writes, on the seeded estate and in production alike.
	tail, err := s.st.JournalTail(500)
	if err != nil {
		http.Error(w, "the journal could not be read", http.StatusInternalServerError)
		return
	}
	var ran []cadenceRanRow
	for _, rec := range tail {
		if rec.Event != "crew_ran" || len(ran) >= 3 {
			continue
		}
		ran = append(ran, cadenceRanRow{
			When:         rec.When(),
			Sprint:       stringField(rec.Data, "sprint"),
			TasksRun:     intField(rec.Data, "tasks_run"),
			TasksRefused: intField(rec.Data, "tasks_refused"),
			Cost:         usdMicros(int64Field(rec.Data, "cost_micros")),
			SwitchedOnBy: stringField(rec.Data, "switched_on_by"),
		})
	}

	s.render(w, tplCadence, struct {
		shell
		Enabled     bool
		Ceiling     money.Cents
		ChangedBy   string
		ChangedAt   string
		Rows        []cadenceDueRow
		TotalWorst  string
		OverCeiling bool
		Ran         []cadenceRanRow
		CanAct      bool
	}{s.shellFor(r, "Cadence", "cadence"), enabled, ceilingCents, changedBy, changedAt,
		rows, usdMicros(totalWorstMicros), totalWorstMicros > int64(ceilingCents)*10_000,
		ran, u.May("operator")})
}

func (s *Server) setCadence(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	if !s.checked(w, r, "/cadence", u) {
		return
	}
	enabled := r.PostFormValue("enabled") == "on"
	ceiling, err := money.Parse(r.PostFormValue("ceiling"))
	if err != nil {
		redirectMsg(w, r, "/cadence", "the ceiling must look like 25.00")
		return
	}
	if err := crew.SetCadence(s.db, enabled, ceiling, u.Username); err != nil {
		redirectMsg(w, r, "/cadence", err.Error())
		return
	}
	if s.rec != nil {
		_ = s.rec.Emit("cadence_set", "supervisor", "info", map[string]any{
			"enabled": enabled, "ceiling_cents": int64(ceiling), "changed_by": u.Username,
		}, s.delegation(u.Username, "supervisor"))
	}
	redirectMsg(w, r, "/cadence", "")
}

// crewTaskForPreview is a synthetic, not-yet-created task priced the same
// way tools/run's own -due prices one: title, goal, assignee, desk and the
// item's own budget (the analyst's PerTask), no id, no anomaly.
func crewTaskForPreview(it crew.PlanItem) crew.Task {
	return crew.Task{Title: it.Title, Goal: it.Goal, Assignee: it.Assignee,
		Desk: it.Desk, Budget: it.Budget}
}

// parseCadenceWhy reads the cadence word and the last-posted date back out
// of a cadence-due item's own Why, which internal/crew/plan.go's CadenceDue
// always writes as "<word> cadence, last posted <date>", optionally
// followed by "; <name> skipped: no headroom this month" for the
// no-headroom fallback. Not a second source of truth: CadenceDue is the one
// place this sentence is built, and this only reads it back apart, the same
// way the /audit page's summarise() reads a journal payload back into a
// line rather than a second copy of what wrote it.
func parseCadenceWhy(why string) (word, lastPosted string) {
	before, after, ok := strings.Cut(why, " cadence, last posted ")
	if !ok {
		return "", ""
	}
	lastPosted, _, _ = strings.Cut(after, ";")
	return before, strings.TrimSpace(lastPosted)
}

func stringField(data map[string]any, key string) string {
	if s, ok := data[key].(string); ok {
		return s
	}
	return ""
}

func intField(data map[string]any, key string) int {
	return int(int64Field(data, key))
}

// int64Field is intField's own basis, kept as int64 for cost_micros: a run
// costing several dollars is already past the safe range a plain int keeps
// on a 32-bit build, and micro-dollars only grow from there.
func int64Field(data map[string]any, key string) int64 {
	switch v := data[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	}
	return 0
}

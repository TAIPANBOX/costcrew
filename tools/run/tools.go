package main

// The tool catalogue: one table, two renderings.
//
// B2-SPEC.md section 3.1. Before this an analyst's "skills" were a tag on
// its card and nothing it could actually exercise; every skill that needs
// figures-read, sql-readonly, budgets-read, kpi-registry or export-data now
// maps onto a tool the model can call, and the dispatcher (dispatch.go)
// refuses one it holds no right for. A tool exists ONCE, in the table
// below: anthropicTools renders it for the Messages API, openAITools for
// OpenRouter's chat-completions route, so the two providers are never able
// to see a different catalogue by construction (TestBothProvidersSeeTheSameCatalogue).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// toolFunc is what a catalogue entry actually runs, once the dispatcher has
// checked the right and validated the arguments. roDB is charges_query's
// read-only pool; every other tool ignores it.
type toolFunc func(ctx context.Context, db, roDB *sql.DB, args json.RawMessage) (string, error)

// toolDef is one row of the one table this catalogue is.
type toolDef struct {
	Name        string
	Right       string
	Description string
	// Schema is a JSON Schema object for the tool's arguments, in the shape
	// both providers read it in: {"type":"object","properties":{...},"required":[...]}.
	Schema map[string]any
	Run    toolFunc
}

// catalogue is the one table. Adding a tool means one entry here, never a
// second copy in a provider-specific list.
var catalogue = []toolDef{
	{
		Name: "anomaly", Right: "figures-read",
		Description: "Read one anomaly's figures: source, team, service, day, " +
			"direction, amount, baseline, excess, z, the rule, the driver and " +
			"who or what caused it, the same fields the task packet carries.",
		Schema: objSchema([]string{"id"}, map[string]any{
			"id": strProp("the anomaly id, e.g. A-1a2b3c4d5e6f"),
		}),
		Run: runAnomalyTool,
	},
	{
		Name: "series", Right: "figures-read",
		Description: "Read a dense daily series of cost, zero-filled for a day " +
			"with no charge, for one source/team/service.",
		Schema: objSchema([]string{"source", "team", "service"}, map[string]any{
			"source":  strProp("the desk, e.g. aws"),
			"team":    strProp("the team, may be empty for the desk's shared spend"),
			"service": strProp("the service name, e.g. Amazon EC2"),
			"days":    intProp("how many of the most recent days to return, at most 120", 120),
		}),
		Run: runSeriesTool,
	},
	{
		Name: "drivers", Right: "figures-read",
		Description: "Read the drivers registry: known one-time or recurring " +
			"events that explain a move in spend, for one service (or * for every " +
			"service) since a given day.",
		Schema: objSchema([]string{"service", "since"}, map[string]any{
			"service": strProp("a service name, or * for every service"),
			"since":   strProp("a day, YYYY-MM-DD; only drivers reaching this day or later"),
		}),
		Run: runDriversTool,
	},
	{
		Name: "team_month", Right: "figures-read",
		Description: "Read one team's month: budget, spend to date, variance, " +
			"and its top services by spend, for one period.",
		Schema: objSchema([]string{"team", "period"}, map[string]any{
			"team":   strProp("the team name"),
			"period": strProp("the month, YYYY-MM"),
		}),
		Run: runTeamMonthTool,
	},
	{
		Name: "charges_query", Right: "sql-readonly",
		Description: "Run a read-only SELECT over the charges, drivers and " +
			"attribution tables. One statement, at most 200 rows, no writes, no " +
			"comments, no other table.",
		Schema: objSchema([]string{"sql"}, map[string]any{
			"sql": strProp("a single SELECT statement naming only charges, drivers or attribution"),
		}),
		Run: runChargesQueryTool,
	},
	{
		// C7-SPEC.md section 2: the charges_query shape (ai_calls_query.go),
		// gated by figures-read -- ai-spend and unit-econ-ai already hold it
		// to read ai_calls at all -- rather than sql-readonly, which is
		// charges_query's own broader right and stays exactly what it was.
		Name: "ai_calls_query", Right: "figures-read",
		Description: "Run a read-only SELECT over the ai_calls table only. One " +
			"statement, at most 200 rows, no writes, no comments, no other table.",
		Schema: objSchema([]string{"sql"}, map[string]any{
			"sql": strProp("a single SELECT statement naming only ai_calls"),
		}),
		Run: runAICallsQueryTool,
	},
	{
		Name: "budgets", Right: "budgets-read",
		Description: "Read every team's budget, spend and variance on one desk, " +
			"for one period.",
		Schema: objSchema([]string{"source", "period"}, map[string]any{
			"source": strProp("the desk"),
			"period": strProp("the month, YYYY-MM"),
		}),
		Run: runBudgetsTool,
	},
	{
		Name: "variance", Right: "budgets-read",
		Description: "Read one team's month against its budget, summed across " +
			"every desk it has one on.",
		Schema: objSchema([]string{"team", "period"}, map[string]any{
			"team":   strProp("the team name"),
			"period": strProp("the month, YYYY-MM"),
		}),
		Run: runVarianceTool,
	},
	{
		Name: "kpis", Right: "kpi-registry",
		Description: "Read the KPI library for one period: every measure this " +
			"practice tracks, with its value or the reason it refuses to report one.",
		Schema: objSchema([]string{"period"}, map[string]any{
			"period": strProp("the month, YYYY-MM"),
		}),
		Run: runKPIsTool,
	},
	{
		Name: "maturity", Right: "kpi-registry",
		Description: "Read the six practice-maturity capabilities, each with its " +
			"level and the evidence behind it.",
		Schema: objSchema([]string{"period"}, map[string]any{
			"period": strProp("the month, YYYY-MM"),
		}),
		Run: runMaturityTool,
	},
	{
		Name: "allocation", Right: "export-data",
		Description: "Read the month's allocation: every team's direct and " +
			"allocated cost, and what could not be placed.",
		Schema: objSchema([]string{"period"}, map[string]any{
			"period": strProp("the month, YYYY-MM"),
		}),
		Run: runAllocationTool,
	},
	{
		Name: "showback", Right: "export-data",
		Description: "Read one team's showback statement for a period: the " +
			"frozen figures if the period is closed, the live allocation otherwise.",
		Schema: objSchema([]string{"team", "period"}, map[string]any{
			"team":   strProp("the team name"),
			"period": strProp("the month, YYYY-MM"),
		}),
		Run: runShowbackTool,
	},
}

func toolByName(name string) (toolDef, bool) {
	for _, t := range catalogue {
		if t.Name == name {
			return t, true
		}
	}
	return toolDef{}, false
}

func objSchema(required []string, props map[string]any) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string, max int) map[string]any {
	return map[string]any{"type": "integer", "description": desc, "maximum": max}
}

// anthropicTools renders the catalogue as Anthropic's `tools` array.
func anthropicTools() []map[string]any {
	out := make([]map[string]any, 0, len(catalogue))
	for _, t := range catalogue {
		out = append(out, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": t.Schema,
		})
	}
	return out
}

// openAITools renders the catalogue as the OpenAI-style `tools` array
// OpenRouter's chat-completions route reads.
func openAITools() []map[string]any {
	out := make([]map[string]any, 0, len(catalogue))
	for _, t := range catalogue {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Schema,
			},
		})
	}
	return out
}

// ---------------------------------------------------------------- the tools

func runAnomalyTool(_ context.Context, db, _ *sql.DB, args json.RawMessage) (string, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	an, err := anomaly.Get(db, in.ID)
	if err != nil {
		return "", fmt.Errorf("no such anomaly: %s", in.ID)
	}
	return anomalySection(an), nil
}

func runSeriesTool(_ context.Context, db, _ *sql.DB, args json.RawMessage) (string, error) {
	var in struct {
		Source, Team, Service string
		Days                  int
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	days, vals, err := estate.SeriesDays(db, estate.SeriesKey{
		Source: in.Source, Team: in.Team, Service: in.Service})
	if err != nil {
		return "", err
	}
	if len(days) == 0 {
		return "no series found for that source, team and service", nil
	}
	n := in.Days
	if n <= 0 || n > 120 {
		n = 120
	}
	if n > len(days) {
		n = len(days)
	}
	days, vals = days[len(days)-n:], vals[len(vals)-n:]

	var b strings.Builder
	for i := range days {
		fmt.Fprintf(&b, "%s  %s\n", days[i], vals[i])
	}
	return b.String(), nil
}

func runDriversTool(_ context.Context, db, _ *sql.DB, args json.RawMessage) (string, error) {
	var in struct{ Service, Since string }
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	all, err := estate.Drivers(db)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	n := 0
	for _, d := range all {
		if in.Service != "" && in.Service != "*" && d.Scope != "*" && d.Scope != in.Service {
			continue
		}
		if in.Since != "" && d.End < in.Since {
			continue
		}
		fmt.Fprintf(&b, "%s to %s  %s  %s (%s) source=%s\n",
			d.Start, d.End, d.Scope, d.Label, d.Kind, d.Source)
		n++
	}
	if n == 0 {
		return "no drivers match", nil
	}
	return b.String(), nil
}

func runTeamMonthTool(_ context.Context, db, _ *sql.DB, args json.RawMessage) (string, error) {
	var in struct{ Team, Period string }
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	var budget, spent int64
	found := false
	rows, err := db.Query(`SELECT source, budget_cents FROM budgets WHERE team=? AND month=?`, in.Team, in.Period)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var src string
		var b int64
		if err := rows.Scan(&src, &b); err != nil {
			rows.Close()
			return "", err
		}
		budget += b
		found = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	if !found {
		return fmt.Sprintf("no budget found for %s in %s", in.Team, in.Period), nil
	}
	if err := db.QueryRow(`SELECT COALESCE(SUM(billed_cents),0) FROM charges
		WHERE team=? AND category='Usage' AND substr(day,1,7)=?`, in.Team, in.Period).Scan(&spent); err != nil {
		return "", err
	}
	variance := money.Cents(spent) - money.Cents(budget)

	top, err := db.Query(`SELECT service, SUM(billed_cents) v FROM charges
		WHERE team=? AND substr(day,1,7)=? GROUP BY service ORDER BY v DESC LIMIT 5`, in.Team, in.Period)
	if err != nil {
		return "", err
	}
	defer top.Close()

	var b strings.Builder
	fmt.Fprintf(&b, "team %s, %s\n", in.Team, in.Period)
	fmt.Fprintf(&b, "budget:   %s\n", money.Cents(budget))
	fmt.Fprintf(&b, "spend:    %s\n", money.Cents(spent))
	fmt.Fprintf(&b, "variance: %s\n", variance)
	b.WriteString("top services:\n")
	for top.Next() {
		var svc string
		var v int64
		if err := top.Scan(&svc, &v); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "  %-24s %s\n", svc, money.Cents(v))
	}
	return b.String(), top.Err()
}

func runBudgetsTool(_ context.Context, db, _ *sql.DB, args json.RawMessage) (string, error) {
	var in struct{ Source, Period string }
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	rows, err := finops.BudgetsFor(db, in.Source, in.Period)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return fmt.Sprintf("no budgets found for %s in %s", in.Source, in.Period), nil
	}
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%-20s budget %s  actual %s  variance %s\n",
			r.Team, r.Budget, r.Actual, r.Variance)
	}
	return b.String(), nil
}

func runVarianceTool(_ context.Context, db, _ *sql.DB, args json.RawMessage) (string, error) {
	var in struct{ Team, Period string }
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	var budget int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(budget_cents),0) FROM budgets
		WHERE team=? AND month=?`, in.Team, in.Period).Scan(&budget); err != nil {
		return "", err
	}
	var spent int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(billed_cents),0) FROM charges
		WHERE team=? AND category='Usage' AND substr(day,1,7)=?`, in.Team, in.Period).Scan(&spent); err != nil {
		return "", err
	}
	variance := money.Cents(spent) - money.Cents(budget)
	return fmt.Sprintf("team %s, %s\nbudget:   %s\nspend:    %s\nvariance: %s\n",
		in.Team, in.Period, money.Cents(budget), money.Cents(spent), variance), nil
}

func runKPIsTool(_ context.Context, db, _ *sql.DB, args json.RawMessage) (string, error) {
	var in struct{ Period string }
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	ks, err := finops.KPIs(db, in.Period)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, k := range ks {
		if k.Blocked != "" {
			fmt.Fprintf(&b, "%-28s refuses: %s\n", k.Name, k.Blocked)
			continue
		}
		meets := "meets"
		if !k.Meets {
			meets = "misses"
		}
		fmt.Fprintf(&b, "%-28s %s%s (target %s, %s)\n", k.Name, k.Value, k.Unit, k.Target, meets)
	}
	return b.String(), nil
}

func runMaturityTool(_ context.Context, db, _ *sql.DB, args json.RawMessage) (string, error) {
	var in struct{ Period string }
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	caps, err := finops.Maturity(db, in.Period)
	if err != nil {
		return "", err
	}
	levels := finops.Levels()
	var b strings.Builder
	for _, c := range caps {
		level := "?"
		if c.Level >= 0 && c.Level < len(levels) {
			level = levels[c.Level]
		}
		fmt.Fprintf(&b, "%-20s %s -- %s\n", c.Name, level, c.Evidence)
	}
	return b.String(), nil
}

func runAllocationTool(_ context.Context, db, _ *sql.DB, args json.RawMessage) (string, error) {
	var in struct{ Period string }
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	a, err := finops.Allocate(db, in.Period)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: direct %s, shared %s, placed %s, unallocated %s, coverage %.1f%%\n",
		a.Period, a.Direct, a.Shared, a.Placed, a.Unallocated, a.Coverage)
	for _, tc := range a.Teams {
		fmt.Fprintf(&b, "  %-10s %-20s direct %s  allocated %s\n", tc.Source, tc.Team, tc.Direct, tc.Allocated)
	}
	return b.String(), nil
}

func runShowbackTool(_ context.Context, db, _ *sql.DB, args json.RawMessage) (string, error) {
	var in struct{ Team, Period string }
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	closed, err := finops.IsClosed(db, in.Period)
	if err != nil {
		return "", err
	}
	if closed {
		fp, err := finops.FrozenPeriod(db, in.Period)
		if err != nil {
			return "", err
		}
		for _, f := range fp.Teams {
			if f.Team != in.Team {
				continue
			}
			return fmt.Sprintf("FROZEN %s, %s: direct %s, allocated %s, loaded %s (closed %s by %s)\n",
				in.Team, in.Period, f.Direct, f.Allocated, f.Loaded(), fp.FrozenAt, fp.ClosedBy), nil
		}
		return fmt.Sprintf("%s is closed and %s has no frozen row in it", in.Period, in.Team), nil
	}
	a, err := finops.Allocate(db, in.Period)
	if err != nil {
		return "", err
	}
	for _, tc := range a.Teams {
		if tc.Team != in.Team {
			continue
		}
		return fmt.Sprintf("LIVE (not yet closed) %s, %s: direct %s, allocated %s, loaded %s\n",
			in.Team, in.Period, tc.Direct, tc.Allocated, tc.Loaded()), nil
	}
	return fmt.Sprintf("%s has no spend against %s in %s", in.Team, in.Period, in.Period), nil
}

// sortedToolNames is used by TestBothProvidersSeeTheSameCatalogue so the
// comparison does not depend on catalogue's own declaration order.
func sortedToolNames() []string {
	out := make([]string, 0, len(catalogue))
	for _, t := range catalogue {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

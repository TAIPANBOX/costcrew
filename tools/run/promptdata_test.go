package main

// The prompt-data policy (CLAUDE.md invariant 70) at the runner: what the tool
// catalogue offers, what a tool hands back, what goes over the wire, what is
// done with the answer, and what the bus is told.
//
// THE GATE for the tools is TestNoRealIdentifierLeavesInAnyToolResult: every
// tool, in every mode, against a fully populated installation, and no
// identifier the schema walk found may be in a result. The enumeration comes
// from internal/promptfixture, not from the masker.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/promptfixture"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// ------------------------------------------------------ one shared installation

var sharedInst struct {
	once  sync.Once
	st    *store.Store
	ro    *sql.DB
	dir   string
	check *promptfixture.Checker
	err   error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedInst.ro != nil {
		sharedInst.ro.Close()
	}
	if sharedInst.st != nil {
		sharedInst.st.Close()
	}
	if sharedInst.dir != "" {
		os.RemoveAll(sharedInst.dir)
	}
	os.Exit(code)
}

func installation(t *testing.T) (db, ro *sql.DB, check *promptfixture.Checker) {
	t.Helper()
	sharedInst.once.Do(func() {
		dir, err := os.MkdirTemp("", "costcrew-run-gate-")
		if err != nil {
			sharedInst.err = err
			return
		}
		sharedInst.dir = dir
		st, err := promptfixture.Build(dir)
		if err != nil {
			sharedInst.err = err
			return
		}
		sharedInst.st = st
		if sharedInst.ro, sharedInst.err = store.OpenReadOnly(dir); sharedInst.err != nil {
			return
		}
		sharedInst.check, sharedInst.err = promptfixture.NewChecker(st.DB())
	})
	if sharedInst.err != nil {
		t.Fatalf("building the installation: %v", sharedInst.err)
	}
	return sharedInst.st.DB(), sharedInst.ro, sharedInst.check
}

// withPolicy installs a policy of mode over db for the life of the test.
func withPolicy(t *testing.T, db *sql.DB, mode deliver.PromptData) *deliver.Policy {
	t.Helper()
	p, err := deliver.NewPolicy(mode, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p.Bind(db)
	t.Cleanup(deliver.SetActivePolicy(p))
	return p
}

// everyRight is an analyst holding every right the catalogue gates on: the
// union of the skills on the roster.
func everyRight(t *testing.T, db *sql.DB) crew.Analyst {
	t.Helper()
	roster, err := crew.Roster(db)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var skills []string
	for _, a := range roster {
		for _, s := range a.Skills {
			if !seen[s] {
				seen[s] = true
				skills = append(skills, s)
			}
		}
	}
	sort.Strings(skills)
	return crew.Analyst{Name: "investigator-gcp", Desk: "gcp", State: "active", Skills: skills}
}

// ------------------------------------------------------------- the catalogue

// The decision per tool, written out here so that a tool added to the
// catalogue is a failing test until somebody says what each mode does with it.
var (
	withheldWhenMasked    = map[string]bool{"charges_query": true, "ai_calls_query": true}
	offeredWhenAggregates = map[string]bool{
		"team_month": true, "budgets": true, "variance": true, "kpis": true,
		"maturity": true, "allocation": true, "showback": true,
	}
)

func TestEveryToolHasAPolicyDecision(t *testing.T) {
	masked, _ := deliver.NewPolicy(deliver.PromptMasked, t.TempDir())
	agg, _ := deliver.NewPolicy(deliver.PromptAggregates, t.TempDir())
	full, _ := deliver.NewPolicy(deliver.PromptFull, t.TempDir())

	names := map[string]bool{}
	for _, tl := range catalogue {
		names[tl.Name] = true
		if !full.ToolOffered(tl.Name) {
			t.Errorf("full mode does not offer %s", tl.Name)
		}
		if got, want := masked.ToolOffered(tl.Name), !withheldWhenMasked[tl.Name]; got != want {
			t.Errorf("masked ToolOffered(%s) = %v, want %v", tl.Name, got, want)
		}
		if got, want := agg.ToolOffered(tl.Name), offeredWhenAggregates[tl.Name]; got != want {
			t.Errorf("aggregates ToolOffered(%s) = %v, want %v", tl.Name, got, want)
		}
	}
	for n := range withheldWhenMasked {
		if !names[n] {
			t.Errorf("this test names %s as withheld but the catalogue has no such tool", n)
		}
	}
	for n := range offeredWhenAggregates {
		if !names[n] {
			t.Errorf("this test names %s as offered under aggregates but the catalogue has no such tool", n)
		}
	}
	// A tool nobody has decided about is withheld by both restricting modes.
	for _, p := range []*deliver.Policy{masked, agg} {
		if p.ToolOffered("a_tool_added_tomorrow") {
			t.Errorf("%s mode offered a tool nobody has decided about", p.Mode())
		}
	}
}

func toolNames(tools []map[string]any, openAI bool) []string {
	var out []string
	for _, tl := range tools {
		if openAI {
			out = append(out, tl["function"].(map[string]any)["name"].(string))
		} else {
			out = append(out, tl["name"].(string))
		}
	}
	sort.Strings(out)
	return out
}

func TestSQLToolsAreNotOfferedUnderMaskedOrAggregates(t *testing.T) {
	db, ro, _ := installation(t)
	a := everyRight(t, db)

	withPolicy(t, db, deliver.PromptFull)
	if n := len(anthropicTools()); n != len(catalogue) {
		t.Fatalf("full mode offers %d of %d tools", n, len(catalogue))
	}

	for _, mode := range []deliver.PromptData{deliver.PromptMasked, deliver.PromptAggregates} {
		withPolicy(t, db, mode)
		for _, openAI := range []bool{false, true} {
			var tools []map[string]any
			if openAI {
				tools = openAITools()
			} else {
				tools = anthropicTools()
			}
			for _, n := range toolNames(tools, openAI) {
				if n == "charges_query" || n == "ai_calls_query" {
					t.Errorf("%s mode offered %s (openai=%v): a model that writes SQL can select any identifier", mode, n, openAI)
				}
			}
			if len(tools) == 0 {
				t.Errorf("%s mode offered no tool at all", mode)
			}
		}
		// The model can name a tool it was not offered; it must not be run.
		for _, name := range []string{"charges_query", "ai_calls_query"} {
			res := dispatch(context.Background(), db, ro, a, name,
				json.RawMessage(`{"sql":"SELECT agent FROM ai_calls"}`), bus{})
			if res.Outcome != outcomeWithheld {
				t.Errorf("%s mode: asking for %s gave outcome %q (%q), want %q",
					mode, name, res.Outcome, res.Text, outcomeWithheld)
			}
			if strings.Contains(res.Text, "agent://") {
				t.Errorf("%s mode: %s ran and returned an agent id: %q", mode, name, res.Text)
			}
		}
	}
	// aggregates offers exactly the tools that answer in totals
	withPolicy(t, db, deliver.PromptAggregates)
	got := toolNames(anthropicTools(), false)
	var want []string
	for n := range offeredWhenAggregates {
		want = append(want, n)
	}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("aggregates offers %v, want %v", got, want)
	}
}

// ---------------------------------------------------------- the tool results

// rowToken is a token of a kind that names a row-level thing. Matched as a
// token (hex digits after the dash), not as a substring.
var rowToken = regexp.MustCompile(`\b(svc|agent|user|inv|vendor|product|cmt|res|model|run|host)-[0-9a-f]{4,}\b`)

// toolArgs is one plausible call of each tool against the fixture, with the
// names a model would be using: real ones in full mode, tokens otherwise.
func toolArgs(t *testing.T, db *sql.DB, pol *deliver.Policy) map[string][]string {
	t.Helper()
	var src, team, svc string
	if err := db.QueryRow(`SELECT source, team, service FROM charges
		WHERE team IS NOT NULL AND team <> '' AND source='aws'
		GROUP BY 1,2,3 ORDER BY SUM(billed_cents) DESC LIMIT 1`).Scan(&src, &team, &svc); err != nil {
		t.Fatal(err)
	}
	var anom string
	if err := db.QueryRow(`SELECT id FROM anomalies ORDER BY id LIMIT 1`).Scan(&anom); err != nil {
		t.Fatal(err)
	}
	m := func(s string) string { return pol.MaskText(s) }
	q := func(format string, a ...any) string { return fmt.Sprintf(format, a...) }
	return map[string][]string{
		"anomaly":        {q(`{"id":%q}`, anom)},
		"series":         {q(`{"source":%q,"team":%q,"service":%q,"days":30}`, m(src), m(team), m(svc))},
		"drivers":        {q(`{"service":"*","since":"2026-01-01"}`), q(`{"service":%q,"since":"2026-01-01"}`, m("GKE"))},
		"team_month":     {q(`{"team":%q,"period":"2026-08"}`, m(team))},
		"charges_query":  {`{"sql":"SELECT team, service, SUM(billed_cents) FROM charges GROUP BY 1,2 LIMIT 50"}`},
		"ai_calls_query": {`{"sql":"SELECT agent, model, run_id, invoice_id FROM ai_calls"}`},
		"budgets":        {q(`{"source":%q,"period":"2026-08"}`, m(src))},
		"variance":       {q(`{"team":%q,"period":"2026-08"}`, m(team))},
		"kpis":           {`{"period":"2026-08"}`},
		"maturity":       {`{"period":"2026-08"}`},
		"allocation":     {`{"period":"2026-08"}`},
		"showback":       {q(`{"team":%q,"period":"2026-07"}`, m(team)), q(`{"team":%q,"period":"2026-08"}`, m(team))},
	}
}

func TestEveryToolIsExercisedByTheGate(t *testing.T) {
	db, _, _ := installation(t)
	pol := withPolicy(t, db, deliver.PromptFull)
	args := toolArgs(t, db, pol)
	for _, tl := range catalogue {
		if len(args[tl.Name]) == 0 {
			t.Errorf("the gate has no call for the tool %s, so it would never look at what it returns", tl.Name)
		}
	}
}

// TestNoRealIdentifierLeavesInAnyToolResult is the gate. Every tool, every
// mode, every plausible call, byte for byte.
func TestNoRealIdentifierLeavesInAnyToolResult(t *testing.T) {
	db, ro, check := installation(t)
	a := everyRight(t, db)
	if n := check.Identifiers(); n < 100 {
		t.Fatalf("the installation holds only %d identifiers; the gate would measure nothing", n)
	}

	// Full mode first: the tools DO return identifiers there, or this test
	// could be green because its fixture and its checker never met.
	pol := withPolicy(t, db, deliver.PromptFull)
	fullText := map[string]string{}
	carrying := 0
	for tool, calls := range toolArgs(t, db, pol) {
		for i, args := range calls {
			res := dispatch(context.Background(), db, ro, a, tool, json.RawMessage(args), bus{})
			if res.Outcome != outcomeOK {
				t.Fatalf("full mode: %s(%s) = %s: %s", tool, args, res.Outcome, res.Text)
			}
			fullText[fmt.Sprintf("%s#%d", tool, i)] = res.Text
			if len(check.Leaks(res.Text, a.Name)) > 0 {
				carrying++
			}
		}
	}
	if carrying < 8 {
		t.Fatalf("only %d tool calls carry an identifier in full mode; the checker or the fixture is blind", carrying)
	}

	for _, mode := range []deliver.PromptData{deliver.PromptMasked, deliver.PromptAggregates} {
		pol := withPolicy(t, db, mode)
		checked := 0
		for tool, calls := range toolArgs(t, db, pol) {
			for _, args := range calls {
				res := dispatch(context.Background(), db, ro, a, tool, json.RawMessage(args), bus{})
				if leaks := check.Leaks(res.Text, a.Name); len(leaks) > 0 {
					t.Errorf("%s mode: %s(%s) leaks %s\n--- result\n%s", mode, tool, args,
						strings.Join(leaks[:min(len(leaks), 6)], "; "), res.Text)
				}
				// Under aggregates not even a token of a row-level kind: a
				// service, an agent, an invoice is a row, masked or not.
				if mode == deliver.PromptAggregates {
					if tok := rowToken.FindString(res.Text); tok != "" {
						t.Errorf("aggregates: %s(%s) returned the token %q, which names a row-level thing\n%s", tool, args, tok, res.Text)
					}
				}
				if pol.ToolOffered(tool) {
					checked++
					if res.Outcome != outcomeOK {
						t.Errorf("%s mode: %s(%s) = %s: %s", mode, tool, args, res.Outcome, res.Text)
					}
					if len(res.Text) > toolResultMaxBytes {
						t.Errorf("%s mode: %s returned %d bytes, over the %d cap", mode, tool, len(res.Text), toolResultMaxBytes)
					}
				} else if res.Outcome != outcomeWithheld {
					t.Errorf("%s mode: %s is not offered and was %s", mode, tool, res.Outcome)
				}
			}
		}
		if checked < 8 {
			t.Fatalf("%s mode: only %d offered tool calls were checked", mode, checked)
		}
	}
}

// Under masked, a tool whose answer needs no structural change returns the
// full answer with the names turned to tokens and nothing else: the arguments
// were put back before it ran (or it would have answered "no budget found"),
// and the result was masked, not rewritten.
func TestAMaskedToolResultIsTheFullResultWithItsNamesMasked(t *testing.T) {
	db, ro, _ := installation(t)
	a := everyRight(t, db)

	full := map[string]string{}
	pol := withPolicy(t, db, deliver.PromptFull)
	for tool, calls := range toolArgs(t, db, pol) {
		for i, args := range calls {
			full[fmt.Sprintf("%s#%d", tool, i)] = dispatch(context.Background(), db, ro, a, tool, json.RawMessage(args), bus{}).Text
		}
	}

	pol = withPolicy(t, db, deliver.PromptMasked)
	same := 0
	for tool, calls := range toolArgs(t, db, pol) {
		switch tool {
		case "charges_query", "ai_calls_query", "drivers", "anomaly":
			continue // not offered, or a label withheld on purpose
		}
		for i, args := range calls {
			got := dispatch(context.Background(), db, ro, a, tool, json.RawMessage(args), bus{}).Text
			want := pol.MaskText(full[fmt.Sprintf("%s#%d", tool, i)], a.Name)
			if got != want {
				t.Errorf("%s(%s) under masked is not the masked full result.\n--- got\n%s\n--- want\n%s", tool, args, got, want)
				continue
			}
			if strings.Contains(got, "no budget found") || strings.Contains(got, "no series found") {
				t.Errorf("%s(%s): the tool did not find what the full call found: %q", tool, args, got)
			}
			same++
		}
	}
	if same < 6 {
		t.Fatalf("only %d tool calls were compared", same)
	}
}

func TestATokenTheModelInventedFindsNothingAndIsNotAnError(t *testing.T) {
	db, ro, _ := installation(t)
	a := everyRight(t, db)
	withPolicy(t, db, deliver.PromptMasked)
	res := dispatch(context.Background(), db, ro, a, "variance",
		json.RawMessage(`{"team":"team-0000","period":"2026-08"}`), bus{})
	if res.Outcome != outcomeOK || !strings.Contains(res.Text, "team-0000") {
		t.Errorf("an invented token gave %s: %q; it should be looked up as written and find nothing", res.Outcome, res.Text)
	}
}

// A tool's own error repeats what it was asked for. What it was asked for was
// re-identified first, so the error must be masked on the way out.
func TestAToolErrorThatEchoesANameIsMasked(t *testing.T) {
	db, ro, check := installation(t)
	a := everyRight(t, db)
	pol := withPolicy(t, db, deliver.PromptMasked)
	// the model passes a token that is a real team; the anomaly tool echoes
	// "no such anomaly: <that team>"
	team := "ml-platform"
	args := fmt.Sprintf(`{"id":%q}`, pol.MaskText(team))
	res := dispatch(context.Background(), db, ro, a, "anomaly", json.RawMessage(args), bus{})
	if res.Outcome != outcomeError {
		t.Fatalf("the premise failed: %s %q", res.Outcome, res.Text)
	}
	if leaks := check.Leaks(res.Text, a.Name); len(leaks) > 0 {
		t.Errorf("an error repeated a real name: %s in %q", strings.Join(leaks, "; "), res.Text)
	}
	if !strings.Contains(res.Text, pol.MaskText(team)) {
		t.Errorf("the error does not carry the token the model used: %q", res.Text)
	}
}

func TestAFullModeToolResultIsExactlyWhatTheToolReturns(t *testing.T) {
	db, ro, _ := installation(t)
	a := everyRight(t, db)
	withPolicy(t, db, deliver.PromptFull)
	var team string
	_ = db.QueryRow(`SELECT team FROM budgets WHERE team <> '' LIMIT 1`).Scan(&team)
	args := json.RawMessage(fmt.Sprintf(`{"team":%q,"period":"2026-08"}`, team))
	want, err := runVarianceTool(context.Background(), db, ro, args)
	if err != nil {
		t.Fatal(err)
	}
	if got := dispatch(context.Background(), db, ro, a, "variance", args, bus{}).Text; got != want {
		t.Errorf("full mode changed a tool result.\n got %q\nwant %q", got, want)
	}
}

// ------------------------------------------------------------- over the wire

// A model that is shown the prompt, asks for a tool in tokens, is shown the
// result, and answers in tokens. Everything the process sent is recorded.
type scriptedModel struct {
	mu       sync.Mutex
	requests []string // every request body, verbatim
	toolCall string   // the tool_use input, JSON
	answer   func(prompt string) string
	tool     string
}

func (m *scriptedModel) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.requests = append(m.requests, string(raw))
		n := len(m.requests)
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			fmt.Fprintf(w, `{"content":[{"type":"tool_use","id":"call-1","name":%q,"input":%s}],`+
				`"stop_reason":"tool_use","usage":{"input_tokens":20,"output_tokens":8}}`, m.tool, m.toolCall)
			return
		}
		text, _ := json.Marshal(m.answer(string(raw)))
		fmt.Fprintf(w, `{"content":[{"type":"text","text":%s}],"stop_reason":"end_turn",`+
			`"usage":{"input_tokens":40,"output_tokens":15}}`, text)
	}
}

func TestWhatReachesTheModelOverTheWireLeaksNoIdentifierAndTheDraftComesBackNamed(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	db, ro, check := installation(t)

	roster, _ := crew.Roster(db)
	var analyst crew.Analyst
	for _, a := range roster {
		if a.Name == "investigator-gcp" {
			analyst = a
		}
	}
	analyst.State = "active"
	analyst.Engine = "anthropic"
	tasks, _ := crew.Tasks(db, crew.TaskFilter{})
	var task crew.Task
	for _, tk := range tasks {
		if tk.Assignee == analyst.Name && tk.Anomaly != "" {
			task = tk
			break
		}
	}
	if task.ID == 0 {
		t.Fatal("the fixture has no anomaly task for investigator-gcp")
	}
	task.Goal = promptfixture.OperatorGoalText + " for the ml-platform team"

	for _, mode := range []deliver.PromptData{deliver.PromptMasked, deliver.PromptAggregates} {
		t.Run(string(mode), func(t *testing.T) {
			pol := withPolicy(t, db, mode)
			team := pol.MaskText("ml-platform")
			model := &scriptedModel{
				tool:     "team_month",
				toolCall: fmt.Sprintf(`{"team":%q,"period":"2026-08"}`, team),
				answer: func(string) string {
					return "## " + team + " overspent\nAsk " + team + " about it.\n"
				},
			}
			srv := httptest.NewServer(model.handler())
			defer srv.Close()

			e := price(db, task, analyst, 200)
			e.Engine, e.Model = "anthropic", "claude-x"
			e.Price, e.Priced = engines.Price{InPerM: 1, OutPerM: 1}, true
			e.WorstMicros = 5_000
			run := &runBudget{ceilingMicros: 10_000_000}
			gw := gatewayConfig{URL: srv.URL, Host: "x.test", CeilingUSD: money.Cents(10_000_00)}
			if err := execute(context.Background(), db, ro, e, 200, run, bus{run: "r1", promptData: string(mode)}, gw); err != nil {
				t.Fatalf("execute: %v", err)
			}

			if len(model.requests) < 2 {
				t.Fatalf("the model was called %d time(s); the tool round trip did not happen", len(model.requests))
			}
			for i, body := range model.requests {
				// the JSON body is the wire: decode so escapes cannot hide a name
				var decoded any
				if err := json.Unmarshal([]byte(body), &decoded); err != nil {
					t.Fatal(err)
				}
				flat, _ := json.Marshal(decoded)
				text := strings.NewReplacer(`\n`, "\n", `\"`, `"`, `>`, ">", `&`, "&", `<`, "<").Replace(string(flat))
				// The working analyst's own persona, brief and job description
				// come first and are the analyst's own configuration (they say
				// "the gcp desk" because the analyst IS on it, and its name
				// says so too). Everything from the packet on is data: the
				// packet, the task, the tool schemas and every tool result.
				at := strings.Index(text, "TASK PACKET")
				if at < 0 {
					t.Fatalf("request %d carries no packet: %.200s", i+1, text)
				}
				if leaks := check.Leaks(text[at:], analyst.Name); len(leaks) > 0 {
					t.Errorf("request %d to the model leaks %s", i+1, strings.Join(leaks[:min(len(leaks), 8)], "; "))
				}
			}
			if !strings.Contains(model.requests[0], "Prompt data policy: "+string(mode)) {
				t.Error("the first request does not state its mode")
			}

			// The answer was written in tokens. The draft a person reads has names.
			var body string
			if err := db.QueryRow(`SELECT body FROM artifacts WHERE task=? AND source='live' ORDER BY id DESC LIMIT 1`, task.ID).Scan(&body); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(body, "## ml-platform overspent") || strings.Contains(body, team) {
				t.Errorf("the saved draft was not re-identified: %q", body)
			}
			if _, err := db.Exec(`DELETE FROM artifacts WHERE task=? AND source='live'`, task.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// ------------------------------------------------------------------- the bus

type captureRec struct{ events []map[string]any }

func (c *captureRec) Emit(kind, actor, severity string, data map[string]any, _ []string) error {
	d := map[string]any{"kind": kind}
	for k, v := range data {
		d[k] = v
	}
	c.events = append(c.events, d)
	return nil
}

func TestTheModeIsOnTheToolCallEventsAndTheCrewRanSummary(t *testing.T) {
	db, ro, _ := installation(t)
	a := everyRight(t, db)
	for _, mode := range []deliver.PromptData{deliver.PromptFull, deliver.PromptMasked, deliver.PromptAggregates} {
		t.Run(string(mode), func(t *testing.T) {
			withPolicy(t, db, mode)
			b, path := testBus(t, "x.test", "run-1")
			b.promptData = string(mode)
			rec := &captureRec{}
			b.rec = rec

			// a tool dispatch, a model call, and a finished run
			dispatch(context.Background(), db, ro, a, "kpis", json.RawMessage(`{"period":"2026-08"}`), b)
			if err := b.toolCall(estimate{Analyst: a, Engine: "anthropic", Model: "m"}, callResult{Text: "x"}); err != nil {
				t.Fatal(err)
			}
			if err := b.crewRan("Sprint X", 1, 0, 1234, money.Cents(500), "alice"); err != nil {
				t.Fatal(err)
			}

			var sawDispatch, sawCall, sawRan bool
			for _, ev := range allEvents(t, path) {
				data, _ := ev["data"].(map[string]any)
				switch ev["type"] {
				case "tool_call":
					if data["prompt_data"] != string(mode) {
						t.Errorf("a tool_call event says prompt_data=%v, want %s: %v", data["prompt_data"], mode, data)
					}
					if _, isDispatch := data["tool"]; isDispatch {
						sawDispatch = true
					} else {
						sawCall = true
					}
				case "crew_ran":
					sawRan = true
					if data["prompt_data"] != string(mode) {
						t.Errorf("crew_ran on the bus says prompt_data=%v, want %s", data["prompt_data"], mode)
					}
				}
			}
			if !sawDispatch || !sawCall || !sawRan {
				t.Errorf("events seen: tool dispatch %v, model call %v, crew_ran %v", sawDispatch, sawCall, sawRan)
			}
			for _, ev := range rec.events {
				if ev["kind"] == "crew_ran" && ev["prompt_data"] != string(mode) {
					t.Errorf("crew_ran in the journal says prompt_data=%v, want %s", ev["prompt_data"], mode)
				}
			}
		})
	}
}

// A bus nobody told anything reads as full: it sent what it always sent.
func TestABusThatWasNeverToldItsModeSaysFull(t *testing.T) {
	if got := (bus{}).mode(); got != "full" {
		t.Errorf("a zero bus says %q", got)
	}
}

// ---------------------------------------------------------------- the flag

// The flag refuses a misspelling before anything is opened, priced or spent.
func TestAMisspeltPromptDataFlagRefusesToStartTheRunner(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "costcrew-run")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building: %v\n%s", err, out)
	}
	for _, bad := range []string{"maskd", "Masked", ""} {
		data := filepath.Join(t.TempDir(), "data")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, err := exec.CommandContext(ctx, bin, "-data", data, "-prompt-data", bad).CombinedOutput()
		cancel()
		if err == nil {
			t.Errorf("-prompt-data %q started: %s", bad, out)
			continue
		}
		if !strings.Contains(string(out), "full, masked, aggregates") {
			t.Errorf("-prompt-data %q was refused without naming the three modes: %s", bad, out)
		}
		if _, statErr := os.Stat(filepath.Join(data, "app.db")); statErr == nil {
			t.Errorf("-prompt-data %q opened a store before refusing", bad)
		}
	}

	// the environment twin is parsed by the same function
	data := filepath.Join(t.TempDir(), "data")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-data", data)
	cmd.Env = append(os.Environ(), "COSTCREW_PROMPT_DATA=maskd")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("COSTCREW_PROMPT_DATA=maskd started: %s", out)
	}
}

func TestADryRunUnderMaskedSaysSoAndMakesAKey(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "costcrew-run")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building: %v\n%s", err, out)
	}
	installation(t) // a populated store to read; a dry run reads and writes nothing
	data := sharedInst.dir
	out, err := exec.Command(bin, "-data", data, "-prompt-data", "masked").CombinedOutput()
	if err != nil {
		t.Fatalf("a dry run under masked failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Prompt data policy: masked.") {
		t.Errorf("the run did not say it was masked:\n%s", out)
	}
	fi, err := os.Stat(filepath.Join(data, deliver.KeyFileName))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("no 0600 key in the data directory after a masked run: %v %v", fi, err)
	}
}

// ---------------------------------------------------------- the catalogue text

// The examples in the schemas are masked and nothing else of them is: this
// console's own words ("the desk", "the month") must survive a dictionary that
// holds team names like "research" and "growth".
func TestMaskingTheCatalogueOnlyChangesItsExamples(t *testing.T) {
	db, _, check := installation(t)
	example := regexp.MustCompile(`e\.g\..*$`)
	strip := func(s string) string { return example.ReplaceAllString(s, "") }

	var walk func(path string, orig, got any)
	walk = func(path string, orig, got any) {
		switch o := orig.(type) {
		case map[string]any:
			g := got.(map[string]any)
			for k, v := range o {
				if s, ok := v.(string); ok && k == "description" {
					if strip(s) != strip(g[k].(string)) {
						t.Errorf("%s.%s was changed by more than its example:\n  was %q\n  now %q", path, k, s, g[k])
					}
					continue
				}
				walk(path+"."+k, v, g[k])
			}
		}
	}
	examples := 0
	for _, mode := range []deliver.PromptData{deliver.PromptMasked, deliver.PromptAggregates} {
		pol := withPolicy(t, db, mode)
		for _, tl := range offered() {
			orig, _ := toolByName(tl.Name)
			if strip(orig.Description) != strip(tl.Description) {
				t.Errorf("%s: %s's description was changed by more than an example:\n  was %q\n  now %q",
					mode, tl.Name, orig.Description, tl.Description)
			}
			walk(tl.Name, orig.Schema, tl.Schema)
			if leaks := check.Leaks(tl.Description+fmt.Sprint(tl.Schema), ""); len(leaks) > 0 {
				t.Errorf("%s: the schema of %s carries %s", mode, tl.Name, strings.Join(leaks, "; "))
			}
			if pol.Mode() != deliver.PromptFull && example.MatchString(fmt.Sprint(tl.Schema)) {
				examples++
			}
		}
	}
	if examples == 0 {
		t.Fatal("no schema carries an example, so this test looked at nothing")
	}
	// and the table itself was not touched
	if d, _ := toolByName("series"); !strings.Contains(fmt.Sprint(d.Schema), "e.g. aws") {
		t.Error("masking a rendering rewrote the catalogue's own table")
	}
}

// What the catalogue costs is bounded by deliver.ToolCatalogueTokens, which
// is priced in advance. A restricting mode offers fewer tools with slightly
// longer examples, and must stay inside the bound.
func TestAMaskedCatalogueStaysInsideItsPricedBound(t *testing.T) {
	db, _, _ := installation(t)
	for _, mode := range []deliver.PromptData{deliver.PromptFull, deliver.PromptMasked, deliver.PromptAggregates} {
		withPolicy(t, db, mode)
		for engine, tools := range map[string][]map[string]any{"anthropic": anthropicTools(), "openrouter": openAITools()} {
			raw, err := json.Marshal(tools)
			if err != nil {
				t.Fatal(err)
			}
			if bound := deliver.ToolCatalogueTokens(engine); len(raw) > bound {
				t.Errorf("%s mode, %s: the catalogue is %d bytes and the priced bound is %d", mode, engine, len(raw), bound)
			}
		}
	}
}

// ------------------------------------------------------------- hostile input

// A real name is data. A team called `ml","period":"2099-01` that is spliced
// into the text of the arguments closes the string it sits in and writes an
// argument of its own: the model's tokens were put back by replacing inside
// the raw JSON, and the data decided what the call asked for.
func TestPuttingANameBackCannotWriteArgumentsOfItsOwn(t *testing.T) {
	db, ro, _ := installation(t)
	pol := withPolicy(t, db, deliver.PromptMasked)
	evil := `ml","period":"2099-01`
	if _, err := db.Exec(`INSERT INTO teams(name, owner) VALUES (?, 'alice')`, evil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM teams WHERE name=?`, evil) })
	tok := pol.MaskText(evil)
	if tok == evil {
		t.Fatal("the hostile name was not masked, so the premise fails")
	}

	got := reidentifyArgs(pol, json.RawMessage(`{"team":"`+tok+`","period":"2026-08"}`))
	var args map[string]string
	if err := json.Unmarshal(got, &args); err != nil {
		t.Fatalf("the arguments are no longer JSON after a name was put back: %v\n%s", err, got)
	}
	if args["team"] != evil || args["period"] != "2026-08" || len(args) != 2 {
		t.Errorf("the name rewrote the call: %v", args)
	}

	a := everyRight(t, db)
	res := dispatch(context.Background(), db, ro, a, "variance",
		json.RawMessage(`{"team":"`+tok+`","period":"2026-08"}`), bus{})
	if res.Outcome != outcomeOK || strings.Contains(res.Text, "2099") {
		t.Errorf("the tool ran with the period the NAME chose: %s %q", res.Outcome, res.Text)
	}
}

func TestArgumentsThatAreNotJSONAreLeftForTheValidatorToRefuse(t *testing.T) {
	db, ro, _ := installation(t)
	pol := withPolicy(t, db, deliver.PromptMasked)
	a := everyRight(t, db)
	tok := pol.MaskText("ml-platform")
	for _, raw := range []string{``, `not json`, `{"team":`, `[1,2`, `{"team":"` + tok + `"`, strings.Repeat("[", 100000)} {
		got := reidentifyArgs(pol, json.RawMessage(raw))
		if len(raw) < 1000 && string(got) != raw {
			t.Errorf("malformed arguments %q were rewritten to %q", raw, got)
		}
		res := dispatch(context.Background(), db, ro, a, "variance", json.RawMessage(raw), bus{})
		if res.Outcome != outcomeInvalidArgs {
			t.Errorf("arguments %.40q gave %s (%q), want %s", raw, res.Outcome, res.Text, outcomeInvalidArgs)
		}
	}
}

func TestANumberAndAnArrayInTheArgumentsSurviveBeingRewritten(t *testing.T) {
	db, _, _ := installation(t)
	pol := withPolicy(t, db, deliver.PromptMasked)
	tok := pol.MaskText("ml-platform")
	got := reidentifyArgs(pol, json.RawMessage(`{"days":30,"big":12345678901234567890,"team":"`+tok+`","xs":["`+tok+`",1,null,true]}`))
	var v map[string]any
	d := json.NewDecoder(strings.NewReader(string(got)))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(v["days"]) != "30" || fmt.Sprint(v["big"]) != "12345678901234567890" || v["team"] != "ml-platform" {
		t.Errorf("rewriting the arguments changed a value: %s", got)
	}
	if xs := v["xs"].([]any); xs[0] != "ml-platform" || fmt.Sprint(xs[1]) != "1" || xs[2] != nil || xs[3] != true {
		t.Errorf("rewriting the arguments changed an array: %s", got)
	}
}

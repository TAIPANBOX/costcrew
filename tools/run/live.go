package main

// The half that can spend.
//
// It is a separate file on purpose. main.go holds the estimator and holds no
// way to call anything, and TestThisBinaryCannotSpend reads that file to keep
// it so. Everything that can put a charge on somebody's account lives here,
// where it can be read in one sitting.
//
// Four things bound it, and every one of them refuses BEFORE a call rather
// than reporting after:
//
//  1. -live must be passed. Without it nothing here runs at all.
//  2. -ceiling must be passed with it. A run with no ceiling is refused, not
//     defaulted: a default ceiling is a number nobody chose.
//  3. The worst case of the whole run is checked against that ceiling before
//     the first call.
//  4. Each call is checked against what is left of its task's guard AND
//     against what is left of the run's ceiling, using the same worst-case
//     arithmetic the dry run prints.
//
// The credential is read from the environment and never written anywhere: not
// to the database, not to the journal, not into an error message.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/money"
	"sync"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/stack"
)

// callResult is what came back, and what it actually cost.
type callResult struct {
	Text         string
	InTokens     int
	OutTokens    int
	ActualMicros int64
}

// gatewayConfig is this INVOCATION's gateway setup: the same for every call a
// run makes, built once from -gateway, -stack-host and -ceiling.
//
// An empty URL means the gateway is off, and the Anthropic route calls
// api.anthropic.com exactly as it did before this file knew a gateway
// existed. Only the Anthropic route uses this: OpenRouter and Bedrock keep
// calling their own hosts directly, because TokenFuse speaks the Anthropic
// Messages API at /v1/messages and nothing OpenAI-shaped.
type gatewayConfig struct {
	URL        string      // normalized: http(s) only, no trailing slash
	Host       string      // this installation's trust domain, for the agent id
	CeilingUSD money.Cents // the run's ceiling, i.e. -ceiling parsed
}

func (g gatewayConfig) on() bool { return g.URL != "" }

// gatewayHeaders is what ONE call tells TokenFuse: who is asking, on whose
// run, and what it may spend. Built fresh per call because the budget is the
// tighter of the run's ceiling and THIS task's own guard, which differs task
// to task even though the run id and the agent id do not.
type gatewayHeaders struct {
	URL       string // empty means "do not route through a gateway at all"
	RunID     string
	AgentID   string
	BudgetUSD string // already formatted, two decimals minimum

	// ParentRunID is sent only when non-empty. This runner has no notion of a
	// parent run today (crew.Analyst.Parent is a different thing: it names
	// who an agent acts on behalf of, not which run started this one), and
	// item 1 of this change is explicit that one must not be invented here.
	// The field exists so a caller that DOES have one someday can set it
	// without another signature change.
	ParentRunID string

	// x-fuse-outcome is deliberately not a field here and is never sent by
	// this file. It is TokenFuse's opaque tag for how a call ended, and this
	// step has nothing worth reporting there yet: adding it later is an
	// additive change, and sending an empty or guessed value now would be
	// worse than the header's plain absence.
}

// gatewayHeadersFor builds one call's headers from the run's shared config
// and that call's own task guard and analyst name. cfg.on() must be checked
// by the caller; this only formats.
func gatewayHeadersFor(cfg gatewayConfig, runID, analystName string, taskGuard money.Cents) gatewayHeaders {
	return gatewayHeaders{
		URL:       cfg.URL,
		RunID:     runID,
		AgentID:   stack.AgentURI(cfg.Host, analystName),
		BudgetUSD: gatewayBudgetUSD(cfg.CeilingUSD, taskGuard),
	}
}

// gatewayBudgetUSD is the tighter of the run's ceiling and the task's own
// guard. Sending the wider of the two would let the gateway wave through a
// call this runner's own reservation would already have refused, which
// would make the header a decoration rather than a real second bound.
func gatewayBudgetUSD(runCeiling, taskGuard money.Cents) string {
	b := runCeiling
	if taskGuard > 0 && taskGuard < b {
		b = taskGuard
	}
	return b.String()
}

// gatewayEnvDefault backs -gateway's default with COSTCREW_GATEWAY, so an
// installation can set it once rather than on every invocation's command
// line. It lives here and not in main.go: main.go is proved to hold no way to
// read the environment (TestThisBinaryCannotSpend reads main.go's own source
// for the literal substring "os.Getenv"), and this is genuinely that.
func gatewayEnvDefault() string {
	return strings.TrimSpace(os.Getenv("COSTCREW_GATEWAY"))
}

// normalizeGateway validates -gateway and strips a trailing slash, so
// <gateway>/v1/messages never doubles a slash. A scheme other than http(s) is
// refused here, before the run does anything, rather than surfacing as a
// confusing dial error on the first call an analyst happens to make.
func normalizeGateway(raw string) (string, error) {
	// The literal empty string, and only that, means "not configured": it is
	// what an unset flag defaulting to gatewayEnvDefault() carries when
	// COSTCREW_GATEWAY is not set either. Anything else that trims down to
	// nothing (whitespace on its own) was NOT left unset; it is refused
	// rather than silently read the same as "off", which would turn a typo
	// or a bad script interpolation into a quiet fallback to
	// api.anthropic.com nobody asked for.
	if raw == "" {
		return "", nil
	}
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if trimmed == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("-gateway %q is not an http(s) URL", raw)
	}
	return strings.TrimRight(trimmed, "/"), nil
}

// directCallsNotice is the one line a run prints when -gateway is set and
// some of its work is on an engine the gateway cannot front. Not an error and
// not silent: OpenRouter and Bedrock keep calling their own hosts directly
// until TokenFuse grows an OpenAI-shaped route, and a person watching the run
// should be told that is happening rather than notice its absence from a
// trace later.
func directCallsNotice(gatewayOn bool, todo []estimate) string {
	if !gatewayOn {
		return ""
	}
	n := 0
	for _, e := range todo {
		if e.Engine != "anthropic" {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d call(s) on openrouter/bedrock go direct: TokenFuse "+
		"has no OpenAI-shaped route yet.\n", n)
}

// parseGatewayRefusal reads TokenFuse's 402 body into the sentence a person
// reads. The body is untrusted input from a process this runner does not
// control: a body that is not the documented shape still produces a readable
// sentence rather than a panic or a silently empty one.
func parseGatewayRefusal(raw []byte) error {
	var out struct {
		Error struct {
			Type      string  `json:"type"`
			BudgetUSD float64 `json:"budget_usd"`
			SpentUSD  float64 `json:"spent_usd"`
			Reason    string  `json:"reason"`
			RunID     string  `json:"run_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Error.Reason == "" {
		return fmt.Errorf("the gateway refused this call with 402, and its body "+
			"was not the documented shape: %s", trim(strings.TrimSpace(string(raw)), 200))
	}
	return fmt.Errorf("the gateway refused this call: %s (budget %.4f, spent %.4f, run %s)",
		out.Error.Reason, out.Error.BudgetUSD, out.Error.SpentUSD, out.Error.RunID)
}

// call routes to the engine the analyst was HIRED with.
//
// Which engine an analyst runs on is a decision recorded at hire time and
// visible on its card, and this is where that decision finally does
// something. A router that ignored it would make the field decoration.
//
// gw is only used by the Anthropic route. OpenRouter and Bedrock are
// unchanged: TokenFuse speaks the Anthropic Messages API at /v1/messages and
// nothing OpenAI-shaped, so those two keep calling their own hosts directly
// until it grows a route for them.
func call(ctx context.Context, engine, model, prompt string, maxTok int, gw gatewayHeaders) (callResult, error) {
	switch engine {
	case "openrouter":
		return callOpenRouter(ctx, model, prompt, maxTok)
	case "anthropic":
		return callAnthropic(ctx, model, prompt, maxTok, gw)
	case "bedrock":
		return callBedrock(ctx, model, prompt, maxTok)
	}
	return callResult{}, fmt.Errorf("no caller is written for engine %q", engine)
}

// callAnthropic is the second of the two functions here that spend money.
//
// A different wire from the router's: the key travels in x-api-key rather
// than a bearer token, the version header is required, and usage comes back
// as input_tokens and output_tokens rather than prompt and completion. Nothing
// about that is guessable, which is why it is written out rather than shared
// with a "compatible" abstraction that would be wrong in one of them.
// anthropicBody is the request, separate so the one thing that is easy to get
// silently wrong can be tested without spending anything.
//
// Thinking is turned OFF explicitly. Four tasks on a full run came back with
// "no text", and the reason, once the error said it, was exact:
//
//	stop_reason "max_tokens", blocks: thinking, 1200 output tokens
//
// The model spent the entire budget reasoning and never reached the answer.
// What this runner wants is the deliverable, not the reasoning, so the fix is
// to ask for the deliverable. It also makes the calls CHEAPER, which is the
// unusual direction for a fix.
func anthropicBody(model, prompt string, maxTok int) ([]byte, error) {
	return json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": maxTok,
		"thinking":   map[string]any{"type": "disabled"},
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	})
}

// anthropicRequest builds the outbound request, separately from sending it,
// so the URL and every header can be asserted without a network call and
// without a key: TestWithNoGatewayTheRequestGoesToAnthropicDirectly does
// exactly that, on the exposed *http.Request, never on the wire.
//
// gw.URL empty routes to api.anthropic.com exactly as this runner did before
// it knew a gateway existed. gw.URL set routes to <gateway>/v1/messages
// instead and carries the x-fuse-* headers TokenFuse reads for metering and
// attribution; the API key travels in x-api-key exactly as today, which is
// what lets the gateway pass it through unchanged to the real upstream.
func anthropicRequest(ctx context.Context, key, model, prompt string, maxTok int, gw gatewayHeaders) (*http.Request, error) {
	body, err := anthropicBody(model, prompt, maxTok)
	if err != nil {
		return nil, err
	}
	endpoint := "https://api.anthropic.com/v1/messages"
	if gw.URL != "" {
		endpoint = gw.URL + "/v1/messages"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	if gw.URL != "" {
		// TokenFuse refuses a call with no run id (400 metering_required), so
		// this is never conditional on RunID being non-empty: openBus mints
		// one for every invocation now, on or off the estate bus, for exactly
		// this reason.
		req.Header.Set("x-fuse-run-id", gw.RunID)
		req.Header.Set("x-fuse-agent-id", gw.AgentID)
		req.Header.Set("x-fuse-budget-usd", gw.BudgetUSD)
		if gw.ParentRunID != "" {
			req.Header.Set("x-fuse-parent-run-id", gw.ParentRunID)
		}
		// x-fuse-outcome is intentionally not set. See gatewayHeaders.
	}
	return req, nil
}

func callAnthropic(ctx context.Context, model, prompt string, maxTok int, gw gatewayHeaders) (callResult, error) {
	key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if key == "" {
		return callResult{}, fmt.Errorf("ANTHROPIC_API_KEY is not set in this process")
	}
	req, err := anthropicRequest(ctx, key, model, prompt, maxTok, gw)
	if err != nil {
		return callResult{}, err
	}

	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return callResult{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	// A 402 from the GATEWAY is a budget refusal, not an ordinary failed
	// call, and it is treated exactly like the runner's own ceiling check:
	// execute() already returns the reservation on any error here, and
	// wrapping this one in refusal{} is what makes spend()'s loop stop the
	// run instead of marking the task blocked, which is the reading a
	// budget event deserves over "this analyst's call failed".
	//
	// Gated on gw.URL != "" so a 402 that somehow came from api.anthropic.com
	// directly (Anthropic does not use this status, but nothing here should
	// assume that forever) is never misread as a gateway refusal it did not
	// send.
	if resp.StatusCode == http.StatusPaymentRequired && gw.URL != "" {
		return callResult{}, refusal{parseGatewayRefusal(raw)}
	}
	if resp.StatusCode != 200 {
		return callResult{}, fmt.Errorf("anthropic answered %d: %s",
			resp.StatusCode, trim(strings.TrimSpace(string(raw)), 160))
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return callResult{}, fmt.Errorf("anthropic answered 200 with an empty body")
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return callResult{}, fmt.Errorf("anthropic's answer did not parse: %w", err)
	}
	var text strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	if text.Len() == 0 {
		// Say WHY. "returned no text" blocked four tasks on a full run and
		// named nothing a person could act on: a refusal, a turn that stopped
		// on max_tokens before writing a word, and an empty content array are
		// three different problems wearing one message.
		kinds := make([]string, 0, len(out.Content))
		for _, c := range out.Content {
			kinds = append(kinds, c.Type)
		}
		where := "no content blocks at all"
		if len(kinds) > 0 {
			where = "blocks: " + strings.Join(kinds, ", ")
		}
		return callResult{}, fmt.Errorf(
			"anthropic returned no text (stop_reason %q, %s, %d output tokens)",
			out.StopReason, where, out.Usage.OutputTokens)
	}
	return callResult{
		Text:      text.String(),
		InTokens:  out.Usage.InputTokens,
		OutTokens: out.Usage.OutputTokens,
	}, nil
}

// callOpenRouter is the first.
func callOpenRouter(ctx context.Context, model, prompt string, maxTok int) (callResult, error) {
	key := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if key == "" {
		return callResult{}, fmt.Errorf("OPENROUTER_API_KEY is not set in this process")
	}
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": maxTok,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return callResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return callResult{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		// The body can echo a request, so only the status and a short prefix
		// travel: a key does not end up in a log through an error message.
		return callResult{}, fmt.Errorf("the router answered %d: %s",
			resp.StatusCode, trim(strings.TrimSpace(string(raw)), 160))
	}
	var out struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return callResult{}, fmt.Errorf("the router answered 200 with an empty body")
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return callResult{}, fmt.Errorf("the router's answer did not parse: %w", err)
	}
	if len(out.Choices) == 0 {
		return callResult{}, fmt.Errorf("the router returned no answer")
	}
	return callResult{
		Text:      out.Choices[0].Message.Content,
		InTokens:  out.Usage.PromptTokens,
		OutTokens: out.Usage.CompletionTokens,
	}, nil
}

// prompt is what the analyst is asked, built only from what the console holds.
//
// The task, and the brief the analyst was hired with. Nothing else: an analyst
// without figures-read is not handed figures, and this is where that rule is
// kept rather than hoped for.
//
// packetText is the TASK PACKET (packet.go), inserted here rather than
// built by this function: it needs a database read the estimator's own
// worst-case measurement (main.go's price()) must not repeat at call time,
// since the estate can move between pricing a run and executing it and a
// bound only true of a moment ago is not a bound. price() calls packet()
// once and carries the result in estimate.Packet; execute() passes that
// same string back in here unchanged. An empty packetText renders nothing,
// which is right both for a task with no figures section to show and for
// every existing caller that has never heard of a packet.
func prompt(t crew.Task, a crew.Analyst, today, packetText string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s, %s on the %s desk of a FinOps practice.\n", a.Name, a.Role, a.Desk)
	if a.Mission != "" {
		fmt.Fprintf(&b, "Your brief: %s\n", a.Mission)
	}
	b.WriteString(jobDescriptionBlock(a.Name, a.Desk))
	b.WriteString(packetText)
	fmt.Fprintf(&b, "\nThe task on your desk is %q.\n", t.Title)
	if t.Goal != "" {
		fmt.Fprintf(&b, "What it asks for: %s\n", t.Goal)
	}
	// The date, because it asked for one and got no answer.
	//
	// A live run produced "**Date:** [Today's Date]" on the face of a
	// deliverable a person was meant to read. A model has no clock, so the
	// choices are to give it the date or to have it guess; and this console's
	// whole argument is that a figure nobody can check is worse than no figure.
	fmt.Fprintf(&b, "\nToday is %s.\n", today)

	// The format, kept to what the console renders.
	//
	// The renderer is deliberately tiny and now covers headings, rules, lists,
	// bold and italic. Asking for a narrow format is cheaper than widening it
	// further, and the renderer holds either way: a model that ignores this
	// still has to come out readable.
	b.WriteString("Use plain prose with ## headings, **bold** and simple " +
		"- bullets. No tables, no code fences.\n")

	b.WriteString(optionsBlockInstructions(a.Name, a.Desk))

	b.WriteString("\nWrite the deliverable. Be specific, say what you do not know, " +
		"and do not invent a number you were not given.\n")
	return b.String()
}

// optionsBlockInstructions tells the model the one shape it must not use
// prose for: the options block, fenced and tagged, at the very end. The
// classes it may name come from the SAME job description jobDescriptionBlock
// already printed above ("You may decide alone" / "You hand to the
// supervisor") -- this repeats them as a closed list next to the JSON shape
// itself, from the same roles.yaml data, so the vocabulary the model sees has
// one source rather than two texts that could drift (B3-SPEC.md section 2:
// "the prompt tells the model the block's shape and the classes it may name,
// from the same roles data").
//
// Empty when the role matches no family, the same additive rule
// jobDescriptionBlock and packet() already hold: nothing here should tell a
// model to produce a shape this console cannot check.
func optionsBlockInstructions(name, desk string) string {
	r, ok := crew.RoleForDesk(name, desk)
	if !ok {
		return ""
	}
	legal := crew.ValidClassesFor(r)
	if len(legal) == 0 {
		if crew.AllowsNoOptions(r) {
			return "\nThis role's deliverable is prose; it needs no options block.\n"
		}
		return ""
	}
	classes := make([]string, 0, len(legal))
	for c := range legal {
		classes = append(classes, c)
	}
	sort.Strings(classes)

	var b strings.Builder
	b.WriteString("\nEnd the deliverable with a fenced block tagged options, JSON, " +
		"naming one to three courses of action -- never one you have already taken:\n")
	b.WriteString("```options\n")
	b.WriteString(`{"options": [{"class": "...", "summary": "...", "figure_cents": 0, ` +
		`"saving_cents": 0, "risk": "low|medium|high", "needs": "nothing|a person to ...", ` +
		"\"evidence\": [\"...\"]}]}\n")
	b.WriteString("```\n")
	fmt.Fprintf(&b, "class must be one of: %s. figure_cents and saving_cents are whole "+
		"numbers of cents, never a decimal. This deliverable proposes; it never applies "+
		"anything itself.\n", strings.Join(classes, ", "))
	if crew.AllowsNoOptions(r) {
		b.WriteString("Zero options is fine here if there is nothing to decide.\n")
	}
	return b.String()
}

// execute runs ONE task and records what it produced and what it cost.
//
// The artifact is a draft, never a post. Only a person's stamp publishes, and
// that invariant is older than this file.
//
// roDB is charges_query's read-only pool (internal/store.OpenReadOnly),
// threaded through to the dispatcher for the one tool that needs it; every
// other tool call in the loop below reads db, same as saveDraft does.
func execute(ctx context.Context, db, roDB *sql.DB, e estimate, maxTok int, run *runBudget, b bus, gw gatewayConfig) error {
	if e.Refused {
		return fmt.Errorf("refused before the call: %s", e.Verdict)
	}

	// Every round of the tool loop is its own model call (B2-SPEC.md
	// section 3.4), so the reservation covers the worst case
	// loopsFor(e.Engine) times over, before the first round rather than
	// growing it round by round: TestTheLoopStopsAtMaxRounds is what proves
	// six rounds fit under it. An engine outside the loop (Bedrock, or
	// anything unknown) still reserves exactly one call's worth, as before
	// this file knew a loop existed.
	loops := int64(loopsFor(e.Engine))
	reserveMicros := e.WorstMicros * loops
	if err := run.reserve(reserveMicros); err != nil {
		return refusal{err}
	}

	// The headers for THIS call, built fresh every time even though the URL,
	// the run id and the trust domain never change within a run: the budget
	// is the tighter of the ceiling and THIS task's own guard, and the agent
	// id names THIS task's analyst. gw.on() false leaves gh at its zero
	// value, which every round below (via anthropicRound, or call() for an
	// engine outside the loop) reads as "no gateway" and routes to
	// api.anthropic.com exactly as before this file knew one existed. The
	// same gh is passed to every round, so every round carries the same
	// three x-fuse headers.
	var gh gatewayHeaders
	if gw.on() {
		gh = gatewayHeadersFor(gw, b.run, e.Analyst.Name, e.Task.Budget)
	}
	sent := prompt(e.Task, e.Analyst, time.Now().Format("2006-01-02"), e.Packet)
	res, err := runToolLoop(ctx, db, roDB, e, sent, maxTok, gh, e.Analyst, b)
	if err != nil {
		run.settle(reserveMicros, res.ActualMicros)
		return err
	}
	run.settle(reserveMicros, res.ActualMicros)

	if err := saveDraft(db, e, res, b); err != nil {
		return err
	}

	fmt.Printf("  %-22s %-14s %-10s in %5d out %5d  cost %s  (worst %s)\n",
		trim(e.Task.Title, 22), e.Analyst.Name, trim(e.Engine, 10),
		res.InTokens, res.OutTokens, usd(res.ActualMicros), usd(e.WorstMicros))
	return nil
}

// saveDraft writes what the model produced and what it cost.
//
// It is separate from execute so that it can be tested without a network call,
// which is the only way to hold the property that matters here: a deliverable
// a model actually wrote is MARKED as one.
//
// The estate ships 279 generated drafts. A live run adds real ones to the same
// table, with the same author and the same state, and for one run 63 real
// deliverables sat indistinguishable among 342. Two kinds of thing under one
// heading is the fault this console exists to catch in other people's data.
func saveDraft(db *sql.DB, e estimate, res callResult, b bus) error {
	title := "Deliverable for " + e.Task.Title
	ins, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created, source)
		VALUES (?,?,?,?, 'draft', datetime('now'), 'live')`,
		e.Task.ID, e.Analyst.Name, trim(title, 120), res.Text)
	if err != nil {
		return err
	}
	artifactID, err := ins.LastInsertId()
	if err != nil {
		return err
	}

	// B3-SPEC.md section 2: the deliverable ends in a machine-readable list
	// of OPTIONS naming a class the writing role's own job description
	// allows; a class outside that is refused whole -- nothing is written to
	// artifact_options, the deliverable is returned to the analyst with the
	// reason, and the refusal is journaled (option_refused, inside
	// ValidateAndSaveOptions itself so it happens whether or not this
	// function does anything else with the reason).
	//
	// "supervisor" is the acting link here: this is a mechanical policy
	// check running before any person has seen the deliverable, not a
	// person's own return, and task.return is the supervisor's class to
	// decide (roles.yaml). See crew.Return's own comment for why every
	// PERSON-driven caller elsewhere passes "owner" instead.
	if refused, reason, verr := crew.ValidateAndSaveOptions(
		db, int(artifactID), e.Analyst.Name, res.Text, b.rec); verr != nil {
		return verr
	} else if refused {
		if rerr := crew.Return(db, int(artifactID), reason, "supervisor"); rerr != nil {
			return rerr
		}
		fmt.Printf("  %-22s %-14s OPTIONS REFUSED: %s\n", trim(e.Task.Title, 22), e.Analyst.Name, reason)
	}

	// The charge lands on the task in cents, which is the ledger's unit. The
	// true amount accumulates in micro-dollars and the cents follow the
	// rounding of the TOTAL, not the sum of the roundings.
	//
	// Rounding each call up on its own recorded 0.56 for a run that cost
	// 0.2337, because a call costs a fraction of a cent and 44 fractions each
	// became a whole one. Rounding it to nothing would be the opposite mistake
	// and is how a bill grows out of a column of zeroes; rounding the total up
	// keeps that property at a cost of at most one cent per run.
	//
	// One statement, because four calls run at once: SQLite reads the row's old
	// values for every SET expression, so the delta and the new total are
	// computed from the same starting point even when two land together.
	// Only the truth here. The cents are worked out once, over the whole run,
	// by crew.SettleLiveSpend: rounding a fifth of a cent up per call recorded
	// 0.56 for a run that billed 0.2337, and rounding per task recorded the
	// same, because there is one call per task.
	if _, err := db.Exec(`UPDATE tasks
		SET live_micros = live_micros + ?, updated = datetime('now')
		WHERE id = ?`, res.ActualMicros, e.Task.ID); err != nil {
		return err
	}
	// And tell the estate. Last, and its failure is reported rather than
	// returned as this function's: the deliverable and the money are already
	// written, and a bus that cannot be appended to must not un-record work
	// that actually happened.
	if err := b.toolCall(e, res); err != nil {
		fmt.Fprintf(os.Stderr, "  the bus refused this call's event: %v\n", err)
	}
	return nil
}

// runBudget is the ceiling, held for the whole run.
//
// It RESERVES the worst case before a call and settles the difference after,
// which is what makes running several at once safe rather than hopeful. With
// a plain running total, four calls in flight could each pass a check against
// the same unspent balance and collectively walk past the ceiling; every one
// of them would have been individually correct.
//
// Reserved money is spent money until proven otherwise. That is the direction
// to be wrong in.
type runBudget struct {
	mu            sync.Mutex
	ceilingMicros int64
	reserved      int64 // in flight, at worst case
	spent         int64 // settled, at what it actually cost
}

// reserve takes the worst case out of the ceiling before the call is made.
func (r *runBudget) reserve(worst int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.spent+r.reserved+worst > r.ceilingMicros {
		return fmt.Errorf("the run's ceiling is %s, %s is spent and %s is in "+
			"flight, and this call could cost %s: refused before making it",
			usd(r.ceilingMicros), usd(r.spent), usd(r.reserved), usd(worst))
	}
	r.reserved += worst
	return nil
}

// settle puts back what the call did not use. actual is 0 when it failed,
// which returns the whole reservation: a call that produced nothing cost
// nothing.
func (r *runBudget) settle(worst, actual int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reserved -= worst
	r.spent += actual
}

func (r *runBudget) total() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.spent
}

// spend runs the live half: it checks the whole run against the ceiling
// before the first call, then executes task by task, stopping the moment
// anything refuses.
//
// Stopping rather than continuing is the point. A run that skips a refusal
// and carries on is a run whose ceiling is advisory, and the next call is
// exactly as likely to be the expensive one.
// refusal is a budget decision: it stops everything. A call that simply
// failed is not one.
//
// The first version returned both as a plain error and stopped the run on
// either, so one empty response from the router aborted a sprint that was two
// cents into a fifty cent ceiling. Stopping on a refusal is the point, because
// a ceiling somebody carries on past is advisory. Stopping on a flaky response
// is just losing the rest of the work.
//
// A failed call becomes what the console already has a word for: the task is
// blocked, with the reason, which the board renders and the agent card shows
// under "Where it stopped".
type refusal struct{ error }

func spend(db, roDB *sql.DB, ests []estimate, maxTok int, cap money.Cents, only int, b bus, gw gatewayConfig) error {
	run := &runBudget{ceilingMicros: int64(cap) * 10_000}

	todo := make([]estimate, 0, len(ests))
	for _, e := range ests {
		if only != 0 && e.Task.ID != only {
			continue
		}
		if e.Refused || !e.Priced {
			continue
		}
		todo = append(todo, e)
	}
	if len(todo) == 0 {
		return fmt.Errorf("nothing to run: every open task was refused, is on a " +
			"subscription, or does not match -only")
	}

	var worst int64
	for _, e := range todo {
		worst += e.WorstMicros
	}
	fmt.Printf("LIVE. %d task(s), worst case %s, ceiling %s.\n", len(todo), usd(worst), cap)
	if worst > run.ceilingMicros {
		return fmt.Errorf("the worst case is %s and the ceiling is %s: refused "+
			"before the first call", usd(worst), cap)
	}
	// Said once, not per call: an operator watching a run of forty tasks does
	// not need forty identical lines to learn that OpenRouter and Bedrock are
	// bypassing the gateway they just pointed this run at.
	if msg := directCallsNotice(gw.on(), todo); msg != "" {
		fmt.Print(msg)
	}
	fmt.Println()

	// A deadline PER CALL, not one for the whole run.
	//
	// This was a single ten-minute context shared by every task, so a run long
	// enough to matter guaranteed its own tail failed: forty-two calls at
	// twenty seconds each exhausted it, and the last fourteen were blocked
	// with "context deadline exceeded" having never been attempted. A bound on
	// one call is a timeout; a bound on all of them is an egg timer.
	// A few at a time. Sixty-three calls at twenty seconds each is twenty
	// minutes of somebody watching a terminal, and the wait is entirely the
	// model's: nothing here is CPU-bound.
	//
	// Four rather than as many as possible, because the far side rate-limits
	// and a run that trips that turns into a page of blocked tasks. Safe at
	// any width, because the ceiling is RESERVED before each call rather than
	// checked against a balance several calls are racing.
	const atOnce = 4

	var wg sync.WaitGroup
	var mu sync.Mutex
	var done, blocked int
	var stop bool

	sem := make(chan struct{}, atOnce)
	for _, e := range todo {
		mu.Lock()
		halted := stop
		mu.Unlock()
		if halted {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(e estimate) {
			defer wg.Done()
			defer func() { <-sem }()

			// Scaled by how many rounds this task's engine can loop
			// through: a task on the tool loop can make up to
			// maxToolRounds model calls in series, each able to take up
			// to the 90-second HTTP timeout the round functions set, so
			// the SAME "2 minutes was for one call" reasoning above needs
			// the same multiple this task's reservation already got.
			deadline := 2 * time.Minute * time.Duration(loopsFor(e.Engine))
			ctx, cancel := context.WithTimeout(context.Background(), deadline)
			err := execute(ctx, db, roDB, e, maxTok, run, b, gw)
			cancel()

			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				done++
				return
			}
			var r refusal
			if errors.As(err, &r) {
				// A refusal stops the run. Nothing new starts; what is already
				// in flight finishes, and every one of those has its worst
				// case reserved, so the ceiling holds.
				fmt.Printf("\nstopped at %q: %v\n", trim(e.Task.Title, 40), err)
				stop = true
				return
			}
			if _, e2 := db.Exec(
				`UPDATE tasks SET state='blocked', reason=?, updated=datetime('now') WHERE id=?`,
				"the engine did not answer: "+trim(err.Error(), 160), e.Task.ID); e2 != nil {
				fmt.Printf("  could not record the block: %v\n", e2)
			}
			blocked++
			fmt.Printf("  %-22s %-14s BLOCKED: %v\n", trim(e.Task.Title, 22), e.Analyst.Name, err)
		}(e)
	}
	wg.Wait()

	// The cents, once, over the whole run. Until this runs the tasks carry the
	// exact micro-dollars and no cents at all, which is the right way round: a
	// number that is not yet worked out shows as nothing, rather than showing
	// as a rounded-up guess that the console then presents as fact.
	booked, err := crew.SettleLiveSpend(db)
	if err != nil {
		return fmt.Errorf("settling what the run cost: %w", err)
	}

	fmt.Printf("\n%d of %d done, %d blocked. Spent %s of a %s ceiling.\n",
		done, len(todo), blocked, usd(run.total()), cap)
	fmt.Printf("The board now carries %s against these tasks, which is that "+
		"total rounded up to whole cents.\n", booked)
	fmt.Printf("Every deliverable is a DRAFT. Nothing is published until a person stamps it.\n")
	return nil
}

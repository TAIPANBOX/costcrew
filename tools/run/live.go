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
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/money"
	"sync"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/stack"
)

// callResult is what came back, and what it actually cost. A type alias
// (B6B-SPEC.md), not a new type: callResult IS deliver.Result, the same
// value under the old local name, so every existing call site and test in
// this package (bus.go's toolCall, loop.go's runToolLoop and both tool
// loops, execute() below) needed no change at all.
type callResult = deliver.Result

// gatewayConfig is this INVOCATION's gateway setup: the same for every call a
// run makes, built once from -gateway, -stack-host and -ceiling.
//
// An empty URL and an empty OpenAIURL mean the gateway is off, and every
// route calls its vendor directly exactly as it did before this file knew a
// gateway existed. With either set, a call goes through the gateway that
// fronts its engine's wire (URL: Anthropic, OpenAIURL: OpenAI, which is what
// OpenRouter speaks) or is refused; it is never sent direct (deliver.
// Gateway.RouteFor). Bedrock has no route and is refused when a gateway is on.
type gatewayConfig struct {
	URL        string      // normalized: http(s) only, no trailing slash; fronts the Anthropic wire
	OpenAIURL  string      // the same, for the gateway that fronts the OpenAI wire
	Host       string      // this installation's trust domain, for the agent id
	CeilingUSD money.Cents // the run's ceiling, i.e. -ceiling parsed

	// The local engine's setup, carried here because this is the one value
	// every layer from run() to execute() already receives. Neither field makes
	// on() true: ModelURL is the operator's own server a local call reaches
	// directly (-model-url), and MaxRunTokens is a ceiling on the tokens the
	// whole run may use (-max-run-tokens, 0 = none), the bound that stands in
	// for money when the local engine is priced at 0.
	ModelURL      string
	MaxRunTokens  int
	LocalParallel int
}

func (g gatewayConfig) on() bool { return g.URL != "" || g.OpenAIURL != "" }

// gatewayHeaders is what ONE call tells TokenFuse: who is asking, on whose
// run, and what it may spend. Built fresh per call because the budget is the
// tighter of the run's ceiling and THIS task's own guard, which differs task
// to task even though the run id and the agent id do not.
//
// A type alias (B6B-SPEC.md, "one gateway type"), not a new type: this IS
// deliver.Gateway under the old local name, so gatewayHeadersFor below and
// every existing gatewayHeaders{...} literal in this package's tests
// (bedrock_test.go's TestBedrockHasACaller included) needed no change.
type gatewayHeaders = deliver.Gateway

// gatewayHeadersFor builds one call's headers from the run's shared config
// and that call's own task guard and analyst name. cfg.on() must be checked
// by the caller; this only formats.
//
// It also names whose spend this is (costcrew#73): the analyst's owner as a
// user:// root, then the analyst's agent, deliver.OnBehalfOfChain. An analyst
// with no owner is an error naming it, not a call with an empty chain.
func gatewayHeadersFor(cfg gatewayConfig, runID string, analyst crew.Analyst, taskGuard money.Cents) (gatewayHeaders, error) {
	chain, err := deliver.AnalystOnBehalfOf(cfg.Host, analyst)
	if err != nil {
		return gatewayHeaders{}, err
	}
	return gatewayHeaders{
		URL:        cfg.URL,
		OpenAIURL:  cfg.OpenAIURL,
		RunID:      runID,
		AgentID:    stack.AgentURI(cfg.Host, analyst.Name),
		BudgetUSD:  gatewayBudgetUSD(cfg.CeilingUSD, taskGuard),
		OnBehalfOf: chain,
	}, nil
}

// gatewayBudgetUSD is the tighter of the run's ceiling and the task's own
// guard. Moved to internal/deliver (B6B-SPEC.md, both binaries need it);
// this keeps the old unexported name as a one-line wrapper so every call
// site and test in this package (gatewayHeadersFor below,
// TestGatewayBudgetUSDIsTheTighterOfCeilingAndTaskGuard) needed no change.
func gatewayBudgetUSD(runCeiling, taskGuard money.Cents) string {
	return deliver.GatewayBudgetUSD(runCeiling, taskGuard)
}

// gatewayEnvDefault backs -gateway's default with COSTCREW_GATEWAY. Moved to
// internal/deliver (both binaries fall back to the same variable); this
// wrapper keeps main.go's own call site and TestGatewayEnvDefaultReadsCOSTCREW_GATEWAY
// unchanged. Reading the environment through internal/deliver rather than
// here is what keeps main.go itself provably unable to
// (TestThisBinaryCannotSpend reads main.go's own source for "os.Getenv").
func gatewayEnvDefault() string {
	return deliver.GatewayEnvDefault()
}

// normalizeGateway validates -gateway and strips a trailing slash. Moved to
// internal/deliver (B6B-SPEC.md: tools/bench needs the identical validation
// "before the store opens"); this wrapper keeps main.go's call site and
// TestNormalizeGatewayRefusesANonHTTPURL and its two neighbours unchanged.
func normalizeGateway(raw string) (string, error) {
	return deliver.NormalizeGateway(raw)
}

// normalizeGatewayOpenAI is normalizeGateway for -gateway-openai.
func normalizeGatewayOpenAI(raw string) (string, error) {
	return deliver.NormalizeGatewayOpenAI(raw)
}

// gatewayOpenAIEnvDefault backs -gateway-openai's default with
// COSTCREW_GATEWAY_OPENAI, read through internal/deliver for the reason
// gatewayEnvDefault is.
func gatewayOpenAIEnvDefault() string {
	return deliver.GatewayOpenAIEnvDefault()
}

// noRouteRefusal is the preflight that replaced directCallsNotice. That
// function said, in one line, that calls on openrouter and bedrock "go
// direct" while -gateway was set, and then made them, so a run pointed at a
// metering gateway spent outside it with a note beside the bill. Now a run
// with any gateway configured refuses, before the first call, when any task
// in it is on an engine no configured gateway fronts, and names the tasks'
// engines and the setting that would give them a route. "" means every task
// has one (or no gateway is on, and every call is direct, as before).
func noRouteRefusal(gw gatewayConfig, todo []estimate) error {
	if !gw.on() {
		return nil
	}
	probe := deliver.Gateway{URL: gw.URL, OpenAIURL: gw.OpenAIURL}
	counts := map[string]int{}
	var order []string
	var first error
	for _, e := range todo {
		if _, err := probe.RouteFor(e.Engine); err != nil {
			if counts[e.Engine] == 0 {
				order = append(order, e.Engine)
			}
			counts[e.Engine]++
			if first == nil {
				first = err
			}
		}
	}
	if first == nil {
		return nil
	}
	var parts []string
	for _, eng := range order {
		parts = append(parts, fmt.Sprintf("%d on %s", counts[eng], eng))
	}
	return fmt.Errorf("a gateway is configured and %s have no gateway route, so the run is "+
		"refused before the first call rather than sending them direct: %w; "+
		"narrow the run with -engine to the engines the gateway fronts",
		strings.Join(parts, ", "), first)
}

// discardedClause is the summary line's words for answers thrown away because
// a person blocked the task mid-call: empty when there were none, so a run
// that discarded nothing reads exactly as it always did.
func discardedClause(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(", %d discarded (blocked by a person while the call was in flight; the call was still paid for)", n)
}

// refuseOwnerless is the pricing-time half of costcrew#73. With a gateway
// configured every call names the analyst's owner to the control plane, and an
// analyst with no owner has none to name, so its task is refused here, in the
// dry run and the live run alike, with a verdict that names the analyst,
// rather than priced and then sent with an empty chain. Without a gateway
// nothing is sent to anyone and nothing changes. execute() holds the same
// line again for a caller that never went through this.
func refuseOwnerless(ests []estimate, gw gatewayConfig) {
	if !gw.on() {
		return
	}
	for i := range ests {
		e := &ests[i]
		if e.Refused || e.Analyst.Name == "" {
			continue
		}
		if _, err := deliver.AnalystOnBehalfOf(gw.Host, e.Analyst); err != nil {
			e.Verdict, e.Refused = err.Error(), true
		}
	}
}

// parseGatewayRefusal reads TokenFuse's 402 body into the sentence a person
// reads. Moved to internal/deliver (loop.go's own anthropicRound, a
// separate pre-existing implementation, reads a 402 on its own wire and has
// always called this by its old unexported name); this wrapper keeps that
// call site, and every other in this package, unchanged.
func parseGatewayRefusal(raw []byte) error {
	return deliver.ParseGatewayRefusal(raw)
}

// call routes to the engine the analyst was HIRED with.
//
// Which engine an analyst runs on is a decision recorded at hire time and
// visible on its card, and this is where that decision finally does
// something. A router that ignored it would make the field decoration.
//
// Moved to internal/deliver as Call (B6B-SPEC.md): this is now a one-line
// wrapper, the same move packet() and prompt() made in B7. The one thing it
// still does locally is translate a deliver.GatewayRefusal back into this
// package's own refusal{} type, because internal/deliver has no notion of a
// "run" to stop and spend()'s loop below reads specifically for refusal to
// decide that. Production reaches this only for bedrock or an engine outside
// the tool loop; loop.go's own anthropicToolLoop/openRouterToolLoop call
// deliver.Call's own callAnthropic wire independently, unaffected by this
// wrapper (see call.go's package comment in internal/deliver for why).
func call(ctx context.Context, engine, model, prompt string, maxTok int, gw gatewayHeaders) (callResult, error) {
	res, err := deliver.Call(ctx, engine, model, prompt, maxTok, gw)
	if err == nil {
		return res, nil
	}
	var gr deliver.GatewayRefusal
	if errors.As(err, &gr) {
		return res, refusal{gr}
	}
	return res, err
}

// prompt is production's own call into internal/deliver.Prompt: see that
// function for everything this used to say about persona, mission, the
// packet, the date, the format note and the options block instructions.
//
// Moved there (B7-SPEC.md section 3's factoring) so tools/bench can send the
// identical prompt tools/run does rather than a second one that only looks
// like it: "so the bench measures what production runs, not a second
// prompt" (B7-SPEC.md section 2). This wrapper keeps the old unexported name
// so every call site and test in this package needed no change.
func prompt(t crew.Task, a crew.Analyst, today, packetText string) string {
	return deliver.Prompt(t, a, today, packetText)
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

	// The headers for THIS call come first, before anything is reserved: an
	// analyst with no owner is refused here, naming it, with nothing taken
	// from the ceiling and no request made (costcrew#73).
	var gh gatewayHeaders
	if gw.on() {
		var herr error
		gh, herr = gatewayHeadersFor(gw, b.run, e.Analyst, e.Task.Budget)
		if herr != nil {
			return fmt.Errorf("refused before the call: %w", herr)
		}
	}
	// The operator's own server, for the local engine's direct route. Set
	// whether or not a gateway is on: with a gateway the call never reads it
	// (Gateway.RouteFor answers first), without one it is the whole address.
	gh.ModelURL = gw.ModelURL

	// Every round of the tool loop is its own model call (B2-SPEC.md
	// section 3.4), so the reservation covers the worst case
	// loopsFor(e.Engine) times over, before the first round rather than
	// growing it round by round: TestTheLoopStopsAtMaxRounds is what proves
	// six rounds fit under it. An engine outside the loop (Bedrock, or
	// anything unknown) still reserves exactly one call's worth, as before
	// this file knew a loop existed.
	//
	// reservedWorstCase(e) (main.go), not a second e.WorstMicros*loops here:
	// this call site and report()'s own summary line/table are the two
	// PRICE-DISPLAY-SPEC.md found had drifted apart -- one multiplying, one
	// not -- so they now share the one function rather than each carrying
	// its own copy of "* loopsFor(e.Engine)".
	reserveMicros := reservedWorstCase(e)
	if err := run.reserve(reserveMicros); err != nil {
		return refusal{err}
	}
	// And the same worst case in tokens, against -max-run-tokens when one is
	// set. Money first, then tokens, and the money comes back if the tokens
	// refuse: a refused call reserves nothing.
	reserveTokens := reservedWorstTokens(e, maxTok)
	if err := run.reserveTokens(reserveTokens); err != nil {
		run.settle(reserveMicros, 0)
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
	// three x-fuse headers. (Built above, before the reservation.)
	sent := prompt(e.Task, e.Analyst, time.Now().Format("2006-01-02"), e.Packet)
	res, err := runToolLoop(ctx, db, roDB, e, sent, maxTok, gh, e.Analyst, b)
	// The charge is the gateway's settlement when there is one, the runner's
	// own price otherwise (deliver.Charge, invariant 51), on BOTH paths: a
	// task that failed after a billed round still cost that round. It is
	// booked against the ceiling as-is even above the reservation, so the
	// next reserve() is checked against what was actually spent.
	charge := res.ChargeMicros()
	run.settle(reserveMicros, charge)
	// Tokens settle at what the rounds counted, above the reservation if it
	// came to that, so the next task is checked against what was really used.
	run.settleTokens(reserveTokens, int64(res.InTokens)+int64(res.OutTokens))
	run.noteSettlement(res.Settlement)
	// And on the task itself, on every path, stopped included: the tokens
	// were used whether or not a draft follows (invariant 93).
	if used := int64(res.InTokens) + int64(res.OutTokens); used > 0 {
		if e2 := recordTokens(db, e.Task.ID, used, run.tokenCeiling); e2 != nil {
			fmt.Fprintf(os.Stderr, "  could not record the tokens task %d used: %v\n", e.Task.ID, e2)
		}
	}
	if err != nil {
		// A task that stopped is not a task that cost nothing (costcrew#82):
		// the run's ceiling above already counts what the rounds that were
		// billed cost, and the board must carry the same figure. saveDraft,
		// which books a finished task's charge, is never reached from here,
		// so this is the only place the unfinished one is recorded.
		if charge > 0 {
			if e2 := recordCharge(db, e.Task.ID, charge); e2 != nil {
				fmt.Fprintf(os.Stderr, "  could not record the charge of the stopped task %d: %v\n", e.Task.ID, e2)
			}
		}
		return err
	}

	if err := saveDraft(db, e, res, b); err != nil {
		if errors.Is(err, errTaskBlockedMeanwhile) {
			// A person blocked the task while the call was in flight. The call
			// was made and the gateway billed it, so the charge is booked
			// exactly as a stopped task's is (invariant 55); the answer is
			// what is thrown away, because the block was an order and a draft
			// written after it would be the runner working around it
			// (invariant 57). saveDraft wrote nothing, so this is the only
			// place the money lands.
			if charge > 0 {
				if e2 := recordCharge(db, e.Task.ID, charge); e2 != nil {
					fmt.Fprintf(os.Stderr, "  could not record the charge of the discarded answer for task %d: %v\n", e.Task.ID, e2)
				}
			}
			if e2 := b.toolCall(e, res); e2 != nil {
				fmt.Fprintf(os.Stderr, "  the bus refused this call's event: %v\n", e2)
			}
			fmt.Printf("  %-22s %-14s DISCARDED: the answer came back after a person blocked the task, "+
				"so no draft was saved; the call cost %s %s\n",
				trim(e.Task.Title, 22), e.Analyst.Name, usd(charge), chargeBasis(res.Settlement))
			return answerDiscarded{taskID: e.Task.ID}
		}
		return err
	}

	fmt.Printf("  %-22s %-14s %-10s in %5d out %5d  cost %s %s  (worst %s)\n",
		trim(e.Task.Title, 22), e.Analyst.Name, trim(e.Engine, 10),
		res.InTokens, res.OutTokens, usd(charge), chargeBasisFor(e.Engine, res.Settlement), usd(e.WorstMicros))
	return nil
}

// chargeBasis is the per-task console line's own word on where the figure
// beside it came from. Exactly two texts, and one more clause only when the
// gateway priced the model at its fallback rate (tokenfuse#305): a person
// reading "0.0581" next to a worst case of "0.0116" is owed the reason.
func chargeBasis(s deliver.Settlement) string {
	if !s.Settled {
		return "priced by the runner: no settlement header"
	}
	if s.PriceBasis == "fallback" {
		return "settled by the gateway at its fallback price (x-fuse-price: fallback)"
	}
	return "settled by the gateway"
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
	// One statement that refuses a task a person has blocked: the insert and
	// the look at the task's state cannot be separated by a click. A block
	// that lands before this statement writes nothing; one that lands after
	// it finds the draft already there, which is a person blocking a task
	// that has a draft, an ordinary thing to do.
	ins, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created, source)
		SELECT ?,?,?,?, 'draft', datetime('now'), 'live'
		WHERE NOT EXISTS (SELECT 1 FROM tasks WHERE id = ? AND state = 'blocked')`,
		e.Task.ID, e.Analyst.Name, trim(title, 120), res.Text, e.Task.ID)
	if err != nil {
		return err
	}
	if n, err := ins.RowsAffected(); err == nil && n == 0 {
		return errTaskBlockedMeanwhile
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
	//
	// The figure is the gateway's settlement when the call was settled, the
	// runner's own price otherwise: deliver.Charge, invariant 51.
	if err := recordCharge(db, e.Task.ID, res.ChargeMicros()); err != nil {
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

// recordCharge adds one call's (or one task's) charge, in micro-dollars, to
// the task's live_micros: the one statement both a finished task (saveDraft)
// and a stopped one (execute's error path) book through, so the two cannot
// come to record money differently. One statement, because four tasks run at
// once and SQLite reads the row's old value for the SET expression.
func recordCharge(db *sql.DB, taskID int, micros int64) error {
	_, err := db.Exec(`UPDATE tasks
		SET live_micros = live_micros + ?, updated = datetime('now')
		WHERE id = ?`, micros, taskID)
	return err
}

// recordTokens adds the tokens one task's calls used to tasks.live_tokens and
// notes the run's token ceiling beside them (invariant 93). One statement, for
// the same reason recordCharge is one.
//
// A store the console has not started on since the columns were added is
// given them here, once, and only by a run that has tokens to record: a dry
// run never reaches this, so it still changes nothing.
func recordTokens(db *sql.DB, taskID int, tokens, ceiling int64) error {
	write := func() error {
		_, err := db.Exec(`UPDATE tasks
			SET live_tokens = live_tokens + ?, live_token_ceiling = ?, updated = datetime('now')
			WHERE id = ?`, tokens, ceiling, taskID)
		return err
	}
	err := write()
	if err != nil && strings.Contains(err.Error(), "no such column") {
		if err := crew.EnsureLiveSpendLedger(db); err != nil {
			return err
		}
		err = write()
	}
	return err
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

	// The same ceiling in TOKENS (-max-run-tokens), for the case money cannot
	// bound: the local engine at a price of 0 reserves nothing in dollars.
	// 0 means no token ceiling and every method below is a no-op, so a budget
	// built without one behaves exactly as it did before this field existed.
	tokenCeiling, tokensReserved, tokensSpent int64

	// What the gateway said, over the whole run (invariant 51): how many
	// tasks reached settle, how many of those the gateway settled, and the
	// largest x-fuse-spent-usd seen, which is the gateway's own view of the
	// run's total because every task of one invocation shares one run id.
	charged, settled  int
	gatewaySpent      int64
	gatewaySpentKnown bool
}

// noteSettlement records one task's settlement for the summary line. Called
// once per execute() that reached settle, on the success and the error path
// alike, right after settle.
func (r *runBudget) noteSettlement(s deliver.Settlement) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.charged++
	if s.Settled {
		r.settled++
	}
	if s.RunSpentKnown {
		r.gatewaySpentKnown = true
		if s.RunSpentMicros > r.gatewaySpent {
			r.gatewaySpent = s.RunSpentMicros
		}
	}
}

func (r *runBudget) settlement() (settled, charged int, gatewaySpent int64, known bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.settled, r.charged, r.gatewaySpent, r.gatewaySpentKnown
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

// reserveTokens is reserve in tokens: it takes the worst case out of the token
// ceiling before the call is made, or refuses. A no-op with no ceiling.
func (r *runBudget) reserveTokens(worst int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tokenCeiling <= 0 {
		return nil
	}
	if r.tokensSpent+r.tokensReserved+worst > r.tokenCeiling {
		return fmt.Errorf("the run's token ceiling is %d, %d are used and %d are in flight, and "+
			"this call could use %d: refused before making it",
			r.tokenCeiling, r.tokensSpent, r.tokensReserved, worst)
	}
	r.tokensReserved += worst
	return nil
}

// settleTokens puts back what the call did not use and books what it did.
func (r *runBudget) settleTokens(worst, actual int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tokenCeiling <= 0 {
		return
	}
	r.tokensReserved -= worst
	r.tokensSpent += actual
}

func (r *runBudget) tokensUsed() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tokensSpent
}

func (r *runBudget) total() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.spent
}

// vendorParallel is how many tasks on the vendor engines (anthropic,
// openrouter, bedrock) a live run keeps in flight at once.
const vendorParallel = 4

// localParallelMax is the most -local-parallel accepts: a typo of 400 would
// open 400 connections to one server, and no self-hosted server this console
// has been run against has that many slots.
const localParallelMax = 64

// localParallel is how many local-engine tasks a live run keeps in flight:
// -local-parallel, and 1 when it was not set. One, not the vendor width,
// because a self-hosted server that answers one request at a time makes every
// other request wait, and that wait counts against the round's timeout.
func localParallel(gw gatewayConfig) int {
	if gw.LocalParallel < 1 {
		return 1
	}
	return gw.LocalParallel
}

// splitByEngine splits the run into the vendor queue and the local queue,
// each keeping the run's own order.
func splitByEngine(todo []estimate) (vendor, local []estimate) {
	for _, e := range todo {
		if e.Engine == engines.LocalID {
			local = append(local, e)
		} else {
			vendor = append(vendor, e)
		}
	}
	return vendor, local
}

// runQueue starts width workers that take q's tasks in order, one each until
// q is exhausted or the run has halted. A worker checks halted after taking a
// task as well as before, so a task taken while a refusal was being recorded
// is not started.
func runQueue(wg *sync.WaitGroup, q []estimate, width int, halted func() bool, work func(estimate)) {
	if len(q) == 0 {
		return
	}
	jobs := make(chan estimate)
	for i := 0; i < width; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range jobs {
				if halted() {
					continue
				}
				work(e)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, e := range q {
			if halted() {
				return
			}
			jobs <- e
		}
	}()
}

// nothingToRun is the refusal of a run with no task left to work. With -only
// it names that one task's own reason: the run priced it and knows exactly
// why it cannot run (its Verdict), and a sentence listing every reason any
// task might have had made an operator re-run the dry run to find out which
// (measured 2026-10-08: two tasks refused this way, and the run did not say
// which check refused them).
func nothingToRun(ests []estimate, only int) error {
	if only != 0 {
		for _, e := range ests {
			if e.Task.ID != only {
				continue
			}
			if e.Verdict != "" {
				return fmt.Errorf("nothing to run: task %d was refused: %s", only, e.Verdict)
			}
			return fmt.Errorf("nothing to run: task %d was refused", only)
		}
		return fmt.Errorf("nothing to run: task %d is not among the open tasks this run priced "+
			"(it may be done, blocked, in another -sprint, or on another -engine)", only)
	}
	return fmt.Errorf("nothing to run: every open task was refused or is on a subscription; " +
		"run without -live to see each task's reason")
}

// taskDeadline is one task's own deadline: two minutes per round its engine
// can loop through. A var only so a test can shorten it.
var taskDeadline = func(engine string) time.Duration {
	return 2 * time.Minute * time.Duration(loopsFor(engine))
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

// errTaskBlockedMeanwhile is saveDraft's answer for a task a person blocked
// while its call was in flight: nothing was written.
var errTaskBlockedMeanwhile = errors.New("the task was blocked while its call was in flight")

// answerDiscarded is what execute returns for that task. It is neither a
// refusal (the run goes on) nor a failure (the task is already blocked, by a
// person, with a reason that spend() must not overwrite with its own), so
// spend() counts it on its own.
type answerDiscarded struct{ taskID int }

func (a answerDiscarded) Error() string {
	return fmt.Sprintf("task %d was blocked while its call was in flight; the answer was discarded", a.taskID)
}

func spend(db, roDB *sql.DB, ests []estimate, maxTok int, cap money.Cents, only int, b bus, gw gatewayConfig) error {
	run := &runBudget{ceilingMicros: int64(cap) * 10_000, tokenCeiling: int64(gw.MaxRunTokens)}

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
		return nothingToRun(ests, only)
	}

	// reservedWorstCase(e), not e.WorstMicros: this IS the "worst case of the
	// whole run... checked against that ceiling before the first call" this
	// file's own package comment promises (point 3). Before this fix it
	// summed one call's own bound per task, so a run whose looped tasks
	// could never actually fit could still pass this preflight, launch its
	// goroutines, and only fail once execute()'s own (already-multiplied)
	// reserve() refused each one individually -- which spend()'s own
	// refusal handling below prints and swallows into a nil return, so the
	// caller never saw this preflight had let anything through it should
	// not have. PRICE-DISPLAY-SPEC.md, 2026-09-03; the same gap report()
	// and price()'s Verdict had, found in this file rather than named there
	// by name.
	var worst int64
	for _, e := range todo {
		worst += reservedWorstCase(e)
	}
	if err := noRouteRefusal(gw, todo); err != nil {
		return err
	}
	if err := localPreflight(gw, todo, maxTok); err != nil {
		return err
	}
	fmt.Printf("LIVE. %d task(s), worst case %s, ceiling %s.\n", len(todo), usd(worst), cap)
	if worst > run.ceilingMicros {
		return fmt.Errorf("the worst case is %s and the ceiling is %s: refused "+
			"before the first call", usd(worst), cap)
	}
	// Last of the refusals, because it is the only one that touches the
	// network: a run the numbers already refuse must not knock on a server.
	if err := localReachRefusal(gw, todo); err != nil {
		return err
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
	// Four on the vendor engines rather than as many as possible, because the
	// far side rate-limits and a run that trips that turns into a page of
	// blocked tasks. Safe at any width, because the ceiling is RESERVED before
	// each call rather than checked against a balance several calls are racing.
	//
	// ONE on the local engine unless the operator says otherwise
	// (-local-parallel). A server on the organisation's own hardware usually
	// answers one request at a time and QUEUES the rest, and the queue counts
	// against each round's client timeout: measured 2026-10-08, four local
	// tasks in flight against Ollama on an 8-vCPU VM left 17 of 19 blocked
	// with "no answer in time", and the same 19 one at a time all finished.
	// The vendor width was right for a vendor and wrong for a single slot.
	//
	// Two queues, each in the run's own order, each with its own workers, so
	// a vendor task never waits for a local slot it does not need. A task's
	// deadline starts when a worker takes it, so waiting in this runner's
	// queue is never counted against it.
	vendorQ, localQ := splitByEngine(todo)
	width := localParallel(gw)
	if len(localQ) > 0 {
		fmt.Printf("Local engine: %d task(s), %d at a time (-local-parallel %d).\n", len(localQ), width, width)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var done, blocked, discarded int
	var stop bool

	halted := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return stop
	}
	work := func(e estimate) {
		// Scaled by how many rounds this task's engine can loop
		// through: a task on the tool loop can make up to
		// maxToolRounds model calls in series, each able to take up
		// to the 90-second HTTP timeout the round functions set, so
		// the SAME "2 minutes was for one call" reasoning above needs
		// the same multiple this task's reservation already got.
		ctx, cancel := context.WithTimeout(context.Background(), taskDeadline(e.Engine))
		err := execute(ctx, db, roDB, e, maxTok, run, b, gw)
		cancel()

		mu.Lock()
		defer mu.Unlock()
		if err == nil {
			done++
			return
		}
		var d answerDiscarded
		if errors.As(err, &d) {
			// Already blocked by a person: leave their reason alone.
			discarded++
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
		// A person may have blocked the task while this call was in
		// flight: their reason stands, and the runner's own is written
		// only on a task nobody blocked (invariant 66).
		if _, e2 := db.Exec(
			`UPDATE tasks SET state='blocked', reason=?, updated=datetime('now') WHERE id=? AND state <> 'blocked'`,
			"the engine did not answer: "+trim(err.Error(), 160), e.Task.ID); e2 != nil {
			fmt.Printf("  could not record the block: %v\n", e2)
		}
		blocked++
		fmt.Printf("  %-22s %-14s BLOCKED: %v\n", trim(e.Task.Title, 22), e.Analyst.Name, err)
	}
	runQueue(&wg, vendorQ, vendorParallel, halted, work)
	runQueue(&wg, localQ, width, halted, work)
	wg.Wait()

	// The cents, once, over the whole run. Until this runs the tasks carry the
	// exact micro-dollars and no cents at all, which is the right way round: a
	// number that is not yet worked out shows as nothing, rather than showing
	// as a rounded-up guess that the console then presents as fact.
	booked, err := crew.SettleLiveSpend(db)
	if err != nil {
		return fmt.Errorf("settling what the run cost: %w", err)
	}

	settled, charged, gwSpent, gwKnown := run.settlement()
	if gwKnown {
		fmt.Printf("\n%d of %d done, %d blocked%s. Spent %s of a %s ceiling: %d of %d task(s) settled by the "+
			"gateway, whose own run total is %s.",
			done, len(todo), blocked, discardedClause(discarded), usd(run.total()), cap, settled, charged, usd(gwSpent))
		// The gateway's own ledger is the bill. When it is higher than what
		// this run booked, a call it settled never reached this runner (a
		// response lost in transit, a task that failed before a header could
		// be read): say so beside the figure rather than let the smaller
		// number stand as the whole of what was spent (costcrew#82).
		if gwSpent > run.total() {
			fmt.Printf(" The gateway's total is %s more than this run booked: a call it settled "+
				"never reached this runner.", usd(gwSpent-run.total()))
		}
		fmt.Println()
	} else {
		fmt.Printf("\n%d of %d done, %d blocked%s. Spent %s of a %s ceiling: %d of %d task(s) settled by the "+
			"gateway, the rest priced by the runner (no settlement header).\n",
			done, len(todo), blocked, discardedClause(discarded), usd(run.total()), cap, settled, charged)
	}
	if gw.MaxRunTokens > 0 {
		fmt.Printf("Tokens used: %d of a %d ceiling.\n", run.tokensUsed(), gw.MaxRunTokens)
	}
	fmt.Printf("The board now carries %s against these tasks, which is that "+
		"total rounded up to whole cents.\n", booked)
	fmt.Printf("Every deliverable is a DRAFT. Nothing is published until a person stamps it.\n")
	return nil
}

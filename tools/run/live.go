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

	"github.com/TAIPANBOX/costcrew/internal/money"
	"sync"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
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
//
// A type alias (B6B-SPEC.md, "one gateway type"), not a new type: this IS
// deliver.Gateway under the old local name, so gatewayHeadersFor below and
// every existing gatewayHeaders{...} literal in this package's tests
// (bedrock_test.go's TestBedrockHasACaller included) needed no change.
type gatewayHeaders = deliver.Gateway

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

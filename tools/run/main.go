// Command run says what it would cost to let the crew actually write, and
// does not let it.
//
// THIS BINARY CANNOT SPEND. It holds no HTTP client, runs no command and
// reads no API key. There is no flag that makes it call anything, because the
// safest first version of a thing that spends money is one that cannot.
// docs/live-agents.md describes the executor this is the first half of.
//
// What it does: takes the open board, prices the WORST case for every task,
// and says which ones a guard would refuse. Worst case, not expected: a
// model's output length is not known before the call, so the only honest bound
// is max-tokens at the output price. An estimate built on the expected length
// is the one that is wrong on exactly the call that runs long.
package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/typryx"
)

func main() {
	dir := flag.String("data", "./local", "the console's data directory")
	ceiling := flag.String("ceiling", "", "refuse the whole run above this, in USD, e.g. 25.00")
	maxTok := flag.Int("max-tokens", 2000, "the output cap every call would be made with")
	sprint := flag.Int("sprint", 0, "only this sprint id; 0 means every open task")
	showPrices := flag.Bool("prices", false, "print the price table and exit")
	live := flag.Bool("live", false, "actually make the calls; needs -ceiling and spends real money")
	only := flag.Int("only", 0, "with -live, run this one task id and stop")
	engine := flag.String("engine", "", "only tasks whose analyst was hired with this engine")
	// B3-SPEC.md section 4: the supervisor's pass, deterministic, no model
	// call. Needs -sprint: which sprint's POSTED deliverables to review, the
	// same requirement -live's -ceiling carries for the same reason -- a
	// pass that ran over every sprint on the board because nobody named one
	// is a pass nobody chose.
	supervise := flag.Bool("supervise", false,
		"run the supervisor's deterministic pass over -sprint's posted deliverables; needs -sprint")
	// B5-SPEC.md section 3: only cadence-due work, under the ceiling, and
	// only while a person has switched the console's cadence.enabled on.
	// Needs -ceiling for the same reason -live's does: a run that can spend
	// has to be bounded by a figure somebody typed, and here it is the
	// smaller of that figure and the console's own cadence.ceiling_cents.
	due := flag.Bool("due", false,
		"run only cadence-due work, under the console's cadence switch and ceiling; needs -ceiling")
	// The estate integration, off unless pointed somewhere, exactly as the
	// console's own is. The file NAME is the integration: genaryx keys each
	// source's read offset off the stem, so this has to be costcrew.ndjson and
	// nothing else, and it is the same file the console appends to.
	events := flag.String("stack-events", "", "append agent-events to this NDJSON file; empty means off")
	host := flag.String("stack-host", "", "the agent:// authority for this installation; must match the console's")
	// The TokenFuse gateways, off unless pointed somewhere. Each falls back
	// to its own environment variable so an installation can set it once
	// rather than on every invocation; an explicit -gateway "" still turns
	// it off even with the environment variable set.
	//
	// Two flags because a TokenFuse process forwards ONE upstream wire shape,
	// chosen by its own TOKENFUSE_WIRE (tokenfuse docs/26): -gateway fronts
	// the Anthropic wire, -gateway-openai the OpenAI wire OpenRouter speaks.
	// With either set, a call goes through the gateway that fronts its engine
	// or is refused; it is never sent direct. Bedrock has no gateway route.
	gateway := flag.String("gateway", gatewayEnvDefault(),
		"TokenFuse gateway for the Anthropic route, e.g. http://127.0.0.1:4177; "+
			"empty calls api.anthropic.com directly. Falls back to COSTCREW_GATEWAY.")
	gatewayOpenAI := flag.String("gateway-openai", gatewayOpenAIEnvDefault(),
		"TokenFuse gateway for the OpenRouter route (a gateway whose TOKENFUSE_WIRE is openai), "+
			"e.g. http://127.0.0.1:4178; empty calls openrouter.ai directly unless -gateway is "+
			"set, in which case openrouter calls are refused. Falls back to COSTCREW_GATEWAY_OPENAI.")
	// The local engine: a model the organisation hosts itself (Ollama, vLLM, LM
	// Studio, llama.cpp), reached over the OpenAI wire. Nothing here names a
	// vendor. -model-url and -model-name fall back to their environment
	// variables the way the gateway flags do; the price and the token ceiling
	// are flags only, because a price that arrives from the environment is a
	// price nobody typed on this command line.
	modelURL := flag.String("model-url", modelURLEnvDefault(),
		"base URL of your own OpenAI-compatible model server for the local engine, "+
			"e.g. http://127.0.0.1:11434/v1; no credentials in the URL. "+
			"Falls back to COSTCREW_MODEL_URL. With -gateway-openai set, the call goes through "+
			"that gateway instead.")
	modelName := flag.String("model-name", modelNameEnvDefault(),
		"the model your server serves, for the local engine, e.g. llama3.1:8b. "+
			"Falls back to COSTCREW_MODEL_NAME. An optional bearer token goes in COSTCREW_MODEL_KEY.")
	localIn := flag.Float64("local-price-in", 0,
		"what your own hardware costs per million input tokens on the local engine, in USD (default 0)")
	localOut := flag.Float64("local-price-out", 0,
		"what your own hardware costs per million output tokens on the local engine, in USD (default 0)")
	maxRunTokens := flag.Int("max-run-tokens", 0,
		"ceiling on the tokens a live run may use, counted over every task and reserved before each "+
			"call; required when the local engine is priced at 0, because money cannot bound it then")
	// How many local-engine tasks run at once. One by default, because a
	// self-hosted server usually answers one request at a time and queues the
	// rest, and the queue counts against each round's timeout (invariant 84).
	// The vendor engines keep their own width, four.
	localParallel := flag.Int("local-parallel", 1,
		"how many local-engine tasks a live run keeps in flight at once (default 1); raise it only to "+
			"what your server answers at once, e.g. Ollama's OLLAMA_NUM_PARALLEL. Vendor engines run four at once")
	// Invariant 76: before -live works a task on an anomaly, typryx is asked
	// for a typed hint, which the analyst then reads in its packet. Off
	// unless pointed somewhere; falls back to COSTCREW_TYPRYX_URL, and the
	// key is read from COSTCREW_TYPRYX_KEY by internal/typryx, never here.
	typryxURL := flag.String("typryx-url", typryx.URLEnvDefault(),
		"with -live, typryx to ask for a typed hint about each anomaly task before it is worked, "+
			"e.g. http://127.0.0.1:4320; empty asks nothing. Falls back to COSTCREW_TYPRYX_URL.")
	// How much of this installation's billing data a model may be sent
	// (invariant 70). Read from the environment by internal/deliver, like the
	// gateway above, so this file stays the one that provably cannot spend.
	promptData := flag.String("prompt-data", deliver.PromptDataEnvDefault(),
		"how much billing data a model may be sent: full (as it always was), masked (every "+
			"name replaced by a stable token, free text withheld, no SQL tools) or aggregates "+
			"(totals only). Anything else refuses to start. Falls back to COSTCREW_PROMPT_DATA.")
	flag.Parse()

	if *showPrices {
		fmt.Print("Prices this estimate would use, per million tokens:\n\n")
		fmt.Print(engines.PriceTable())
		fmt.Print("\nEvery line says where it came from. The ones marked @claude are\n" +
			"unverified against the vendor and must be re-checked before a live call.\n")
		return
	}

	// Before the store is opened, the bus is opened or anything is priced: a
	// misspelt setting that fell back to sending everything is the one mistake
	// this flag exists to prevent.
	if _, err := deliver.ConfigurePromptData(*promptData, *dir); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}

	local := localOptions{ModelURL: *modelURL, ModelName: *modelName,
		PriceIn: *localIn, PriceOut: *localOut, MaxRunTokens: *maxRunTokens, Parallel: *localParallel}
	if err := run(*dir, *ceiling, *maxTok, *sprint, *live, *supervise, *due, *only, *engine, *events, *host, *gateway, *gatewayOpenAI, local, *typryxURL); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(dueExitCode(err))
	}
}

// dueExitCode is main()'s exit-code mapping, pulled out so it is directly
// testable without spawning the binary: errCadenceOff is exit 2, distinct
// from an ordinary failure (1), so a cron wrapper or an operator can tell
// "nothing to do, by design" (the console's switch is off) from "broke".
// B5-SPEC.md section 3 point 1: "exit 2, nothing else touched".
func dueExitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, errCadenceOff) {
		return 2
	}
	return 1
}

// estimate is one task, priced.
type estimate struct {
	Task    crew.Task
	Analyst crew.Analyst
	Engine  string
	Model   string
	Price   engines.Price
	Priced  bool

	PromptTokens int
	// CatalogueTokens is the tool catalogue's own bytes, sent as `tools` on
	// every round but the last for an engine on the tool loop (loop.go) and
	// billed as input tokens; 0 outside the loop. Counted beside
	// PromptTokens in WorstMicros: costcrew#67 found the packet inside the
	// bound and the catalogue outside it, 2874 input tokens settled against
	// a prompt bounded at about 2833. deliver.ToolCatalogueTokens.
	CatalogueTokens int
	// MICRO-dollars, a millionth of a dollar, which is what the TokenFuse wire
	// already uses. Not cents.
	//
	// One call on the cheap route is about 2300 micros, a quarter of a cent,
	// and money.Cents floors that to zero. The first version of this printed
	// 0.00 against every task and, worse, compared 0 against the guard, so the
	// refusal could never fire. An estimator whose bound is always satisfied
	// is not a bound.
	WorstMicros int64

	// Packet is the TASK PACKET (packet.go), captured ONCE here at estimate
	// time and carried unchanged into execute()'s actual prompt. Reading
	// the estate again at call time, rather than reusing this, would let
	// the two disagree: the packet is capped at packetMaxBytes either way,
	// but its CONTENT could grow between pricing a run and executing it (a
	// person posts an explanation while a run is in flight), and the
	// estimate this struct carries would then be an estimate of a prompt
	// that was never actually sent.
	Packet string

	Verdict string // would run, or why not
	Refused bool
}

func run(dir, ceiling string, maxTok, sprint int, live, supervise, due bool, only int, engine, events, host, gateway, gatewayOpenAI string, local localOptions, typryxURL string) error {
	// Validated before the store or the bus are even opened. A bad -gateway
	// value is a configuration mistake, not a spending one, and the sooner it
	// is reported the less of the run has already happened around it.
	gatewayURL, err := normalizeGateway(gateway)
	if err != nil {
		return err
	}
	gatewayOpenAIURL, err := normalizeGatewayOpenAI(gatewayOpenAI)
	if err != nil {
		return err
	}
	// And the local engine's, in the same breath: its address, its price and its
	// token ceiling are configuration mistakes too, and the estimator below
	// reads the price from what apply() publishes.
	modelURL, err := local.apply()
	if err != nil {
		return err
	}
	gwCfg := gatewayConfig{URL: gatewayURL, OpenAIURL: gatewayOpenAIURL, Host: host,
		ModelURL: modelURL, MaxRunTokens: local.MaxRunTokens, LocalParallel: local.Parallel}
	typryxBase, err := typryx.NormalizeURL(typryxURL)
	if err != nil {
		return err
	}

	st, err := store.Open(dir)
	if err != nil {
		return err
	}
	defer st.Close()
	db := st.DB()

	// charges_query's own connection: opened here, once, alongside the
	// read-write one, rather than inside the spending path -- the same
	// reason the bus below is opened here, so a run that cannot get one
	// fails before anything is priced or spent rather than partway through.
	// Safe to open unconditionally even for a dry run: store.Open above has
	// already created app.db, so this never races an empty directory.
	roDB, err := store.OpenReadOnly(dir)
	if err != nil {
		return fmt.Errorf("opening the read-only connection charges_query needs: %w", err)
	}
	defer roDB.Close()

	// The bus this run reports to. Opened here rather than inside the
	// spending path so that a run which cannot open it fails BEFORE it
	// spends anything, rather than after.
	b, err := openBus(events, host)
	if err != nil {
		return err
	}
	defer b.close()
	// The local hash chain: every run opens a store, so every run can write
	// to it, whether or not -stack-events points anywhere. See bus.rec's own
	// comment.
	b.rec = st.AsRecorder()

	// The policy masks the names in THIS store, so it is bound to it now, and
	// the mode is recorded on the events this run writes. Said out loud when
	// it is not the default, because it changes what every prompt below
	// contains.
	deliver.BindActivePolicy(db)
	pol := deliver.ActivePolicy()
	b.promptData = string(pol.Mode())
	if !pol.Full() {
		fmt.Println(pol.ModeLine())
		fmt.Println()
	}

	if supervise {
		if sprint == 0 {
			return fmt.Errorf("-supervise needs -sprint: a pass over every sprint on the " +
				"board because nobody named one is a pass nobody chose")
		}
		return superviseRun(db, sprint, b)
	}

	var cap money.Cents
	hasCap := false
	if ceiling != "" {
		cap, err = money.Parse(ceiling)
		if err != nil {
			return fmt.Errorf("the ceiling must look like 25.00: %w", err)
		}
		hasCap = true
	}

	if due {
		gwCfg.CeilingUSD = cap
		return runDue(db, roDB, cap, hasCap, maxTok, live, b, gwCfg)
	}

	all, err := crew.Tasks(db, crew.TaskFilter{OpenOnly: true, Sprint: sprint})
	if err != nil {
		return err
	}
	tasks := workable(all)
	roster, err := crew.Roster(db)
	if err != nil {
		return err
	}
	by := map[string]crew.Analyst{}
	for _, a := range roster {
		by[a.Name] = a
	}

	// Before pricing, because pricing reads the packet once and carries it to
	// the call (estimate.Packet): a hint asked after it would never reach
	// the analyst. -live only, and only with -live's own ceiling present.
	if live && hasCap && typryxBase != "" {
		var picked []crew.Task
		for _, t := range tasks {
			if (engine == "" || by[t.Assignee].Engine == engine) && (only == 0 || t.ID == only) {
				picked = append(picked, t)
			}
		}
		sum, err := hintTasks(db, typryx.New(typryxBase, typryx.KeyFromEnv(), typryx.DefaultTimeout), picked, b)
		if err != nil {
			return err
		}
		fmt.Printf("typryx: %d asked, %d hinted, %d with no hint\n", sum.Asked, sum.Hinted, sum.NoHint)
	}

	ests := make([]estimate, 0, len(tasks))
	for _, t := range tasks {
		if engine != "" && by[t.Assignee].Engine != engine {
			continue
		}
		ests = append(ests, price(db, t, by[t.Assignee], maxTok))
	}
	refuseOwnerless(ests, gatewayConfig{URL: gatewayURL, OpenAIURL: gatewayOpenAIURL, Host: host, CeilingUSD: cap})
	sort.Slice(ests, func(i, j int) bool { return ests[i].WorstMicros > ests[j].WorstMicros })

	if !live {
		report(db, ests, maxTok, cap, hasCap)
		return nil
	}

	// A run with no ceiling is refused, never defaulted. A default ceiling is
	// a number nobody chose, and this is the one place where the number nobody
	// chose is the one that gets spent.
	if !hasCap {
		return fmt.Errorf("-live needs -ceiling: a run that can spend has to be " +
			"bounded by a figure somebody typed")
	}
	gwCfg.CeilingUSD = cap
	return spend(db, roDB, ests, maxTok, cap, only, b, gwCfg)
}

// price puts a worst case on one task.
func price(db *sql.DB, t crew.Task, a crew.Analyst, maxTok int) estimate {
	e := estimate{Task: t, Analyst: a, Engine: a.Engine}

	switch {
	case a.Name == "":
		e.Verdict, e.Refused = "nobody is assigned to it", true
		return e
	case a.State == "suspended":
		e.Verdict, e.Refused = a.Name+" is suspended", true
		return e
	case a.Engine == "":
		e.Verdict, e.Refused = a.Name+" was hired with no engine", true
		return e
	}

	e.Model = engines.DefaultModel(a.Engine)

	// The bound counts the string that is actually SENT, not the pieces it is
	// built from.
	//
	// It used to count title, goal, mission, role and skills, and none of the
	// fixed text around them: "You are X on the Y desk", the date, the format
	// note, the closing instruction. Measured on a real task, 2026-08-24: it
	// bounded the prompt at 225 tokens and the prompt was 559 bytes. The bound
	// held anyway, because a real tokeniser gives about a quarter of that, but
	// the comment above claims one token per byte and that claim was false for
	// everything it did not count. A bound whose guarantee is narrower than its
	// sentence is the shape of every overrun in this file's history.
	//
	// A fixed date, not today's: the estimate must not move because the clock
	// did, and every date is the same ten bytes.
	//
	// The packet is read HERE, once, and carried in e.Packet rather than
	// rebuilt by execute(): see estimate.Packet's own comment for why.
	e.Packet = packet(db, t, a)
	e.PromptTokens = tokens(prompt(t, a, "0000-00-00", e.Packet))
	e.CatalogueTokens = deliver.ToolCatalogueTokens(a.Engine)

	metered, known := engines.Metered(a.Engine)
	if !known {
		e.Verdict = a.Engine + " is not an engine this console knows, so what a " +
			"call would cost cannot be bounded"
		e.Refused = true
		return e
	}
	if !metered {
		// Not billed, and not run either: no caller is written for a local
		// subscription. Saying only the first half read as "this will happen
		// and cost nothing", and twenty-three tasks quietly did not happen.
		e.Verdict = "on a local subscription: nothing extra is billed, and " +
			"nothing here runs it either"
		e.Refused = true
		return e
	}

	p, ok := engines.PriceFor(a.Engine, e.Model)
	if !ok {
		if a.Engine == engines.LocalID {
			// Not "no price is known": the operator is the price list for this
			// engine, and what is missing is the model they have to name.
			e.Verdict = "the local engine needs -model-name (COSTCREW_MODEL_NAME): the operator " +
				"names the model their own server serves"
		} else {
			e.Verdict = "no price is known for " + a.Engine + "/" + e.Model
		}
		e.Refused = true
		return e
	}
	e.Price, e.Priced = p, true

	// B5-SPEC.md section 3 point 3: this arithmetic now lives in
	// internal/deliver, shared with the /cadence console page, which cannot
	// import this "package main" to call it here directly. Behaviour is
	// unchanged; only the formula's one home moved.
	//
	// e.WorstMicros is ONE call's own bound. It stays that -- callers that
	// need the RESERVED figure (this Verdict comparison, report(), spend()'s
	// and -due's own whole-run preflights, and execute()'s actual reserve()
	// call) go through reservedWorstCase(e) instead of reading this field
	// directly, per PRICE-DISPLAY-SPEC.md, 2026-09-03: see that function's
	// own comment for why a second copy of the multiplier is exactly what
	// broke here the first time.
	e.WorstMicros = deliver.WorstCaseMicros(e.PromptTokens+e.CatalogueTokens, maxTok, p)

	// The guard is in cents and the estimate is in micros, so the comparison
	// happens in micros. Converting the other way would floor the estimate to
	// zero and compare nothing against something.
	//
	// Compared against reservedWorstCase(e), the RESERVED figure, not
	// e.WorstMicros: a task on the tool loop (anthropic, openrouter) can
	// make up to loopsFor(e.Engine) calls in one execute(), each reserved
	// before the first round is sent, so a guard that covers one call's own
	// bound can still be refused live. Before this fix this compared
	// e.WorstMicros directly, so a task could print "inside its guard" here
	// and be refused by execute()'s own reserve() minutes later -- found
	// running the first real live task on a real Anthropic account,
	// PRICE-DISPLAY-SPEC.md.
	leftMicros := int64(t.Budget-t.Spent) * 10_000
	reserved := reservedWorstCase(e)
	switch {
	case t.Budget <= 0:
		e.Verdict = "no per-task guard on this one"
	case reserved > leftMicros:
		e.Verdict = fmt.Sprintf("worst case %s is past what is left of its guard, %s",
			usd(reserved), usd(leftMicros))
		e.Refused = true
	default:
		e.Verdict = fmt.Sprintf("inside its guard, %s left after", usd(leftMicros-reserved))
	}
	return e
}

// reservedWorstCase is the true worst case a run reserves for one task: one
// call's own bound (e.WorstMicros) times however many calls loopsFor(e.Engine)
// says one execute() of it can make through the tool loop (loop.go). The
// SAME figure execute()'s own reserve() call computes before the first round
// (live.go) -- used here by price()'s own Verdict comparison against the
// per-task guard, by report() (so the number a person reads before choosing
// -ceiling is the number a live run will actually reserve), and by spend()'s
// and -due's own whole-run preflight sums (live.go, due.go), so none of them
// can diverge from reserve() the way they did the night PRICE-DISPLAY-SPEC.md
// was written: report() showed a worst case of $0.0385 for task 294 on the
// anthropic engine and reserve() required $0.2312 before it would let the
// first round through -- almost exactly loopsFor("anthropic") (6) times
// more, the exact ratio this function now makes structural rather than
// coincidental.
func reservedWorstCase(e estimate) int64 {
	return e.WorstMicros * int64(loopsFor(e.Engine))
}

// tokens is production's own call into internal/deliver.Tokens, which is an
// UPPER BOUND on a prompt, never an estimate of it: one token per byte, since
// no tokeniser splits below a byte. Moved there (B7-SPEC.md section 3) so
// tools/bench prices a live run's worst case "the same arithmetic tools/run
// prices with" (B7-SPEC.md section 2) rather than a second formula that only
// looks like it. This wrapper keeps the old unexported name so every call
// site and test in this package needed no change.
func tokens(parts ...string) int {
	return deliver.Tokens(parts...)
}

func report(db *sql.DB, ests []estimate, maxTok int, cap money.Cents, hasCap bool) {
	fmt.Println("DRY RUN. Nothing was called and nothing can be: this binary holds no")
	fmt.Println("HTTP client and reads no key. It prices the open board and stops.")
	fmt.Println()

	var worstMicros int64
	var wouldRun, refused, free, localAtZero int
	for _, e := range ests {
		switch {
		case e.Refused:
			refused++
		case !e.Priced:
			free++
		default:
			wouldRun++
			if e.Engine == engines.LocalID && priceIsZero(e.Price) {
				localAtZero++
			}
			// Summed BEFORE rounding. Forty-two calls at a quarter of a cent
			// each is ten cents; forty-two roundings of a quarter of a cent
			// is nothing.
			//
			// reservedWorstCase(e), not e.WorstMicros: this is the number a
			// person reads before choosing -ceiling, so it must be what a
			// live run would actually reserve, loops included, not one
			// call's own bound. PRICE-DISPLAY-SPEC.md, 2026-09-03.
			worstMicros += reservedWorstCase(e)
		}
	}

	fmt.Printf("%d open tasks\n", len(ests))
	fmt.Printf("  %3d would run, worst case %s in total\n", wouldRun, usd(worstMicros))
	fmt.Printf("  %3d on a subscription, nothing new billed\n", free)
	fmt.Printf("  %3d refused before any call\n", refused)
	if localAtZero > 0 {
		// The worst case above counts these at 0, which is a statement about the
		// price and not about the work: money cannot bound them.
		fmt.Printf("  %3d of the above are on the local engine at a price of 0: money cannot bound\n"+
			"      them, so a live run needs -max-run-tokens (or a price for your hardware)\n", localAtZero)
	}
	fmt.Println()

	if hasCap {
		capMicros := int64(cap) * 10_000
		if worstMicros > capMicros {
			fmt.Printf("OVER THE CEILING. The worst case is %s and the ceiling is %s.\n",
				usd(worstMicros), cap)
			fmt.Printf("A live run would refuse to start. Raise it deliberately or narrow the sprint.\n\n")
		} else {
			fmt.Printf("Inside the ceiling: %s of %s, %s to spare.\n\n",
				usd(worstMicros), cap, usd(capMicros-worstMicros))
		}
	} else {
		fmt.Printf("No ceiling given. A live run would need one: pass -ceiling.\n\n")
	}

	fmt.Printf("%-22s %-12s %-28s %9s  %s\n", "TASK", "ANALYST", "ENGINE/MODEL", "WORST", "VERDICT")
	for _, e := range ests {
		mark := "   "
		if e.Refused {
			mark = " ! "
		}
		em := e.Engine
		if e.Model != "" {
			em += "/" + e.Model
		}
		w := "-"
		if e.Priced {
			// The same reservedWorstCase(e) the run total above sums: a
			// person reads this column's own row and this must be the
			// worst case THAT task would actually reserve, not one call's.
			w = usd(reservedWorstCase(e))
		}
		fmt.Printf("%s%-19s %-12s %-28s %9s  %s\n",
			mark, trim(e.Task.Title, 19), trim(e.Analyst.Name, 12), trim(em, 28), w, e.Verdict)
	}

	fmt.Println()
	fmt.Printf("How the worst case is built: the prompt is this task and its analyst's\n")
	fmt.Printf("brief, bounded at one token per byte, which no tokeniser can exceed.\n")
	fmt.Printf("An engine on the tool loop (anthropic, openrouter, local) also sends the tool\n")
	fmt.Printf("catalogue on every round, %d or %d bytes, counted at the same rule, and\n",
		deliver.ToolCatalogueTokens("anthropic"), deliver.ToolCatalogueTokens("openrouter"))
	fmt.Printf("the whole call is reserved %d times over for the loop's rounds.\n", maxToolRounds)
	fmt.Printf("The output is the full %d token cap at the\n", maxTok)
	fmt.Printf("model's output price, because how long an answer runs is not known\n")
	fmt.Printf("before it is asked for.\n\n")
	fmt.Printf("Prices used:\n%s", engines.PriceTable())
	fmt.Printf("\nRe-check anything marked @claude against the vendor before spending.\n")
}

// usd renders micro-dollars at four decimal places, because a call on the
// cheap route costs a fraction of a cent and two places would print every one
// of them as nothing.
// workable drops the tasks somebody has stopped.
//
// crew.TaskFilter{OpenOnly} means queued, active, blocked and returned, which
// is right for a board and wrong for a thing that does the work: `blocked`
// carries a reason a person wrote down, and on the seeded estate those reasons
// are exactly the ones an analyst must not work around.
//
//	Tagging feed from the azure desk has been stale since the 9th;
//	the numbers would be wrong.
//
// A run took 19 of those anyway. Each produced a deliverable off numbers the
// task itself says are wrong, and the page then showed the block and the
// finished draft side by side, contradicting itself in two lines.
//
// So a blocked task stays blocked until a person unblocks it. That also holds
// for a task THIS runner blocked when an engine failed: the person should see
// what happened and decide, rather than have the next run quietly retry.
func workable(in []crew.Task) []crew.Task {
	out := make([]crew.Task, 0, len(in))
	for _, t := range in {
		if t.State == "blocked" {
			continue
		}
		out = append(out, t)
	}
	return out
}

func usd(micros int64) string {
	return fmt.Sprintf("%.4f", float64(micros)/1e6)
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

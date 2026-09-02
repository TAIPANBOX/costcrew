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
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/store"
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
	// The estate integration, off unless pointed somewhere, exactly as the
	// console's own is. The file NAME is the integration: genaryx keys each
	// source's read offset off the stem, so this has to be costcrew.ndjson and
	// nothing else, and it is the same file the console appends to.
	events := flag.String("stack-events", "", "append agent-events to this NDJSON file; empty means off")
	host := flag.String("stack-host", "", "the agent:// authority for this installation; must match the console's")
	// The TokenFuse gateway, off unless pointed somewhere. Falls back to
	// COSTCREW_GATEWAY so an installation can set it once rather than on
	// every invocation; an explicit -gateway "" still turns it off even with
	// the environment variable set. Only the Anthropic route uses it today:
	// TokenFuse speaks the Anthropic Messages API and nothing OpenAI-shaped.
	gateway := flag.String("gateway", gatewayEnvDefault(),
		"TokenFuse gateway for the Anthropic route, e.g. http://127.0.0.1:4177; "+
			"empty calls api.anthropic.com directly. Falls back to COSTCREW_GATEWAY.")
	flag.Parse()

	if *showPrices {
		fmt.Print("Prices this estimate would use, per million tokens:\n\n")
		fmt.Print(engines.PriceTable())
		fmt.Print("\nEvery line says where it came from. The ones marked @claude are\n" +
			"unverified against the vendor and must be re-checked before a live call.\n")
		return
	}

	if err := run(*dir, *ceiling, *maxTok, *sprint, *live, *supervise, *only, *engine, *events, *host, *gateway); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}
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

func run(dir, ceiling string, maxTok, sprint int, live, supervise bool, only int, engine, events, host, gateway string) error {
	// Validated before the store or the bus are even opened. A bad -gateway
	// value is a configuration mistake, not a spending one, and the sooner it
	// is reported the less of the run has already happened around it.
	gatewayURL, err := normalizeGateway(gateway)
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

	ests := make([]estimate, 0, len(tasks))
	for _, t := range tasks {
		if engine != "" && by[t.Assignee].Engine != engine {
			continue
		}
		ests = append(ests, price(db, t, by[t.Assignee], maxTok))
	}
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
	return spend(db, roDB, ests, maxTok, cap, only, b, gatewayConfig{URL: gatewayURL, Host: host, CeilingUSD: cap})
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
		e.Verdict, e.Refused = "no price is known for "+a.Engine+"/"+e.Model, true
		return e
	}
	e.Price, e.Priced = p, true

	in := float64(e.PromptTokens) / 1e6 * p.InPerM
	out := float64(maxTok) / 1e6 * p.OutPerM
	e.WorstMicros = int64((in + out) * 1e6)

	// The guard is in cents and the estimate is in micros, so the comparison
	// happens in micros. Converting the other way would floor the estimate to
	// zero and compare nothing against something.
	leftMicros := int64(t.Budget-t.Spent) * 10_000
	switch {
	case t.Budget <= 0:
		e.Verdict = "no per-task guard on this one"
	case e.WorstMicros > leftMicros:
		e.Verdict = fmt.Sprintf("worst case %s is past what is left of its guard, %s",
			usd(e.WorstMicros), usd(leftMicros))
		e.Refused = true
	default:
		e.Verdict = fmt.Sprintf("inside its guard, %s left after", usd(leftMicros-e.WorstMicros))
	}
	return e
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
	var wouldRun, refused, free int
	for _, e := range ests {
		switch {
		case e.Refused:
			refused++
		case !e.Priced:
			free++
		default:
			wouldRun++
			// Summed BEFORE rounding. Forty-two calls at a quarter of a cent
			// each is ten cents; forty-two roundings of a quarter of a cent
			// is nothing.
			worstMicros += e.WorstMicros
		}
	}

	fmt.Printf("%d open tasks\n", len(ests))
	fmt.Printf("  %3d would run, worst case %s in total\n", wouldRun, usd(worstMicros))
	fmt.Printf("  %3d on a subscription, nothing new billed\n", free)
	fmt.Printf("  %3d refused before any call\n", refused)
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
			w = usd(e.WorstMicros)
		}
		fmt.Printf("%s%-19s %-12s %-28s %9s  %s\n",
			mark, trim(e.Task.Title, 19), trim(e.Analyst.Name, 12), trim(em, 28), w, e.Verdict)
	}

	fmt.Println()
	fmt.Printf("How the worst case is built: the prompt is this task and its analyst's\n")
	fmt.Printf("brief, bounded at one token per byte, which no tokeniser can exceed.\n")
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

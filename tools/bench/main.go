// Command bench scores a named cause against the truth.
//
// B7-SPEC.md. The generated estate knows the truth about itself: every
// planted event that is a driver carries a label, a kind and a scope, and
// the detector's own findings on the fixture therefore have a known cause.
// This runs an analyst (or the mock engine, see mock.go) on N such
// anomalies with that cause HIDDEN from its packet, scores whether the
// deliverable names the right service, day and kind and whether its named
// cause matches the label, and prints accuracy per skill and per engine
// beside the cost per task.
//
// It writes nothing to the estate: no task, no artifact, no charge, no
// journal row (section 3). It reads a store, seeding one that is fresh
// exactly the way the console itself does at startup, and prints.
//
// HARD RULE, stated here because it is the one rule this command exists
// under and not merely follows: -live is refused with mock or mock-oracle,
// and without -live any other engine is priced and refused, never called.
// This agent never passes -live in any test or any run it makes.
//
// B6B-SPEC.md: -live with a real engine now calls the shared caller, one
// call path for tools/run and tools/bench, in internal/deliver.Call -- the
// same one every live crew call goes through since B6 put the TokenFuse
// gateway in tools/run's own call path. This package still holds no caller
// of its own: no model provider credential read from the process
// environment, no HTTP client of any kind anywhere under tools/bench (see
// live_test.go's own structural check on the package's source; gateway.go,
// this step's own new file, reaches every call through deliver.Call). -live
// is refused, before the store opens, unless -gateway is also given: the
// bench's spend must be metered exactly like the crew's, never a second,
// unmetered path.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/detect"
	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

func main() {
	code, err := run(os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
	}
	os.Exit(code)
}

// run is main()'s whole body, pulled out so a test drives it with its own
// argument list and its own buffers rather than a subprocess: flag.Parse
// against the package-level FlagSet calls os.Exit on a bad flag, which
// would take the test binary down with it. This FlagSet uses
// ContinueOnError instead, so a hostile flag comes back as an error a test
// can assert on.
func run(args []string, stdout, stderr io.Writer) (code int, err error) {
	// Reassigning flag.CommandLine, not flag.NewFlagSet's own local
	// variable, so the flags below are declared through the package-level
	// flag.String/Int/Bool functions -- the exact call shape
	// internal/manifest/manifest_test.go's own regexp
	// (`flag\.(?:String|Int|Bool|Duration|Float64)\(`) reads components.json
	// against. A FlagSet method call (fs.String(...)) is invisible to that
	// check, which is not a workaround so much as the whole reason this
	// binary's flags are declared where they are: the same convention
	// tools/run/main.go already uses. ContinueOnError in place of the
	// default's ExitOnError is the only difference from a normal main(),
	// and it is what lets a test drive a hostile flag value and read the
	// result back instead of losing the test binary to os.Exit.
	flag.CommandLine = flag.NewFlagSet("bench", flag.ContinueOnError)
	flag.CommandLine.SetOutput(stderr)
	dir := flag.String("dir", "./local",
		"the store directory; a fresh one is seeded from the generated estate, as the console itself does at startup")
	n := flag.Int("n", 20, "how many known cases, capped by what the fixture holds")
	skill := flag.String("skill", "triage", "triage or investigate: which role answers")
	engine := flag.String("engine", "mock", "which engine route to score, or mock/mock-oracle (section 4)")
	live := flag.Bool("live", false,
		"actually call a model; refused with mock or mock-oracle, and without -gateway too (section 4)")
	// Int, not Int64: the same regexp above has no Int64 alternative, and a
	// plain int is 64 bits on every platform this ever builds for, so
	// nothing is actually lost by the narrower type here.
	seed := flag.Int("seed", 1, "the random seed for case selection, so a run is reproducible")
	maxTok := flag.Int("max-tokens", 2000, "the output cap a live call would be made with")
	// The TokenFuse gateway, the runner's own flag, same help text, same
	// COSTCREW_GATEWAY fallback (B6B-SPEC.md section 2: "the same
	// validation"): a bench live run is metered exactly like a crew one.
	gateway := flag.String("gateway", deliver.GatewayEnvDefault(),
		"TokenFuse gateway for the Anthropic route, e.g. http://127.0.0.1:4177; "+
			"empty calls api.anthropic.com directly. Falls back to COSTCREW_GATEWAY.")
	if err := flag.CommandLine.Parse(args); err != nil {
		return 2, nil // flag.CommandLine already wrote its own message to stderr
	}

	if *n <= 0 {
		return 1, fmt.Errorf("-n must be at least 1, got %d", *n)
	}
	if *skill != "triage" && *skill != "investigate" {
		return 1, fmt.Errorf("-skill must be \"triage\" or \"investigate\", got %q", *skill)
	}
	if *live && isMockEngine(*engine) {
		return 1, fmt.Errorf("-live with -engine %s is refused: neither mock engine is "+
			"ever selectable with -live (section 4)", *engine)
	}
	if !isMockEngine(*engine) {
		if _, known := engines.Metered(*engine); !known {
			return 1, fmt.Errorf("-engine %q is not mock, mock-oracle, or an engine this "+
				"console knows", *engine)
		}
	}

	// -gateway is validated before the store is even opened, the same
	// "a bad value is a configuration mistake, not a spending one" reasoning
	// tools/run/main.go's own run() uses, and the same wording
	// TestNormalizeGatewayRefusesANonHTTPURL already holds there
	// (B6B-SPEC.md section 4: "in both binaries, with the same message").
	gatewayURL, err := deliver.NormalizeGateway(*gateway)
	if err != nil {
		return 1, err
	}

	// Checked after the two engine-name checks above (a bogus -engine value
	// is refused for THAT reason, not this one), and before the store is
	// even opened: no case is selected, no packet is built, nothing is
	// priced. See this file's own top comment for why.
	if *live && gatewayURL == "" {
		return 1, fmt.Errorf("-live needs -gateway: the bench's spend must be metered " +
			"exactly like the crew's, through the same TokenFuse gateway, never a " +
			"second, unmetered path")
	}

	st, err := store.Open(*dir)
	if err != nil {
		return 1, err
	}
	defer st.Close()
	db := st.DB()

	fresh, err := ensureSeeded(db)
	if err != nil {
		return 1, err
	}

	anyDriver, err := hasAnyDriver(db)
	if err != nil {
		return 1, err
	}
	if !anyDriver {
		return runStampMode(db, stdout, *n, *skill, *engine, int64(*seed))
	}
	return runFixtureMode(db, stdout, *n, *skill, *engine, int64(*seed), *maxTok, fresh, *live, gatewayURL)
}

// ensureSeeded brings a fresh -dir up to the same baseline the console's
// own startup builds (B7-SPEC.md section 2: "a fresh install is seeded from
// the generated estate as tools/run does"): the charges, the roster, and
// one detection pass so the anomalies table -- and the driver label on the
// ones a registry entry explains -- exists to select known cases from.
//
// fresh reports whether THIS call is the one that actually created the
// charges, which is estate.Seed's own return value turned into a bool
// rather than assumed: an existing store (a live console's own data, or
// charges newer than whatever detection last ran against them) must never
// have the roster or a detection pass written into it by a tool that is
// only supposed to read and print. Coordinator review of PR #25,
// 2026-09-03, red first on TestBenchDoesNotDetectAgainstAnExistingStore
// (tools/bench): before this, an existing store's anomalies table gained
// nine rows it never asked for. On an existing store this function now
// does nothing but ensure the board's schema exists (see below), and
// run() reads whatever anomalies are already there, however many that is.
//
// Recorder nil on the fresh path: nothing here should journal a governance
// event on the bench's account, and detect.Run already treats a nil
// Recorder as "say nothing", never as an error.
func ensureSeeded(db *sql.DB) (fresh bool, err error) {
	seededRows, err := estate.Seed(db)
	if err != nil {
		return false, fmt.Errorf("seeding the estate: %w", err)
	}
	fresh = seededRows > 0

	// Schema for both the roster and the board, unconditional on fresh:
	// CREATE TABLE IF NOT EXISTS never adds a ROW, only a table a query
	// would otherwise fail against with "no such table" -- selectKnownCases
	// and selectStampCases both read the roster even on an existing store
	// this call did not seed, and stamp mode reads the board the same way.
	// A strict no-op against a directory the console itself already
	// brought up, existing or not.
	if _, err := db.Exec(crew.RosterSchema); err != nil {
		return fresh, fmt.Errorf("ensuring the roster's schema exists: %w", err)
	}
	if _, err := db.Exec(crew.Schema); err != nil {
		return fresh, fmt.Errorf("ensuring the board's schema exists: %w", err)
	}

	// The DATA -- 39 fixture analysts, a detection pass over the estate --
	// only when this call is the one that just created the charges that
	// data is about. An existing store keeps whatever roster and whatever
	// anomalies it already has; this bench reads them, never adds to them.
	if fresh {
		if _, err := crew.SeedRoster(db, "bench"); err != nil {
			return fresh, fmt.Errorf("seeding the roster: %w", err)
		}
		if _, _, err := anomaly.Run(db, time.Now(), detect.Default(), nil); err != nil {
			return fresh, fmt.Errorf("running detection: %w", err)
		}
	}
	return fresh, nil
}

func runStampMode(db *sql.DB, w io.Writer, n int, skill, engine string, seed int64) (int, error) {
	cases, total, err := selectStampCases(db, skill, engine, n)
	if err != nil {
		return 1, err
	}
	note := ""
	if total > len(cases) || n > total {
		note = fmt.Sprintf("       requested %d, %d stamped case(s) match skill %s and engine %s; using %d",
			n, total, skill, engine, len(cases))
	}
	printStampReport(w, seed, cases, skill, engine, note)
	return 0, nil
}

// runFixtureMode: with -live absent, an engine that is not mock is always
// priced and refused (exit 2), never called. With -live present, run()'s
// own preflight has already required -gateway to be set (section 4's
// "before the store opens" boundary), so live here always means "call it,
// through the gateway". fresh is ensureSeeded's own report of whether THIS
// call created the estate; false means the bench read an existing store
// rather than seeding one, and says so, in place of running detection
// against it.
func runFixtureMode(db *sql.DB, w io.Writer, n int, skill, engine string, seed int64, maxTok int, fresh, live bool, gatewayURL string) (int, error) {
	cases, total, eligible, err := selectKnownCases(db, skill, n, seed)
	if err != nil {
		return 1, err
	}
	var notes []string
	if !fresh {
		notes = append(notes, fmt.Sprintf(
			"       existing store, read as found rather than re-detected: "+
				"%d anomal%s carr%s a driver", total, plural(total, "y", "ies"), plural(total, "ies", "y")))
	}
	if n > eligible {
		notes = append(notes, fmt.Sprintf("       requested %d, %d anomal%s carr%s a driver and %d "+
			"eligible for skill %s; using %d",
			n, total, plural(total, "y", "ies"), plural(total, "ies", "y"), eligible, skill, len(cases)))
	}
	note := strings.Join(notes, "\n")

	if !isMockEngine(engine) {
		model := engines.DefaultModel(engine)
		p, known := engines.PriceFor(engine, model)
		if !known {
			return 1, fmt.Errorf("no price is known for %s/%s, so a live run's worst case "+
				"cannot be bounded", engine, model)
		}

		if live {
			results, err := scoreLive(db, cases, engine, model, p, maxTok, gatewayURL)
			if err != nil {
				return 1, err
			}
			printDriverReport(w, seed, results, skill, engine, note)
			return 0, nil
		}

		worst, err := worstCaseMicros(db, cases, engine, model, p, maxTok)
		if err != nil {
			return 1, err
		}
		if note != "" {
			fmt.Fprintln(w, note)
		}
		printWorstCasePrice(w, len(cases), engine, model, worst)
		return 2, nil
	}

	results, err := scoreMock(db, cases, engine)
	if err != nil {
		return 1, err
	}
	printDriverReport(w, seed, results, skill, engine, note)
	return 0, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

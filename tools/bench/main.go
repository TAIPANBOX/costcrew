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
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
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
		"actually call a model; refused with mock or mock-oracle, and without it any other engine is priced and refused")
	// Int, not Int64: the same regexp above has no Int64 alternative, and a
	// plain int is 64 bits on every platform this ever builds for, so
	// nothing is actually lost by the narrower type here.
	seed := flag.Int("seed", 1, "the random seed for case selection, so a run is reproducible")
	maxTok := flag.Int("max-tokens", 2000, "the output cap a live call would be made with")
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

	st, err := store.Open(*dir)
	if err != nil {
		return 1, err
	}
	defer st.Close()
	db := st.DB()

	if err := ensureSeeded(db); err != nil {
		return 1, err
	}

	anyDriver, err := hasAnyDriver(db)
	if err != nil {
		return 1, err
	}
	if !anyDriver {
		return runStampMode(db, stdout, *n, *skill, *engine, int64(*seed))
	}
	return runFixtureMode(context.Background(), db, stdout, *n, *skill, *engine, *live, int64(*seed), *maxTok)
}

// ensureSeeded brings a fresh -dir up to the same baseline the console's
// own startup builds (B7-SPEC.md section 2: "a fresh install is seeded from
// the generated estate as tools/run does"): the charges, the roster, and
// one detection pass so the anomalies table -- and the driver label on the
// ones a registry entry explains -- exists to select known cases from.
// Every step here is idempotent (estate.Seed and crew.SeedRoster both
// refuse to run twice; anomaly.Run reconciles rather than replaces), so
// calling this against an ALREADY-seeded -dir, generated or imported, does
// nothing. Recorder nil: nothing here should journal a governance event on
// the bench's account, and detect.Run already treats a nil Recorder as
// "say nothing", never as an error.
func ensureSeeded(db *sql.DB) error {
	if _, err := estate.Seed(db); err != nil {
		return fmt.Errorf("seeding the estate: %w", err)
	}
	if _, err := crew.SeedRoster(db, "bench"); err != nil {
		return fmt.Errorf("seeding the roster: %w", err)
	}
	if _, _, err := anomaly.Run(db, time.Now(), detect.Default(), nil); err != nil {
		return fmt.Errorf("running detection: %w", err)
	}
	// Schema only, never crew.Seed: this bench never wants the 279-deliverable
	// generated board (fixture mode builds its own in-memory crew.Task and
	// touches the tasks/artifacts TABLES not at all), but stamp mode reads
	// them with crew.Tasks/crew.Artifacts, and a store that has only ever
	// been seeded by THIS command otherwise has no such tables to query at
	// all. CREATE TABLE IF NOT EXISTS is schema, not a row, and it is a
	// strict no-op against a directory the console itself already brought up.
	if _, err := db.Exec(crew.Schema); err != nil {
		return fmt.Errorf("ensuring the board's schema exists: %w", err)
	}
	return nil
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

func runFixtureMode(ctx context.Context, db *sql.DB, w io.Writer, n int, skill, engine string, live bool, seed int64, maxTok int) (int, error) {
	cases, total, eligible, err := selectKnownCases(db, skill, n, seed)
	if err != nil {
		return 1, err
	}
	note := ""
	if n > eligible {
		note = fmt.Sprintf("       requested %d, %d anomal%s carr%s a driver and %d "+
			"eligible for skill %s; using %d",
			n, total, plural(total, "y", "ies"), plural(total, "ies", "y"), eligible, skill, len(cases))
	}

	if !isMockEngine(engine) {
		model := engines.DefaultModel(engine)
		p, known := engines.PriceFor(engine, model)
		if !known {
			return 1, fmt.Errorf("no price is known for %s/%s, so a live run's worst case "+
				"cannot be bounded", engine, model)
		}
		if !live {
			worst, err := worstCaseMicros(db, cases, engine, model, p, maxTok)
			if err != nil {
				return 1, err
			}
			printWorstCasePrice(w, len(cases), engine, model, worst)
			return 2, nil
		}
		results, err := scoreLive(ctx, db, cases, engine, model, p, maxTok)
		if err != nil {
			return 1, err
		}
		printDriverReport(w, seed, results, skill, engine, note)
		return 0, nil
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

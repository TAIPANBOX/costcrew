// Command enforce shows what this console's budgets would set on TokenFuse's
// control plane, and sets them only when told to.
//
// Two steps on purpose. This is the one integration in the estate that CHANGES
// something in another service, and the thing it changes decides whether a
// model call is refused. So the default is to print the diff and send nothing.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/enforce"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

func main() {
	os.Exit(run(os.Args[0], os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// run is main minus os: the program name (for the apply hint), the arguments,
// a way to read the environment, the two streams, and an exit status. The key
// is read through getenv so a test can hand it one without touching the real
// environment, and so no test can pick up a real TokenFuse key by accident.
func run(prog string, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	// Declared through the package-level flag.String calls on a fresh
	// CommandLine, the shape internal/manifest reads components.json against
	// (tools/bench/main.go says why at length); ContinueOnError so a test gets a
	// bad flag back as a status and not as os.Exit.
	flag.CommandLine = flag.NewFlagSet("enforce", flag.ContinueOnError)
	flag.CommandLine.SetOutput(stderr)
	dir := flag.String("data", ".", "the console's data directory")
	base := flag.String("cloud", "", "TokenFuse control plane, e.g. http://127.0.0.1:8791")
	period := flag.String("period", "", "which month's budgets to push; default is the last closed one")
	expect := flag.String("apply", "", "the plan's fingerprint, from a run without this flag. "+
		"Sends exactly the plan that was printed with that fingerprint, and refuses if it has changed")
	if err := flag.CommandLine.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	key := getenv("TOKENFUSE_KEY")
	cfg := enforce.Config{BaseURL: *base, Key: key}
	if !cfg.On() {
		fmt.Fprintln(stderr, "enforcement is off: pass -cloud URL and set TOKENFUSE_KEY.")
		fmt.Fprintln(stderr, "The key is read from the environment and never written anywhere.")
		return 2
	}

	st, err := store.Open(*dir)
	if err != nil {
		return fail(stderr, err)
	}
	defer st.Close()

	p := *period
	if p == "" {
		p = world.DayBefore(world.LastDay, 40)[:7]
	}
	want, err := teamBudgets(st.DB(), p)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "%d team budgets from %s\n\n", len(want), p)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	plan, err := enforce.MakePlan(ctx, cfg, want)
	if err != nil {
		return fail(stderr, err)
	}

	if plan.Empty() {
		fmt.Fprintf(stdout, "Nothing to change: %d already match.\n", plan.Unchanged)
		return 0
	}
	fmt.Fprintf(stdout, "%-22s %14s %14s\n", "UNIT", "SET NOW", "WOULD BE")
	for _, c := range plan.Changes {
		now := "(none)"
		if c.HasNow {
			now = c.Now.String()
		}
		note := ""
		switch {
		case c.Lowered:
			note = "   <-- LOWER, the direction that stops work"
		case c.New:
			note = "   new"
		}
		fmt.Fprintf(stdout, "%-22s %14s %14s%s\n", c.Unit, now, c.Want.String(), note)
	}
	fmt.Fprintf(stdout, "\n%d to change, %d of them lower, %d new, %d already right.\n",
		len(plan.Changes), plan.Lowered, plan.Added, plan.Unchanged)

	fp := plan.Fingerprint()
	if *expect == "" {
		fmt.Fprintf(stdout, "\nNothing was sent. To send exactly this and nothing else:\n")
		fmt.Fprintf(stdout, "  %s -apply %s\n", prog, fp)
		fmt.Fprintf(stdout, "\nIf anything moves in between, that command refuses rather than sending\n"+
			"a different set of numbers than the ones above.\n")
		return 0
	}
	n, err := enforce.Apply(ctx, cfg, plan, *expect)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "\nSet %d unit budget(s). Gateways poll this every three seconds.\n", n)
	return 0
}

// teamBudgets is what this console says each team may spend in a month.
//
// Summed across desks: a team's budget is what it may spend in total, and
// TokenFuse's unit is the team, not the team-on-a-desk.
func teamBudgets(db *sql.DB, period string) (map[string]money.Cents, error) {
	out := map[string]money.Cents{}
	for _, d := range world.Desks {
		rows, err := finops.BudgetsFor(db, d.Name, period)
		if err != nil {
			return nil, err
		}
		for _, b := range rows {
			out[b.Team] += b.Budget
		}
	}
	return out, nil
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "enforce:", err)
	return 1
}

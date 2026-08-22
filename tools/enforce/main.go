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
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/enforce"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

func main() {
	dir := flag.String("data", ".", "the console's data directory")
	base := flag.String("cloud", "", "TokenFuse control plane, e.g. http://127.0.0.1:8791")
	period := flag.String("period", "", "which month's budgets to push; default is the last closed one")
	expect := flag.String("apply", "", "the plan's fingerprint, from a run without this flag. "+
		"Sends exactly the plan that was printed with that fingerprint, and refuses if it has changed")
	flag.Parse()

	key := os.Getenv("TOKENFUSE_KEY")
	cfg := enforce.Config{BaseURL: *base, Key: key}
	if !cfg.On() {
		fmt.Fprintln(os.Stderr, "enforcement is off: pass -cloud URL and set TOKENFUSE_KEY.")
		fmt.Fprintln(os.Stderr, "The key is read from the environment and never written anywhere.")
		os.Exit(2)
	}

	st, err := store.Open(*dir)
	if err != nil {
		fail(err)
	}
	defer st.Close()

	p := *period
	if p == "" {
		p = world.DayBefore(world.LastDay, 40)[:7]
	}
	want, err := teamBudgets(st.DB(), p)
	if err != nil {
		fail(err)
	}
	fmt.Printf("%d team budgets from %s\n\n", len(want), p)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	plan, err := enforce.MakePlan(ctx, cfg, want)
	if err != nil {
		fail(err)
	}

	if plan.Empty() {
		fmt.Printf("Nothing to change: %d already match.\n", plan.Unchanged)
		return
	}
	fmt.Printf("%-22s %14s %14s\n", "UNIT", "SET NOW", "WOULD BE")
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
		fmt.Printf("%-22s %14s %14s%s\n", c.Unit, now, c.Want.String(), note)
	}
	fmt.Printf("\n%d to change, %d of them lower, %d new, %d already right.\n",
		len(plan.Changes), plan.Lowered, plan.Added, plan.Unchanged)

	fp := plan.Fingerprint()
	if *expect == "" {
		fmt.Printf("\nNothing was sent. To send exactly this and nothing else:\n")
		fmt.Printf("  %s -apply %s\n", os.Args[0], fp)
		fmt.Printf("\nIf anything moves in between, that command refuses rather than sending\n" +
			"a different set of numbers than the ones above.\n")
		return
	}
	n, err := enforce.Apply(ctx, cfg, plan, *expect)
	if err != nil {
		fail(err)
	}
	fmt.Printf("\nSet %d unit budget(s). Gateways poll this every three seconds.\n", n)
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

func fail(err error) {
	fmt.Fprintln(os.Stderr, "enforce:", err)
	os.Exit(1)
}

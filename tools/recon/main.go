// Command recon prints the console's planes against the ledger they are
// derived from. It is a reading tool, not a gate: the gate is a Go test.
package main

import (
	"fmt"

	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

func main() {
	month := world.LastFullMonth()
	type key struct{ src, svc, team string }
	byKey := map[key]money.Cents{}
	bySrcTeam := map[key]money.Cents{}
	byDesk := map[string]money.Cents{}
	for _, r := range world.Generate() {
		if len(r.Day) < 7 || r.Day[:7] != month {
			continue
		}
		byKey[key{r.Source, r.Service, r.Team}] += r.Billed
		bySrcTeam[key{r.Source, "", r.Team}] += r.Billed
		byDesk[r.Source] += r.Billed
	}
	fmt.Printf("Reconciling every plane against %s, the last full month.\n\n", month)

	fmt.Println("== UTILISATION: named resources against the line they sit in ==")
	sum := map[key]money.Cents{}
	count := map[key]int{}
	for _, u := range world.UtilisationRows {
		k := key{u.Source, u.Service, u.Team}
		sum[k] += u.Monthly
		count[k]++
	}
	bad := 0
	for k, v := range sum {
		if v != byKey[k] {
			bad++
			fmt.Printf("  MISMATCH %-38s resources %10s   line %10s\n",
				k.src+"/"+k.svc+"/"+k.team, v, byKey[k])
		}
	}
	fmt.Printf("  %d lines carved into %d resources; %d do not add up.\n",
		len(sum), len(world.UtilisationRows), bad)

	fmt.Println("\n== SAAS: seats issued at list price against the invoice ==")
	for _, l := range world.Licences {
		paid := money.Cents(l.Issued) * l.PerSeat
		bill := byKey[key{"saas", l.Vendor, l.Team}]
		fmt.Printf("  %-12s %-16s %3d seats x %8s = %10s   invoice %10s   off by %s\n",
			l.Vendor, l.Team, l.Issued, l.PerSeat, paid, bill, paid-bill)
	}

	fmt.Println("\n== COMMITMENTS: against the spend a commitment can actually cover ==")
	committable := map[string]money.Cents{}
	for k, v := range byKey {
		if world.ResourceKind(k.svc) != "" {
			committable[k.src] += v
		}
	}
	for _, c := range world.Commitments {
		monthly := c.Hourly * 730
		fmt.Printf("  %-32s %-6s %10s a month, %5.1f%% of %s committable (desk bill %s)\n",
			c.Name, c.Source, monthly,
			float64(monthly)/float64(committable[c.Source])*100,
			committable[c.Source], byDesk[c.Source])
	}
}

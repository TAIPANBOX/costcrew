package world_test

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// The invariant: every plane in this console is a reading of ONE ledger.
//
// Before this held, a figure on one page could not be checked against any
// other, and the failure was not subtle. Measured against the charges, every
// licence in the estate cost more per month than the entire SaaS bill for the
// team that held it: Figma read 2,025.00 a month against a real bill of
// 147.70. It looked like data because it was in a table.

type lineKey struct{ Source, Service, Team string }

func ledger(month string) (line map[lineKey]money.Cents, desk map[string]money.Cents) {
	line, desk = map[lineKey]money.Cents{}, map[string]money.Cents{}
	for _, r := range world.Generate() {
		if len(r.Day) < 7 || r.Day[:7] != month {
			continue
		}
		line[lineKey{r.Source, r.Service, r.Team}] += r.Billed
		desk[r.Source] += r.Billed
	}
	return line, desk
}

// The named resources under a line add up to the line, to the cent.
//
// Exactness is the point rather than pedantry: a rightsizing table whose rows
// do not add up to anything is a list of assertions, and the saving column on
// it cannot be checked either.
func TestEveryResourceAddsUpToItsLine(t *testing.T) {
	line, _ := ledger(world.LastFullMonth())
	sum := map[lineKey]money.Cents{}
	seen := map[string]bool{}
	for _, u := range world.UtilisationRows {
		k := lineKey{u.Source, u.Service, u.Team}
		sum[k] += u.Monthly
		if u.Monthly <= 0 {
			t.Errorf("%s costs %s, which is not a resource anybody is paying for", u.Resource, u.Monthly)
		}
		// An id that appears twice reads as one machine with two conflicting
		// recommendations.
		id := u.Source + "|" + u.Service + "|" + u.Team + "|" + u.Resource
		if seen[id] {
			t.Errorf("two resources share the id %q inside one line", u.Resource)
		}
		seen[id] = true
		if u.Saving > u.Monthly {
			t.Errorf("%s: saving %s is more than the %s it costs", u.Resource, u.Saving, u.Monthly)
		}
	}
	if len(sum) == 0 {
		t.Fatal("no lines were carved into resources, so this measured nothing")
	}
	for k, got := range sum {
		if want := line[k]; got != want {
			t.Errorf("%s / %s / %s: its resources total %s, the ledger says %s",
				k.Source, k.Service, k.Team, got, want)
		}
	}
}

// Seats issued at the vendor's list price equal what the invoice paid, to
// within the one seat that rounding to whole seats can move.
func TestEveryLicenceTiesToItsInvoice(t *testing.T) {
	line, _ := ledger(world.LastFullMonth())
	if len(world.Licences) == 0 {
		t.Fatal("no licences were derived, so this measured nothing")
	}
	for _, l := range world.Licences {
		paid := money.Cents(l.Issued) * l.PerSeat
		bill := line[lineKey{"saas", l.Vendor, l.Team}]
		if bill == 0 {
			t.Errorf("%s for %s has no SaaS invoice at all", l.Vendor, l.Team)
			continue
		}
		off := paid - bill
		if off < 0 {
			off = -off
		}
		if off > l.PerSeat {
			t.Errorf("%s / %s: %d seats at %s is %s, the invoice says %s, off by %s "+
				"which is more than one seat", l.Vendor, l.Team, l.Issued, l.PerSeat, paid, bill, off)
		}
		if l.Active30 > l.Issued {
			t.Errorf("%s / %s: %d seats signed in, %d issued", l.Vendor, l.Team, l.Active30, l.Issued)
		}
	}
}

// A commitment covers spend a commitment can actually cover, and does not
// exceed it. A savings plan sized against storage and egress would look
// prudent while covering nothing.
func TestNoCommitmentExceedsWhatItCanCover(t *testing.T) {
	line, _ := ledger(world.LastFullMonth())
	committable := map[string]money.Cents{}
	for k, v := range line {
		if world.ResourceKind(k.Service) != "" {
			committable[k.Source] += v
		}
	}
	if len(world.Commitments) == 0 {
		t.Fatal("no commitments were derived, so this measured nothing")
	}
	for _, c := range world.Commitments {
		monthly := c.Hourly * 730
		cover := committable[c.Source]
		if cover == 0 {
			t.Errorf("%s commits on the %s desk, which has nothing a commitment covers", c.Name, c.Source)
			continue
		}
		if monthly > cover {
			t.Errorf("%s: %s a month committed against %s of committable spend on %s",
				c.Name, monthly, cover, c.Source)
		}
		if c.Used < 0 || c.Used > 100 {
			t.Errorf("%s: %.1f%% used", c.Name, c.Used)
		}
	}
}

// AI units are the ledger's own AI rows, so their total is the AI desk's bill
// for that month and not a number beside it.
func TestAIUnitsTotalTheAIDesk(t *testing.T) {
	month := world.LastFullMonth()
	_, desk := ledger(month)
	var got money.Cents
	for _, u := range world.AIUnits() {
		if u.Month == month {
			got += u.Cost
		}
	}
	if got == 0 {
		t.Fatal("no AI units in the last full month, so this measured nothing")
	}
	// The AI desk also carries rows that are not metered in tokens or hours,
	// so the units are a subset and must never exceed the desk.
	if got > desk["ai"] {
		t.Errorf("AI units total %s, the AI desk billed %s", got, desk["ai"])
	}
}

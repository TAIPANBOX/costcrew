package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

func report(t *testing.T) string {
	t.Helper()
	var out bytes.Buffer
	if code := run(&out); code != 0 {
		t.Fatalf("run exited %d", code)
	}
	return out.String()
}

// The month the report names is the last full month of the generated ledger,
// and every plane is held against that month only.
func TestReportNamesTheMonthAndEveryPlane(t *testing.T) {
	out := report(t)
	for _, want := range []string{
		"Reconciling every plane against " + world.LastFullMonth() + ", the last full month.",
		"== UTILISATION: named resources against the line they sit in ==",
		"== SAAS: seats issued at list price against the invoice ==",
		"== COMMITMENTS: against the spend a commitment can actually cover ==",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report does not say %q:\n%s", want, out)
		}
	}
}

// The fixture's named resources add up, to the cent, to the line they sit
// in. The count in the report is recomputed here from the ledger by a
// different route (one pass over the rows, keyed by a joined string).
func TestUtilisationReconcilesAndCountsItsLines(t *testing.T) {
	month := world.LastFullMonth()
	ledger := map[string]money.Cents{}
	for _, r := range world.Generate() {
		if strings.HasPrefix(r.Day, month) {
			ledger[r.Source+"/"+r.Service+"/"+r.Team] += r.Billed
		}
	}
	lines := map[string]bool{}
	for _, u := range world.UtilisationRows {
		lines[u.Source+"/"+u.Service+"/"+u.Team] = true
	}
	out := report(t)
	want := fmt.Sprintf("%d lines carved into %d resources; 0 do not add up.",
		len(lines), len(world.UtilisationRows))
	if !strings.Contains(out, want) {
		t.Errorf("want %q in:\n%s", want, out)
	}
	if strings.Contains(out, "MISMATCH") {
		t.Errorf("the fixture reconciles, but the report names a mismatch:\n%s", out)
	}
	for k := range lines {
		if _, ok := ledger[k]; !ok {
			t.Errorf("resource line %s has no ledger line behind it", k)
		}
	}
}

// A resource whose figure is off by one cent is a line that does not add up.
// The report must name it and count it, and must name both figures.
func TestAResourceOffByACentIsNamedAsAMismatch(t *testing.T) {
	saved := world.UtilisationRows
	t.Cleanup(func() { world.UtilisationRows = saved })
	bent := append([]world.Utilisation(nil), saved...)
	bent[0].Monthly++
	world.UtilisationRows = bent

	out := report(t)
	key := bent[0].Source + "/" + bent[0].Service + "/" + bent[0].Team
	if !strings.Contains(out, "MISMATCH "+key) {
		t.Errorf("the bent line %s is not named:\n%s", key, out)
	}
	if !strings.Contains(out, "; 1 do not add up.") {
		t.Errorf("the count of lines that do not add up is not 1:\n%s", out)
	}
}

// Every licence appears with its seats, its list price, what that comes to,
// the invoice, and the difference with its sign.
func TestEveryLicenceShowsItsGapToTheInvoice(t *testing.T) {
	month := world.LastFullMonth()
	invoice := map[[2]string]money.Cents{}
	for _, r := range world.Generate() {
		if r.Source == "saas" && strings.HasPrefix(r.Day, month) {
			invoice[[2]string{r.Service, r.Team}] += r.Billed
		}
	}
	if len(world.Licences) == 0 {
		t.Fatal("no licences to reconcile; the test would assert nothing")
	}
	out := report(t)
	for _, l := range world.Licences {
		bill := invoice[[2]string{l.Vendor, l.Team}]
		paid := money.Cents(l.Issued) * l.PerSeat
		want := fmt.Sprintf("%d seats x %8s = %10s   invoice %10s   off by %s",
			l.Issued, l.PerSeat, paid, bill, paid-bill)
		if !strings.Contains(out, want) {
			t.Errorf("licence %s/%s: want %q in:\n%s", l.Vendor, l.Team, want, out)
		}
	}
	// The sign is part of the claim: seats at list price can sit under the
	// invoice as well as over it, and the report must say which.
	if !strings.Contains(out, "off by -") {
		t.Errorf("no licence sits under its invoice in this fixture, or the sign is lost:\n%s", out)
	}
}

// A commitment is shown as a monthly figure (hourly x 730) against the spend
// it can cover, as a percentage, beside the desk's whole bill.
func TestEveryCommitmentShowsItsShareOfCommittableSpend(t *testing.T) {
	month := world.LastFullMonth()
	committable := map[string]money.Cents{}
	desk := map[string]money.Cents{}
	for _, r := range world.Generate() {
		if !strings.HasPrefix(r.Day, month) {
			continue
		}
		desk[r.Source] += r.Billed
		if world.ResourceKind(r.Service) != "" {
			committable[r.Source] += r.Billed
		}
	}
	if len(world.Commitments) == 0 {
		t.Fatal("no commitments to reconcile; the test would assert nothing")
	}
	out := report(t)
	for _, c := range world.Commitments {
		monthly := c.Hourly * 730
		want := fmt.Sprintf("%10s a month, %5.1f%% of %s committable (desk bill %s)",
			monthly, float64(monthly)/float64(committable[c.Source])*100,
			committable[c.Source], desk[c.Source])
		if !strings.Contains(out, want) {
			t.Errorf("commitment %s: want %q in:\n%s", c.Name, want, out)
		}
	}
}

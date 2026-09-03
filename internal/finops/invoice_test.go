package finops_test

// InvoiceReconciliation is C2-SPEC.md section 2's own requirement: "the
// reconciliation of the period against the invoice to the cent" when
// charges.invoice_id exists, "else one sentence saying no invoice column is
// loaded" -- the sentence itself is the packet's job (internal/deliver), but
// the figures it is built from live here. Red against unchanged code
// because the function does not exist yet.

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

func TestInvoiceReconciliationSaysNoColumnIsLoadedWhenNoneIs(t *testing.T) {
	db := seeded(t)
	m := aMonth(t, db)
	invoices, uncovered, has, err := finops.InvoiceReconciliation(db, m)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatalf("the seeded, generated estate never sets invoice_id, but "+
			"InvoiceReconciliation reports %d invoice(s) loaded", len(invoices))
	}
	if len(invoices) != 0 {
		t.Errorf("%d invoice totals reported with no invoice data loaded", len(invoices))
	}
	a, err := finops.Allocate(db, m)
	if err != nil {
		t.Fatal(err)
	}
	if bill := a.Direct + a.Shared; uncovered != bill {
		t.Errorf("uncovered is %s, want the whole bill %s: with no invoice_id "+
			"anywhere, every cent is uncovered", uncovered, bill)
	}
}

func TestInvoiceReconciliationGroupsByInvoiceToTheCent(t *testing.T) {
	db := seeded(t)
	m := aMonth(t, db)
	day := m + "-11"
	mustExecArgs(t, db, `INSERT INTO charges (source, day, service, category, billed_cents, invoice_id)
		VALUES ('aws', ?, 'EC2', 'Usage', 10050, 'INV-1')`, day)
	mustExecArgs(t, db, `INSERT INTO charges (source, day, service, category, billed_cents, invoice_id)
		VALUES ('aws', ?, 'S3', 'Usage', 249, 'INV-1')`, day)
	mustExecArgs(t, db, `INSERT INTO charges (source, day, service, category, billed_cents, invoice_id)
		VALUES ('gcp', ?, 'BigQuery', 'Usage', 500, 'INV-2')`, day)

	invoices, uncovered, has, err := finops.InvoiceReconciliation(db, m)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("three rows carry an invoice_id, but InvoiceReconciliation says none is loaded")
	}
	byID := map[string]money.Cents{}
	for _, inv := range invoices {
		byID[inv.InvoiceID] = inv.Amount
	}
	if byID["INV-1"] != 10299 {
		t.Errorf("INV-1 totals %s, want 102.99", byID["INV-1"])
	}
	if byID["INV-2"] != 500 {
		t.Errorf("INV-2 totals %s, want 5.00", byID["INV-2"])
	}
	a, err := finops.Allocate(db, m)
	if err != nil {
		t.Fatal(err)
	}
	wantUncovered := a.Direct + a.Shared - 10299 - 500
	if uncovered != wantUncovered {
		t.Errorf("uncovered is %s, want %s (the whole bill minus the two invoices above)",
			uncovered, wantUncovered)
	}
}

// C2-SPEC.md section 4's own named mutant: "reconcile with float
// arithmetic (assert cents-exact on a fixture that breaks floats)". 29 and
// 57 cents are each individually exact enough in IEEE 754 float64 that a
// ROUND-HALF-UP float64 accumulator (dollars = cents/100.0, summed, cents
// back via +0.5 then truncate) still recovers them correctly one at a
// time -- this repository's own TestCostIsNeverParsedThroughFloat64
// (invariant 25) is about parsing a string through float64, a different
// failure mode, and would not go red on a fixture this small either. What
// actually breaks at this magnitude is a careless reimplementation that
// skips the rounding safety margin and truncates outright
// (int64(sum*100), no +0.5): summed as float64 dollars, 0.29+0.57 lands a
// hair under 0.86 and truncates to 85, not 86. Grouping still happens in
// SQL, in integer cents (InvoiceReconciliation's own doc comment), so this
// stays green on the real code; it is the regression test for a plausible
// rewrite that stopped doing that.
func TestInvoiceReconciliationIsCentsExactWhereFloatWouldLoseACent(t *testing.T) {
	db := seeded(t)
	m := aMonth(t, db)
	day := m + "-12"
	mustExecArgs(t, db, `INSERT INTO charges (source, day, service, category, billed_cents, invoice_id)
		VALUES ('aws', ?, 'EC2', 'Usage', 29, 'INV-FLOAT')`, day)
	mustExecArgs(t, db, `INSERT INTO charges (source, day, service, category, billed_cents, invoice_id)
		VALUES ('gcp', ?, 'BigQuery', 'Usage', 57, 'INV-FLOAT')`, day)

	invoices, _, has, err := finops.InvoiceReconciliation(db, m)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("two rows carry an invoice_id, but InvoiceReconciliation says none is loaded")
	}
	var got money.Cents
	found := false
	for _, inv := range invoices {
		if inv.InvoiceID == "INV-FLOAT" {
			got, found = inv.Amount, true
		}
	}
	if !found {
		t.Fatal("INV-FLOAT is not in the reconciliation at all")
	}
	if got != 86 {
		t.Errorf("INV-FLOAT totals %s, want 0.86: 29+57 cents summed through a naive "+
			"float64 dollar accumulator truncates to 85, which is exactly the "+
			"regression this fixture exists to catch", got)
	}
}

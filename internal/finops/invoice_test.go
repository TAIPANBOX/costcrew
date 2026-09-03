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

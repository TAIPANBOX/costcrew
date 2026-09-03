package finops

// Invoice reconciliation: C2-SPEC.md section 2. "the reconciliation of the
// period against the invoice to the cent" once charges.invoice_id exists,
// "else one sentence saying no invoice column is loaded" -- the sentence
// itself is the packet's own job (internal/deliver.closePackSection); this
// file is only the figures it is built from.
//
// invoice_id is never invented here: it is NULL unless a reader carried it
// across from a real FOCUS file's own optional InvoiceId column
// (internal/connectors's tokenfuse-focus reader is the one reader that can
// set it today; the generated, seeded estate never does).

import (
	"database/sql"
	"sort"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// InvoiceTotal is one invoice's own share of a period: its id, and the
// cents-exact sum of every charge row this store holds under it.
type InvoiceTotal struct {
	InvoiceID string
	Amount    money.Cents
}

// InvoiceReconciliation groups a period's charges by invoice_id.
// HasInvoiceData is false when not one row this period carries one, which is
// the caller's signal to print the "no invoice column is loaded" sentence
// rather than a reconciliation that has nothing to reconcile against.
// Uncovered is the period's own charges with NO invoice_id -- cents this
// console holds that no invoice ties back to -- and is meaningful only when
// HasInvoiceData is true: with no invoice data at all, Uncovered simply
// equals the whole bill, which is exactly what "no invoice column is
// loaded" already says in words.
//
// Grouped in SQL, in integer cents, never through a float: SQLite's SUM on
// an INTEGER column is exact, and every NULL invoice_id in the period
// collapses into the single "uncovered" group on its own, the same way SQL
// GROUP BY has always treated NULL as one group.
func InvoiceReconciliation(db *sql.DB, period string) (invoices []InvoiceTotal, uncovered money.Cents, hasInvoiceData bool, err error) {
	rows, err := db.Query(`SELECT invoice_id, SUM(billed_cents) FROM charges
		WHERE substr(day,1,7)=? GROUP BY invoice_id`, period)
	if err != nil {
		return nil, 0, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id sql.NullString
		var v int64
		if err := rows.Scan(&id, &v); err != nil {
			return nil, 0, false, err
		}
		if !id.Valid || id.String == "" {
			uncovered += money.Cents(v)
			continue
		}
		hasInvoiceData = true
		invoices = append(invoices, InvoiceTotal{InvoiceID: id.String, Amount: money.Cents(v)})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, err
	}
	// Deterministic order: invariant 7, "the same estate renders the same
	// way every time" -- GROUP BY's own row order is not guaranteed.
	sort.Slice(invoices, func(i, j int) bool { return invoices[i].InvoiceID < invoices[j].InvoiceID })
	return invoices, uncovered, hasInvoiceData, nil
}

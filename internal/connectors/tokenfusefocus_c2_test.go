package connectors

// C2-SPEC.md section 2: "add the column through the FOCUS reader if the
// FOCUS InvoiceId column is present in a file; absent otherwise." Red
// against unchanged code: InvoiceId is not read at all today, so
// charges.invoice_id (once it exists as a column at all -- see
// internal/estate's own SeedSchema) stays NULL even when a file carries one.

import (
	"database/sql"
	"strings"
	"testing"
)

func TestFocusReaderCarriesInvoiceIdWhenTheColumnIsPresent(t *testing.T) {
	dir := t.TempDir()
	header := focusHeader + ",InvoiceId"
	row := strings.Join(focusRowFields(), ",") + ",INV-C2-42"
	writeFocusFile(t, dir, "with-invoice.csv", header+"\n"+row)

	msg, db, err := importFrom(t, dir)
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	var got sql.NullString
	if err := db.QueryRow(`SELECT invoice_id FROM charges WHERE provenance='tokenfuse-focus'`).
		Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.String != "INV-C2-42" {
		t.Errorf("charges.invoice_id = %v, want INV-C2-42", got)
	}
}

// The golden fixture (fixtureCSV) has no InvoiceId column at all: a file
// with no such header is not refused (it is not in requiredFocusColumns),
// and every charges row it derives carries a NULL invoice_id, exactly as an
// installation that has never seen an InvoiceId column always has.
func TestFocusReaderLeavesInvoiceIdNullWhenTheColumnIsAbsent(t *testing.T) {
	dir := copyIntoDir(t, fixtureCSV)
	msg, db, err := importFrom(t, dir)
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM charges
		WHERE provenance='tokenfuse-focus' AND invoice_id IS NOT NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d charges row(s) carry a non-NULL invoice_id from a file with no InvoiceId column", n)
	}
}

func TestEnsureFocusSchemaAddsInvoiceIdColumns(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	// Simulate an installation from before this column existed: create the
	// two tables the OLD way (no invoice_id anywhere), then run the ensure
	// function and confirm both tables gained the column, safely, twice.
	if _, err := db.Exec(`CREATE TABLE charges(
		source TEXT, day TEXT, service TEXT, team TEXT, category TEXT,
		billed_cents INTEGER, quantity REAL, unit TEXT, meter TEXT, model TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := EnsureFocusSchema(db); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO charges (source, day, service, category, billed_cents, invoice_id)
		VALUES ('aws','2026-01-01','EC2','Usage',100,'INV-1')`); err != nil {
		t.Fatalf("charges.invoice_id does not exist after EnsureFocusSchema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO ai_calls
		(file_sha256, row_no, ts, day, agent, model, tokens_in, tokens_out,
		 billed_microusd, blocked, basis, invoice_id)
		VALUES ('x',1,'2026-01-01T00:00:00Z','2026-01-01','a','m',1,1,1,0,'settled','INV-1')`); err != nil {
		t.Fatalf("ai_calls.invoice_id does not exist after EnsureFocusSchema: %v", err)
	}
}

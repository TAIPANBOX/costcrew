package connectors

// C6-SPEC.md section 4, "the FOCUS reader's hostile suite reused": the same
// KIND of hostile input tokenfusefocus_test.go already exercises (a missing
// header column, a ragged row, a value that will not parse, a truncated
// gzip), applied to the saas-seats reader's own documented shape and its two
// domain-specific refusals (active greater than issued, a term of zero
// months).
//
// Red first, run against main before this file's own reader existed: every
// test below fails to COMPILE ("undefined: saasSeatsReader" is not
// reachable from outside the package, so the failure is really "no such
// connector" from Import itself -- readers["saas-seats"] is empty on main,
// and Import's own refusal is "no live account is connected to this
// installation, so there is nothing to read. The estate you are looking at
// is generated"). Verified by running this suite against a readers map with
// the "saas-seats" entry commented out: every test below fails with exactly
// that sentence.

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/store"
)

// fixtureSaasSeatsCSV is internal/connectors/testdata/saas-seats-2026-09-03.csv,
// a hand-built fixture (no vendor's real export -- the catalogue's own Note
// says there is no standard one to capture) with four vendor/product lines
// chosen to hit the boundaries C6-SPEC.md section 4 names: a renewal today,
// a renewal at exactly the ninety-day edge, one one day past it (so the
// calendar's own filter has something to exclude), and a line where issued
// equals active (zero waste).
const fixtureSaasSeatsCSV = "testdata/saas-seats-2026-09-03.csv"

func openSaasSeatsStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func configureSaasSeats(t *testing.T, db *sql.DB, dir string) {
	t.Helper()
	if err := Save(db, "saas-seats", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
}

// -------------------------------------------------------- the golden import

func TestSaasSeatsIsRead(t *testing.T) {
	st := openSaasSeatsStore(t)
	db := st.DB()
	dir := copyIntoDir(t, fixtureSaasSeatsCSV)
	configureSaasSeats(t, db, dir)

	msg, err := Import(db, "saas-seats", false, ImportOptions{Actor: "t.tester", Rec: st.AsRecorder()})
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	t.Logf("Import said: %s", msg)

	var total int
	var provenances int
	if err := db.QueryRow(`SELECT COUNT(*) FROM licences`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 4 {
		t.Errorf("licences has %d rows, want 4", total)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM licences WHERE provenance='imported'`).
		Scan(&provenances); err != nil {
		t.Fatal(err)
	}
	if provenances != 4 {
		t.Errorf("%d rows carry provenance='imported', want 4 (every imported row)", provenances)
	}

	var issued, active int
	var perSeat int64
	if err := db.QueryRow(`SELECT seats_issued, seats_active, per_seat_cents FROM licences
		WHERE vendor='Zendesk' AND product='Suite Professional'`).
		Scan(&issued, &active, &perSeat); err != nil {
		t.Fatal(err)
	}
	if issued != 60 || active != 42 || perSeat != 11500 {
		t.Errorf("Zendesk row = issued %d active %d perSeat %d, want 60 42 11500", issued, active, perSeat)
	}

	if !strings.Contains(msg, "4 row") {
		t.Errorf("Import's own sentence does not say 4 rows: %s", msg)
	}
}

// Import is re-run against the same folder: the row for each vendor/product
// converges rather than duplicating, because (vendor, product) is the
// table's own primary key.
func TestSaasSeatsReimportConverges(t *testing.T) {
	st := openSaasSeatsStore(t)
	db := st.DB()
	dir := copyIntoDir(t, fixtureSaasSeatsCSV)
	configureSaasSeats(t, db, dir)

	if _, err := Import(db, "saas-seats", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(db, "saas-seats", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM licences`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 4 {
		t.Errorf("licences has %d rows after two imports of the same file, want 4 (converged, not duplicated)", total)
	}
}

// C6-SPEC.md section 4's named mutant: "compute waste with floats". 29 idle
// seats at one cent each is 29 cents exactly in int64 arithmetic; a
// dollars-then-back-to-cents float64 round trip (idle*perSeat/100.0*100.0,
// truncated to an int64 the way a naive rewrite would do it) lands on
// 28.999999999999996 and truncates to 28. @measured, python3: `29/100*100`
// is `28.999999999999996`. This is why the fixture below uses one cent a
// seat rather than a rounder price: most cent amounts survive the same
// round trip exactly, and a mutant that only shows up on one value in a
// hundred is a mutant this case would miss by luck.
func TestSaasSeatsWasteIsCentsExactNotFloatRounded(t *testing.T) {
	f := saasSeatsRowFields()
	f[0], f[1] = "PennyTool", "Basic"
	f[2], f[3] = "30", "1" // SeatsIssued, SeatsActive: idle = 29
	f[5] = "1"             // MonthlyCents: one cent a seat
	dir := t.TempDir()
	writeSaasSeatsFile(t, dir, "penny.csv",
		strings.Join(saasSeatsHeaderFields(), ",")+"\n"+strings.Join(f, ","))
	msg, db, err := importSaasSeatsFrom(t, dir)
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	var issued, active int
	if err := db.QueryRow(`SELECT seats_issued, seats_active FROM licences
		WHERE vendor='PennyTool'`).Scan(&issued, &active); err != nil {
		t.Fatal(err)
	}
	if issued-active != 29 {
		t.Fatalf("this test's own fixture does not have 29 idle seats: %d-%d", issued, active)
	}
	if !strings.Contains(msg, "0.29 a month wasted") {
		t.Errorf("Import's own sentence does not say 0.29 wasted (29 cents, exact): %s", msg)
	}
}

// ----------------------------------------------------------- hostile input

// A minimal, otherwise-valid header and row, so a subtest can replace
// exactly the field it wants to make hostile.
func saasSeatsHeaderFields() []string {
	return []string{"Vendor", "Product", "SeatsIssued", "SeatsActive", "ActiveWindowDays",
		"MonthlyCents", "RenewalDate", "TermMonths", "NoticeDays"}
}

func saasSeatsRowFields() []string {
	return []string{"Zendesk", "Suite Professional", "60", "42", "30", "11500", "2026-09-03", "12", "15"}
}

func writeSaasSeatsFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func importSaasSeatsFrom(t *testing.T, dir string) (string, *sql.DB, error) {
	t.Helper()
	st := openSaasSeatsStore(t)
	db := st.DB()
	configureSaasSeats(t, db, dir)
	msg, err := Import(db, "saas-seats", false, ImportOptions{})
	return msg, db, err
}

func assertNoLicenceRows(t *testing.T, db *sql.DB) {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM licences`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d rows were written despite the refusal", n)
	}
}

func TestSaasSeatsHostileInput(t *testing.T) {
	t.Run("header missing SeatsIssued", func(t *testing.T) {
		var kept []string
		for _, f := range saasSeatsHeaderFields() {
			if f != "SeatsIssued" {
				kept = append(kept, f)
			}
		}
		dir := t.TempDir()
		// The header check runs before any row is ever read (processSaasSeatsFile
		// reads the header, validates it, and only then starts on rows), so
		// the row's own field count does not matter to this case; the
		// ordinary 9-field row is kept as-is.
		writeSaasSeatsFile(t, dir, "bad.csv",
			strings.Join(kept, ",")+"\n"+strings.Join(saasSeatsRowFields(), ","))
		msg, db, err := importSaasSeatsFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error rather than naming the file: %v", err)
		}
		if !strings.Contains(msg, "SeatsIssued") {
			t.Errorf("the refusal does not name the missing column: %s", msg)
		}
		assertNoLicenceRows(t, db)
	})

	t.Run("header carries an unknown column", func(t *testing.T) {
		header := append(append([]string{}, saasSeatsHeaderFields()...), "Currency")
		row := append(append([]string{}, saasSeatsRowFields()...), "USD")
		dir := t.TempDir()
		writeSaasSeatsFile(t, dir, "bad.csv", strings.Join(header, ",")+"\n"+strings.Join(row, ","))
		msg, db, err := importSaasSeatsFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error rather than naming the file: %v", err)
		}
		if !strings.Contains(msg, "unknown column") || !strings.Contains(msg, "Currency") {
			t.Errorf("the refusal does not name the unknown column: %s", msg)
		}
		assertNoLicenceRows(t, db)
	})

	t.Run("SeatsIssued not a number", func(t *testing.T) {
		f := saasSeatsRowFields()
		f[2] = "sixty"
		dir := t.TempDir()
		writeSaasSeatsFile(t, dir, "bad.csv",
			strings.Join(saasSeatsHeaderFields(), ",")+"\n"+strings.Join(f, ","))
		msg, db, err := importSaasSeatsFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "sixty") {
			t.Errorf("the refusal does not name the bad value: %s", msg)
		}
		assertNoLicenceRows(t, db)
	})

	// C6-SPEC.md section 4's own named hostile case.
	t.Run("active greater than issued", func(t *testing.T) {
		f := saasSeatsRowFields()
		f[2], f[3] = "10", "11" // SeatsIssued, SeatsActive
		dir := t.TempDir()
		writeSaasSeatsFile(t, dir, "bad.csv",
			strings.Join(saasSeatsHeaderFields(), ",")+"\n"+strings.Join(f, ","))
		msg, db, err := importSaasSeatsFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "greater than") {
			t.Errorf("the refusal does not say active exceeds issued: %s", msg)
		}
		assertNoLicenceRows(t, db)
	})

	// C6-SPEC.md section 4's own named hostile case.
	t.Run("term of zero months", func(t *testing.T) {
		f := saasSeatsRowFields()
		f[7] = "0" // TermMonths
		dir := t.TempDir()
		writeSaasSeatsFile(t, dir, "bad.csv",
			strings.Join(saasSeatsHeaderFields(), ",")+"\n"+strings.Join(f, ","))
		msg, db, err := importSaasSeatsFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "term of at least one month") {
			t.Errorf("the refusal does not name the zero-month term: %s", msg)
		}
		assertNoLicenceRows(t, db)
	})

	t.Run("renewal date does not parse", func(t *testing.T) {
		f := saasSeatsRowFields()
		f[6] = "next quarter"
		dir := t.TempDir()
		writeSaasSeatsFile(t, dir, "bad.csv",
			strings.Join(saasSeatsHeaderFields(), ",")+"\n"+strings.Join(f, ","))
		msg, db, err := importSaasSeatsFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "next quarter") {
			t.Errorf("the refusal does not name the bad date: %s", msg)
		}
		assertNoLicenceRows(t, db)
	})

	t.Run("negative notice days", func(t *testing.T) {
		f := saasSeatsRowFields()
		f[8] = "-5"
		dir := t.TempDir()
		writeSaasSeatsFile(t, dir, "bad.csv",
			strings.Join(saasSeatsHeaderFields(), ",")+"\n"+strings.Join(f, ","))
		msg, db, err := importSaasSeatsFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "-5") {
			t.Errorf("the refusal does not name the bad value: %s", msg)
		}
		assertNoLicenceRows(t, db)
	})

	t.Run("truncated gzip", func(t *testing.T) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		gz.Write([]byte(strings.Join(saasSeatsHeaderFields(), ",") + "\n" +
			strings.Join(saasSeatsRowFields(), ",") + "\n"))
		gz.Close()
		full := buf.Bytes()
		truncated := full[:len(full)-4]
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "bad.csv.gz"), truncated, 0o644); err != nil {
			t.Fatal(err)
		}
		msg, db, err := importSaasSeatsFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error rather than naming the file: %v", err)
		}
		if !strings.Contains(msg, "not read") {
			t.Errorf("the truncated file is not named as unread: %s", msg)
		}
		assertNoLicenceRows(t, db)
	})

	t.Run("a row with 11 columns against a 9-column header", func(t *testing.T) {
		f := saasSeatsRowFields()
		f = append(f, "extra1", "extra2")
		dir := t.TempDir()
		writeSaasSeatsFile(t, dir, "bad.csv",
			strings.Join(saasSeatsHeaderFields(), ",")+"\n"+strings.Join(f, ","))
		msg, db, err := importSaasSeatsFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		if !strings.Contains(msg, "field(s), header has 9") {
			t.Errorf("the refusal does not name the field-count mismatch: %s", msg)
		}
		assertNoLicenceRows(t, db)
	})

	// One good row and one bad row in the same file: the good one is
	// imported, the bad one is named -- the same property
	// tokenfusefocus_test.go already proves for FOCUS, at row grain rather
	// than file grain.
	t.Run("one good row beside one bad row", func(t *testing.T) {
		bad := saasSeatsRowFields()
		bad[0] = "OtherVendor"
		bad[3] = "999" // SeatsActive > SeatsIssued
		dir := t.TempDir()
		writeSaasSeatsFile(t, dir, "mixed.csv", strings.Join(saasSeatsHeaderFields(), ",")+"\n"+
			strings.Join(saasSeatsRowFields(), ",")+"\n"+strings.Join(bad, ","))
		msg, db, err := importSaasSeatsFrom(t, dir)
		if err != nil {
			t.Fatalf("Import returned a hard error: %v", err)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM licences`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%d rows written, want 1 (the good row alone)", n)
		}
		if !strings.Contains(msg, "OtherVendor") && !strings.Contains(msg, "greater than") {
			t.Errorf("the refusal does not name the bad row: %s", msg)
		}
	})
}

// Boundary: issued equal to active is not hostile, and it is not refused --
// zero waste is a valid, ordinary answer.
func TestSaasSeatsIssuedEqualsActiveIsZeroWasteNotRefused(t *testing.T) {
	f := saasSeatsRowFields()
	f[2], f[3] = "12", "12"
	dir := t.TempDir()
	writeSaasSeatsFile(t, dir, "ok.csv",
		strings.Join(saasSeatsHeaderFields(), ",")+"\n"+strings.Join(f, ","))
	msg, db, err := importSaasSeatsFrom(t, dir)
	if err != nil {
		t.Fatalf("Import: %v (%s)", err, msg)
	}
	var issued, active int
	if err := db.QueryRow(`SELECT seats_issued, seats_active FROM licences`).
		Scan(&issued, &active); err != nil {
		t.Fatal(err)
	}
	if issued != active {
		t.Fatalf("issued %d active %d, this test's own fixture is broken", issued, active)
	}
	if strings.Contains(msg, "refused") {
		t.Errorf("issued==active was refused, and it must not be: %s", msg)
	}
}

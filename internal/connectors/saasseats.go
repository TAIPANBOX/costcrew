package connectors

// The saas-seats reader: one documented CSV header per vendor export, into
// the licences table, provenance 'imported'. C6-SPEC.md section 1: "vendor
// seat and renewal data arrives as CSV the way budgets do (plan-before-write)".
//
// Unlike tokenfuse-focus, this reader touches no generated table: the
// generated licence estate (world.Licences) never reaches the store at all,
// it is computed in memory from the generated charges every time the SaaS
// page renders. So there is no "generated estate mixed with real money"
// refusal to make here -- the licences table holds imported rows or it holds
// nothing, and internal/finops.Licences is what tells the two apart for a
// page or a packet. What IS reused from tokenfuse-focus, the model of an
// intake: stream each file, never hold it whole in memory, a SAVEPOINT per
// file so a file that fails part way through contributes nothing, and a row
// refused by name rather than a file refused by silence.

import (
	"compress/gzip"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// ------------------------------------------------------------------ schema

// licenceSchema is its own table, not an ALTER on anything estate.SeedSchema
// already owns: a licence line is not a charge, and this practice's own
// renewal calendar has no generated counterpart in the store to sit beside.
const licenceSchema = `
CREATE TABLE IF NOT EXISTS licences(
  vendor TEXT NOT NULL, product TEXT NOT NULL,
  seats_issued INTEGER NOT NULL, seats_active INTEGER NOT NULL,
  active_window_days INTEGER NOT NULL, per_seat_cents INTEGER NOT NULL,
  renewal_date TEXT NOT NULL, term_months INTEGER NOT NULL, notice_days INTEGER NOT NULL,
  provenance TEXT NOT NULL,
  PRIMARY KEY (vendor, product));
`

// EnsureLicenceSchema is invariant 11's discipline applied to this reader:
// every startup step is a migration and every one runs on every start, so a
// store that has never imported anything still has a licences table to
// query. Called from cmd/costcrew/main.go on every start, unconditionally,
// the same way EnsureFocusSchema already is, because internal/finops.Licences
// reads this table on every SaaS page render and every renewals packet,
// whether or not this connector has ever been pointed at a folder.
func EnsureLicenceSchema(db *sql.DB) error {
	if _, err := db.Exec(licenceSchema); err != nil {
		return fmt.Errorf("creating licences: %w", err)
	}
	return nil
}

// requiredSaasSeatsColumns is the ONE documented header this reader accepts,
// by name, in any order. Unlike requiredFocusColumns (a subset of a much
// wider public standard, where a file may carry other columns this reader
// does not need), this format has no standard behind it -- the catalogue's
// own Note says so ("There is no standard here. Every vendor exports
// something different") -- so the header is closed: a column this reader
// does not know about is refused by name rather than silently ignored,
// because silently ignoring an extra column is how a mis-mapped export
// (the wrong vendor's file, an old template) gets read as though it matched.
var requiredSaasSeatsColumns = []string{
	"Vendor", "Product", "SeatsIssued", "SeatsActive", "ActiveWindowDays",
	"MonthlyCents", "RenewalDate", "TermMonths", "NoticeDays",
}

// saasSeatsHeaderIndex checks the header against requiredSaasSeatsColumns in
// both directions -- a required name absent, or a header name this reader
// does not know -- and refuses the WHOLE FILE, naming the reason, rather
// than reading what it can and staying silent about the rest.
func saasSeatsHeaderIndex(header []string) (map[string]int, error) {
	want := map[string]bool{}
	for _, c := range requiredSaasSeatsColumns {
		want[c] = true
	}
	col := map[string]int{}
	var unknown []string
	for i, h := range header {
		if !want[h] {
			unknown = append(unknown, h)
			continue
		}
		col[h] = i
	}
	var missing []string
	for _, c := range requiredSaasSeatsColumns {
		if _, ok := col[c]; !ok {
			missing = append(missing, c)
		}
	}
	if len(unknown) == 0 && len(missing) == 0 {
		return col, nil
	}
	var parts []string
	if len(unknown) > 0 {
		parts = append(parts, "unknown column(s): "+strings.Join(unknown, ", "))
	}
	if len(missing) > 0 {
		parts = append(parts, "missing required column(s): "+strings.Join(missing, ", "))
	}
	return nil, fmt.Errorf("the header does not match the documented saas-seats shape (%s): %s",
		strings.Join(requiredSaasSeatsColumns, ", "), strings.Join(parts, "; "))
}

// saasSeatsReader is registered in the readers map literal in connectors.go
// under "saas-seats".
func saasSeatsReader(db *sql.DB, cfg map[string]string, opt ImportOptions) (string, error) {
	if err := EnsureLicenceSchema(db); err != nil {
		return "", err
	}
	path := strings.TrimSpace(cfg["path"])
	if path == "" {
		return "", fmt.Errorf("no folder is configured; set the path and save before importing")
	}
	files, err := saasSeatsFiles(path)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("no *.csv or *.csv.gz files found in %s", path)
	}

	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var ins *sql.Stmt
	if !opt.DryRun {
		// A row is keyed on (vendor, product): re-importing the same export,
		// or a second export naming the same line, CONVERGES on whatever the
		// file most recently said rather than accumulating duplicate rows a
		// vendor+product grouping would then have to sum by accident.
		ins, err = tx.Prepare(`INSERT INTO licences
			(vendor, product, seats_issued, seats_active, active_window_days,
			 per_seat_cents, renewal_date, term_months, notice_days, provenance)
			VALUES (?,?,?,?,?,?,?,?,?,'imported')
			ON CONFLICT(vendor, product) DO UPDATE SET
			  seats_issued=excluded.seats_issued, seats_active=excluded.seats_active,
			  active_window_days=excluded.active_window_days,
			  per_seat_cents=excluded.per_seat_cents, renewal_date=excluded.renewal_date,
			  term_months=excluded.term_months, notice_days=excluded.notice_days,
			  provenance=excluded.provenance`)
		if err != nil {
			return "", err
		}
		defer ins.Close()
	}

	sum := newSaasSeatsSummary()
	for i, f := range files {
		// A SAVEPOINT per file, the same reason tokenfusefocus.go gives: a
		// file that fails part way through must contribute NOTHING, not
		// whatever row it got through before the failure.
		sp := fmt.Sprintf("saas_seats_file_%d", i)
		if !opt.DryRun {
			if _, err := tx.Exec("SAVEPOINT " + sp); err != nil {
				return "", err
			}
		}
		local, ferr := processSaasSeatsFile(f, ins)
		if ferr != nil {
			sum.FileRefusals = append(sum.FileRefusals,
				fmt.Sprintf("%s: %v", filepath.Base(f), ferr))
			if !opt.DryRun {
				if _, err := tx.Exec("ROLLBACK TO " + sp); err != nil {
					return "", err
				}
			}
			continue
		}
		sum.FilesRead++
		sum.merge(local)
		if !opt.DryRun {
			if _, err := tx.Exec("RELEASE " + sp); err != nil {
				return "", err
			}
		}
	}

	if !opt.DryRun {
		if err := tx.Commit(); err != nil {
			return "", err
		}
	}
	return sum.Sentence(opt.DryRun), nil
}

// -------------------------------------------------------------- the folder

func saasSeatsFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		lower := strings.ToLower(e.Name())
		if strings.HasSuffix(lower, ".csv") || strings.HasSuffix(lower, ".csv.gz") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	// Sorted, not directory order: the same determinism reason
	// tokenfusefocus.go's own focusFiles already gives (invariant 7).
	sort.Strings(out)
	return out, nil
}

// processSaasSeatsFile reads one file start to finish with csv.Reader, one
// record at a time. A file-level problem (the header does not match, the
// gzip will not open) returns an error and the caller rolls back this
// file's savepoint; a row-level problem is named in the returned summary and
// the file carries on.
func processSaasSeatsFile(path string, ins *sql.Stmt) (*saasSeatsSummary, error) {
	sum := newSaasSeatsSummary()

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var r io.Reader = f
	if strings.HasSuffix(strings.ToLower(path), ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("not a valid gzip file: %w", err)
		}
		defer gz.Close()
		r = gz
	}

	cr := csv.NewReader(r)
	// Field-count enforcement OFF, the same reason tokenfusefocus.go gives:
	// a ragged row is refused BY NAME, one row at a time, rather than
	// aborting the whole file.
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("reading the header: %w", err)
	}
	col, err := saasSeatsHeaderIndex(header)
	if err != nil {
		return nil, err
	}
	nCols := len(header)

	rowNo := 0
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("after row %d: %w", rowNo, err)
		}
		rowNo++

		if len(rec) != nCols {
			sum.refuse(fmt.Sprintf("%s row %d: %d field(s), header has %d",
				filepath.Base(path), rowNo, len(rec), nCols))
			continue
		}

		row, err := parseSaasSeatsRow(rec, col)
		if err != nil {
			sum.refuse(fmt.Sprintf("%s row %d: %v", filepath.Base(path), rowNo, err))
			continue
		}
		sum.accept(row)

		if ins == nil {
			continue
		}
		if _, err := ins.Exec(row.Vendor, row.Product, row.Issued, row.Active,
			row.ActiveWindowDays, row.PerSeatCents, row.RenewalDate,
			row.TermMonths, row.NoticeDays); err != nil {
			return nil, fmt.Errorf("row %d: writing to licences: %w", rowNo, err)
		}
	}
	return sum, nil
}

// -------------------------------------------------------------- row shape

type saasSeatsRow struct {
	Vendor, Product                  string
	Issued, Active, ActiveWindowDays int
	PerSeatCents                     int64
	RenewalDate                      string
	TermMonths, NoticeDays           int
}

// parseSaasSeatsRow validates and converts one already-aligned record into a
// saasSeatsRow, or names what was wrong. Every amount is parsed straight as
// an integer count of cents (strconv, never a float64 conversion anywhere in
// this file): the column is already in cents, so there is no decimal string
// to round on the way in, unlike tokenfuse-focus's own BilledCost.
func parseSaasSeatsRow(rec []string, col map[string]int) (saasSeatsRow, error) {
	field := func(name string) string {
		i, ok := col[name]
		if !ok || i < 0 || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	vendor := field("Vendor")
	if vendor == "" {
		return saasSeatsRow{}, fmt.Errorf("no vendor")
	}
	product := field("Product")
	if product == "" {
		return saasSeatsRow{}, fmt.Errorf("no product")
	}

	issuedStr := field("SeatsIssued")
	issued, err := strconv.Atoi(issuedStr)
	if err != nil || issued < 0 {
		return saasSeatsRow{}, fmt.Errorf("SeatsIssued %q is not a whole number of seats", issuedStr)
	}
	activeStr := field("SeatsActive")
	active, err := strconv.Atoi(activeStr)
	if err != nil || active < 0 {
		return saasSeatsRow{}, fmt.Errorf("SeatsActive %q is not a whole number of seats", activeStr)
	}
	// Hostile case C6-SPEC.md section 4 names directly: active seats cannot
	// exceed issued ones. Refused by name rather than clamped, because
	// clamping would print a number the file did not actually say.
	if active > issued {
		return saasSeatsRow{}, fmt.Errorf(
			"SeatsActive %d is greater than SeatsIssued %d", active, issued)
	}

	windowStr := field("ActiveWindowDays")
	window, err := strconv.Atoi(windowStr)
	if err != nil || window <= 0 {
		return saasSeatsRow{}, fmt.Errorf(
			"ActiveWindowDays %q is not a positive number of days", windowStr)
	}

	perSeatStr := field("MonthlyCents")
	perSeat, err := strconv.ParseInt(perSeatStr, 10, 64)
	if err != nil || perSeat < 0 {
		return saasSeatsRow{}, fmt.Errorf("MonthlyCents %q is not a whole number of cents", perSeatStr)
	}

	renews := field("RenewalDate")
	if _, err := time.Parse("2006-01-02", renews); err != nil {
		return saasSeatsRow{}, fmt.Errorf(
			"RenewalDate %q does not parse as a date; write it as 2026-11-01", renews)
	}

	// Hostile case C6-SPEC.md section 4 names directly: a term of zero
	// months. A vendor contract with no length is not a term this reader
	// can carry into a "shorter term" ask, so it is refused by name the same
	// way an impossible seat count is, rather than let a zero silently
	// reach the negotiation pack's own arithmetic.
	termStr := field("TermMonths")
	term, err := strconv.Atoi(termStr)
	if err != nil || term < 1 {
		return saasSeatsRow{}, fmt.Errorf(
			"TermMonths %q is not a term of at least one month", termStr)
	}

	noticeStr := field("NoticeDays")
	notice, err := strconv.Atoi(noticeStr)
	if err != nil || notice < 0 {
		return saasSeatsRow{}, fmt.Errorf("NoticeDays %q is not a whole number of days", noticeStr)
	}

	return saasSeatsRow{
		Vendor: vendor, Product: product,
		Issued: issued, Active: active, ActiveWindowDays: window,
		PerSeatCents: perSeat, RenewalDate: renews,
		TermMonths: term, NoticeDays: notice,
	}, nil
}

// ----------------------------------------------------------------- summary

// saasSeatsSummary is what both Test (DryRun) and Import build and report:
// the same sentence describes what WOULD happen and what DID.
type saasSeatsSummary struct {
	FilesRead    int
	RowsAccepted int
	Refusals     []string
	FileRefusals []string
	Vendors      map[string]bool
	IdleSeats    int
	WasteCents   money.Cents
}

func newSaasSeatsSummary() *saasSeatsSummary {
	return &saasSeatsSummary{Vendors: map[string]bool{}}
}

func (s *saasSeatsSummary) accept(row saasSeatsRow) {
	s.RowsAccepted++
	s.Vendors[row.Vendor] = true
	idle := row.Issued - row.Active
	s.IdleSeats += idle
	// Idle seats times the unit cost, cents exact: no float64 anywhere on
	// this path, the same discipline invariant 25 already holds for
	// ai_calls. Mutant (a) in C6-SPEC.md section 4 is exactly this line
	// rewritten through a float.
	s.WasteCents += money.Cents(int64(idle) * row.PerSeatCents)
}

func (s *saasSeatsSummary) refuse(reason string) { s.Refusals = append(s.Refusals, reason) }

func (s *saasSeatsSummary) merge(o *saasSeatsSummary) {
	s.RowsAccepted += o.RowsAccepted
	for v := range o.Vendors {
		s.Vendors[v] = true
	}
	s.IdleSeats += o.IdleSeats
	s.WasteCents += o.WasteCents
	s.Refusals = append(s.Refusals, o.Refusals...)
}

func (s *saasSeatsSummary) Sentence(dryRun bool) string {
	verb := "Read"
	if dryRun {
		verb = "Would read"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %d file%s, %d row%s, %d distinct vendor%s, %d idle seat%s, %s a month wasted.",
		verb, s.FilesRead, plural(s.FilesRead), s.RowsAccepted, plural(s.RowsAccepted),
		len(s.Vendors), plural(len(s.Vendors)), s.IdleSeats, plural(s.IdleSeats), s.WasteCents)
	if n := len(s.Refusals); n > 0 {
		verb2 := "refused"
		if dryRun {
			verb2 = "would be refused"
		}
		fmt.Fprintf(&b, " %d row%s %s: %s.", n, plural(n), verb2, strings.Join(s.Refusals, "; "))
	}
	if n := len(s.FileRefusals); n > 0 {
		fmt.Fprintf(&b, " %d file%s not read: %s.", n, plural(n), strings.Join(s.FileRefusals, "; "))
	}
	return b.String()
}

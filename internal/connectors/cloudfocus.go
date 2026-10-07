package connectors

// The plain-FOCUS folder readers: aws-data-exports and gcp-billing-export.
//
// Until these existed no cloud bill reached the crew. The only FOCUS reader,
// tokenfusefocus.go, requires the gateway's own x_ columns and refuses a
// plain cloud file by name (costcrew#68). This one reads what a cloud
// provider writes itself: a FOCUS Cost and Usage CSV or CSV.gz, in a folder.
// There is no S3 client and no BigQuery client in this binary. The folder is
// where somebody synced or extracted the export to.
//
// WHICH FOCUS, PINNED
//
// The columns below are the FOCUS 1.2 Cost and Usage dataset as
// focus.finops.org/docs/specification/v1-2/ defines them and as AWS Data
// Exports documents its table FOCUS_1_2_AWS (docs.aws.amazon.com/cur/latest/
// userguide/table-dictionary-focus-1-2-aws-columns.html), read 2026-10-07.
//
// FOCUS VERSIONS ACCEPTED
//
// The reader asks for columns by NAME, never for a version number, and the
// eight below exist unchanged from FOCUS 1.0 on, so it accepts a 1.0, 1.1 or
// 1.2 file. What was actually looked at:
//
//	1.0  a real AWS Data Exports FOCUS_1_0_AWS CSV.gz (48 columns, 451 rows,
//	     timestamps with milliseconds, no InvoiceId column), read 2026-10-07.
//	     Not committed: it is a private account's bill.
//	1.2  the spec and AWS's FOCUS_1_2_AWS dictionary; the committed fixture is
//	     hand-written from that column list. No real 1.2 file was read.
//	1.1  neither looked at nor needed: no column this reader uses changed.
//	1.3+ not looked at. ProviderName is deprecated there, so a file without it
//	     is refused by name as missing that column; nothing is guessed.
//
// Required in the header (a file without one is refused whole, by name):
//
//	BilledCost, BillingCurrency, ChargeCategory, ChargePeriodStart,
//	ChargePeriodEnd, ProviderName, ServiceName, SubAccountId
//
// All eight are in the 1.2 Cost and Usage dataset. SubAccountId is the one
// whose CELL may be empty (1.2 makes it nullable: AWS leaves it empty on a
// payer-level Tax row), so the column must exist and a row may leave it
// blank. ProviderName is the 1.2 column; FOCUS 1.3 deprecates it for
// ServiceProviderName and HostProviderName, which AWS's 1.2 table does not
// carry, so this reader does not ask for them.
//
// Accepted when present, never required:
//
//	ChargeClass  null, or "Correction" (the only non-null value 1.2 allows)
//	InvoiceId    carried to charges.invoice_id, as the gateway reader does
//	Tags         a JSON object (the 1.2 Key-Value Format); the team comes
//	             from one key of it, named by the team_tag setting
//	x_Tags       Google's BigQuery FOCUS view calls its tags x_Tags and makes
//	             them a repeated {Key, Value} record, which a flattened CSV
//	             carries as a JSON list; read as a fallback when Tags is absent
//
// Everything else in the file (EffectiveCost, ResourceId, RegionId, the
// pricing and commitment columns, AWS's x_ columns) is read by the CSV parser
// and ignored by this reader.
//
// WHAT IS NOT VERIFIED
//
// AWS's dictionary does not state the value of ProviderName; "AWS" is this
// reader's assumption, with "Amazon Web Services" also accepted. Google's page
// for its FOCUS export does not state it either; "Google Cloud" and "Google
// Cloud Platform" are assumptions. A row whose ProviderName is outside the
// accepted set is refused by name, saying so, and the provider_names setting
// replaces the set, so a wrong assumption costs one setting and not a
// release. No real export of either provider was available to measure
// against.
//
// WHAT A ROW BECOMES
//
// Every row lands in cloud_focus_rows, keyed (connector, file sha256, row
// number) so importing the same bytes twice changes nothing, with BilledCost
// kept exact in micros. charges is then rebuilt, per day this import touched,
// from that table: one row per (service, team, category, invoice) summed in
// micros and rounded to cents ONCE (money.Micros.Cents), exactly as the
// gateway reader does for ai_calls. Nothing is ever rounded per row.
//
// charges.provenance is the connector id, never NULL: NULL is what means
// "generated" everywhere in this console, so a real row can never be mistaken
// for the fixture and invariant 24 (no mixing) can tell them apart.
//
// All five FOCUS ChargeCategory values land in charges under their own name.
// The generated estate already uses Usage, Purchase, Tax and Credit for its
// shared costs, and every query that means "spend on resources" filters
// category='Usage', so a Purchase (a Savings Plan fee), a Tax or a Credit can
// never inflate usage, budgets or commitment eligibility. FOCUS lets
// BilledCost be negative (a credit, a refund) and does not forbid it on
// Usage; a negative amount is kept and counted in the sentence, never
// refused. This reader does NOT feed the commitments table: a Purchase row
// is money on the bill, not a commitment's coverage or utilisation, and
// deriving those from a cloud's discount columns is a separate piece of work.
//
// THE FOLDER IS NOT TRUSTED
//
// The same discipline as the gateway reader (stream with csv.Reader, USD only,
// RFC 3339, a SAVEPOINT per file, refuse a row by name rather than a file by
// silence), plus what that reader lacks: a byte cap on each file on disk
// (max_file_mb), a cap on what a gzip may inflate to (max_unpacked_mb), a cap
// on one CSV record (an unterminated quote would otherwise be read to the end
// of the file), no symlink is followed, and the list of refusals a sentence
// carries is bounded however many rows fail.

import (
	"compress/gzip"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// cloudSpec says what one registered connector is. The two share one engine
// because the file is the same shape; only the desk and the provider names
// differ.
type cloudSpec struct {
	id        string   // the connector id, also charges.provenance
	source    string   // the desk the rows are filed under
	providers []string // ProviderName values accepted by default
}

var awsDataExportsSpec = cloudSpec{
	id: "aws-data-exports", source: "aws",
	providers: []string{"AWS", "Amazon Web Services"},
}

var gcpBillingExportSpec = cloudSpec{
	id: "gcp-billing-export", source: "gcp",
	providers: []string{"Google Cloud", "Google Cloud Platform"},
}

var requiredCloudFocusColumns = []string{
	"BilledCost", "BillingCurrency", "ChargeCategory", "ChargePeriodStart",
	"ChargePeriodEnd", "ProviderName", "ServiceName", "SubAccountId",
}

// cloudChargeCategories is FOCUS 1.2's closed ChargeCategory enumeration, in
// the order a sentence lists them. The spec says the column MUST be one of
// these, so a sixth value is refused, not kept and flagged.
var cloudChargeCategories = []string{"Usage", "Purchase", "Tax", "Credit", "Adjustment"}

const (
	cloudDefaultMaxFileMB     = 4096  // on disk, per file
	cloudDefaultMaxUnpackedMB = 20480 // what one file may inflate to
	cloudMaxSettingMB         = 1 << 20

	cloudMaxRecordBytes  = 1 << 20  // one CSV record, quoted newlines included
	cloudMaxFieldBytes   = 1024     // ServiceName, SubAccountId, InvoiceId
	cloudMaxTagsBytes    = 64 << 10 // the Tags cell
	cloudMaxCostBytes    = 64
	cloudMaxTeamBytes    = 128
	cloudMaxFiles        = 50_000
	cloudMaxSubAccounts  = 10_000 // distinct ids tracked for the sentence
	cloudRefusalsShown   = 20
	cloudMaxCostDigits   = 9 // integer digits: under a billion in one row
	cloudMaxExponent     = 40
	cloudFirstYear       = 2000
	cloudLastYear        = 2099
	cloudMaxSettingItems = 8
	cloudMaxSpanDays     = 366
)

const cloudMaxSpan = cloudMaxSpanDays * 24 * time.Hour

// cloudFocusSchema is the raw rows. Exact micros, one table for both
// connectors (the connector column keeps them apart), and nothing the
// console does not read.
const cloudFocusSchema = `
CREATE TABLE IF NOT EXISTS cloud_focus_rows(
  connector TEXT NOT NULL, file_sha256 TEXT NOT NULL, row_no INTEGER NOT NULL,
  path TEXT NOT NULL,
  day TEXT NOT NULL, service TEXT NOT NULL, team TEXT,
  category TEXT NOT NULL, charge_class TEXT,
  billed_microusd INTEGER NOT NULL,
  invoice_id TEXT, sub_account_id TEXT,
  PRIMARY KEY (connector, file_sha256, row_no));
CREATE INDEX IF NOT EXISTS cloud_focus_rows_day ON cloud_focus_rows(connector, day);
CREATE INDEX IF NOT EXISTS cloud_focus_rows_path ON cloud_focus_rows(connector, path);
`

func cloudFocusReader(spec cloudSpec) Reader {
	return func(db *sql.DB, cfg map[string]string, opt ImportOptions) (string, error) {
		return readCloudFocus(spec, db, cfg, opt)
	}
}

// ------------------------------------------------------------------ settings

type cloudConfig struct {
	maxFile, maxUnpacked int64 // bytes
	maxFileMB            int64
	maxUnpackedMB        int64
	teamKeys             []string
	providers            map[string]bool // lower-cased
	providerList         string
}

func settingMB(cfg map[string]string, key string, def int64) (int64, error) {
	v := strings.TrimSpace(cfg[key])
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 1 || n > cloudMaxSettingMB {
		return 0, fmt.Errorf("%s %q must be a whole number of megabytes from 1 to %d; "+
			"a limit that cannot be read is refused rather than dropped", key, v, cloudMaxSettingMB)
	}
	return n, nil
}

func settingList(cfg map[string]string, key string, def []string) ([]string, error) {
	v := strings.TrimSpace(cfg[key])
	if v == "" {
		return def, nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if len(p) > cloudMaxTeamBytes {
			return nil, fmt.Errorf("%s has an entry over %d bytes", key, cloudMaxTeamBytes)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return def, nil
	}
	if len(out) > cloudMaxSettingItems {
		return nil, fmt.Errorf("%s names %d entries, at most %d", key, len(out), cloudMaxSettingItems)
	}
	return out, nil
}

func parseCloudConfig(spec cloudSpec, cfg map[string]string) (cloudConfig, error) {
	var c cloudConfig
	var err error
	if c.maxFileMB, err = settingMB(cfg, "max_file_mb", cloudDefaultMaxFileMB); err != nil {
		return c, err
	}
	if c.maxUnpackedMB, err = settingMB(cfg, "max_unpacked_mb", cloudDefaultMaxUnpackedMB); err != nil {
		return c, err
	}
	c.maxFile, c.maxUnpacked = c.maxFileMB<<20, c.maxUnpackedMB<<20
	if c.teamKeys, err = settingList(cfg, "team_tag", []string{"team"}); err != nil {
		return c, err
	}
	names, err := settingList(cfg, "provider_names", spec.providers)
	if err != nil {
		return c, err
	}
	c.providers = map[string]bool{}
	for _, n := range names {
		c.providers[strings.ToLower(n)] = true
	}
	c.providerList = strings.Join(names, ", ")
	return c, nil
}

// ------------------------------------------------------------------- reading

func readCloudFocus(spec cloudSpec, db *sql.DB, cfg map[string]string, opt ImportOptions) (string, error) {
	conf, err := parseCloudConfig(spec, cfg)
	if err != nil {
		return "", err
	}
	if err := EnsureFocusSchema(db); err != nil {
		return "", err
	}
	if _, err := db.Exec(cloudFocusSchema); err != nil {
		return "", fmt.Errorf("creating cloud_focus_rows: %w", err)
	}
	path := strings.TrimSpace(cfg["path"])
	if path == "" {
		return "", fmt.Errorf("no folder is configured; set the path and save before importing")
	}
	files, ignored, err := cloudFocusFiles(path)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		msg := fmt.Sprintf("no *.csv or *.csv.gz files found in %s", path)
		if ignored > 0 {
			msg += fmt.Sprintf("; %d .parquet, .zip or linked file(s) are there, and this reader reads "+
				"only CSV and CSV.gz (in AWS Data Exports choose text/csv with gzip)", ignored)
		}
		return "", errors.New(msg)
	}

	// Invariant 24: a generated estate is not mixed with real money. Checked
	// before anything is read, and not at all for Test, which writes nothing.
	var wipe bool
	if !opt.DryRun {
		mixed, err := hasGeneratedCharges(db)
		if err != nil {
			return "", err
		}
		if mixed && !opt.ReplaceGenerated {
			return "", errors.New(replaceGeneratedRefusal)
		}
		wipe = mixed
	}

	var tx *sql.Tx
	var w *cloudWriter
	if !opt.DryRun {
		tx, err = db.Begin()
		if err != nil {
			return "", err
		}
		defer tx.Rollback()
		if wipe {
			if err := replaceGeneratedEstate(tx); err != nil {
				return "", err
			}
		}
		ins, err := tx.Prepare(`INSERT INTO cloud_focus_rows
			(connector, file_sha256, row_no, path, day, service, team, category,
			 charge_class, billed_microusd, invoice_id, sub_account_id)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(connector, file_sha256, row_no) DO UPDATE SET
			  path=excluded.path, day=excluded.day, service=excluded.service,
			  team=excluded.team, category=excluded.category,
			  charge_class=excluded.charge_class, billed_microusd=excluded.billed_microusd,
			  invoice_id=excluded.invoice_id, sub_account_id=excluded.sub_account_id`)
		if err != nil {
			return "", err
		}
		defer ins.Close()
		w = &cloudWriter{tx: tx, ins: ins, connector: spec.id}
	}

	sum := newCloudSummary(conf)
	sum.Ignored = ignored
	touched := map[string]bool{}
	seen := map[string]bool{}
	for i, f := range files {
		if f.size > conf.maxFile {
			sum.FileRefusals = append(sum.FileRefusals, fmt.Sprintf(
				"%s: %d bytes on disk, over the %d MB limit (max_file_mb)", f.rel, f.size, conf.maxFileMB))
			continue
		}
		sha, err := sha256OfFile(f.abs)
		if err != nil {
			sum.FileRefusals = append(sum.FileRefusals, fmt.Sprintf("%s: hashing: %v", f.rel, err))
			continue
		}
		// The same bytes twice (a file copied beside itself) are one file:
		// their rows would collide on (sha256, row number) and the sentence
		// would count them twice.
		if seen[sha] {
			sum.Duplicates++
			continue
		}
		seen[sha] = true

		sp := fmt.Sprintf("cloud_focus_file_%d", i)
		if w != nil {
			if _, err := tx.Exec("SAVEPOINT " + sp); err != nil {
				return "", err
			}
		}
		local, stale, ferr := processCloudFile(spec, conf, f, sha, w)
		if ferr != nil {
			sum.FileRefusals = append(sum.FileRefusals, fmt.Sprintf("%s: %v", f.rel, ferr))
			if w != nil {
				if _, err := tx.Exec("ROLLBACK TO " + sp); err != nil {
					return "", err
				}
				if _, err := tx.Exec("RELEASE " + sp); err != nil {
					return "", err
				}
			}
			continue
		}
		if sum.AbsTotal > math.MaxInt64-local.AbsTotal {
			ferr = errors.New("the folder's amounts add up past what a 64-bit count of micro-dollars can hold")
		}
		if ferr != nil {
			sum.FileRefusals = append(sum.FileRefusals, fmt.Sprintf("%s: %v", f.rel, ferr))
			if w != nil {
				if _, err := tx.Exec("ROLLBACK TO " + sp); err != nil {
					return "", err
				}
				if _, err := tx.Exec("RELEASE " + sp); err != nil {
					return "", err
				}
			}
			continue
		}
		sum.FilesRead++
		sum.merge(local)
		for d := range local.Days {
			touched[d] = true
		}
		for _, d := range stale {
			touched[d] = true
		}
		if w != nil {
			if _, err := tx.Exec("RELEASE " + sp); err != nil {
				return "", err
			}
		}
	}

	if !opt.DryRun {
		if err := deriveCloudCharges(tx, spec, touched); err != nil {
			return "", err
		}
		if wipe && opt.Rec != nil {
			if err := opt.Rec.Emit("generated_estate_replaced", opt.Actor, "info", map[string]any{
				"connector": spec.id,
				"tables":    "charges (provenance IS NULL)," + strings.Join(generatedTables, ","),
			}, nil); err != nil {
				return "", fmt.Errorf("journaling the generated estate's replacement: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
	}
	return sum.Sentence(opt.DryRun), nil
}

type cloudFile struct {
	abs, rel string
	size     int64
}

// cloudFocusFiles walks the folder. An S3 sync leaves partition folders
// (BILLING_PERIOD=2026-09/...), so it recurses; it reads regular files only,
// so a symlink (to /dev/zero, say) is never opened. The count of what it
// passed over for being Parquet, zip or a link is returned so the sentence
// can say so: an operator who picked the wrong export format must be told,
// not shown an empty folder.
func cloudFocusFiles(dir string) ([]cloudFile, int, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, 0, fmt.Errorf("reading %s: %w", dir, err)
	}
	st, err := os.Stat(root)
	if err != nil {
		return nil, 0, fmt.Errorf("reading %s: %w", dir, err)
	}
	if !st.IsDir() {
		return nil, 0, fmt.Errorf("%s is a file, and this reader wants the folder the export was synced to", dir)
	}
	var files []cloudFile
	ignored := 0
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		lower := strings.ToLower(d.Name())
		if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			ignored++
			return nil
		}
		switch {
		case strings.HasSuffix(lower, ".csv") || strings.HasSuffix(lower, ".csv.gz"):
			if len(files) >= cloudMaxFiles {
				return fmt.Errorf("the folder holds more than %d CSV files", cloudMaxFiles)
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			files = append(files, cloudFile{abs: p, rel: filepath.ToSlash(rel), size: info.Size()})
		case strings.HasSuffix(lower, ".parquet") || strings.HasSuffix(lower, ".zip"):
			ignored++
		}
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("reading %s: %w", dir, err)
	}
	// Sorted: invariant 7 holds for what a folder produces, whatever order
	// the filesystem walks it in.
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	return files, ignored, nil
}

type cloudWriter struct {
	tx        *sql.Tx
	ins       *sql.Stmt
	connector string
}

// capReader fails with a NAMED error once more than left bytes have come out
// of the reader it wraps, instead of the silent EOF io.LimitReader gives.
// That EOF would make a cut-off gzip bomb look like a short, valid file.
type capReader struct {
	r    io.Reader
	left int64
	err  error
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		var one [1]byte
		n, err := c.r.Read(one[:])
		if n > 0 {
			return 0, c.err
		}
		return 0, err
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// recordReader bounds how much one csv.Reader.Read may consume. A record that
// opens a quote and never closes it is otherwise read, and held, to the end
// of the file. The caller resets it before every record.
type recordReader struct {
	r    io.Reader
	used int64
	max  int64
}

var errRecordTooLong = fmt.Errorf("a single record is longer than %d bytes (an unterminated quote?)", cloudMaxRecordBytes)

func (c *recordReader) reset() { c.used = 0 }
func (c *recordReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.used += int64(n)
	if c.used > c.max {
		return n, errRecordTooLong
	}
	return n, err
}

// processCloudFile reads one file start to finish, one record at a time. A
// row-level problem goes in the returned summary and the file carries on; a
// file-level problem returns an error and the caller rolls the file's
// savepoint back, so a refused file contributes nothing, not "whatever it got
// through before it broke". w is nil for Test.
//
// stale returns the days whose rows a successfully read file just replaced:
// an earlier file at the same path with different content (AWS revises a
// period by overwriting the export file in place). Those rows are removed so
// the period is not counted twice, and their days are rebuilt. This runs only
// after the new file was read to its end, so a half-synced revision that is
// refused leaves the earlier version standing.
func processCloudFile(spec cloudSpec, conf cloudConfig, f cloudFile, sha string, w *cloudWriter) (*cloudSummary, []string, error) {
	sum := newCloudSummary(conf)
	fh, err := os.Open(f.abs)
	if err != nil {
		return nil, nil, err
	}
	defer fh.Close()

	var r io.Reader = fh
	if strings.HasSuffix(strings.ToLower(f.abs), ".gz") {
		gz, err := gzip.NewReader(fh)
		if err != nil {
			return nil, nil, fmt.Errorf("not a valid gzip file: %w", err)
		}
		defer gz.Close()
		r = gz
	}
	capped := &capReader{r: r, left: conf.maxUnpacked, err: fmt.Errorf(
		"unpacked size is over the %d MB limit (max_unpacked_mb): a gzip bomb, or an export far "+
			"larger than expected", conf.maxUnpackedMB)}
	rec := &recordReader{r: capped, max: cloudMaxRecordBytes}

	cr := csv.NewReader(rec)
	// Field-count enforcement off: a ragged row is refused by name, one row,
	// instead of csv.ErrFieldCount ending the file.
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true

	rec.reset()
	header, err := cr.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("reading the header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		if i == 0 {
			h = strings.TrimPrefix(h, "\ufeff") // BigQuery and spreadsheets write one
		}
		h = strings.TrimSpace(h)
		if _, dup := col[h]; dup && h != "" {
			return nil, nil, fmt.Errorf("column %s appears twice in the header, so which one is meant cannot be told", h)
		}
		col[h] = i
	}
	var missing []string
	for _, want := range requiredCloudFocusColumns {
		if _, ok := col[want]; !ok {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return nil, nil, fmt.Errorf("missing required column(s): %s", strings.Join(missing, ", "))
	}
	tagCol, hasTags := col["Tags"]
	if !hasTags {
		tagCol, hasTags = col["x_Tags"]
	}
	nCols := len(header)

	rowNo := 0
	for {
		rec.reset()
		fields, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("after row %d: %w", rowNo, err)
		}
		rowNo++
		if len(fields) != nCols {
			sum.refuse(fmt.Sprintf("%s row %d: %d field(s), header has %d", f.rel, rowNo, len(fields), nCols))
			continue
		}
		row, err := parseCloudRow(conf, fields, col, tagCol, hasTags)
		if err != nil {
			sum.refuse(fmt.Sprintf("%s row %d: %v", f.rel, rowNo, err))
			continue
		}
		if err := sum.accept(row); err != nil {
			return nil, nil, fmt.Errorf("row %d: %w", rowNo, err)
		}
		if w == nil {
			continue
		}
		if _, err := w.ins.Exec(w.connector, sha, rowNo, f.rel, row.Day, row.Service,
			nullIfEmpty(row.Team), row.Category, nullIfEmpty(row.Class), int64(row.Micros),
			nullIfEmpty(row.Invoice), nullIfEmpty(row.SubAccount)); err != nil {
			return nil, nil, fmt.Errorf("row %d: writing to cloud_focus_rows: %w", rowNo, err)
		}
	}

	var stale []string
	if w != nil {
		rows, err := w.tx.Query(`SELECT DISTINCT day FROM cloud_focus_rows
			WHERE connector=? AND path=? AND file_sha256<>?`, w.connector, f.rel, sha)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var d string
			if err := rows.Scan(&d); err != nil {
				rows.Close()
				return nil, nil, err
			}
			stale = append(stale, d)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, nil, err
		}
		rows.Close()
		if _, err := w.tx.Exec(`DELETE FROM cloud_focus_rows
			WHERE connector=? AND path=? AND file_sha256<>?`, w.connector, f.rel, sha); err != nil {
			return nil, nil, err
		}
	}
	return sum, stale, nil
}

// ----------------------------------------------------------------- row shape

type cloudRow struct {
	Day, Service, Team, Category string
	Class, Invoice, SubAccount   string
	Micros                       money.Micros
	TeamFound, TagUnusable       bool
	Multiday                     bool
}

func fieldTooLong(name, v string) error {
	if len(v) > cloudMaxFieldBytes {
		return fmt.Errorf("%s is %d bytes, over the %d byte limit", name, len(v), cloudMaxFieldBytes)
	}
	return nil
}

func parseCloudRow(conf cloudConfig, rec []string, col map[string]int, tagCol int, hasTags bool) (cloudRow, error) {
	field := func(name string) string { return strings.TrimSpace(focusField(rec, col, name)) }
	var row cloudRow

	provider := field("ProviderName")
	if !conf.providers[strings.ToLower(provider)] {
		return row, fmt.Errorf("ProviderName %q is not one this connector reads (%s); if the export names "+
			"its provider differently, set provider_names", provider, conf.providerList)
	}
	currency := field("BillingCurrency")
	if currency != "USD" {
		return row, fmt.Errorf("currency %q, this reader is USD only", currency)
	}
	row.Category = field("ChargeCategory")
	valid := false
	for _, c := range cloudChargeCategories {
		if row.Category == c {
			valid = true
		}
	}
	if !valid {
		return row, fmt.Errorf("ChargeCategory %q is not one of Usage, Purchase, Tax, Credit, Adjustment", row.Category)
	}
	row.Class = field("ChargeClass")
	if row.Class != "" && row.Class != "Correction" {
		return row, fmt.Errorf("ChargeClass %q, FOCUS allows only empty or Correction", row.Class)
	}
	row.Service = field("ServiceName")
	if row.Service == "" {
		return row, fmt.Errorf("ServiceName is empty")
	}
	row.Invoice, row.SubAccount = field("InvoiceId"), field("SubAccountId")
	for _, c := range []struct{ n, v string }{
		{"ServiceName", row.Service}, {"InvoiceId", row.Invoice}, {"SubAccountId", row.SubAccount}} {
		if err := fieldTooLong(c.n, c.v); err != nil {
			return row, err
		}
	}

	costStr := field("BilledCost")
	micros, err := parseFocusDecimal(costStr)
	switch {
	case errors.Is(err, errDecimalRange):
		return row, fmt.Errorf("BilledCost %q is over %d digits before the point in one row", costStr, cloudMaxCostDigits)
	case err != nil:
		return row, fmt.Errorf("BilledCost %q does not parse as a decimal amount", costStr)
	}
	row.Micros = micros

	startStr, endStr := field("ChargePeriodStart"), field("ChargePeriodEnd")
	st, err := time.Parse(time.RFC3339, startStr)
	if err != nil {
		return row, fmt.Errorf("ChargePeriodStart %q does not parse as RFC 3339", startStr)
	}
	en, err := time.Parse(time.RFC3339, endStr)
	if err != nil {
		return row, fmt.Errorf("ChargePeriodEnd %q does not parse as RFC 3339", endStr)
	}
	st, en = st.UTC(), en.UTC()
	if y := st.Year(); y < cloudFirstYear || y > cloudLastYear {
		return row, fmt.Errorf("ChargePeriodStart %q is outside %d to %d", startStr, cloudFirstYear, cloudLastYear)
	}
	if !en.After(st) {
		return row, fmt.Errorf("ChargePeriodEnd %q is not after ChargePeriodStart %q (the end is exclusive)", endStr, startStr)
	}
	// A row lands whole on the day its charge period STARTS, as every other
	// row does. One longer than a day is kept, not refused: AWS writes a
	// month's Tax as one row for the whole billing period even in a DAILY
	// export (measured on a real FOCUS 1.0 file, 10 of 451 rows, 2026-10-07),
	// and refusing it would drop real money off the board. It is not spread
	// over its days, because charges are whole cents per day and an even split
	// loses up to half a cent on every day; it is counted in the sentence.
	span := en.Sub(st)
	if span > cloudMaxSpan {
		return row, fmt.Errorf("the charge period spans %s, over the %d day limit", span.Round(time.Hour), cloudMaxSpanDays)
	}
	row.Multiday = span > 24*time.Hour
	row.Day = st.Format("2006-01-02")

	if hasTags && tagCol < len(rec) {
		team, found, unusable, err := teamFromTags(rec[tagCol], conf.teamKeys)
		if err != nil {
			return row, err
		}
		row.Team, row.TeamFound, row.TagUnusable = team, found, unusable
	}
	return row, nil
}

var (
	errDecimalShape = errors.New("not a decimal amount")
	errDecimalRange = errors.New("amount out of range")
)

// parseFocusDecimal reads a FOCUS Decimal into exact micros. FOCUS 1.2's
// Numeric Format allows E notation ("35.2E-7"), which money.ParseMicros does
// not; the exponent is applied to the DIGITS, as text, so no float64 is ever
// involved, and the plain decimal that results goes through money.ParseMicros
// like every other amount in this console.
func parseFocusDecimal(s string) (money.Micros, error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > cloudMaxCostBytes {
		return 0, errDecimalShape
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp, err := strconv.Atoi(s[i+1:])
		if err != nil || exp > cloudMaxExponent || exp < -cloudMaxExponent {
			return 0, errDecimalShape
		}
		expanded, ok := expandExponent(s[:i], exp)
		if !ok {
			return 0, errDecimalShape
		}
		s = expanded
	}
	digits := strings.TrimLeft(s, "+-")
	intPart, _, _ := strings.Cut(digits, ".")
	if len(strings.TrimLeft(intPart, "0")) > cloudMaxCostDigits {
		// Checked on the text, before any multiplication: whole*1e6 wraps an
		// int64 silently for an integer part past about 9.2e12.
		if strings.Trim(intPart, "0123456789") == "" {
			return 0, errDecimalRange
		}
	}
	m, err := money.ParseMicros(s)
	if err != nil {
		return 0, errDecimalShape
	}
	return m, nil
}

// expandExponent turns mantissa and exponent into a plain decimal string:
// ("35.2", -7) is "0.00000352". The mantissa must be an optional '-', digits
// and at most one point.
func expandExponent(mant string, exp int) (string, bool) {
	neg := false
	if strings.HasPrefix(mant, "-") {
		neg, mant = true, mant[1:]
	} else if strings.HasPrefix(mant, "+") {
		mant = mant[1:]
	}
	intPart, frac, _ := strings.Cut(mant, ".")
	digits := intPart + frac
	if digits == "" || strings.Trim(digits, "0123456789") != "" || strings.Contains(frac, ".") {
		return "", false
	}
	point := len(intPart) + exp
	var out string
	switch {
	case point <= 0:
		out = "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		out = digits + strings.Repeat("0", point-len(digits))
	default:
		out = digits[:point] + "." + digits[point:]
	}
	if neg {
		out = "-" + out
	}
	return out, true
}

// teamFromTags finds the team in a Tags cell. FOCUS 1.2's Key-Value Format is
// a JSON object whose values are strings, numbers, booleans or null; Google's
// x_Tags is a list of {Key, Value} records. Only a non-empty string names a
// team: a valueless tag (true) or null is no team, and the row is shared
// cost. Keys are tried in the order the team_tag setting lists them, exact
// first, then ignoring case.
func teamFromTags(raw string, keys []string) (team string, found, unusable bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return "", false, false, nil
	}
	if len(raw) > cloudMaxTagsBytes {
		return "", false, false, fmt.Errorf("the Tags cell is %d bytes, over the %d byte limit", len(raw), cloudMaxTagsBytes)
	}
	strs := map[string]string{}
	switch raw[0] {
	case '{':
		var obj map[string]any
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			return "", false, false, fmt.Errorf("the Tags cell is not a JSON object: %v", err)
		}
		for k, v := range obj {
			if s, ok := v.(string); ok {
				strs[k] = s
			}
		}
	case '[':
		var list []struct {
			Key   string `json:"Key"`
			Value any    `json:"Value"`
		}
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			return "", false, false, fmt.Errorf("the Tags cell is not a JSON list of key/value records: %v", err)
		}
		for _, kv := range list {
			if s, ok := kv.Value.(string); ok {
				if _, dup := strs[kv.Key]; !dup {
					strs[kv.Key] = s
				}
			}
		}
	default:
		return "", false, false, fmt.Errorf("the Tags cell is not a JSON object")
	}
	names := make([]string, 0, len(strs))
	for k := range strs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, want := range keys {
		v, ok := strs[want]
		if !ok {
			for _, k := range names {
				if strings.EqualFold(k, want) {
					v, ok = strs[k], true
					break
				}
			}
		}
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if len(v) > cloudMaxTeamBytes || strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return "", false, true, nil
		}
		return v, true, false, nil
	}
	return "", false, false, nil
}

// ------------------------------------------------------------------- summary

type cloudSummary struct {
	conf         cloudConfig
	FilesRead    int
	Rows         int
	RefusedRows  int
	Refusals     []string // the first few only
	FileRefusals []string
	Days         map[string]bool
	FirstDay     string
	LastDay      string
	Subs         map[string]bool
	SubsMore     bool
	Total        money.Micros
	AbsTotal     uint64
	ByCategory   map[string]money.Micros
	Tagged       int
	TagUnusable  int
	Negative     int
	Multiday     int
	Corrections  int
	Duplicates   int
	Ignored      int
}

func newCloudSummary(conf cloudConfig) *cloudSummary {
	return &cloudSummary{conf: conf, Days: map[string]bool{}, Subs: map[string]bool{},
		ByCategory: map[string]money.Micros{}}
}

func (s *cloudSummary) refuse(reason string) {
	s.RefusedRows++
	if len(s.Refusals) < cloudRefusalsShown {
		s.Refusals = append(s.Refusals, reason)
	}
}

// accept folds one row in. The only error is an absolute total that would
// overflow int64, which no real bill reaches and a hostile file can: the
// file is then refused, rather than a wrapped number being summed into
// charges.
func (s *cloudSummary) accept(r cloudRow) error {
	abs := uint64(r.Micros)
	if r.Micros < 0 {
		abs = uint64(-r.Micros)
	}
	if s.AbsTotal > math.MaxInt64-abs {
		return fmt.Errorf("the sum of BilledCost overflows: this file's amounts add up past what a "+
			"64-bit count of micro-dollars can hold (%d)", int64(math.MaxInt64))
	}
	s.AbsTotal += abs
	s.Rows++
	s.Days[r.Day] = true
	if s.FirstDay == "" || r.Day < s.FirstDay {
		s.FirstDay = r.Day
	}
	if r.Day > s.LastDay {
		s.LastDay = r.Day
	}
	if r.SubAccount != "" {
		if len(s.Subs) < cloudMaxSubAccounts {
			s.Subs[r.SubAccount] = true
		} else if !s.Subs[r.SubAccount] {
			s.SubsMore = true
		}
	}
	s.Total += r.Micros
	s.ByCategory[r.Category] += r.Micros
	if r.TeamFound {
		s.Tagged++
	}
	if r.TagUnusable {
		s.TagUnusable++
	}
	if r.Micros < 0 {
		s.Negative++
	}
	if r.Multiday {
		s.Multiday++
	}
	if r.Class == "Correction" {
		s.Corrections++
	}
	return nil
}

func (s *cloudSummary) merge(o *cloudSummary) {
	s.Rows += o.Rows
	s.RefusedRows += o.RefusedRows
	for _, r := range o.Refusals {
		if len(s.Refusals) < cloudRefusalsShown {
			s.Refusals = append(s.Refusals, r)
		}
	}
	for d := range o.Days {
		s.Days[d] = true
	}
	if o.FirstDay != "" && (s.FirstDay == "" || o.FirstDay < s.FirstDay) {
		s.FirstDay = o.FirstDay
	}
	if o.LastDay > s.LastDay {
		s.LastDay = o.LastDay
	}
	for a := range o.Subs {
		if len(s.Subs) < cloudMaxSubAccounts {
			s.Subs[a] = true
		} else if !s.Subs[a] {
			s.SubsMore = true
		}
	}
	s.SubsMore = s.SubsMore || o.SubsMore
	s.Total += o.Total
	s.AbsTotal += o.AbsTotal
	for c, m := range o.ByCategory {
		s.ByCategory[c] += m
	}
	s.Tagged += o.Tagged
	s.TagUnusable += o.TagUnusable
	s.Negative += o.Negative
	s.Multiday += o.Multiday
	s.Corrections += o.Corrections
}

// Sentence is what both Test and Import report: the same words for what
// would happen and what did, because a describe-only pass that drifts from
// the real one is worse than none.
func (s *cloudSummary) Sentence(dryRun bool) string {
	verb := "Read"
	if dryRun {
		verb = "Would read"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %d file%s", verb, s.FilesRead, plural(s.FilesRead))
	if s.FirstDay != "" {
		fmt.Fprintf(&b, ", %s to %s", s.FirstDay, s.LastDay)
	}
	more := ""
	if s.SubsMore {
		more = "+"
	}
	fmt.Fprintf(&b, ", %d row%s, %d%s sub account%s, %s net BilledCost",
		s.Rows, plural(s.Rows), len(s.Subs), more, plural(len(s.Subs)), exactMicros(s.Total))
	var parts []string
	for _, c := range cloudChargeCategories {
		if m, ok := s.ByCategory[c]; ok {
			parts = append(parts, fmt.Sprintf("%s %s", c, exactMicros(m)))
		}
	}
	if len(parts) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(parts, ", "))
	}
	b.WriteString(".")
	if s.Rows > 0 {
		fmt.Fprintf(&b, " %d of %d row%s carry the team tag (%s); %d have none and land in the shared pot.",
			s.Tagged, s.Rows, plural(s.Rows), strings.Join(s.conf.teamKeys, ", "), s.Rows-s.Tagged)
	}
	if n := s.TagUnusable; n > 0 {
		fmt.Fprintf(&b, " %d team tag value%s too long or not text, left out.", n, plural(n))
	}
	if n := s.Negative; n > 0 {
		have := "have"
		if n == 1 {
			have = "has"
		}
		fmt.Fprintf(&b, " %d row%s %s a negative BilledCost (credits and refunds, kept).", n, plural(n), have)
	}
	if n := s.Multiday; n > 0 {
		cover, land := "cover", "land"
		if n == 1 {
			cover, land = "covers", "lands"
		}
		fmt.Fprintf(&b, " %d row%s %s more than one day and %s whole on the day it starts.", n, plural(n), cover, land)
	}
	if n := s.Corrections; n > 0 {
		fmt.Fprintf(&b, " %d correction%s to an earlier invoiced period (ChargeClass Correction, kept on the day it names).", n, plural(n))
	}
	if n := s.Duplicates; n > 0 {
		fmt.Fprintf(&b, " %d file%s repeated an earlier file's bytes and %s counted once.", n, plural(n), map[bool]string{true: "was", false: "were"}[n == 1])
	}
	if n := s.Ignored; n > 0 {
		fmt.Fprintf(&b, " %d file%s ignored (Parquet, zip or a link: this reader reads CSV and CSV.gz).", n, plural(n))
	}
	if n := s.RefusedRows; n > 0 {
		verb2 := "refused"
		if dryRun {
			verb2 = "would be refused"
		}
		shown := ""
		if n > len(s.Refusals) {
			shown = fmt.Sprintf(" (first %d shown)", len(s.Refusals))
		}
		fmt.Fprintf(&b, " %d row%s %s%s: %s.", n, plural(n), verb2, shown, strings.Join(s.Refusals, "; "))
	}
	if n := len(s.FileRefusals); n > 0 {
		fmt.Fprintf(&b, " %d file%s not read: %s.", n, plural(n), strings.Join(s.FileRefusals, "; "))
	}
	return b.String()
}

// exactMicros prints an amount the way money.Micros.String does, except under
// a cent, where it prints all six decimals: String shows four, so a real
// -0.000005 would read "-0.0000", which looks like nothing and is not.
func exactMicros(m money.Micros) string {
	if m == 0 || m <= -10_000 || m >= 10_000 {
		return m.String()
	}
	if m < 0 {
		return fmt.Sprintf("-0.%06d", -int64(m))
	}
	return fmt.Sprintf("0.%06d", int64(m))
}

// ------------------------------------------------------------- derived rows

// deriveCloudCharges rebuilds the days this import touched from the raw
// table, so a re-import changes nothing and two files touching one day
// converge. Per day: delete this connector's charges, then one row per
// (service, team, category, invoice), the SUM taken in micros and rounded to
// cents once. A group whose micros are exactly zero carries no money and
// writes no row (a free-tier line is not a charge); one that only rounds to
// zero cents is kept, so sub-cent money is never hidden from the table.
func deriveCloudCharges(tx *sql.Tx, spec cloudSpec, touched map[string]bool) error {
	days := sortedDays(touched)
	if len(days) == 0 {
		return nil
	}
	del, err := tx.Prepare(`DELETE FROM charges WHERE provenance=? AND day=?`)
	if err != nil {
		return err
	}
	defer del.Close()
	ins, err := tx.Prepare(`INSERT INTO charges
		(source, day, service, team, category, billed_cents, invoice_id, provenance)
		VALUES (?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer ins.Close()

	for _, d := range days {
		if _, err := del.Exec(spec.id, d); err != nil {
			return fmt.Errorf("clearing %s's derived charges: %w", d, err)
		}
		rows, err := tx.Query(`SELECT service, COALESCE(team,''), category,
				COALESCE(invoice_id,''), SUM(billed_microusd)
			FROM cloud_focus_rows WHERE connector=? AND day=?
			GROUP BY service, COALESCE(team,''), category, COALESCE(invoice_id,'')
			ORDER BY 1,2,3,4`, spec.id, d)
		if err != nil {
			return err
		}
		type grp struct {
			service, team, category, invoice string
			micros                           int64
		}
		var groups []grp
		for rows.Next() {
			var g grp
			if err := rows.Scan(&g.service, &g.team, &g.category, &g.invoice, &g.micros); err != nil {
				rows.Close()
				return err
			}
			groups = append(groups, g)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, g := range groups {
			if g.micros == 0 {
				continue
			}
			cents := money.Micros(g.micros).Cents() // the one rounding, after the sum
			if _, err := ins.Exec(spec.source, d, g.service, nullIfEmpty(g.team), g.category,
				int64(cents), nullIfEmpty(g.invoice), spec.id); err != nil {
				return fmt.Errorf("writing the %s/%s charges row for %s: %w", g.service, g.category, d, err)
			}
		}
	}
	return nil
}

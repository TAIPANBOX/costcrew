package connectors

// C5-SPEC.md: the providers already publish their own rightsizing and idle
// recommendations for free -- AWS Cost Explorer's Rightsizing
// Recommendations CSV, a GCP Recommender export, Azure Advisor's cost
// recommendations CSV -- and this file is the intake for all three, into
// one shared `recommendations` table. One reader per provider format
// because each publishes a different column set for the same idea; one
// shared engine underneath (runRightsizingImport, processRecommendationFile)
// because the CSV mechanics -- stream the file, refuse a bad row by name,
// never hold the whole thing in memory -- are the same mechanics
// tokenfusefocus.go already established as the shape a second reader
// reuses.
//
// A recommendation ROW here is a current snapshot, not a log: the primary
// key is desk+resource, and a re-import upserts it. Unlike the FOCUS
// reader's ai_calls (an append-only ledger keyed by file hash and row
// number, because a call happened once and stays true forever), a
// provider's rightsizing recommendation for one resource is a single
// standing fact that supersedes whatever the last import said about the
// same resource.

import (
	"bufio"
	"bytes"
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

const recommendationsSchema = `
CREATE TABLE IF NOT EXISTS recommendations(
  id TEXT PRIMARY KEY, provider TEXT NOT NULL, desk TEXT NOT NULL,
  resource TEXT NOT NULL, action TEXT NOT NULL,
  current TEXT NOT NULL, recommended TEXT NOT NULL,
  monthly_saving_cents INTEGER NOT NULL, lookback_days INTEGER NOT NULL,
  source_file TEXT NOT NULL, imported_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS recommendations_desk ON recommendations(desk);
`

// EnsureRecommendationsSchema is invariant 11's discipline applied to this
// table, the same shape EnsureFocusSchema already holds for ai_calls: every
// startup step is a migration and every one runs on every start, so a store
// from before this step gets the table without anybody asking for it, and
// running it again changes nothing. Called from cmd/costcrew/main.go
// unconditionally, and from each reader itself, so a test store that only
// ever calls Import or Test still gets it.
func EnsureRecommendationsSchema(db *sql.DB) error {
	if _, err := db.Exec(recommendationsSchema); err != nil {
		return fmt.Errorf("creating recommendations: %w", err)
	}
	return nil
}

// Recommendation is one row of recommendations, read back for a page or a
// packet section to rank and render.
type Recommendation struct {
	ID, Provider, Desk                     string
	Resource, Action, Current, Recommended string
	MonthlySavingCents                     money.Cents
	LookbackDays                           int
	SourceFile, ImportedAt                 string
}

// Recommendations reads every row on one desk, in a stable base order
// (resource, ascending). Ranking by saving is a READER's own concern --
// deliver.recommendationsSection ranks one way, a page could rank another
// -- so this hands back a deterministic order and leaves ranking to the
// caller, the same separation internal/finops.Supervise draws between
// reading options and ranking them "by saving then risk".
func Recommendations(db *sql.DB, desk string) ([]Recommendation, error) {
	rows, err := db.Query(`SELECT id, provider, desk, resource, action, current, recommended,
			monthly_saving_cents, lookback_days, source_file, imported_at
		FROM recommendations WHERE desk=? ORDER BY resource`, desk)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Recommendation
	for rows.Next() {
		var r Recommendation
		var cents int64
		if err := rows.Scan(&r.ID, &r.Provider, &r.Desk, &r.Resource, &r.Action, &r.Current,
			&r.Recommended, &cents, &r.LookbackDays, &r.SourceFile, &r.ImportedAt); err != nil {
			return nil, err
		}
		r.MonthlySavingCents = money.Cents(cents)
		out = append(out, r)
	}
	return out, rows.Err()
}

// RankBySaving sorts recs in place by MonthlySavingCents, descending, with
// Resource as an ascending tie-break, so the same estate renders the same
// list every time (invariant 7) rather than on map, slice or file order.
// Recommendations itself stays order-agnostic on purpose (see its own
// comment above): a future caller wanting a different order is free to
// write one. But deliver.recommendationsSection and web's /rightsizing
// page both want exactly THIS order, and used to each carry their own
// separately-maintained copy of the comparator -- coordinator review of
// PR #34, 2026-09-03, found that scripts/gates-have-teeth.sh's own "rank by
// current cost" case only ever mutated one of the two copies (deliver's),
// so a mutation to the page's own copy compiled clean and passed the whole
// internal/web suite: two callers wanting the same order is not a reason
// for two copies of the comparator, so now there is one, and one gate on
// it protects both.
func RankBySaving(recs []Recommendation) {
	sort.SliceStable(recs, func(i, j int) bool {
		if recs[i].MonthlySavingCents != recs[j].MonthlySavingCents {
			return recs[i].MonthlySavingCents > recs[j].MonthlySavingCents
		}
		return recs[i].Resource < recs[j].Resource
	})
}

// ---------------------------------------------------------------- catalogue

var requiredAWSRightsizingColumns = []string{
	"AccountId", "InstanceId", "Region", "CurrentInstanceType", "RecommendedInstanceType",
	"RightsizingType", "EstimatedMonthlySavings", "CurrencyCode", "LookbackPeriodInDays",
}

var requiredGCPRecommenderColumns = []string{
	"project_id", "resource", "recommender_subtype", "current_machine_type",
	"recommended_machine_type", "monthly_cost_savings", "currency_code", "observation_period_days",
}

var requiredAzureAdvisorColumns = []string{
	"SubscriptionId", "ImpactedResource", "Category", "RecommendationType",
	"CurrentSku", "RecommendedSku", "PotentialAnnualCostSavings", "Currency", "LookbackDays",
}

// awsRightsizingReader, gcpRecommenderReader and azureAdvisorReader are
// registered in the readers map literal in connectors.go under
// "aws-rightsizing", "gcp-recommender" and "azure-advisor".
func awsRightsizingReader(db *sql.DB, cfg map[string]string, opt ImportOptions) (string, error) {
	return runRightsizingImport(db, cfg, opt, "aws", requiredAWSRightsizingColumns, parseAWSRightsizingRow)
}

func gcpRecommenderReader(db *sql.DB, cfg map[string]string, opt ImportOptions) (string, error) {
	return runRightsizingImport(db, cfg, opt, "gcp", requiredGCPRecommenderColumns, parseGCPRecommenderRow)
}

func azureAdvisorReader(db *sql.DB, cfg map[string]string, opt ImportOptions) (string, error) {
	return runRightsizingImport(db, cfg, opt, "azure", requiredAzureAdvisorColumns, parseAzureAdvisorRow)
}

// -------------------------------------------------------------- row shape

// recRow is one row already validated and converted, before it is written.
// Current and Recommended are the resource's SIZE (an instance type, a
// machine type, a SKU), never a cost -- the mission this connector serves
// is "propose the smaller size with the saving attached"
// (roles.yaml, family optimizer), and the saving is carried separately.
type recRow struct {
	Resource, Action, Current, Recommended string
	MonthlySavingCents                     money.Cents
	LookbackDays                           int
}

// recRowParser turns one already-column-aligned CSV record into a recRow, or
// names what was wrong with it -- the same shape parseFocusRow already
// established, one per provider because AWS, GCP and Azure each publish a
// different column set for the same idea.
type recRowParser func(rec []string, col map[string]int) (recRow, error)

func rowField(rec []string, col map[string]int, name string) string {
	i, ok := col[name]
	if !ok || i < 0 || i >= len(rec) {
		return ""
	}
	return rec[i]
}

// parseAWSRightsizingRow reads Cost Explorer's own Rightsizing
// Recommendations CSV columns (documented at
// https://docs.aws.amazon.com/cost-management/latest/userguide/ce-rightsizing.html,
// `@claude`, not measured against a real export).
func parseAWSRightsizingRow(rec []string, col map[string]int) (recRow, error) {
	field := func(name string) string { return strings.TrimSpace(rowField(rec, col, name)) }

	currency := field("CurrencyCode")
	if currency != "USD" {
		return recRow{}, fmt.Errorf("currency %q, this reader is USD only", currency)
	}
	savingStr := field("EstimatedMonthlySavings")
	saving, err := money.Parse(savingStr)
	if err != nil {
		return recRow{}, fmt.Errorf("EstimatedMonthlySavings %q does not parse as a decimal amount", savingStr)
	}
	if saving < 0 {
		return recRow{}, fmt.Errorf("EstimatedMonthlySavings %q is negative", savingStr)
	}
	resource := field("InstanceId")
	if resource == "" {
		return recRow{}, fmt.Errorf("no InstanceId")
	}
	lookbackStr := field("LookbackPeriodInDays")
	lookback, err := strconv.Atoi(lookbackStr)
	if err != nil || lookback < 0 {
		return recRow{}, fmt.Errorf("LookbackPeriodInDays %q is not a non-negative integer", lookbackStr)
	}
	return recRow{
		Resource:           resource,
		Action:             field("RightsizingType"),
		Current:            field("CurrentInstanceType"),
		Recommended:        field("RecommendedInstanceType"),
		MonthlySavingCents: saving,
		LookbackDays:       lookback,
	}, nil
}

// parseGCPRecommenderRow reads a Recommender export's own columns
// (google.compute.instance.MachineTypeRecommender and its neighbours,
// documented at
// https://cloud.google.com/recommender/docs/machine-type-recommendations,
// `@claude`, not measured against a real export).
func parseGCPRecommenderRow(rec []string, col map[string]int) (recRow, error) {
	field := func(name string) string { return strings.TrimSpace(rowField(rec, col, name)) }

	currency := field("currency_code")
	if currency != "USD" {
		return recRow{}, fmt.Errorf("currency %q, this reader is USD only", currency)
	}
	savingStr := field("monthly_cost_savings")
	saving, err := money.Parse(savingStr)
	if err != nil {
		return recRow{}, fmt.Errorf("monthly_cost_savings %q does not parse as a decimal amount", savingStr)
	}
	if saving < 0 {
		return recRow{}, fmt.Errorf("monthly_cost_savings %q is negative", savingStr)
	}
	resource := field("resource")
	if resource == "" {
		return recRow{}, fmt.Errorf("no resource")
	}
	lookbackStr := field("observation_period_days")
	lookback, err := strconv.Atoi(lookbackStr)
	if err != nil || lookback < 0 {
		return recRow{}, fmt.Errorf("observation_period_days %q is not a non-negative integer", lookbackStr)
	}
	return recRow{
		Resource:           resource,
		Action:             field("recommender_subtype"),
		Current:            field("current_machine_type"),
		Recommended:        field("recommended_machine_type"),
		MonthlySavingCents: saving,
		LookbackDays:       lookback,
	}, nil
}

// parseAzureAdvisorRow reads Advisor's own cost recommendations CSV
// columns (documented at
// https://learn.microsoft.com/en-us/azure/advisor/advisor-cost-recommendations,
// `@claude`, not measured against a real export). Advisor's own export
// reports a recommendation's saving as POTENTIAL ANNUAL cost, never
// monthly, unlike the AWS and GCP exports; this is the one reader that
// divides by twelve.
func parseAzureAdvisorRow(rec []string, col map[string]int) (recRow, error) {
	field := func(name string) string { return strings.TrimSpace(rowField(rec, col, name)) }

	currency := field("Currency")
	if currency != "USD" {
		return recRow{}, fmt.Errorf("currency %q, this reader is USD only", currency)
	}
	annualStr := field("PotentialAnnualCostSavings")
	annual, err := money.Parse(annualStr)
	if err != nil {
		return recRow{}, fmt.Errorf("PotentialAnnualCostSavings %q does not parse as a decimal amount", annualStr)
	}
	if annual < 0 {
		return recRow{}, fmt.Errorf("PotentialAnnualCostSavings %q is negative", annualStr)
	}
	resource := field("ImpactedResource")
	if resource == "" {
		return recRow{}, fmt.Errorf("no ImpactedResource")
	}
	lookbackStr := field("LookbackDays")
	lookback, err := strconv.Atoi(lookbackStr)
	if err != nil || lookback < 0 {
		return recRow{}, fmt.Errorf("LookbackDays %q is not a non-negative integer", lookbackStr)
	}
	monthly := divRoundHalfAwayFromZero(int64(annual), 12)
	return recRow{
		Resource:           resource,
		Action:             field("RecommendationType"),
		Current:            field("CurrentSku"),
		Recommended:        field("RecommendedSku"),
		MonthlySavingCents: money.Cents(monthly),
		LookbackDays:       lookback,
	}, nil
}

// divRoundHalfAwayFromZero divides n by d, rounding half away from zero --
// the same convention money.Parse and money.Bps already use, restated here
// because this is the one place in the module that divides a money amount
// by a plain integer rather than parsing or scaling one. n is never
// negative by the time this is called (the row is refused above while it
// still is); the negative branch exists for this function's own honesty
// rather than a path this file ever takes.
func divRoundHalfAwayFromZero(n, d int64) int64 {
	if d == 0 {
		return 0
	}
	if n < 0 {
		return -((-n + d/2) / d)
	}
	return (n + d/2) / d
}

// ------------------------------------------------------------- the engine

// recSummary mirrors focusSummary: the same sentence describes what an
// import DID and what Test's DryRun would do.
type recSummary struct {
	FilesRead    int
	RowsAccepted int
	Refusals     []string
	FileRefusals []string
}

func (s *recSummary) Sentence(dryRun bool) string {
	verb := "Read"
	if dryRun {
		verb = "Would read"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %d file%s, %d row%s", verb, s.FilesRead, plural(s.FilesRead),
		s.RowsAccepted, plural(s.RowsAccepted))
	if n := len(s.Refusals); n > 0 {
		verb2 := "refused"
		if dryRun {
			verb2 = "would be refused"
		}
		fmt.Fprintf(&b, ". %d row%s %s: %s", n, plural(n), verb2, strings.Join(s.Refusals, "; "))
	}
	if n := len(s.FileRefusals); n > 0 {
		fmt.Fprintf(&b, ". %d file%s not read: %s", n, plural(n), strings.Join(s.FileRefusals, "; "))
	}
	b.WriteString(".")
	return b.String()
}

// runRightsizingImport is the whole engine, shared by all three readers:
// walk the folder (focusFiles, the same *.csv/*.csv.gz walk the FOCUS
// reader already established), stream each file, and upsert every accepted
// row keyed by desk+resource. DryRun runs the exact same read and validation
// path and simply never opens a transaction or executes an insert, the same
// discipline tokenFuseFocusReader holds for the same reason: a describe-only
// pass that drifts from the real one is worse than none.
func runRightsizingImport(db *sql.DB, cfg map[string]string, opt ImportOptions,
	desk string, required []string, parse recRowParser) (string, error) {
	if err := EnsureRecommendationsSchema(db); err != nil {
		return "", err
	}
	path := strings.TrimSpace(cfg["path"])
	if path == "" {
		return "", fmt.Errorf("no folder is configured; set the path and save before importing")
	}
	files, err := focusFiles(path)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("no *.csv or *.csv.gz files found in %s", path)
	}

	var tx *sql.Tx
	var ins *sql.Stmt
	if !opt.DryRun {
		tx, err = db.Begin()
		if err != nil {
			return "", err
		}
		defer tx.Rollback()
		ins, err = tx.Prepare(`INSERT INTO recommendations
			(id, provider, desk, resource, action, current, recommended,
			 monthly_saving_cents, lookback_days, source_file, imported_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET
			  provider=excluded.provider, action=excluded.action, current=excluded.current,
			  recommended=excluded.recommended, monthly_saving_cents=excluded.monthly_saving_cents,
			  lookback_days=excluded.lookback_days, source_file=excluded.source_file,
			  imported_at=excluded.imported_at`)
		if err != nil {
			return "", err
		}
		defer ins.Close()
	}

	importedAt := time.Now().UTC().Format(time.RFC3339)
	sum := &recSummary{}
	for _, f := range files {
		parsed, ferr := processRecommendationFile(f, required, parse)
		if ferr != nil {
			sum.FileRefusals = append(sum.FileRefusals, fmt.Sprintf("%s: %v", filepath.Base(f), ferr))
			continue
		}
		sum.FilesRead++
		sum.RowsAccepted += len(parsed.accepted)
		sum.Refusals = append(sum.Refusals, parsed.refusals...)
		if ins == nil {
			continue
		}
		base := filepath.Base(f)
		for _, r := range parsed.accepted {
			id := desk + ":" + r.Resource
			if _, err := ins.Exec(id, desk, desk, r.Resource, r.Action, r.Current, r.Recommended,
				int64(r.MonthlySavingCents), r.LookbackDays, base, importedAt); err != nil {
				return "", fmt.Errorf("row for %s: writing to recommendations: %w", r.Resource, err)
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

// parsedRecommendationFile is one file's own accepted rows and refusals,
// kept separate from the caller's running total the same reason
// processFocusFile's own return does: the caller decides what "this file
// succeeded" means, this function only reports what it found.
type parsedRecommendationFile struct {
	accepted []recRow
	refusals []string
}

// processRecommendationFile reads one file start to finish with csv.Reader,
// one record at a time, and never io.ReadAll or os.ReadFile on it -- the
// "100 MB line" hostile case exists to prove that holds under load. Unlike
// processFocusFile, this never writes to the database itself: it hands back
// the file's own accepted rows (small, validated Go values; the padded
// field a streaming test pads is never one this function maps to anything,
// so it is never copied out of the CSV reader's own reused buffer) and lets
// the caller write them once the whole file is known good. That is a
// deliberate, simpler difference from the FOCUS reader's per-row insert
// with a SAVEPOINT per file: a recommendation row is an upserted snapshot,
// never an append-only ledger, so there is no partial-file state to protect
// against here in the first place -- a file that fails partway through
// never had any of its rows handed to the database at all.
func processRecommendationFile(path string, required []string, parse recRowParser) (*parsedRecommendationFile, error) {
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
	r = stripBOM(r)

	cr := csv.NewReader(r)
	// Field-count enforcement OFF for the same reason tokenfusefocus.go
	// turns it off: a row with the wrong number of fields is refused BY
	// NAME, one row at a time, rather than aborting the whole file.
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("reading the header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[h] = i
	}
	var missing []string
	for _, want := range required {
		if _, ok := col[want]; !ok {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required column(s): %s", strings.Join(missing, ", "))
	}
	nCols := len(header)

	out := &parsedRecommendationFile{}
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
			out.refusals = append(out.refusals, fmt.Sprintf("%s row %d: %d field(s), header has %d",
				filepath.Base(path), rowNo, len(rec), nCols))
			continue
		}
		row, err := parse(rec, col)
		if err != nil {
			out.refusals = append(out.refusals, fmt.Sprintf("%s row %d: %v", filepath.Base(path), rowNo, err))
			continue
		}
		out.accepted = append(out.accepted, row)
	}
	return out, nil
}

// stripBOM removes a leading UTF-8 byte-order mark, which Excel and both
// Azure's and GCP's own console exports write by default: encoding/csv does
// not know about it, and left in place it folds into the FIRST header
// field's own name (three extra bytes ahead of "AccountId" make it a
// different string), so a legitimate export would be refused as missing
// every required column it actually has.
func stripBOM(r io.Reader) io.Reader {
	br := bufio.NewReader(r)
	if b, err := br.Peek(3); err == nil && bytes.Equal(b, []byte{0xEF, 0xBB, 0xBF}) {
		br.Discard(3)
	}
	return br
}

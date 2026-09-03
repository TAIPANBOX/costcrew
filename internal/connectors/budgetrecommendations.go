package connectors

// PARTNER-BUDGET-RECOMMENDATIONS-SPEC.md: a second, read-only budget input
// beside the person-entered one this console has always had.
// `@yurii 2026-09-03`: «це можна отримувати від користувача, або, наприклад,
// подивитись, які пропозиції дають провайдери хмарні».
//
// Three readers, one per provider's own published budget-recommendation
// shape, into one shared `budget_recommendations` table -- the same split
// tokenfusefocus.go established for one source and this package's own
// rightsizing-style readers (not yet on this branch; see the PR body for the
// style this was modelled on) extend to three. One shared engine
// (runBudgetRecommendationImport, processBudgetRecommendationFile) because
// the CSV mechanics -- stream the file, refuse a bad row by name, never hold
// the whole thing in memory, strip a BOM, accept CRLF -- are the same
// mechanics every reader in this package already uses; `focusFiles` and
// `plural` are reused from tokenfusefocus.go rather than copied.
//
// THE GUARDRAIL, stated here because this is the only place
// budget_recommendations is written: a row here is citation material for a
// finops-partner's own brief, never a number this console applies. It is
// never read by estate.BudgetVsActual, crew.SpendInMonth, or any headroom or
// guard computation anywhere in this module, and no code path in this
// package or any other ever copies a row from here into the `budgets` table.
// internal/deliver/partnerbudget.go is the one reader outside this package;
// see its own comment, CLAUDE.md invariant 46, and
// internal/deliver/partnerbudget_test.go's guardrail tests.
//
// The three fixtures are internal/connectors/testdata/{aws-budgets-
// recommended,gcp-cost-recommender-budget,azure-advisor-budget}-2026-09-03.csv,
// hand-authored from each provider's own published documentation the same
// way tokenfuse-focus-2026-09-02.csv has a real stub-server capture and this
// package's own AWS/GCP/Azure fixtures do not: `@claude`, not measured
// against a real export. None of the three clouds publishes a literal
// "recommended team budget" report; AWS Budgets, GCP's Recommender family and
// Azure Advisor each publish cost-shaped recommendations that this reader's
// own CSV shape approximates, with the team dimension carried the way an
// operator's own budget naming or tagging convention would carry it into an
// export.

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
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// ------------------------------------------------------------------ schema

const budgetRecommendationsSchema = `
CREATE TABLE IF NOT EXISTS budget_recommendations(
  provider TEXT NOT NULL, team TEXT NOT NULL, month TEXT NOT NULL,
  recommended_cents INTEGER NOT NULL,
  source_file TEXT NOT NULL, imported_at TEXT NOT NULL,
  PRIMARY KEY (provider, team, month));
`

// EnsureBudgetRecommendationsSchema is invariant 11's discipline applied to
// this table, the same shape EnsureFocusSchema already holds for ai_calls:
// every startup step is a migration and every one runs on every start, so a
// store from before this step gets the table without anybody asking for it,
// and running it again changes nothing. Called from cmd/costcrew/main.go
// unconditionally, and from each reader itself, so a test store that only
// ever calls Import or Test still gets it.
func EnsureBudgetRecommendationsSchema(db *sql.DB) error {
	if _, err := db.Exec(budgetRecommendationsSchema); err != nil {
		return fmt.Errorf("creating budget_recommendations: %w", err)
	}
	return nil
}

// BudgetRecommendation is one row, read back by the packet section that
// cites it beside the team's real budget.
type BudgetRecommendation struct {
	Provider, Team, Month  string
	RecommendedCents       money.Cents
	SourceFile, ImportedAt string
}

// BudgetRecommendations reads every row for one provider, in a stable base
// order (team, then month, ascending). Which (team, month) pairs also have a
// real budget row -- the pairing that decides what a packet section actually
// shows -- is the CALLER's own concern (internal/deliver/partnerbudget.go),
// the same separation connectors.Recommendations draws for rightsizing: this
// hands back a deterministic order over everything imported, and leaves
// pairing and ranking to the reader that needs them.
func BudgetRecommendations(db *sql.DB, provider string) ([]BudgetRecommendation, error) {
	rows, err := db.Query(`SELECT provider, team, month, recommended_cents, source_file, imported_at
		FROM budget_recommendations WHERE provider=? ORDER BY team, month`, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BudgetRecommendation
	for rows.Next() {
		var r BudgetRecommendation
		var cents int64
		if err := rows.Scan(&r.Provider, &r.Team, &r.Month, &cents, &r.SourceFile, &r.ImportedAt); err != nil {
			return nil, err
		}
		r.RecommendedCents = money.Cents(cents)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- catalogue

var requiredAWSBudgetsRecommendedColumns = []string{
	"AccountId", "Team", "Month", "RecommendedBudgetAmount", "CurrencyCode",
}

var requiredGCPCostRecommenderBudgetColumns = []string{
	"project_id", "team", "month", "recommended_budget_amount", "currency_code",
}

var requiredAzureAdvisorBudgetColumns = []string{
	"SubscriptionId", "Team", "Month", "RecommendedBudgetAmount", "Currency",
}

// awsBudgetsRecommendedReader, gcpCostRecommenderBudgetReader and
// azureAdvisorBudgetReader are registered in the readers map literal in
// connectors.go under "aws-budgets-recommended", "gcp-cost-recommender-budget"
// and "azure-advisor-budget".
func awsBudgetsRecommendedReader(db *sql.DB, cfg map[string]string, opt ImportOptions) (string, error) {
	return runBudgetRecommendationImport(db, cfg, opt, "aws",
		requiredAWSBudgetsRecommendedColumns, parseAWSBudgetsRecommendedRow)
}

func gcpCostRecommenderBudgetReader(db *sql.DB, cfg map[string]string, opt ImportOptions) (string, error) {
	return runBudgetRecommendationImport(db, cfg, opt, "gcp",
		requiredGCPCostRecommenderBudgetColumns, parseGCPCostRecommenderBudgetRow)
}

func azureAdvisorBudgetReader(db *sql.DB, cfg map[string]string, opt ImportOptions) (string, error) {
	return runBudgetRecommendationImport(db, cfg, opt, "azure",
		requiredAzureAdvisorBudgetColumns, parseAzureAdvisorBudgetRow)
}

// -------------------------------------------------------------- row shape

// budgetRecRow is one row already validated and converted, before it is
// written. Team is never a number this reader invents: a row whose own Team
// column is empty is refused (see the three parsers below), not guessed from
// the account or subscription id beside it.
type budgetRecRow struct {
	Team, Month      string
	RecommendedCents money.Cents
}

// budgetRecRowParser turns one already-column-aligned CSV record into a
// budgetRecRow, or names what was wrong with it -- one per provider because
// AWS, GCP and Azure each publish a different column set for the same idea,
// the same shape this package's own per-provider parsers already use.
type budgetRecRowParser func(rec []string, col map[string]int) (budgetRecRow, error)

func budgetRecField(rec []string, col map[string]int, name string) string {
	i, ok := col[name]
	if !ok || i < 0 || i >= len(rec) {
		return ""
	}
	return rec[i]
}

// validBudgetRecMonth is estate.validMonth's own check, restated here rather
// than exported from internal/estate for one boolean: "2026-09", never
// "2026-9" or "September".
func validBudgetRecMonth(s string) bool {
	if len(s) != 7 || s[4] != '-' {
		return false
	}
	for i, c := range s {
		if i == 4 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return s[5] == '0' && s[6] >= '1' && s[6] <= '9' ||
		s[5] == '1' && s[6] >= '0' && s[6] <= '2'
}

// parseAWSBudgetsRecommendedRow reads AWS Budgets' own recommended-threshold
// export (`@claude`, not measured against a real export -- see this file's
// own header comment).
func parseAWSBudgetsRecommendedRow(rec []string, col map[string]int) (budgetRecRow, error) {
	field := func(name string) string { return strings.TrimSpace(budgetRecField(rec, col, name)) }

	currency := field("CurrencyCode")
	if currency != "USD" {
		return budgetRecRow{}, fmt.Errorf("currency %q, this reader is USD only", currency)
	}
	amountStr := field("RecommendedBudgetAmount")
	amount, err := money.Parse(amountStr)
	if err != nil {
		return budgetRecRow{}, fmt.Errorf("RecommendedBudgetAmount %q does not parse as a decimal amount", amountStr)
	}
	if amount < 0 {
		return budgetRecRow{}, fmt.Errorf("RecommendedBudgetAmount %q is negative", amountStr)
	}
	team := field("Team")
	if team == "" {
		return budgetRecRow{}, fmt.Errorf("no Team")
	}
	month := field("Month")
	if !validBudgetRecMonth(month) {
		return budgetRecRow{}, fmt.Errorf("Month %q is not a month; write it as 2026-09", month)
	}
	return budgetRecRow{Team: team, Month: month, RecommendedCents: amount}, nil
}

// parseGCPCostRecommenderBudgetRow reads a Cost Recommender export's own
// budget-shaped recommendation columns (`@claude`, not measured against a
// real export).
func parseGCPCostRecommenderBudgetRow(rec []string, col map[string]int) (budgetRecRow, error) {
	field := func(name string) string { return strings.TrimSpace(budgetRecField(rec, col, name)) }

	currency := field("currency_code")
	if currency != "USD" {
		return budgetRecRow{}, fmt.Errorf("currency %q, this reader is USD only", currency)
	}
	amountStr := field("recommended_budget_amount")
	amount, err := money.Parse(amountStr)
	if err != nil {
		return budgetRecRow{}, fmt.Errorf("recommended_budget_amount %q does not parse as a decimal amount", amountStr)
	}
	if amount < 0 {
		return budgetRecRow{}, fmt.Errorf("recommended_budget_amount %q is negative", amountStr)
	}
	team := field("team")
	if team == "" {
		return budgetRecRow{}, fmt.Errorf("no team")
	}
	month := field("month")
	if !validBudgetRecMonth(month) {
		return budgetRecRow{}, fmt.Errorf("month %q is not a month; write it as 2026-09", month)
	}
	return budgetRecRow{Team: team, Month: month, RecommendedCents: amount}, nil
}

// parseAzureAdvisorBudgetRow reads Advisor's own budget-shaped cost
// recommendation columns (`@claude`, not measured against a real export).
// Unlike this package's own rightsizing-style Advisor reader, this one is
// already monthly: AWS Budgets and GCP's Recommender both state a
// recommended budget as a monthly figure, and this reader does not invent an
// annual-to-monthly conversion the export's own column does not ask for.
func parseAzureAdvisorBudgetRow(rec []string, col map[string]int) (budgetRecRow, error) {
	field := func(name string) string { return strings.TrimSpace(budgetRecField(rec, col, name)) }

	currency := field("Currency")
	if currency != "USD" {
		return budgetRecRow{}, fmt.Errorf("currency %q, this reader is USD only", currency)
	}
	amountStr := field("RecommendedBudgetAmount")
	amount, err := money.Parse(amountStr)
	if err != nil {
		return budgetRecRow{}, fmt.Errorf("RecommendedBudgetAmount %q does not parse as a decimal amount", amountStr)
	}
	if amount < 0 {
		return budgetRecRow{}, fmt.Errorf("RecommendedBudgetAmount %q is negative", amountStr)
	}
	team := field("Team")
	if team == "" {
		return budgetRecRow{}, fmt.Errorf("no Team")
	}
	month := field("Month")
	if !validBudgetRecMonth(month) {
		return budgetRecRow{}, fmt.Errorf("Month %q is not a month; write it as 2026-09", month)
	}
	return budgetRecRow{Team: team, Month: month, RecommendedCents: amount}, nil
}

// ------------------------------------------------------------- the engine

// budgetRecSummary mirrors focusSummary and this package's own recSummary
// equivalent: the same sentence describes what an import DID and what
// Test's DryRun would do.
type budgetRecSummary struct {
	FilesRead    int
	RowsAccepted int
	Refusals     []string
	FileRefusals []string
}

func (s *budgetRecSummary) Sentence(dryRun bool) string {
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

// runBudgetRecommendationImport is the whole engine, shared by all three
// readers: walk the folder (focusFiles, tokenfusefocus.go's own *.csv/
// *.csv.gz walk), stream each file, and upsert every accepted row keyed by
// provider+team+month -- a recommendation is a current snapshot, not a log,
// the same reasoning this package's own rightsizing-style readers apply to a
// per-resource recommendation. DryRun runs the exact same read and
// validation path and never opens a transaction or executes an insert, the
// same discipline tokenFuseFocusReader holds for the same reason: a
// describe-only pass that drifts from the real one is worse than none.
func runBudgetRecommendationImport(db *sql.DB, cfg map[string]string, opt ImportOptions,
	provider string, required []string, parse budgetRecRowParser) (string, error) {
	if err := EnsureBudgetRecommendationsSchema(db); err != nil {
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
		ins, err = tx.Prepare(`INSERT INTO budget_recommendations
			(provider, team, month, recommended_cents, source_file, imported_at)
			VALUES (?,?,?,?,?,?)
			ON CONFLICT(provider, team, month) DO UPDATE SET
			  recommended_cents=excluded.recommended_cents,
			  source_file=excluded.source_file, imported_at=excluded.imported_at`)
		if err != nil {
			return "", err
		}
		defer ins.Close()
	}

	importedAt := time.Now().UTC().Format(time.RFC3339)
	sum := &budgetRecSummary{}
	for _, f := range files {
		parsed, ferr := processBudgetRecommendationFile(f, required, parse)
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
			if _, err := ins.Exec(provider, r.Team, r.Month, int64(r.RecommendedCents), base, importedAt); err != nil {
				return "", fmt.Errorf("row for %s/%s: writing to budget_recommendations: %w", r.Team, r.Month, err)
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

// parsedBudgetRecFile is one file's own accepted rows and refusals, kept
// separate from the caller's running total the same reason
// processFocusFile's own return does.
type parsedBudgetRecFile struct {
	accepted []budgetRecRow
	refusals []string
}

// processBudgetRecommendationFile reads one file start to finish with
// csv.Reader, one record at a time, and never io.ReadAll or os.ReadFile on
// it -- the "100 MB line" hostile case proves that holds under load. It
// never writes to the database itself: a file that fails partway through
// never had any of its rows handed to the database at all, the same
// deliberate difference from tokenFuseFocusReader's per-row insert that this
// package's own rightsizing-style readers already draw, for the same
// reason: a recommendation row is an upserted snapshot, never an
// append-only ledger, so there is no partial-file state to protect against
// here in the first place.
func processBudgetRecommendationFile(path string, required []string, parse budgetRecRowParser) (*parsedBudgetRecFile, error) {
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
	r = stripUTF8BOM(r)

	cr := csv.NewReader(r)
	// Field-count enforcement OFF for the same reason tokenfusefocus.go turns
	// it off: a row with the wrong number of fields is refused BY NAME, one
	// row at a time, rather than aborting the whole file.
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

	out := &parsedBudgetRecFile{}
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

// stripUTF8BOM removes a leading UTF-8 byte-order mark, which Excel and both
// Azure's and GCP's own console exports write by default: encoding/csv does
// not know about it, and left in place it folds into the FIRST header
// field's own name, so a legitimate export would be refused as missing
// every required column it actually has.
func stripUTF8BOM(r io.Reader) io.Reader {
	br := bufio.NewReader(r)
	if b, err := br.Peek(3); err == nil && bytes.Equal(b, []byte{0xEF, 0xBB, 0xBF}) {
		br.Discard(3)
	}
	return br
}

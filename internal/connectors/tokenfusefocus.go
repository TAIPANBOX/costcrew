package connectors

// The tokenfuse-focus reader: a folder of FOCUS 1.2-style CSV files as
// `tokenfuse focus-export` writes them, with the gateway's own extension
// columns (an agent id and a run id on every row) that let this console
// attribute AI spend to an agent rather than only a team.
//
// This is the first reader the registry has ever held, so it is also the
// shape the cloud FOCUS readers (AWS, Azure, GCP) will reuse: stream the
// file, never hold the whole thing in memory, refuse a bad row by name
// rather than a bad file by silence, and never mix a generated estate with
// real money without being told to.

import (
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// ------------------------------------------------------------------ schema

// focusSchema is the additive part of section 3 this package owns.
// charges.provenance belongs to internal/estate, which defines charges; this
// table is specific to what a FOCUS-shaped reader needs to keep, and no
// other package reads it today.
//
// billed_microusd, not billed_cents: an LLM call routinely costs a few
// tenths of a cent (this fixture's own haiku calls are $0.0035 each), and a
// PER-CALL column in cents rounds every one of them to zero before they ever
// have a chance to add up -- ten of them are three and a half cents, not
// nothing. Micro-dollars (1e-6, the unit TokenFuse's own trace already uses)
// keep each call's own amount exact; charges.billed_cents, the daily total,
// is still cents, rounded once from the SUM of a day's micros in
// deriveCharges, never per call. See money.Micros for the type and why the
// rounding lives there and nowhere else.
const focusSchema = `
CREATE TABLE IF NOT EXISTS ai_calls(
  file_sha256 TEXT NOT NULL, row_no INTEGER NOT NULL,
  ts TEXT NOT NULL, day TEXT NOT NULL,
  team TEXT, agent TEXT NOT NULL, run_id TEXT, parent_run_id TEXT,
  provider TEXT, model TEXT NOT NULL,
  tokens_in INTEGER NOT NULL, tokens_out INTEGER NOT NULL,
  billed_microusd INTEGER NOT NULL,
  blocked INTEGER NOT NULL, basis TEXT NOT NULL,
  outcome TEXT, tool_calls INTEGER,
  PRIMARY KEY (file_sha256, row_no));
CREATE INDEX IF NOT EXISTS ai_calls_day ON ai_calls(day, team, model);
CREATE INDEX IF NOT EXISTS ai_calls_agent ON ai_calls(agent, day);
`

// EnsureFocusSchema is invariant 11's discipline applied to this reader:
// every startup step is a migration and every one runs on every start, so a
// store from before this step gets the provenance column and ai_calls table
// without anybody asking for it, and running it again changes nothing.
//
// Called from cmd/costcrew/main.go on every start, unconditionally, not only
// when this connector is imported: the AI page and the KPI library have to
// read charges.provenance and ai_calls on every render, whether or not
// anybody has ever pointed this reader at a folder, and a query against a
// column or table that does not exist yet fails rather than reading as
// "nothing here". It also runs from the reader itself, so a test store that
// only ever calls Import or Test still gets it.
//
// estate.SeedSchema runs first so a bare store (nothing has called
// estate.Seed yet, which is every test store in this package) has a charges
// table to alter in the first place; on an installation where Seed already
// ran, CREATE TABLE IF NOT EXISTS repeats harmlessly.
func EnsureFocusSchema(db *sql.DB) error {
	if _, err := db.Exec(estate.SeedSchema); err != nil {
		return fmt.Errorf("ensuring the estate schema exists: %w", err)
	}
	if _, err := db.Exec(`ALTER TABLE charges ADD COLUMN provenance TEXT`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("adding charges.provenance: %w", err)
	}
	if _, err := db.Exec(focusSchema); err != nil {
		return fmt.Errorf("creating ai_calls: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------- catalogue

// requiredFocusColumns must all be present, by name, order irrelevant. A
// file missing one is refused whole; nothing from it is written.
var requiredFocusColumns = []string{
	"BilledCost", "BillingCurrency", "ChargePeriodStart", "ProviderName",
	"ServiceName", "ResourceId", "SubAccountId", "x_agent_id", "x_run_id",
	"x_model", "x_tokens_in", "x_tokens_out", "x_blocked", "x_cost_basis",
}

// generatedTables is what reading internal/history and cmd/costcrew/main.go
// found seeded from the generated world, minus what stays: the roster,
// accounts and connections are somebody's own work (an agent a person hired,
// a connector's own saved settings) and a switch to real money must not
// erase them. charges is handled separately, scoped to its generated rows
// only (provenance IS NULL), because after this step it can also hold real
// ones this reader wrote, and those must survive.
var generatedTables = []string{
	"drivers", "attribution", "anomalies", "tasks", "artifacts",
	"sprints", "forecasts", "chargeback",
}

const replaceGeneratedRefusal = "this store still holds the generated estate (rows in charges " +
	"with no provenance). -replace-generated removes it FIRST, in one transaction: the " +
	"generated charges, drivers, attribution, and the seeded anomalies, tasks, artifacts, " +
	"sprints, forecasts and chargeback rows. The roster, accounts and connections are not " +
	"touched. Pass it once, when real data is ready to replace the fixture"

// tokenFuseFocusReader is registered in the readers map literal in
// connectors.go under "tokenfuse-focus".
func tokenFuseFocusReader(db *sql.DB, cfg map[string]string, opt ImportOptions) (string, error) {
	if err := EnsureFocusSchema(db); err != nil {
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

	// Refusal 1: a generated estate is not mixed with real money. Checked
	// before anything is read, and before the transaction that would do the
	// wiping is even opened, so Test (DryRun) never has to care about it -
	// it never writes regardless.
	var wipe bool
	if !opt.DryRun {
		mixed, err := hasGeneratedCharges(db)
		if err != nil {
			return "", err
		}
		if mixed && !opt.ReplaceGenerated {
			return "", fmt.Errorf(replaceGeneratedRefusal)
		}
		wipe = mixed
	}

	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	// Rolled back unless explicitly committed below. For DryRun this is the
	// second guarantee, beside DryRun itself, that nothing it does can
	// persist: even a bug that tried to write would be undone here.
	defer tx.Rollback()

	if wipe {
		if err := replaceGeneratedEstate(tx); err != nil {
			return "", err
		}
	}

	var ins *sql.Stmt
	if !opt.DryRun {
		ins, err = tx.Prepare(`INSERT INTO ai_calls
			(file_sha256, row_no, ts, day, team, agent, run_id, parent_run_id,
			 provider, model, tokens_in, tokens_out, billed_microusd, blocked, basis,
			 outcome, tool_calls)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(file_sha256, row_no) DO UPDATE SET
			  ts=excluded.ts, day=excluded.day, team=excluded.team, agent=excluded.agent,
			  run_id=excluded.run_id, parent_run_id=excluded.parent_run_id,
			  provider=excluded.provider, model=excluded.model,
			  tokens_in=excluded.tokens_in, tokens_out=excluded.tokens_out,
			  billed_microusd=excluded.billed_microusd, blocked=excluded.blocked, basis=excluded.basis,
			  outcome=excluded.outcome, tool_calls=excluded.tool_calls`)
		if err != nil {
			return "", err
		}
		defer ins.Close()
	}

	sum := newFocusSummary()
	daysTouched := map[string]bool{}
	for i, f := range files {
		// A SAVEPOINT per file, not just a Go-level skip: without it, a file
		// that fails PART WAY through (a truncated gzip after one good row)
		// would leave that row's already-executed INSERT sitting in the
		// transaction even though the file is reported as refused. "the
		// good one is imported, the bad one named" means a named file
		// contributes NOTHING, not "whatever it got through before it
		// broke." Buffering the file's rows in Go instead, to decide after
		// the fact, would reintroduce exactly the whole-file-in-memory
		// problem the streaming read exists to avoid.
		sp := fmt.Sprintf("focus_file_%d", i)
		if !opt.DryRun {
			if _, err := tx.Exec("SAVEPOINT " + sp); err != nil {
				return "", err
			}
		}
		local, ferr := processFocusFile(f, ins)
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
		for d := range local.Days {
			daysTouched[d] = true
		}
		if !opt.DryRun {
			if _, err := tx.Exec("RELEASE " + sp); err != nil {
				return "", err
			}
		}
	}

	if !opt.DryRun {
		if err := deriveCharges(tx, daysTouched); err != nil {
			return "", err
		}
		if err := deriveAttribution(tx, daysTouched); err != nil {
			return "", err
		}
		if wipe && opt.Rec != nil {
			if err := opt.Rec.Emit("generated_estate_replaced", opt.Actor, "", map[string]any{
				"connector": "tokenfuse-focus",
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

func hasGeneratedCharges(db *sql.DB) (bool, error) {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM charges WHERE provenance IS NULL`).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// replaceGeneratedEstate removes exactly what Seed and history.Seed put
// there. Called only once refusal 1 has already required either the flag or
// an empty store, so this never runs silently.
func replaceGeneratedEstate(tx *sql.Tx) error {
	if _, err := tx.Exec(`DELETE FROM charges WHERE provenance IS NULL`); err != nil {
		return fmt.Errorf("removing the generated charges: %w", err)
	}
	for _, t := range generatedTables {
		if _, err := tx.Exec("DELETE FROM " + t); err != nil &&
			!strings.Contains(err.Error(), "no such table") {
			return fmt.Errorf("emptying %s: %w", t, err)
		}
	}
	return nil
}

// -------------------------------------------------------------- the folder

func focusFiles(dir string) ([]string, error) {
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
	// Sorted, not directory order: invariant 7 holds for this console's own
	// data and a folder read in whatever order the filesystem happens to
	// return is the same failure wearing a different hat.
	sort.Strings(out)
	return out, nil
}

// processFocusFile reads one file start to finish with csv.Reader, one
// record at a time, and never io.ReadAll or os.ReadFile on it: the 200 MB
// hostile case exists to prove that this stays true under load rather than
// only in the small fixture.
//
// It returns its OWN summary rather than writing into a shared one: the
// caller only merges it, and only keeps whatever rows this file's ins.Exec
// calls already made, when this returns a nil error. A row-level problem
// (bad currency, bad cost, a ragged record) is recorded in the returned
// summary and the file carries on; a file-level problem (missing column, a
// gzip that will not open, a byte stream the CSV parser cannot make sense
// of) returns an error, and the caller rolls back this file's savepoint so
// nothing it inserted survives either.
func processFocusFile(path string, ins *sql.Stmt) (*focusSummary, error) {
	sum := newFocusSummary()

	var sha string
	if ins != nil {
		// Only computed when something might be written: Test's dry run has
		// no idempotency key to keep, and hashing is a second full read of
		// whatever the file's size turns out to be.
		s, err := sha256OfFile(path)
		if err != nil {
			return nil, fmt.Errorf("hashing: %w", err)
		}
		sha = s
	}

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
	// Field-count enforcement OFF: a row with the wrong number of fields is
	// refused BY NAME, one row at a time, rather than aborting the file the
	// way csv.ErrFieldCount would. encoding/csv's own RFC 4180 handling
	// (quoted commas, quoted newlines, doubled quotes) is unaffected either
	// way; this setting only turns off the COUNT check.
	cr.FieldsPerRecord = -1
	// The returned slice's backing array may be reused on the next Read.
	// Safe here because every field is copied into a Go string (via
	// strings.TrimSpace, or stored directly) before the next Read is called;
	// nothing retains the slice itself past one loop iteration.
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
	for _, want := range requiredFocusColumns {
		if _, ok := col[want]; !ok {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required column(s): %s", strings.Join(missing, ", "))
	}
	nCols := len(header)

	rowNo := 0
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// A byte stream the CSV parser itself cannot make sense of
			// (unterminated quote, truncated gzip mid-stream): the reader's
			// position afterwards cannot be trusted for another Read, so
			// this ends the FILE, named, rather than the row.
			return nil, fmt.Errorf("after row %d: %w", rowNo, err)
		}
		rowNo++

		if len(rec) != nCols {
			sum.refuse(fmt.Sprintf("%s row %d: %d field(s), header has %d",
				filepath.Base(path), rowNo, len(rec), nCols))
			continue
		}

		row, err := parseFocusRow(rec, col)
		if err != nil {
			sum.refuse(fmt.Sprintf("%s row %d: %v", filepath.Base(path), rowNo, err))
			continue
		}
		sum.accept(row)

		if ins == nil {
			continue
		}
		blockedInt := 0
		if row.Blocked {
			blockedInt = 1
		}
		var toolCalls any
		if row.ToolCalls != nil {
			toolCalls = *row.ToolCalls
		}
		if _, err := ins.Exec(sha, rowNo, row.TS, row.Day, nullIfEmpty(row.Team), row.Agent,
			nullIfEmpty(row.RunID), nullIfEmpty(row.ParentRunID), nullIfEmpty(row.Provider),
			row.Model, row.TokensIn, row.TokensOut, int64(row.BilledMicros), blockedInt,
			row.Basis, nullIfEmpty(row.Outcome), toolCalls); err != nil {
			return nil, fmt.Errorf("row %d: writing to ai_calls: %w", rowNo, err)
		}
	}
	return sum, nil
}

func sha256OfFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// -------------------------------------------------------------- row shape

type focusRow struct {
	TS, Day                      string
	Team, Agent, RunID           string
	ParentRunID, Provider, Model string
	TokensIn, TokensOut          int64
	BilledMicros                 money.Micros
	Blocked                      bool
	Basis, Outcome               string
	ToolCalls                    *int64
}

// parseFocusRow validates and converts one already-aligned record (same
// field count as the header) into a focusRow, or names what was wrong.
//
// BilledCost goes through money.ParseMicros, never float64: mutant (b) in
// the testing plan is exactly this line reverted, to a naive float64 parse
// and multiply. A single row's own amount is kept exact in Micros; only
// deriveCharges rounds, once, on a whole day's SUM.
func parseFocusRow(rec []string, col map[string]int) (focusRow, error) {
	field := func(name string) string {
		i, ok := col[name]
		if !ok || i < 0 || i >= len(rec) {
			return ""
		}
		return rec[i]
	}

	currency := strings.TrimSpace(field("BillingCurrency"))
	if currency != "USD" {
		return focusRow{}, fmt.Errorf("currency %q, this reader is USD only", currency)
	}

	costStr := strings.TrimSpace(field("BilledCost"))
	micros, err := money.ParseMicros(costStr)
	if err != nil {
		return focusRow{}, fmt.Errorf("BilledCost %q does not parse as a decimal amount", costStr)
	}
	if micros < 0 {
		return focusRow{}, fmt.Errorf("BilledCost %q is negative", costStr)
	}

	tsStr := strings.TrimSpace(field("ChargePeriodStart"))
	if _, err := time.Parse(time.RFC3339, tsStr); err != nil {
		return focusRow{}, fmt.Errorf("ChargePeriodStart %q does not parse as RFC 3339", tsStr)
	}
	day := tsStr
	if len(day) > 10 {
		day = day[:10]
	}

	agent := strings.TrimSpace(field("x_agent_id"))
	if agent == "" {
		agent = strings.TrimSpace(field("ResourceId"))
	}
	if agent == "" {
		return focusRow{}, fmt.Errorf("no agent: x_agent_id and ResourceId are both empty")
	}

	blocked := strings.EqualFold(strings.TrimSpace(field("x_blocked")), "true")
	if blocked && micros != 0 {
		return focusRow{}, fmt.Errorf("blocked but BilledCost %q is not zero", costStr)
	}

	tokensIn, err := parseFocusTokens(field("x_tokens_in"), blocked)
	if err != nil {
		return focusRow{}, fmt.Errorf("x_tokens_in %q: %w", field("x_tokens_in"), err)
	}
	tokensOut, err := parseFocusTokens(field("x_tokens_out"), blocked)
	if err != nil {
		return focusRow{}, fmt.Errorf("x_tokens_out %q: %w", field("x_tokens_out"), err)
	}

	var toolCalls *int64
	if s := strings.TrimSpace(field("x_tool_calls")); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return focusRow{}, fmt.Errorf("x_tool_calls %q is not an integer", s)
		}
		toolCalls = &n
	}

	return focusRow{
		TS: tsStr, Day: day,
		Team:        strings.TrimSpace(field("x_unit")),
		Agent:       agent,
		RunID:       strings.TrimSpace(field("x_run_id")),
		ParentRunID: strings.TrimSpace(field("x_parent_run_id")),
		Provider:    strings.TrimSpace(field("ProviderName")),
		Model:       strings.TrimSpace(field("x_model")),
		TokensIn:    tokensIn, TokensOut: tokensOut,
		BilledMicros: micros, Blocked: blocked,
		Basis:     strings.TrimSpace(field("x_cost_basis")),
		Outcome:   strings.TrimSpace(field("x_outcome")),
		ToolCalls: toolCalls,
	}, nil
}

func parseFocusTokens(s string, blocked bool) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		if blocked {
			return 0, nil
		}
		return 0, fmt.Errorf("empty")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("not an integer")
	}
	if n < 0 {
		return 0, fmt.Errorf("negative")
	}
	return n, nil
}

// ----------------------------------------------------------------- summary

// focusSummary is what both Test (DryRun) and Import build and report: the
// same sentence describes what WOULD happen and what DID, because a
// describe-only pass that drifts from the real one is worse than none.
type focusSummary struct {
	FilesRead       int
	RowsAccepted    int
	Refusals        []string
	FileRefusals    []string
	Agents          map[string]bool
	Days            map[string]bool // this file's (or this whole import's) days touched
	FirstTS, LastTS string
	TotalMicros     money.Micros
}

func newFocusSummary() *focusSummary {
	return &focusSummary{Agents: map[string]bool{}, Days: map[string]bool{}}
}

func (s *focusSummary) accept(row focusRow) {
	s.RowsAccepted++
	s.Agents[row.Agent] = true
	s.Days[row.Day] = true
	s.TotalMicros += row.BilledMicros
	if s.FirstTS == "" || row.TS < s.FirstTS {
		s.FirstTS = row.TS
	}
	if s.LastTS == "" || row.TS > s.LastTS {
		s.LastTS = row.TS
	}
}

func (s *focusSummary) refuse(reason string) { s.Refusals = append(s.Refusals, reason) }

// merge folds a successfully-processed file's summary into the whole
// import's. Called only when that file returned no error, so what it adds
// here is exactly what its savepoint just kept.
func (s *focusSummary) merge(o *focusSummary) {
	s.RowsAccepted += o.RowsAccepted
	for a := range o.Agents {
		s.Agents[a] = true
	}
	for d := range o.Days {
		s.Days[d] = true
	}
	s.TotalMicros += o.TotalMicros
	if o.FirstTS != "" && (s.FirstTS == "" || o.FirstTS < s.FirstTS) {
		s.FirstTS = o.FirstTS
	}
	if o.LastTS != "" && (s.LastTS == "" || o.LastTS > s.LastTS) {
		s.LastTS = o.LastTS
	}
	s.Refusals = append(s.Refusals, o.Refusals...)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (s *focusSummary) Sentence(dryRun bool) string {
	verb := "Read"
	if dryRun {
		verb = "Would read"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %d file%s", verb, s.FilesRead, plural(s.FilesRead))
	if s.FirstTS != "" {
		fmt.Fprintf(&b, ", %s to %s", s.FirstTS, s.LastTS)
	}
	// TotalMicros.String(), not .Cents(): the whole point of keeping this in
	// Micros is that a reader asking what this folder holds sees a sub-cent
	// total (four decimals) rather than a total rounded down to nothing.
	fmt.Fprintf(&b, ", %d row%s, %d distinct agent%s, %s total BilledCost.",
		s.RowsAccepted, plural(s.RowsAccepted), len(s.Agents), plural(len(s.Agents)), s.TotalMicros)
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

// ------------------------------------------------------ derived ledger rows

func sortedDays(touched map[string]bool) []string {
	out := make([]string, 0, len(touched))
	for d := range touched {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// deriveCharges rebuilds the days this import touched from ai_calls, so a
// re-import changes nothing: the provenance-scoped rows for those days are
// deleted and rewritten from whatever ai_calls holds for them NOW, not only
// from the rows this call happened to insert. That is what makes two files
// touching the same day, or the same file imported twice, converge on the
// same charges instead of drifting apart.
//
// SUM THEN ROUND, exactly once, per group: the SQL SUM below adds whole
// Micros values (SQLite's SUM on an INTEGER column is exact 64-bit integer
// arithmetic, never float, well short of where a day's LLM calls could
// overflow it), and money.Micros.Cents is called on that sum, not on any row
// before it. Mutant (f) in the testing plan swaps this to ROUND THEN SUM --
// pre-rounding each row to its own cent boundary inside the SQL before
// summing those -- and the ten-row test catches it: ten calls at $0.0035 sum
// to four cents correctly rounded once, and to zero each rounded on its own
// first.
func deriveCharges(tx *sql.Tx, daysTouched map[string]bool) error {
	days := sortedDays(daysTouched)
	if len(days) == 0 {
		return nil
	}
	del, err := tx.Prepare(`DELETE FROM charges WHERE provenance='tokenfuse-focus' AND day=?`)
	if err != nil {
		return err
	}
	defer del.Close()
	ins, err := tx.Prepare(`INSERT INTO charges
		(source, day, service, team, category, billed_cents, quantity, unit, meter, model, provenance)
		VALUES ('ai', ?, ?, ?, 'Usage', ?, ?, 'tokens', ?, ?, 'tokenfuse-focus')`)
	if err != nil {
		return err
	}
	defer ins.Close()

	for _, d := range days {
		if _, err := del.Exec(d); err != nil {
			return fmt.Errorf("clearing %s's derived charges: %w", d, err)
		}
		rows, err := tx.Query(`SELECT COALESCE(team,''), COALESCE(provider,''), model,
				SUM(billed_microusd), SUM(tokens_in+tokens_out)
			FROM ai_calls WHERE day=? AND blocked=0
			GROUP BY COALESCE(team,''), COALESCE(provider,''), model
			ORDER BY 1,2,3`, d)
		if err != nil {
			return err
		}
		type grp struct {
			team, provider, model string
			micros, qty           int64
		}
		var groups []grp
		for rows.Next() {
			var g grp
			if err := rows.Scan(&g.team, &g.provider, &g.model, &g.micros, &g.qty); err != nil {
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
			service := strings.TrimSpace(g.provider) + " API"
			cents := money.Micros(g.micros).Cents() // the one rounding, after the sum
			if _, err := ins.Exec(d, service, nullIfEmpty(g.team), int64(cents), g.qty,
				g.model, g.model); err != nil {
				return fmt.Errorf("writing the %s/%s charges row for %s: %w", g.team, g.model, d, err)
			}
		}
	}
	return nil
}

// deriveAttribution picks, for every (day, team, service) this import
// touched, the agent with the largest billed, non-blocked share that day,
// and replaces whatever attribution row already covered exactly that day.
//
// team is written as the Go string directly, never NULL: attribution.team
// has never held NULL in this codebase (world.Planted's seeded rows write it
// the same way in estate.Seed), and estate.AgentFor reads it back with a
// bare `team=?`, no COALESCE. A NULL here would agree with nothing that
// query ever binds, including the empty string this reader's own team-less
// rows produce, and the forecaster's own attribution would silently vanish.
func deriveAttribution(tx *sql.Tx, daysTouched map[string]bool) error {
	days := sortedDays(daysTouched)
	for _, d := range days {
		// Ranked by the exact Micros sum, not by billed_cents: two agents
		// whose calls individually round to zero cents would otherwise tie
		// at zero and the ranking would fall back to whatever order SQLite
		// happened to return, rather than to who actually spent more.
		rows, err := tx.Query(`SELECT COALESCE(team,''), COALESCE(provider,''),
				agent, SUM(billed_microusd) AS micros
			FROM ai_calls WHERE day=? AND blocked=0
			GROUP BY COALESCE(team,''), COALESCE(provider,''), agent
			ORDER BY 1, 2, micros DESC, agent ASC`, d)
		if err != nil {
			return err
		}
		type row struct {
			team, provider, agent string
			micros                int64
		}
		var all []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.team, &r.provider, &r.agent, &r.micros); err != nil {
				rows.Close()
				return err
			}
			all = append(all, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		// The winner per (team, service) is the first row of its group: the
		// query above already orders by micros DESC within the group, ties
		// broken on the agent's own name so the pick is the same on every
		// run rather than on whatever order SQLite happened to visit rows.
		seen := map[string]bool{}
		for _, r := range all {
			key := r.team + "\x00" + r.provider
			if seen[key] {
				continue
			}
			seen[key] = true
			service := strings.TrimSpace(r.provider) + " API"
			if _, err := tx.Exec(`DELETE FROM attribution
				WHERE source='ai' AND team=? AND service=? AND day_start=? AND day_end=?`,
				r.team, service, d, d); err != nil {
				return fmt.Errorf("clearing the old attribution for %s on %s: %w", service, d, err)
			}
			if _, err := tx.Exec(`INSERT INTO attribution
				(source, team, service, day_start, day_end, agent, confidence)
				VALUES ('ai', ?, ?, ?, ?, ?, 'gateway-header')`,
				r.team, service, d, d, r.agent); err != nil {
				return fmt.Errorf("writing attribution for %s on %s: %w", service, d, err)
			}
		}
	}
	return nil
}

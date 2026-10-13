package main

// The drop itself: which calls a month holds, how they become rows, and the
// folder Cloudability reads them from.

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/plainname"
)

// reportPeriod is the ingress's <Report_Period> folder for the month that
// starts at month: YYYYMMDD of its first day, a hyphen, YYYYMMDD of the next
// month's first day. IBM's example is February 2023 as 20230201-20230301.
func reportPeriod(month time.Time) string {
	return month.Format("20060102") + "-" + month.AddDate(0, 1, 0).Format("20060102")
}

const (
	dayLayout     = "2006-01-02"
	instantLayout = "2006-01-02T15:04:05Z"

	// Bounds on the values the gateway's export carries into a printed cell
	// that its own reader does not already hold to one (x_unit and x_key_id
	// are; the agent, the model and the provider are not).
	agentMaxBytes    = 512
	modelMaxBytes    = 256
	providerMaxBytes = 128
)

// contents is a month's drop before it is written: its rows, and what the
// tool says about what it left out.
type contents struct {
	records        [][]string
	calls, blocked int64
	micros         money.Micros
	firstDay       string
	lastDay        string
	outside        int64 // calls in the folder that settled in another month
	blockedNoRow   int64 // blocked calls in groups that spent nothing
	groupsNoRow    int64
}

// build reads the month's calls out of ai_calls, where the tokenfuse-focus
// reader put them, and groups them into rows: one per UTC day, unit, agent,
// model, credential and provider.
//
// The day is the UTC date of the instant the gateway settled the call,
// date(ts) in SQLite, and not ai_calls.day, which is the text's own date:
// FOCUS dates are UTC, and a timestamp written with an offset (23:30 at
// UTC-5 is the next day) would otherwise land on the wrong one. TokenFuse
// writes every instant in UTC, so for its own exports the two agree.
//
// Money is summed in SQL as exact 64-bit integers of micro-dollars, the
// reader's own unit, and written out as that sum to six decimals: nothing is
// rounded, per call or per row. A row of cents would lose up to half a cent
// on every one of a month's thousands of rows (invariant 25's reason, one
// level up); six decimals are what the gateway itself exports. SQLite's SUM
// refuses an integer overflow rather than wrapping, so a hostile export that
// would overflow a sum stops the drop.
func build(db *sql.DB, month time.Time, accountID, accountName string) (contents, error) {
	var c contents
	from, to := month.Format(dayLayout), month.AddDate(0, 1, 0).Format(dayLayout)

	var undated int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_calls WHERE date(ts) IS NULL`).Scan(&undated); err != nil {
		return c, err
	}
	if undated > 0 {
		return c, fmt.Errorf("%d call%s carry an instant SQLite cannot place on a UTC day, so the month they "+
			"belong to is unknown", undated, plural(undated))
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_calls WHERE date(ts) < ?1 OR date(ts) >= ?2`,
		from, to).Scan(&c.outside); err != nil {
		return c, err
	}

	rows, err := db.Query(`SELECT date(ts), COALESCE(team,''), agent, model, COALESCE(key_id,''),
			COALESCE(provider,''),
			SUM(CASE WHEN blocked=0 THEN billed_microusd ELSE 0 END),
			SUM(CASE WHEN blocked=0 THEN tokens_in ELSE 0 END),
			SUM(CASE WHEN blocked=0 THEN tokens_out ELSE 0 END),
			SUM(CASE WHEN blocked=0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN blocked=0 THEN 0 ELSE 1 END),
			SUM(CASE WHEN blocked=0 AND basis='estimated' THEN 1 ELSE 0 END)
		FROM ai_calls WHERE date(ts) >= ?1 AND date(ts) < ?2
		GROUP BY 1, 2, 3, 4, 5, 6
		ORDER BY 1, 2, 3, 4, 5, 6`, from, to)
	if err != nil {
		return c, err
	}
	defer rows.Close()

	periodStart := month.Format(instantLayout)
	periodEnd := month.AddDate(0, 1, 0).Format(instantLayout)
	for rows.Next() {
		var r row
		var micros int64
		if err := rows.Scan(&r.day, &r.unit, &r.agent, &r.model, &r.key, &r.provider, &micros,
			&r.tokensIn, &r.tokensOut, &r.calls, &r.blocked, &r.estimate); err != nil {
			return c, err
		}
		c.calls += r.calls
		c.blocked += r.blocked
		if c.firstDay == "" {
			c.firstDay = r.day
		}
		c.lastDay = r.day
		if r.calls == 0 {
			// Every call in the group was blocked: it cost nothing and
			// consumed nothing, and FOCUS 1.1 requires a positive consumed
			// quantity on a Usage row, so it has no row of its own. The
			// gateway's refusals by reason are on CostCrew's /ai page.
			c.blockedNoRow += r.blocked
			c.groupsNoRow++
			continue
		}
		if err := printable(r); err != nil {
			return c, err
		}
		day, err := time.Parse(dayLayout, r.day)
		if err != nil {
			return c, fmt.Errorf("a call's day %q: %w", r.day, err)
		}
		r.day, r.nextDay = day.Format(instantLayout), day.AddDate(0, 0, 1).Format(instantLayout)
		r.periodStart, r.periodEnd = periodStart, periodEnd
		r.cost = microsDecimal(money.Micros(micros))
		r.accountID, r.accountName = accountID, accountName
		if c.micros+money.Micros(micros) < c.micros {
			return c, errors.New("the month's total overflows 64 bits of micro-dollars")
		}
		c.micros += money.Micros(micros)
		c.records = append(c.records, r.fill())
	}
	return c, rows.Err()
}

// printable holds the values the export carries into a printed cell to the
// rule this console holds every printed name to (plainname.Check): bounded,
// valid text, no control, format or separator character, and not beginning
// with = + - or @. A value that breaks it stops the drop rather than being
// rewritten: FOCUS 1.1 requires a string to keep its provider's spelling, so
// the apostrophe CostCrew's own CSV downloads put in front of such a cell
// (internal/web/download.go, csvCell) would change the agent's id in
// Cloudability. Stopping keeps both: no cell a spreadsheet runs, and no id
// that is not the gateway's.
func printable(r row) error {
	for _, f := range []struct {
		label, v string
		max      int
	}{
		{"x_agent_id", r.agent, agentMaxBytes},
		{"x_model", r.model, modelMaxBytes},
		{"ProviderName", r.provider, providerMaxBytes},
	} {
		if f.v == "" {
			return fmt.Errorf("a call on %s names no %s, which a FOCUS row cannot leave empty", r.day, f.label)
		}
		if reason := plainname.Check(f.label, f.v, f.max); reason != "" {
			return errors.New(reason)
		}
	}
	return nil
}

// microsDecimal writes a non-negative amount of micro-dollars as a decimal
// with six places, exactly: 3500 is 0.003500.
func microsDecimal(m money.Micros) string {
	return fmt.Sprintf("%d.%06d", int64(m)/1_000_000, int64(m)%1_000_000)
}

func plural(n int64) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ---------------------------------------------------------------- the files

// layout is where one drop goes.
type layout struct {
	out     string   // the local folder that stands for the bucket's root
	rootDir string   // the bucket or container name, for the manifest
	subDirs []string // the optional folders between the root and the vendor
	vendor  string
	prefix  string
}

type manifest struct {
	// The field order is IBM's own example's.
	Compression   string   `json:"compression"`
	ContentType   string   `json:"content_type"`
	ReportID      string   `json:"report_id"`
	RootDir       string   `json:"root_dir"`
	AllReportKeys []string `json:"all_report_keys"`
	UpdatedAt     string   `json:"updated_at"`
	FocusVersion  string   `json:"focus_version"`
}

// written is what write left on disk.
type written struct {
	epochDir, dataPath, manifestPath string
	keys                             []string
}

// write puts a drop at
// <out>/<sub dirs>/<vendor>/<report period>/<epoch>/: the data as
// <prefix>-00001.csv.gz and the manifest as <prefix>-Manifest.json, written
// last, so a folder with a manifest in it always has its data beside it.
//
// The epoch folder is new or the drop is refused: os.Mkdir fails on one that
// exists, and a drop whose epoch is not after every drop already there for the
// month is refused too, because Cloudability reads the latest epoch folder of
// a month and a drop behind it would never be read. A drop therefore never
// adds to another and never replaces one in place; the next one supersedes it
// by being later. If writing fails part way, the epoch folder this call made
// is removed, so a half-written drop is never left for a sync to carry.
func write(l layout, month, now time.Time, records [][]string) (written, error) {
	var w written
	period := reportPeriod(month)
	epoch := strconv.FormatInt(now.Unix(), 10)

	rel := append(append([]string{}, l.subDirs...), l.vendor, period)
	periodDir, err := makeDirs(l.out, rel)
	if err != nil {
		return w, err
	}
	entries, err := os.ReadDir(periodDir)
	if err != nil {
		return w, err
	}
	for _, e := range entries {
		n, perr := strconv.ParseInt(e.Name(), 10, 64)
		if perr != nil || !e.IsDir() {
			continue
		}
		if n == now.Unix() {
			return w, fmt.Errorf("the epoch folder %s already exists; a drop is never written into one that does",
				filepath.Join(periodDir, e.Name()))
		}
		if n > now.Unix() {
			return w, fmt.Errorf("a drop for %s already exists at epoch %d, later than this run's %s; Cloudability "+
				"reads the latest epoch folder, so this drop would never be the one it reads (is this machine's clock behind?)",
				month.Format("2006-01"), n, epoch)
		}
	}

	w.epochDir = filepath.Join(periodDir, epoch)
	if err := os.Mkdir(w.epochDir, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return w, fmt.Errorf("the epoch folder %s already exists; a drop is never written into one that does", w.epochDir)
		}
		return w, err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(w.epochDir) // this call made it a moment ago, and it is incomplete
		}
	}()

	data, err := gzipCSV(header(), records)
	if err != nil {
		return w, err
	}
	dataName := l.prefix + "-00001.csv.gz"
	w.dataPath = filepath.Join(w.epochDir, dataName)
	if err := createFile(w.dataPath, data); err != nil {
		return w, err
	}
	// The keys are paths inside the bucket WITHOUT the bucket's own name:
	// "all_report_keys ... excluding root_dir". path, not filepath: they are
	// object keys, slash-separated whatever this machine uses.
	keyParts := append(append([]string{}, rel...), epoch, dataName)
	w.keys = []string{path.Join(keyParts...)}

	sum := sha256.Sum256(data)
	m := manifest{
		Compression:   "GZIP",
		ContentType:   "CSV",
		ReportID:      reportID(l.rootDir, w.keys, sum),
		RootDir:       l.rootDir,
		AllReportKeys: w.keys,
		UpdatedAt:     now.UTC().Format(instantLayout),
		FocusVersion:  focusVersion,
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return w, err
	}
	body = append(body, '\n')
	w.manifestPath = filepath.Join(w.epochDir, l.prefix+"-Manifest.json")
	tmp := filepath.Join(w.epochDir, "."+l.prefix+"-Manifest.json.partial")
	if err := createFile(tmp, body); err != nil {
		return w, err
	}
	if err := os.Rename(tmp, w.manifestPath); err != nil {
		return w, err
	}
	ok = true
	return w, nil
}

// makeDirs creates each folder of rel under out, refusing one that exists as
// a symbolic link: a link on the drop's own path would carry the drop, and
// the next sync, wherever it points.
func makeDirs(out string, rel []string) (string, error) {
	if err := os.MkdirAll(out, 0o700); err != nil {
		return "", err
	}
	dir := out
	for _, seg := range rel {
		dir = filepath.Join(dir, seg)
		fi, err := os.Lstat(dir)
		switch {
		case err == nil && fi.Mode()&os.ModeSymlink != 0:
			return "", fmt.Errorf("%s is a symbolic link; the drop is not written through one", dir)
		case err == nil && !fi.IsDir():
			return "", fmt.Errorf("%s exists and is not a folder", dir)
		case err == nil:
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return "", err
			}
		default:
			return "", err
		}
	}
	return dir, nil
}

// gzipCSV is the data file. The gzip header carries no name and no time
// (Go's writer leaves both empty unless told), so the same rows are the same
// bytes on every run, whatever the clock says.
func gzipCSV(hdr []string, records [][]string) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	zw.Header.ModTime = time.Time{}
	cw := csv.NewWriter(zw)
	if err := cw.Write(hdr); err != nil {
		return nil, err
	}
	if err := cw.WriteAll(records); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// createFile is how write puts a file down: writeNew, except in a test that
// makes a write fail part way to show the folder is not left behind.
var createFile = writeNew

// writeNew writes a file that must not exist yet, private to this account,
// and flushes it to disk before returning.
func writeNew(p string, data []byte) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// reportID is the manifest's report_id: a UUID (version 8, RFC 9562's
// application-defined form) derived from the bucket, the keys and the data's
// sha256. The keys carry the epoch, so two drops never share one, and the
// same drop described twice gets the same one.
func reportID(rootDir string, keys []string, data [32]byte) string {
	h := sha256.New()
	h.Write([]byte(rootDir + "\n" + strings.Join(keys, "\n") + "\n"))
	h.Write(data[:])
	b := h.Sum(nil)[:16]
	b[6] = b[6]&0x0f | 0x80
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

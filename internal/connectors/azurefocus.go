package connectors

// The azure-focus reader: the FOCUS export Azure Cost Management writes to a
// storage container, read from a folder the container was synced to. It runs
// on the same engine as aws-data-exports and gcp-billing-export
// (cloudfocus.go): the file is the same FOCUS shape, the money rules are the
// same, and so are the bounds on an untrusted folder. There is no Azure SDK
// and no blob client in this binary.
//
// WHAT MICROSOFT DOCUMENTS, READ 2026-11-23
//
// learn.microsoft.com, cost-management-billing/dataset-schema/
// cost-usage-details-focus: the "Cost and usage details (FOCUS)" dataset in
// versions 1.0-preview(v1), 1.0, 1.0r2 and 1.2-preview. Every one carries the
// eight columns the engine requires (BilledCost, BillingCurrency,
// ChargeCategory, ChargePeriodStart, ChargePeriodEnd, ProviderName,
// ServiceName, SubAccountId) and Tags as a JSON object. Three differences
// matter here, and each is a field of azureFocusSpec rather than a change to
// the engine, so the AWS and Google readers are not loosened by them:
//
//   - Timestamps. "a 1.0 export may use "2024-01-01T00:00Z" and a 1.0r2
//     export would use "2024-01-01T00:00:00Z"": minutes without seconds,
//     which time.RFC3339 refuses. minuteTimes accepts that one extra form.
//   - The invoice. InvoiceId is a column from 1.2-preview on ("Renamed from
//     x_InvoiceId in FOCUS 1.0"); 1.0 carries x_InvoiceId. invoiceAlt reads
//     it when InvoiceId is absent or empty. Microsoft's own FOCUS guide says
//     the invoice is not available for Enterprise Agreement accounts and is
//     filled only once an invoice exists for a Microsoft Customer Agreement,
//     so an empty invoice is ordinary and is not refused.
//   - ProviderName. The page describes the column and gives no value.
//     "Microsoft" (and "Microsoft Azure") is this reader's assumption, the
//     same kind the AWS and Google readers make and say they make; the
//     provider_names setting replaces it, so a wrong guess costs a setting.
//
// The 1.0-preview(v1) dataset describes ChargeCategory as "whether the row
// represents an upfront or recurring fee", not the FOCUS enumeration; a row
// whose category is not one of the five is refused by name, never guessed.
//
// BilledCost, not EffectiveCost, is what lands, as for every cloud reader
// here: the board is the bill. A usage row a reservation or savings plan
// covered bills 0 and carries its amortized cost in EffectiveCost, so it
// writes no charge; the reservation is a Purchase row on its own line. That
// is the actual-cost view. The amortized view is not built.
//
// EXPORT RUNS, AND WHY ONLY THE NEWEST IS READ
//
// tutorial-improved-exports, same site: the container holds
// StorageContainer/StorageDirectory/ExportName/[YYYYMMDD-YYYYMMDD]/[RunID]/
// with the parts and a manifest.json. A daily export "creates a daily export
// of costs accumulated from the start of the month to the current date".
// "Without file overwrite: You see multiple RunIDs within the month folder";
// "With file overwrite: You see only one RunID within the month folder,
// representing the latest run" (on by default). So every run folder of a
// period holds the whole period so far, and reading them all would count
// each day once per run.
//
// The rule: per period folder, only the newest run is read, newest by the
// manifest's runInfo.submittedTime and by nothing else (the folder name is a
// GUID, a file's time is whatever the sync tool left). A period with two runs
// and a manifest missing, unreadable or tied is refused by name and nothing
// of it is read. A run is one unit: if any part is refused, or a CSV part its
// manifest lists is not in the folder (a sync still running), the run is
// rolled back whole and the period keeps what the store held. Once the newest
// run has landed, the store's rows from every OTHER run of that period are
// removed, which is also what makes the overwrite mode correct: there the run
// id changes every day and a sync with deletion leaves only the new folder,
// while the store still holds yesterday's.
//
// What the rule does not do: the folder is the truth, so a single run on disk
// replaces the store's rows for its period even if an older copy of the
// container was restored over a newer one; a period folder that disappears
// from disk keeps its rows, as for every other reader here. The manifest's
// row counts are not compared with what was read. A file outside any run
// folder is read as an ordinary file, with no run rule.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var azureFocusSpec = cloudSpec{
	id: "azure-focus", source: "azure",
	providers:   []string{"Microsoft", "Microsoft Azure"},
	minuteTimes: true,
	invoiceAlt:  "x_InvoiceId",
	exportRuns:  true,
}

// azurePeriodFolder is the [YYYYMMDD-YYYYMMDD] folder of an export.
var errNoManifest = errors.New("no manifest.json; which parts a run holds, and which run is newest, " +
	"are read from it and from nothing else")

var azurePeriodFolder = regexp.MustCompile(`^[0-9]{8}-[0-9]{8}$`)

const azureManifestMaxBytes = 1 << 20

// cloudUnit is what the reading loop lands as one: a single file, or every
// CSV part of one export run.
type cloudUnit struct {
	files  []cloudFile
	period string // the period folder, relative to the root; "" for a single file
	run    string // the run folder's name under it
}

type runPlan struct {
	units           []cloudUnit
	refusals        []string
	superseded      []string // the first few, named
	supersededFiles int
}

type azureManifest struct {
	RunInfo struct {
		SubmittedTime string `json:"submittedTime"`
		RunID         string `json:"runId"`
	} `json:"runInfo"`
	Blobs []struct {
		BlobName  string `json:"blobName"`
		ByteCount *int64 `json:"byteCount"`
	} `json:"blobs"`
}

type azureRun struct {
	name      string
	files     []cloudFile
	manifest  *azureManifest
	submitted time.Time
	err       error // the manifest was there and could not be read
}

// planExportRuns groups the files into units. files is sorted, so the units
// come out in a fixed order too (invariant 7).
func planExportRuns(files []cloudFile) runPlan {
	var plan runPlan
	type period struct {
		rel  string
		root string
		runs map[string]*azureRun
	}
	periods := map[string]*period{}
	var order []string
	addPeriod := func(key, root string) *period {
		p, ok := periods[key]
		if !ok {
			p = &period{rel: key, root: root, runs: map[string]*azureRun{}}
			periods[key] = p
			order = append(order, key)
		}
		return p
	}
	for _, f := range files {
		segs := strings.Split(f.rel, "/")
		root := strings.TrimSuffix(f.abs, filepath.FromSlash(f.rel))
		// A CSV straight in a period folder, beside its run folders, is no
		// part of any run: reading it as a file of its own would count its
		// days on top of the run's, so it is refused by name.
		if len(segs) >= 2 && azurePeriodFolder.MatchString(segs[len(segs)-2]) {
			plan.refusals = append(plan.refusals, fmt.Sprintf("%s: a CSV directly in the period folder, "+
				"outside any [RunID] folder, is not read", f.rel))
			addPeriod(strings.Join(segs[:len(segs)-1], "/"), root)
			continue
		}
		at := -1
		// The deepest period folder that has a run folder and then the file
		// below it.
		for i := len(segs) - 3; i >= 0; i-- {
			if azurePeriodFolder.MatchString(segs[i]) {
				at = i
				break
			}
		}
		if at < 0 {
			plan.units = append(plan.units, cloudUnit{files: []cloudFile{f}})
			continue
		}
		p := addPeriod(strings.Join(segs[:at+1], "/"), root)
		run := segs[at+1]
		r, ok := p.runs[run]
		if !ok {
			r = &azureRun{name: run}
			p.runs[run] = r
		}
		r.files = append(r.files, f)
	}

	for _, key := range order {
		p := periods[key]
		dir := filepath.Join(p.root, filepath.FromSlash(p.rel))
		// A newer run whose parts have not arrived yet still takes part in
		// choosing the newest: run folders are listed, not inferred from the
		// CSV files found, so one holding only its manifest is seen.
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if !e.IsDir() || p.runs[e.Name()] != nil {
					continue
				}
				if _, err := os.Lstat(filepath.Join(dir, e.Name(), "manifest.json")); err == nil {
					p.runs[e.Name()] = &azureRun{name: e.Name()}
				}
			}
		}
		if len(p.runs) == 0 {
			continue
		}
		names := make([]string, 0, len(p.runs))
		for n, r := range p.runs {
			names = append(names, n)
			r.manifest, r.err = readAzureManifest(filepath.Join(dir, n))
			if r.err == nil && r.manifest == nil {
				r.err = errNoManifest
			}
			if r.err == nil {
				r.submitted, r.err = manifestTime(r.manifest)
			}
		}
		sort.Strings(names)

		var chosen *azureRun
		reason := ""
		for _, n := range names {
			r := p.runs[n]
			if r.err != nil {
				reason = fmt.Sprintf("%s: %v", n, r.err)
				break
			}
			if chosen == nil || r.submitted.After(chosen.submitted) {
				chosen = r
			}
		}
		if reason == "" {
			for _, n := range names {
				if r := p.runs[n]; r != chosen && r.submitted.Equal(chosen.submitted) {
					reason = fmt.Sprintf("runs %s and %s were submitted at the same instant (%s)",
						chosen.name, r.name, chosen.submitted.Format(time.RFC3339Nano))
					break
				}
			}
		}
		if reason != "" {
			plan.refusals = append(plan.refusals, fmt.Sprintf(
				"%s: %d export run%s, and each run's parts and submission time are read from its manifest.json: "+
					"%s; the period was not read and keeps what it held before (keep manifest.json in the sync)",
				p.rel, len(names), plural(len(names)), reason))
			continue
		}

		parts, extra, problem := manifestParts(chosen)
		if problem != "" {
			plan.refusals = append(plan.refusals, fmt.Sprintf(
				"%s/%s: export run not read, because %s; the period keeps what it held before",
				p.rel, chosen.name, problem))
			continue
		}
		for _, x := range extra {
			plan.refusals = append(plan.refusals, fmt.Sprintf("%s: not listed in its run's manifest.json, not read", x))
		}
		for _, n := range names {
			if r := p.runs[n]; r != chosen {
				plan.supersededFiles += len(r.files)
				if len(plan.superseded) < cloudRefusalsShown {
					plan.superseded = append(plan.superseded, p.rel+"/"+n)
				}
			}
		}
		plan.units = append(plan.units, cloudUnit{files: parts, period: p.rel, run: chosen.name})
	}
	return plan
}

// readAzureManifest reads dir/manifest.json. A missing file is (nil, nil),
// which the caller refuses: every run needs one. A link, something that is not a regular file, a file
// over a megabyte or one that is not JSON is an error, so it is named.
func readAzureManifest(dir string) (*azureManifest, error) {
	p := filepath.Join(dir, "manifest.json")
	st, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("manifest.json is a link or not a regular file, and is not followed")
	}
	if st.Size() > azureManifestMaxBytes {
		return nil, fmt.Errorf("manifest.json is %d bytes, over the %d byte limit", st.Size(), azureManifestMaxBytes)
	}
	fh, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	data, err := io.ReadAll(io.LimitReader(fh, azureManifestMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > azureManifestMaxBytes {
		return nil, fmt.Errorf("manifest.json is over the %d byte limit", azureManifestMaxBytes)
	}
	var m azureManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest.json is not JSON this reader understands: %v", err)
	}
	return &m, nil
}

func manifestTime(m *azureManifest) (time.Time, error) {
	if m == nil {
		return time.Time{}, nil // unreachable: a run without one is refused first
	}
	v := strings.TrimSpace(m.RunInfo.SubmittedTime)
	if v == "" {
		return time.Time{}, errors.New("manifest.json has no runInfo.submittedTime")
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		// The same manifest writes runInfo.startDate with no zone and
		// endDate with a Z (Microsoft's own example), so a time with no zone
		// is read as UTC rather than refused.
		t, err = time.Parse("2006-01-02T15:04:05.999999999", v)
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("runInfo.submittedTime %q in manifest.json is not an RFC 3339 time", v)
	}
	return t.UTC(), nil
}

// manifestParts is the run's files that its manifest lists, in the folder's
// order, and the rel paths of the files it does not list. problem is why the
// run cannot be read whole: a listed CSV part missing from the folder, or of a
// different size from the one the manifest records (a sync still running, a
// file cut short), a blob name that climbs out of its folder, or no CSV part
// listed at all.
func manifestParts(r *azureRun) (parts []cloudFile, extra []string, problem string) {
	if r.manifest == nil {
		return r.files, nil, "" // unreachable: a run without one is refused first
	}
	want := map[string]int64{} // base name -> byteCount, -1 when not given
	var order []string
	for _, b := range r.manifest.Blobs {
		name := strings.TrimSpace(b.BlobName)
		for _, seg := range strings.Split(name, "/") {
			if seg == ".." {
				return nil, nil, fmt.Sprintf("its manifest.json lists %q, a blob name that climbs out of its folder", name)
			}
		}
		base := path.Base(name)
		lower := strings.ToLower(base)
		if !strings.HasSuffix(lower, ".csv") && !strings.HasSuffix(lower, ".csv.gz") {
			continue
		}
		size := int64(-1)
		if b.ByteCount != nil {
			size = *b.ByteCount
		}
		if _, dup := want[base]; !dup {
			order = append(order, base)
		}
		want[base] = size
	}
	if len(want) == 0 {
		return nil, nil, "its manifest.json lists no CSV part (a Parquet export? this reader reads CSV and CSV.gz)"
	}
	have := map[string]cloudFile{}
	for _, f := range r.files {
		base := path.Base(f.rel)
		if _, listed := want[base]; listed {
			have[base] = f
		} else {
			extra = append(extra, f.rel)
		}
	}
	for _, base := range order {
		f, ok := have[base]
		if !ok {
			return nil, nil, fmt.Sprintf("its manifest.json lists %s and the folder does not hold it (a sync still running?)", base)
		}
		if size := want[base]; size >= 0 && f.size != size {
			return nil, nil, fmt.Sprintf("%s is %d bytes on disk and its manifest.json records %d "+
				"(a sync still running, or a file cut short?)", base, f.size, size)
		}
	}
	for _, f := range r.files {
		if _, ok := have[path.Base(f.rel)]; ok {
			parts = append(parts, f)
		}
	}
	return parts, extra, ""
}

// dropOtherRuns removes the store's rows of every run of period except run,
// and returns the days they were on so those days are rebuilt. Prefixes are
// compared with substr, not LIKE: an export or folder name may hold '_'.
func dropOtherRuns(tx *sql.Tx, connector, period, run string) ([]string, error) {
	pp := period + "/"
	rp := period + "/" + run + "/"
	// substr on a BLOB counts bytes, as len does; on TEXT it counts
	// characters, and a non-ASCII folder name would compare the wrong prefix.
	// path >= pp AND path < pp+"\xff" is the same prefix under SQLite's
	// BINARY collation, and lets the (connector, path) index narrow the scan.
	const where = `connector=? AND path >= ? AND path < ?
		AND substr(CAST(path AS BLOB),1,?)=CAST(? AS BLOB)
		AND substr(CAST(path AS BLOB),1,?)<>CAST(? AS BLOB)`
	args := []any{connector, pp, pp + "\xff", len(pp), pp, len(rp), rp}
	rows, err := tx.Query(`SELECT DISTINCT day FROM cloud_focus_rows WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	var days []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			rows.Close()
			return nil, err
		}
		days = append(days, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if _, err := tx.Exec(`DELETE FROM cloud_focus_rows WHERE `+where, args...); err != nil {
		return nil, err
	}
	return days, nil
}

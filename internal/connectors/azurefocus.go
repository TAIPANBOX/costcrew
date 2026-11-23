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
		BlobName string `json:"blobName"`
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
	for _, f := range files {
		segs := strings.Split(f.rel, "/")
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
		key := strings.Join(segs[:at+1], "/")
		p, ok := periods[key]
		if !ok {
			p = &period{rel: key, root: strings.TrimSuffix(f.abs, filepath.FromSlash(f.rel)),
				runs: map[string]*azureRun{}}
			periods[key] = p
			order = append(order, key)
		}
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
		names := make([]string, 0, len(p.runs))
		for n, r := range p.runs {
			names = append(names, n)
			r.manifest, r.err = readAzureManifest(filepath.Join(p.root, filepath.FromSlash(p.rel), n))
			if r.err == nil && r.manifest != nil {
				r.submitted, r.err = manifestTime(r.manifest)
			}
		}
		sort.Strings(names)

		var chosen *azureRun
		if len(names) == 1 {
			chosen = p.runs[names[0]]
			if chosen.err != nil {
				plan.refusals = append(plan.refusals, fmt.Sprintf("%s/%s/manifest.json: %v; the period was not read",
					p.rel, chosen.name, chosen.err))
				continue
			}
		} else {
			reason := ""
			for _, n := range names {
				r := p.runs[n]
				switch {
				case r.err != nil:
					reason = fmt.Sprintf("%s/manifest.json: %v", n, r.err)
				case r.manifest == nil:
					reason = fmt.Sprintf("%s has no manifest.json", n)
				}
				if reason != "" {
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
					"%s: %d export runs, and which is newest is read from each manifest.json's runInfo.submittedTime "+
						"and nothing else: %s; the period was not read (turn on Overwrite data in the export, "+
						"or sync the manifests)", p.rel, len(names), reason))
				continue
			}
		}

		if missing := missingParts(chosen); missing != "" {
			plan.refusals = append(plan.refusals, fmt.Sprintf(
				"%s/%s: export run not read, because its manifest.json lists %s and the folder does not hold it "+
					"(a sync still running?); the period keeps what it held before", p.rel, chosen.name, missing))
			continue
		}
		for _, n := range names {
			if r := p.runs[n]; r != chosen {
				plan.supersededFiles += len(r.files)
				if len(plan.superseded) < cloudRefusalsShown {
					plan.superseded = append(plan.superseded, p.rel+"/"+n)
				}
			}
		}
		plan.units = append(plan.units, cloudUnit{files: chosen.files, period: p.rel, run: chosen.name})
	}
	return plan
}

// readAzureManifest reads dir/manifest.json. A missing file is (nil, nil): a
// lone run needs none. A link, something that is not a regular file, a file
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
	v := strings.TrimSpace(m.RunInfo.SubmittedTime)
	if v == "" {
		return time.Time{}, errors.New("manifest.json has no runInfo.submittedTime")
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("runInfo.submittedTime %q in manifest.json is not an RFC 3339 time", v)
	}
	return t.UTC(), nil
}

// missingParts names the first CSV part the run's manifest lists that the
// folder does not hold, or a blob name that climbs out of its folder. A run
// with no manifest has nothing to check against.
func missingParts(r *azureRun) string {
	if r.manifest == nil {
		return ""
	}
	have := map[string]bool{}
	for _, f := range r.files {
		have[path.Base(f.rel)] = true
	}
	for _, b := range r.manifest.Blobs {
		name := strings.TrimSpace(b.BlobName)
		for _, seg := range strings.Split(name, "/") {
			if seg == ".." {
				return fmt.Sprintf("%q, a blob name that climbs out of its folder", name)
			}
		}
		base := path.Base(name)
		lower := strings.ToLower(base)
		if !strings.HasSuffix(lower, ".csv") && !strings.HasSuffix(lower, ".csv.gz") {
			continue
		}
		if !have[base] {
			return base
		}
	}
	return ""
}

// dropOtherRuns removes the store's rows of every run of period except run,
// and returns the days they were on so those days are rebuilt. Prefixes are
// compared with substr, not LIKE: an export or folder name may hold '_'.
func dropOtherRuns(tx *sql.Tx, connector, period, run string) ([]string, error) {
	pp := period + "/"
	rp := period + "/" + run + "/"
	// substr on a BLOB counts bytes, as len does; on TEXT it counts
	// characters, and a non-ASCII folder name would compare the wrong prefix.
	const where = `connector=? AND substr(CAST(path AS BLOB),1,?)=CAST(? AS BLOB)
		AND substr(CAST(path AS BLOB),1,?)<>CAST(? AS BLOB)`
	args := []any{connector, len(pp), pp, len(rp), rp}
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

package main

import (
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// The two header shapes `tokenfuse focus-export` writes: 26 columns before
// TokenFuse 1.7.0, and 29 after it appended x_key_id, x_block_reason and
// x_identity_reason (crates/gateway/src/focusexport.rs, HEADER).
const (
	tfHeader26 = "BilledCost,EffectiveCost,BillingCurrency,ChargePeriodStart,ChargePeriodEnd," +
		"ChargeDescription,ProviderName,PublisherName,InvoiceIssuerName,ServiceName,ServiceCategory," +
		"ResourceId,ResourceName,SubAccountId,SubAccountName,x_run_id,x_parent_run_id,x_agent_id," +
		"x_model,x_tokens_in,x_tokens_out,x_blocked,x_cost_basis,x_outcome,x_unit,x_tool_calls"
	tfHeader29 = tfHeader26 + ",x_key_id,x_block_reason,x_identity_reason"
)

// call is one model call as the gateway exports it. Empty fields take the
// value TokenFuse writes for an ordinary settled call.
type call struct {
	at       string // the settle instant, ChargePeriodStart and ChargePeriodEnd
	run      string
	agent    string // x_agent_id; empty for an identity refusal
	resource string // ResourceId; the agent when empty
	model    string
	unit     string
	key      string
	cost     string
	in, out  string
	blocked  bool
	reason   string // x_block_reason
	basis    string // settled, estimated or blocked
	currency string
	provider string
}

func (c call) fields(newColumns bool) []string {
	or := func(v, d string) string {
		if v == "" {
			return d
		}
		return v
	}
	resource := or(c.resource, c.agent)
	basis := or(c.basis, "settled")
	blocked := "false"
	if c.blocked {
		blocked, basis = "true", or(c.basis, "blocked")
	}
	provider := or(c.provider, "Anthropic")
	if c.provider == "-" { // a hostile export that names no provider
		provider = ""
	}
	cost := or(c.cost, "0.010000")
	run := or(c.run, "run-1")
	f := []string{
		cost, cost, or(c.currency, "USD"), c.at, c.at,
		"LLM call model=" + c.model, provider, provider, provider,
		"LLM inference", "AI and Machine Learning",
		resource, resource, run, run, run, "", c.agent,
		or(c.model, "claude-haiku-4-5"), or(c.in, "100"), or(c.out, "50"), blocked, basis, "", c.unit, "0",
	}
	if newColumns {
		f = append(f, c.key, c.reason, "")
	}
	return f
}

// writeExport writes calls as one TokenFuse export file, through
// encoding/csv so a hostile value is quoted the way the gateway quotes it.
func writeExport(t *testing.T, dir, name string, newColumns bool, calls ...call) string {
	t.Helper()
	var b bytes.Buffer
	if newColumns {
		b.WriteString(tfHeader29 + "\n")
	} else {
		b.WriteString(tfHeader26 + "\n")
	}
	w := csv.NewWriter(&b)
	for _, c := range calls {
		if err := w.Write(c.fields(newColumns)); err != nil {
			t.Fatal(err)
		}
	}
	w.Flush()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeRaw(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// at is the clock every test runs the tool under unless it says otherwise:
// 13 October 2026, noon UTC.
var at = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

type result struct {
	code           int
	stdout, stderr string
}

// baseFlags is a drop for October 2026 into byod-input-prod.
func baseFlags(in, out string) []string {
	return []string{"-in", in, "-out", out, "-root-dir", "byod-input-prod",
		"-billing-account-id", "acme-tokenfuse-gateway", "-month", "2026-10"}
}

func runTool(t *testing.T, when time.Time, args ...string) result {
	t.Helper()
	var so, se bytes.Buffer
	code := run(args, &so, &se, func() time.Time { return when })
	return result{code, so.String(), se.String()}
}

// dropAt runs the tool with the base flags plus extra, and fails the test
// unless it wrote a drop.
func dropAt(t *testing.T, in, out string, when time.Time, extra ...string) result {
	t.Helper()
	r := runTool(t, when, append(baseFlags(in, out), extra...)...)
	if r.code != 0 {
		t.Fatalf("the tool refused to write a drop (exit %d)\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	return r
}

type manifestDoc struct {
	Compression   string   `json:"compression"`
	ContentType   string   `json:"content_type"`
	ReportID      string   `json:"report_id"`
	RootDir       string   `json:"root_dir"`
	AllReportKeys []string `json:"all_report_keys"`
	UpdatedAt     string   `json:"updated_at"`
	FocusVersion  string   `json:"focus_version"`
}

// dropFiles is one drop as the tool left it on disk, read back the way a
// consumer reads it: the manifest by its documented path, the data by the
// keys the manifest lists.
type dropFiles struct {
	epochDir    string
	manifestRaw []byte
	manifest    manifestDoc
	data        []byte // the .csv.gz exactly as written
	header      []string
	records     [][]string
	rows        []map[string]string
}

// readDrop opens the drop for October 2026 written at when, with the default
// vendor and prefix.
func readDrop(t *testing.T, out string, when time.Time) dropFiles {
	t.Helper()
	return readDropAt(t, out, "", "TokenFuse", "20261001-20261101", "costcrew", when)
}

func readDropAt(t *testing.T, out, subDir, vendor, period, prefix string, when time.Time) dropFiles {
	t.Helper()
	var d dropFiles
	d.epochDir = filepath.Join(out, filepath.FromSlash(subDir), vendor, period, fmt.Sprint(when.Unix()))
	raw, err := os.ReadFile(filepath.Join(d.epochDir, prefix+"-Manifest.json"))
	if err != nil {
		t.Fatalf("no manifest where Cloudability looks for one: %v", err)
	}
	d.manifestRaw = raw
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d.manifest); err != nil {
		t.Fatalf("the manifest does not decode into the seven documented fields: %v\n%s", err, raw)
	}
	if len(d.manifest.AllReportKeys) != 1 {
		t.Fatalf("all_report_keys = %q, want exactly one data file", d.manifest.AllReportKeys)
	}
	// Checked before the key is followed: Cloudability resolves a key inside
	// root_dir, so a key that names the bucket again points at nothing there.
	for _, k := range d.manifest.AllReportKeys {
		if strings.HasPrefix(k, "/") || strings.HasPrefix(k, d.manifest.RootDir+"/") {
			t.Fatalf("report key %q carries the root directory or a leading slash; Cloudability's keys exclude root_dir", k)
		}
	}
	d.data, err = os.ReadFile(filepath.Join(out, filepath.FromSlash(d.manifest.AllReportKeys[0])))
	if err != nil {
		t.Fatalf("the manifest's key %q names no file under the drop folder: %v", d.manifest.AllReportKeys[0], err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(d.data))
	if err != nil {
		t.Fatalf("the data file is not gzip: %v", err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("the data file does not inflate: %v", err)
	}
	cr := csv.NewReader(bytes.NewReader(plain))
	all, err := cr.ReadAll()
	if err != nil {
		t.Fatalf("the data file is not CSV: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("the data file has no header")
	}
	d.header, d.records = all[0], all[1:]
	for _, rec := range d.records {
		m := map[string]string{}
		for i, h := range d.header {
			m[h] = rec[i]
		}
		d.rows = append(d.rows, m)
	}
	return d
}

// filesUnder lists every regular file under dir, relative and sorted; a
// directory that does not exist holds none.
func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// rowFor finds the one row for an agent and model on a day, failing the test
// when there is not exactly one.
func rowFor(t *testing.T, d dropFiles, day, agent, model string) map[string]string {
	t.Helper()
	var hits []map[string]string
	for _, r := range d.rows {
		if strings.HasPrefix(r["ChargePeriodStart"], day) && r["ResourceId"] == agent && r["x_Model"] == model {
			hits = append(hits, r)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("%d rows for %s on %s with %s, want 1; rows: %v", len(hits), agent, day, model, d.rows)
	}
	return hits[0]
}

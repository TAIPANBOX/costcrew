package connectors

// Tests for the azure-focus reader: the FOCUS export Azure Cost Management
// writes into a storage container, synced to a folder. They go through the
// package's public door only (Save, Import, Test, Get) and plain SQL, so they
// compile against the code as it was before the reader existed and fail
// there by behaviour: Import answers "no live account is connected".
//
// The fixture (testdata/azure-focus-1-2-preview-sample.csv) carries the 53
// non-x_ columns of Microsoft's "Cost and usage details (FOCUS)" dataset,
// version 1.2-preview, in the order learn.microsoft.com lists them
// (cost-management-billing/dataset-schema/cost-usage-details-focus, read
// 2026-11-23), plus x_AmortizationClass. Every id, amount and name in it was
// typed for the test. ProviderName "Microsoft" is the fixture's assumption:
// the page describes the column and gives no value, which is why the
// provider_names setting exists.
//
// The folder layout (StorageContainer/StorageDirectory/ExportName/
// [YYYYMMDD-YYYYMMDD]/[RunID]/, a manifest.json beside the parts) and the
// manifest's runInfo.submittedTime and blobs fields are from the same site's
// tutorial-improved-exports page, read 2026-11-23.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const azureFocusFixture = "testdata/azure-focus-1-2-preview-sample.csv"

// azRow is one valid daily row in the FOCUS 1.0 shape Azure writes: minute
// timestamps (1.0 before the 1.0r2 revision) and the invoice in x_InvoiceId.
func azRow(over map[string]string) map[string]string {
	r := map[string]string{
		"BilledCost": "1.00", "BillingCurrency": "USD", "ChargeCategory": "Usage",
		"ChargeClass": "", "ChargePeriodStart": "2026-10-01T00:00Z",
		"ChargePeriodEnd": "2026-10-02T00:00Z", "x_InvoiceId": "G000000002",
		"ProviderName": "Microsoft", "ServiceName": "Virtual Machines",
		"SubAccountId": "/subscriptions/11111111-1111-1111-1111-111111111111",
		"Tags":         `{"team":"payments"}`, "x_AmortizationClass": "Amortized Charge",
	}
	for k, v := range over {
		r[k] = v
	}
	return r
}

var azCols10 = []string{
	"BilledCost", "BillingCurrency", "ChargeCategory", "ChargeClass", "ChargePeriodEnd",
	"ChargePeriodStart", "ProviderName", "ServiceName", "SubAccountId", "Tags",
	"x_AmortizationClass", "x_InvoiceId",
}

// azRun writes one export run: dir/costs/focus-daily/<period>/<run>/part_0_N.csv
// for each body, and a manifest.json naming every part with its byte count,
// submitted at the given time. A submitted time of "" writes no manifest.
func azRun(t *testing.T, dir, period, run, submitted string, bodies ...string) {
	t.Helper()
	var blobs []azBlob
	for i, b := range bodies {
		name := fmt.Sprintf("part_0_%04d.csv", i+1)
		cfWrite(t, dir, filepath.Join("costs", "focus-daily", period, run, name), b)
		blobs = append(blobs, azBlob{name, int64(len(b))})
	}
	if submitted == "" {
		return
	}
	azManifest(t, dir, period, run, submitted, blobs)
}

// azBlob is one blob a manifest lists; a size below zero leaves byteCount out.
type azBlob struct {
	name string
	size int64
}

func azManifest(t *testing.T, dir, period, run, submitted string, blobs []azBlob) {
	t.Helper()
	var parts []string
	for _, b := range blobs {
		count := ""
		if b.size >= 0 {
			count = fmt.Sprintf(`, "byteCount": %d`, b.size)
		}
		parts = append(parts, fmt.Sprintf(`{"blobName": "costs/focus-daily/%s/%s/%s"%s, "dataRowCount": 1}`,
			period, run, b.name, count))
	}
	m := fmt.Sprintf(`{
  "manifestVersion": "2024-04-01",
  "byteCount": 1, "blobCount": %d, "dataRowCount": 1,
  "exportConfig": {"exportName": "focus-daily", "type": "FocusCost", "timeFrame": "MonthToDate"},
  "deliveryConfig": {"partitionData": true, "dataOverwriteBehavior": "CreateNewReport", "fileFormat": "Csv", "compressionMode": "None"},
  "runInfo": {"executionType": "Scheduled", "submittedTime": %q, "runId": %q,
              "startDate": "2026-10-01T00:00:00", "endDate": "2026-10-31T00:00:00Z"},
  "blobs": [%s]
}`, len(blobs), submitted, run, strings.Join(parts, ", "))
	cfWrite(t, dir, filepath.Join("costs", "focus-daily", period, run, "manifest.json"), m)
}

const (
	azPeriod = "20261001-20261031"
	azRunOld = "aaaaaaaa-0000-0000-0000-000000000001"
	azRunNew = "bbbbbbbb-0000-0000-0000-000000000002"
)

// TestAzureCostManagementFocusIsRead: a FOCUS export written by Azure Cost
// Management lands in charges under the azure desk, every row marked with the
// connector as its provenance, money summed in exact micros and rounded to
// cents once. A usage row a reservation covered bills 0 (its cost is in
// EffectiveCost, amortized), so it writes no charge; the reservation itself
// is a Purchase on its own line, never usage; a credit stays negative.
func TestAzureCostManagementFocusIsRead(t *testing.T) {
	msg, db, err := cfImportFresh(t, "azure-focus", copyIntoDir(t, azureFocusFixture), nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	cfWant(t, cfCharges(t, db, "azure-focus"), []string{
		"azure|2026-10-01|Storage|data-eng|Usage|1|G000000001",
		"azure|2026-10-01|Virtual Machines|-|Purchase|73000|G000000001",
		"azure|2026-10-01|Virtual Machines|payments|Usage|1235|G000000001",
		"azure|2026-10-02|Virtual Machines|payments|Credit|-500|G000000001",
	})
	for _, want := range []string{"Read 1 file", "6 rows", "737.35 net BilledCost",
		"Purchase 730.00", "1 row has a negative BilledCost"} {
		if !strings.Contains(msg, want) {
			t.Errorf("sentence does not say %q:\n%s", want, msg)
		}
	}
	if n := cfCount(db, `SELECT COUNT(*) FROM charges WHERE provenance IS NULL`); n != 0 {
		t.Errorf("%d charges carry no provenance, so they read as generated", n)
	}
}

// TestAnAzureFocus10FileIsReadWithMinuteTimestampsAndXInvoiceId: Azure's
// FOCUS 1.0 dataset writes "2024-01-01T00:00Z" (no seconds; 1.0r2 added
// them) and carries the invoice as x_InvoiceId, since InvoiceId became a
// FOCUS column only in 1.2. Both are read.
func TestAnAzureFocus10FileIsReadWithMinuteTimestampsAndXInvoiceId(t *testing.T) {
	dir := t.TempDir()
	cfWrite(t, dir, "focus-1-0.csv", cfCSV(azCols10,
		azRow(nil),
		azRow(map[string]string{"BilledCost": "2.50", "ChargePeriodStart": "2026-10-02T00:00Z",
			"ChargePeriodEnd": "2026-10-03T00:00Z"})))
	_, db, err := cfImportFresh(t, "azure-focus", dir, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	cfWant(t, cfCharges(t, db, "azure-focus"), []string{
		"azure|2026-10-01|Virtual Machines|payments|Usage|100|G000000002",
		"azure|2026-10-02|Virtual Machines|payments|Usage|250|G000000002",
	})
}

// TestTheMinuteTimestampIsAzuresAloneAndTheAWSReaderStaysStrict: the
// minute form is accepted for the azure-focus reader only; the AWS reader
// still refuses it by name, so widening one connector did not loosen another.
func TestTheMinuteTimestampIsAzuresAloneAndTheAWSReaderStaysStrict(t *testing.T) {
	dir := t.TempDir()
	cfWrite(t, dir, "aws.csv", cfCSV(cfCols, cfRow(map[string]string{
		"ChargePeriodStart": "2026-09-01T00:00Z", "ChargePeriodEnd": "2026-09-02T00:00Z"})))
	msg, db, err := cfImportFresh(t, "aws-data-exports", dir, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if n := len(cfCharges(t, db, "aws-data-exports")); n != 0 {
		t.Errorf("the AWS reader accepted a minute timestamp: %d charges", n)
	}
	if !strings.Contains(msg, "does not parse as RFC 3339") {
		t.Errorf("the AWS refusal does not name the timestamp: %s", msg)
	}
}

// TestTheAzureReaderIsBuiltAndAsksForAFolder: the catalogue entry is Built
// (a reader is registered) and its first input is the synced folder.
func TestTheAzureReaderIsBuiltAndAsksForAFolder(t *testing.T) {
	c, ok := Get("azure-focus")
	if !ok {
		t.Fatal("azure-focus is not in the catalogue")
	}
	if c.Status != Built {
		t.Errorf("azure-focus is %q, want built", c.Status)
	}
	if len(c.Inputs) == 0 || c.Inputs[0].Name != "path" {
		t.Errorf("first input is not the folder path: %+v", c.Inputs)
	}
	for _, in := range c.Inputs {
		if in.Name == "container" {
			t.Errorf("still asks for a blob container, which the reader never opens")
		}
	}
	st := openFocusStore(t)
	cfSave(t, st.DB(), "azure-focus", copyIntoDir(t, azureFocusFixture), nil)
	res, ok, err := Test(st.DB(), "azure-focus", func(string) string { return "" })
	if err != nil || !ok {
		t.Errorf("Test with only the folder set: ok=%v err=%v %s", ok, err, res)
	}
}

// TestOnlyTheNewestExportRunOfAPeriodIsCounted: with "Overwrite data" off,
// every daily run of a month-to-date export writes a new run folder holding
// the month so far, so a synced container holds the same days several times.
// Only the newest run of each period, by its manifest's submittedTime, is
// read; the older one is named as superseded, never added.
func TestOnlyTheNewestExportRunOfAPeriodIsCounted(t *testing.T) {
	dir := t.TempDir()
	azRun(t, dir, azPeriod, azRunOld, "2026-10-02T06:00:00.1234567Z",
		cfCSV(azCols10, azRow(nil)))
	azRun(t, dir, azPeriod, azRunNew, "2026-10-03T06:00:00.7654321Z",
		cfCSV(azCols10, azRow(nil), azRow(map[string]string{"BilledCost": "2.00",
			"ChargePeriodStart": "2026-10-02T00:00Z", "ChargePeriodEnd": "2026-10-03T00:00Z"})))
	msg, db, err := cfImportFresh(t, "azure-focus", dir, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	cfWant(t, cfCharges(t, db, "azure-focus"), []string{
		"azure|2026-10-01|Virtual Machines|payments|Usage|100|G000000002",
		"azure|2026-10-02|Virtual Machines|payments|Usage|200|G000000002",
	})
	if !strings.Contains(msg, "superseded") || !strings.Contains(msg, azRunOld) {
		t.Errorf("the sentence does not name the superseded run:\n%s", msg)
	}
}

// TestARunReplacedOnDiskReplacesItsRowsInTheStore: with "Overwrite data" on
// (Azure's default) the container keeps one run per period, under a new run
// id each day, and a sync with deletion drops the old folder. The store still
// holds the old run's rows; the new run replaces them, days only the old run
// had included, instead of counting the period twice.
func TestARunReplacedOnDiskReplacesItsRowsInTheStore(t *testing.T) {
	dir := t.TempDir()
	azRun(t, dir, azPeriod, azRunOld, "2026-10-02T06:00:00Z",
		cfCSV(azCols10, azRow(nil), azRow(map[string]string{"BilledCost": "9.00",
			"ChargePeriodStart": "2026-10-05T00:00Z", "ChargePeriodEnd": "2026-10-06T00:00Z"})))
	_, db, err := cfImportFresh(t, "azure-focus", dir, nil)
	if err != nil {
		t.Fatalf("first Import: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "costs", "focus-daily", azPeriod, azRunOld)); err != nil {
		t.Fatal(err)
	}
	azRun(t, dir, azPeriod, azRunNew, "2026-10-03T06:00:00Z",
		cfCSV(azCols10, azRow(map[string]string{"BilledCost": "1.50"})))
	if _, err := Import(db, "azure-focus", false, ImportOptions{}); err != nil {
		t.Fatalf("second Import: %v", err)
	}
	cfWant(t, cfCharges(t, db, "azure-focus"), []string{
		"azure|2026-10-01|Virtual Machines|payments|Usage|150|G000000002",
	})
}

// TestTwoRunsWithoutAManifestAreRefusedNotGuessed: which of two runs is newer
// is read from the manifest and from nothing else (a folder name is a GUID, a
// file's time is whatever the sync tool left). Without it the period is
// refused by name and nothing of it is imported.
func TestTwoRunsWithoutAManifestAreRefusedNotGuessed(t *testing.T) {
	dir := t.TempDir()
	azRun(t, dir, azPeriod, azRunOld, "2026-10-02T06:00:00Z", cfCSV(azCols10, azRow(nil)))
	azRun(t, dir, azPeriod, azRunNew, "", cfCSV(azCols10, azRow(nil)))
	msg, db, err := cfImportFresh(t, "azure-focus", dir, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := cfCharges(t, db, "azure-focus"); len(got) != 0 {
		t.Errorf("a period with two runs and no way to order them was imported: %v", got)
	}
	if !strings.Contains(msg, azPeriod) || !strings.Contains(msg, "manifest.json") {
		t.Errorf("the refusal does not name the period and the manifest:\n%s", msg)
	}
}

// TestARunMissingAPartItsManifestListsIsRefusedAndTheEarlierRunStands: the
// manifest lists every part of a run. A run whose folder lacks one (a sync
// still in progress) is not read, and the period keeps what the store held.
func TestARunMissingAPartItsManifestListsIsRefusedAndTheEarlierRunStands(t *testing.T) {
	dir := t.TempDir()
	azRun(t, dir, azPeriod, azRunOld, "2026-10-02T06:00:00Z", cfCSV(azCols10, azRow(nil)))
	_, db, err := cfImportFresh(t, "azure-focus", dir, nil)
	if err != nil {
		t.Fatalf("first Import: %v", err)
	}
	cfWrite(t, dir, filepath.Join("costs", "focus-daily", azPeriod, azRunNew, "part_0_0001.csv"),
		cfCSV(azCols10, azRow(map[string]string{"BilledCost": "7.00"})))
	part1 := cfCSV(azCols10, azRow(map[string]string{"BilledCost": "7.00"}))
	azManifest(t, dir, azPeriod, azRunNew, "2026-10-03T06:00:00Z",
		[]azBlob{{"part_0_0001.csv", int64(len(part1))}, {"part_0_0002.csv", 100}})
	msg, err := Import(db, "azure-focus", false, ImportOptions{})
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	cfWant(t, cfCharges(t, db, "azure-focus"), []string{
		"azure|2026-10-01|Virtual Machines|payments|Usage|100|G000000002",
	})
	if !strings.Contains(msg, "part_0_0002.csv") {
		t.Errorf("the refusal does not name the missing part:\n%s", msg)
	}
}

// TestARunWithARefusedPartIsRolledBackWhole: a run is one unit. If any of its
// parts is refused, none of it lands, and the earlier run of the period
// stands, so the period is never half new and half old.
func TestARunWithARefusedPartIsRolledBackWhole(t *testing.T) {
	dir := t.TempDir()
	azRun(t, dir, azPeriod, azRunOld, "2026-10-02T06:00:00Z", cfCSV(azCols10, azRow(nil)))
	_, db, err := cfImportFresh(t, "azure-focus", dir, nil)
	if err != nil {
		t.Fatalf("first Import: %v", err)
	}
	broken := cfCSV([]string{"BilledCost", "ServiceName"}, map[string]string{"BilledCost": "1", "ServiceName": "x"})
	azRun(t, dir, azPeriod, azRunNew, "2026-10-03T06:00:00Z",
		cfCSV(azCols10, azRow(map[string]string{"BilledCost": "7.00"})), broken)
	msg, err := Import(db, "azure-focus", false, ImportOptions{})
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	cfWant(t, cfCharges(t, db, "azure-focus"), []string{
		"azure|2026-10-01|Virtual Machines|payments|Usage|100|G000000002",
	})
	if !strings.Contains(msg, azRunNew) || !strings.Contains(msg, "missing required column") {
		t.Errorf("the refusal does not name the run and its broken part:\n%s", msg)
	}
}

// TestHostileManifestsAreRefusedByName: a manifest that cannot order two runs
// refuses their period by name; nothing is guessed and nothing is read.
func TestHostileManifestsAreRefusedByName(t *testing.T) {
	cases := []struct {
		name, manifest, want string
		link                 bool
	}{
		{"not JSON", `{"runInfo": `, "manifest.json", false},
		{"no submittedTime", `{"runInfo": {"runId": "x"}, "blobs": []}`, "submittedTime", false},
		{"submittedTime not a time", `{"runInfo": {"submittedTime": "yesterday"}, "blobs": []}`, "submittedTime", false},
		{"over a megabyte", `{"runInfo": {"submittedTime": "2026-10-03T06:00:00Z"}, "pad": "` +
			strings.Repeat("x", 1<<20) + `"}`, "manifest.json", false},
		{"a blob path that climbs out", `{"runInfo": {"submittedTime": "2026-10-03T06:00:00Z"}, "blobs": [{"blobName": "../../etc/passwd"}]}`, "passwd", false},
		{"a link instead of a file", "", "manifest.json", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			azRun(t, dir, azPeriod, azRunOld, "2026-10-02T06:00:00Z", cfCSV(azCols10, azRow(nil)))
			part := cfCSV(azCols10, azRow(map[string]string{"BilledCost": "7.00"}))
			cfWrite(t, dir, filepath.Join("costs", "focus-daily", azPeriod, azRunNew, "part_0_0001.csv"), part)
			mp := filepath.Join(dir, "costs", "focus-daily", azPeriod, azRunNew, "manifest.json")
			if c.link {
				// The link points at a well-formed manifest NEWER than the old
				// run's, listing the new run's part at its true size, so
				// following it would read the new run: only the refusal to
				// follow a link keeps the period out.
				target := cfWrite(t, dir, filepath.Join("elsewhere", "manifest.json"), fmt.Sprintf(
					`{"runInfo": {"submittedTime": "2026-10-09T06:00:00Z"}, "blobs": [{"blobName": "part_0_0001.csv", "byteCount": %d}]}`,
					len(part)))
				if err := os.Symlink(target, mp); err != nil {
					t.Skip("no symlinks here")
				}
			} else {
				cfWrite(t, dir, filepath.Join("costs", "focus-daily", azPeriod, azRunNew, "manifest.json"), c.manifest)
			}
			msg, db, err := cfImportFresh(t, "azure-focus", dir, nil)
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			if got := cfCharges(t, db, "azure-focus"); len(got) != 0 {
				t.Errorf("a period whose runs cannot be ordered was imported: %v", got)
			}
			if !strings.Contains(msg, c.want) || !strings.Contains(msg, azPeriod) {
				t.Errorf("the refusal does not name %q and the period:\n%s", c.want, msg)
			}
		})
	}
}

// TestTwoRunsSubmittedAtTheSameInstantAreRefused: a tie cannot say which run
// is newer, so the period is refused rather than one picked by name order.
func TestTwoRunsSubmittedAtTheSameInstantAreRefused(t *testing.T) {
	dir := t.TempDir()
	azRun(t, dir, azPeriod, azRunOld, "2026-10-02T06:00:00Z", cfCSV(azCols10, azRow(nil)))
	azRun(t, dir, azPeriod, azRunNew, "2026-10-02T06:00:00Z", cfCSV(azCols10, azRow(nil)))
	msg, db, err := cfImportFresh(t, "azure-focus", dir, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := cfCharges(t, db, "azure-focus"); len(got) != 0 {
		t.Errorf("a tie was broken by guessing: %v", got)
	}
	if !strings.Contains(msg, "same instant") {
		t.Errorf("the refusal does not say why:\n%s", msg)
	}
}

// TestRunsOfDifferentPeriodsAreIndependent: the newest-run rule is per period
// folder. September's run and October's run are both read.
func TestRunsOfDifferentPeriodsAreIndependent(t *testing.T) {
	dir := t.TempDir()
	azRun(t, dir, "20260901-20260930", azRunOld, "2026-10-01T06:00:00Z",
		cfCSV(azCols10, azRow(map[string]string{"ChargePeriodStart": "2026-09-30T00:00Z",
			"ChargePeriodEnd": "2026-10-01T00:00Z"})))
	azRun(t, dir, azPeriod, azRunNew, "2026-10-02T06:00:00Z", cfCSV(azCols10, azRow(nil)))
	_, db, err := cfImportFresh(t, "azure-focus", dir, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	cfWant(t, cfCharges(t, db, "azure-focus"), []string{
		"azure|2026-09-30|Virtual Machines|payments|Usage|100|G000000002",
		"azure|2026-10-01|Virtual Machines|payments|Usage|100|G000000002",
	})
}

// azFolder is the path of a run folder under dir.
func azFolder(dir, period, run string) string {
	return filepath.Join(dir, "costs", "focus-daily", period, run)
}

// TestTheNewestRunIsChosenByItsManifestNotItsName: the newer run's id sorts
// FIRST here, so an implementation that took the last folder by name would
// read the older run.
func TestTheNewestRunIsChosenByItsManifestNotItsName(t *testing.T) {
	dir := t.TempDir()
	azRun(t, dir, azPeriod, "zzzzzzzz-0000-0000-0000-000000000009", "2026-10-02T06:00:00Z",
		cfCSV(azCols10, azRow(map[string]string{"BilledCost": "9.00"})))
	azRun(t, dir, azPeriod, "00000000-0000-0000-0000-000000000001", "2026-10-03T06:00:00Z",
		cfCSV(azCols10, azRow(map[string]string{"BilledCost": "3.00"})))
	_, db, err := cfImportFresh(t, "azure-focus", dir, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	cfWant(t, cfCharges(t, db, "azure-focus"), []string{
		"azure|2026-10-01|Virtual Machines|payments|Usage|300|G000000002",
	})
}

// TestALoneRunWithoutAManifestDoesNotReplaceTheMonth: with overwrite on, the
// folder holds one run; if its manifest has not been synced, whether its
// parts are all there cannot be told, so it does not replace what the store
// holds for the month.
func TestALoneRunWithoutAManifestDoesNotReplaceTheMonth(t *testing.T) {
	dir := t.TempDir()
	azRun(t, dir, azPeriod, azRunOld, "2026-10-02T06:00:00Z", cfCSV(azCols10, azRow(nil)))
	_, db, err := cfImportFresh(t, "azure-focus", dir, nil)
	if err != nil {
		t.Fatalf("first Import: %v", err)
	}
	if err := os.RemoveAll(azFolder(dir, azPeriod, azRunOld)); err != nil {
		t.Fatal(err)
	}
	azRun(t, dir, azPeriod, azRunNew, "", cfCSV(azCols10, azRow(map[string]string{"BilledCost": "0.50"})))
	msg, err := Import(db, "azure-focus", false, ImportOptions{})
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	cfWant(t, cfCharges(t, db, "azure-focus"), []string{
		"azure|2026-10-01|Virtual Machines|payments|Usage|100|G000000002",
	})
	if !strings.Contains(msg, "manifest.json") {
		t.Errorf("the refusal does not name the missing manifest:\n%s", msg)
	}
}

// TestARunWhosePartIsShorterThanItsManifestSaysIsRefused: a plain CSV cut
// short by a sync still running parses as a smaller valid file; the byte
// count the manifest records is what tells.
func TestARunWhosePartIsShorterThanItsManifestSaysIsRefused(t *testing.T) {
	dir := t.TempDir()
	azRun(t, dir, azPeriod, azRunOld, "2026-10-02T06:00:00Z", cfCSV(azCols10, azRow(nil)))
	_, db, err := cfImportFresh(t, "azure-focus", dir, nil)
	if err != nil {
		t.Fatalf("first Import: %v", err)
	}
	full := cfCSV(azCols10, azRow(map[string]string{"BilledCost": "4.00"}),
		azRow(map[string]string{"BilledCost": "5.00", "ChargePeriodStart": "2026-10-02T00:00Z",
			"ChargePeriodEnd": "2026-10-03T00:00Z"}))
	cut := full[:strings.LastIndex(strings.TrimSuffix(full, "\n"), "\n")+1] // the last row has not arrived
	cfWrite(t, dir, filepath.Join("costs", "focus-daily", azPeriod, azRunNew, "part_0_0001.csv"), cut)
	azManifest(t, dir, azPeriod, azRunNew, "2026-10-03T06:00:00Z", []azBlob{{"part_0_0001.csv", int64(len(full))}})
	msg, err := Import(db, "azure-focus", false, ImportOptions{})
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	cfWant(t, cfCharges(t, db, "azure-focus"), []string{
		"azure|2026-10-01|Virtual Machines|payments|Usage|100|G000000002",
	})
	if !strings.Contains(msg, "bytes on disk") {
		t.Errorf("the refusal does not say the part is short:\n%s", msg)
	}
}

// TestAFileTheManifestDoesNotListIsNotRead: a part left in the run folder by
// a sync without deletion, and not in the run's manifest, is named and not
// read, so its days are not counted on top of the run's.
func TestAFileTheManifestDoesNotListIsNotRead(t *testing.T) {
	dir := t.TempDir()
	azRun(t, dir, azPeriod, azRunNew, "2026-10-03T06:00:00Z", cfCSV(azCols10, azRow(nil)))
	cfWrite(t, dir, filepath.Join("costs", "focus-daily", azPeriod, azRunNew, "part_9_0001.csv"),
		cfCSV(azCols10, azRow(map[string]string{"BilledCost": "50.00"})))
	msg, db, err := cfImportFresh(t, "azure-focus", dir, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	cfWant(t, cfCharges(t, db, "azure-focus"), []string{
		"azure|2026-10-01|Virtual Machines|payments|Usage|100|G000000002",
	})
	if !strings.Contains(msg, "part_9_0001.csv") || !strings.Contains(msg, "not listed") {
		t.Errorf("the unlisted part is not named:\n%s", msg)
	}
}

// TestACSVDirectlyInThePeriodFolderIsRefused: a file beside the run folders
// is in no run; read as a file of its own it would count its days on top of
// the run's.
func TestACSVDirectlyInThePeriodFolderIsRefused(t *testing.T) {
	dir := t.TempDir()
	azRun(t, dir, azPeriod, azRunNew, "2026-10-03T06:00:00Z", cfCSV(azCols10, azRow(nil)))
	cfWrite(t, dir, filepath.Join("costs", "focus-daily", azPeriod, "loose.csv"),
		cfCSV(azCols10, azRow(map[string]string{"BilledCost": "50.00"})))
	msg, db, err := cfImportFresh(t, "azure-focus", dir, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	cfWant(t, cfCharges(t, db, "azure-focus"), []string{
		"azure|2026-10-01|Virtual Machines|payments|Usage|100|G000000002",
	})
	if !strings.Contains(msg, "loose.csv") {
		t.Errorf("the loose file is not named:\n%s", msg)
	}
}

// TestARunThatKeptNoRowDoesNotReplaceThePeriod: a newer run whose every row is
// refused (here, another currency) does not replace the month with nothing.
func TestARunThatKeptNoRowDoesNotReplaceThePeriod(t *testing.T) {
	dir := t.TempDir()
	azRun(t, dir, azPeriod, azRunOld, "2026-10-02T06:00:00Z", cfCSV(azCols10, azRow(nil)))
	_, db, err := cfImportFresh(t, "azure-focus", dir, nil)
	if err != nil {
		t.Fatalf("first Import: %v", err)
	}
	azRun(t, dir, azPeriod, azRunNew, "2026-10-03T06:00:00Z",
		cfCSV(azCols10, azRow(map[string]string{"BillingCurrency": "EUR"})))
	msg, err := Import(db, "azure-focus", false, ImportOptions{})
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	cfWant(t, cfCharges(t, db, "azure-focus"), []string{
		"azure|2026-10-01|Virtual Machines|payments|Usage|100|G000000002",
	})
	if !strings.Contains(msg, "kept no row") || !strings.Contains(msg, "EUR") {
		t.Errorf("the refusal does not say why:\n%s", msg)
	}
}

// TestARunWhosePartsHaveNotArrivedKeepsTheEarlierRun: a newer run folder that
// holds only its manifest so far still takes part in choosing the newest, and
// being incomplete it is refused, rather than the older run being read as if
// it were the latest.
func TestARunWhosePartsHaveNotArrivedKeepsTheEarlierRun(t *testing.T) {
	dir := t.TempDir()
	azRun(t, dir, azPeriod, azRunOld, "2026-10-02T06:00:00Z", cfCSV(azCols10, azRow(nil)))
	azManifest(t, dir, azPeriod, azRunNew, "2026-10-03T06:00:00Z", []azBlob{{"part_0_0001.csv", 10}})
	msg, db, err := cfImportFresh(t, "azure-focus", dir, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := cfCharges(t, db, "azure-focus"); len(got) != 0 {
		t.Errorf("the older run was read while a newer one is arriving: %v", got)
	}
	if !strings.Contains(msg, azRunNew) || !strings.Contains(msg, "part_0_0001.csv") {
		t.Errorf("the refusal does not name the incomplete run:\n%s", msg)
	}
}

// TestTheDryRunReadsOnlyTheNewestRun: Test says what Import would do, run
// rule included, and writes nothing.
func TestTheDryRunReadsOnlyTheNewestRun(t *testing.T) {
	dir := t.TempDir()
	azRun(t, dir, azPeriod, azRunOld, "2026-10-02T06:00:00Z", cfCSV(azCols10, azRow(nil)))
	azRun(t, dir, azPeriod, azRunNew, "2026-10-03T06:00:00Z", cfCSV(azCols10, azRow(nil)))
	st := openFocusStore(t)
	cfSave(t, st.DB(), "azure-focus", dir, nil)
	res, ok, err := Test(st.DB(), "azure-focus", func(string) string { return "" })
	if err != nil || !ok {
		t.Fatalf("Test: ok=%v err=%v %s", ok, err, res)
	}
	if !strings.Contains(res, "Would read 1 file") || !strings.Contains(res, "superseded") {
		t.Errorf("the dry run does not read the newest run alone:\n%s", res)
	}
	if n := cfCount(st.DB(), `SELECT COUNT(*) FROM cloud_focus_rows`); n > 0 {
		t.Errorf("the dry run wrote %d rows", n)
	}
}

// TestAManifestTimeWithNoZoneIsReadAsUTC: the manifest writes some of its
// times with no zone; a submittedTime written that way is read as UTC.
func TestAManifestTimeWithNoZoneIsReadAsUTC(t *testing.T) {
	dir := t.TempDir()
	azRun(t, dir, azPeriod, azRunOld, "2026-10-02T06:00:00", cfCSV(azCols10, azRow(nil)))
	azRun(t, dir, azPeriod, azRunNew, "2026-10-03T06:00:00.5234447",
		cfCSV(azCols10, azRow(map[string]string{"BilledCost": "2.00"})))
	_, db, err := cfImportFresh(t, "azure-focus", dir, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	cfWant(t, cfCharges(t, db, "azure-focus"), []string{
		"azure|2026-10-01|Virtual Machines|payments|Usage|200|G000000002",
	})
}

// TestRunPrefixesWithUnderscoresAndNonASCIIAreKeptApart: the store's rows of
// another run are found by path prefix; a run id that is a prefix of another,
// and folder names with '_' or non-ASCII letters, must neither match too much
// nor too little.
func TestRunPrefixesWithUnderscoresAndNonASCIIAreKeptApart(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "експорт_витрат")
	azRunIn := func(run, submitted, cost string) {
		body := cfCSV(azCols10, azRow(map[string]string{"BilledCost": cost}))
		cfWrite(t, root, filepath.Join(azPeriod, run, "part_0_0001.csv"), body)
		m := fmt.Sprintf(`{"runInfo": {"submittedTime": %q}, "blobs": [{"blobName": "x/%s/part_0_0001.csv", "byteCount": %d}]}`,
			submitted, run, len(body))
		cfWrite(t, root, filepath.Join(azPeriod, run, "manifest.json"), m)
	}
	// The older run's id has the newer run's id as its prefix, so a prefix
	// compared without the closing slash would keep the older run's rows.
	azRunIn("run_10", "2026-10-02T06:00:00Z", "1.00")
	_, db, err := cfImportFresh(t, "azure-focus", dir, nil)
	if err != nil {
		t.Fatalf("first Import: %v", err)
	}
	azRunIn("run_1", "2026-10-03T06:00:00Z", "2.00")
	if _, err := Import(db, "azure-focus", false, ImportOptions{}); err != nil {
		t.Fatalf("second Import: %v", err)
	}
	cfWant(t, cfCharges(t, db, "azure-focus"), []string{
		"azure|2026-10-01|Virtual Machines|payments|Usage|200|G000000002",
	})
	if n := cfCount(db, `SELECT COUNT(*) FROM cloud_focus_rows WHERE connector='azure-focus'`); n != 1 {
		t.Errorf("%d rows in the store, want the newest run's one", n)
	}
}

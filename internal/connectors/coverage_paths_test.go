package connectors

// Paths of this package's readers that no earlier test walked: what the
// connector page's Test says for each state a connector can be in, what a
// row the FOCUS reader refuses is refused for, and what each reader says when
// its store or its folder cannot be read. Each test asserts the sentence or
// the error a person would read, so a path that stops saying it goes red.

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheConnectorTestSaysWhatStateTheConnectorIsIn(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	noEnv := func(string) string { return "" }

	if _, _, err := Test(db, "no-such-connector", noEnv); err == nil || err.Error() != "no such connector" {
		t.Errorf("an unknown connector: %v", err)
	}
	// A built reader with its folder not saved yet.
	msg, ok, err := Test(db, "tokenfuse-focus", noEnv)
	if err != nil || ok || msg != "Not configured yet. Still needed: Folder tokenfuse focus-export wrote to." {
		t.Errorf("an unconfigured reader: %q %v %v", msg, ok, err)
	}
	// A documented connector whose key lives in the environment names the
	// variable when it is unset, and still says it is not built.
	msg, ok, err = Test(db, "openrouter-usage", noEnv)
	if err != nil || ok || !strings.Contains(msg, "documented, not built") {
		t.Errorf("a documented connector: %q %v %v", msg, ok, err)
	}
	// A built reader whose folder is not there: the reader's own refusal,
	// and the connector marked not working.
	configureFocus(t, db, filepath.Join(t.TempDir(), "absent"))
	msg, ok, err = Test(db, "tokenfuse-focus", noEnv)
	if err != nil || ok || !strings.Contains(msg, "absent") {
		t.Errorf("a reader that cannot read its folder: %q %v %v", msg, ok, err)
	}
	conn, err := Load(db, "tokenfuse-focus")
	if err != nil || conn.OK || conn.LastResult != msg {
		t.Errorf("the failed test is not recorded on the connector: %+v %v", conn, err)
	}
}

func TestASecretIsNeverSavedAndAnUnknownConnectorIsRefused(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	if err := Save(db, "openrouter-usage", map[string]string{"key": "sk-never-stored", "note": "kept"}); err != nil {
		t.Fatal(err)
	}
	conn, err := Load(db, "openrouter-usage")
	if err != nil {
		t.Fatal(err)
	}
	if _, has := conn.Config["key"]; has || conn.Config["note"] != "kept" {
		t.Errorf("a secret input reached the store, or a plain one did not: %v", conn.Config)
	}
	if err := Save(db, "no-such-connector", nil); err == nil || err.Error() != "no such connector" {
		t.Errorf("saving an unknown connector: %v", err)
	}
}

func TestAStoreThatCannotBeReadIsReportedByEveryEntryPoint(t *testing.T) {
	st := openFocusStore(t)
	db := st.DB()
	dir := copyIntoDir(t, "testdata/tokenfuse-focus-runs-2026-10-14.csv")
	configureFocus(t, db, dir)
	db.Close()
	if _, err := Load(db, "tokenfuse-focus"); err == nil {
		t.Error("Load read a closed store")
	}
	if err := Save(db, "tokenfuse-focus", map[string]string{"path": dir}); err == nil {
		t.Error("Save wrote to a closed store")
	}
	if _, _, err := Test(db, "tokenfuse-focus", os.Getenv); err == nil {
		t.Error("Test read a closed store")
	}
	if _, err := Import(db, "tokenfuse-focus", false, ImportOptions{}); err == nil {
		t.Error("Import read a closed store")
	}
	if _, err := Reconcile(db, "anthropic-usage", "", ""); err == nil {
		t.Error("Reconcile read a closed store")
	}
	if _, err := LoadTokenFuseFocus(db, dir); err == nil {
		t.Error("LoadTokenFuseFocus wrote to a closed store")
	}
	if err := EnsureFocusSchema(db); err == nil || !strings.Contains(err.Error(), "ensuring the estate schema exists") {
		t.Errorf("EnsureFocusSchema on a closed store: %v", err)
	}
	_, err := ImportFocus(db, FocusInput{Main: dir}, nil)
	if err == nil || !strings.Contains(err.Error(), "and recording that failed") {
		t.Errorf("an automatic import whose refusal cannot be recorded does not say so: %v", err)
	}
}

func TestTheFocusFolderEnvironmentDefaultsAreTrimmed(t *testing.T) {
	t.Setenv(FocusDirEnv, "  /srv/focus/main  ")
	t.Setenv(FocusDirOpenAIEnv, "\t/srv/focus/openai\n")
	if got := FocusDirEnvDefault(); got != "/srv/focus/main" {
		t.Errorf("FocusDirEnvDefault: %q", got)
	}
	if got := FocusDirOpenAIEnvDefault(); got != "/srv/focus/openai" {
		t.Errorf("FocusDirOpenAIEnvDefault: %q", got)
	}
}

func TestAFolderOfOnlyLinksNamesTheFirstTwentyAndCountsTheRest(t *testing.T) {
	target := filepath.Join(t.TempDir(), "real.csv")
	if err := os.WriteFile(target, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for i := 0; i < 23; i++ {
		if err := os.Symlink(target, filepath.Join(dir, "link-"+string(rune('a'+i))+".csv")); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := csvFolder(dir)
	if err == nil || !strings.Contains(err.Error(), "link-t.csv, and 3 more") || strings.Contains(err.Error(), "link-u.csv") {
		t.Errorf("a folder of 23 links: %v", err)
	}
}

func TestTheFocusReaderPassesOverWhatIsNotAnExportAndNamesABadGzip(t *testing.T) {
	dir := copyIntoDir(t, "testdata/tokenfuse-focus-runs-2026-10-14.csv")
	if err := os.Mkdir(filepath.Join(dir, "archive.csv"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFocusFile(t, dir, "README.txt", "not an export\n")
	writeFocusFile(t, dir, "broken.csv.gz", "this is not gzip")
	msg, db, err := importFrom(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(msg, "Read 1 file") || countWhere(t, db, `1=1`) != 15 {
		t.Errorf("the folder's one export was not read alone: %s", msg)
	}
	if !strings.Contains(msg, "broken.csv.gz: not a valid gzip file") {
		t.Errorf("a .gz that is not gzip is not named: %s", msg)
	}
	if strings.Contains(msg, "README") || strings.Contains(msg, "archive") {
		t.Errorf("a folder or a file that is not an export is named as one: %s", msg)
	}

	// A good .gz is read like its CSV.
	data, err := os.ReadFile("testdata/tokenfuse-focus-runs-2026-10-14.csv")
	if err != nil {
		t.Fatal(err)
	}
	gzdir := t.TempDir()
	f, err := os.Create(filepath.Join(gzdir, "focus.csv.gz"))
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	f.Close()
	if msg, _, err := importFrom(t, gzdir); err != nil || !strings.HasPrefix(msg, "Read 1 file") {
		t.Errorf("a gzipped export: %q %v", msg, err)
	}
}

func TestAFileTheFolderListedButIsNoLongerAFileIsRefused(t *testing.T) {
	dir := t.TempDir()
	if _, err := processFocusFile(filepath.Join(dir, "gone.csv"), DoorMain, nil, nil); err == nil {
		t.Error("a file that vanished after the listing was read")
	}
	target := filepath.Join(dir, "real.csv")
	writeFocusFile(t, dir, "real.csv", focusHeader+"\n")
	link := filepath.Join(dir, "swapped.csv")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := processFocusFile(link, DoorMain, nil, nil); err == nil || !strings.Contains(err.Error(), "not followed") {
		t.Errorf("a file swapped for a link after the listing: %v", err)
	}
}

func TestBadTokenToolAndCommitmentFieldsAreRefusedByName(t *testing.T) {
	row := func(edit func(f []string)) string {
		f := focusRowFields()
		edit(f)
		return strings.Join(f, ",")
	}
	usage := []string{
		row(func(f []string) { f[19] = "many" }),
		row(func(f []string) { f[20] = "-4" }),
		row(func(f []string) { f[25] = "two" }),
	}
	const commitmentHeader = focusHeader + ",ChargeCategory,CommitmentDiscountId,CommitmentDiscountType," +
		"CommitmentDiscountStatus,CommitmentDiscountQuantity,CommitmentDiscountUnit"
	purchase := func(edit func(f []string)) string {
		f := append(focusRowFields(), "Purchase", "cd-1", "Reserved", "Used", "1", "Hours")
		edit(f)
		return strings.Join(f, ",")
	}
	commitments := []string{
		purchase(func(f []string) { f[2] = "EUR" }),
		purchase(func(f []string) { f[0] = "ten" }),
		purchase(func(f []string) { f[0] = "-1" }),
		purchase(func(f []string) { f[3] = "yesterday" }),
		purchase(func(f []string) { f[4] = "tomorrow" }),
		purchase(func(f []string) { f[30] = "lots" }),
		purchase(func(f []string) { f[4] = "" }), // no end: its own window, kept
	}
	dir := t.TempDir()
	writeFocusFile(t, dir, "usage.csv", focusHeader+"\n"+strings.Join(usage, "\n")+"\n")
	writeFocusFile(t, dir, "commitments.csv", commitmentHeader+"\n"+
		strings.Join(append(make([]string, 0), commitments...), "\n")+"\n")
	// The usage file's rows carry 26 fields; the commitments file's carry 32.
	msg, db, err := importFrom(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`x_tokens_in "many": not an integer`, `x_tokens_out "-4": negative`, `x_tool_calls "two" is not an integer`,
		`currency "EUR", this reader is USD only`, `BilledCost "ten" does not parse as a decimal amount`,
		`BilledCost "-1" is negative`, `ChargePeriodStart "yesterday" does not parse as RFC 3339`,
		`ChargePeriodEnd "tomorrow" does not parse as RFC 3339`, `CommitmentDiscountQuantity "lots" is not a number`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not name %q:\n%s", want, msg)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM commitments WHERE id='cd-1'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("a Purchase row with no end date was not kept as its own window (%d, %v)", n, err)
	}
}

func TestAGatewayRefusalWithNobodyToFileItUnderReadsAsTheGateways(t *testing.T) {
	if got := (errUnfiledRefusal{reason: "identity_mismatch"}).Error(); got !=
		"a gateway refusal (identity_mismatch) filed under no agent and no key" {
		t.Errorf("errUnfiledRefusal: %q", got)
	}
	// LoadTokenFuseFocus counts them for the Cloudability drop.
	header := focusHeader + ",x_key_id,x_block_reason"
	f := focusRowFields()
	f[0], f[1] = "0", "0"
	f[11], f[12], f[17] = "", "", ""
	f[21], f[22] = "true", "blocked"
	dir := t.TempDir()
	writeFocusFile(t, dir, "focus.csv", header+"\n"+strings.Join(append(f, "", "identity_mismatch"), ",")+"\n"+
		strings.Join(append(focusRowFields(), "k", ""), ",")+"\n")
	st := openFocusStore(t)
	load, err := LoadTokenFuseFocus(st.DB(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if load.Unfiled != 1 || load.Calls != 1 {
		t.Errorf("LoadTokenFuseFocus: %d unfiled, %d calls; want 1 and 1", load.Unfiled, load.Calls)
	}
	if _, err := LoadTokenFuseFocus(st.DB(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "no *.csv") {
		t.Errorf("an empty folder: %v", err)
	}
}

func TestFindingsOverTheirByteBoundAreRefusedByTheirSize(t *testing.T) {
	long := "pii_card=1," + strings.Repeat("pii_x=1,", 31) + "pii_last_kind_name=1"
	if len(long) <= xPIIFindingsMaxBytes {
		t.Fatalf("the probe is %d bytes, not over the bound", len(long))
	}
	err := checkPIIFindings(long)
	if err == nil || !strings.Contains(err.Error(), "over the 256 byte limit") || strings.Contains(err.Error(), "pii_x") {
		t.Errorf("findings over the bound: %v", err)
	}
}

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// focus11MandatoryFromTheSpec is every column FOCUS 1.1 marks Mandatory, read from the
// specification's own column pages at tag v1.1 of
// FinOps-Open-Cost-and-Usage-Spec/FOCUS_Spec (specification/columns/*.md,
// "Feature level | Mandatory") on 2026-10-13. It is written out here, and not
// taken from the tool's own table, so the table cannot lose a column and
// agree with itself.
var focus11MandatoryFromTheSpec = []string{
	"BilledCost", "BillingAccountId", "BillingAccountName", "BillingCurrency",
	"BillingPeriodEnd", "BillingPeriodStart", "ChargeCategory", "ChargeClass",
	"ChargeDescription", "ChargePeriodEnd", "ChargePeriodStart", "ContractedCost",
	"EffectiveCost", "InvoiceIssuerName", "ListCost", "PricingQuantity", "PricingUnit",
	"ProviderName", "PublisherName", "ServiceCategory", "ServiceName",
}

const (
	reconciler = "agent://acme.example/payments/reconciler"
	scorer     = "agent://acme.example/risk/scorer"
)

func TestADropCarriesEachAgentsSpendByUnitTaggedForGrouping(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeExport(t, in, "tokenfuse-2026-10-02.csv", true,
		call{at: "2026-10-01T09:00:00.100Z", agent: reconciler, model: "claude-haiku-4-5", unit: "payments",
			key: "recon-key", cost: "0.050000", in: "100", out: "50"},
		call{at: "2026-10-01T09:05:00.200Z", agent: reconciler, model: "claude-haiku-4-5", unit: "payments",
			key: "recon-key", cost: "0.090000", in: "200", out: "80"},
		call{at: "2026-10-01T10:00:00.000Z", agent: scorer, model: "gpt-4o", unit: "risk",
			key: "risk-key", cost: "0.200000", in: "1000", out: "300", provider: "OpenAI"},
		call{at: "2026-10-02T08:00:00.000Z", agent: reconciler, model: "claude-sonnet-4-5", unit: "payments",
			key: "recon-key", cost: "0.300000", in: "500", out: "400"},
	)
	r := dropAt(t, in, out, at)
	d := readDrop(t, out, at)

	if len(d.rows) != 3 {
		t.Fatalf("%d rows, want 3: one per day, agent, unit, model and credential; rows: %v", len(d.rows), d.rows)
	}
	haiku := rowFor(t, d, "2026-10-01", reconciler, "claude-haiku-4-5")
	want := map[string]string{
		"BilledCost": "0.140000", "EffectiveCost": "0.140000", "ListCost": "0.140000", "ContractedCost": "0.140000",
		"BillingCurrency": "USD", "BillingAccountId": "acme-tokenfuse-gateway",
		"ChargePeriodStart": "2026-10-01T00:00:00Z", "ChargePeriodEnd": "2026-10-02T00:00:00Z",
		"BillingPeriodStart": "2026-10-01T00:00:00Z", "BillingPeriodEnd": "2026-11-01T00:00:00Z",
		"ResourceId": reconciler, "SubAccountId": "payments", "SubAccountName": "payments",
		"ProviderName": "Anthropic", "PublisherName": "Anthropic", "InvoiceIssuerName": "Anthropic",
		"ServiceName": "Anthropic API", "PricingQuantity": "430", "PricingUnit": "Tokens",
		"ConsumedQuantity": "2", "ConsumedUnit": "Requests",
		"x_AgentId": reconciler, "x_Unit": "payments", "x_Model": "claude-haiku-4-5", "x_KeyId": "recon-key",
		"x_Calls": "2", "x_BlockedCalls": "0", "x_EstimatedCalls": "0", "x_TokensIn": "300", "x_TokensOut": "130",
		"Tags": `{"tokenfuse/agent":"agent://acme.example/payments/reconciler","tokenfuse/key_id":"recon-key",` +
			`"tokenfuse/model":"claude-haiku-4-5","tokenfuse/unit":"payments"}`,
	}
	for col, v := range want {
		if haiku[col] != v {
			t.Errorf("%s = %q, want %q", col, haiku[col], v)
		}
	}
	gpt := rowFor(t, d, "2026-10-01", scorer, "gpt-4o")
	if gpt["SubAccountId"] != "risk" || gpt["ProviderName"] != "OpenAI" || gpt["ServiceName"] != "OpenAI API" ||
		gpt["BilledCost"] != "0.200000" || !strings.Contains(gpt["Tags"], `"tokenfuse/unit":"risk"`) {
		t.Errorf("the risk unit's row is not the scorer's own spend: %v", gpt)
	}
	sonnet := rowFor(t, d, "2026-10-02", reconciler, "claude-sonnet-4-5")
	if sonnet["BilledCost"] != "0.300000" || sonnet["ChargePeriodStart"] != "2026-10-02T00:00:00Z" {
		t.Errorf("the second day's row: %v", sonnet)
	}
	if !strings.Contains(r.stdout, "0.640000 USD") {
		t.Errorf("the tool does not state the drop's total, 0.640000 USD, the gateway's own sum:\n%s", r.stdout)
	}
}

func TestADropHoldsTheWholeMonthToDate(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	var first, second []call
	for day := 1; day <= 13; day++ {
		c := call{at: fmt.Sprintf("2026-10-%02dT12:00:00.000Z", day), agent: reconciler, unit: "payments",
			key: "recon-key", cost: "0.010000"}
		if day <= 5 {
			first = append(first, c)
		} else {
			second = append(second, c)
		}
	}
	writeExport(t, in, "a-first-week.csv", true, first...)
	writeExport(t, in, "b-since.csv", true, second...)
	writeExport(t, in, "c-either-side.csv", true,
		call{at: "2026-09-30T23:59:59.999Z", agent: reconciler, unit: "payments", key: "recon-key", cost: "1.000000"},
		call{at: "2026-11-01T00:00:00.000Z", agent: reconciler, unit: "payments", key: "recon-key", cost: "2.000000"},
		// 23:30 on 31 October in UTC-5 is 04:30 on 1 November in UTC: FOCUS
		// dates are UTC, so it is November's.
		call{at: "2026-10-31T23:30:00-05:00", agent: reconciler, unit: "payments", key: "recon-key", cost: "4.000000"},
	)
	r := dropAt(t, in, out, at)
	d := readDrop(t, out, at)

	var days []string
	for _, row := range d.rows {
		days = append(days, row["ChargePeriodStart"][:10])
	}
	if len(days) != 13 || days[0] != "2026-10-01" || days[12] != "2026-10-13" {
		t.Fatalf("the drop holds the days %v, want every day from 2026-10-01 to 2026-10-13", days)
	}
	for _, row := range d.rows {
		if row["BilledCost"] != "0.010000" {
			t.Errorf("%s holds %s, want 0.010000: a call from another month leaked into October",
				row["ChargePeriodStart"], row["BilledCost"])
		}
	}
	if !strings.Contains(r.stdout, "3 calls outside 2026-10") {
		t.Errorf("the tool does not say it left 3 calls of other months out:\n%s", r.stdout)
	}
}

func TestASecondDropSupersedesAndNeverAppends(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeExport(t, in, "a.csv", true,
		call{at: "2026-10-01T09:00:00.000Z", agent: reconciler, unit: "payments", key: "recon-key", cost: "0.100000"})
	dropAt(t, in, out, at)
	before := filesUnder(t, out)
	firstDrop := readDrop(t, out, at)

	// The gateway exports the days since into a new file; the next drop must
	// carry the whole month again, not only what is new.
	writeExport(t, in, "b.csv", true,
		call{at: "2026-10-13T09:00:00.000Z", agent: reconciler, unit: "payments", key: "recon-key", cost: "0.200000"})
	later := at.Add(time.Hour)
	dropAt(t, in, out, later)
	second := readDrop(t, out, later)
	if len(second.rows) != 2 || second.rows[0]["BilledCost"] != "0.100000" || second.rows[1]["BilledCost"] != "0.200000" {
		t.Fatalf("the later drop is not the whole month: %v", second.rows)
	}
	if second.epochDir == firstDrop.epochDir {
		t.Fatal("the later drop was written into the earlier drop's folder")
	}
	again := readDrop(t, out, at)
	if !bytes.Equal(again.data, firstDrop.data) || !bytes.Equal(again.manifestRaw, firstDrop.manifestRaw) {
		t.Error("the earlier drop's files changed when the later one was written")
	}
	after := filesUnder(t, out)
	if len(after) != len(before)+2 {
		t.Errorf("files after the second drop: %v, want the first drop's %v and two more", after, before)
	}

	// The same second again: the folder exists, and nothing is written into it.
	r := runTool(t, later, baseFlags(in, out)...)
	if r.code == 0 || !strings.Contains(r.stderr, "already") {
		t.Errorf("a drop into an epoch folder that already exists was not refused (exit %d): %s", r.code, r.stderr)
	}
	// A clock behind the latest drop: Cloudability reads the latest epoch, so
	// a drop written behind it would never be the one it reads.
	r = runTool(t, at.Add(30*time.Minute), baseFlags(in, out)...)
	if r.code == 0 || !strings.Contains(r.stderr, "later") {
		t.Errorf("a drop behind a later one was not refused (exit %d): %s", r.code, r.stderr)
	}
	if got := filesUnder(t, out); strings.Join(got, ",") != strings.Join(after, ",") {
		t.Errorf("a refused drop changed the folder: %v, want %v", got, after)
	}
}

func TestADropIsTheSameBytesEveryTime(t *testing.T) {
	in := t.TempDir()
	var calls []call
	for i := 0; i < 40; i++ {
		agent := []string{reconciler, scorer, "agent://acme.example/ops/triage"}[i%3]
		calls = append(calls, call{at: fmt.Sprintf("2026-10-%02dT%02d:00:00.000Z", 1+i%9, i%24), agent: agent,
			unit: []string{"payments", "risk"}[i%2], model: []string{"claude-haiku-4-5", "claude-sonnet-4-5"}[i%2],
			key: "k" + fmt.Sprint(i%4), cost: fmt.Sprintf("0.%06d", 1000+i*37)})
	}
	writeExport(t, in, "z.csv", true, calls[:20]...)
	writeExport(t, in, "a.csv", true, calls[20:]...)

	out1, out2 := t.TempDir(), t.TempDir()
	dropAt(t, in, out1, at)
	later := at.Add(36 * time.Hour)
	dropAt(t, in, out2, later)
	d1, d2 := readDrop(t, out1, at), readDrop(t, out2, later)
	if !bytes.Equal(d1.data, d2.data) {
		t.Fatal("two drops of the same exports differ in their data file")
	}
	// The manifest may differ in exactly three places: the epoch inside the
	// key, the time it was written, and the report id derived from both.
	m1, m2 := d1.manifest, d2.manifest
	m2.AllReportKeys = []string{strings.Replace(m2.AllReportKeys[0], fmt.Sprint(later.Unix()), fmt.Sprint(at.Unix()), 1)}
	m2.UpdatedAt, m2.ReportID = m1.UpdatedAt, m1.ReportID
	if fmt.Sprint(m1) != fmt.Sprint(m2) {
		t.Errorf("the manifests differ beyond the epoch, updated_at and report_id:\n%v\n%v", m1, m2)
	}
	if d1.manifest.ReportID == d2.manifest.ReportID {
		t.Error("two drops share a report_id")
	}
}

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestTheManifestIsWhatCloudabilityReads(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeExport(t, in, "a.csv", true,
		call{at: "2026-10-01T09:00:00.000Z", agent: reconciler, unit: "payments", key: "recon-key"})
	dropAt(t, in, out, at, "-sub-dir", "finops/ai", "-prefix", "acme", "-vendor", "TokenFuse")
	d := readDropAt(t, out, "finops/ai", "TokenFuse", "20261001-20261101", "acme", at)
	m := d.manifest

	epoch := fmt.Sprint(at.Unix())
	wantKey := "finops/ai/TokenFuse/20261001-20261101/" + epoch + "/acme-00001.csv.gz"
	if len(m.AllReportKeys) != 1 || m.AllReportKeys[0] != wantKey {
		t.Errorf("all_report_keys = %q, want [%q]", m.AllReportKeys, wantKey)
	}
	for _, k := range m.AllReportKeys {
		if strings.HasPrefix(k, "/") || strings.HasPrefix(k, m.RootDir+"/") {
			t.Errorf("report key %q carries the root directory or a leading slash; Cloudability's keys exclude root_dir", k)
		}
		for _, seg := range strings.Split(k, "/") {
			if seg == m.RootDir {
				t.Errorf("report key %q names the bucket %q as a folder", k, m.RootDir)
			}
		}
	}
	checks := []struct{ field, got, want string }{
		{"compression", m.Compression, "GZIP"},
		{"content_type", m.ContentType, "CSV"},
		{"root_dir", m.RootDir, "byod-input-prod"},
		{"updated_at", m.UpdatedAt, "2026-10-13T12:00:00Z"},
		{"focus_version", m.FocusVersion, "1.1"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("manifest %s = %q, want %q", c.field, c.got, c.want)
		}
	}
	if !uuidShape.MatchString(m.ReportID) {
		t.Errorf("report_id %q is not a UUID", m.ReportID)
	}
	// IBM's own example lists the fields in this order; a reader that cares
	// about order finds them where the example puts them.
	last := -1
	for _, f := range []string{"compression", "content_type", "report_id", "root_dir", "all_report_keys", "updated_at", "focus_version"} {
		i := bytes.Index(d.manifestRaw, []byte(`"`+f+`"`))
		if i < last {
			t.Errorf("manifest field %s is out of the documented order:\n%s", f, d.manifestRaw)
		}
		last = i
	}
	// The epoch folder holds the manifest and what it lists, and nothing else.
	got := filesUnder(t, d.epochDir)
	if strings.Join(got, ",") != "acme-00001.csv.gz,acme-Manifest.json" {
		t.Errorf("the epoch folder holds %v, want the data file and the manifest", got)
	}
	if !bytes.HasPrefix(d.data, []byte{0x1f, 0x8b}) {
		t.Error("the data file is not gzip")
	}
}

func TestTheReportPeriodRunsMonthToMonthAcrossYearsAndLeapFebruary(t *testing.T) {
	for _, c := range []struct {
		y    int
		m    time.Month
		want string
	}{
		{2023, time.February, "20230201-20230301"}, // IBM's own example
		{2026, time.October, "20261001-20261101"},
		{2026, time.December, "20261201-20270101"},
		{2027, time.January, "20270101-20270201"},
		{2028, time.February, "20280201-20280301"}, // a leap year: the 29th is February's
		{2027, time.February, "20270201-20270301"},
	} {
		if got := reportPeriod(time.Date(c.y, c.m, 1, 0, 0, 0, 0, time.UTC)); got != c.want {
			t.Errorf("reportPeriod(%d-%02d) = %q, want %q", c.y, c.m, got, c.want)
		}
	}

	in, out := t.TempDir(), t.TempDir()
	writeExport(t, in, "a.csv", true,
		call{at: "2026-12-31T23:59:59.999Z", agent: reconciler, unit: "payments", key: "k", cost: "0.100000"},
		call{at: "2027-01-01T00:00:00.000Z", agent: reconciler, unit: "payments", key: "k", cost: "0.200000"},
		call{at: "2028-02-29T12:00:00.000Z", agent: reconciler, unit: "payments", key: "k", cost: "0.300000"},
		call{at: "2028-03-01T00:00:00.000Z", agent: reconciler, unit: "payments", key: "k", cost: "0.400000"},
	)
	whenDec := time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC)
	r := runTool(t, whenDec, "-in", in, "-out", out, "-root-dir", "b", "-billing-account-id", "a", "-month", "2026-12")
	if r.code != 0 {
		t.Fatalf("December's drop: exit %d: %s", r.code, r.stderr)
	}
	dec := readDropAt(t, out, "", "TokenFuse", "20261201-20270101", "costcrew", whenDec)
	if len(dec.rows) != 1 || dec.rows[0]["BilledCost"] != "0.100000" ||
		dec.rows[0]["BillingPeriodEnd"] != "2027-01-01T00:00:00Z" || dec.rows[0]["ChargePeriodEnd"] != "2027-01-01T00:00:00Z" {
		t.Errorf("December's drop: %v", dec.rows)
	}
	whenFeb := time.Date(2028, 3, 2, 0, 0, 0, 0, time.UTC)
	r = runTool(t, whenFeb, "-in", in, "-out", out, "-root-dir", "b", "-billing-account-id", "a", "-month", "2028-02")
	if r.code != 0 {
		t.Fatalf("February's drop: exit %d: %s", r.code, r.stderr)
	}
	feb := readDropAt(t, out, "", "TokenFuse", "20280201-20280301", "costcrew", whenFeb)
	if len(feb.rows) != 1 || feb.rows[0]["ChargePeriodStart"] != "2028-02-29T00:00:00Z" ||
		feb.rows[0]["ChargePeriodEnd"] != "2028-03-01T00:00:00Z" || feb.rows[0]["BillingPeriodEnd"] != "2028-03-01T00:00:00Z" {
		t.Errorf("a leap February's drop: %v", feb.rows)
	}
}

var (
	pascalID     = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	focusNumber  = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?(E-?[0-9]+)?$`)
	focusInstant = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`)
)

func TestEveryMandatoryFocus11ColumnIsFilledHonestly(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeExport(t, in, "a.csv", true,
		call{at: "2026-10-01T09:00:00.000Z", agent: reconciler, unit: "payments", key: "recon-key", cost: "0.120000"},
		// No unit and no credential: the columns that may be null are, and
		// nothing is written in their place.
		call{at: "2026-10-02T09:00:00.000Z", agent: scorer, model: "gpt-4o", provider: "OpenAI", cost: "0.050000"},
		// Priced at the gateway's fallback, no tokens parsed.
		call{at: "2026-10-03T09:00:00.000Z", agent: scorer, model: "gpt-4o", provider: "OpenAI", cost: "0.070000",
			in: "0", out: "0", basis: "estimated"},
	)
	dropAt(t, in, out, at)
	d := readDrop(t, out, at)

	col := map[string]int{}
	for i, h := range d.header {
		col[h] = i
	}
	for _, id := range focus11MandatoryFromTheSpec {
		if _, ok := col[id]; !ok {
			t.Errorf("the drop has no %s column; FOCUS 1.1 makes it mandatory", id)
		}
	}
	seenCustom := false
	for _, h := range d.header {
		if strings.HasPrefix(h, "x_") {
			seenCustom = true
			if !pascalID.MatchString(strings.TrimPrefix(h, "x_")) {
				t.Errorf("custom column %s does not follow FOCUS's naming after its x_ prefix", h)
			}
			continue
		}
		if seenCustom {
			t.Errorf("FOCUS column %s comes after a custom column", h)
		}
		if !pascalID.MatchString(h) {
			t.Errorf("column %s is not a FOCUS column ID and carries no x_ prefix", h)
		}
	}
	notNull := []string{"BilledCost", "BillingAccountId", "BillingCurrency", "BillingPeriodEnd", "BillingPeriodStart",
		"ChargeCategory", "ChargeFrequency", "ChargePeriodEnd", "ChargePeriodStart", "ConsumedQuantity", "ConsumedUnit",
		"ContractedCost", "EffectiveCost", "InvoiceIssuerName", "ListCost", "PricingQuantity", "PricingUnit",
		"ProviderName", "PublisherName", "ServiceCategory", "ServiceName", "ServiceSubcategory"}
	for _, row := range d.rows {
		for _, c := range notNull {
			if strings.TrimSpace(row[c]) == "" {
				t.Errorf("%s is null on %v; FOCUS 1.1 forbids a null there on a Usage row", c, row)
			}
		}
		for _, c := range []string{"BilledCost", "EffectiveCost", "ListCost", "ContractedCost", "PricingQuantity", "ConsumedQuantity"} {
			if !focusNumber.MatchString(row[c]) {
				t.Errorf("%s = %q is not in FOCUS's numeric format", c, row[c])
			}
		}
		for _, c := range []string{"BillingPeriodStart", "BillingPeriodEnd", "ChargePeriodStart", "ChargePeriodEnd"} {
			if !focusInstant.MatchString(row[c]) {
				t.Errorf("%s = %q is not FOCUS's UTC date/time", c, row[c])
			}
		}
		if row["ChargeCategory"] != "Usage" || row["ChargeClass"] != "" || row["ChargeFrequency"] != "Usage-Based" ||
			row["ServiceCategory"] != "AI and Machine Learning" || row["ServiceSubcategory"] != "Generative AI" ||
			row["BillingCurrency"] != "USD" {
			t.Errorf("a restricted column holds a value FOCUS 1.1 does not allow here: %v", row)
		}
		// The gateway knows one price: what it settled. There is no
		// discount it could know of, so list, contracted, effective and
		// billed are the same figure, and no unit price is claimed.
		if row["ListCost"] != row["BilledCost"] || row["ContractedCost"] != row["BilledCost"] || row["EffectiveCost"] != row["BilledCost"] {
			t.Errorf("the four costs disagree on one row: %v", row)
		}
		if _, ok := col["ListUnitPrice"]; ok {
			t.Error("the drop claims a ListUnitPrice the gateway never published")
		}
		if row["ConsumedQuantity"] == "0" {
			t.Errorf("ConsumedQuantity is 0 on a Usage row; FOCUS 1.1 requires a positive value: %v", row)
		}
	}
	bare := rowFor(t, d, "2026-10-02", scorer, "gpt-4o")
	if bare["SubAccountId"] != "" || bare["SubAccountName"] != "" || bare["x_KeyId"] != "" ||
		strings.Contains(bare["Tags"], "unit") || strings.Contains(bare["Tags"], "key_id") {
		t.Errorf("a call with no unit and no credential was given one: %v", bare)
	}
	if bare["BillingAccountName"] != "" {
		t.Errorf("BillingAccountName = %q although no name was given; nothing is invented", bare["BillingAccountName"])
	}
	est := rowFor(t, d, "2026-10-03", scorer, "gpt-4o")
	if est["x_EstimatedCalls"] != "1" || est["PricingQuantity"] != "0" || est["BilledCost"] != "0.070000" {
		t.Errorf("a call priced at the gateway's fallback is not marked as such: %v", est)
	}
}

func TestTheDropValidatorRefusesEachViolation(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeExport(t, in, "a.csv", true,
		call{at: "2026-10-01T09:00:00.000Z", agent: reconciler, unit: "payments", key: "recon-key"})
	dropAt(t, in, out, at)
	d := readDrop(t, out, at)
	if err := validateDrop(d.header, d.records); err != nil {
		t.Fatalf("the tool's own drop does not pass its validator: %v", err)
	}
	idx := map[string]int{}
	for i, h := range d.header {
		idx[h] = i
	}
	set := func(col, v string) func([]string, [][]string) ([]string, [][]string) {
		return func(h []string, rows [][]string) ([]string, [][]string) {
			rows[0][idx[col]] = v
			return h, rows
		}
	}
	drop := func(col string) func([]string, [][]string) ([]string, [][]string) {
		return func(h []string, rows [][]string) ([]string, [][]string) {
			i := idx[col]
			h = append(append([]string{}, h[:i]...), h[i+1:]...)
			for r := range rows {
				rows[r] = append(append([]string{}, rows[r][:i]...), rows[r][i+1:]...)
			}
			return h, rows
		}
	}
	for _, c := range []struct {
		name, column string
		mutate       func([]string, [][]string) ([]string, [][]string)
	}{
		{"a mandatory column missing", "ListCost", drop("ListCost")},
		{"a null where none is allowed", "BilledCost", set("BilledCost", "")},
		{"a cost with a plus sign", "BilledCost", set("BilledCost", "+1")},
		{"a cost with a thousands comma", "EffectiveCost", set("EffectiveCost", "1,000.00")},
		{"a cost with a currency symbol", "ListCost", set("ListCost", "$1")},
		{"a category outside the list", "ChargeCategory", set("ChargeCategory", "usage")},
		{"a charge class outside the list", "ChargeClass", set("ChargeClass", "Refund")},
		{"a frequency outside the list", "ChargeFrequency", set("ChargeFrequency", "Monthly")},
		{"a service category outside the list", "ServiceCategory", set("ServiceCategory", "AI")},
		{"a subcategory of another category", "ServiceSubcategory", set("ServiceSubcategory", "Virtual Machines")},
		{"a date without its Z", "ChargePeriodStart", set("ChargePeriodStart", "2026-10-01T00:00:00")},
		{"a date with a space", "BillingPeriodEnd", set("BillingPeriodEnd", "2026-11-01 00:00:00Z")},
		{"a currency that is not a code", "BillingCurrency", set("BillingCurrency", "usd")},
		{"tags that are an array", "Tags", set("Tags", `["a"]`)},
		{"tags with an object value", "Tags", set("Tags", `{"a":{"b":"c"}}`)},
		{"tags that are not JSON", "Tags", set("Tags", `team=payments`)},
		{"a zero consumed quantity", "ConsumedQuantity", set("ConsumedQuantity", "0")},
		{"a cell a spreadsheet runs", "ResourceId", set("ResourceId", "=HYPERLINK(\"http://x\")")},
		{"a custom column with no x_ prefix", "AgentLabel", func(h []string, rows [][]string) ([]string, [][]string) {
			h = append(append([]string{}, h...), "AgentLabel")
			for r := range rows {
				rows[r] = append(rows[r], "x")
			}
			return h, rows
		}},
		{"a FOCUS column after a custom one", "BilledCost", func(h []string, rows [][]string) ([]string, [][]string) {
			h, rows = drop("BilledCost")(h, rows)
			h = append(h, "BilledCost")
			for r := range rows {
				rows[r] = append(rows[r], "0.010000")
			}
			return h, rows
		}},
		{"a row shorter than the header", "row", func(h []string, rows [][]string) ([]string, [][]string) {
			rows[0] = rows[0][:3]
			return h, rows
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := append([]string{}, d.header...)
			rows := make([][]string, len(d.records))
			for i := range d.records {
				rows[i] = append([]string{}, d.records[i]...)
			}
			h, rows = c.mutate(h, rows)
			err := validateDrop(h, rows)
			if err == nil {
				t.Fatalf("the validator passed a drop with %s", c.name)
			}
			if !strings.Contains(err.Error(), c.column) {
				t.Errorf("the refusal does not name %s: %v", c.column, err)
			}
		})
	}
}

func TestManySubCentCallsAreSummedExactly(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	var calls []call
	for i := 0; i < 10000; i++ {
		calls = append(calls, call{at: fmt.Sprintf("2026-10-05T%02d:%02d:%02d.%03dZ", i/3600%24, i/60%60, i%60, i%1000),
			agent: reconciler, unit: "payments", key: "recon-key", cost: "0.003333", in: "10", out: "5"})
	}
	for i := 0; i < 10; i++ {
		calls = append(calls, call{at: fmt.Sprintf("2026-10-05T23:00:%02d.000Z", i), agent: scorer, unit: "risk",
			key: "risk-key", cost: "0.0035", in: "1", out: "1"})
	}
	writeExport(t, in, "many.csv", true, calls...)
	r := dropAt(t, in, out, at)
	d := readDrop(t, out, at)
	// 10,000 x 0.003333 is 33.33 exactly; each rounded to a cent first is 0.
	if got := rowFor(t, d, "2026-10-05", reconciler, "claude-haiku-4-5")["BilledCost"]; got != "33.330000" {
		t.Errorf("ten thousand calls at $0.003333 sum to %s, want 33.330000", got)
	}
	// Ten at $0.0035 are three and a half cents, which rounded per call are none.
	if got := rowFor(t, d, "2026-10-05", scorer, "claude-haiku-4-5")["BilledCost"]; got != "0.035000" {
		t.Errorf("ten calls at $0.0035 sum to %s, want 0.035000", got)
	}
	if !strings.Contains(r.stdout, "33.365000 USD") {
		t.Errorf("the tool's total is not the exact sum 33.365000 USD:\n%s", r.stdout)
	}
}

// theTrace is three calls exported twice: before TokenFuse 1.7.0 the identity
// refusal is filed under its victim, from 1.7.0 under the credential.
func theTrace(newColumns bool) []call {
	refusal := call{at: "2026-10-07T10:05:00.000Z", run: "run-2", agent: "agent://acme.example/victim",
		in: "0", out: "0", cost: "0", blocked: true, key: "imposter", reason: "identity_mismatch"}
	if newColumns {
		refusal.agent, refusal.resource = "", "key:imposter"
	}
	return []call{
		{at: "2026-10-07T10:00:00.120Z", run: "run-1", agent: reconciler, unit: "payments", key: "recon-key",
			cost: "0.050000", in: "100", out: "50"},
		{at: "2026-10-07T10:00:01.480Z", run: "run-1", agent: reconciler, unit: "payments", key: "recon-key",
			cost: "0.090000", in: "200", out: "80"},
		refusal,
	}
}

func TestACallExportedTwiceIsInTheDropOnce(t *testing.T) {
	both, onlyNew := t.TempDir(), t.TempDir()
	writeExport(t, both, "tokenfuse-before-1.7.0.csv", false, theTrace(false)...)
	writeExport(t, both, "tokenfuse-1.7.0.csv", true, theTrace(true)...)
	writeExport(t, onlyNew, "tokenfuse-1.7.0.csv", true, theTrace(true)...)

	outBoth, outNew := t.TempDir(), t.TempDir()
	r := dropAt(t, both, outBoth, at)
	dropAt(t, onlyNew, outNew, at)
	dBoth, dNew := readDrop(t, outBoth, at), readDrop(t, outNew, at)
	if len(dBoth.rows) != 1 || dBoth.rows[0]["BilledCost"] != "0.140000" || dBoth.rows[0]["x_Calls"] != "2" {
		t.Fatalf("the same calls exported twice: %v, want one row of 0.140000 for 2 calls", dBoth.rows)
	}
	if !bytes.Equal(dBoth.data, dNew.data) {
		t.Error("a folder holding both exports makes a different drop from the newer export alone")
	}
	if !strings.Contains(r.stdout, "counted once") {
		t.Errorf("the tool does not pass on the reader's account of the calls it counted once:\n%s", r.stdout)
	}
}

func TestAnOlderExportWithoutTheNewColumnsStillMakesADrop(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeExport(t, in, "before-1.7.0.csv", false,
		call{at: "2026-10-02T09:00:00.000Z", agent: reconciler, unit: "payments", cost: "0.250000"},
		call{at: "2026-10-02T09:01:00.000Z", agent: reconciler, unit: "payments", cost: "0.250000"})
	dropAt(t, in, out, at)
	d := readDrop(t, out, at)
	if len(d.rows) != 1 {
		t.Fatalf("rows: %v", d.rows)
	}
	row := d.rows[0]
	if row["BilledCost"] != "0.500000" || row["x_KeyId"] != "" || strings.Contains(row["Tags"], "key_id") ||
		row["x_Calls"] != "2" {
		t.Errorf("an export from before the credential columns: %v", row)
	}
}

func TestBlockedCallsAreCountedNotCharged(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	writeExport(t, in, "a.csv", true,
		call{at: "2026-10-04T09:00:00.000Z", agent: reconciler, unit: "payments", key: "recon-key", cost: "0.100000", in: "100", out: "50"},
		call{at: "2026-10-04T09:01:00.000Z", agent: reconciler, unit: "payments", key: "recon-key", cost: "0.200000", in: "100", out: "50"},
		call{at: "2026-10-04T09:02:00.000Z", agent: reconciler, unit: "payments", key: "recon-key", cost: "0",
			in: "500", out: "0", blocked: true, reason: "budget_exceeded"},
		call{at: "2026-10-04T09:03:00.000Z", agent: "", resource: "key:imposter", unit: "payments", key: "imposter",
			cost: "0", in: "0", out: "0", blocked: true, reason: "identity_mismatch"},
	)
	r := dropAt(t, in, out, at)
	d := readDrop(t, out, at)
	if len(d.rows) != 1 {
		t.Fatalf("rows: %v, want one: the imposter's calls were all blocked and spent nothing", d.rows)
	}
	row := d.rows[0]
	want := map[string]string{"BilledCost": "0.300000", "x_Calls": "2", "x_BlockedCalls": "1",
		"ConsumedQuantity": "2", "PricingQuantity": "300", "x_TokensIn": "200", "x_TokensOut": "100"}
	for c, v := range want {
		if row[c] != v {
			t.Errorf("%s = %q, want %q: a blocked call is counted on its row and never charged or consumed", c, row[c], v)
		}
	}
	if !strings.Contains(r.stdout, "1 blocked call in a group that spent nothing") {
		t.Errorf("the tool does not say how many blocked calls had no row:\n%s", r.stdout)
	}
}

func TestHostileInputMakesNoDrop(t *testing.T) {
	ok := call{at: "2026-10-01T09:00:00.000Z", agent: reconciler, unit: "payments", key: "recon-key"}
	cases := []struct {
		name  string
		write func(t *testing.T, dir string)
		says  string
	}{
		{"an unterminated quote", func(t *testing.T, dir string) {
			writeExport(t, dir, "a.csv", true, ok)
			writeRaw(t, dir, "b.csv", tfHeader29+"\n\"0.1,0.1,USD\n")
		}, "not read"},
		{"a ragged row", func(t *testing.T, dir string) {
			// Beside a good row, so a drop COULD be made: the refusal is
			// about the row left out, not about an empty month.
			writeRaw(t, dir, "a.csv", tfHeader29+"\n"+strings.Join(ok.fields(true), ",")+"\n0.1,0.1,USD\n")
		}, "refused"},
		{"another currency", func(t *testing.T, dir string) {
			c := ok
			c.currency = "EUR"
			writeExport(t, dir, "a.csv", true, ok, c)
		}, "EUR"},
		{"an amount past what 64 bits of micro-dollars hold", func(t *testing.T, dir string) {
			c := ok
			c.cost = "18446744073710" // wrapped to $0.448384 before money.ParseMicros refused it
			writeExport(t, dir, "a.csv", true, ok, c)
		}, "18446744073710"},
		{"token counts whose sum overflows", func(t *testing.T, dir string) {
			a, b := ok, ok
			a.in, b.in = "9223372036854775000", "9223372036854775000"
			b.at = "2026-10-01T09:00:01.000Z"
			writeExport(t, dir, "a.csv", true, a, b)
		}, "overflow"},
		{"an agent that opens as a formula", func(t *testing.T, dir string) {
			c := ok
			c.agent = `=HYPERLINK("http://evil.example","x")`
			writeExport(t, dir, "a.csv", true, ok, c)
		}, "formula"},
		{"a model that opens as a formula", func(t *testing.T, dir string) {
			c := ok
			c.model = "+cmd|' /C calc'!A0"
			writeExport(t, dir, "a.csv", true, c)
		}, "formula"},
		{"a unit that opens as a formula", func(t *testing.T, dir string) {
			c := ok
			c.unit = "@SUM(A1)"
			writeExport(t, dir, "a.csv", true, ok, c)
		}, "x_unit"},
		{"an agent carrying a text-direction override", func(t *testing.T, dir string) {
			c := ok
			c.agent = "agent://acme.example/\u202egnp.exe"
			writeExport(t, dir, "a.csv", true, c)
		}, "control"},
		{"an agent past its bound", func(t *testing.T, dir string) {
			c := ok
			c.agent = "agent://" + strings.Repeat("a", 600)
			writeExport(t, dir, "a.csv", true, c)
		}, "byte limit"},
		{"no provider named", func(t *testing.T, dir string) {
			c := ok
			c.provider = "-"
			writeExport(t, dir, "a.csv", true, c)
		}, "ProviderName"},
		{"a purchase row", func(t *testing.T, dir string) {
			writeRaw(t, dir, "a.csv", tfHeader29+",ChargeCategory,CommitmentDiscountId,CommitmentDiscountQuantity\n"+
				strings.Join(ok.fields(true), ",")+",Purchase,cd-1,1\n")
		}, "Purchase"},
		{"a folder with nothing to read", func(t *testing.T, dir string) {}, "no *.csv"},
		{"a folder holding only a link", func(t *testing.T, dir string) {
			target := filepath.Join(t.TempDir(), "elsewhere.csv")
			writeExport(t, filepath.Dir(target), "elsewhere.csv", true, ok)
			if err := os.Symlink(target, filepath.Join(dir, "a.csv")); err != nil {
				t.Skip("no symlinks here:", err)
			}
		}, "not followed"},
		{"a link beside a good file", func(t *testing.T, dir string) {
			writeExport(t, dir, "a.csv", true, ok)
			target := filepath.Join(t.TempDir(), "elsewhere.csv")
			writeExport(t, filepath.Dir(target), "elsewhere.csv", true, ok)
			if err := os.Symlink(target, filepath.Join(dir, "b.csv")); err != nil {
				t.Skip("no symlinks here:", err)
			}
		}, "not followed"},
		{"no call in the month", func(t *testing.T, dir string) {
			c := ok
			c.at = "2026-09-15T09:00:00.000Z"
			writeExport(t, dir, "a.csv", true, c)
		}, "no call"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in, out := t.TempDir(), t.TempDir()
			c.write(t, in)
			r := runTool(t, at, baseFlags(in, out)...)
			if r.code == 0 {
				t.Fatalf("a drop was written from %s:\n%s", c.name, r.stdout)
			}
			if got := filesUnder(t, out); len(got) != 0 {
				t.Errorf("a refused drop left files behind: %v", got)
			}
			if !strings.Contains(r.stderr, c.says) {
				t.Errorf("the refusal does not say %q:\n%s", c.says, r.stderr)
			}
		})
	}
}

func TestFlagsThatWouldWriteOutsideTheFolderAreRefused(t *testing.T) {
	in := t.TempDir()
	writeExport(t, in, "a.csv", true, call{at: "2026-10-01T09:00:00.000Z", agent: reconciler, unit: "payments"})
	set := func(flags []string, name, v string) []string {
		out := append([]string{}, flags...)
		for i := 0; i < len(out)-1; i++ {
			if out[i] == name {
				out[i+1] = v
				return out
			}
		}
		return append(out, name, v)
	}
	cases := []struct{ flag, value, says string }{
		{"-vendor", "../escaped", "-vendor"},
		{"-vendor", "a/b", "-vendor"},
		{"-vendor", "..", "-vendor"},
		{"-vendor", ".hidden", "-vendor"},
		{"-sub-dir", "../../escaped", "-sub-dir"},
		{"-sub-dir", "/etc", "-sub-dir"},
		{"-sub-dir", "a//b", "-sub-dir"},
		{"-sub-dir", "a/../../b", "-sub-dir"},
		{"-prefix", "x/y", "-prefix"},
		{"-prefix", "..", "-prefix"},
		{"-prefix", "", "-prefix"},
		{"-root-dir", "", "-root-dir"},
		{"-root-dir", "bucket/inner", "-root-dir"},
		{"-month", "2026-13", "-month"},
		{"-month", "26-10", "-month"},
		{"-month", "2026-10-01", "-month"},
		{"-billing-account-id", "", "-billing-account-id"},
		{"-billing-account-id", "=cmd", "-billing-account-id"},
		{"-billing-account-id", "acme\nsecond", "-billing-account-id"},
		{"-billing-account-name", "@name", "-billing-account-name"},
		{"-in", "", "-in"},
		{"-out", "", "-out"},
	}
	for _, c := range cases {
		t.Run(c.flag+"="+c.value, func(t *testing.T) {
			parent := t.TempDir()
			out := filepath.Join(parent, "drop")
			r := runTool(t, at, set(baseFlags(in, out), c.flag, c.value)...)
			if r.code != 2 {
				t.Errorf("exit %d, want 2 for a flag the tool cannot use; stderr: %s", r.code, r.stderr)
			}
			if !strings.Contains(r.stderr, c.says) {
				t.Errorf("the refusal does not name %s: %s", c.says, r.stderr)
			}
			if got := filesUnder(t, parent); len(got) != 0 {
				t.Errorf("a refused flag still wrote: %v", got)
			}
		})
	}

	// A link where the drop's own folders go would carry the drop wherever
	// it points.
	t.Run("a symbolic link on the drop's path", func(t *testing.T) {
		out, elsewhere := t.TempDir(), t.TempDir()
		if err := os.Symlink(elsewhere, filepath.Join(out, "TokenFuse")); err != nil {
			t.Skip("no symlinks here:", err)
		}
		r := runTool(t, at, baseFlags(in, out)...)
		if r.code == 0 {
			t.Fatal("the drop followed a symbolic link out of its folder")
		}
		if got := filesUnder(t, elsewhere); len(got) != 0 {
			t.Errorf("files written through the link: %v", got)
		}
		if !strings.Contains(r.stderr, "symbolic link") {
			t.Errorf("the refusal does not name the link: %s", r.stderr)
		}
	})
}

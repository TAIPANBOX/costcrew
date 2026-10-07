package connectors

// The provider usage readers (anthropic-usage, openai-usage): a folder of
// the providers' own JSON reports, read strictly, stored per day, model and
// token type, replaced rather than added to on a second import.

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	anthropicUsageFixture = "testdata/provider-usage/anthropic"
	openaiUsageFixture    = "testdata/provider-usage/openai"
)

func copyUsageFixture(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func usageStore(t *testing.T, id, dir string, extra map[string]string) *sql.DB {
	t.Helper()
	db := openTestStore(t).DB()
	cfg := map[string]string{"path": dir}
	for k, v := range extra {
		cfg[k] = v
	}
	if err := Save(db, id, cfg); err != nil {
		t.Fatal(err)
	}
	return db
}

// providerCost is the stored cost for one connector, day and model.
func providerCost(t *testing.T, db *sql.DB, id, day, model string) int64 {
	t.Helper()
	var v sql.NullInt64
	if err := db.QueryRow(`SELECT SUM(cost_microusd) FROM provider_usage
		WHERE connector=? AND report='cost' AND day=? AND model=?`, id, day, model).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v.Int64
}

func providerTokens(t *testing.T, db *sql.DB, id, day, model, tokenType string) int64 {
	t.Helper()
	var v sql.NullInt64
	if err := db.QueryRow(`SELECT SUM(tokens) FROM provider_usage
		WHERE connector=? AND report='usage' AND day=? AND model=? AND token_type=?`,
		id, day, model, tokenType).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v.Int64
}

func usageRowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	ok, err := tableExists(db, "provider_usage")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return 0
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM provider_usage`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAnthropicUsageFolderIsRead(t *testing.T) {
	dir := copyUsageFixture(t, anthropicUsageFixture)
	db := usageStore(t, "anthropic-usage", dir, nil)
	msg, err := Import(db, "anthropic-usage", false, ImportOptions{Actor: "boss"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	for _, want := range []string{"Read 2 files (1 usage, 1 cost)", "Cost: 2 days (2026-10-01 to 2026-10-02)",
		"Usage: 2 days (2026-10-01 to 2026-10-02)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the sentence %q does not say %q", msg, want)
		}
	}
	// "35.0" + "70.5" + "0.00005" cents: 350000 + 705000 + 1 micro-dollars.
	// The last is half a micro-dollar, rounded half away from zero, once.
	if got := providerCost(t, db, "anthropic-usage", "2026-10-01", "claude-haiku-4-5"); got != 1_055_001 {
		t.Errorf("haiku cost on 2026-10-01 = %d micros, want 1055001", got)
	}
	// "123.78912" cents is $1.2378912: 1237891.2 micros, rounded to 1237891.
	if got := providerCost(t, db, "anthropic-usage", "2026-10-01", "claude-sonnet-4-5"); got != 1_237_891 {
		t.Errorf("sonnet cost on 2026-10-01 = %d micros, want 1237891", got)
	}
	// A web search cost has no model: kept under the empty model, typed by
	// its cost_type, never folded into a model's figure.
	var ws int64
	if err := db.QueryRow(`SELECT cost_microusd FROM provider_usage WHERE connector='anthropic-usage'
		AND report='cost' AND day='2026-10-01' AND model='' AND token_type='web_search'`).Scan(&ws); err != nil {
		t.Fatalf("the web search cost row: %v", err)
	}
	if ws != 10_000_000 {
		t.Errorf("web search cost = %d micros, want 10000000 (\"1000\" cents)", ws)
	}
	if got := providerCost(t, db, "anthropic-usage", "2026-10-02", "claude-haiku-4-5"); got != 100_000 {
		t.Errorf("haiku cost on 2026-10-02 = %d micros, want 100000", got)
	}
	for _, c := range []struct {
		day, model, tt string
		want           int64
	}{
		{"2026-10-01", "claude-haiku-4-5", "uncached_input_tokens", 1000},
		{"2026-10-01", "claude-haiku-4-5", "cache_read_input_tokens", 200},
		{"2026-10-01", "claude-haiku-4-5", "cache_creation.ephemeral_5m_input_tokens", 50},
		{"2026-10-01", "claude-haiku-4-5", "output_tokens", 500},
		{"2026-10-01", "claude-sonnet-4-5", "uncached_input_tokens", 3000},
		{"2026-10-02", "claude-haiku-4-5", "output_tokens", 20},
	} {
		if got := providerTokens(t, db, "anthropic-usage", c.day, c.model, c.tt); got != c.want {
			t.Errorf("%s %s %s = %d tokens, want %d", c.day, c.model, c.tt, got, c.want)
		}
	}
	var key, scope string
	if err := db.QueryRow(`SELECT api_key_id, scope FROM provider_usage WHERE connector='anthropic-usage'
		AND report='usage' AND model='claude-sonnet-4-5' LIMIT 1`).Scan(&key, &scope); err != nil {
		t.Fatal(err)
	}
	if key != "apikey_B" || scope != "wrkspc_1" {
		t.Errorf("sonnet usage carries key %q workspace %q, want apikey_B wrkspc_1", key, scope)
	}
}

func TestOpenAIUsageFolderIsRead(t *testing.T) {
	dir := copyUsageFixture(t, openaiUsageFixture)
	db := usageStore(t, "openai-usage", dir, nil)
	if _, err := Import(db, "openai-usage", false, ImportOptions{}); err != nil {
		t.Fatalf("import: %v", err)
	}
	// 0.06 and 1.5e-3 dollars, both read exactly: 60000 + 1500.
	if got := providerCost(t, db, "openai-usage", "2026-10-01", "gpt-5-mini"); got != 61_500 {
		t.Errorf("gpt-5-mini cost = %d micros, want 61500", got)
	}
	// A line item without "model, type" is kept whole as its own model.
	if got := providerCost(t, db, "openai-usage", "2026-10-01", "web search"); got != 100_000 {
		t.Errorf("the \"web search\" line item = %d micros, want 100000", got)
	}
	if got := providerTokens(t, db, "openai-usage", "2026-10-01", "gpt-5-mini", "input_tokens"); got != 1000 {
		t.Errorf("input_tokens = %d, want 1000", got)
	}
	if got := providerTokens(t, db, "openai-usage", "2026-10-01", "gpt-5-mini", "input_cached_tokens"); got != 200 {
		t.Errorf("input_cached_tokens = %d, want 200", got)
	}
}

func TestBothUsageConnectorsAreBuiltAndFree(t *testing.T) {
	for _, id := range []string{"anthropic-usage", "openai-usage"} {
		c, ok := Get(id)
		if !ok {
			t.Fatalf("%s is not in the catalogue", id)
		}
		if c.Status != Built {
			t.Errorf("%s is %s, want built", id, c.Status)
		}
		if c.Metered {
			t.Errorf("%s says metered; neither provider's documentation names a charge for these reports", id)
		}
		if c.Feeds != "provider_usage" {
			t.Errorf("%s feeds %q, want provider_usage", id, c.Feeds)
		}
		for _, in := range c.Inputs {
			if in.Secret {
				t.Errorf("%s asks the console for a secret (%s); the console reads a folder and holds no key", id, in.Name)
			}
		}
	}
}

func TestProviderUsageImportTwiceChangesNothing(t *testing.T) {
	dir := copyUsageFixture(t, anthropicUsageFixture)
	db := usageStore(t, "anthropic-usage", dir, nil)
	snapshot := func() string {
		rows, err := db.Query(`SELECT report, day, model, token_type, api_key_id, scope, tokens, cost_microusd
			FROM provider_usage ORDER BY 1,2,3,4,5,6`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var b strings.Builder
		for rows.Next() {
			var a [6]string
			var tk, c int64
			if err := rows.Scan(&a[0], &a[1], &a[2], &a[3], &a[4], &a[5], &tk, &c); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(&b, a, tk, c)
		}
		return b.String()
	}
	if _, err := Import(db, "anthropic-usage", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	first := snapshot()
	if _, err := Import(db, "anthropic-usage", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if second := snapshot(); second != first || first == "" {
		t.Errorf("a second import changed the table:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// A newer pull of the same day replaces it; it is never added to it.
func TestALaterFileReplacesADayRatherThanAddingToIt(t *testing.T) {
	dir := copyUsageFixture(t, anthropicUsageFixture)
	db := usageStore(t, "anthropic-usage", dir, nil)
	if _, err := Import(db, "anthropic-usage", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	later := `{"data":[{"starting_at":"2026-10-02T00:00:00Z","ending_at":"2026-10-03T00:00:00Z","results":[
		{"amount":"12","currency":"USD","model":"claude-haiku-4-5","token_type":"output_tokens"}]}],
		"has_more":false,"next_page":null}`
	if err := os.WriteFile(filepath.Join(dir, "cost-20261009T000000Z-2026-10-02-2026-10-02.json"), []byte(later), 0o600); err != nil {
		t.Fatal(err)
	}
	msg, err := Import(db, "anthropic-usage", false, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := providerCost(t, db, "anthropic-usage", "2026-10-02", "claude-haiku-4-5"); got != 120_000 {
		t.Errorf("2026-10-02 haiku = %d micros after a newer pull of the day, want 120000 (replaced, not 220000 added)", got)
	}
	if got := providerCost(t, db, "anthropic-usage", "2026-10-01", "claude-haiku-4-5"); got != 1_055_001 {
		t.Errorf("2026-10-01 was not in the newer file and changed to %d", got)
	}
	if !strings.Contains(msg, "1 day carried by more than one file") {
		t.Errorf("the sentence does not say a day was superseded: %q", msg)
	}
	var src string
	if err := db.QueryRow(`SELECT source_file FROM provider_usage_days WHERE connector='anthropic-usage'
		AND report='cost' AND day='2026-10-02'`).Scan(&src); err != nil {
		t.Fatal(err)
	}
	if src != "cost-20261009T000000Z-2026-10-02-2026-10-02.json" {
		t.Errorf("the day is credited to %q, want the newer file", src)
	}
}

func TestProviderUsageTestDescribesAndWritesNothing(t *testing.T) {
	dir := copyUsageFixture(t, anthropicUsageFixture)
	db := usageStore(t, "anthropic-usage", dir, nil)
	msg, ok, err := Test(db, "anthropic-usage", func(string) string { return "" })
	if err != nil || !ok {
		t.Fatalf("Test: %q ok=%v err=%v", msg, ok, err)
	}
	if !strings.HasPrefix(msg, "Read 2 files") || !strings.Contains(msg, "Would write") {
		t.Errorf("Test's sentence does not describe a dry run: %q", msg)
	}
	if n := usageRowCount(t, db); n != 0 {
		t.Errorf("Test wrote %d provider_usage rows", n)
	}
}

func TestDecimalMicrosIsExactAndBounded(t *testing.T) {
	for _, c := range []struct {
		lit   string
		shift int
		want  int64
	}{
		{"123.78912", 4, 1_237_891},
		{"0.00005", 4, 1},
		{"-0.00005", 4, -1},
		{"0.00004", 4, 0},
		{"1.5e-3", 6, 1500},
		{"0.06", 6, 60_000},
		{"1E+2", 6, 100_000_000},
		{"0.1", 6, 100_000}, // a float64 0.1 is not 0.1; this is
		// float64(0.0000035) is 3.4999999999999999e-06, which a float route
		// rounds to 3 micro-dollars. The literal is a tie, and ties go up.
		{"0.0000035", 6, 4},
		{"10000000", 6, 10_000_000_000_000},
	} {
		got, err := decimalMicros("x", c.lit, c.shift)
		if err != nil || got != c.want {
			t.Errorf("decimalMicros(%q, %d) = %d, %v; want %d", c.lit, c.shift, got, err, c.want)
		}
	}
	for _, lit := range []string{"1e999999999", "1e41", "10000000.000001", "NaN", "0x10", "1..2", "",
		strings.Repeat("9", 65), "+1"} {
		if got, err := decimalMicros("x", lit, 6); err == nil {
			t.Errorf("decimalMicros(%q) = %d, want a refusal", lit, got)
		}
	}
}

func TestMicrosExact(t *testing.T) {
	for in, want := range map[int64]string{0: "0.000000", 1: "0.000001", -1: "-0.000001",
		1_237_891: "1.237891", -1_000_000: "-1.000000", -9223372036854775808: "-9223372036854.775808"} {
		if got := MicrosExact(in); got != want {
			t.Errorf("MicrosExact(%d) = %q, want %q", in, got, want)
		}
	}
}

// ------------------------------------------------------------------ hostile

const goodAnthropicCost = `{"data":[{"starting_at":"2026-10-05T00:00:00Z","ending_at":"2026-10-06T00:00:00Z",
	"results":[{"amount":"100","currency":"USD","model":"claude-haiku-4-5","token_type":"output_tokens"}]}],
	"has_more":false,"next_page":null}`

func anthropicCostPage(day, results string, more bool, next string) string {
	d := strings.Split(day, "-")
	var dd int
	fmt.Sscanf(d[2], "%d", &dd)
	end := fmt.Sprintf("%s-%s-%02d", d[0], d[1], dd+1)
	n := "null"
	if next != "" {
		n = `"` + next + `"`
	}
	return fmt.Sprintf(`{"data":[{"starting_at":"%sT00:00:00Z","ending_at":"%sT00:00:00Z","results":[%s]}],"has_more":%v,"next_page":%s}`,
		day, end, results, more, n)
}

// TestProviderUsageHostileInput: every file below sits beside one good file.
// The bad one is refused by name with its reason, nothing it carries reaches
// the store, and the good one still lands.
func TestProviderUsageHostileInput(t *testing.T) {
	haiku := `{"amount":"100","currency":"USD","model":"claude-haiku-4-5","token_type":"output_tokens"}`
	cases := []struct {
		name, file, body, reason string
	}{
		{"truncated", "cost-bad.json", goodAnthropicCost[:len(goodAnthropicCost)/2], "truncated"},
		{"not JSON at all", "cost-bad.json", "amount,currency\n100,USD\n", "does not parse"},
		{"trailing data", "cost-bad.json", goodAnthropicCost + `{"x":1}`, "more after the JSON value"},
		{"amount as a number", "cost-bad.json", anthropicCostPage("2026-10-01",
			`{"amount":100,"currency":"USD","model":"m"}`, false, ""), "amount is a number, want a decimal string"},
		{"tokens as a string", "usage-bad.json", anthropicCostPage("2026-10-01",
			`{"uncached_input_tokens":"10","cache_read_input_tokens":0,"output_tokens":1,"model":"m"}`, false, ""),
			"uncached_input_tokens is a string"},
		{"tokens with a fraction", "usage-bad.json", anthropicCostPage("2026-10-01",
			`{"uncached_input_tokens":10.5,"cache_read_input_tokens":0,"output_tokens":1,"model":"m"}`, false, ""),
			"not a plain non-negative whole number"},
		{"negative tokens", "usage-bad.json", anthropicCostPage("2026-10-01",
			`{"uncached_input_tokens":-1,"cache_read_input_tokens":0,"output_tokens":1,"model":"m"}`, false, ""),
			"not a plain non-negative whole number"},
		{"a huge token count", "usage-bad.json", anthropicCostPage("2026-10-01",
			`{"uncached_input_tokens":99999999999999999999999999999,"cache_read_input_tokens":0,"output_tokens":1,"model":"m"}`, false, ""),
			"not a plain non-negative whole number"},
		{"a huge amount", "cost-bad.json", anthropicCostPage("2026-10-01",
			`{"amount":"999999999999999999","currency":"USD","model":"m"}`, false, ""), "over the cap"},
		{"an exponent that would never finish", "cost-bad.json", anthropicCostPage("2026-10-01",
			`{"amount":"1e999999999","currency":"USD","model":"m"}`, false, ""), "exponent past"},
		{"a duplicate key", "cost-bad.json", anthropicCostPage("2026-10-01",
			`{"amount":"1","amount":"999999","currency":"USD","model":"m"}`, false, ""), "appears twice"},
		{"another currency", "cost-bad.json", anthropicCostPage("2026-10-01",
			`{"amount":"1","currency":"EUR","model":"m"}`, false, ""), "USD only"},
		{"a control character in a model name", "cost-bad.json", anthropicCostPage("2026-10-01",
			`{"amount":"1","currency":"USD","model":"evil\nrow"}`, false, ""), "control character"},
		{"a pagination loop saved to disk", "cost-bad.json",
			"[" + anthropicCostPage("2026-10-01", haiku, true, "p2") + "," +
				anthropicCostPage("2026-10-01", haiku, false, "") + "]", "pagination loop"},
		{"the last page says more exist", "cost-bad.json", anthropicCostPage("2026-10-01", haiku, true, "p2"),
			"has_more: true"},
		{"an hourly bucket", "cost-bad.json", `{"data":[{"starting_at":"2026-10-01T00:00:00Z","ending_at":"2026-10-01T01:00:00Z","results":[]}],"has_more":false}`,
			"not one whole day"},
		{"a bucket not at midnight", "cost-bad.json", `{"data":[{"starting_at":"2026-10-01T05:00:00Z","ending_at":"2026-10-02T05:00:00Z","results":[]}],"has_more":false}`,
			"not at a UTC midnight"},
		{"a bucket in another time zone", "cost-bad.json", `{"data":[{"starting_at":"2026-10-01T00:00:00+02:00","ending_at":"2026-10-02T00:00:00+02:00","results":[]}],"has_more":false}`,
			"not UTC"},
		{"no buckets at all", "cost-bad.json", `{"data":[],"has_more":false}`, "no bucket"},
		{"a top-level string", "cost-bad.json", `"hello"`, "holds a string"},
		{"nesting deeper than any report", "cost-bad.json", strings.Repeat("[", 40) + strings.Repeat("]", 40), "nests deeper"},
		{"results not a list", "cost-bad.json", `{"data":[{"starting_at":"2026-10-01T00:00:00Z","ending_at":"2026-10-02T00:00:00Z","results":{"amount":"1"}}],"has_more":false}`,
			"results is an object, want an array"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "cost-good.json"), []byte(goodAnthropicCost), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, c.file), []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			db := usageStore(t, "anthropic-usage", dir, nil)
			msg, err := Import(db, "anthropic-usage", false, ImportOptions{})
			if err != nil {
				t.Fatalf("one good file sits beside the bad one, and the import failed whole: %v", err)
			}
			if !strings.Contains(msg, c.file+": ") || !strings.Contains(msg, c.reason) {
				t.Errorf("the sentence does not refuse %s for %q:\n%s", c.file, c.reason, msg)
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM provider_usage WHERE day<>'2026-10-05'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("%d rows from the refused file reached the store", n)
			}
			if got := providerCost(t, db, "anthropic-usage", "2026-10-05", "claude-haiku-4-5"); got != 1_000_000 {
				t.Errorf("the good file's figure is %d, want 1000000", got)
			}
		})
	}
}

// encoding/json would match "AMOUNT" to an amount field and let it win. An
// unknown field is ignored here, and a field that merely LOOKS like a known
// one in another case is an unknown field.
func TestAnUnknownFieldNeverChangesAKnownOne(t *testing.T) {
	dir := t.TempDir()
	body := anthropicCostPage("2026-10-01",
		`{"amount":"100","AMOUNT":"999999","Amount":"5","currency":"USD","model":"m","token_type":"output_tokens",
		  "a_new_field":{"deep":[{"amount":"77777"}]}}`, false, "")
	if err := os.WriteFile(filepath.Join(dir, "cost-x.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	db := usageStore(t, "anthropic-usage", dir, nil)
	if _, err := Import(db, "anthropic-usage", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := providerCost(t, db, "anthropic-usage", "2026-10-01", "m"); got != 1_000_000 {
		t.Errorf("cost = %d micros, want 1000000 from \"amount\":\"100\" alone", got)
	}
}

func TestOpenAIHostileInput(t *testing.T) {
	page := func(results string) string {
		return `{"object":"page","data":[{"object":"bucket","start_time":1790812800,"end_time":1790899200,"results":[` +
			results + `]}],"has_more":false,"next_page":null}`
	}
	for _, c := range []struct{ name, file, body, reason string }{
		{"amount as a string", "cost-x.json", page(`{"object":"organization.costs.result","amount":{"value":"0.06","currency":"usd"},"line_item":"m, input"}`),
			"amount.value is a string"},
		{"a costs result in a usage file", "usage-x.json", page(`{"object":"organization.costs.result","amount":{"value":0.06,"currency":"usd"}}`),
			"not a completions usage result"},
		{"a bucket of the wrong width", "cost-x.json", `{"object":"page","data":[{"object":"bucket","start_time":1790812800,"end_time":1790816400,"results":[]}],"has_more":false}`,
			"not one whole day"},
		{"cached more than the total", "usage-x.json", page(`{"object":"organization.usage.completions.result","input_tokens":10,"input_cached_tokens":11,"output_tokens":1,"num_model_requests":1}`),
			"more than input_tokens"},
		{"not a page", "cost-x.json", `{"object":"list","data":[],"has_more":false}`, "not \"page\""},
		{"euros", "cost-x.json", page(`{"object":"organization.costs.result","amount":{"value":1,"currency":"eur"},"line_item":"m, input"}`),
			"USD only"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, c.file), []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			db := usageStore(t, "openai-usage", dir, nil)
			_, err := Import(db, "openai-usage", false, ImportOptions{})
			if err == nil || !strings.Contains(err.Error(), c.reason) {
				t.Errorf("err = %v, want a refusal naming %q", err, c.reason)
			}
			if n := usageRowCount(t, db); n != 0 {
				t.Errorf("%d rows reached the store", n)
			}
		})
	}
}

func TestProviderUsageFolderBoundaries(t *testing.T) {
	t.Run("a file over max_file_mb is refused by its size", func(t *testing.T) {
		dir := t.TempDir()
		big := `{"data":[],"has_more":false,"pad":"` + strings.Repeat("x", 1<<20+10) + `"}`
		if err := os.WriteFile(filepath.Join(dir, "cost-big.json"), []byte(big), 0o600); err != nil {
			t.Fatal(err)
		}
		db := usageStore(t, "anthropic-usage", dir, map[string]string{"max_file_mb": "1"})
		_, err := Import(db, "anthropic-usage", false, ImportOptions{})
		if err == nil || !strings.Contains(err.Error(), "over the 1 MB limit") {
			t.Errorf("err = %v, want the size refusal", err)
		}
	})
	t.Run("a symlink is not followed", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(t.TempDir(), "elsewhere.json")
		if err := os.WriteFile(target, []byte(goodAnthropicCost), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "cost-link.json")); err != nil {
			t.Skip("no symlinks here:", err)
		}
		db := usageStore(t, "anthropic-usage", dir, nil)
		_, err := Import(db, "anthropic-usage", false, ImportOptions{})
		if err == nil || !strings.Contains(err.Error(), "no usage-*.json or cost-*.json files") {
			t.Errorf("err = %v, want the folder read as holding no report", err)
		}
	})
	t.Run("files of other names are counted, not read", func(t *testing.T) {
		dir := t.TempDir()
		for name, body := range map[string]string{"cost-a.json": goodAnthropicCost, "notes.txt": "x", "costs.json": "{"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		db := usageStore(t, "anthropic-usage", dir, nil)
		msg, err := Import(db, "anthropic-usage", false, ImportOptions{})
		if err != nil || !strings.Contains(msg, "2 other entries are in the folder") {
			t.Errorf("msg %q err %v, want two other entries counted", msg, err)
		}
	})
	t.Run("the same bytes under two names are read once", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{"cost-a.json", "cost-b.json"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(goodAnthropicCost), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		db := usageStore(t, "anthropic-usage", dir, nil)
		msg, err := Import(db, "anthropic-usage", false, ImportOptions{})
		if err != nil || !strings.Contains(msg, "1 file with the same bytes as another") {
			t.Errorf("msg %q err %v", msg, err)
		}
		if got := providerCost(t, db, "anthropic-usage", "2026-10-05", "claude-haiku-4-5"); got != 1_000_000 {
			t.Errorf("cost = %d, want 1000000 counted once", got)
		}
	})
	t.Run("a bad setting is refused, not dropped", func(t *testing.T) {
		for k, v := range map[string]string{"api_key_ids": "apikey_A, drop table", "tolerance_bp": "-1",
			"tolerance_cents": "lots", "max_file_mb": "0", "workspace_ids": strings.Repeat("w", 200)} {
			db := usageStore(t, "anthropic-usage", t.TempDir(), map[string]string{k: v})
			if _, err := Import(db, "anthropic-usage", false, ImportOptions{}); err == nil || !strings.Contains(err.Error(), k) {
				t.Errorf("%s=%q: err = %v, want a refusal naming the setting", k, v, err)
			}
		}
	})
}

// Importing provider usage never writes charges: the provider's figure is a
// check on the gateway's, not a second copy of the same money.
func TestProviderUsageNeverWritesCharges(t *testing.T) {
	dir := copyUsageFixture(t, anthropicUsageFixture)
	db := usageStore(t, "anthropic-usage", dir, nil)
	if err := EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(db, "anthropic-usage", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM charges`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("importing provider usage wrote %d charges rows", n)
	}
}

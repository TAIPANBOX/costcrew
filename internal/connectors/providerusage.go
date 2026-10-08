package connectors

// The provider usage readers: anthropic-usage and openai-usage.
//
// WHY THESE EXIST
//
// The crew's agents share one provider API key. Which agent spent what is
// known only from the gateway's own per-call rows (ai_calls, written by the
// tokenfuse-focus reader); the provider sees one key and nothing about who
// was behind it. That attribution is only as honest as the gateway's rows
// are complete, and the control that keeps it honest is a reconciliation:
// what the provider says the key cost per model per day, against the sum of
// the gateway's rows for the same model and day, with the gap shown as its
// own figure. These readers bring in the provider's half; reconcile.go does
// the comparison. Nothing here writes charges: the provider's figure is a
// control on the gateway's figure, never a second copy of the same money.
//
// THE CONSOLE NEVER CALLS THE PROVIDER
//
// Invariant 64 holds that the console makes no outbound call of its own
// (internal/web/egress_test.go walks every package the console imports and
// refuses any that builds an outbound request). So the console's reader reads
// a FOLDER of the providers' own JSON responses, saved verbatim, and nothing
// else. The network half is a separate binary, costcrew-usage (tools/usage),
// which an operator runs with an admin key in its environment, writes the
// pages it fetched into that folder, and is never imported by the console.
// The folder is also what makes the reader work offline: a person can save
// the same JSON by hand.
//
// WHICH ENDPOINTS, PINNED (read 2026-10-07)
//
// Anthropic, https://platform.claude.com/docs/en/api/admin-api/usage-cost/get-messages-usage-report
// and .../get-cost-report, with the guide at
// https://platform.claude.com/docs/en/build-with-claude/usage-cost-api:
//
//	GET /v1/organizations/usage_report/messages   tokens per bucket
//	GET /v1/organizations/cost_report             money per bucket
//	page:   {data: [bucket], has_more: bool, next_page: string|null}
//	bucket: {starting_at: RFC 3339, ending_at: RFC 3339, results: [...]}
//	usage result: uncached_input_tokens, cache_read_input_tokens,
//	  output_tokens (numbers), cache_creation {ephemeral_1h_input_tokens,
//	  ephemeral_5m_input_tokens}, model, api_key_id, workspace_id (string|null)
//	cost result: amount (a DECIMAL STRING in the lowest currency unit,
//	  "123.45" in USD is $1.23), currency ("USD"), model, token_type,
//	  cost_type, workspace_id (string|null)
//
// The cost report groups by description and workspace_id only. It has no
// api_key_id, so Anthropic's MONEY can be scoped to a workspace and not to a
// key; its TOKENS can be scoped to a key. Priority Tier costs are not in the
// cost report at all (the guide says so), and code execution is in the cost
// report and not in the usage report.
//
// OpenAI, the public OpenAPI document at github.com/openai/openai-openapi,
// openapi.json at commit 17f9a665031777071b26e63356d78280bf7c128c (the
// platform.openai.com reference pages answered 403 to a plain fetch):
//
//	GET /v1/organization/usage/completions        tokens per bucket
//	GET /v1/organization/costs                    money per bucket
//	page:   {object: "page", data: [bucket], has_more, next_page}
//	bucket: {object: "bucket", start_time, end_time (Unix seconds), results}
//	usage result (object "organization.usage.completions.result"):
//	  input_tokens (cached and cache-write included), input_cached_tokens,
//	  output_tokens, model, api_key_id, project_id
//	cost result (object "organization.costs.result"): amount {value: a JSON
//	  NUMBER in whole currency units, currency: "usd"}, line_item
//	  ("gpt-x, input_tokens" in the spec's own example), project_id, api_key_id
//
// Neither document names a charge for calling these endpoints, which is why
// both entries are Metered: false. That is a reading of what the documents
// say, not a measurement of a bill.
//
// WHAT A FILE IS
//
// A file is named usage-*.json or cost-*.json, which says which report it
// holds, and contains either one page as the provider returned it or a JSON
// array of such pages. Anything else in the folder is ignored and counted.
// A whole file is refused, by name, for any of: JSON that is malformed or
// truncated, a duplicate key in one object, a field of the wrong type, a
// number that is not a plain integer where tokens go, an amount over the cap
// or with an exponent past it, a bucket that is not one whole UTC day, the
// same day twice in one file (a pagination loop saved to disk), a currency
// other than USD, or a last page that says more pages exist. Unknown fields
// are ignored: both providers add fields, and refusing them would refuse
// every export the month a field is added. What an unknown field cannot do
// is change a known one, because keys are matched EXACTLY and a duplicate is
// refused (encoding/json alone matches keys case-insensitively and lets the
// last duplicate win, so "AMOUNT" or a second "amount" would otherwise
// silently replace the real figure).
//
// WHAT A ROW BECOMES
//
// provider_usage holds one row per (connector, report, day, model, token
// type, api key, workspace or project): tokens for a usage report, exact
// micro-dollars for a cost report. A day's rows are REPLACED, never added
// to, so importing the same folder twice changes nothing; when two files
// carry the same day, the one whose name sorts last wins (costcrew-usage
// names its files by the time it fetched them, so that is the newer pull).
// provider_usage_days records which days each report actually covered, which
// is how the reconciliation tells "the provider reported zero" from "the
// provider has not been read for that day".

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// usageSpec says what one registered provider-usage connector is.
type usageSpec struct {
	id     string // connector id, provider_usage.connector
	vendor string // "anthropic" or "openai"
	// gatewayProvider is the ai_calls.provider value the gateway writes for
	// this vendor's calls (TokenFuse derives ProviderName from the model
	// name: "Anthropic", "OpenAI"), compared case-insensitively.
	gatewayProvider string
	scopeName       string // "workspace" or "project"
	scopeKey        string // the setting naming which scopes to keep
	// inputTypes are the token types that together are a request's input,
	// so the reconciliation can set the provider's input beside the
	// gateway's tokens_in without counting a subset twice.
	inputTypes []string
	// costHasKey is whether this vendor's COST report can carry an API key.
	costHasKey bool
}

var anthropicUsageSpec = usageSpec{
	id: "anthropic-usage", vendor: "anthropic", gatewayProvider: "Anthropic",
	scopeName: "workspace", scopeKey: "workspace_ids",
	inputTypes: []string{"uncached_input_tokens", "cache_creation.ephemeral_1h_input_tokens",
		"cache_creation.ephemeral_5m_input_tokens", "cache_read_input_tokens"},
	costHasKey: false,
}

var openaiUsageSpec = usageSpec{
	id: "openai-usage", vendor: "openai", gatewayProvider: "OpenAI",
	scopeName: "project", scopeKey: "project_ids",
	// input_tokens already includes the cached and cache-write tokens; the
	// cached count is kept as its own row for the page and never added in.
	inputTypes: []string{"input_tokens"},
	costHasKey: true,
}

func usageSpecFor(id string) (usageSpec, bool) {
	switch id {
	case anthropicUsageSpec.id:
		return anthropicUsageSpec, true
	case openaiUsageSpec.id:
		return openaiUsageSpec, true
	}
	return usageSpec{}, false
}

func providerUsageReader(spec usageSpec) Reader {
	return func(db *sql.DB, cfg map[string]string, opt ImportOptions) (string, error) {
		return readProviderUsage(spec, db, cfg, opt)
	}
}

const (
	usageDefaultMaxFileMB = 64
	usageMaxSettingMB     = 4096
	usageMaxFiles         = 10_000
	usageMaxPagesPerFile  = 10_000
	usageMaxJSONDepth     = 16
	usageMaxNodesPerMB    = 400_000 // JSON values per megabyte of file, a generous ceiling
	usageMaxRows          = 2_000_000
	usageMaxStringBytes   = 256
	usageMaxNumberBytes   = 64
	usageMaxExponent      = 40
	usageRefusalsShown    = 20
	usageMaxSettingIDs    = 64
	usageFirstYear        = 2000
	usageLastYear         = 2099
	// usageMaxTokens bounds one token count: ten quadrillion, far past any
	// real day and well inside int64 however many rows are summed.
	usageMaxTokens = 10_000_000_000_000_000
	// usageMaxMicros bounds one cost amount: ten million dollars in one row.
	usageMaxMicros = 10_000_000_000_000
)

// ProviderUsageSchema is the provider's half of the reconciliation. Exported
// so internal/web can make sure the tables exist before it reads them.
const ProviderUsageSchema = `
CREATE TABLE IF NOT EXISTS provider_usage(
  connector TEXT NOT NULL, report TEXT NOT NULL, day TEXT NOT NULL,
  model TEXT NOT NULL, token_type TEXT NOT NULL,
  api_key_id TEXT NOT NULL, scope TEXT NOT NULL,
  tokens INTEGER NOT NULL, cost_microusd INTEGER NOT NULL,
  PRIMARY KEY (connector, report, day, model, token_type, api_key_id, scope));
CREATE TABLE IF NOT EXISTS provider_usage_days(
  connector TEXT NOT NULL, report TEXT NOT NULL, day TEXT NOT NULL,
  source_file TEXT NOT NULL, file_sha256 TEXT NOT NULL, imported_at TEXT NOT NULL,
  PRIMARY KEY (connector, report, day));
`

// ------------------------------------------------------------------ settings

type usageConfig struct {
	path      string
	maxFile   int64
	maxFileMB int64
	keys      []string
	scopes    []string
	tolCents  int64
	tolBP     int64
	tolIsSet  bool
}

var usageIDShape = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// usageIDList reads a comma-separated list of provider ids. An id outside
// the shape the providers use is refused rather than dropped: a filter that
// silently loses one of its entries scopes the reconciliation to something
// nobody asked for.
func usageIDList(cfg map[string]string, key string) ([]string, error) {
	v := strings.TrimSpace(cfg[key])
	if v == "" {
		return nil, nil
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !usageIDShape.MatchString(p) {
			return nil, fmt.Errorf("%s has an entry %q that is not a provider id "+
				"(letters, digits, - and _, at most 128 bytes)", key, truncateForMessage(p))
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) > usageMaxSettingIDs {
		return nil, fmt.Errorf("%s names %d entries, at most %d", key, len(out), usageMaxSettingIDs)
	}
	sort.Strings(out)
	return out, nil
}

func usageWholeSetting(cfg map[string]string, key string, def, lo, hi int64) (int64, bool, error) {
	v := strings.TrimSpace(cfg[key])
	if v == "" {
		return def, false, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < lo || n > hi {
		return 0, false, fmt.Errorf("%s %q must be a whole number from %d to %d; "+
			"a setting that cannot be read is refused rather than dropped", key, truncateForMessage(v), lo, hi)
	}
	return n, true, nil
}

func parseUsageConfig(spec usageSpec, cfg map[string]string) (usageConfig, error) {
	var c usageConfig
	var err error
	c.path = strings.TrimSpace(cfg["path"])
	if c.maxFileMB, _, err = usageWholeSetting(cfg, "max_file_mb", usageDefaultMaxFileMB, 1, usageMaxSettingMB); err != nil {
		return c, err
	}
	c.maxFile = c.maxFileMB << 20
	if c.keys, err = usageIDList(cfg, "api_key_ids"); err != nil {
		return c, err
	}
	if c.scopes, err = usageIDList(cfg, spec.scopeKey); err != nil {
		return c, err
	}
	var setC, setB bool
	if c.tolCents, setC, err = usageWholeSetting(cfg, "tolerance_cents", DefaultToleranceCents, 0, 1_000_000); err != nil {
		return c, err
	}
	if c.tolBP, setB, err = usageWholeSetting(cfg, "tolerance_bp", DefaultToleranceBP, 0, 10_000); err != nil {
		return c, err
	}
	c.tolIsSet = setC || setB
	return c, nil
}

// --------------------------------------------------------------- strict JSON

// jv is one JSON value decoded without encoding/json's two silent habits:
// case-insensitive key matching and last-duplicate-wins. Numbers keep their
// literal text, so nothing passes through float64.
type jv struct {
	kind byte // 'o' object, 'a' array, 's' string, 'n' number, 'b' bool, 'z' null
	obj  map[string]*jv
	arr  []*jv
	s    string
	b    bool
}

type strictJSON struct {
	dec      *json.Decoder
	nodes    int
	maxNodes int
}

func decodeStrictJSON(r io.Reader, maxNodes int) (*jv, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	st := &strictJSON{dec: dec, maxNodes: maxNodes}
	v, err := st.value(0)
	if err != nil {
		return nil, err
	}
	if tok, err := dec.Token(); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("after the JSON value: %v", err)
		}
		return nil, fmt.Errorf("there is more after the JSON value (%v)", tok)
	}
	return v, nil
}

func truncatedJSON(err error) error {
	if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
		return errors.New("the JSON stops before it is complete (a truncated file)")
	}
	return fmt.Errorf("the JSON does not parse: %v", err)
}

func (st *strictJSON) value(depth int) (*jv, error) {
	if depth > usageMaxJSONDepth {
		return nil, fmt.Errorf("the JSON nests deeper than %d levels", usageMaxJSONDepth)
	}
	st.nodes++
	if st.nodes > st.maxNodes {
		return nil, fmt.Errorf("the JSON holds more than %d values for its size", st.maxNodes)
	}
	tok, err := st.dec.Token()
	if err != nil {
		return nil, truncatedJSON(err)
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := &jv{kind: 'o', obj: map[string]*jv{}}
			for st.dec.More() {
				kt, err := st.dec.Token()
				if err != nil {
					return nil, truncatedJSON(err)
				}
				key, ok := kt.(string)
				if !ok {
					return nil, errors.New("an object key is not a string")
				}
				if _, dup := o.obj[key]; dup {
					return nil, fmt.Errorf("the key %q appears twice in one object; "+
						"which one is the real figure cannot be decided, so neither is used", truncateForMessage(key))
				}
				v, err := st.value(depth + 1)
				if err != nil {
					return nil, err
				}
				o.obj[key] = v
			}
			if _, err := st.dec.Token(); err != nil {
				return nil, truncatedJSON(err)
			}
			return o, nil
		case '[':
			a := &jv{kind: 'a'}
			for st.dec.More() {
				v, err := st.value(depth + 1)
				if err != nil {
					return nil, err
				}
				a.arr = append(a.arr, v)
			}
			if _, err := st.dec.Token(); err != nil {
				return nil, truncatedJSON(err)
			}
			return a, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	case string:
		return &jv{kind: 's', s: t}, nil
	case json.Number:
		return &jv{kind: 'n', s: string(t)}, nil
	case bool:
		return &jv{kind: 'b', b: t}, nil
	case nil:
		return &jv{kind: 'z'}, nil
	}
	return nil, fmt.Errorf("unexpected JSON token %v", tok)
}

func kindName(k byte) string {
	switch k {
	case 'o':
		return "an object"
	case 'a':
		return "an array"
	case 's':
		return "a string"
	case 'n':
		return "a number"
	case 'b':
		return "true or false"
	case 'z':
		return "null"
	}
	return "nothing"
}

// field returns obj[name], exact match only.
func (v *jv) field(name string) (*jv, bool) {
	f, ok := v.obj[name]
	return f, ok
}

func wrongType(name string, got *jv, want string) error {
	return fmt.Errorf("%s is %s, want %s", name, kindName(got.kind), want)
}

func reqObject(o *jv, name string) (*jv, error) {
	f, ok := o.field(name)
	if !ok {
		return nil, fmt.Errorf("%s is missing", name)
	}
	if f.kind != 'o' {
		return nil, wrongType(name, f, "an object")
	}
	return f, nil
}

func reqArray(o *jv, name string) (*jv, error) {
	f, ok := o.field(name)
	if !ok {
		return nil, fmt.Errorf("%s is missing", name)
	}
	if f.kind != 'a' {
		return nil, wrongType(name, f, "an array")
	}
	return f, nil
}

func reqBool(o *jv, name string) (bool, error) {
	f, ok := o.field(name)
	if !ok {
		return false, fmt.Errorf("%s is missing", name)
	}
	if f.kind != 'b' {
		return false, wrongType(name, f, "true or false")
	}
	return f.b, nil
}

// cleanString refuses what a provider id or model name never carries and a
// page or a CSV would render as something else: control characters,
// invalid UTF-8, and an unbounded length.
func cleanString(name, s string) (string, error) {
	if len(s) > usageMaxStringBytes {
		return "", fmt.Errorf("%s is %d bytes, at most %d", name, len(s), usageMaxStringBytes)
	}
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("%s is not valid UTF-8", name)
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return "", fmt.Errorf("%s carries a control character", name)
		}
	}
	return s, nil
}

func reqString(o *jv, name string) (string, error) {
	f, ok := o.field(name)
	if !ok {
		return "", fmt.Errorf("%s is missing", name)
	}
	if f.kind != 's' {
		return "", wrongType(name, f, "a string")
	}
	return cleanString(name, f.s)
}

// optString is a field the provider leaves null when it was not grouped by.
func optString(o *jv, name string) (string, error) {
	f, ok := o.field(name)
	if !ok || f.kind == 'z' {
		return "", nil
	}
	if f.kind != 's' {
		return "", wrongType(name, f, "a string or null")
	}
	return cleanString(name, f.s)
}

var plainInteger = regexp.MustCompile(`^(0|[1-9][0-9]{0,16})$`)

func tokenCount(name string, f *jv) (int64, error) {
	if f.kind != 'n' {
		return 0, wrongType(name, f, "a whole number")
	}
	if !plainInteger.MatchString(f.s) {
		return 0, fmt.Errorf("%s %q is not a plain non-negative whole number", name, truncateForMessage(f.s))
	}
	n, err := strconv.ParseInt(f.s, 10, 64)
	if err != nil || n > usageMaxTokens {
		return 0, fmt.Errorf("%s %q is over the %d cap", name, truncateForMessage(f.s), int64(usageMaxTokens))
	}
	return n, nil
}

func reqTokens(o *jv, name string) (int64, error) {
	f, ok := o.field(name)
	if !ok {
		return 0, fmt.Errorf("%s is missing", name)
	}
	return tokenCount(name, f)
}

func optTokens(o *jv, name string) (int64, error) {
	f, ok := o.field(name)
	if !ok || f.kind == 'z' {
		return 0, nil
	}
	return tokenCount(name, f)
}

var decimalShape = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// decimalMicros reads a decimal literal exactly (math/big, never float64)
// and returns it times 10^shift, rounded half away from zero to a whole
// number. shift is 4 for Anthropic's cents and 6 for OpenAI's dollars, so
// either way the result is micro-dollars.
//
// The literal is bounded BEFORE big.Rat sees it: big.Rat.SetString on
// "1e999999999" computes ten to that power and does not come back.
func decimalMicros(name, lit string, shift int) (int64, error) {
	if len(lit) > usageMaxNumberBytes {
		return 0, fmt.Errorf("%s is %d characters long, at most %d", name, len(lit), usageMaxNumberBytes)
	}
	m := decimalShape.FindStringSubmatch(lit)
	if m == nil {
		return 0, fmt.Errorf("%s %q is not a decimal number", name, truncateForMessage(lit))
	}
	if m[3] != "" {
		e, err := strconv.Atoi(strings.TrimLeft(m[3][1:], "+"))
		if err != nil || e > usageMaxExponent || e < -usageMaxExponent {
			return 0, fmt.Errorf("%s %q has an exponent past %d", name, truncateForMessage(lit), usageMaxExponent)
		}
	}
	r, ok := new(big.Rat).SetString(lit)
	if !ok {
		return 0, fmt.Errorf("%s %q is not a decimal number", name, truncateForMessage(lit))
	}
	r.Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(shift)), nil)))
	num, den := new(big.Int).Set(r.Num()), r.Denom()
	neg := num.Sign() < 0
	num.Abs(num)
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	if new(big.Int).Mul(rem, big.NewInt(2)).Cmp(den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() || q.Int64() > usageMaxMicros {
		return 0, fmt.Errorf("%s %q is over the cap of %d micro-dollars in one row",
			name, truncateForMessage(lit), int64(usageMaxMicros))
	}
	v := q.Int64()
	if neg {
		v = -v
	}
	return v, nil
}

func truncateForMessage(s string) string {
	const max = 60
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// ------------------------------------------------------------------- pages

// usageRow is one row of the provider's report, before it reaches the store.
type usageRow struct {
	report, day, model, tokenType, apiKey, scope string
	tokens, micros                               int64
}

type usageKey struct{ report, day, model, tokenType, apiKey, scope string }

// parsedUsageFile is everything one file says, already validated.
type parsedUsageFile struct {
	report string
	days   []string // the days its buckets covered, in order
	rows   map[usageKey]*usageRow
}

func (p *parsedUsageFile) add(r usageRow) error {
	k := usageKey{r.report, r.day, r.model, r.tokenType, r.apiKey, r.scope}
	cur, ok := p.rows[k]
	if !ok {
		if len(p.rows) >= usageMaxRows {
			return fmt.Errorf("more than %d distinct rows in one file", usageMaxRows)
		}
		cp := r
		p.rows[k] = &cp
		return nil
	}
	// The same key twice in one bucket is legitimate: the report splits a
	// model's cost by context window, service tier and geography, which this
	// table does not keep. The sum is checked, never allowed to wrap.
	if (r.tokens > 0 && cur.tokens > math.MaxInt64-r.tokens) ||
		(r.micros > 0 && cur.micros > math.MaxInt64-r.micros) ||
		(r.micros < 0 && cur.micros < math.MinInt64-r.micros) {
		return errors.New("the amounts for one model and day add up past what 64 bits can hold")
	}
	cur.tokens += r.tokens
	cur.micros += r.micros
	return nil
}

// ParseProviderUsageFile reads one file of either vendor's report. Exported
// for tools/usage, which validates what it fetched with the SAME code the
// console reads it with before writing a byte of it.
func ParseProviderUsageFile(connectorID, report string, r io.Reader, size int64) (int, []string, error) {
	spec, ok := usageSpecFor(connectorID)
	if !ok {
		return 0, nil, fmt.Errorf("no such provider usage connector %q", connectorID)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, nil, err
	}
	p, err := parseUsageFile(spec, report, data, size)
	if err != nil {
		return 0, nil, err
	}
	return len(p.rows), p.days, nil
}

func parseUsageFile(spec usageSpec, report string, data []byte, size int64) (*parsedUsageFile, error) {
	// JSON is UTF-8 (RFC 8259), and encoding/json does not refuse a byte
	// that is not: it quietly turns it into U+FFFD, so a model name with a
	// broken byte in it would be stored as something nobody wrote. The whole
	// file is checked before a value is read.
	if !utf8.Valid(data) {
		return nil, errors.New("the file is not valid UTF-8, which JSON must be")
	}
	mb := size>>20 + 1
	root, err := decodeStrictJSON(bytes.NewReader(data), int(mb)*usageMaxNodesPerMB)
	if err != nil {
		return nil, err
	}
	var pages []*jv
	switch root.kind {
	case 'o':
		pages = []*jv{root}
	case 'a':
		if len(root.arr) == 0 {
			return nil, errors.New("an empty list of pages")
		}
		if len(root.arr) > usageMaxPagesPerFile {
			return nil, fmt.Errorf("%d pages, at most %d", len(root.arr), usageMaxPagesPerFile)
		}
		pages = root.arr
	default:
		return nil, fmt.Errorf("the file holds %s, want a page object or a list of pages", kindName(root.kind))
	}
	out := &parsedUsageFile{report: report, rows: map[usageKey]*usageRow{}}
	seenDay := map[string]int{}
	for i, pg := range pages {
		where := fmt.Sprintf("page %d", i+1)
		if pg.kind != 'o' {
			return nil, fmt.Errorf("%s is %s, want an object", where, kindName(pg.kind))
		}
		if spec.vendor == "openai" {
			if obj, err := reqString(pg, "object"); err != nil || obj != "page" {
				return nil, fmt.Errorf("%s: object is not \"page\" (%v)", where, err)
			}
		}
		more, err := reqBool(pg, "has_more")
		if err != nil {
			return nil, fmt.Errorf("%s: %v", where, err)
		}
		if i == len(pages)-1 && more {
			return nil, fmt.Errorf("%s is the last page here and says has_more: true, so the report "+
				"stops before the window it was asked for; save every page", where)
		}
		data, err := reqArray(pg, "data")
		if err != nil {
			return nil, fmt.Errorf("%s: %v", where, err)
		}
		for j, b := range data.arr {
			bw := fmt.Sprintf("%s bucket %d", where, j+1)
			if b.kind != 'o' {
				return nil, fmt.Errorf("%s is %s, want an object", bw, kindName(b.kind))
			}
			day, err := bucketDay(spec, b)
			if err != nil {
				return nil, fmt.Errorf("%s: %v", bw, err)
			}
			if prev, dup := seenDay[day]; dup {
				return nil, fmt.Errorf("%s repeats the day %s already read on page %d: a page "+
					"fetched twice (a pagination loop) would count that day twice, so the file is refused", bw, day, prev)
			}
			seenDay[day] = i + 1
			out.days = append(out.days, day)
			results, err := reqArray(b, "results")
			if err != nil {
				return nil, fmt.Errorf("%s: %v", bw, err)
			}
			for k, res := range results.arr {
				rw := fmt.Sprintf("%s result %d", bw, k+1)
				if res.kind != 'o' {
					return nil, fmt.Errorf("%s is %s, want an object", rw, kindName(res.kind))
				}
				rows, err := resultRows(spec, report, day, res)
				if err != nil {
					return nil, fmt.Errorf("%s: %v", rw, err)
				}
				for _, row := range rows {
					if err := out.add(row); err != nil {
						return nil, fmt.Errorf("%s: %v", rw, err)
					}
				}
			}
		}
	}
	if len(out.days) == 0 {
		return nil, errors.New("no bucket in any page, so it says nothing about any day")
	}
	return out, nil
}

// bucketDay requires a bucket to be exactly one UTC day and returns it.
func bucketDay(spec usageSpec, b *jv) (string, error) {
	var start, end time.Time
	if spec.vendor == "openai" {
		if obj, err := reqString(b, "object"); err != nil || obj != "bucket" {
			return "", fmt.Errorf("object is not \"bucket\" (%v)", err)
		}
		s, err := unixSeconds(b, "start_time")
		if err != nil {
			return "", err
		}
		e, err := unixSeconds(b, "end_time")
		if err != nil {
			return "", err
		}
		start, end = time.Unix(s, 0).UTC(), time.Unix(e, 0).UTC()
	} else {
		ss, err := reqString(b, "starting_at")
		if err != nil {
			return "", err
		}
		es, err := reqString(b, "ending_at")
		if err != nil {
			return "", err
		}
		if start, err = time.Parse(time.RFC3339, ss); err != nil {
			return "", fmt.Errorf("starting_at %q is not RFC 3339", ss)
		}
		if end, err = time.Parse(time.RFC3339, es); err != nil {
			return "", fmt.Errorf("ending_at %q is not RFC 3339", es)
		}
		if _, off := start.Zone(); off != 0 {
			return "", fmt.Errorf("starting_at %q is not UTC", ss)
		}
		start, end = start.UTC(), end.UTC()
	}
	if start.Hour() != 0 || start.Minute() != 0 || start.Second() != 0 || start.Nanosecond() != 0 {
		return "", fmt.Errorf("the bucket starts at %s, not at a UTC midnight: only daily "+
			"buckets (bucket_width 1d) are read", start.Format(time.RFC3339))
	}
	if !end.Equal(start.Add(24 * time.Hour)) {
		return "", fmt.Errorf("the bucket runs from %s to %s, not one whole day: only daily "+
			"buckets (bucket_width 1d) are read", start.Format(time.RFC3339), end.Format(time.RFC3339))
	}
	if start.Year() < usageFirstYear || start.Year() > usageLastYear {
		return "", fmt.Errorf("the bucket's year %d is outside %d to %d", start.Year(), usageFirstYear, usageLastYear)
	}
	return start.Format("2006-01-02"), nil
}

func unixSeconds(o *jv, name string) (int64, error) {
	f, ok := o.field(name)
	if !ok {
		return 0, fmt.Errorf("%s is missing", name)
	}
	if f.kind != 'n' || !plainInteger.MatchString(f.s) {
		return 0, fmt.Errorf("%s is not a whole number of seconds", name)
	}
	n, err := strconv.ParseInt(f.s, 10, 64)
	if err != nil || n > 1<<40 {
		return 0, fmt.Errorf("%s %q is out of range", name, truncateForMessage(f.s))
	}
	return n, nil
}

func resultRows(spec usageSpec, report, day string, res *jv) ([]usageRow, error) {
	switch {
	case spec.vendor == "anthropic" && report == "usage":
		return anthropicUsageRows(day, res)
	case spec.vendor == "anthropic" && report == "cost":
		return anthropicCostRows(day, res)
	case spec.vendor == "openai" && report == "usage":
		return openaiUsageRows(day, res)
	case spec.vendor == "openai" && report == "cost":
		return openaiCostRows(day, res)
	}
	return nil, fmt.Errorf("no report %q", report)
}

func anthropicUsageRows(day string, res *jv) ([]usageRow, error) {
	model, err := optString(res, "model")
	if err != nil {
		return nil, err
	}
	key, err := optString(res, "api_key_id")
	if err != nil {
		return nil, err
	}
	ws, err := optString(res, "workspace_id")
	if err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, name := range []string{"uncached_input_tokens", "cache_read_input_tokens", "output_tokens"} {
		if counts[name], err = reqTokens(res, name); err != nil {
			return nil, err
		}
	}
	if cc, ok := res.field("cache_creation"); ok && cc.kind != 'z' {
		if cc.kind != 'o' {
			return nil, wrongType("cache_creation", cc, "an object")
		}
		for _, name := range []string{"ephemeral_1h_input_tokens", "ephemeral_5m_input_tokens"} {
			n, err := optTokens(cc, name)
			if err != nil {
				return nil, fmt.Errorf("cache_creation.%v", err)
			}
			counts["cache_creation."+name] = n
		}
	}
	return tokenRows("usage", day, model, key, ws, counts), nil
}

func openaiUsageRows(day string, res *jv) ([]usageRow, error) {
	obj, err := reqString(res, "object")
	if err != nil {
		return nil, err
	}
	if obj != "organization.usage.completions.result" {
		return nil, fmt.Errorf("object %q is not a completions usage result", truncateForMessage(obj))
	}
	model, err := optString(res, "model")
	if err != nil {
		return nil, err
	}
	key, err := optString(res, "api_key_id")
	if err != nil {
		return nil, err
	}
	proj, err := optString(res, "project_id")
	if err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	if counts["input_tokens"], err = reqTokens(res, "input_tokens"); err != nil {
		return nil, err
	}
	if counts["output_tokens"], err = reqTokens(res, "output_tokens"); err != nil {
		return nil, err
	}
	if counts["input_cached_tokens"], err = optTokens(res, "input_cached_tokens"); err != nil {
		return nil, err
	}
	if counts["input_cached_tokens"] > counts["input_tokens"] {
		return nil, errors.New("input_cached_tokens is more than input_tokens, which includes it")
	}
	return tokenRows("usage", day, model, key, proj, counts), nil
}

func tokenRows(report, day, model, key, scope string, counts map[string]int64) []usageRow {
	names := make([]string, 0, len(counts))
	for n := range counts {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []usageRow
	for _, n := range names {
		if counts[n] == 0 {
			continue
		}
		out = append(out, usageRow{report: report, day: day, model: model, tokenType: n,
			apiKey: key, scope: scope, tokens: counts[n]})
	}
	return out
}

func anthropicCostRows(day string, res *jv) ([]usageRow, error) {
	cur, err := reqString(res, "currency")
	if err != nil {
		return nil, err
	}
	if cur != "USD" {
		return nil, fmt.Errorf("currency %q, this reader is USD only", truncateForMessage(cur))
	}
	f, ok := res.field("amount")
	if !ok {
		return nil, errors.New("amount is missing")
	}
	// Anthropic's amount is a STRING in cents. A number here is not what the
	// reference says, and guessing its unit is how a figure ends up a
	// hundred times wrong.
	if f.kind != 's' {
		return nil, wrongType("amount", f, "a decimal string in cents")
	}
	micros, err := decimalMicros("amount", f.s, 4)
	if err != nil {
		return nil, err
	}
	model, err := optString(res, "model")
	if err != nil {
		return nil, err
	}
	tt, err := optString(res, "token_type")
	if err != nil {
		return nil, err
	}
	ct, err := optString(res, "cost_type")
	if err != nil {
		return nil, err
	}
	ws, err := optString(res, "workspace_id")
	if err != nil {
		return nil, err
	}
	if tt == "" {
		tt = ct
	}
	if tt == "" {
		tt = "unspecified"
	}
	return []usageRow{{report: "cost", day: day, model: model, tokenType: tt, scope: ws, micros: micros}}, nil
}

func openaiCostRows(day string, res *jv) ([]usageRow, error) {
	obj, err := reqString(res, "object")
	if err != nil {
		return nil, err
	}
	if obj != "organization.costs.result" {
		return nil, fmt.Errorf("object %q is not a costs result", truncateForMessage(obj))
	}
	amt, err := reqObject(res, "amount")
	if err != nil {
		return nil, err
	}
	cur, err := reqString(amt, "currency")
	if err != nil {
		return nil, fmt.Errorf("amount.%v", err)
	}
	if !strings.EqualFold(cur, "usd") {
		return nil, fmt.Errorf("currency %q, this reader is USD only", truncateForMessage(cur))
	}
	val, ok := amt.field("value")
	if !ok {
		return nil, errors.New("amount.value is missing")
	}
	if val.kind != 'n' {
		return nil, wrongType("amount.value", val, "a number in dollars")
	}
	micros, err := decimalMicros("amount.value", val.s, 6)
	if err != nil {
		return nil, err
	}
	li, err := optString(res, "line_item")
	if err != nil {
		return nil, err
	}
	key, err := optString(res, "api_key_id")
	if err != nil {
		return nil, err
	}
	proj, err := optString(res, "project_id")
	if err != nil {
		return nil, err
	}
	model, tt := splitLineItem(li)
	return []usageRow{{report: "cost", day: day, model: model, tokenType: tt, apiKey: key,
		scope: proj, micros: micros}}, nil
}

// splitLineItem reads OpenAI's "model, token type" line item. The shape is
// the specification's own example, not a documented grammar, so a line item
// without the comma is kept whole as the model with the type "unspecified":
// it then shows on the reconciliation as a model the gateway never saw,
// which is the honest place for it.
func splitLineItem(li string) (string, string) {
	li = strings.TrimSpace(li)
	if li == "" {
		return "", "unspecified"
	}
	if i := strings.LastIndex(li, ", "); i > 0 {
		return strings.TrimSpace(li[:i]), strings.TrimSpace(li[i+2:])
	}
	return li, "unspecified"
}

// ------------------------------------------------------------------- folder

type usageFile struct {
	name, abs, report string
	size              int64
}

func usageFiles(dir string) ([]usageFile, int, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, 0, fmt.Errorf("reading %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, 0, fmt.Errorf("%s is not a folder", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, fmt.Errorf("reading %s: %w", dir, err)
	}
	var out []usageFile
	ignored := 0
	for _, e := range entries {
		name := e.Name()
		report := ""
		switch {
		case strings.HasPrefix(name, "usage-") && strings.HasSuffix(name, ".json"):
			report = "usage"
		case strings.HasPrefix(name, "cost-") && strings.HasSuffix(name, ".json"):
			report = "cost"
		}
		// Only a regular file is read: a symlink could point anywhere on
		// this machine, and a directory is not a report.
		if report == "" || !e.Type().IsRegular() {
			ignored++
			continue
		}
		fi, err := e.Info()
		if err != nil {
			ignored++
			continue
		}
		out = append(out, usageFile{name: name, abs: filepath.Join(dir, name), report: report, size: fi.Size()})
		if len(out) > usageMaxFiles {
			return nil, 0, fmt.Errorf("more than %d report files in %s", usageMaxFiles, dir)
		}
	}
	// Name order is the supersession order and invariant 7's order: the same
	// folder always reads the same way.
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, ignored, nil
}

// --------------------------------------------------------------------- read

type usageDayWinner struct {
	file   int
	sha    string
	source string
}

func readProviderUsage(spec usageSpec, db *sql.DB, cfg map[string]string, opt ImportOptions) (string, error) {
	conf, err := parseUsageConfig(spec, cfg)
	if err != nil {
		return "", err
	}
	if conf.path == "" {
		return "", errors.New("no folder is configured; set the path and save before importing")
	}
	files, ignored, err := usageFiles(conf.path)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		msg := fmt.Sprintf("no usage-*.json or cost-*.json files found in %s", conf.path)
		if ignored > 0 {
			msg += fmt.Sprintf("; %d other entr%s there, which this reader does not read", ignored, pluralY(ignored))
		}
		return "", errors.New(msg)
	}

	var refusals []string
	parsed := make([]*parsedUsageFile, len(files))
	shas := make([]string, len(files))
	seenSHA := map[string]bool{}
	duplicates := 0
	for i, f := range files {
		if f.size > conf.maxFile {
			refusals = append(refusals, fmt.Sprintf("%s: %d bytes, over the %d MB limit (max_file_mb)",
				f.name, f.size, conf.maxFileMB))
			continue
		}
		p, sha, err := readUsageFile(spec, f, conf.maxFile)
		if err != nil {
			refusals = append(refusals, fmt.Sprintf("%s: %v", f.name, err))
			continue
		}
		if seenSHA[sha] {
			duplicates++
			continue
		}
		seenSHA[sha] = true
		parsed[i], shas[i] = p, sha
	}

	// The day each report's figures come from: the last file by name that
	// carries it.
	winners := map[[2]string]usageDayWinner{}
	superseded := 0
	for i, p := range parsed {
		if p == nil {
			continue
		}
		for _, d := range p.days {
			k := [2]string{p.report, d}
			if _, had := winners[k]; had {
				superseded++
			}
			winners[k] = usageDayWinner{file: i, sha: shas[i], source: files[i].name}
		}
	}
	if len(winners) == 0 {
		return "", fmt.Errorf("nothing was read: %s", strings.Join(boundedList(refusals), "; "))
	}

	sum := usageSummary{dryRun: opt.DryRun, ignored: ignored, duplicates: duplicates,
		superseded: superseded, refusals: refusals, models: map[string]bool{}, files: map[string]int{}}
	var rows []*usageRow
	for i, p := range parsed {
		if p == nil {
			continue
		}
		sum.files[p.report]++
		for _, r := range p.rows {
			if w := winners[[2]string{r.report, r.day}]; w.file != i {
				continue
			}
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return lessUsageRow(rows[i], rows[j]) })
	for k := range winners {
		sum.addDay(k[0], k[1])
	}
	for _, r := range rows {
		if err := sum.addRow(spec, r); err != nil {
			return "", err
		}
	}
	if opt.DryRun {
		return sum.sentence(), nil
	}

	if _, err := db.Exec(ProviderUsageSchema); err != nil {
		return "", fmt.Errorf("creating provider_usage: %w", err)
	}
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	keys := make([][2]string, 0, len(winners))
	for k := range winners {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		w := winners[k]
		// A day's rows are replaced whole: that is what makes a second
		// import of the same report change nothing, and a newer pull of the
		// same day replace the older one rather than add to it.
		if _, err := tx.Exec(`DELETE FROM provider_usage WHERE connector=? AND report=? AND day=?`,
			spec.id, k[0], k[1]); err != nil {
			return "", err
		}
		if _, err := tx.Exec(`INSERT INTO provider_usage_days
			(connector, report, day, source_file, file_sha256, imported_at) VALUES (?,?,?,?,?,?)
			ON CONFLICT(connector, report, day) DO UPDATE SET source_file=excluded.source_file,
			  file_sha256=excluded.file_sha256, imported_at=excluded.imported_at`,
			spec.id, k[0], k[1], w.source, w.sha, now); err != nil {
			return "", err
		}
	}
	ins, err := tx.Prepare(`INSERT INTO provider_usage
		(connector, report, day, model, token_type, api_key_id, scope, tokens, cost_microusd)
		VALUES (?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return "", err
	}
	defer ins.Close()
	for _, r := range rows {
		if _, err := ins.Exec(spec.id, r.report, r.day, r.model, r.tokenType, r.apiKey, r.scope,
			r.tokens, r.micros); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return sum.sentence(), nil
}

func lessUsageRow(a, b *usageRow) bool {
	ka := [6]string{a.report, a.day, a.model, a.tokenType, a.apiKey, a.scope}
	kb := [6]string{b.report, b.day, b.model, b.tokenType, b.apiKey, b.scope}
	for i := range ka {
		if ka[i] != kb[i] {
			return ka[i] < kb[i]
		}
	}
	return false
}

func readUsageFile(spec usageSpec, f usageFile, maxFile int64) (*parsedUsageFile, string, error) {
	fh, err := os.Open(f.abs)
	if err != nil {
		return nil, "", err
	}
	defer fh.Close()
	// The size was checked from the directory entry; reading one byte past
	// the limit holds the same bound if the file grew between that look and
	// this read.
	data, err := io.ReadAll(io.LimitReader(fh, maxFile+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > maxFile {
		return nil, "", fmt.Errorf("it grew past the %d MB limit while it was being read", maxFile>>20)
	}
	p, err := parseUsageFile(spec, f.report, data, int64(len(data)))
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	return p, hex.EncodeToString(sum[:]), nil
}

// ------------------------------------------------------------------ sentence

type usageSummary struct {
	dryRun                          bool
	files                           map[string]int
	ignored, duplicates, superseded int
	refusals                        []string
	costDays, usageDays             []string
	costMicros                      int64
	tokensIn, tokensOut, rows       int64
	models                          map[string]bool
}

func (s *usageSummary) addDay(report, day string) {
	if report == "cost" {
		s.costDays = append(s.costDays, day)
	} else {
		s.usageDays = append(s.usageDays, day)
	}
}

func (s *usageSummary) addRow(spec usageSpec, r *usageRow) error {
	s.rows++
	if r.model != "" {
		s.models[r.model] = true
	}
	if r.report == "cost" {
		if (r.micros > 0 && s.costMicros > math.MaxInt64-r.micros) ||
			(r.micros < 0 && s.costMicros < math.MinInt64-r.micros) {
			return errors.New("the folder's cost adds up past what 64 bits can hold")
		}
		s.costMicros += r.micros
		return nil
	}
	isInput := false
	for _, t := range spec.inputTypes {
		if r.tokenType == t {
			isInput = true
		}
	}
	switch {
	case isInput && s.tokensIn <= math.MaxInt64-r.tokens:
		s.tokensIn += r.tokens
	case r.tokenType == "output_tokens" && s.tokensOut <= math.MaxInt64-r.tokens:
		s.tokensOut += r.tokens
	case isInput || r.tokenType == "output_tokens":
		return errors.New("the folder's tokens add up past what 64 bits can hold")
	}
	return nil
}

func span(days []string) string {
	if len(days) == 0 {
		return "none"
	}
	sort.Strings(days)
	return days[0] + " to " + days[len(days)-1]
}

func pluralY(n int) string {
	if n == 1 {
		return "y is"
	}
	return "ies are"
}

func boundedList(xs []string) []string {
	if len(xs) <= usageRefusalsShown {
		return xs
	}
	out := append([]string(nil), xs[:usageRefusalsShown]...)
	return append(out, fmt.Sprintf("and %d more", len(xs)-usageRefusalsShown))
}

func (s *usageSummary) sentence() string {
	verb := "Wrote"
	if s.dryRun {
		verb = "Would write"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Read %d file%s (%d usage, %d cost). ", s.files["usage"]+s.files["cost"],
		plural(s.files["usage"]+s.files["cost"]), s.files["usage"], s.files["cost"])
	fmt.Fprintf(&b, "%s %d row%s across %d model%s. Cost: %d day%s (%s), USD %s. "+
		"Usage: %d day%s (%s), %d input and %d output tokens.",
		verb, s.rows, plural(int(s.rows)), len(s.models), plural(len(s.models)),
		len(s.costDays), plural(len(s.costDays)), span(s.costDays), MicrosExact(s.costMicros),
		len(s.usageDays), plural(len(s.usageDays)), span(s.usageDays), s.tokensIn, s.tokensOut)
	if s.superseded > 0 {
		fmt.Fprintf(&b, " %d day%s carried by more than one file were taken from the file whose name sorts last.",
			s.superseded, plural(s.superseded))
	}
	if s.duplicates > 0 {
		fmt.Fprintf(&b, " %d file%s with the same bytes as another were read once.", s.duplicates, plural(s.duplicates))
	}
	if s.ignored > 0 {
		fmt.Fprintf(&b, " %d other entr%s in the folder, not read.", s.ignored, pluralY(s.ignored))
	}
	if len(s.refusals) > 0 {
		fmt.Fprintf(&b, " Refused %d file%s: %s.", len(s.refusals), plural(len(s.refusals)),
			strings.Join(boundedList(s.refusals), "; "))
	}
	return b.String()
}

// MicrosExact renders micro-dollars with all six decimals. A reconciliation
// that rounds its own figures to cents before setting them side by side
// hides exactly the sub-cent drift it exists to show.
func MicrosExact(m int64) string {
	neg := m < 0
	u := uint64(m)
	if neg {
		u = uint64(-(m + 1)) + 1
	}
	s := fmt.Sprintf("%d.%06d", u/1_000_000, u%1_000_000)
	if neg {
		return "-" + s
	}
	return s
}

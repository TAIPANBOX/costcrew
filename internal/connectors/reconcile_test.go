package connectors

// The reconciliation: the provider's cost per model per day beside the sum
// of the gateway's rows, the gap shown and never absorbed.

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gatewayCall writes one ai_calls row the way the tokenfuse-focus reader
// would have.
func gatewayCall(t *testing.T, db *sql.DB, row int, day, provider, model string, micros, in, out int64, blocked bool) {
	t.Helper()
	if err := EnsureFocusSchema(db); err != nil {
		t.Fatal(err)
	}
	b := 0
	if blocked {
		b = 1
	}
	if _, err := db.Exec(`INSERT INTO ai_calls(file_sha256, row_no, ts, day, agent, provider, model,
		tokens_in, tokens_out, billed_microusd, blocked, basis) VALUES ('t',?,?,?,'agent://x/a',?,?,?,?,?,?,'settled')`,
		row, day+"T12:00:00Z", day, provider, model, in, out, micros, b); err != nil {
		t.Fatal(err)
	}
}

func rowFor(t *testing.T, rec Reconciliation, day, model string) ReconRow {
	t.Helper()
	for _, r := range rec.Rows {
		if r.Day == day && r.Model == model {
			return r
		}
	}
	t.Fatalf("no reconciliation row for %s %q in %+v", day, model, rec.Rows)
	return ReconRow{}
}

// The fixture's provider side, 2026-10-01: haiku 1055001, sonnet 1237891,
// web search 10000000 (no model); 2026-10-02: haiku 100000.
func reconciledFixture(t *testing.T, extra map[string]string) *sql.DB {
	t.Helper()
	db := usageStore(t, "anthropic-usage", copyUsageFixture(t, anthropicUsageFixture), extra)
	if _, err := Import(db, "anthropic-usage", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestReconcileSetsTheProviderBesideTheGatewaySum(t *testing.T) {
	db := reconciledFixture(t, nil)
	// haiku on 10-01: two calls summing exactly to the provider's figure.
	gatewayCall(t, db, 1, "2026-10-01", "Anthropic", "claude-haiku-4-5", 1_000_000, 1250, 400, false)
	gatewayCall(t, db, 2, "2026-10-01", "Anthropic", "claude-haiku-4-5", 55_001, 0, 100, false)
	// sonnet on 10-01: the gateway recorded a dollar less than the provider billed.
	gatewayCall(t, db, 3, "2026-10-01", "Anthropic", "claude-sonnet-4-5", 237_891, 3000, 700, false)
	// haiku on 10-02: the gateway recorded more than the provider billed.
	gatewayCall(t, db, 4, "2026-10-02", "Anthropic", "claude-haiku-4-5", 300_000, 100, 20, false)
	// 10-03: the provider's report has not been read for it.
	gatewayCall(t, db, 5, "2026-10-03", "Anthropic", "claude-haiku-4-5", 5_000, 1, 1, false)

	rec, err := Reconcile(db, "anthropic-usage", "2026-10-01", "2026-10-03")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		day, model        string
		provider, gateway int64
		status            string
	}{
		{"2026-10-01", "claude-haiku-4-5", 1_055_001, 1_055_001, StatusMatched},
		{"2026-10-01", "claude-sonnet-4-5", 1_237_891, 237_891, StatusGatewayUnder},
		{"2026-10-01", "", 10_000_000, 0, StatusGatewayUnder},
		{"2026-10-02", "claude-haiku-4-5", 100_000, 300_000, StatusGatewayOver},
		{"2026-10-03", "claude-haiku-4-5", 0, 5_000, StatusProviderMissing},
	} {
		r := rowFor(t, rec, c.day, c.model)
		if r.ProviderMicros != c.provider || r.GatewayMicros != c.gateway || r.Status != c.status ||
			r.GapMicros != c.gateway-c.provider {
			t.Errorf("%s %q: provider %d gateway %d gap %d %q; want %d %d %d %q", c.day, c.model,
				r.ProviderMicros, r.GatewayMicros, r.GapMicros, r.Status, c.provider, c.gateway, c.gateway-c.provider, c.status)
		}
	}
	h := rowFor(t, rec, "2026-10-01", "claude-haiku-4-5")
	// 1000 uncached + 200 cache read + 50 cache creation; 500 out.
	if h.ProviderIn != 1250 || h.ProviderOut != 500 || h.GatewayIn != 1250 || h.GatewayOut != 500 {
		t.Errorf("haiku tokens in %d/%d out %d/%d, want 1250/1250 and 500/500",
			h.ProviderIn, h.GatewayIn, h.ProviderOut, h.GatewayOut)
	}
	if rec.DaysCovered != 2 || rec.DaysMissing != 1 {
		t.Errorf("days covered %d missing %d, want 2 and 1", rec.DaysCovered, rec.DaysMissing)
	}
}

// The gap over the window is the sum of every row's gap, matched rows
// included, and a model only the provider knows is its own row.
func TestTheGapIsNeverAbsorbed(t *testing.T) {
	db := reconciledFixture(t, nil)
	// Within tolerance, but not zero: the 3-micro gap must still be counted.
	gatewayCall(t, db, 1, "2026-10-01", "Anthropic", "claude-haiku-4-5", 1_054_998, 0, 0, false)
	rec, err := Reconcile(db, "anthropic-usage", "2026-10-01", "2026-10-02")
	if err != nil {
		t.Fatal(err)
	}
	var sumGap, sumP, sumG int64
	for _, r := range rec.Rows {
		sumGap += r.GapMicros
		sumP += r.ProviderMicros
		sumG += r.GatewayMicros
	}
	if rec.GapMicros != sumGap || rec.ProviderMicros != sumP || rec.GatewayMicros != sumG {
		t.Errorf("totals provider %d gateway %d gap %d do not add up to the rows' %d %d %d",
			rec.ProviderMicros, rec.GatewayMicros, rec.GapMicros, sumP, sumG, sumGap)
	}
	want := int64(1_054_998) - (1_055_001 + 1_237_891 + 10_000_000 + 100_000)
	if rec.GapMicros != want {
		t.Errorf("window gap = %d, want %d", rec.GapMicros, want)
	}
	h := rowFor(t, rec, "2026-10-01", "claude-haiku-4-5")
	if h.Status != StatusMatched || h.GapMicros != -3 {
		t.Errorf("haiku: %q gap %d, want matched with its -3 micro gap still shown", h.Status, h.GapMicros)
	}
	if len(rec.Rows) != 4 {
		t.Errorf("%d rows, want 4: haiku and sonnet and the modelless web search on 10-01, haiku on 10-02", len(rec.Rows))
	}
}

func TestReconcileToleranceBoundary(t *testing.T) {
	// Default: the larger of 1 cent and 0.5% of the provider's figure.
	for _, c := range []struct {
		provider, gap int64
		want          string
	}{
		{1_000_000, 10_000, StatusMatched},     // 1 cent of $1: 0.5% is 5000, the cent is larger
		{1_000_000, 10_001, StatusGatewayOver}, // one micro past it
		{1_000_000, -10_001, StatusGatewayUnder},
		{100_000_000, 500_000, StatusMatched}, // 0.5% of $100 is 50 cents
		{100_000_000, 500_001, StatusGatewayOver},
		{0, 10_000, StatusMatched},
		{0, 10_001, StatusGatewayOver},
		{-1_000_000, 0, StatusMatched}, // a credit is compared as its size
	} {
		if got := reconStatus(true, c.provider, c.gap, DefaultToleranceCents, DefaultToleranceBP); got != c.want {
			t.Errorf("provider %d gap %d: %q, want %q", c.provider, c.gap, got, c.want)
		}
	}
	if got := reconStatus(false, 5, 0, 1, 50); got != StatusProviderMissing {
		t.Errorf("an uncovered day with a zero gap is %q, want provider missing", got)
	}
	if got := reconStatus(true, 1_000_000, 1, 0, 0); got != StatusGatewayOver {
		t.Errorf("a zero tolerance let a one-micro gap match: %q", got)
	}
	// The connector's own settings move it.
	db := reconciledFixture(t, map[string]string{"tolerance_cents": "0", "tolerance_bp": "0"})
	gatewayCall(t, db, 1, "2026-10-01", "Anthropic", "claude-haiku-4-5", 1_055_000, 0, 0, false)
	rec, err := Reconcile(db, "anthropic-usage", "2026-10-01", "2026-10-01")
	if err != nil {
		t.Fatal(err)
	}
	if r := rowFor(t, rec, "2026-10-01", "claude-haiku-4-5"); r.Status != StatusGatewayUnder {
		t.Errorf("with no tolerance, a one-micro shortfall is %q, want gateway under", r.Status)
	}
}

// A covered day the provider billed nothing for, and a day never read, are
// two different statuses.
func TestAProviderZeroIsNotProviderMissing(t *testing.T) {
	dir := t.TempDir()
	empty := `{"data":[{"starting_at":"2026-10-07T00:00:00Z","ending_at":"2026-10-08T00:00:00Z","results":[]}],"has_more":false,"next_page":null}`
	if err := os.WriteFile(filepath.Join(dir, "cost-a.json"), []byte(empty), 0o600); err != nil {
		t.Fatal(err)
	}
	db := usageStore(t, "anthropic-usage", dir, nil)
	if _, err := Import(db, "anthropic-usage", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	gatewayCall(t, db, 1, "2026-10-07", "Anthropic", "claude-haiku-4-5", 50_000, 0, 0, false)
	gatewayCall(t, db, 2, "2026-10-08", "Anthropic", "claude-haiku-4-5", 50_000, 0, 0, false)
	rec, err := Reconcile(db, "anthropic-usage", "2026-10-07", "2026-10-08")
	if err != nil {
		t.Fatal(err)
	}
	if r := rowFor(t, rec, "2026-10-07", "claude-haiku-4-5"); r.Status != StatusGatewayOver || !r.Covered {
		t.Errorf("a day the provider reported as empty: %q covered=%v, want gateway over", r.Status, r.Covered)
	}
	if r := rowFor(t, rec, "2026-10-08", "claude-haiku-4-5"); r.Status != StatusProviderMissing || r.Covered {
		t.Errorf("a day never read: %q covered=%v, want provider missing", r.Status, r.Covered)
	}
}

func TestReconcileReadsOnlyThisProvidersUnblockedCalls(t *testing.T) {
	db := reconciledFixture(t, nil)
	gatewayCall(t, db, 1, "2026-10-02", "Anthropic", "claude-haiku-4-5", 100_000, 0, 0, false)
	gatewayCall(t, db, 2, "2026-10-02", "anthropic", "claude-haiku-4-5", 1, 0, 0, false) // case does not matter
	gatewayCall(t, db, 3, "2026-10-02", "Anthropic", "claude-haiku-4-5", 999_999, 0, 0, true)
	gatewayCall(t, db, 4, "2026-10-02", "OpenAI", "claude-haiku-4-5", 777_777, 0, 0, false)
	rec, err := Reconcile(db, "anthropic-usage", "2026-10-02", "2026-10-02")
	if err != nil {
		t.Fatal(err)
	}
	if r := rowFor(t, rec, "2026-10-02", "claude-haiku-4-5"); r.GatewayMicros != 100_001 {
		t.Errorf("gateway = %d, want 100001: blocked calls and another provider's rows excluded", r.GatewayMicros)
	}
}

func TestReconcileScopesByKeyAndWorkspace(t *testing.T) {
	// Anthropic: the key filter narrows tokens and never the money, which
	// carries no key; the workspace filter narrows both.
	db := reconciledFixture(t, map[string]string{"api_key_ids": "apikey_B"})
	rec, err := Reconcile(db, "anthropic-usage", "2026-10-01", "2026-10-01")
	if err != nil {
		t.Fatal(err)
	}
	h := rowFor(t, rec, "2026-10-01", "claude-haiku-4-5")
	if h.ProviderMicros != 1_055_001 || h.ProviderIn != 0 {
		t.Errorf("haiku with key apikey_B kept: cost %d tokens in %d, want the cost kept (no key on it) and the tokens dropped",
			h.ProviderMicros, h.ProviderIn)
	}
	if !strings.Contains(rec.Scope, "tokens only") {
		t.Errorf("the scope sentence does not say the key filter narrows tokens only: %q", rec.Scope)
	}
	db = reconciledFixture(t, map[string]string{"workspace_ids": "wrkspc_1"})
	rec, err = Reconcile(db, "anthropic-usage", "2026-10-01", "2026-10-01")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rec.Rows {
		if r.Model != "claude-sonnet-4-5" && r.ProviderMicros != 0 {
			t.Errorf("workspace wrkspc_1 kept %d micros of %q, which is in the default workspace", r.ProviderMicros, r.Model)
		}
	}
	// OpenAI: the cost report carries a key, so the key filter narrows money.
	odb := usageStore(t, "openai-usage", copyUsageFixture(t, openaiUsageFixture), map[string]string{"api_key_ids": "key_Z"})
	if _, err := Import(odb, "openai-usage", false, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	orec, err := Reconcile(odb, "openai-usage", "2026-10-01", "2026-10-01")
	if err != nil {
		t.Fatal(err)
	}
	if orec.ProviderMicros != 0 {
		t.Errorf("OpenAI with only key_Z kept %d micros of key_A's cost", orec.ProviderMicros)
	}
}

func TestReconcileOnAFreshStoreIsEmptyAndCreatesNothing(t *testing.T) {
	db := openTestStore(t).DB()
	rec, err := Reconcile(db, "anthropic-usage", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Empty || len(rec.Rows) != 0 {
		t.Errorf("a fresh store reconciles to %+v, want empty", rec)
	}
	for _, tb := range []string{"provider_usage", "provider_usage_days", "ai_calls"} {
		if ok, _ := tableExists(db, tb); ok {
			t.Errorf("reconciling created %s: a read must not write", tb)
		}
	}
}

func TestReconcileDefaultWindowEndsOnTheLatestDay(t *testing.T) {
	db := reconciledFixture(t, nil)
	rec, err := Reconcile(db, "anthropic-usage", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if rec.To != "2026-10-02" || rec.From != "2026-09-02" {
		t.Errorf("default window %s to %s, want 2026-09-02 to 2026-10-02", rec.From, rec.To)
	}
}

func TestReconcileRefusesABadWindow(t *testing.T) {
	db := openTestStore(t).DB()
	for _, w := range [][2]string{{"2026-13-01", "2026-10-01"}, {"yesterday", ""}, {"2026-10-02", "2026-10-01"},
		{"2024-01-01", "2026-01-01"}, {"2026-10-01'; DROP TABLE x;--", ""}} {
		if _, err := Reconcile(db, "anthropic-usage", w[0], w[1]); err == nil {
			t.Errorf("window %q to %q was accepted", w[0], w[1])
		}
	}
	if _, err := Reconcile(db, "openrouter-usage", "", ""); err == nil {
		t.Error("a connector with no reconciliation was reconciled")
	}
}

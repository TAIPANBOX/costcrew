package finops_test

// Invariant 103: a call the gateway priced at its fallback rate carries the
// gateway's estimate, not a bill. The fixture is
// internal/connectors/testdata/tokenfuse-focus-price-basis-2026-10-14.csv:
// book-priced Haiku (0.01) and Opus (0.04) and a fallback-priced
// gpt-4.1-mini (0.00036) in run-p1, a fallback-priced gpt-4.1-mini (0.002)
// and a blocked call in run-p2, and on 2026-10-15 a book-priced Haiku
// (0.005) and an admitted Haiku whose basis is empty (0.001) in run-p3.

import (
	"reflect"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

const exportPriceBasis = "tokenfuse-focus-price-basis-2026-10-14.csv"

func TestTheMonthIsSplitByPriceBasis(t *testing.T) {
	db := bareDB(t)
	importExports(t, db, exportPriceBasis)
	p, err := finops.PriceBases(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	got := []int64{int64(p.BookCalls), int64(p.BookCost), int64(p.FallbackCalls), int64(p.FallbackCost),
		int64(p.UnreportedCalls), int64(p.UnstatedCalls), int64(p.UnstatedCost)}
	if want := []int64{3, 55_000, 2, 2_360, 0, 1, 1_000}; !reflect.DeepEqual(got, want) {
		t.Errorf("book calls/cost, fallback calls/cost, unreported, unstated calls/cost:\n got %v\nwant %v", got, want)
	}
	if want := []finops.BasisGroup{{Basis: "fallback", Model: "openai/gpt-4.1-mini", Calls: 2, Cost: 2_360}}; !reflect.DeepEqual(p.Fallback, want) {
		t.Errorf("fallback by model: %+v", p.Fallback)
	}

	older := bareDB(t)
	importExports(t, older, export180)
	q, err := finops.PriceBases(older, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if q.UnreportedCalls != 4 || q.BookCalls+q.FallbackCalls+q.UnstatedCalls != 0 {
		t.Errorf("an export without x_price_basis: %+v, want its 4 admitted calls unreported", q)
	}
}

func TestAFallbackEstimateIsNeverInAnAgentsOrAModelsCost(t *testing.T) {
	db := bareDB(t)
	importExports(t, db, exportPriceBasis)
	agents, err := finops.AIByAgent(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Fatalf("agents: %+v", agents)
	}
	a := agents[0]
	if a.Cost != money.Micros(56_000) || a.FallbackCalls != 2 || a.FallbackCost != money.Micros(2_360) {
		t.Errorf("the agent's cost %s with %d fallback calls (%s); want 0.056 and 2 calls of 0.00236 apart",
			a.Cost, a.FallbackCalls, a.FallbackCost)
	}
	models, err := finops.AIByModel(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range models {
		if m.Model == "openai/gpt-4.1-mini" && (m.Cost != 0 || m.FallbackCost != money.Micros(2_360)) {
			t.Errorf("gpt-4.1-mini costs %s with %s fallback; want 0 and 0.00236 apart", m.Cost, m.FallbackCost)
		}
	}
	doors, err := finops.AIDoors(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if len(doors) != 1 || doors[0].Cost != money.Micros(56_000) {
		t.Errorf("the month by door: %+v, want 0.056 of spend", doors)
	}
}

func TestARunsFallbackEstimateIsKeptOutOfItsSpendAndItsShares(t *testing.T) {
	db := bareDB(t)
	importExports(t, db, exportPriceBasis)
	runs, err := finops.AIRuns(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]finops.RunRow{}
	for _, r := range runs {
		by[r.RunID] = r
	}
	p1 := by["run-p1"]
	if p1.Cost != money.Micros(50_000) || p1.FallbackCalls != 1 || p1.FallbackCost != money.Micros(360) {
		t.Errorf("run-p1: spend %s, %d fallback (%s); want 0.05 and one call of 0.00036 apart", p1.Cost, p1.FallbackCalls, p1.FallbackCost)
	}
	var shares []string
	for _, m := range p1.Models {
		shares = append(shares, m.Model+" "+m.Share())
	}
	if want := []string{"claude-haiku-5-5 20%", "claude-opus-5-5 80%", "openai/gpt-4.1-mini fallback estimate only"}; !reflect.DeepEqual(shares, want) {
		t.Errorf("run-p1's shares: %q, want %q (an estimate takes no share of spend)", shares, want)
	}
	if p2 := by["run-p2"]; p2.Cost != 0 || p2.FallbackCost != money.Micros(2_000) || p2.Models[0].Share() != "fallback estimate only" {
		t.Errorf("run-p2 spent only at the fallback rate: %+v", p2)
	}
}

func TestAMonthOfOnlyFallbackPricedCallsIsStillAMonthOfRealCalls(t *testing.T) {
	db := bareDB(t)
	importExports(t, db, exportPriceBasis)
	if _, err := db.Exec(`DELETE FROM ai_calls WHERE COALESCE(price_basis,'') <> 'fallback'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM charges WHERE provenance='tokenfuse-focus'`); err != nil {
		t.Fatal(err)
	}
	month, ok, err := finops.LatestRealAIMonth(db)
	if err != nil || !ok || month != "2026-10" {
		t.Errorf("a month of fallback-priced calls only: %q %v %v; want 2026-10", month, ok, err)
	}
}

func TestPersonalDataLetThroughIsCountedByKindAgentAndRun(t *testing.T) {
	db := bareDB(t)
	importExports(t, db, exportPriceBasis)
	p, err := finops.PIIFindings(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if p.Calls != 2 || finops.KindsText(p.Kinds) != "3 pii_email, 1 pii_card" || p.Reported != 6 || p.Unreported != 0 {
		t.Errorf("the month's findings: %d calls, %q, reported %d, unreported %d; want 2, \"3 pii_email, 1 pii_card\", 6, 0",
			p.Calls, finops.KindsText(p.Kinds), p.Reported, p.Unreported)
	}
	if len(p.ByAgent) != 1 || p.ByAgent[0].Calls != 2 || finops.KindsText(p.ByAgent[0].Kinds) != "3 pii_email, 1 pii_card" {
		t.Errorf("by agent: %+v", p.ByAgent)
	}
	runs, err := finops.AIRuns(db, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.RunID == "run-p1" && (r.PIICalls != 2 || finops.KindsText(r.PII) != "3 pii_email, 1 pii_card") {
			t.Errorf("run-p1's findings: %d calls, %q", r.PIICalls, finops.KindsText(r.PII))
		}
		if r.RunID != "run-p1" && r.PIICalls != 0 {
			t.Errorf("run %s carries findings it never had", r.RunID)
		}
	}

	older := bareDB(t)
	importExports(t, older, export180)
	q, err := finops.PIIFindings(older, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if q.Calls != 0 || q.Reported != 0 || q.Unreported != 4 {
		t.Errorf("an export without x_pii_findings: %+v, want 4 admitted calls unreported", q)
	}
	db.Close()
	if _, err := finops.PIIFindings(db, "2026-10"); err == nil {
		t.Error("PIIFindings read a closed store without an error")
	}
	if _, err := finops.PriceBases(db, "2026-10"); err == nil {
		t.Error("PriceBases read a closed store without an error")
	}
}

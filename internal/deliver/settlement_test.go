package deliver

// costcrew#67. White-box (package deliver, not deliver_test) because D3
// calls callAnthropic directly, which is unexported.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// D1: the parser reads all three headers, and an unrecognised x-fuse-price
// word is informational only -- it never un-settles a valid cost header.
func TestParseSettlementReadsTheGatewaysOwnThreeHeaders(t *testing.T) {
	h := http.Header{}
	h.Set(HeaderFuseCostUSD, "0.058110")
	// Deliberately NOT equal to the cost, so a parser reading the wrong
	// header (M3) is visible here too, not only in a multi-task test.
	h.Set(HeaderFuseSpentUSD, "0.100000")
	h.Set(HeaderFusePrice, "fallback")

	got := ParseSettlement(h)
	want := Settlement{Settled: true, SettledMicros: 58110,
		RunSpentKnown: true, RunSpentMicros: 100000, PriceBasis: "fallback"}
	if got != want {
		t.Errorf("ParseSettlement(the issue's own fixture) = %+v, want %+v", got, want)
	}

	h.Set(HeaderFusePrice, "known")
	if got := ParseSettlement(h).PriceBasis; got != "known" {
		t.Errorf("PriceBasis with x-fuse-price=known = %q, want \"known\"", got)
	}

	h.Set(HeaderFusePrice, "exact")
	got2 := ParseSettlement(h)
	if got2.PriceBasis != "" {
		t.Errorf("PriceBasis with a word this file does not know = %q, want empty", got2.PriceBasis)
	}
	if !got2.Settled {
		t.Error("an unrecognised x-fuse-price word must not un-settle an otherwise valid x-fuse-cost-usd")
	}
}

// D2: every hostile shape the header can carry is ABSENT, never a charge and
// never a panic, through both header names; the settled rows on the other
// side of the same table are exact.
func TestHostileSettlementHeadersNeverPanicAndNeverBecomeACharge(t *testing.T) {
	absent := []string{
		"", "   ", "abc", "NaN", "Inf", "inf", "0x10",
		"-1", "-0.000001", "+0.058110", "1e300", "1,234.50",
		"1.2.3", ".", "1000000.000001", "1000001",
		// Exactly 32 bytes (the length cap), all digits, no dot: passes the
		// shape check but overflows int64 inside money.ParseMicros itself,
		// which is the one branch of parseFuseUSD nothing else here reaches.
		"99999999999999999999999999999999",
	}
	for _, v := range absent {
		v := v
		t.Run("cost/"+v, func(t *testing.T) {
			h := http.Header{}
			h.Set(HeaderFuseCostUSD, v)
			s := ParseSettlement(h)
			if s.Settled || s.SettledMicros != 0 {
				t.Errorf("x-fuse-cost-usd %q became a charge of %d micros; a value the "+
					"runner cannot read must be absent, never a charge", v, s.SettledMicros)
			}
		})
		t.Run("spent/"+v, func(t *testing.T) {
			h := http.Header{}
			h.Set(HeaderFuseSpentUSD, v)
			s := ParseSettlement(h)
			if s.RunSpentKnown {
				t.Errorf("x-fuse-spent-usd %q became a charge of %d micros; a value the "+
					"runner cannot read must be absent, never a charge", v, s.RunSpentMicros)
			}
		})
	}

	t.Run("duplicated header", func(t *testing.T) {
		h := http.Header{}
		h.Add(HeaderFuseCostUSD, "0.058110")
		h.Add(HeaderFuseCostUSD, "0.058110")
		h.Add(HeaderFuseSpentUSD, "0.058110")
		h.Add(HeaderFuseSpentUSD, "0.058110")
		s := ParseSettlement(h)
		if s.Settled || s.SettledMicros != 0 {
			t.Errorf("a duplicated x-fuse-cost-usd became a charge of %d micros; a value "+
				"the runner cannot read must be absent, never a charge", s.SettledMicros)
		}
		if s.RunSpentKnown {
			t.Errorf("a duplicated x-fuse-spent-usd became a charge of %d micros", s.RunSpentMicros)
		}
	})

	t.Run("a megabyte long", func(t *testing.T) {
		h := http.Header{}
		huge := strings.Repeat("9", 1<<20)
		h.Set(HeaderFuseCostUSD, huge)
		s := ParseSettlement(h)
		if s.Settled {
			t.Errorf("x-fuse-cost-usd %d bytes long became a charge of %d micros; longer "+
				"than 32 bytes must be refused unread", len(huge), s.SettledMicros)
		}
	})

	settled := []struct {
		raw    string
		micros int64
	}{
		{"0.058110", 58110},
		{"0.000000", 0},
		{"0", 0},
		{"1000000.000000", MaxSettlementMicros},
		{"5.", 5_000_000},
		{".5", 500_000},
	}
	for _, c := range settled {
		c := c
		t.Run("settled/"+c.raw, func(t *testing.T) {
			h := http.Header{}
			h.Set(HeaderFuseCostUSD, c.raw)
			s := ParseSettlement(h)
			if !s.Settled || s.SettledMicros != c.micros {
				t.Errorf("x-fuse-cost-usd %q parsed as Settled=%v SettledMicros=%d, want "+
					"Settled=true SettledMicros=%d", c.raw, s.Settled, s.SettledMicros, c.micros)
			}
		})
	}
	// The whole test finishing without a panic is itself the no-panic proof.
}

// D3: callAnthropic itself carries the gateway's settlement on its Result,
// and ActualMicros stays 0 (Call never prices its own call, before or after
// this change).
func TestCallAnthropicCarriesTheGatewaysSettlementOnItsResult(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-fuse-cost-usd", "0.058110")
		w.Header().Set("x-fuse-spent-usd", "0.058110")
		w.Header().Set("x-fuse-price", "fallback")
		fmt.Fprint(w, `{"content":[{"type":"text","text":"the deliverable"}],`+
			`"stop_reason":"end_turn","usage":{"input_tokens":2874,"output_tokens":200}}`)
	}))
	defer srv.Close()

	gw := Gateway{URL: srv.URL, RunID: "crew-1", AgentID: "agent://x/y.mercer", BudgetUSD: "1.00"}
	res, err := callAnthropic(context.Background(), "claude-x", "hello", 100, gw)
	if err != nil {
		t.Fatalf("callAnthropic: %v", err)
	}
	if !res.Settled || res.SettledMicros != 58110 {
		t.Errorf("Settled=%v SettledMicros=%d, want true/58110", res.Settled, res.SettledMicros)
	}
	if res.PriceBasis != "fallback" {
		t.Errorf("PriceBasis = %q, want \"fallback\"", res.PriceBasis)
	}
	if got := res.ChargeMicros(); got != 58110 {
		t.Errorf("ChargeMicros() = %d, want 58110", got)
	}
	if res.ActualMicros != 0 {
		t.Errorf("ActualMicros = %d, want 0: Call never prices its own call", res.ActualMicros)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"content":[{"type":"text","text":"the deliverable"}],`+
			`"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	defer srv2.Close()
	gw2 := Gateway{URL: srv2.URL, RunID: "crew-1", AgentID: "agent://x/y.mercer", BudgetUSD: "1.00"}
	res2, err := callAnthropic(context.Background(), "claude-x", "hello", 100, gw2)
	if err != nil {
		t.Fatalf("callAnthropic: %v", err)
	}
	if res2.Settled {
		t.Error("Settled = true with no settlement header sent at all")
	}
	if got := Charge(res2.Settlement, 777); got != 777 {
		t.Errorf("Charge(unsettled, 777) = %d, want 777", got)
	}
}

// D4: a task is settled only when every one of its rounds was.
func TestAddRoundSettlesATaskOnlyWhenEveryRoundWas(t *testing.T) {
	a := Settlement{Settled: true, SettledMicros: 100, RunSpentKnown: true, RunSpentMicros: 100, PriceBasis: "known"}
	b := Settlement{Settled: true, SettledMicros: 250, RunSpentKnown: true, RunSpentMicros: 350, PriceBasis: "known"}

	if got, want := a.AddRound(b), (Settlement{Settled: true, SettledMicros: 350,
		RunSpentKnown: true, RunSpentMicros: 350, PriceBasis: "known"}); got != want {
		t.Errorf("a.AddRound(b) = %+v, want %+v", got, want)
	}

	if got := a.AddRound(Settlement{}); got.Settled || got.SettledMicros != 0 ||
		!got.RunSpentKnown || got.RunSpentMicros != 100 || got.PriceBasis != "" {
		t.Errorf("a.AddRound(an unsettled round) = %+v, want unsettled, RunSpentKnown true "+
			"at 100, no price basis: a sum that mixes the gateway's figure for some rounds "+
			"with the runner's own for others is a third kind of number under one heading", got)
	}

	if got := a.AddRound(Settlement{Settled: true, SettledMicros: 1, PriceBasis: "fallback"}); got.PriceBasis != "fallback" || got.RunSpentMicros != 100 {
		t.Errorf("a.AddRound(a fallback round with no spend figure) = %+v, want PriceBasis "+
			"\"fallback\" and RunSpentMicros 100 (the largest seen so far)", got)
	}

	if got, want := (Settlement{}).AddRound(Settlement{}), (Settlement{}); got != want {
		t.Errorf("Settlement{}.AddRound(Settlement{}) = %+v, want the zero value", got)
	}
}

// D5: Charge is the settlement when settled, the caller's own priced figure
// otherwise; a settlement of zero (a cache hit) is still a settlement.
func TestChargeIsTheSettlementWhenSettledAndTheCallersOwnPriceOtherwise(t *testing.T) {
	if got := Charge(Settlement{Settled: true, SettledMicros: 58110}, 11622); got != 58110 {
		t.Errorf("Charge(settled 58110, priced 11622) = %d, want 58110", got)
	}
	if got := Charge(Settlement{}, 11622); got != 11622 {
		t.Errorf("Charge(unsettled, priced 11622) = %d, want 11622", got)
	}
	if got := Charge(Settlement{Settled: true, SettledMicros: 0}, 11622); got != 0 {
		t.Errorf("Charge(settled at 0, priced 11622) = %d, want 0: a cache hit is a "+
			"settlement of zero", got)
	}
	if got := (Result{ActualMicros: 5}).ChargeMicros(); got != 5 {
		t.Errorf("Result{ActualMicros: 5}.ChargeMicros() = %d, want 5", got)
	}
	if got := (Result{ActualMicros: 5, Settlement: Settlement{Settled: true, SettledMicros: 9}}).ChargeMicros(); got != 9 {
		t.Errorf("a settled Result{ActualMicros: 5, ...settled at 9}.ChargeMicros() = %d, want 9", got)
	}
}

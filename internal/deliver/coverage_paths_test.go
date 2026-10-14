package deliver

// Paths of the one call door and its helpers that no earlier test walked: the
// header that bounds a call, and what each engine's
// call says before it sends anything when it cannot be made. Each test
// asserts the figure or the sentence, so a path that changes it goes red.

import (
	"context"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

func TestTheGatewayBudgetIsTheTighterOfTheRunAndTheTask(t *testing.T) {
	for _, c := range []struct {
		run, guard money.Cents
		want       string
	}{
		{500, 0, "5.00"},   // no guard: the run's ceiling
		{500, 200, "2.00"}, // a tighter guard wins
		{500, 700, "5.00"}, // a wider guard never loosens the run
		{500, 500, "5.00"},
	} {
		if got := GatewayBudgetUSD(c.run, c.guard); got != c.want {
			t.Errorf("GatewayBudgetUSD(%d, %d) = %q, want %q", c.run, c.guard, got, c.want)
		}
	}
}

func TestACallThatCannotBeMadeSaysWhyBeforeSendingAnything(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	ctx := context.Background()
	for engine, want := range map[string]string{
		"anthropic":  "ANTHROPIC_API_KEY is not set in this process",
		"openrouter": "OPENROUTER_API_KEY is not set in this process",
		"claude-cli": `no caller is written for engine "claude-cli"`,
	} {
		if _, err := Call(ctx, engine, "some-model", "hello", 10, Gateway{}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", engine, err, want)
		}
	}
	if _, err := Call(ctx, engines.LocalID, " ", "hello", 10, Gateway{}); err == nil ||
		!strings.Contains(err.Error(), "the local engine has no model name") {
		t.Errorf("the local engine with no model name: %v", err)
	}
	// An engine this door has no caller for is not routed anywhere, with or
	// without a gateway: Call's own switch refuses it by name.
	if url, err := (Gateway{URL: "http://gw.example"}).RouteFor("claude-cli"); url != "" || err != nil {
		t.Errorf("RouteFor an engine with no caller: %q %v", url, err)
	}
}

func TestAnAgentIDThatCannotRideInTheHeaderIsRefused(t *testing.T) {
	_, err := OnBehalfOfChain("costcrew.example", "owner", "two words")
	if err == nil || !strings.Contains(err.Error(), "cannot ride in x-fuse-on-behalf-of") {
		t.Errorf("an analyst whose id holds a space: %v", err)
	}
	chain, err := OnBehalfOfChain("costcrew.example", "owner", "triage")
	if err != nil || len(chain) != 2 || chain[0] != "user://costcrew.example/owner" || chain[1] != "agent://costcrew.example/triage" {
		t.Errorf("a plain analyst: %v %v", chain, err)
	}
}

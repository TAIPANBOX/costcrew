package deliver

// B6B-SPEC.md: call() and everything it needs to spend correctly moved here
// from tools/run/live.go, as one exported Call over one exported Gateway
// type (renamed, once, from tools/run's own gatewayHeaders -- the shape
// that actually reached one call, not gatewayConfig, which stays the
// run-wide setup tools/run's own execute() builds a Gateway from). These two
// tests are the ones that test the moved anthropicRequest directly and so
// cannot stay behind a wrapper the way the rest of gateway_test.go's tests
// do (tools/run/gateway_test.go keeps its other nine, unchanged, because
// gatewayHeaders is now a type alias of Gateway and every other name they
// use -- execute, runBudget, estimate, normalizeGateway, directCallsNotice,
// gatewayBudgetUSD, gatewayEnvDefault -- still resolves locally there). The
// only edit either test needed is the type name itself, gatewayHeaders ->
// Gateway, which is what "one gateway type" replacing two actually means.
//
// Red first, against the unchanged tree: neither Gateway nor
// anthropicRequest exists yet in this package, so this fails to compile.

import (
	"context"
	"testing"
)

// With no gateway, nothing changes: the request is built for the real
// upstream host. Asserted on the request the client would send, never by
// actually sending it, so this test spends nothing and reaches nothing.
func TestWithNoGatewayTheRequestGoesToAnthropicDirectly(t *testing.T) {
	req, err := anthropicRequest(context.Background(), "a-key", "claude-x", "hello", 100, Gateway{})
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != "https://api.anthropic.com/v1/messages" {
		t.Errorf("URL %q, want the direct Anthropic endpoint unchanged", got)
	}
	for _, h := range []string{"x-fuse-run-id", "x-fuse-agent-id", "x-fuse-budget-usd", "x-fuse-outcome"} {
		if v := req.Header.Get(h); v != "" {
			t.Errorf("with no gateway configured, header %s carries %q; it must not be set at all", h, v)
		}
	}
}

// Every gateway request carries all three required headers, never
// x-fuse-outcome (not built yet, and said so in a comment rather than sent as
// an empty lie), and x-fuse-parent-run-id only when the caller actually gave
// one: a runner with no notion of a parent run must not invent one.
func TestAGatewayRequestCarriesTheThreeHeadersAndNeverInventsAParent(t *testing.T) {
	gw := Gateway{
		URL: "http://127.0.0.1:1", RunID: "crew-9", AgentID: "agent://x/y.mercer", BudgetUSD: "1.00",
	}
	req, err := anthropicRequest(context.Background(), "a-key", "claude-x", "hello", 100, gw)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != "http://127.0.0.1:1/v1/messages" {
		t.Errorf("URL %q, want the gateway's own /v1/messages", got)
	}
	for h, want := range map[string]string{
		"x-fuse-run-id":     "crew-9",
		"x-fuse-agent-id":   "agent://x/y.mercer",
		"x-fuse-budget-usd": "1.00",
	} {
		if got := req.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	if v := req.Header.Get("x-fuse-outcome"); v != "" {
		t.Errorf("x-fuse-outcome carries %q; this step never sets it", v)
	}
	if v := req.Header.Get("x-fuse-parent-run-id"); v != "" {
		t.Errorf("x-fuse-parent-run-id carries %q with no parent given; a "+
			"parent must never be invented", v)
	}

	gw.ParentRunID = "crew-8"
	req2, err := anthropicRequest(context.Background(), "a-key", "claude-x", "hello", 100, gw)
	if err != nil {
		t.Fatal(err)
	}
	if got := req2.Header.Get("x-fuse-parent-run-id"); got != "crew-8" {
		t.Errorf("x-fuse-parent-run-id %q, want crew-8 once the caller actually gave one", got)
	}
}

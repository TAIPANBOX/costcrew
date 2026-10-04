package deliver

// The OpenAI-shaped gateway, and the end of the silent direct call.
//
// Call routed openrouter and bedrock to their own hosts and ignored its
// Gateway argument entirely, whatever -gateway said, on the ground (written
// in its own comment) that TokenFuse speaks nothing OpenAI-shaped. TokenFuse
// has served POST /v1/chat/completions since 2026-09-07 (tokenfuse
// docs/26-the-openai-door.md), so that ground was gone, and a run pointed at
// a metering gateway kept spending outside it with nothing said.
//
// Nothing here makes a network call: the one test that has to observe where
// a call goes replaces http.DefaultTransport with a recorder that answers
// from memory, so a call that would have reached openrouter.ai is SEEN
// reaching it and never sent.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingTransport stands in for the network. It records the host of every
// request it is handed and answers a canned OpenRouter-shaped 200, with
// whatever extra response headers the test wants, so a "went direct" call
// completes instead of failing for an unrelated reason.
type recordingTransport struct {
	mu      sync.Mutex
	hosts   []string
	headers http.Header
}

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.hosts = append(rt.hosts, r.URL.Host)
	rt.mu.Unlock()
	h := http.Header{"Content-Type": {"application/json"}}
	for k, v := range rt.headers {
		h[k] = v
	}
	return &http.Response{
		StatusCode: 200,
		Header:     h,
		Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"the deliverable"}}],` +
			`"usage":{"prompt_tokens":100,"completion_tokens":20}}`)),
		Request: r,
	}, nil
}

func (rt *recordingTransport) seen() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.hosts...)
}

func recordTheNetwork(t *testing.T, headers http.Header) *recordingTransport {
	t.Helper()
	rt := &recordingTransport{headers: headers}
	old := http.DefaultTransport
	http.DefaultTransport = rt
	t.Cleanup(func() { http.DefaultTransport = old })
	return rt
}

func anAnthropicGatewayOnly() Gateway {
	return Gateway{URL: "http://anthropic-gateway.invalid", RunID: "crew-1",
		AgentID: "agent://x/y.mercer", BudgetUSD: "1.00"}
}

// The defect, as it ran: engine openrouter, a gateway configured, and the
// call went to openrouter.ai. Red against the unchanged tree for that reason,
// with only identifiers the unchanged tree has.
func TestAGatewayIsNeverSilentlyIgnoredForAnOpenRouterCall(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	rt := recordTheNetwork(t, nil)

	_, err := Call(context.Background(), "openrouter", "some/model", "hello", 100, anAnthropicGatewayOnly())

	if hosts := rt.seen(); len(hosts) != 0 {
		t.Fatalf("the call went to %v with a gateway configured: the gateway was ignored and "+
			"the spend left it", hosts)
	}
	if err == nil {
		t.Fatal("an openrouter call with only an Anthropic-shaped gateway configured was accepted")
	}
	if !strings.Contains(err.Error(), "no gateway route") {
		t.Errorf("the refusal does not say there is no gateway route: %v", err)
	}
}

// The same refusal, by its type, and the same for the engine on the other
// side: an Anthropic call with only an OpenAI-shaped gateway configured has
// no route either, and goes nowhere.
func TestACallWithNoGatewayRouteForItsEngineIsRefusedBeforeAnyRequest(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	rt := recordTheNetwork(t, nil)

	openAIOnly := Gateway{OpenAIURL: "http://openai-gateway.invalid", RunID: "crew-1",
		AgentID: "agent://x/y.mercer", BudgetUSD: "1.00"}

	for _, c := range []struct {
		name, engine string
		gw           Gateway
	}{
		{"openrouter, Anthropic gateway only", "openrouter", anAnthropicGatewayOnly()},
		{"anthropic, OpenAI gateway only", "anthropic", openAIOnly},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Call(context.Background(), c.engine, "m", "hello", 100, c.gw)
			if !errors.Is(err, ErrNoGatewayRoute) {
				t.Fatalf("err = %v, want one wrapping ErrNoGatewayRoute", err)
			}
		})
	}
	if hosts := rt.seen(); len(hosts) != 0 {
		t.Errorf("a refused call still reached %v", hosts)
	}
}

// Bedrock speaks neither wire. With any gateway on it is refused, before the
// AWS client is even built; the context is bounded so a regression to the old
// direct route fails in seconds rather than waiting on AWS credential lookup.
func TestABedrockCallWithAGatewayConfiguredIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	both := anAnthropicGatewayOnly()
	both.OpenAIURL = "http://openai-gateway.invalid"

	_, err := Call(ctx, "bedrock", "some.model", "hello", 100, both)
	if !errors.Is(err, ErrNoGatewayRoute) {
		t.Fatalf("err = %v, want one wrapping ErrNoGatewayRoute: bedrock has no route even when "+
			"both wires have a gateway", err)
	}
	if !strings.Contains(err.Error(), "bedrock") {
		t.Errorf("the refusal does not name the engine: %v", err)
	}
}

// With no gateway at all, nothing changes: every engine routes direct, and
// the openrouter request is built for openrouter.ai with none of the
// x-fuse-* headers on it.
func TestWithNoGatewayAtAllEveryEngineStillGoesDirect(t *testing.T) {
	for _, engine := range []string{"anthropic", "openrouter", "bedrock", "something-else"} {
		if got, err := (Gateway{}).RouteFor(engine); got != "" || err != nil {
			t.Errorf("RouteFor(%q) with no gateway = (%q, %v), want (\"\", nil)", engine, got, err)
		}
	}
	req, err := openRouterRequest(context.Background(), "sk-or-k", "m", "hello", 100, Gateway{})
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != "https://openrouter.ai/api/v1/chat/completions" {
		t.Errorf("URL %q, want the direct OpenRouter endpoint unchanged", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-or-k" {
		t.Errorf("Authorization = %q", got)
	}
	for _, h := range []string{"x-fuse-run-id", "x-fuse-agent-id", "x-fuse-budget-usd", "x-fuse-parent-run-id"} {
		if v := req.Header.Get(h); v != "" {
			t.Errorf("with no gateway configured, header %s carries %q; it must not be set", h, v)
		}
	}
}

// RouteFor is a table over the three engines and the four ways of
// configuring two gateways, so the rule is one a reader can check at a glance.
func TestRouteForNamesTheGatewayThatFrontsEachEnginesWire(t *testing.T) {
	a, o := "http://a.invalid", "http://o.invalid"
	for _, c := range []struct {
		engine string
		gw     Gateway
		want   string
		refuse bool
	}{
		{"anthropic", Gateway{URL: a}, a, false},
		{"anthropic", Gateway{URL: a, OpenAIURL: o}, a, false},
		{"anthropic", Gateway{OpenAIURL: o}, "", true},
		{"openrouter", Gateway{OpenAIURL: o}, o, false},
		{"openrouter", Gateway{URL: a, OpenAIURL: o}, o, false},
		{"openrouter", Gateway{URL: a}, "", true},
		{"bedrock", Gateway{URL: a, OpenAIURL: o}, "", true},
		{"bedrock", Gateway{URL: a}, "", true},
		{"bedrock", Gateway{OpenAIURL: o}, "", true},
		{"bedrock", Gateway{}, "", false},
	} {
		got, err := c.gw.RouteFor(c.engine)
		if c.refuse {
			if !errors.Is(err, ErrNoGatewayRoute) {
				t.Errorf("RouteFor(%q) with %+v = (%q, %v), want ErrNoGatewayRoute", c.engine, c.gw, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("RouteFor(%q) with %+v = (%q, %v), want (%q, nil)", c.engine, c.gw, got, err, c.want)
		}
	}
}

// Through the OpenAI-shaped gateway the request is the OpenAI request, at the
// gateway's own /v1/chat/completions, carrying the very x-fuse-* headers the
// Anthropic request carries; a parent run id only when the caller has one.
func TestAnOpenRouterRequestThroughTheGatewayCarriesTheSameFuseHeaders(t *testing.T) {
	gw := Gateway{
		URL: "http://anthropic-gateway.invalid", OpenAIURL: "http://127.0.0.1:1",
		RunID: "crew-9", AgentID: "agent://x/y.mercer", BudgetUSD: "1.00",
	}
	req, err := openRouterRequest(context.Background(), "sk-or-k", "some/model", "hello", 123, gw)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != "http://127.0.0.1:1/v1/chat/completions" {
		t.Errorf("URL %q, want the OpenAI-shaped gateway's own /v1/chat/completions "+
			"(not the Anthropic gateway, not openrouter.ai)", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-or-k" {
		t.Errorf("Authorization = %q: the provider key must travel as it does direct, for the "+
			"gateway to pass through", got)
	}
	// Exactly the headers anthropicRequest sets for the same Gateway.
	ant, err := anthropicRequest(context.Background(), "k", "m", "p", 1, gw)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"x-fuse-run-id", "x-fuse-agent-id", "x-fuse-budget-usd", "x-fuse-parent-run-id"} {
		if req.Header.Get(h) != ant.Header.Get(h) {
			t.Errorf("%s = %q on the OpenAI request and %q on the Anthropic one", h,
				req.Header.Get(h), ant.Header.Get(h))
		}
	}
	for h, want := range map[string]string{
		"x-fuse-run-id": "crew-9", "x-fuse-agent-id": "agent://x/y.mercer", "x-fuse-budget-usd": "1.00",
	} {
		if got := req.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	if v := req.Header.Get("x-fuse-parent-run-id"); v != "" {
		t.Errorf("x-fuse-parent-run-id carries %q with no parent given; it must never be invented", v)
	}
	if v := req.Header.Get("x-fuse-outcome"); v != "" {
		t.Errorf("x-fuse-outcome carries %q; this runner never sets it", v)
	}
	gw.ParentRunID = "crew-8"
	req2, _ := openRouterRequest(context.Background(), "sk-or-k", "some/model", "hello", 123, gw)
	if got := req2.Header.Get("x-fuse-parent-run-id"); got != "crew-8" {
		t.Errorf("x-fuse-parent-run-id = %q, want crew-8 once the caller has a parent", got)
	}

	body, _ := io.ReadAll(req.Body)
	for _, want := range []string{`"model":"some/model"`, `"max_tokens":123`, `"role":"user"`, `"content":"hello"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the request body %s does not carry %s", body, want)
		}
	}
}

// openAIGateway answers the way a TokenFuse process on TOKENFUSE_WIRE=openai
// does: an OpenAI-shaped body and the three settlement headers.
func openAIGateway(t *testing.T, headers map[string]string, seen *http.Request) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = *r.Clone(context.Background())
		}
		w.Header().Set("Content-Type", "application/json")
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"the deliverable"}}],`+
			`"usage":{"prompt_tokens":2874,"completion_tokens":200}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The settlement the gateway sends on the OpenAI door is the charge, read
// exactly as the Anthropic path reads it (invariant 51).
func TestCallOpenRouterCarriesTheGatewaysSettlementOnItsResult(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	var seen http.Request
	srv := openAIGateway(t, map[string]string{
		"x-fuse-cost-usd": "0.058110", "x-fuse-spent-usd": "0.116220", "x-fuse-price": "fallback",
	}, &seen)

	gw := Gateway{OpenAIURL: srv.URL, RunID: "crew-1", AgentID: "agent://x/y.mercer", BudgetUSD: "1.00"}
	res, err := Call(context.Background(), "openrouter", "some/model", "hello", 100, gw)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if seen.URL.Path != "/v1/chat/completions" {
		t.Errorf("the gateway saw path %q, want /v1/chat/completions", seen.URL.Path)
	}
	if got := seen.Header.Get("x-fuse-run-id"); got != "crew-1" {
		t.Errorf("x-fuse-run-id = %q", got)
	}
	if res.Text != "the deliverable" || res.InTokens != 2874 || res.OutTokens != 200 {
		t.Errorf("result = %+v", res)
	}
	if !res.Settled || res.SettledMicros != 58110 {
		t.Errorf("Settled=%v SettledMicros=%d, want true/58110", res.Settled, res.SettledMicros)
	}
	if !res.RunSpentKnown || res.RunSpentMicros != 116220 {
		t.Errorf("RunSpent = %v/%d, want true/116220", res.RunSpentKnown, res.RunSpentMicros)
	}
	if res.PriceBasis != "fallback" {
		t.Errorf("PriceBasis = %q, want fallback", res.PriceBasis)
	}
	if got := res.ChargeMicros(); got != 58110 {
		t.Errorf("ChargeMicros() = %d, want 58110: the gateway's settlement, never the caller's price", got)
	}

	// No settlement header: unsettled, so the caller prices it itself.
	bare := openAIGateway(t, nil, nil)
	res2, err := Call(context.Background(), "openrouter", "some/model", "hello", 100,
		Gateway{OpenAIURL: bare.URL, RunID: "crew-1", AgentID: "agent://x/y.mercer", BudgetUSD: "1.00"})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Settled {
		t.Error("Settled = true with no settlement header sent at all")
	}
	if got := Charge(res2.Settlement, 777); got != 777 {
		t.Errorf("Charge(unsettled, 777) = %d, want 777", got)
	}
}

// Hostile settlement headers on the OpenAI door: whatever the gateway (or
// something pretending to be it) sends, the call neither panics nor books a
// charge from a value that is not a plain bounded decimal. ParseSettlement
// is exhaustively tested on its own; this proves the OpenRouter path reads
// through it and not around it.
func TestHostileSettlementHeadersOnTheOpenAIDoorNeverBecomeACharge(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	for name, v := range map[string]string{
		"signed negative":  "-0.5",
		"signed positive":  "+0.5",
		"not a number":     "NaN",
		"infinity":         "Inf",
		"exponent":         "1e9",
		"above the cap":    "1000000.000001",
		"far above":        "999999999999999999999",
		"two dots":         "1.2.3",
		"embedded space":   "0.05 0.06",
		"a megabyte":       strings.Repeat("9", 1<<20),
		"empty":            "",
		"hex":              "0x10",
		"unicode digits":   "٠.٥",
		"leading garbage":  "usd0.05",
		"trailing garbage": "0.05usd",
	} {
		t.Run(name, func(t *testing.T) {
			// A dedicated handler rather than openAIGateway's header map, so
			// the value reaches the wire byte for byte, empty and a megabyte
			// included.
			hostile := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header()["X-Fuse-Cost-Usd"] = []string{v}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"choices":[{"message":{"content":"x"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
			}))
			defer hostile.Close()
			res, err := Call(context.Background(), "openrouter", "m", "hello", 10,
				Gateway{OpenAIURL: hostile.URL, RunID: "crew-1", AgentID: "agent://x/y", BudgetUSD: "1.00"})
			if err != nil {
				// A megabyte header may be refused by the transport itself;
				// that is no charge either.
				return
			}
			if res.Settled {
				t.Errorf("header %.20q became a charge of %d micros", v, res.SettledMicros)
			}
		})
	}
	// Two values for one name is an ambiguity, and an ambiguous charge is none.
	dup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header()["X-Fuse-Cost-Usd"] = []string{"0.05", "0.06"}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"x"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer dup.Close()
	res, err := Call(context.Background(), "openrouter", "m", "hello", 10,
		Gateway{OpenAIURL: dup.URL, RunID: "crew-1", AgentID: "agent://x/y", BudgetUSD: "1.00"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Settled {
		t.Errorf("two x-fuse-cost-usd values became a charge of %d micros", res.SettledMicros)
	}
}

// On the direct route a vendor that happens to send an x-fuse-cost-usd header
// is not a gateway, and the header is never a charge.
func TestASettlementHeaderOnTheDirectOpenRouterRouteIsNeverACharge(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	rt := recordTheNetwork(t, http.Header{"X-Fuse-Cost-Usd": {"0.058110"}, "X-Fuse-Spent-Usd": {"0.058110"}})

	res, err := Call(context.Background(), "openrouter", "m", "hello", 10, Gateway{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.seen(); len(got) != 1 || got[0] != "openrouter.ai" {
		t.Fatalf("hosts %v, want exactly openrouter.ai: with no gateway the call is direct", got)
	}
	if res.Settled || res.RunSpentKnown {
		t.Errorf("a settlement was read off a direct call: %+v", res.Settlement)
	}
}

// A 402 from the OpenAI-shaped gateway is a budget refusal, parsed from its
// body (a superset of the Anthropic one: message, code and param beside the
// documented fields), never an ordinary failed call.
func TestA402FromTheOpenAIGatewayIsAGatewayRefusal(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprint(w, `{"error":{"type":"run_budget_exceeded","message":"run crew-1 stopped","code":"run_budget_exceeded",`+
			`"param":null,"budget_usd":0.35,"spent_usd":0.2028,"reason":"per-run budget exceeded","run_id":"crew-1"}}`)
	}))
	defer srv.Close()

	_, err := Call(context.Background(), "openrouter", "m", "hello", 10,
		Gateway{OpenAIURL: srv.URL, RunID: "crew-1", AgentID: "agent://x/y", BudgetUSD: "0.35"})
	var gr GatewayRefusal
	if !errors.As(err, &gr) {
		t.Fatalf("err = %v, want a GatewayRefusal", err)
	}
	for _, want := range []string{"per-run budget exceeded", "0.3500", "0.2028", "crew-1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not name %q", err.Error(), want)
		}
	}
}

// TokenFuse refuses a call with no run id or no agent id; this runner refuses
// first, on the OpenAI route as on the Anthropic one.
func TestAnEmptyRunOrAgentIDRefusesBeforeAnOpenRouterGatewayCall(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()

	for _, c := range []struct {
		name string
		gw   Gateway
		want string
	}{
		{"run id", Gateway{OpenAIURL: srv.URL, RunID: "", AgentID: "agent://x/y"}, "run id"},
		{"agent id", Gateway{OpenAIURL: srv.URL, RunID: "crew-1", AgentID: ""}, "agent id"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Call(context.Background(), "openrouter", "m", "hello", 10, c.gw)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want a refusal naming the %s", err, c.want)
			}
		})
	}
	if called {
		t.Error("the gateway was reached by a call that has no identity to meter")
	}
}

// -gateway-openai is validated exactly as -gateway is, and says which flag
// was wrong.
func TestGatewayOpenAIIsValidatedLikeGateway(t *testing.T) {
	for _, bad := range []string{"ftp://localhost:4178", "localhost:4178", "not a url at all", "  "} {
		_, err := NormalizeGatewayOpenAI(bad)
		if err == nil {
			t.Errorf("NormalizeGatewayOpenAI(%q) was accepted; only http(s) may be a gateway", bad)
			continue
		}
		if !strings.Contains(err.Error(), "-gateway-openai") {
			t.Errorf("the refusal %q does not name -gateway-openai", err)
		}
		if _, err2 := NormalizeGateway(bad); err2 == nil {
			t.Errorf("the two flags disagree about %q", bad)
		}
	}
	if got, err := NormalizeGatewayOpenAI(""); got != "" || err != nil {
		t.Errorf("empty means off: got (%q, %v)", got, err)
	}
	if got, err := NormalizeGatewayOpenAI("http://localhost:4178/"); err != nil || got != "http://localhost:4178" {
		t.Errorf("trailing slash: got (%q, %v), want it stripped", got, err)
	}
	// The existing flag keeps its own name in its own message.
	if _, err := NormalizeGateway("ftp://x"); err == nil || !strings.Contains(err.Error(), "-gateway ") {
		t.Errorf("NormalizeGateway's message changed: %v", err)
	}
}

func TestGatewayOpenAIEnvDefaultReadsItsOwnVariable(t *testing.T) {
	t.Setenv("COSTCREW_GATEWAY", "http://anthropic.invalid")
	t.Setenv("COSTCREW_GATEWAY_OPENAI", " http://127.0.0.1:4178 ")
	if got := GatewayOpenAIEnvDefault(); got != "http://127.0.0.1:4178" {
		t.Errorf("GatewayOpenAIEnvDefault() = %q, want the trimmed env value", got)
	}
	if got := GatewayEnvDefault(); got != "http://anthropic.invalid" {
		t.Errorf("GatewayEnvDefault() = %q: the two variables must not read each other", got)
	}
	t.Setenv("COSTCREW_GATEWAY_OPENAI", "")
	if got := GatewayOpenAIEnvDefault(); got != "" {
		t.Errorf("with nothing set = %q, want empty", got)
	}
}

package deliver

// The local engine's door: where a call to the operator's own model server
// goes, what it carries, what it is counted as, and what it says when nobody
// answers.
//
// No test here reaches a real model or a real host. Every server is an
// httptest server on loopback; the two tests that must SEE a call leave (to a
// gateway the call should not reach, to a host that should not be named) use a
// server that records, or a recording transport, so a call that would have
// escaped is observed escaping and never sent.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// recorded is a request a fake server was handed.
type recorded struct {
	Method, Path string
	Header       http.Header
	Body         []byte
}

// fakeModelServer answers every request with status and body, and records
// each one. It stands in for Ollama, vLLM, LM Studio and the llama.cpp server,
// which all speak this one route.
type fakeModelServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []recorded
}

func newFakeModelServer(t *testing.T, status int, body string) *fakeModelServer {
	t.Helper()
	f := &fakeModelServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, r.Header.Clone(), raw})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeModelServer) seen() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

const answerWithUsage = `{"choices":[{"message":{"role":"assistant","content":"the deliverable"},` +
	`"finish_reason":"stop"}],"usage":{"prompt_tokens":120,"completion_tokens":30}}`

const answerWithoutUsage = `{"choices":[{"message":{"role":"assistant","content":"the deliverable"},` +
	`"finish_reason":"stop"}]}`

// closedURL is a loopback address nothing listens on: a server started and
// shut down again, so the port is real and refused.
func closedURL(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.NotFoundHandler())
	u := s.URL
	s.Close()
	return u
}

// ---------------------------------------------------------------- -model-url

// Red first: NormalizeModelURL did not exist. The cases are the ways an
// address a call is built from goes wrong, and the two that carry a secret.
func TestNormalizeModelURL(t *testing.T) {
	ok := map[string]string{
		"":                                  "",
		"http://127.0.0.1:11434/v1":         "http://127.0.0.1:11434/v1",
		"http://127.0.0.1:11434/v1/":        "http://127.0.0.1:11434/v1",
		"  https://models.internal:8443/v1": "https://models.internal:8443/v1",
		"http://localhost:1234":             "http://localhost:1234",
	}
	for in, want := range ok {
		got, err := NormalizeModelURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeModelURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	bad := []struct{ name, in, mustSay string }{
		{"not http", "ftp://models.internal/v1", "not an http(s) URL"},
		{"no scheme", "127.0.0.1:11434/v1", "not an http(s) URL"},
		{"no host", "http:///v1", "not an http(s) URL"},
		{"only whitespace", "   ", "not an http(s) URL"},
		{"userinfo with password", "http://svc:hunter2@models.internal/v1", "credentials"},
		{"userinfo name only", "http://svc@models.internal/v1", "credentials"},
		{"userinfo on a refused scheme", "ftp://svc:hunter2@models.internal/v1", "credentials"},
		{"query string", "http://models.internal/v1?key=abc", "query string or fragment"},
		{"fragment", "http://models.internal/v1#x", "query string or fragment"},
		{"empty query marker", "http://models.internal/v1?", "query string or fragment"},
	}
	for _, c := range bad {
		got, err := NormalizeModelURL(c.in)
		if err == nil {
			t.Errorf("%s: %q was accepted as %q", c.name, c.in, got)
			continue
		}
		if !strings.Contains(err.Error(), c.mustSay) {
			t.Errorf("%s: refused for the wrong reason: %v", c.name, err)
		}
		// The refusal must not repeat the secret it refused.
		for _, secret := range []string{"hunter2", "key=abc"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: the refusal repeats %q: %v", c.name, secret, err)
			}
		}
	}

	// A value that does not even parse may still hold a password.
	if _, err := NormalizeModelURL("http://svc:hunter2@models.internal:notaport/v1"); err == nil {
		t.Error("an unparseable URL carrying a password was accepted")
	} else if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the refusal of an unparseable URL repeats the password: %v", err)
	}
}

// The env var backs the flag, and a blank one is "not configured".
func TestTheModelEnvironmentVariablesBackTheFlags(t *testing.T) {
	t.Setenv("COSTCREW_MODEL_URL", "  http://127.0.0.1:11434/v1  ")
	t.Setenv("COSTCREW_MODEL_NAME", " llama3.1:8b ")
	if got := ModelURLEnvDefault(); got != "http://127.0.0.1:11434/v1" {
		t.Errorf("ModelURLEnvDefault = %q", got)
	}
	if got := ModelNameEnvDefault(); got != "llama3.1:8b" {
		t.Errorf("ModelNameEnvDefault = %q", got)
	}
	t.Setenv("COSTCREW_MODEL_URL", "")
	if got := ModelURLEnvDefault(); got != "" {
		t.Errorf("an empty COSTCREW_MODEL_URL reads as %q", got)
	}
}

// ---------------------------------------------------------------------- route

// Invariant 54 decides this one, and this test is where the decision is
// written down: with ANY gateway configured, the local engine goes through the
// gateway that fronts the OpenAI wire or is refused. -gateway alone (the
// Anthropic wire) is not a route for it, and the refusal is the existing rule
// and not a new one.
func TestRouteForLocalFollowsTheOpenAIGatewayOrRefuses(t *testing.T) {
	// No gateway at all: direct, "" and nil, as every engine is.
	if base, err := (Gateway{}).RouteFor("local"); base != "" || err != nil {
		t.Errorf("no gateway: RouteFor(local) = %q, %v; want direct", base, err)
	}
	// The OpenAI-shaped gateway fronts it.
	if base, err := (Gateway{OpenAIURL: "http://oa.test"}).RouteFor("local"); base != "http://oa.test" || err != nil {
		t.Errorf("OpenAI gateway: RouteFor(local) = %q, %v", base, err)
	}
	// Both configured: still the OpenAI one.
	if base, err := (Gateway{URL: "http://an.test", OpenAIURL: "http://oa.test"}).RouteFor("local"); base != "http://oa.test" || err != nil {
		t.Errorf("both gateways: RouteFor(local) = %q, %v", base, err)
	}
	// Only the Anthropic-shaped gateway: refused, naming what would fix it.
	_, err := (Gateway{URL: "http://an.test", ModelURL: "http://127.0.0.1:11434/v1"}).RouteFor("local")
	if !errors.Is(err, ErrNoGatewayRoute) {
		t.Fatalf("only -gateway set: err = %v, want one wrapping ErrNoGatewayRoute", err)
	}
	if !strings.Contains(err.Error(), "-gateway-openai") || !strings.Contains(err.Error(), "local") {
		t.Errorf("the refusal does not name the engine and the flag that gives it a route: %v", err)
	}
}

func TestTheLocalEndpointIsTheOperatorsOwnAddress(t *testing.T) {
	// Direct: <ModelURL>/chat/completions, ModelURL already carrying /v1.
	ep, routed, err := OpenAIEndpoint("local", Gateway{ModelURL: "http://127.0.0.1:11434/v1"})
	if err != nil || routed || ep != "http://127.0.0.1:11434/v1/chat/completions" {
		t.Errorf("direct: %q routed=%v err=%v", ep, routed, err)
	}
	// Through the gateway: the gateway's own route, exactly as for openrouter.
	ep, routed, err = OpenAIEndpoint("local", Gateway{OpenAIURL: "http://oa.test", ModelURL: "http://127.0.0.1:11434/v1"})
	if err != nil || !routed || ep != "http://oa.test/v1/chat/completions" {
		t.Errorf("via gateway: %q routed=%v err=%v", ep, routed, err)
	}
	// A gateway that does not front the wire: refused, and the operator's
	// address is NOT the fallback.
	ep, _, err = OpenAIEndpoint("local", Gateway{URL: "http://an.test", ModelURL: "http://127.0.0.1:11434/v1"})
	if !errors.Is(err, ErrNoGatewayRoute) || ep != "" {
		t.Errorf("an Anthropic-only gateway: %q, %v; want a refusal and no endpoint", ep, err)
	}
	// Nowhere to go: says so, naming both ways to give it somewhere.
	_, _, err = OpenAIEndpoint("local", Gateway{})
	if err == nil || !strings.Contains(err.Error(), "-model-url") || !strings.Contains(err.Error(), "-gateway-openai") {
		t.Errorf("no server configured: %v", err)
	}
	// openrouter keeps its own answer through the same function.
	want, wantRouted, wantErr := OpenRouterEndpoint(Gateway{OpenAIURL: "http://oa.test"})
	got, gotRouted, gotErr := OpenAIEndpoint("openrouter", Gateway{OpenAIURL: "http://oa.test"})
	if got != want || gotRouted != wantRouted || (gotErr == nil) != (wantErr == nil) {
		t.Errorf("OpenAIEndpoint(openrouter) = %q,%v,%v; OpenRouterEndpoint = %q,%v,%v",
			got, gotRouted, gotErr, want, wantRouted, wantErr)
	}
	if _, _, err := OpenAIEndpoint("anthropic", Gateway{}); err == nil {
		t.Error("OpenAIEndpoint invented a route for an engine that does not speak this wire")
	}
}

// ------------------------------------------------------------------- the call

// Direct: the request lands on the operator's server, at the OpenAI route,
// carrying the model, the cap and the prompt, with no Authorization header at
// all when the operator set no key.
func TestACallToTheLocalEngineGoesToTheOperatorsServerWithNoKey(t *testing.T) {
	t.Setenv("COSTCREW_MODEL_KEY", "")
	srv := newFakeModelServer(t, 200, answerWithUsage)

	res, err := Call(context.Background(), "local", "llama3.1:8b", "hello", 64,
		Gateway{ModelURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	reqs := srv.seen()
	if len(reqs) != 1 {
		t.Fatalf("the server saw %d request(s), want 1", len(reqs))
	}
	r := reqs[0]
	if r.Method != "POST" || r.Path != "/v1/chat/completions" {
		t.Errorf("%s %s, want POST /v1/chat/completions", r.Method, r.Path)
	}
	if got := r.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q with no COSTCREW_MODEL_KEY: most servers want none, and a "+
			"dangling \"Bearer \" is refused by some", got)
	}
	for _, h := range []string{"x-fuse-run-id", "x-fuse-agent-id", "x-fuse-budget-usd"} {
		if v := r.Header.Get(h); v != "" {
			t.Errorf("a direct call carries %s = %q: there is no gateway to read it", h, v)
		}
	}
	var body struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct{ Role, Content string }
	}
	if err := json.Unmarshal(r.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != "llama3.1:8b" || body.MaxTokens != 64 || len(body.Messages) != 1 ||
		body.Messages[0].Role != "user" || body.Messages[0].Content != "hello" {
		t.Errorf("request body %s", r.Body)
	}
	if res.Text != "the deliverable" || res.InTokens != 120 || res.OutTokens != 30 {
		t.Errorf("result %+v, want the server's text and its own usage 120/30", res)
	}
	if res.Settled {
		t.Error("a direct local call reads as settled by a gateway there is not")
	}
}

// Some servers want a key. It is sent as a bearer token and appears nowhere
// the runner can print.
func TestTheOptionalModelKeyIsSentAsABearerTokenAndNeverEchoed(t *testing.T) {
	const key = "sk-local-secret-8c1f"
	t.Setenv("COSTCREW_MODEL_KEY", "  "+key+"  ")
	srv := newFakeModelServer(t, 200, answerWithUsage)

	if _, err := Call(context.Background(), "local", "m", "hi", 8, Gateway{ModelURL: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if got := srv.seen()[0].Header.Get("Authorization"); got != "Bearer "+key {
		t.Errorf("Authorization = %q, want a bearer token (trimmed)", got)
	}

	// Every error path the call has: none may contain the key.
	for name, g := range map[string]Gateway{
		"unreachable":        {ModelURL: closedURL(t) + "/v1"},
		"server error":       {ModelURL: newFakeModelServer(t, 500, `{"error":"model not loaded"}`).URL + "/v1"},
		"empty body":         {ModelURL: newFakeModelServer(t, 200, "").URL + "/v1"},
		"not json":           {ModelURL: newFakeModelServer(t, 200, "<html>nope</html>").URL + "/v1"},
		"no choices":         {ModelURL: newFakeModelServer(t, 200, `{"choices":[]}`).URL + "/v1"},
		"refused no gateway": {URL: "http://an.test", ModelURL: "http://127.0.0.1:1/v1"},
		"no server":          {},
	} {
		_, err := Call(context.Background(), "local", "m", "hi", 8, g)
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if strings.Contains(err.Error(), key) {
			t.Errorf("%s: the error repeats COSTCREW_MODEL_KEY: %v", name, err)
		}
	}
}

// A server that omits its usage block must not make a call free. On this
// engine the price may be 0, and the token count is what a run's ceiling is
// made of.
func TestAServerThatReportsNoUsageIsCountedAtTheWorstCase(t *testing.T) {
	srv := newFakeModelServer(t, 200, answerWithoutUsage)
	res, err := Call(context.Background(), "local", "m", "a prompt of some length", 200,
		Gateway{ModelURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	sent := len(srv.seen()[0].Body)
	if res.InTokens != sent {
		t.Errorf("counted %d prompt tokens, want the request's %d bytes (one token per byte, the "+
			"rule Tokens already uses)", res.InTokens, sent)
	}
	if res.OutTokens != 200 {
		t.Errorf("counted %d output tokens, want the whole cap, 200", res.OutTokens)
	}

	// Zero and zero is the same absence, not a measurement of nothing.
	zero := newFakeModelServer(t, 200, `{"choices":[{"message":{"content":"x"}}],`+
		`"usage":{"prompt_tokens":0,"completion_tokens":0}}`)
	res, err = Call(context.Background(), "local", "m", "p", 50, Gateway{ModelURL: zero.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.InTokens == 0 || res.OutTokens != 50 {
		t.Errorf("a usage block of zeros was believed: in %d out %d", res.InTokens, res.OutTokens)
	}

	// And a server that DID report is believed, including a short reply.
	one := newFakeModelServer(t, 200, `{"choices":[{"message":{"content":"x"}}],`+
		`"usage":{"prompt_tokens":7,"completion_tokens":0}}`)
	res, err = Call(context.Background(), "local", "m", "p", 50, Gateway{ModelURL: one.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.InTokens != 7 || res.OutTokens != 0 {
		t.Errorf("a reported 7/0 was replaced by %d/%d", res.InTokens, res.OutTokens)
	}
}

func TestCountLocalUsage(t *testing.T) {
	for _, c := range []struct {
		name                  string
		rin, rout, bytes, max int
		wantIn, wantOut       int
		wantEstimated         bool
	}{
		{"reported", 10, 5, 999, 100, 10, 5, false},
		{"only output reported", 0, 5, 999, 100, 0, 5, false},
		{"only input reported", 10, 0, 999, 100, 10, 0, false},
		{"nothing reported", 0, 0, 999, 100, 999, 100, true},
		// A server cannot have used a negative number of tokens: all-negative
		// is a report of nothing, and one negative beside a real number is
		// that number and zero, never a subtraction.
		{"negative is absent", -1, -1, 50, 10, 50, 10, true},
		{"one negative", -3, 5, 50, 10, 0, 5, false},
	} {
		in, out, est := CountLocalUsage(c.rin, c.rout, c.bytes, c.max)
		if in != c.wantIn {
			t.Errorf("%s: in %d, want %d", c.name, in, c.wantIn)
		}
		if out != c.wantOut {
			t.Errorf("%s: out %d, want %d", c.name, out, c.wantOut)
		}
		if est != c.wantEstimated {
			t.Errorf("%s: estimated %v, want %v", c.name, est, c.wantEstimated)
		}
	}
}

// Through the gateway: the gateway's own route, the same x-fuse headers an
// openrouter call carries, the gateway's settlement read, and the operator's
// own server NEVER named by the call (the gateway's upstream is that server).
func TestACallToTheLocalEngineThroughTheOpenAIGatewayIsMetered(t *testing.T) {
	t.Setenv("COSTCREW_MODEL_KEY", "")
	gw := newFakeModelServer(t, 200, answerWithUsage)
	own := newFakeModelServer(t, 200, answerWithUsage)
	// The fake gateway answers with settlement headers via a wrapper server.
	meter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-fuse-cost-usd", "0.004200")
		w.Header().Set("x-fuse-spent-usd", "0.010000")
		w.Header().Set("x-fuse-price", "known")
		gw.Server.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(meter.Close)

	res, err := Call(context.Background(), "local", "m", "hi", 8, Gateway{
		OpenAIURL: meter.URL, ModelURL: own.URL + "/v1",
		RunID: "crew-9", AgentID: "agent://x/y.mercer", BudgetUSD: "1.00"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if n := len(own.seen()); n != 0 {
		t.Errorf("the operator's server was called directly %d time(s) with a gateway configured: "+
			"the spend left the gateway", n)
	}
	r := gw.seen()
	if len(r) != 1 || r[0].Path != "/v1/chat/completions" {
		t.Fatalf("gateway saw %+v, want one request at /v1/chat/completions", r)
	}
	for h, want := range map[string]string{"x-fuse-run-id": "crew-9",
		"x-fuse-agent-id": "agent://x/y.mercer", "x-fuse-budget-usd": "1.00"} {
		if got := r[0].Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	if !res.Settled || res.SettledMicros != 4200 || res.RunSpentMicros != 10000 {
		t.Errorf("settlement not read: %+v", res.Settlement)
	}
	if got := res.ChargeMicros(); got != 4200 {
		t.Errorf("ChargeMicros = %d, want the gateway's 4200 (invariant 51)", got)
	}
}

// Invariant 54, for this engine: a gateway configured that does not front the
// OpenAI wire refuses the call, and the operator's server is not called
// instead.
func TestTheLocalEngineIsNeverSentDirectBehindAGatewaysBack(t *testing.T) {
	own := newFakeModelServer(t, 200, answerWithUsage)
	anthropicGW := newFakeModelServer(t, 200, answerWithUsage)

	_, err := Call(context.Background(), "local", "m", "hi", 8, Gateway{
		URL: anthropicGW.URL, ModelURL: own.URL + "/v1",
		RunID: "crew-9", AgentID: "agent://x/y.mercer", BudgetUSD: "1.00"})
	if !errors.Is(err, ErrNoGatewayRoute) {
		t.Fatalf("err = %v, want one wrapping ErrNoGatewayRoute", err)
	}
	if n := len(own.seen()); n != 0 {
		t.Errorf("the operator's server was called %d time(s) behind the gateway's back", n)
	}
	if n := len(anthropicGW.seen()); n != 0 {
		t.Errorf("the Anthropic-shaped gateway was sent an OpenAI body %d time(s)", n)
	}
}

// A gateway cannot meter a call it cannot attribute.
func TestALocalGatewayCallWithNoRunIDIsRefusedBeforeTheRequest(t *testing.T) {
	gw := newFakeModelServer(t, 200, answerWithUsage)
	_, err := Call(context.Background(), "local", "m", "hi", 8, Gateway{OpenAIURL: gw.URL, AgentID: "agent://x/y"})
	if err == nil || !strings.Contains(err.Error(), "run id") {
		t.Errorf("err = %v, want a refusal naming the run id", err)
	}
	if n := len(gw.seen()); n != 0 {
		t.Errorf("a request was sent anyway (%d)", n)
	}
}

// A 402 is a budget refusal ONLY from a gateway.
func TestA402FromTheLocalGatewayIsARefusalAndFromTheServerIsNot(t *testing.T) {
	body := `{"error":{"type":"run_budget_exceeded","budget_usd":0.35,"spent_usd":0.2,` +
		`"reason":"per-run budget exceeded","run_id":"crew-1"}}`
	gw := newFakeModelServer(t, http.StatusPaymentRequired, body)
	_, err := Call(context.Background(), "local", "m", "hi", 8, Gateway{
		OpenAIURL: gw.URL, RunID: "crew-1", AgentID: "agent://x/y", BudgetUSD: "1.00"})
	var gr GatewayRefusal
	if !errors.As(err, &gr) {
		t.Fatalf("a 402 from the gateway: err = %v, want a GatewayRefusal", err)
	}

	direct := newFakeModelServer(t, http.StatusPaymentRequired, body)
	_, err = Call(context.Background(), "local", "m", "hi", 8, Gateway{ModelURL: direct.URL + "/v1"})
	if err == nil {
		t.Fatal("a 402 from the operator's server was accepted")
	}
	if errors.As(err, &gr) {
		t.Error("a 402 from the operator's own server was read as a gateway budget refusal")
	}
}

// ------------------------------------------------------------ unreachable

// One line, naming the URL, and not the stack of wrappers around "connection
// refused".
func TestAnUnreachableServerIsOneLineNamingItsURL(t *testing.T) {
	base := closedURL(t) + "/v1"
	_, err := Call(context.Background(), "local", "m", "hi", 8, Gateway{ModelURL: base})
	if err == nil {
		t.Fatal("a call to a closed port succeeded")
	}
	msg := err.Error()
	if !strings.Contains(msg, base) {
		t.Errorf("the message does not name the server's URL %q: %s", base, msg)
	}
	if strings.ContainsAny(msg, "\n\r") {
		t.Errorf("the message is more than one line: %q", msg)
	}
	if strings.Contains(msg, "Post \"") || strings.Contains(msg, "goroutine") {
		t.Errorf("the message is the raw transport error, not a sentence: %s", msg)
	}
	if !strings.Contains(msg, "did not answer") {
		t.Errorf("the message does not say what happened: %s", msg)
	}
	if len(msg) > 400 {
		t.Errorf("the message is %d bytes", len(msg))
	}
}

func TestReachErrorTruncatesAndFlattensWhatItWraps(t *testing.T) {
	long := errors.New("boom\nline two\t" + strings.Repeat("x", 500))
	err := ReachError("the local model server", "http://h/v1", long)
	if strings.ContainsAny(err.Error(), "\n\r\t") {
		t.Errorf("not one line: %q", err.Error())
	}
	if len(err.Error()) > 300 {
		t.Errorf("a %d-byte reason was not cut: %d bytes", len(long.Error()), len(err.Error()))
	}
}

// Before a run starts: any answer at all is "there"; only silence is a refusal.
func TestProbeModelServer(t *testing.T) {
	for _, status := range []int{200, 401, 404, 500} {
		srv := newFakeModelServer(t, status, "")
		if err := ProbeModelServer(context.Background(), srv.URL+"/v1"); err != nil {
			t.Errorf("a server that answered %d was reported unreachable: %v", status, err)
		}
		if got := srv.seen(); len(got) != 1 || got[0].Method != "GET" || got[0].Path != "/v1/models" {
			t.Errorf("probe sent %+v, want one GET /v1/models", got)
		}
		if got := srv.seen()[0].Header.Get("Authorization"); got != "" {
			t.Errorf("the probe carried an Authorization header: it names an address and nothing else")
		}
	}
	base := closedURL(t) + "/v1"
	err := ProbeModelServer(context.Background(), base)
	if err == nil || !strings.Contains(err.Error(), base) || strings.ContainsAny(err.Error(), "\n\r") {
		t.Errorf("a closed port: %v", err)
	}
	if err := ProbeModelServer(context.Background(), "http://bad host/v1"); err == nil {
		t.Error("an unusable address was probed without complaint")
	}
}

// -------------------------------------------------- hostile responses

// Whatever a server sends back is input from a process this runner does not
// control. None of these may panic, and every one must be an error or a
// bounded answer.
func TestTheLocalCallSurvivesHostileResponses(t *testing.T) {
	huge := strings.Repeat("a", 5<<20)
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"empty 200":            {200, ""},
		"whitespace 200":       {200, " \n\t "},
		"html 200":             {200, "<html><body>not a model</body></html>"},
		"truncated json":       {200, `{"choices":[{"message":{"content":"ab`},
		"choices not an array": {200, `{"choices":"x"}`},
		"content not a string": {200, `{"choices":[{"message":{"content":42}}]}`},
		"null choices":         {200, `{"choices":null}`},
		"usage as string":      {200, `{"choices":[{"message":{"content":"x"}}],"usage":"lots"}`},
		"negative usage":       {200, `{"choices":[{"message":{"content":"x"}}],"usage":{"prompt_tokens":-5,"completion_tokens":-5}}`},
		"overflowing usage":    {200, `{"choices":[{"message":{"content":"x"}}],"usage":{"prompt_tokens":99999999999999999999,"completion_tokens":1}}`},
		"5 MB 200":             {200, `{"choices":[{"message":{"content":"` + huge + `"}}]}`},
		"5 MB 500":             {500, huge},
		"500 json":             {500, `{"error":{"message":"out of memory"}}`},
		"404 route missing":    {404, "404 page not found"},
	} {
		srv := newFakeModelServer(t, tc.status, tc.body)
		res, err := Call(context.Background(), "local", "m", "hi", 8, Gateway{ModelURL: srv.URL + "/v1"})
		if err != nil {
			if len(err.Error()) > 400 {
				t.Errorf("%s: the error is %d bytes: a hostile body must be cut", name, len(err.Error()))
			}
			if strings.ContainsAny(err.Error(), "\n\r") && tc.status != 200 {
				t.Errorf("%s: the error is more than one line", name)
			}
			continue
		}
		// An answer is only acceptable when it is a real one.
		if res.InTokens < 0 || res.OutTokens < 0 {
			t.Errorf("%s: negative tokens counted: %d/%d", name, res.InTokens, res.OutTokens)
		}
	}
}

// ---------------------------------------------------------- structure

// The local route can name an address the operator typed and nothing else.
func TestNoVendorHostAppearsInTheLocalRoute(t *testing.T) {
	src, err := os.ReadFile("local.go")
	if err != nil {
		t.Fatal(err)
	}
	// Drop comments: the file explains what it never does and names the
	// vendors in doing so.
	var code []string
	for _, line := range strings.Split(string(src), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		code = append(code, line)
	}
	text := strings.Join(code, "\n")
	for _, vendor := range []string{
		"openrouter.ai", "anthropic.com", "deepseek.com", "amazonaws.com",
		"api.openai.com", "googleapis.com", "OpenRouterDirectEndpoint",
		"ANTHROPIC_API_KEY", "OPENROUTER_API_KEY",
	} {
		if strings.Contains(text, vendor) {
			t.Errorf("local.go names %q: a local call must be able to reach only an address the "+
				"operator typed", vendor)
		}
	}
}

// The tool loop's catalogue and rounds cover this engine exactly as they cover
// openrouter, because it is the same wire.
func TestTheLocalEngineLoopsAndSendsTheOpenAICatalogue(t *testing.T) {
	if LoopsFor("local") != MaxToolRounds {
		t.Errorf("LoopsFor(local) = %d, want %d: a local task runs the same tool loop, so it reserves "+
			"the same number of rounds", LoopsFor("local"), MaxToolRounds)
	}
	if ToolCatalogueTokens("local") != ToolCatalogueTokens("openrouter") || ToolCatalogueTokens("local") == 0 {
		t.Errorf("ToolCatalogueTokens(local) = %d, openrouter = %d: the same OpenAI catalogue is sent",
			ToolCatalogueTokens("local"), ToolCatalogueTokens("openrouter"))
	}
}

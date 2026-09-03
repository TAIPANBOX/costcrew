package deliver

// New coverage for the moved call path (B6B-SPEC.md section 4), on top of
// gateway_test.go's own two moved tests. Tier T3: the money path. Every test
// here talks to nothing but net/http/httptest's own fake server -- see
// internal/deliver's package comment on call.go for the rule this file
// exists under: the only "model" any test in this repository talks to.
//
// Red first, against the unchanged tree: Gateway, Result, GatewayRefusal and
// callAnthropic do not exist here yet, so this fails to compile the same way
// gateway_test.go's own two tests do.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Moved from tools/run/anthropic_test.go (B6B-SPEC.md): it tested
// anthropicBody directly, which moved here with call(). Content unchanged.
//
// The request asks for a deliverable, not for reasoning.
//
// Red first: without the field, this read back nothing, which is what four
// tasks on a full run hit. They came back "no text", and once the error named
// its reason it was exact:
//
//	stop_reason "max_tokens", blocks: thinking, 1200 output tokens
//
// The model spent all 1200 tokens reasoning and never reached an answer. The
// task blocked, the person got no deliverable, and the call was billed in full.
func TestAnthropicIsAskedForAnAnswerRatherThanReasoning(t *testing.T) {
	raw, err := anthropicBody("anthropic/claude-sonnet-5", "write it", 1200)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		MaxTokens int `json:"max_tokens"`
		Thinking  struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.Thinking.Type != "disabled" {
		t.Errorf("thinking is %q, want disabled: the model can spend the whole "+
			"token budget reasoning and never write the deliverable, and the "+
			"call is billed either way", body.Thinking.Type)
	}
	if body.MaxTokens != 1200 {
		t.Errorf("max_tokens %d, want 1200: it is what bounds the worst case",
			body.MaxTokens)
	}
}

// Boundary: TokenFuse refuses a call with no run id at all (400
// metering_required, the runner's own comment on why openBus mints one
// unconditionally). This runner must refuse it BEFORE making the call,
// exactly the "refuse before any call" discipline every guard in this
// repository holds, rather than let an empty run id surface as a confusing
// mid-call oddity from the gateway's own side.
func TestAnEmptyRunIDRefusesBeforeTheCall(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	_, err := callAnthropic(context.Background(), "claude-x", "hello", 100,
		Gateway{URL: srv.URL, RunID: "", AgentID: "agent://x/y.mercer"})
	if err == nil {
		t.Fatal("an empty run id with the gateway on was accepted")
	}
	if called {
		t.Error("the gateway was called at all; an empty run id must refuse before any request reaches it")
	}
	if !strings.Contains(err.Error(), "run id") {
		t.Errorf("the refusal does not name the run id: %v", err)
	}
}

// Boundary, the other half: an empty agent id is exactly as unmeterable as
// an empty run id (TokenFuse cannot attribute spend with nobody named), and
// this runner refuses it the same way, before the call.
func TestAnEmptyAgentIDRefusesBeforeTheCall(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	_, err := callAnthropic(context.Background(), "claude-x", "hello", 100,
		Gateway{URL: srv.URL, RunID: "crew-1", AgentID: ""})
	if err == nil {
		t.Fatal("an empty agent id with the gateway on was accepted")
	}
	if called {
		t.Error("the gateway was called at all; an empty agent id must refuse before any request reaches it")
	}
	if !strings.Contains(err.Error(), "agent id") {
		t.Errorf("the refusal does not name the agent id: %v", err)
	}
}

// With the gateway OFF (gw.URL empty), neither boundary check above applies
// at all: TestWithNoGatewayTheRequestGoesToAnthropicDirectly (gateway_test.go)
// already proves anthropicRequest builds a plain direct request from
// Gateway{} with no error and none of the x-fuse-* headers, which is exactly
// the case an empty RunID/AgentID must stay harmless in -- every caller with
// no gateway configured has always carried a zero-value Gateway.

// Hostile: a 400 is not the documented 402 budget-refusal shape, so it must
// not be read as one -- it takes the same path any other non-200 status
// does today, a plain readable error, never a panic and never mistaken for
// the kind of refusal that stops a whole run.
func TestA400MeteringRequiredIsAPlainErrorNotAGatewayRefusal(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"type":"metering_required","reason":"no run id on this call"}}`))
	}))
	defer srv.Close()

	_, err := callAnthropic(context.Background(), "claude-x", "hello", 100,
		Gateway{URL: srv.URL, RunID: "crew-1", AgentID: "agent://x/y.mercer", BudgetUSD: "1.00"})
	if err == nil {
		t.Fatal("a 400 from the gateway was not reported as an error at all")
	}
	var gr GatewayRefusal
	if errors.As(err, &gr) {
		t.Errorf("a 400 is not the documented 402 shape and must not be read as a "+
			"budget refusal (which stops the whole run): %v", err)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("the error does not name the status: %v", err)
	}
}

// Hostile: the gateway's body is input from a process this runner does not
// control, and nothing bounds its size before reading it (read looking for
// an existing cap, per B6B-SPEC.md's own instruction to check live.go for
// one; there is none). A 5 MB body that is not valid JSON must still come
// back as a readable parse error inside a generous bound, never a hang and
// never a panic.
func TestTheGatewayAnswersA5MBBodyWithoutHangingOrPanicking(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	big := strings.Repeat("x", 5*1024*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(big))
	}))
	defer srv.Close()

	type outcome struct {
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		_, err := callAnthropic(context.Background(), "claude-x", "hello", 100,
			Gateway{URL: srv.URL, RunID: "crew-1", AgentID: "agent://x/y.mercer", BudgetUSD: "1.00"})
		done <- outcome{err}
	}()
	select {
	case o := <-done:
		if o.err == nil {
			t.Fatal("5 MB of non-JSON was not reported as an error")
		}
		if !strings.Contains(o.err.Error(), "did not parse") {
			t.Errorf("the error does not say the answer did not parse: %v", o.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("a 5 MB body made the call hang past a generous timeout")
	}
}

// Hostile: a dropped connection is a plain failed call, never a panic and
// never a budget refusal -- the distinction matters because execute()
// stops the WHOLE run on a refusal and only marks a single task blocked on
// anything else, and a network hiccup must take the second path.
func TestTheGatewayClosesTheConnectionWithNoResponse(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		conn.Close()
	}))
	defer srv.Close()

	_, err := callAnthropic(context.Background(), "claude-x", "hello", 100,
		Gateway{URL: srv.URL, RunID: "crew-1", AgentID: "agent://x/y.mercer", BudgetUSD: "1.00"})
	if err == nil {
		t.Fatal("a dropped connection was not reported as an error")
	}
	var gr GatewayRefusal
	if errors.As(err, &gr) {
		t.Errorf("a dropped connection is not a budget refusal: %v", err)
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Error("the error carries no readable message at all")
	}
}

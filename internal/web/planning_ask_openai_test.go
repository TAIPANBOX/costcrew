package web_test

// The console's own spend, the supervisor's one planning call, through the
// gateway that fronts the engine's wire, or refused: never direct. The
// supervisor is seeded onto anthropic; a supervisor re-briefed onto openrouter
// speaks the OpenAI wire and needs -gateway-openai.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type seenRequest struct {
	path    string
	headers http.Header
}

// fakeOpenAIPlanGateway answers every call in the OpenAI shape, with the
// three settlement headers, and records what it was asked.
func fakeOpenAIPlanGateway(t *testing.T, plan string, headers map[string]string) (*httptest.Server, func() []seenRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []seenRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, seenRequest{r.URL.Path, r.Header.Clone()})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		esc := strings.ReplaceAll(strings.ReplaceAll(plan, `\`, `\\`), `"`, `\"`)
		esc = strings.ReplaceAll(esc, "\n", `\n`)
		fmt.Fprintf(w, `{"choices":[{"message":{"content":"%s"}}],`+
			`"usage":{"prompt_tokens":40,"completion_tokens":20}}`, esc)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []seenRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]seenRequest(nil), seen...)
	}
}

func askThePlanner(t *testing.T, h *harness) (int, string) {
	t.Helper()
	_, getBody, _ := h.get(t, "/sprint/plan")
	form := planFormFields(t, getBody)
	form.Set("csrf", h.csrf(t, "/sprint/plan"))
	return postBody(t, h, "/sprint/plan/ask", form)
}

func spendBeforeAndAfter(t *testing.T, h *harness) func() int64 {
	t.Helper()
	var before int64
	if err := h.st.DB().QueryRow(`SELECT COALESCE(SUM(micros),0) FROM plan_asks`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	return func() int64 {
		var after int64
		if err := h.st.DB().QueryRow(`SELECT COALESCE(SUM(micros),0) FROM plan_asks`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		return after - before
	}
}

// An OpenAI-shaped gateway alone does not front the seeded supervisor's
// anthropic engine: the ask is refused with the reason, nothing reaches the
// gateway and nothing is booked.
func TestAskPlanWithOnlyAnOpenAIGatewayRefusesASupervisorOnAnthropic(t *testing.T) {
	srv, seen := fakeOpenAIPlanGateway(t, "```plan\n{\"items\": []}\n```", nil)
	h := startWithGateways(t, "", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	h.signUp(t, "owner", "owner-password-2026")
	spent := spendBeforeAndAfter(t, h)

	code, body := askThePlanner(t, h)
	if code != http.StatusOK {
		t.Fatalf("POST /sprint/plan/ask = %d, want 200 (a shown refusal)", code)
	}
	if !strings.Contains(body, "no gateway route") {
		t.Errorf("the page does not say the engine has no gateway route:\n%s", trimTo(body, 3000))
	}
	if n := len(seen()); n != 0 {
		t.Errorf("the OpenAI-shaped gateway was called %d time(s) for an anthropic supervisor", n)
	}
	if got := spent(); got != 0 {
		t.Errorf("the refused ask booked %d micros", got)
	}
}

// A supervisor on openrouter, with only the Anthropic-shaped gateway
// configured, is refused the same way.
func TestAskPlanForAnOpenRouterSupervisorWithOnlyTheAnthropicGatewayIsRefused(t *testing.T) {
	answer := &planAnswer{body: "```plan\n{\"items\": []}\n```"}
	anthropicSrv := fakePlanGateway(t, answer)
	h := startWithGateways(t, anthropicSrv.URL, "")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	if _, err := h.st.DB().Exec(`UPDATE analysts SET engine='openrouter' WHERE name='supervisor'`); err != nil {
		t.Fatal(err)
	}
	h.signUp(t, "owner", "owner-password-2026")

	code, body := askThePlanner(t, h)
	if code != http.StatusOK {
		t.Fatalf("POST /sprint/plan/ask = %d, want 200", code)
	}
	if !strings.Contains(body, "no gateway route") || !strings.Contains(body, "-gateway-openai") {
		t.Errorf("the page does not name the missing OpenAI-shaped gateway:\n%s", trimTo(body, 3000))
	}
}

// With the OpenAI-shaped gateway, an openrouter supervisor's ask goes to its
// /v1/chat/completions under the supervisor's own agent id, and the
// gateway's settlement is what the ledger books (invariant 51).
func TestAskPlanForAnOpenRouterSupervisorGoesThroughTheOpenAIGateway(t *testing.T) {
	srv, seen := fakeOpenAIPlanGateway(t, "```plan\n{\"items\": []}\n```", map[string]string{
		"x-fuse-cost-usd": "0.058110", "x-fuse-spent-usd": "0.058110", "x-fuse-price": "known",
	})
	h := startWithGateways(t, "", srv.URL)
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	if _, err := h.st.DB().Exec(`UPDATE analysts SET engine='openrouter' WHERE name='supervisor'`); err != nil {
		t.Fatal(err)
	}
	h.signUp(t, "owner", "owner-password-2026")
	spent := spendBeforeAndAfter(t, h)

	code, body := askThePlanner(t, h)
	if code != http.StatusOK {
		t.Fatalf("POST /sprint/plan/ask = %d:\n%s", code, trimTo(body, 2000))
	}
	reqs := seen()
	if len(reqs) != 1 {
		t.Fatalf("the OpenAI-shaped gateway saw %d request(s), want 1:\n%s", len(reqs), trimTo(body, 2000))
	}
	if reqs[0].path != "/v1/chat/completions" {
		t.Errorf("path %q, want /v1/chat/completions", reqs[0].path)
	}
	if got := reqs[0].headers.Get("x-fuse-agent-id"); got != "agent://costcrew.test/supervisor" {
		t.Errorf("x-fuse-agent-id = %q, want the supervisor's own", got)
	}
	if reqs[0].headers.Get("x-fuse-run-id") == "" || reqs[0].headers.Get("x-fuse-budget-usd") == "" {
		t.Errorf("run id or budget missing: %v", reqs[0].headers)
	}
	if got := spent(); got != 58110 {
		t.Errorf("plan_asks booked %d micros, want 58110: the gateway's settlement", got)
	}
}

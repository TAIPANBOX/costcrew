package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestDemoModeRefusesThePlanAskBeforeAnyCall: COSTCREW_DEMO promises that in
// demo mode nobody spends the owner's model budget, whatever their role
// (internal/auth's package rule). The supervisor's plan-ask is the one
// request this console can spend on, so with demo mode on it is refused
// before the call, even with a gateway configured and an admin asking, and
// the gateway is never dialled.
func TestDemoModeRefusesThePlanAskBeforeAnyCall(t *testing.T) {
	var dialled atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dialled.Add(1)
		http.Error(w, "the demo console must never reach a gateway", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	h := startWithGateway(t, srv.URL)
	t.Setenv("COSTCREW_DEMO", "1")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	h.signUp(t, "owner", "owner-password-2026")

	_, getBody, _ := h.get(t, "/sprint/plan")
	form := planFormFields(t, getBody)
	form.Set("csrf", h.csrf(t, "/sprint/plan"))
	code, body := postBody(t, h, "/sprint/plan/ask", form)
	if code != http.StatusOK {
		t.Fatalf("POST /sprint/plan/ask = %d, want 200:\n%s", code, trimTo(body, 2000))
	}
	if n := dialled.Load(); n != 0 {
		t.Errorf("demo mode reached the gateway %d time(s); it must refuse before the call", n)
	}
	if !strings.Contains(body, "demo mode") {
		t.Errorf("the refusal does not say demo mode refused it:\n%s", trimTo(body, 3000))
	}
}

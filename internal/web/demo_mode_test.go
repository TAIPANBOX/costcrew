package web_test

import (
	"net/url"

	"github.com/TAIPANBOX/costcrew/internal/crew"
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

// TestDemoModeRefusesSwitchingTheCadenceOn: the cadence switch turns on the
// clock-driven crew, which spends through costcrew-run -due. In demo mode it
// is refused on, whoever asks, and turning it off still works.
func TestDemoModeRefusesSwitchingTheCadenceOn(t *testing.T) {
	h := start(t)
	t.Setenv("COSTCREW_DEMO", "1")
	h.signUp(t, "boss", "boss-password-2026")
	code, loc := h.post(t, "/cadence", url.Values{
		"enabled": {"on"}, "ceiling": {"25.00"}, "csrf": {h.csrf(t, "/cadence")},
	})
	if code != 303 || !strings.Contains(loc, "demo") {
		t.Errorf("POST /cadence in demo mode = %d %s, want a redirect naming demo mode", code, loc)
	}
	enabled, _, _, _, err := crew.CadenceSettings(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Error("demo mode switched the clock-driven crew on")
	}
	code, loc = h.post(t, "/cadence", url.Values{
		"ceiling": {"0.00"}, "csrf": {h.csrf(t, "/cadence")},
	})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Errorf("turning the switch off in demo mode = %d %s, want a clean redirect", code, loc)
	}
}

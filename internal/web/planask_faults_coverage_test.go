package web_test

// The supervisor's plan-ask, in the refusals and the failures the existing
// plan-ask tests do not reach: a supervisor who is not active, a model answer
// with no plan in it, and a settlement the store cannot write after the model
// has already answered.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// covAskForm is the form the plan page posts, with the token it issued.
func covAskForm(t *testing.T, h *harness) url.Values {
	t.Helper()
	_, page, _ := h.get(t, "/sprint/plan")
	form := planFormFields(t, page)
	form.Set("csrf", h.csrf(t, "/sprint/plan"))
	return form
}

// A supervisor who is not active is refused before any call is made, and the
// page says so.
func TestCovPlanAskRefusesWhenTheSupervisorIsNotActive(t *testing.T) {
	gw := fakePlanGateway(t, &planAnswer{body: "unused"})
	h := startWithGateway(t, gw.URL)
	h.signUp(t, "owner", "owner-password-2026")
	if _, err := h.st.DB().Exec(`UPDATE analysts SET state='suspended' WHERE name='supervisor'`); err != nil {
		t.Fatal(err)
	}
	code, body := postBody(t, h, "/sprint/plan/ask", covAskForm(t, h))
	if code != http.StatusOK || !strings.Contains(body, "no active supervisor analyst is on the roster") {
		t.Errorf("a suspended supervisor's ask: %d, want the page saying no active supervisor is on the roster", code)
	}
}

// A model answer with no plan in it is shown as a refusal of that answer.
func TestCovPlanAskSaysWhenTheAnswerCarriesNoPlan(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	gw := fakePlanGateway(t, &planAnswer{body: "Keep the sprint as it is; nothing needs moving."})
	h := startWithGateway(t, gw.URL)
	h.signUp(t, "owner", "owner-password-2026")
	code, body := postBody(t, h, "/sprint/plan/ask", covAskForm(t, h))
	if code != http.StatusOK || !strings.Contains(body, "carried no fenced") {
		t.Errorf("an answer with no plan block: %d, want the page to say it carried no fenced plan", code)
	}
}

// The model answers, and while the call is in flight the store loses the table
// the settlement writes to. The answer cannot be booked, so the page is a
// server fault, not a plan that looks paid for and was not recorded.
func TestCovPlanAskFailsWhenTheSettlementCannotBeWritten(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	var h *harness
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := h.st.DB().Exec(`DROP TABLE plan_asks`); err != nil {
			t.Errorf("dropping plan_asks while the call is in flight: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"content":[{"type":"text","text":"no plan here"}],`+
			`"stop_reason":"end_turn","usage":{"input_tokens":40,"output_tokens":20}}`)
	}))
	t.Cleanup(gw.Close)
	h = startWithGateway(t, gw.URL)
	h.signUp(t, "owner", "owner-password-2026")

	code, _ := postBody(t, h, "/sprint/plan/ask", covAskForm(t, h))
	if code != http.StatusInternalServerError {
		t.Errorf("an answer that cannot be booked: %d, want 500", code)
	}
}

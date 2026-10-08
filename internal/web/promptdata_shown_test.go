package web_test

// Invariant 91: the -prompt-data setting is shown where a person meets it, and
// a plan-ask's refusal says whether anything was paid.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/deliver"
)

// withPolicy installs a policy for the test and puts back what was there.
func withPolicy(t *testing.T, mode deliver.PromptData) {
	t.Helper()
	p, err := deliver.NewPolicy(mode, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(deliver.SetActivePolicy(p))
}

func TestTheEnginesAndPlanPagesShowThePromptDataMode(t *testing.T) {
	for _, mode := range []deliver.PromptData{deliver.PromptFull, deliver.PromptMasked, deliver.PromptAggregates} {
		t.Run(string(mode), func(t *testing.T) {
			withPolicy(t, mode)
			line := deliver.ActivePolicy().ModeLine()
			h := start(t)
			h.signUp(t, "boss", "boss-password-2026")
			for _, p := range []string{"/engines", "/sprint/plan"} {
				_, body, _ := h.get(t, p)
				if !strings.Contains(body, "<code>-prompt-data "+string(mode)+"</code>") && !strings.Contains(body, "<code>"+string(mode)+"</code>") {
					t.Errorf("%s does not name the -prompt-data mode %s", p, mode)
				}
				if !strings.Contains(body, htmlText(line)) {
					t.Errorf("%s does not show the mode line the model is shown", p)
				}
				withheld := strings.Contains(body, "withheld")
				if mode == deliver.PromptFull && withheld {
					t.Errorf("%s says typed text is withheld under full", p)
				}
				if mode != deliver.PromptFull && !withheld {
					t.Errorf("%s does not say the typed goal is withheld under %s", p, mode)
				}
			}
			_, plan, _ := h.get(t, "/sprint/plan")
			if mode != deliver.PromptFull && !strings.Contains(plan, "the goal you type is withheld") {
				t.Errorf("/sprint/plan does not say plainly that the typed goal is withheld under %s", mode)
			}
		})
	}
}

// htmlText is s as html/template prints it in element text.
func htmlText(s string) string {
	return strings.NewReplacer("&", "&amp;", "'", "&#39;", "<", "&lt;", ">", "&gt;", `"`, "&#34;").Replace(s)
}

// TestAPaidAnswerThatFailedValidationIsNotCalledARefusedCall: the call was
// made and booked, so the page says so and names the amount.
func TestAPaidAnswerThatFailedValidationIsNotCalledARefusedCall(t *testing.T) {
	answer := &planAnswer{body: "```plan\n" +
		`{"items": [{"assignee": "supervisor", "budget_cents": 100, "why": "invented"}]}` + "\n```"}
	srv := fakePlanGateway(t, answer)
	h := startWithGateway(t, srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	h.signUp(t, "owner", "owner-password-2026")
	_, getBody, _ := h.get(t, "/sprint/plan")
	form := planFormFields(t, getBody)
	form.Set("csrf", h.csrf(t, "/sprint/plan"))
	code, body := postBody(t, h, "/sprint/plan/ask", form)
	if code != http.StatusOK {
		t.Fatalf("POST /sprint/plan/ask = %d", code)
	}
	if !strings.Contains(body, "The call was made and paid for, booked at") || !strings.Contains(body, "its answer was refused") {
		t.Errorf("a paid call whose answer failed validation is not said to be paid: %s", trimTo(body, 4000))
	}
	if strings.Contains(body, "refused before it was made") {
		t.Error("a paid call is described as refused before it was made")
	}
}

// TestARefusalBeforeTheCallSaysNothingWasSpent: no gateway, so no call.
func TestARefusalBeforeTheCallSaysNothingWasSpent(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	_, getBody, _ := h.get(t, "/sprint/plan")
	form := planFormFields(t, getBody)
	form.Set("csrf", h.csrf(t, "/sprint/plan"))
	_, body := postBody(t, h, "/sprint/plan/ask", form)
	if !strings.Contains(body, "The call was refused before it was made, so nothing was spent") {
		t.Errorf("a refusal before any call does not say nothing was spent: %s", trimTo(body, 4000))
	}
	if strings.Contains(body, "The call was made and paid for") {
		t.Error("a refusal before any call is described as paid")
	}
}

// TestACallThatFailedSaysNothingWasBooked: the gateway answered with an
// error, so the call was tried, failed, and booked nothing.
func TestACallThatFailedSaysNothingWasBooked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	h := startWithGateway(t, srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	h.signUp(t, "owner", "owner-password-2026")
	_, getBody, _ := h.get(t, "/sprint/plan")
	form := planFormFields(t, getBody)
	form.Set("csrf", h.csrf(t, "/sprint/plan"))
	_, body := postBody(t, h, "/sprint/plan/ask", form)
	if !strings.Contains(body, "The call was tried and failed, and nothing was booked for it") {
		t.Errorf("a failed call is not described as tried and failed: %s", trimTo(body, 4000))
	}
}

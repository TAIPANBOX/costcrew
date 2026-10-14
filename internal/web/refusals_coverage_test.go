package web_test

// Refusals the console makes at its doors and at its plan, each with the
// reason it gives. Where a read or a write fails, the fault is a table dropped
// after the page that names it has been fetched, so the form is real and the
// failure is the store's.

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var covRuleAction = regexp.MustCompile(`action="/allocation/rule/(\d+)"`)

// The sign-up door refuses a body it cannot parse, a password too short to
// register, and a registration it cannot check. The first two are refusals; the
// third is a server fault.
func TestCovSignupRefusesWhatItCannotRegister(t *testing.T) {
	h := startBare(t)
	csrf := h.csrf(t, "/signup")

	bad, err := h.c.Post(h.srv.URL+"/signup", "application/x-www-form-urlencoded",
		strings.NewReader("username=%zz"))
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if !strings.Contains(bad.Header.Get("Location"), "reload%20the%20page%20and%20try%20again") {
		t.Errorf("an unparseable sign-up: %d %s, want the reload message", bad.StatusCode, bad.Header.Get("Location"))
	}

	code, loc := h.post(t, "/signup", url.Values{
		"csrf": {csrf}, "username": {"founder"}, "password": {"short"}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/signup?msg=") {
		t.Errorf("a password too short to register: %d %s, want the sign-up page with the reason", code, loc)
	}

	// With the users table unreadable the console cannot say whether anybody
	// may register, so it does not let anybody register.
	if _, err := h.st.DB().Exec(`DROP TABLE users`); err != nil {
		t.Fatal(err)
	}
	code, _ = h.post(t, "/signup", url.Values{
		"csrf": {csrf}, "username": {"founder"}, "password": {"founder-password-2026"}})
	if code != http.StatusInternalServerError {
		t.Errorf("sign-up with the users table gone: %d, want 500", code)
	}
}

// Sign-in refuses a form it cannot parse, and answers a store it cannot read
// with a server fault, not with a refusal of the password.
func TestCovSigninRefusesWhatItCannotRead(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	csrf := h.csrf(t, "/login")

	bad, err := h.c.Post(h.srv.URL+"/login", "application/x-www-form-urlencoded",
		strings.NewReader("username=%zz"))
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if !strings.Contains(bad.Header.Get("Location"), "reload%20the%20page%20and%20try%20again") {
		t.Errorf("an unparseable sign-in: %d %s, want the reload message", bad.StatusCode, bad.Header.Get("Location"))
	}

	if _, err := h.st.DB().Exec(`DROP TABLE users`); err != nil {
		t.Fatal(err)
	}
	code, _ := h.post(t, "/login", url.Values{
		"csrf": {csrf}, "username": {"owner"}, "password": {"owner-password-2026"}})
	if code != http.StatusInternalServerError {
		t.Errorf("sign-in with the users table gone: %d, want 500", code)
	}
}

// Each plan action that re-reads the sprints table refuses with the store's
// own reason when that read fails, and books nothing.
func TestCovPlanActionsSayWhyTheSprintsCannotBeRead(t *testing.T) {
	actions := []struct{ name, path string }{
		{"the supervisor's ask", "/sprint/plan/ask"},
		{"the model's approval", "/sprint/plan/approve-model"},
		{"the deterministic approval", "/sprint/plan"},
	}
	for _, a := range actions {
		t.Run(a.name, func(t *testing.T) {
			h := start(t)
			h.signUp(t, "owner", "owner-password-2026")
			form := covAskForm(t, h)
			form.Set("answer", "")
			if _, err := h.st.DB().Exec(`DROP TABLE sprints`); err != nil {
				t.Fatal(err)
			}
			code, loc := h.post(t, a.path, form)
			if code != http.StatusSeeOther || !covHasMsg(loc, "no such table") {
				t.Errorf("%s with the sprints table gone: %d %s, want the store's reason", a.name, code, loc)
			}
		})
	}
}

// The allocation rule form refuses a rule number that is not one and a method
// the console does not know, and saves a method it does.
func TestCovAllocationRuleFormRefusesWhatItCannotSave(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	_, page, _ := h.get(t, "/allocation")
	m := covRuleAction.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("the allocation page carries no rule to change")
	}
	csrf := h.csrf(t, "/allocation")
	rule := m[1]

	code, loc := h.post(t, "/allocation/rule/abc", url.Values{"csrf": {csrf}, "method": {"even-split"}})
	if code != http.StatusSeeOther || !covHasMsg(loc, "no such rule") {
		t.Errorf("a rule that is not a number: %d %s", code, loc)
	}
	code, loc = h.post(t, "/allocation/rule/"+rule, url.Values{"csrf": {csrf}, "method": {"sideways"}})
	if code != http.StatusSeeOther || !covHasMsg(loc, `no such method: "sideways"`) {
		t.Errorf("an unknown method: %d %s", code, loc)
	}
	code, loc = h.post(t, "/allocation/rule/"+rule, url.Values{"csrf": {csrf}, "method": {"even-split"}})
	if code != http.StatusSeeOther || strings.Contains(loc, "msg=") {
		t.Errorf("a known method: %d %s, want it saved with no message", code, loc)
	}
}

// The chargeback page reads the customer-unit rules; with that read gone it is
// a server fault, not a page of units with no rules behind them.
func TestCovChargebackFailsWhenItsUnitRulesCannotBeRead(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	if _, err := h.st.DB().Exec(`DROP TABLE unit_rules`); err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.get(t, "/chargeback")
	if code != http.StatusInternalServerError || !strings.Contains(body, "store unavailable") {
		t.Errorf("chargeback with the unit rules gone: %d, want 500", code)
	}
}

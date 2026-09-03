package web_test

// C9-SPEC.md section 2: "Lifting: a console action on the desk page,
// operator, CSRF, with a reason, journaled; the analysts return to active."
//
// Red first, against main: POST /desk/{name}/lift does not exist, so every
// request against it answers 404 and nothing here can pass.

import (
	"net/url"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

func TestLiftHaltReactivatesTheDesksAnalysts(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	op := h.as(t, "boss", "boss-password-2026")

	reason := "the tagging feed on aws is current again"
	if _, _, err := crew.ApplyHalt(h.st.DB(), "aws", "stale feed",
		"data-quality", "owner", "2026-09-01", h.st.AsRecorder()); err != nil {
		t.Fatal(err)
	}
	code, loc := op.post(t, "/desk/aws/lift", url.Values{
		"csrf":   {op.csrf(t, "/desk/aws")},
		"reason": {reason},
	})
	if code != 303 {
		t.Fatalf("POST /desk/aws/lift = %d, want 303", code)
	}
	// redirectMsg's own convention: a bare success redirect carries no
	// ?msg= at all, only a refusal does.
	if strings.Contains(loc, "msg=") {
		t.Errorf("lifting aws was refused: Location %q", loc)
	}

	if _, found, err := crew.ActiveHalt(h.st.DB(), "aws"); err != nil || found {
		t.Errorf("ActiveHalt(aws) after lifting: found=%v err=%v, want the halt gone", found, err)
	}
	roster, err := crew.Roster(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range roster {
		if a.Desk == "aws" && a.State == "suspended" && a.Reason == "stale feed" {
			t.Errorf("%s is still suspended for the lifted halt's own reason", a.Name)
		}
	}
}

// A lift with no reason is refused, and changes nothing: C9-SPEC.md's own
// mutant, "lift without a reason".
func TestLiftHaltRequiresAReason(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	op := h.as(t, "boss", "boss-password-2026")

	if _, _, err := crew.ApplyHalt(h.st.DB(), "gcp", "stale feed",
		"data-quality", "owner", "2026-09-01", h.st.AsRecorder()); err != nil {
		t.Fatal(err)
	}

	code, loc := op.post(t, "/desk/gcp/lift", url.Values{
		"csrf":   {op.csrf(t, "/desk/gcp")},
		"reason": {""},
	})
	if code != 303 {
		t.Fatalf("POST /desk/gcp/lift with no reason = %d, want 303 (a redirect back with a message)", code)
	}
	if !strings.Contains(loc, "msg=") {
		t.Errorf("lifting gcp with no reason was accepted: Location %q", loc)
	}
	if _, found, err := crew.ActiveHalt(h.st.DB(), "gcp"); err != nil || !found {
		t.Errorf("ActiveHalt(gcp) after a refused lift: found=%v err=%v, want the halt still there", found, err)
	}
}

// The desk page itself shows the halt and the lift form, and shows neither
// on a desk that is not halted -- the parity property C9-SPEC.md's own
// report asks for ("the diff is exactly that route"): every OTHER desk's
// page must render exactly as it did before this feature existed.
func TestADeskPageShowsTheHaltBannerOnlyWhenHalted(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	op := h.as(t, "boss", "boss-password-2026")

	reason := "the tagging feed on onprem has been stale for 6 days"
	if _, _, err := crew.ApplyHalt(h.st.DB(), "onprem", reason,
		"data-quality", "owner", "2026-09-01", h.st.AsRecorder()); err != nil {
		t.Fatal(err)
	}

	code, body, _ := op.get(t, "/desk/onprem")
	if code != 200 {
		t.Fatalf("GET /desk/onprem = %d, want 200", code)
	}
	if !strings.Contains(body, "halted") {
		t.Errorf("the halted onprem desk's page does not say so:\n%s", trimForLog(body))
	}
	if !strings.Contains(body, reason) {
		t.Errorf("the halted onprem desk's page does not carry its own reason")
	}
	if !strings.Contains(body, `action="/desk/onprem/lift"`) {
		t.Errorf("the halted onprem desk's page carries no lift form")
	}

	code2, body2, _ := op.get(t, "/desk/aws")
	if code2 != 200 {
		t.Fatalf("GET /desk/aws = %d, want 200", code2)
	}
	if strings.Contains(body2, "action=\"/desk/aws/lift\"") {
		t.Errorf("an UNhalted desk (aws) still carries a lift form")
	}
}

func trimForLog(s string) string {
	if len(s) > 400 {
		return s[:400] + "...(truncated)"
	}
	return s
}

// A viewer may read the desk page but not lift a halt on it.
func TestAViewerCannotLiftAHalt(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	if _, err := h.au.Create("looker", "looker-password-2026", "viewer"); err != nil {
		t.Fatal(err)
	}
	v := h.as(t, "looker", "looker-password-2026")

	if _, _, err := crew.ApplyHalt(h.st.DB(), "azure", "stale feed",
		"data-quality", "owner", "2026-09-01", h.st.AsRecorder()); err != nil {
		t.Fatal(err)
	}

	code, loc := v.post(t, "/desk/azure/lift", url.Values{
		"csrf": {v.csrf(t, "/desk/azure")}, "reason": {"trying anyway"},
	})
	if code != 303 || !strings.Contains(loc, "msg=") {
		t.Errorf("a viewer lifted a halt: %d %s", code, loc)
	}
	if _, found, err := crew.ActiveHalt(h.st.DB(), "azure"); err != nil || !found {
		t.Errorf("ActiveHalt(azure) after a viewer's lift attempt: found=%v err=%v, want still halted",
			found, err)
	}
}

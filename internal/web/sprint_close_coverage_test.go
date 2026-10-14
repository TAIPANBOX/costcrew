package web_test

// Closing a sprint, and refusing a model's plan that no longer validates. A
// sprint with work still open may be closed, and the page says how much was
// left open; a sprint cannot be closed twice; a sprint that does not exist is
// said so.

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestCovCloseSprintSaysWhatWasLeftOpen(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	sprint := decisionSprint(t, h)
	id := strconv.Itoa(sprint)

	code, loc := h.post(t, "/sprint/abc/close", url.Values{"csrf": {h.csrf(t, "/sprints")}})
	if code != http.StatusNotFound {
		t.Errorf("closing a sprint that is not a number: %d %s, want 404", code, loc)
	}

	code, loc = h.post(t, "/sprint/999999/close", url.Values{"csrf": {h.csrf(t, "/sprints")}})
	if code != http.StatusSeeOther || !covHasMsg(loc, "no such sprint") {
		t.Errorf("closing a sprint that does not exist: %d %s", code, loc)
	}

	var open int
	if err := h.st.DB().QueryRow(`SELECT COUNT(*) FROM tasks WHERE sprint=? AND
		state IN ('queued','active','blocked','returned')`, sprint).Scan(&open); err != nil {
		t.Fatal(err)
	}
	code, loc = h.post(t, "/sprint/"+id+"/close", url.Values{"csrf": {h.csrf(t, "/sprints")}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/sprint/"+id) {
		t.Fatalf("closing sprint %s: %d %s", id, code, loc)
	}
	if open > 0 {
		want := "closed, and " + strconv.Itoa(open) + " tasks are still open"
		if !covHasMsg(loc, want) {
			t.Errorf("closing with %d tasks open said %q, want it to say %q", open, loc, want)
		}
	} else if strings.Contains(loc, "msg=") {
		t.Errorf("closing with nothing open said %q, want no message", loc)
	}

	code, loc = h.post(t, "/sprint/"+id+"/close", url.Values{"csrf": {h.csrf(t, "/sprints")}})
	if code != http.StatusSeeOther || !covHasMsg(loc, "that sprint is already closed") {
		t.Errorf("closing a sprint twice: %d %s", code, loc)
	}
}

// The model's plan is approved only if it still validates against today's
// roster. An answer that does not is refused, and nothing is booked.
func TestCovApproveModelPlanRefusesAnAnswerThatNoLongerValidates(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	_, page, _ := h.get(t, "/sprint/plan")
	form := planFormFields(t, page)
	form.Set("csrf", h.csrf(t, "/sprint/plan"))
	form.Set("answer", "")

	before := covSprintCount(t, h)
	code, loc := h.post(t, "/sprint/plan/approve-model", form)
	if code != http.StatusSeeOther || !covHasMsg(loc, "the model's plan no longer re-validates against the current roster; ask again") {
		t.Errorf("an empty model answer: %d %s, want the re-validate refusal", code, loc)
	}
	if after := covSprintCount(t, h); after != before {
		t.Errorf("a refused plan changed the number of sprints from %d to %d", before, after)
	}
}

func covSprintCount(t *testing.T, h *harness) int {
	t.Helper()
	var n int
	if err := h.st.DB().QueryRow(`SELECT COUNT(*) FROM sprints`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

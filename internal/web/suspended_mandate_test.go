package web_test

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// Suspension has to be a rule, not a filter.
//
// Both queues build their dropdown from the active roster, so nobody clicking
// through the console can hand work to a suspended analyst. That is the UI.
// These two post the name anyway, which is what an operator with a stale page
// open does by accident and what anything scripted against the console does by
// default, and they assert the server refuses.
//
// The failure they were written against was real: before the seam existed,
// both posts succeeded. The board then showed a task assigned and active, and
// the anomaly showed triaged and owned, while the analyst's own card said
// suspended with a written reason beside it. Nothing would ever have spent
// money on either, because the live runner refuses to price a task whose
// analyst is suspended, so the damage was not the bill. It was that the two
// screens people read disagreed about who was working, and the one that was
// wrong was the one with the work on it.
func TestSuspensionRefusesNewWorkOnBothQueues(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	const victim = "triage-aws"
	if err := crew.SetState(h.st.DB(), victim, "suspended",
		"pulled off the rota while its allocation rule is re-checked"); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	a := h.anyAnomaly(t)
	path := "/anomalies/" + a.ID
	if _, loc := h.post(t, path+"/assign", url.Values{
		"analyst": {victim}, "csrf": {h.csrf(t, path)},
	}); !strings.Contains(loc, "msg=") {
		got, _ := anomaly.Get(h.st.DB(), a.ID)
		t.Errorf("the anomaly queue accepted a suspended analyst: %s is now %q, handled by %q",
			a.ID, got.State, got.HandledBy)
	}

	tasks, err := crew.Tasks(h.st.DB(), crew.TaskFilter{OpenOnly: true})
	if err != nil || len(tasks) == 0 {
		t.Fatalf("no open task to hand over: %v", err)
	}
	id := tasks[0].ID
	tp := "/task/" + strconv.Itoa(id)
	if _, loc := h.post(t, tp+"/assign", url.Values{
		"analyst": {victim}, "csrf": {h.csrf(t, tp)},
	}); !strings.Contains(loc, "msg=") {
		got, _ := crew.GetTask(h.st.DB(), id)
		t.Errorf("the board accepted a suspended analyst: task %d is now %q, assigned to %q",
			id, got.State, got.Assignee)
	}

	// And the state it does not cover, so the refusal is known to be narrow.
	// An analyst that could be given no work could never come off probation.
	if err := crew.SetState(h.st.DB(), victim, "probation",
		"first-pass rate under the bar for two sprints"); err != nil {
		t.Fatalf("probation: %v", err)
	}
	if _, loc := h.post(t, tp+"/assign", url.Values{
		"analyst": {victim}, "csrf": {h.csrf(t, tp)},
	}); strings.Contains(loc, "msg=") {
		t.Errorf("the board refused an analyst on probation: %s", loc)
	}
}

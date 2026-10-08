package web_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
)

// Invariant 92: the typed hint is shown on the task it was meant to inform,
// the anomaly's panel names typryx's answer id and what was sent, and a row
// where nothing was sent does not say typryx saw anything. Invariant 77 still
// holds: with typryx off, no panel anywhere.

func taskForAnomaly(t *testing.T, h *harness, anomalyID string) string {
	t.Helper()
	plantDraftOnAnomalyTask(t, h, anomalyID, "ai-spend", "ai", "anomaly.explain", "a cause")
	var id int
	if err := h.st.DB().QueryRow(`SELECT id FROM tasks WHERE anomaly=? ORDER BY id DESC LIMIT 1`, anomalyID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return "/task/" + strconv.Itoa(id)
}

func TestTheTaskPageShowsTheHintOnItsAnomaly(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	if err := anomaly.EnsureHintColumns(h.st.DB()); err != nil {
		t.Fatal(err)
	}
	plantAnomalyForTelling(t, h, "A-task-hint", "", 1)
	if err := anomaly.SaveHint(h.st.DB(), "A-task-hint", anomaly.Hint{Class: "runaway_agent", Probability: 0.71,
		Backend: anomaly.BackendJev, AnswerID: "ans-42", LatencyMS: 120, At: "2026-10-07T10:00:00Z",
		FieldsSent: "anomaly,recent_changes", HeldBack: 1}); err != nil {
		t.Fatal(err)
	}
	_, body, _ := h.get(t, taskForAnomaly(t, h, "A-task-hint"))
	for _, want := range []string{"A suggestion from typryx", "<strong>runaway_agent</strong>, probability 0.71",
		"Jev, hosted by TypeSafe AI", `<a href="/anomalies/A-task-hint">anomaly A-task-hint</a>, where what was sent is shown`,
		"never a decision"} {
		if !strings.Contains(body, want) {
			t.Errorf("the task page does not show %q:\n%s", want, fragment(body, "typryx", 900))
		}
	}
}

func TestTheAnomalyPanelNamesTheAnswerAndTheFieldsSent(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	if err := anomaly.EnsureHintColumns(h.st.DB()); err != nil {
		t.Fatal(err)
	}
	plantAnomalyForTelling(t, h, "A-fields", "", 1)
	if err := anomaly.SaveHint(h.st.DB(), "A-fields", anomaly.Hint{Class: "runaway_agent", Probability: 0.71,
		Backend: anomaly.BackendJev, AnswerID: "ans-42", At: "2026-10-07T10:00:00Z",
		FieldsSent: "anomaly,recent_changes", HeldBack: 1}); err != nil {
		t.Fatal(err)
	}
	_, body, _ := h.get(t, "/anomalies/A-fields")
	for _, want := range []string{"<dt>Answer id</dt><dd><code>ans-42</code>", "<code>anomaly</code> <code>recent_changes</code>",
		"<dt>Held back</dt><dd>1 "} {
		if !strings.Contains(body, want) {
			t.Errorf("the anomaly panel does not show %q:\n%s", want, fragment(body, "typryx", 1200))
		}
	}
}

func TestANoHintRowWhereNothingWasSentDoesNotSayTypryxSawFields(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	if err := anomaly.EnsureHintColumns(h.st.DB()); err != nil {
		t.Fatal(err)
	}
	plantAnomalyForTelling(t, h, "A-unsent", "", 1)
	if err := anomaly.SaveHint(h.st.DB(), "A-unsent", anomaly.Hint{Reason: "typryx could not be reached",
		At: "2026-10-07T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	_, body, _ := h.get(t, "/anomalies/A-unsent")
	if strings.Contains(body, "typryx saw only the fields") {
		t.Error("a no-hint row where nothing was sent says typryx saw the template's fields")
	}
	if !strings.Contains(body, "nothing about this anomaly left for typryx") {
		t.Errorf("the panel does not say nothing was sent:\n%s", fragment(body, "typryx", 900))
	}
}

func TestWithTypryxOffTheTaskPageHasNoHintPanel(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	plantAnomalyForTelling(t, h, "A-off", "", 1)
	_, body, _ := h.get(t, taskForAnomaly(t, h, "A-off"))
	if strings.Contains(body, "typryx") {
		t.Errorf("a task page on a console with no typryx mentions typryx:\n%s", fragment(body, "typryx", 600))
	}
}

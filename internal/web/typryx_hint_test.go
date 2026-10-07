package web_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
)

// Invariant 76: the anomaly page shows typryx's hint as a suggestion,
// labelled with the backend that produced it, and invariant 77: without one
// it renders exactly what it rendered before.

func TestTheAnomalyPageShowsTheHintLabelledWithItsSource(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	if err := anomaly.EnsureHintColumns(h.st.DB()); err != nil {
		t.Fatal(err)
	}
	for i, c := range []struct{ backend, label string }{
		{anomaly.BackendJev, "Jev, hosted by TypeSafe AI"},
		{anomaly.BackendOwnModel, "the operator&#39;s own model, through typryx&#39;s openai-logprobs backend"},
		{anomaly.BackendOff, "typed answers are off, and this is not a model&#39;s judgement"},
	} {
		id := "A-hint-" + c.backend
		plantAnomalyForTelling(t, h, id, "", 1)
		if err := anomaly.SaveHint(h.st.DB(), id, anomaly.Hint{Class: "runaway_agent",
			Probability: 0.6 + float64(i)/10, Backend: c.backend, Model: "m-" + c.backend,
			AnswerID: "a", LatencyMS: 231, At: "2026-10-07T10:00:00Z"}); err != nil {
			t.Fatal(err)
		}
		_, body, _ := h.get(t, "/anomalies/"+id)
		for _, want := range []string{
			"A suggestion from typryx", "<strong>runaway_agent</strong>", c.label,
			"backend: " + c.backend, "model: m-" + c.backend, "231 ms",
			"A suggestion, never a decision",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: the page is missing %q:\n%s", c.backend, want, fragment(body, "typryx", 900))
			}
		}
	}
}

func TestTheAnomalyPageSaysWhyThereIsNoHintAndEscapesIt(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	if err := anomaly.EnsureHintColumns(h.st.DB()); err != nil {
		t.Fatal(err)
	}
	plantAnomalyForTelling(t, h, "A-nohint", "", 1)
	if err := anomaly.SaveHint(h.st.DB(), "A-nohint", anomaly.Hint{Reason: "<script>alert(1)</script>"}); err != nil {
		t.Fatal(err)
	}
	_, body, _ := h.get(t, "/anomalies/A-nohint")
	if strings.Contains(body, "<script>alert(1)") {
		t.Error("a stored reason reached the page unescaped")
	}
	if !strings.Contains(body, "No hint") || !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("the page does not say why there is no hint:\n%s", fragment(body, "typryx", 600))
	}
}

// Without typryx the page is byte for byte what it was, and adding the hint
// columns with nothing recorded changes no byte either. The comparison is
// against the same server in the same second, with the CSRF token taken out
// (it is per session, not per page).
func TestWithoutAHintTheAnomalyPageIsByteIdentical(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	plantAnomalyForTelling(t, h, "A-same", "", 2)
	csrf := regexp.MustCompile(`name="csrf" value="[^"]+"`)
	page := func() string {
		_, body, _ := h.get(t, "/anomalies/A-same")
		return csrf.ReplaceAllString(body, `name="csrf" value=""`)
	}
	before := page()
	if strings.Contains(before, "typryx") {
		t.Fatal("a page with no typryx configured mentions typryx")
	}
	if err := anomaly.EnsureHintColumns(h.st.DB()); err != nil {
		t.Fatal(err)
	}
	if after := page(); after != before {
		t.Error("adding the hint columns, with no hint recorded, changed the anomaly page")
	}
}

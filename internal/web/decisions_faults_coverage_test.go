package web_test

// The decision page for one owner of one sprint. Each read it makes can fail,
// and each failure is its own answer to the person who asked: a sprint that
// is not a number, a request whose deliverable has gone, a store that cannot
// be read. None of them may render a page of decisions from what was read
// before the failure.

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// decisionSprint is the id of a sprint the seeded console lists.
func decisionSprint(t *testing.T, h *harness) int {
	t.Helper()
	_, sprints, _ := h.get(t, "/sprints")
	m := covSprintLink.FindStringSubmatch(sprints)
	if m == nil {
		t.Fatal("the seeded console lists no sprint")
	}
	id, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCovDecisionPageSaysWhyItCannotShowADecision(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	sprint := decisionSprint(t, h)

	if code, _, _ := h.get(t, "/sprint/abc/decisions/owner"); code != http.StatusNotFound {
		t.Errorf("a sprint that is not a number: %d, want 404", code)
	}
	code, body, _ := h.get(t, "/sprint/"+strconv.Itoa(sprint)+"/decisions/nobody")
	if code != http.StatusNotFound || !strings.Contains(body, "no decision request on file") {
		t.Errorf("an owner with no request: %d, want 404 saying none is on file", code)
	}
}

// The request points at a deliverable that no longer exists.
func TestCovDecisionPageRefusesARequestWhoseDeliverableIsGone(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	sprint := decisionSprint(t, h)
	if _, err := h.st.DB().Exec(
		`INSERT INTO decision_requests(artifact, sprint, owner) VALUES (999999, ?, 'owner')`,
		sprint); err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.get(t, "/sprint/"+strconv.Itoa(sprint)+"/decisions/owner")
	if code != http.StatusNotFound || !strings.Contains(body, "no such deliverable") {
		t.Errorf("a request on a missing deliverable: %d, want 404 saying no such deliverable", code)
	}
}

// Each read below the request itself fails in its own way. The request is on a
// real deliverable, so the page gets as far as the read under test.
func TestCovDecisionPageAnswersEachBrokenReadAsAServerFault(t *testing.T) {
	cases := []struct {
		name   string
		damage func(t *testing.T, h *harness)
	}{
		{"the request table cannot be read", func(t *testing.T, h *harness) {
			if _, err := h.st.DB().Exec(`DROP TABLE decision_requests`); err != nil {
				t.Fatal(err)
			}
		}},
		{"the carried options cannot be read", func(t *testing.T, h *harness) {
			if _, err := h.st.DB().Exec(`DROP TABLE artifact_options`); err != nil {
				t.Fatal(err)
			}
		}},
		{"the answered options cannot be read", func(t *testing.T, h *harness) {
			covDropColumn(t, h, "artifact_options", "decided_by")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := start(t)
			h.signUp(t, "owner", "owner-password-2026")
			sprint := decisionSprint(t, h)
			var artifact int
			if err := h.st.DB().QueryRow(`SELECT id FROM artifacts ORDER BY id LIMIT 1`).Scan(&artifact); err != nil {
				t.Fatalf("the seeded console has no deliverable to request a decision on: %v", err)
			}
			if _, err := h.st.DB().Exec(
				`INSERT INTO decision_requests(artifact, sprint, owner) VALUES (?, ?, 'owner')`,
				artifact, sprint); err != nil {
				t.Fatal(err)
			}
			c.damage(t, h)

			code, _, _ := h.get(t, "/sprint/"+strconv.Itoa(sprint)+"/decisions/owner")
			if code != http.StatusInternalServerError {
				t.Errorf("%s: the decision page answered %d, want 500", c.name, code)
			}
		})
	}
}

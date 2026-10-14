package web_test

// The inbound links an alert carries: an agent address with no name in it, an
// incident address that names an agent with an anomaly, and one with no name at
// all. Each lands where its own rule says, and a link that names nothing is
// not guessed at.

import (
	"net/http"
	"strings"
	"testing"
)

func TestCovInboundLinksLandWhereTheirRulesSendThem(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	// An agent address that names nobody is refused, not sent to a blank card.
	code, body, _ := h.get(t, "/a/")
	if code != http.StatusNotFound || !strings.Contains(body, "that link carries no agent") {
		t.Errorf("an agent link with no name: %d, want 404 saying it carries no agent", code)
	}

	// An anomaly address for an agent that caused one goes to that anomaly.
	var caused string
	if err := h.st.DB().QueryRow(
		`SELECT caused_by FROM anomalies WHERE caused_by <> '' ORDER BY id LIMIT 1`).Scan(&caused); err != nil {
		t.Fatalf("the seeded console attributes no anomaly to an agent: %v", err)
	}
	code, _, loc := h.get(t, "/i/anomaly:agent://"+caused)
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/anomalies/") {
		t.Errorf("an incident link for %s: %d %s, want the anomaly it caused", caused, code, loc)
	}

	// An incident address with nothing in it goes to the audit trail.
	code, _, loc = h.get(t, "/i/")
	if code != http.StatusSeeOther || loc != "/audit" {
		t.Errorf("an empty incident link: %d %s, want /audit", code, loc)
	}
}

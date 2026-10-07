package web_test

// costcrew#73: the console's own plan-ask is a gateway call too. It is made
// by a person clicking, for the supervisor, so the chain TokenFuse folds an
// owner from is that person as the root and the supervisor as the agent, not
// the roster's owner of the supervisor: the money is spent on this person's
// request.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recordingPlanGateway answers like fakePlanGateway and keeps every request's
// x-fuse-on-behalf-of header.
func recordingPlanGateway(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var chains []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		chains = append(chains, r.Header.Get("x-fuse-on-behalf-of"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "{\"content\":[{\"type\":\"text\",\"text\":\"```plan\\n{\\\"items\\\": []}\\n```\"}],"+
			"\"stop_reason\":\"end_turn\",\"usage\":{\"input_tokens\":40,\"output_tokens\":20}}")
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), chains...)
	}
}

func askAsPlan(t *testing.T, h *harness) {
	t.Helper()
	_, getBody, _ := h.get(t, "/sprint/plan")
	form := planFormFields(t, getBody)
	form.Set("csrf", h.csrf(t, "/sprint/plan"))
	if code, body := postBody(t, h, "/sprint/plan/ask", form); code != http.StatusOK {
		t.Fatalf("POST /sprint/plan/ask = %d, want 200:\n%s", code, trimTo(body, 2000))
	}
}

// The person who asked is the root; the supervisor is the agent. The fixture
// roster is owned by "owner" and the asker is somebody else, so a chain built
// from the roster instead of the session would be caught here.
func TestThePlanAskNamesTheAskingPersonAsTheRoot(t *testing.T) {
	srv, chains := recordingPlanGateway(t)
	h := startWithGateway(t, srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	h.signUp(t, "pat.ng", "pat-password-2026")

	askAsPlan(t, h)

	got := chains()
	if len(got) != 1 {
		t.Fatalf("the gateway saw %d call(s), want 1", len(got))
	}
	want := "user://costcrew.test/pat.ng,agent://costcrew.test/supervisor"
	if got[0] != want {
		t.Errorf("x-fuse-on-behalf-of = %q, want %q", got[0], want)
	}
}

// A username is whatever somebody registered: one with a comma in it still
// gives the gateway exactly two entries and cannot add a second root or a
// forged agent.
func TestAHostileUsernameCannotReshapeThePlanAsksChain(t *testing.T) {
	srv, chains := recordingPlanGateway(t)
	h := startWithGateway(t, srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	h.signUp(t, "owner", "owner-password-2026")

	const hostile = "pat, user://evil.example/boss,agent://evil.example/admin"
	if _, err := h.au.Create(hostile, "pat-password-2026", "admin"); err != nil {
		t.Fatal(err)
	}
	askAsPlan(t, h.as(t, hostile, "pat-password-2026"))

	got := chains()
	if len(got) != 1 {
		t.Fatalf("the gateway saw %d call(s), want 1", len(got))
	}
	parts := strings.Split(got[0], ",")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "user://costcrew.test/") ||
		parts[1] != "agent://costcrew.test/supervisor" || strings.Contains(got[0], "evil.example/boss") {
		t.Errorf("a hostile username reshaped the chain: %q", got[0])
	}
}

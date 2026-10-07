package main

// B6B-SPEC.md: the bench gains the runner's own -gateway flag, the same
// validation, and calls the same internal/deliver.Call every live crew call
// now goes through. HARD RULE this file holds like every other test in this
// package: the only "model" any test here talks to is a local
// httptest.NewServer standing in for the gateway; nothing here ever passes
// -live without also pointing -gateway at one.
//
// Red first, against the unchanged tree: -gateway does not exist as a flag
// yet, and gatewayFor/scoreLive/benchRunID do not exist, so this fails to
// compile the same way call_test.go does in internal/deliver.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/engines"
)

// fakeAnthropicResponse builds a valid Anthropic Messages API response body
// the way the fake gateway in these tests answers with, through
// encoding/json rather than a hand-escaped string literal: a deliverable's
// own text carries newlines and backticks, and a raw string with a regular
// (non-backtick) "\n" inside it embeds an ACTUAL newline byte into JSON,
// which is not valid JSON -- exactly the bug this helper exists to make
// impossible to repeat.
func fakeAnthropicResponse(text string, inTok, outTok int) []byte {
	body := struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}{StopReason: "end_turn"}
	body.Content = []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{{Type: "text", Text: text}}
	body.Usage.InputTokens = inTok
	body.Usage.OutputTokens = outTok
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err) // a fixed literal struct; a marshal failure is a bug in this helper, not in a test
	}
	return raw
}

// -gateway accepts only http(s), and refuses before the store opens: the
// same boundary tools/run's own -gateway holds (TestNormalizeGatewayRefusesANonHTTPURL,
// unmoved, in tools/run/gateway_test.go), now proved here too because
// B6B-SPEC.md section 4 asks for it "in both binaries, with the same
// message."
func TestLiveRefusesANonHTTPGatewayURLBeforeTheStoreOpens(t *testing.T) {
	dir := t.TempDir()
	code, _, errOut := runArgs(t, "-dir", dir, "-live", "-engine", "anthropic", "-gateway", "ftp://localhost:4177")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, errOut)
	}
	if !strings.Contains(errOut, "http(s)") {
		t.Errorf("refusal does not say the gateway must be http(s): %s", errOut)
	}
	// Refused before store.Open ever ran: a fresh -dir stays empty, the same
	// way tools/run's own gatewayURL validation happens before store.Open.
	if _, err := os.Stat(dir + "/app.db"); err == nil {
		t.Error("app.db exists: the store was opened despite the bad -gateway value")
	}
}

// The central case: a live run through a fake gateway sends the exact three
// x-fuse-* headers a live crew call does, and the fake server's own
// received headers are what the report's "Ran it" section pastes verbatim.
// -skill investigate so both of the fixture's two known driver cases run
// (gcp/GKE and onprem/Batch cluster -- invariant 29's own words), which is
// also the mutant catch B6B-SPEC.md section 4 names: "let the bench call
// with an empty gateway" would show up here as a request with none of these
// three headers set.
func TestLiveWithGatewaySendsTheThreeFuseHeaders(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")

	deliverable := "## Mock deliverable\n\nservice: Amazon EKS\nday: 2026-07-14\n" +
		"cause noted.\n\n```options\n" +
		`{"options":[{"class":"anomaly.explain","summary":"noted","risk":"low","needs":"nothing","evidence":["x"]}]}` +
		"\n```\n"

	var received []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = append(received, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeAnthropicResponse(deliverable, 12, 34))
	}))
	defer srv.Close()

	dir := t.TempDir()
	code, out, errOut := runArgs(t, "-dir", dir, "-live", "-skill", "investigate",
		"-engine", "anthropic", "-gateway", srv.URL, "-stack-host", "gcp.taipanbox.local", "-n", "5")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout: %s stderr: %s", code, out, errOut)
	}
	if len(received) != 2 {
		t.Fatalf("the fake gateway received %d request(s), want 2 (both known cases): %s", len(received), out)
	}
	for i, h := range received {
		if got := h.Get("x-fuse-run-id"); got == "" {
			t.Errorf("case %d: x-fuse-run-id is empty", i)
		}
		if got := h.Get("x-fuse-agent-id"); !strings.HasPrefix(got, "agent://gcp.taipanbox.local/") {
			t.Errorf("case %d: x-fuse-agent-id = %q, want it minted under -stack-host's "+
				"trust domain (gcp.taipanbox.local), not the default", i, got)
		}
		if got := h.Get("x-fuse-budget-usd"); got == "" {
			t.Errorf("case %d: x-fuse-budget-usd is empty", i)
		}
	}
	// Both cases share one run id: one bench invocation is one run, the same
	// reading tools/run's own bus.go gives "one run of this binary".
	if received[0].Get("x-fuse-run-id") != received[1].Get("x-fuse-run-id") {
		t.Errorf("the two cases carry different run ids: %q vs %q",
			received[0].Get("x-fuse-run-id"), received[1].Get("x-fuse-run-id"))
	}
	// The two cases are different analysts (investigator-gcp, investigator-onprem):
	// their agent ids must differ, or TokenFuse could not attribute the two
	// calls to two different analysts.
	if received[0].Get("x-fuse-agent-id") == received[1].Get("x-fuse-agent-id") {
		t.Errorf("both cases carry the same agent id %q: the two known cases have "+
			"different analysts and must be attributed separately",
			received[0].Get("x-fuse-agent-id"))
	}
	if !strings.Contains(out, "BENCH  fixture") {
		t.Errorf("a live run does not print the same fixed report header a mock run does:\n%s", out)
	}
}

// -skill triage has only one eligible desk on the fixture's two known cases
// (gcp; select.go's own comment: "the roster has no triage-onprem"), so a
// live run scores exactly one case and calls the fake gateway exactly once
// -- proof the live path reaches selectKnownCases' own eligibility filter
// rather than trying to run every known case regardless of skill.
func TestLiveWithTriageSkillCallsOnlyTheEligibleCase(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeAnthropicResponse("cause noted.", 1, 1))
	}))
	defer srv.Close()

	dir := t.TempDir()
	code, out, errOut := runArgs(t, "-dir", dir, "-live", "-skill", "triage",
		"-engine", "anthropic", "-gateway", srv.URL, "-stack-host", "gcp.taipanbox.local", "-n", "5")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout: %s stderr: %s", code, out, errOut)
	}
	if calls != 1 {
		t.Errorf("the fake gateway was called %d time(s), want exactly 1 (the one "+
			"triage-eligible case)", calls)
	}
}

// Coordinator review of PR #29, 2026-09-03: gatewayFor minted every agent id
// under stack.AgentURI's own default ("", meaning costcrew.local) regardless
// of what trust domain the console this bench stands in for actually runs
// under, so a live bench run's spend would be filed in TokenFuse under an
// agent id the console's own installation would not recognise as itself.
// -stack-host, the runner's own flag (tools/run/main.go), fixes it: required
// whenever -gateway is set, the same pairing tools/run's own openBus already
// holds between -stack-events and -stack-host ("an event minted under a
// trust domain the record plane was not given is refused as foreign").
//
// Red first, against the tree before this fix: -stack-host does not exist as
// a flag, so this fails to compile ("unknown flag" only appears at runtime
// through flag.ContinueOnError, but the flag literal below is unused code
// today) -- confirmed by running this file before gatewayFor/main.go changed:
// runArgs(t, ..., "-stack-host", "example.test") is accepted as an ordinary
// (currently unvalidated) flag with no effect, so TestLiveWithStackHostMintsTheAgentIdUnderIt
// failed on the agent id still reading costcrew.local, and
// TestLiveRefusesWithNoStackHost failed because nothing refused at all
// (exit 0, not 1).
func TestLiveRefusesWithNoStackHost(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	dir := t.TempDir()
	code, _, errOut := runArgs(t, "-dir", dir, "-live", "-skill", "triage",
		"-engine", "anthropic", "-gateway", srv.URL)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, errOut)
	}
	if called {
		t.Error("the fake gateway was called at all; -gateway with no -stack-host must refuse before the store opens")
	}
	for _, want := range []string{"-gateway", "-stack-host"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("refusal does not name %q: %s", want, errOut)
		}
	}
	if _, err := os.Stat(dir + "/app.db"); err == nil {
		t.Error("app.db exists: the store was opened despite -gateway with no -stack-host")
	}
}

// With -stack-host given, the agent id is minted under IT, not under
// AgentURI's own costcrew.local default. -n 5 (both known cases, the
// shuffle order is not this test's concern) and checked by membership
// rather than by which case landed first, matching the coordinator's own
// review, which named agent://example.test/investigator-gcp specifically.
func TestLiveWithStackHostMintsTheAgentIdUnderIt(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	var gotAgentIDs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAgentIDs = append(gotAgentIDs, r.Header.Get("x-fuse-agent-id"))
		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeAnthropicResponse("cause noted.", 1, 1))
	}))
	defer srv.Close()

	dir := t.TempDir()
	code, out, errOut := runArgs(t, "-dir", dir, "-live", "-skill", "investigate",
		"-engine", "anthropic", "-gateway", srv.URL, "-stack-host", "example.test", "-n", "5")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout: %s stderr: %s", code, out, errOut)
	}
	want := "agent://example.test/investigator-gcp"
	found := false
	for _, id := range gotAgentIDs {
		if id == want {
			found = true
		}
		if !strings.HasPrefix(id, "agent://example.test/") {
			t.Errorf("x-fuse-agent-id = %q, not minted under -stack-host's domain", id)
		}
	}
	if !found {
		t.Errorf("no request carried x-fuse-agent-id %q; got %v", want, gotAgentIDs)
	}
}

// gatewayFor in isolation: the one thing that varies case to case is the
// agent id, built the same way tools/run's own gatewayHeadersFor derives one
// (stack.AgentURI), and BudgetUSD/RunID/URL are carried through unchanged.
// Isolated from a live run so a mutant that, say, swapped in the run id
// where the agent id belongs is caught here directly rather than only via
// the end-to-end header test above -- the same "isolate the layer" reasoning
// CLAUDE.md's invariant 26 already gives for wrapWithLimit/refuseUnknownTables.
//
// host is now a real argument (coordinator review of PR #29, 2026-09-03):
// gatewayFor used to hard-code stack.AgentURI's own "" default, which reads
// as costcrew.local regardless of what trust domain the console this bench
// stands in for actually runs under.
func TestGatewayForBuildsThePerCaseGateway(t *testing.T) {
	gw, err := gatewayFor("http://127.0.0.1:4177", "", "bench-9", "gcp.taipanbox.local",
		crew.Analyst{Name: "investigator-gcp", Owner: "bench"}, "0.05")
	if err != nil {
		t.Fatal(err)
	}
	if gw.URL != "http://127.0.0.1:4177" {
		t.Errorf("URL = %q", gw.URL)
	}
	if gw.RunID != "bench-9" {
		t.Errorf("RunID = %q", gw.RunID)
	}
	if gw.BudgetUSD != "0.05" {
		t.Errorf("BudgetUSD = %q", gw.BudgetUSD)
	}
	if want := "agent://gcp.taipanbox.local/investigator-gcp"; gw.AgentID != want {
		t.Errorf("AgentID = %q, want %q", gw.AgentID, want)
	}
}

// An empty -gateway (the flag's own zero value, meaning "off") must build a
// Gateway whose URL is empty too, never a URL guessed from somewhere else --
// the same "empty means off, and off is not an error" rule
// TestNormalizeGatewayEmptyMeansOff already holds for tools/run.
func TestGatewayForWithNoURLBuildsAnOffGateway(t *testing.T) {
	gw, err := gatewayFor("", "", "bench-9", "gcp.taipanbox.local",
		crew.Analyst{Name: "investigator-gcp", Owner: "bench"}, "0.05")
	if err != nil {
		t.Fatal(err)
	}
	if gw.URL != "" {
		t.Errorf("URL = %q, want empty", gw.URL)
	}
}

// An openrouter engine speaks the OpenAI wire, and the bench's -live with only
// the Anthropic-shaped -gateway would reach openrouter.ai directly: the second,
// unmetered path -live exists to refuse. It refuses, before the store opens,
// naming the flag that would give the engine a route.
func TestLiveOpenRouterWithOnlyAnAnthropicGatewayRefusesBeforeTheStoreOpens(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()

	dir := t.TempDir()
	code, _, errOut := runArgs(t, "-dir", dir, "-live", "-skill", "triage",
		"-engine", "openrouter", "-gateway", srv.URL, "-stack-host", "gcp.taipanbox.local")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, errOut)
	}
	if called {
		t.Error("the Anthropic-shaped gateway was called for an openrouter engine")
	}
	for _, want := range []string{"openrouter", "-gateway-openai"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("refusal does not name %q: %s", want, errOut)
		}
	}
	if _, err := os.Stat(dir + "/app.db"); err == nil {
		t.Error("app.db exists: the store was opened before the route was checked")
	}
}

// With the OpenAI-shaped gateway, the bench's live call goes to its
// /v1/chat/completions carrying the same three x-fuse-* headers.
func TestLiveOpenRouterThroughTheOpenAIGatewaySendsTheFuseHeaders(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	var paths []string
	var heads []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		heads = append(heads, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"cause noted."}}],` +
			`"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	code, out, errOut := runArgs(t, "-dir", dir, "-live", "-skill", "triage",
		"-engine", "openrouter", "-gateway-openai", srv.URL, "-stack-host", "gcp.taipanbox.local")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout: %s stderr: %s", code, out, errOut)
	}
	if len(paths) != 1 || paths[0] != "/v1/chat/completions" {
		t.Fatalf("the OpenAI-shaped gateway saw %v, want exactly one /v1/chat/completions", paths)
	}
	h := heads[0]
	for _, k := range []string{"x-fuse-run-id", "x-fuse-agent-id", "x-fuse-budget-usd"} {
		if h.Get(k) == "" {
			t.Errorf("%s is empty on the OpenAI-shaped request", k)
		}
	}
	if got := h.Get("x-fuse-agent-id"); !strings.HasPrefix(got, "agent://gcp.taipanbox.local/") {
		t.Errorf("x-fuse-agent-id = %q, want it under -stack-host's domain", got)
	}
}

// -gateway-openai is validated like -gateway, before the store opens.
func TestLiveRefusesANonHTTPOpenAIGatewayURLBeforeTheStoreOpens(t *testing.T) {
	dir := t.TempDir()
	code, _, errOut := runArgs(t, "-dir", dir, "-live", "-engine", "openrouter", "-gateway-openai", "ftp://localhost:4178")
	if code != 1 || !strings.Contains(errOut, "-gateway-openai") || !strings.Contains(errOut, "http(s)") {
		t.Fatalf("exit %d, stderr %q: want 1 naming -gateway-openai and http(s)", code, errOut)
	}
	if _, err := os.Stat(dir + "/app.db"); err == nil {
		t.Error("app.db exists: the store was opened despite the bad -gateway-openai value")
	}
}

// costcrew#73: a live bench call names the analyst's owner, from the roster,
// the way a crew call does. The fixture roster is seeded under the owner
// "bench" (ensureSeeded), so each case's chain is that root and then its own
// analyst.
func TestLiveSendsTheAnalystsOwnerOnEveryCase(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	var heads []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		heads = append(heads, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeAnthropicResponse("cause noted.", 1, 1))
	}))
	defer srv.Close()

	dir := t.TempDir()
	code, out, errOut := runArgs(t, "-dir", dir, "-live", "-skill", "investigate",
		"-engine", "anthropic", "-gateway", srv.URL, "-stack-host", "gcp.taipanbox.local", "-n", "5")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout: %s stderr: %s", code, out, errOut)
	}
	if len(heads) != 2 {
		t.Fatalf("the fake gateway received %d request(s), want 2", len(heads))
	}
	for i, h := range heads {
		agent := h.Get("x-fuse-agent-id")
		want := "user://gcp.taipanbox.local/bench," + agent
		if got := h.Get("x-fuse-on-behalf-of"); got != want {
			t.Errorf("case %d: x-fuse-on-behalf-of = %q, want %q", i, got, want)
		}
	}
}

// An analyst with no owner refuses the whole run before the first call: the
// case before it is not scored and billed first.
func TestLiveRefusesBeforeAnyCallWhenACasesAnalystHasNoOwner(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	var seen int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen++
		w.Header().Set("Content-Type", "application/json")
		w.Write(fakeAnthropicResponse("cause noted.", 1, 1))
	}))
	defer srv.Close()

	db := seededTestDB(t)
	cases, _, _, err := selectKnownCases(db, "investigate", 5, 1)
	if err != nil || len(cases) != 2 {
		t.Fatalf("selectKnownCases: %d cases, %v; the fixture is expected to hold both known cases", len(cases), err)
	}
	// The SECOND case's analyst loses its owner, so a run that scored case
	// one before reaching case two would already have spent.
	bare := cases[1].Analyst.Name
	if _, err := db.Exec(`UPDATE analysts SET owner = '' WHERE name = ?`, bare); err != nil {
		t.Fatal(err)
	}
	for i := range cases {
		a, err := crew.GetAnalyst(db, cases[i].Analyst.Name)
		if err != nil {
			t.Fatal(err)
		}
		cases[i].Analyst = a
	}

	model := engines.DefaultModel("anthropic")
	p, ok := engines.PriceFor("anthropic", model)
	if !ok {
		t.Fatal("no price for the default anthropic model")
	}
	_, err = scoreLive(db, cases, "anthropic", model, p, 200, srv.URL, "", "gcp.taipanbox.local")
	if err == nil {
		t.Fatal("a live run with an ownerless analyst among its cases was accepted")
	}
	if !strings.Contains(err.Error(), bare) {
		t.Errorf("the refusal does not name the analyst %q: %v", bare, err)
	}
	if seen != 0 {
		t.Errorf("the gateway received %d request(s) before the refusal, want 0", seen)
	}
}

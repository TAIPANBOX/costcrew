package main

// costcrew#73: the crew's owner reaches the control plane.
//
// A crew call carried x-fuse-run-id, x-fuse-agent-id, x-fuse-budget-usd and
// x-fuse-parent-run-id and never x-fuse-on-behalf-of, so TokenFuse's owner
// attribution (the first user:// entry of that chain) had nobody to fold and
// the crew's spend sat under "unassigned". The tool loop's Anthropic round
// also built its headers by hand, a private copy of deliver.SetFuseHeaders
// that had drifted: the last test here holds the two together by recording
// what each actually sends.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/deliver"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

const wantChain = "user://gcp.taipanbox.local/alice,agent://gcp.taipanbox.local/y.mercer"

// Every round of a task on the Anthropic wire carries the owner: a task that
// asks for a tool makes two calls, and both are attributed.
func TestEveryRoundOfAnAnthropicTaskCarriesTheAnalystsOwner(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	srv := newScriptedGateway(t,
		scriptedRound{body: anthropicToolUse}, scriptedRound{body: anthropicAnswer})

	db, task, analyst := runnerDB(t)
	task.Budget = money.Cents(500)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-2026-w41")
	run := &runBudget{ceilingMicros: 5_000_000}
	gw := gatewayConfig{URL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(1000)}
	e := estimate{Task: task, Analyst: analyst, Engine: "anthropic",
		Model: "claude-x", WorstMicros: 1_000, Priced: true}

	var err error
	captureStdout(t, func() { err = execute(context.Background(), db, nil, e, 100, run, b, gw) })
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	reqs := srv.seen()
	if len(reqs) != 2 {
		t.Fatalf("the gateway saw %d request(s), want 2 (a tool round and the answer)", len(reqs))
	}
	for i, r := range reqs {
		if got := r.Header.Get("x-fuse-on-behalf-of"); got != wantChain {
			t.Errorf("round %d x-fuse-on-behalf-of %q, want %q", i+1, got, wantChain)
		}
	}
}

// The same on the OpenAI wire, where the round builder was already on the
// shared header function and the owner has to arrive through it.
func TestEveryRoundOfAnOpenRouterTaskCarriesTheAnalystsOwner(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	srv := newScriptedGateway(t,
		scriptedRound{body: openAIToolUse}, scriptedRound{body: openAIAnswer})

	db, task, analyst := runnerDB(t)
	task.Budget = money.Cents(500)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-2026-w41")
	run := &runBudget{ceilingMicros: 5_000_000}
	gw := gatewayConfig{OpenAIURL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(1000)}
	e := estimate{Task: task, Analyst: analyst, Engine: "openrouter",
		Model: "some/model", WorstMicros: 1_000, Priced: true}

	var err error
	captureStdout(t, func() { err = execute(context.Background(), db, nil, e, 100, run, b, gw) })
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	reqs := srv.seen()
	if len(reqs) != 2 {
		t.Fatalf("the gateway saw %d request(s), want 2", len(reqs))
	}
	for i, r := range reqs {
		if got := r.Header.Get("x-fuse-on-behalf-of"); got != wantChain {
			t.Errorf("round %d x-fuse-on-behalf-of %q, want %q", i+1, got, wantChain)
		}
	}
}

// An owner that is not an ordinary account name cannot add, replace or forge
// a chain entry on the wire: what TokenFuse splits on commas is exactly two
// entries, the first a user:// root.
func TestAHostileOwnerStillGivesTheGatewayExactlyTwoEntries(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	srv := newScriptedGateway(t, scriptedRound{body: anthropicAnswer})

	db, task, analyst := runnerDB(t)
	analyst.Owner = "alice,user://evil.example/boss,agent://evil.example/admin"
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
	run := &runBudget{ceilingMicros: 5_000_000}
	gw := gatewayConfig{URL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(1000)}
	e := estimate{Task: task, Analyst: analyst, Engine: "anthropic",
		Model: "claude-x", WorstMicros: 1_000, Priced: true}

	var err error
	captureStdout(t, func() { err = execute(context.Background(), db, nil, e, 100, run, b, gw) })
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := srv.seen()[0].Header.Get("x-fuse-on-behalf-of")
	parts := strings.Split(got, ",")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "user://gcp.taipanbox.local/") ||
		parts[1] != "agent://gcp.taipanbox.local/y.mercer" || strings.Contains(got, "evil.example/boss") {
		t.Errorf("a hostile owner reshaped the chain on the wire: %q", got)
	}
}

// No owner, no call. The refusal happens before the reservation is taken,
// names the analyst, and nothing reaches the gateway.
func TestAnAnalystWithNoOwnerIsRefusedBeforeAnyCall(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()

	for _, owner := range []string{"", "   "} {
		for _, engine := range []string{"anthropic", "openrouter"} {
			db, task, analyst := runnerDB(t)
			analyst.Owner = owner
			b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")
			run := &runBudget{ceilingMicros: 5_000_000}
			gw := gatewayConfig{URL: srv.URL, OpenAIURL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(1000)}
			e := estimate{Task: task, Analyst: analyst, Engine: engine,
				Model: "m", WorstMicros: 1_000, Priced: true}

			err := execute(context.Background(), db, nil, e, 100, run, b, gw)
			if err == nil {
				t.Errorf("owner %q on %s: a task whose analyst has no owner was run", owner, engine)
				continue
			}
			if !strings.Contains(err.Error(), "y.mercer") {
				t.Errorf("owner %q on %s: the refusal does not name the analyst: %v", owner, engine, err)
			}
			if run.reserved != 0 || run.total() != 0 {
				t.Errorf("owner %q on %s: reserved %d, spent %d after a refused task", owner, engine, run.reserved, run.total())
			}
		}
	}
	if hit {
		t.Error("the gateway was reached by a task whose analyst has no owner")
	}
}

// With a gateway configured, the task of an analyst that has no owner is
// refused when it is priced, before any call, in the words that name the
// analyst: the dry run says so too. The placeholder a roster seeded without
// -stack-owner carries ("unclaimed") is no owner either. Without a gateway
// nothing is sent, so nothing is refused.
func TestWithAGatewayAnOwnerlessAnalystsTaskIsRefusedWhenItIsPriced(t *testing.T) {
	db, task, _ := runnerDB(t)
	gw := gatewayConfig{URL: "http://127.0.0.1:1", Host: "gcp.taipanbox.local"}
	for _, owner := range []string{"", "  ", "unclaimed"} {
		a := crew.Analyst{Name: "y.mercer", Owner: owner, Engine: "anthropic", State: "active"}
		ests := []estimate{price(db, task, a, 100)}
		refuseOwnerless(ests, gw)
		e := ests[0]
		if !e.Refused {
			t.Errorf("owner %q: an ownerless analyst's task was priced to run: %s", owner, e.Verdict)
		}
		if !strings.Contains(e.Verdict, "y.mercer") || !strings.Contains(e.Verdict, "no owner") {
			t.Errorf("owner %q: the verdict %q does not name the analyst and the missing owner", owner, e.Verdict)
		}

		off := []estimate{price(db, task, a, 100)}
		refuseOwnerless(off, gatewayConfig{})
		if strings.Contains(off[0].Verdict, "no owner") {
			t.Errorf("owner %q: with no gateway an analyst was refused for having no owner: %s", owner, off[0].Verdict)
		}
	}
	a := crew.Analyst{Name: "y.mercer", Owner: "alice", Engine: "anthropic", State: "active"}
	ests := []estimate{price(db, task, a, 100)}
	refuseOwnerless(ests, gw)
	if strings.Contains(ests[0].Verdict, "no owner") {
		t.Errorf("an analyst with an owner was refused for having none: %s", ests[0].Verdict)
	}
}

// The run, not only the estimate: spend() skips a refused task, so an
// ownerless analyst's task never reaches the gateway, and the one that has an
// owner still runs.
func TestARunSkipsAnOwnerlessAnalystsTaskAndStillRunsTheOthers(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	srv := newScriptedGateway(t, scriptedRound{body: anthropicAnswer})

	db, tasks, analyst := runnerTasks(t, 2)
	ownerless := analyst
	ownerless.Owner = ""
	ests := []estimate{
		{Task: tasks[0], Analyst: analyst, Engine: "anthropic", Model: "claude-x", WorstMicros: 1_000, Priced: true},
		{Task: tasks[1], Analyst: ownerless, Engine: "anthropic", Model: "claude-x", WorstMicros: 1_000, Priced: true},
	}
	gw := gatewayConfig{URL: srv.URL, Host: "gcp.taipanbox.local", CeilingUSD: money.Cents(1000)}
	refuseOwnerless(ests, gw)
	b, _ := testBus(t, "gcp.taipanbox.local", "crew-1")

	var err error
	captureStdout(t, func() { err = spend(db, nil, ests, 100, money.Cents(1000), 0, b, gw) })
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	if n := len(srv.seen()); n != 1 {
		t.Fatalf("the gateway saw %d request(s), want 1: the ownerless analyst's task must not be sent", n)
	}
}

// recordingGateway answers like the gateway and keeps every request's headers.
type recordingGateway struct {
	*httptest.Server
	mu   sync.Mutex
	seen []http.Header
}

func newRecordingGateway(t *testing.T, body string) *recordingGateway {
	t.Helper()
	g := &recordingGateway{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.seen = append(g.seen, r.Header.Clone())
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(g.Close)
	return g
}

// fuseHeaders flattens every x-fuse-* header of a request to a sorted list of
// "name=value", so two requests compare as sets.
func fuseHeaders(h http.Header) []string {
	var out []string
	for name, vals := range h {
		if strings.HasPrefix(strings.ToLower(name), "x-fuse-") {
			out = append(out, strings.ToLower(name)+"="+strings.Join(vals, "|"))
		}
	}
	sort.Strings(out)
	return out
}

// The tool loop builds its own request and deliver.Call builds its own, and
// they once drifted (the loop's was a hand-written copy of the header
// function). Both are pointed at a recording gateway with the same Gateway
// and must send the identical x-fuse-* set, on both wires.
func TestTheToolLoopAndDeliverCallSendTheSameFuseHeaders(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")

	chain, err := deliver.OnBehalfOfChain("gcp.taipanbox.local", "alice", "y.mercer")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(url, openaiURL string) gatewayHeaders {
		return gatewayHeaders{
			URL: url, OpenAIURL: openaiURL, RunID: "crew-9", AgentID: "agent://gcp.taipanbox.local/y.mercer",
			BudgetUSD: "1.00", ParentRunID: "crew-8", OnBehalfOf: chain,
		}
	}

	t.Run("anthropic", func(t *testing.T) {
		loopGW := newRecordingGateway(t, anthropicAnswer)
		callGW := newRecordingGateway(t, anthropicAnswer)
		if _, _, err := anthropicRound(context.Background(), "claude-x", []anthropicMsg{{Role: "user"}}, nil, 10, mk(loopGW.URL, "")); err != nil {
			t.Fatalf("anthropicRound: %v", err)
		}
		if _, err := deliver.Call(context.Background(), "anthropic", "claude-x", "hi", 10, mk(callGW.URL, "")); err != nil {
			t.Fatalf("deliver.Call: %v", err)
		}
		a, b := fuseHeaders(loopGW.seen[0]), fuseHeaders(callGW.seen[0])
		if strings.Join(a, "\n") != strings.Join(b, "\n") {
			t.Errorf("the tool loop sends\n  %s\nand deliver.Call sends\n  %s", strings.Join(a, "\n  "), strings.Join(b, "\n  "))
		}
		if !strings.Contains(strings.Join(a, " "), "x-fuse-on-behalf-of="+wantChain) {
			t.Errorf("the set the tool loop sends does not carry the owner chain: %v", a)
		}
	})

	t.Run("openai", func(t *testing.T) {
		loopGW := newRecordingGateway(t, openAIAnswer)
		callGW := newRecordingGateway(t, openAIAnswer)
		if _, _, err := openRouterRound(context.Background(), "some/model", []openAIMsg{{Role: "user"}}, nil, 10, mk("", loopGW.URL)); err != nil {
			t.Fatalf("openRouterRound: %v", err)
		}
		if _, err := deliver.Call(context.Background(), "openrouter", "some/model", "hi", 10, mk("", callGW.URL)); err != nil {
			t.Fatalf("deliver.Call: %v", err)
		}
		a, b := fuseHeaders(loopGW.seen[0]), fuseHeaders(callGW.seen[0])
		if strings.Join(a, "\n") != strings.Join(b, "\n") {
			t.Errorf("the tool loop sends\n  %s\nand deliver.Call sends\n  %s", strings.Join(a, "\n  "), strings.Join(b, "\n  "))
		}
		if !strings.Contains(strings.Join(a, " "), "x-fuse-on-behalf-of="+wantChain) {
			t.Errorf("the set the tool loop sends does not carry the owner chain: %v", a)
		}
	})
}

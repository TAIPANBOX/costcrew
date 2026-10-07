package deliver

// costcrew#73: every gateway call names the person it is for.
//
// The chain these tests pin is the one TokenFuse's own parser reads
// (crates/gateway/src/proxy.rs on_behalf_of_header and
// crates/gateway/src/chainproof.rs declared_chain): one header value,
// comma-separated, root first, entries trimmed and empty ones dropped, at most
// 32 entries and at most 4096 bytes before the whole header is ignored
// without an error. Its owner fold takes the first user:// entry.
// declaredChainLikeTokenFuse below is that parser in Go, so a chain this
// package builds is judged by what the receiving side would make of it.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// declaredChainLikeTokenFuse is chainproof::declared_chain plus the byte cap
// of proxy.rs's on_behalf_of_header: ok is false when TokenFuse would ignore
// the header (over 4096 bytes) or refuse it (over 32 entries).
func declaredChainLikeTokenFuse(raw string) (chain []string, ok bool) {
	if len(raw) > 4096 {
		return nil, false
	}
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			chain = append(chain, p)
		}
	}
	if len(chain) > 32 {
		return nil, false
	}
	return chain, true
}

// ownerOfChainLikeTokenFuse is cloud/store.rs owner_of_chain: the first
// user:// entry, if it names somebody.
func ownerOfChainLikeTokenFuse(chain []string) string {
	for _, p := range chain {
		if strings.HasPrefix(p, "user://") && len(p) > len("user://") {
			return p
		}
	}
	return ""
}

func TestTheChainIsTheOwnersUserRootThenTheAnalystsAgent(t *testing.T) {
	got, err := OnBehalfOfChain("acme.example", "alice", "y.mercer")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"user://acme.example/alice", "agent://acme.example/y.mercer"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("chain = %v, want %v", got, want)
	}
	// An empty host reads as the installation default, the way AgentURI does,
	// so the root and the actor can never name two trust domains.
	got, err = OnBehalfOfChain("", "alice", "y.mercer")
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "user://costcrew.local/alice" || got[1] != "agent://costcrew.local/y.mercer" {
		t.Errorf("default-host chain = %v", got)
	}
}

func TestAnAnalystWithNoOwnerGetsNoChainAndIsNamed(t *testing.T) {
	for _, owner := range []string{"", " ", "\t\n"} {
		chain, err := OnBehalfOfChain("acme.example", owner, "y.mercer")
		if err == nil {
			t.Fatalf("owner %q was accepted and produced %v: an empty or partial chain is exactly what must never be sent", owner, chain)
		}
		if chain != nil {
			t.Errorf("owner %q: a chain came back beside the error: %v", owner, chain)
		}
		if !strings.Contains(err.Error(), "y.mercer") {
			t.Errorf("owner %q: the refusal does not name the analyst: %v", owner, err)
		}
	}
}

// An owner is an account name nobody validated for this purpose, so it is
// input. Whatever it holds, the chain on the wire is exactly two entries, the
// first one a user:// root that decodes back to the owner, and the analyst's
// agent is not displaced, duplicated or forged.
func TestHostileOwnersCannotForgeOrBreakAChain(t *testing.T) {
	for _, owner := range []string{
		"alice,agent://evil.example/admin",
		"alice, user://evil.example/boss",
		"alice\r\nx-fuse-agent-id: agent://evil/x",
		"alice\x00bob",
		"zoë",
		"John Smith",
		"a/b/../c",
		"alice;bob?x=1#f",
		"%2C",
		"user://other/root",
		",,,",
	} {
		chain, err := OnBehalfOfChain("acme.example", owner, "y.mercer")
		if err != nil {
			// A refusal is an acceptable answer to a hostile owner; a chain
			// that is wrong is not. ",,," trims to a name made only of
			// separators, which is escaped rather than refused, so the error
			// path is only for the empty owner.
			t.Errorf("owner %q was refused: %v", owner, err)
			continue
		}
		header := strings.Join(chain, ",")
		parsed, ok := declaredChainLikeTokenFuse(header)
		if !ok || len(parsed) != 2 {
			t.Errorf("owner %q: TokenFuse would read %d entries (ok=%v) from %q, want 2", owner, len(parsed), ok, header)
			continue
		}
		root := ownerOfChainLikeTokenFuse(parsed)
		if root != parsed[0] || !strings.HasPrefix(root, "user://acme.example/") {
			t.Errorf("owner %q: the owner TokenFuse folds is %q, want the user:// root %q", owner, root, parsed[0])
		}
		back, uerr := url.PathUnescape(strings.TrimPrefix(root, "user://acme.example/"))
		if uerr != nil || back != strings.TrimSpace(owner) {
			t.Errorf("owner %q: the root %q decodes to %q (%v), want the owner back", owner, root, back, uerr)
		}
		if parsed[1] != "agent://acme.example/y.mercer" {
			t.Errorf("owner %q: the actor became %q", owner, parsed[1])
		}
		for _, r := range header {
			if r < 0x21 && r != ' ' || r > 0x7e {
				t.Errorf("owner %q: the header %q carries a byte TokenFuse's header reader would drop (%U)", owner, header, r)
				break
			}
		}
	}
}

// The receiving side ignores a header over 4096 bytes without a word. A chain
// that long is refused here, where it can be said out loud, rather than sent
// to be dropped.
func TestAChainTheGatewayWouldSilentlyIgnoreIsRefusedHere(t *testing.T) {
	long := strings.Repeat("a", 4100)
	if chain, err := OnBehalfOfChain("acme.example", long, "y.mercer"); err == nil {
		t.Fatalf("a %d-byte owner produced a chain of %d bytes that TokenFuse ignores", len(long), len(strings.Join(chain, ",")))
	}
	// And the longest that still fits is accepted, so the cap is not off by a
	// wide margin in the safe direction either.
	fits := strings.Repeat("a", 4000)
	chain, err := OnBehalfOfChain("acme.example", fits, "y.mercer")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := declaredChainLikeTokenFuse(strings.Join(chain, ",")); !ok {
		t.Error("the accepted chain is one TokenFuse ignores")
	}
}

// Both the Anthropic request and the OpenAI-shaped one carry the header, from
// the one function that sets every x-fuse-* header.
func TestEveryRequestShapeCarriesTheOnBehalfOfChain(t *testing.T) {
	chain, err := OnBehalfOfChain("acme.example", "alice", "y.mercer")
	if err != nil {
		t.Fatal(err)
	}
	gw := Gateway{
		URL: "http://127.0.0.1:1", OpenAIURL: "http://127.0.0.1:2",
		RunID: "crew-9", AgentID: "agent://acme.example/y.mercer", BudgetUSD: "1.00", OnBehalfOf: chain,
	}
	ant, err := anthropicRequest(context.Background(), "k", "m", "p", 10, gw)
	if err != nil {
		t.Fatal(err)
	}
	oai, err := openRouterRequest(context.Background(), "k", "m", "p", 10, gw)
	if err != nil {
		t.Fatal(err)
	}
	want := "user://acme.example/alice,agent://acme.example/y.mercer"
	for name, req := range map[string]*http.Request{"anthropic": ant, "openai": oai} {
		if got := req.Header.Get("x-fuse-on-behalf-of"); got != want {
			t.Errorf("%s request x-fuse-on-behalf-of = %q, want %q", name, got, want)
		}
	}
}

// RequireIdentity is the boundary every gateway call passes: a gateway call
// with a run and an agent but no chain is refused before a request exists,
// on both wires, and the refusal names the agent so the person knows whose
// owner is missing.
func TestAGatewayCallWithNoOwnerChainIsRefusedBeforeAnyRequest(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stub-not-real")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-stub-not-real")
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()

	for _, c := range []struct {
		engine string
		gw     Gateway
	}{
		{"anthropic", Gateway{URL: srv.URL, RunID: "crew-1", AgentID: "agent://x/y.mercer", BudgetUSD: "1.00"}},
		{"openrouter", Gateway{OpenAIURL: srv.URL, RunID: "crew-1", AgentID: "agent://x/y.mercer", BudgetUSD: "1.00"}},
	} {
		_, err := Call(context.Background(), c.engine, "m", "hello", 10, c.gw)
		if err == nil {
			t.Errorf("%s: a gateway call with no owner chain was made", c.engine)
			continue
		}
		if !strings.Contains(err.Error(), "agent://x/y.mercer") || !strings.Contains(err.Error(), "owner") {
			t.Errorf("%s: the refusal does not name the agent and the missing owner: %v", c.engine, err)
		}
	}
	if hit {
		t.Error("the gateway was reached by a call that names nobody")
	}
}

// With no gateway nothing is asked for: the direct route is as it was, and
// carries no x-fuse-* header at all.
func TestNoGatewayNeedsNoOwnerChain(t *testing.T) {
	req, err := anthropicRequest(context.Background(), "k", "m", "p", 10, Gateway{})
	if err != nil {
		t.Fatal(err)
	}
	if v := req.Header.Get("x-fuse-on-behalf-of"); v != "" {
		t.Errorf("a direct request carries x-fuse-on-behalf-of %q", v)
	}
	if err := RequireIdentity(Gateway{}, false); err != nil {
		t.Errorf("RequireIdentity refused a call that is not going to a gateway: %v", err)
	}
}

// testChain is the chain the older gateway tests in this package send; they
// are about other things (settlement, refusals, routing) and only need a
// call that names its owner to get past RequireIdentity.
var testChain = []string{"user://x/alice", "agent://x/y.mercer"}

// "unclaimed" is the placeholder a roster seeded without -stack-owner carries
// on every agent. It is a word, not a person, and it must not reach the
// control plane as one; a real account that happens to be called that is
// still a person when it is the console asking (OnBehalfOfChain), only the
// roster's own owner field is read through the placeholder rule.
func TestAnUnclaimedRosterOwnerIsNoOwner(t *testing.T) {
	a := crew.Analyst{Name: "y.mercer", Owner: "unclaimed"}
	if chain, err := AnalystOnBehalfOf("acme.example", a); err == nil {
		t.Fatalf("the placeholder owner produced a chain: %v", chain)
	} else if !strings.Contains(err.Error(), "y.mercer") {
		t.Errorf("the refusal does not name the analyst: %v", err)
	}
	a.Owner = " alice "
	chain, err := AnalystOnBehalfOf("acme.example", a)
	if err != nil || chain[0] != "user://acme.example/alice" {
		t.Errorf("a real owner was refused or mangled: %v, %v", chain, err)
	}
	if chain, err := OnBehalfOfChain("acme.example", "unclaimed", "supervisor"); err != nil || chain[0] != "user://acme.example/unclaimed" {
		t.Errorf("an account that is actually named that is a person when it is the asker: %v, %v", chain, err)
	}
}

package stack_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	asgevent "github.com/TAIPANBOX/agent-stack-go/event"
	asgpassport "github.com/TAIPANBOX/agent-stack-go/passport"

	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/stack"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

func open(t *testing.T) (*stack.Emitter, string, string) {
	t.Helper()
	dir := t.TempDir()
	events := filepath.Join(dir, "events", "costcrew.ndjson")
	pass := filepath.Join(dir, "passports")
	em, err := stack.Open(stack.Config{
		EventsPath: events, PassportDir: pass,
		Host: "costcrew.test", Owner: "finops", Attestation: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { em.Close() })
	return em, events, pass
}

// Off by default, and off means silent. A product that starts writing into a
// shared event stream without being asked is one nobody installs twice.
func TestOffByDefaultAndSilent(t *testing.T) {
	em, err := stack.Open(stack.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if em.On() {
		t.Fatal("the emitter is on with no path configured")
	}
	if err := em.Emit("anomaly_detected", "detector", "high", nil, nil); err != nil {
		t.Fatalf("emitting with the stack off returned an error: %v", err)
	}
	if n, err := em.WritePassports(world.Crew); err != nil || n != 0 {
		t.Fatalf("passports written with no directory configured: %d %v", n, err)
	}
}

// The chain is verified by the CONTRACT's own verifier, not by anything in
// this repository. A chain this product believes in and the stack does not is
// no chain at all.
func TestTheEventStreamVerifiesWithTheContractsOwnChecker(t *testing.T) {
	em, path, _ := open(t)
	for i := 0; i < 25; i++ {
		if err := em.Emit("anomaly_detected", "detector", "medium",
			map[string]any{"anomaly": "A-0000000000", "n": i}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := em.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rep, err := asgevent.VerifyChain(f)
	if err != nil {
		t.Fatalf("the contract's verifier could not read the stream: %v", err)
	}
	if len(rep.Breaks) != 0 {
		t.Fatalf("the verifier found %d break(s): %+v", len(rep.Breaks), rep.Breaks)
	}
	if rep.Chained < 20 {
		t.Errorf("only %d events were chained out of 25", rep.Chained)
	}
}

// Every event has to satisfy the envelope's required fields, or the identity
// graph drops it on ingest and this product never finds out.
func TestEveryEventIsAValidEnvelope(t *testing.T) {
	em, path, _ := open(t)
	if err := em.Emit("anomaly_dismissed", "triage-aws", "low",
		map[string]any{"reason": "known load test"},
		em.Delegation("yurii", "triage-aws")); err != nil {
		t.Fatal(err)
	}
	em.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(raw))
	ev, err := asgevent.Unmarshal([]byte(line))
	if err != nil {
		t.Fatalf("the contract refused an event this product wrote: %v", err)
	}
	if ev.Source != stack.Source {
		t.Errorf("source %q", ev.Source)
	}
	if ev.AgentID != "agent://costcrew.test/triage-aws" {
		t.Errorf("agent id %q", ev.AgentID)
	}
	if len(ev.OnBehalfOf) == 0 {
		t.Error("no delegation chain, so nobody can tell who asked for this")
	}
	// The actor is not repeated in its own chain: it is already the agent_id,
	// and repeating it claims an agent acted on its own behalf.
	for _, p := range ev.OnBehalfOf {
		if p == ev.AgentID {
			t.Errorf("the actor %q appears in its own delegation chain", p)
		}
	}
}

func TestDelegationIsRootFirst(t *testing.T) {
	em, _, _ := open(t)
	chain := em.Delegation("yurii", "reporter-gcp")
	if len(chain) != 2 {
		t.Fatalf("chain %v", chain)
	}
	if !strings.HasPrefix(chain[0], "user://") {
		t.Errorf("the chain does not start with the human who asked: %v", chain)
	}
	if chain[1] != "agent://costcrew.test/supervisor" {
		t.Errorf("the supervisor is not the second link: %v", chain)
	}
	// The supervisor does not act on behalf of itself.
	if sup := em.Delegation("yurii", "supervisor"); len(sup) != 1 {
		t.Errorf("the supervisor's own chain is %v", sup)
	}
}

// Severity decides whether a person is interrupted, so it follows money and
// not how many deviations out something sits.
func TestSeverityFollowsMoney(t *testing.T) {
	cases := []struct {
		amount money.Cents
		want   string
	}{
		{money.Cents(10_00), "low"},
		{money.Cents(-10_00), "low"}, // a fall of the same size is as loud
		{money.Cents(300_00), "medium"},
		{money.Cents(2_000_00), "high"},
		{money.Cents(9_000_00), "critical"},
		{money.Cents(-9_000_00), "critical"},
	}
	for _, c := range cases {
		if got := stack.Severity(c.amount); got != c.want {
			t.Errorf("Severity(%s) = %q, want %q", c.amount, got, c.want)
		}
	}
}

// Passports are validated by the contract's own parser, which is the same
// check the identity graph runs on ingest.
func TestPassportsAreValidAndComplete(t *testing.T) {
	em, _, dir := open(t)
	n, err := em.WritePassports(world.Crew)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(world.Crew) {
		t.Fatalf("%d passports for %d analysts", n, len(world.Crew))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(world.Crew) {
		t.Fatalf("%d files on disk for %d analysts", len(entries), len(world.Crew))
	}

	var sawParent, sawReason bool
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		p, err := asgpassport.Parse(raw)
		if err != nil {
			t.Errorf("%s: the contract refused it: %v", e.Name(), err)
			continue
		}
		if p.Owner == "" {
			t.Errorf("%s has no owner", e.Name())
		}
		if p.Attestation == nil || p.Attestation.Method != "none" {
			t.Errorf("%s: attestation %+v; it must say plainly that the id is unproven",
				e.Name(), p.Attestation)
		}
		if strings.HasSuffix(e.Name(), "supervisor.json") {
			if p.Parent != "" {
				t.Errorf("the supervisor has a parent: %q", p.Parent)
			}
		} else if p.Parent != "" {
			sawParent = true
		}
		if p.Labels["reason"] != "" {
			sawReason = true
		}
		// An empty label is worse than an absent one: it renders as a field
		// somebody forgot to fill rather than one that does not apply.
		for k, v := range p.Labels {
			if strings.TrimSpace(v) == "" {
				t.Errorf("%s: label %q is empty", e.Name(), k)
			}
		}
	}
	if !sawParent {
		t.Error("no analyst has a parent, so the delegation the stack reads is absent")
	}
	if !sawReason {
		t.Error("no suspended analyst carried its reason onto its passport, so the " +
			"identity graph has to ask the console why an agent is off the rota")
	}
}

// Writing passports without an owner must fail loudly rather than produce
// documents the graph will reject one at a time, later, somewhere else.
func TestPassportsRefuseAnEmptyOwner(t *testing.T) {
	em, err := stack.Open(stack.Config{
		EventsPath:  filepath.Join(t.TempDir(), "e.ndjson"),
		PassportDir: filepath.Join(t.TempDir(), "p"),
		Host:        "costcrew.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer em.Close()
	if _, err := em.WritePassports(world.Crew); err == nil {
		t.Fatal("passports were written with no owner")
	}
}

// Re-publishing must be safe: the same crew produces the same documents, so a
// restart does not churn a directory somebody else is watching.
func TestRepublishingIsStable(t *testing.T) {
	em, _, dir := open(t)
	if _, err := em.WritePassports(world.Crew); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(dir, "supervisor.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := em.WritePassports(world.Crew); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(dir, "supervisor.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("republishing changed a passport that nothing about the crew changed")
	}
	var doc map[string]any
	if err := json.Unmarshal(first, &doc); err != nil {
		t.Fatalf("the passport is not valid JSON: %v", err)
	}
}

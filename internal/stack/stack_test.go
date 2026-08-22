package stack_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	asgevent "github.com/TAIPANBOX/agent-stack-go/event"
	asgpassport "github.com/TAIPANBOX/agent-stack-go/passport"

	"github.com/TAIPANBOX/agent-stack-go/passport"

	"github.com/TAIPANBOX/costcrew/internal/crew"
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
	if n, err := em.WritePassports(rosterOf(world.Crew)); err != nil || n != 0 {
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
	n, err := em.WritePassports(rosterOf(world.Crew))
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
	if _, err := em.WritePassports(rosterOf(world.Crew)); err == nil {
		t.Fatal("passports were written with no owner")
	}
}

// Re-publishing must be safe: the same crew produces the same documents, so a
// restart does not churn a directory somebody else is watching.
func TestRepublishingIsStable(t *testing.T) {
	em, _, dir := open(t)
	if _, err := em.WritePassports(rosterOf(world.Crew)); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(dir, "supervisor.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := em.WritePassports(rosterOf(world.Crew)); err != nil {
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

// What was decided at hire time is what the passport says.
//
// The document is a statement about THIS agent. An analyst hired under a
// spiffe-svid and routed under a named parent must not have either replaced
// by whatever the server happened to be started with, because the identity
// graph downstream has no other record of the decision and would read the
// server's default as the agent's own claim.
func TestThePassportCarriesWhatWasDecidedAtHireTime(t *testing.T) {
	dir := t.TempDir()
	em, err := stack.Open(stack.Config{
		EventsPath: filepath.Join(dir, "e.ndjson"), PassportDir: filepath.Join(dir, "p"),
		Host: "costcrew.local", Owner: "yurii", Attestation: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer em.Close()

	a := crew.Analyst{
		Name: "night-desk", Role: "after-hours variance", Desk: "aws",
		Engine: "local-llama", State: "active",
		Skills: []string{"anomaly-triage"}, Rights: []string{"figures-read"},
		PerTask: money.Cents(1500), Monthly: money.Cents(40000),
		Cadence: "daily", Audience: "the desk",
		Owner: "yurii", Parent: "capacity-aws", Attestation: "spiffe-svid",
		Hired: "2026-08-22",
	}
	if _, err := em.WritePassports([]crew.Analyst{a}); err != nil {
		t.Fatal(err)
	}
	buf, err := os.ReadFile(filepath.Join(dir, "p", "night-desk.json"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := passport.Parse(buf)
	if err != nil {
		t.Fatalf("the document is not a valid Passport: %v", err)
	}
	if p.Attestation == nil || p.Attestation.Method != "spiffe-svid" {
		t.Errorf("attestation: hired with spiffe-svid, passport says %v", p.Attestation)
	}
	if want := "agent://costcrew.local/capacity-aws"; p.Parent != want {
		t.Errorf("parent: hired under capacity-aws, passport says %q, want %q", p.Parent, want)
	}
	for k, want := range map[string]string{
		"rights": "figures-read", "cadence": "daily", "hired_by": "yurii",
	} {
		if p.Labels[k] != want {
			t.Errorf("label %s: got %q, want %q", k, p.Labels[k], want)
		}
	}
}

// rosterOf turns the fixture crew into roster records, so the tests that were
// written against the fixture keep testing the same agents.
func rosterOf(in []world.Agent) []crew.Analyst {
	out := make([]crew.Analyst, 0, len(in))
	for _, a := range in {
		out = append(out, crew.Analyst{
			Name: a.Name, Role: a.Role, Desk: a.Desk, Engine: a.Engine,
			State: string(a.State), Reason: a.Reason, Skills: a.Skills,
			PerTask: mustCents(a.PerTaskUSD), Monthly: mustCents(a.MonthlyUSD),
		})
	}
	return out
}

func mustCents(s string) money.Cents {
	c, _ := money.Parse(s)
	return c
}

// The passport's owner is the account that answers for the agent.
//
// It carried the installation's own owner flag, so the document said one name
// while the card's "hired by" said another, six lines apart. A transfer of
// ownership has to show up in the document, or the identity graph downstream
// keeps naming somebody who handed the agent on months ago.
func TestThePassportNamesWhoAnswersForTheAgent(t *testing.T) {
	dir := t.TempDir()
	em, err := stack.Open(stack.Config{
		EventsPath: filepath.Join(dir, "e.ndjson"), PassportDir: filepath.Join(dir, "p"),
		Host: "costcrew.local", Owner: "the-installation", Attestation: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer em.Close()

	a := crew.Analyst{Name: "night-desk", Role: "night watch", Desk: "aws",
		State: "active", Owner: "tania", Hired: "2026-08-22"}
	if got := em.PassportFor(a).Owner; got != "tania" {
		t.Errorf("the passport says %q answers for it; the roster says tania hired it", got)
	}
	// Handed on. The document has to follow.
	a.Owner = "yurii"
	if got := em.PassportFor(a).Owner; got != "yurii" {
		t.Errorf("after a transfer the passport still says %q", got)
	}
	// And with nobody recorded, it falls back to the installation rather than
	// publishing a document with an empty owner, which the contract refuses.
	a.Owner = ""
	if got := em.PassportFor(a).Owner; got != "the-installation" {
		t.Errorf("with no owner recorded the passport says %q", got)
	}
}

// The estate has a word for this, and it is the one that goes on the wire.
//
// Both downstream services read the same vocabulary. This console invented its
// own, and the cost was measured rather than argued: trailryx read all
// sixty-nine lines, accepted the envelope, the schema, the agent id and the
// trust domain, and refused every one with "an event type this reading does
// not map"; heraldyx composed real mail that had to say "an event this build
// does not have a description for".
func TestADetectedAnomalyGoesOutAsASpendSpike(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "e.ndjson")
	em, err := stack.Open(stack.Config{EventsPath: path, Host: "costcrew.local", Owner: "yurii"})
	if err != nil {
		t.Fatal(err)
	}
	if err := em.Emit("anomaly_detected", "detector", "high", map[string]any{
		"anomaly": "A-27019dfc1208", "service": "OpenRouter", "excess": "348.05",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := em.Close(); err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(strings.SplitN(string(buf), "\n", 2)[0]), &ev); err != nil {
		t.Fatal(err)
	}

	if ev["type"] != "spend_spike" {
		t.Errorf("a detected anomaly went out as %q; the estate's word for it is spend_spike", ev["type"])
	}
	// And nothing is lost: which of this console's decisions produced it has
	// to survive, or an auditor reading the record has to know that this
	// console renames things on the way out.
	data, _ := ev["data"].(map[string]any)
	if data["costcrew_type"] != "anomaly_detected" {
		t.Errorf("the original type is not in the payload: %v", data["costcrew_type"])
	}
	// A run id, because a record belonging to no execution cannot be asked
	// for, and the shape is the contract's: lowercase, no / or :, 64 bytes.
	run, _ := ev["run_id"].(string)
	if run == "" {
		t.Fatal("no run id, which is nine records trailryx will refuse")
	}
	if run != strings.ToLower(run) {
		t.Errorf("run id %q has a capital in it; the contract's character set is [a-z0-9._-] "+
			"and this console's anomaly ids start with a capital A", run)
	}
	for _, bad := range []string{"/", ":", " "} {
		if strings.Contains(run, bad) {
			t.Errorf("run id %q contains %q, which the contract does not allow", run, bad)
		}
	}
	if len(run) > 64 {
		t.Errorf("run id is %d bytes, the contract allows 64", len(run))
	}
}

// What is deliberately NOT translated stays untranslated.
//
// The shared vocabulary is about a run. Hiring an agent, planning a sprint and
// triaging a finding are about a practice, and forcing them into a runtime
// word would be a false claim in a record somebody audits. The downstream
// refusing them is the correct outcome, not a gap.
func TestAPracticeEventKeepsItsOwnName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "e.ndjson")
	em, err := stack.Open(stack.Config{EventsPath: path, Host: "costcrew.local", Owner: "yurii"})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"agent_hired", "sprint_planned", "anomaly_triaged"} {
		if err := em.Emit(kind, "supervisor", "info", map[string]any{"analyst": "triage-aws"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := em.Close(); err != nil {
		t.Fatal(err)
	}
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(strings.TrimSpace(string(buf)), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		want := []string{"agent_hired", "sprint_planned", "anomaly_triaged"}[i]
		if ev["type"] != want {
			t.Errorf("%s went out as %q; it has no equivalent in a runtime vocabulary "+
				"and inventing one would be a claim nobody made", want, ev["type"])
		}
		if ev["run_id"] != nil && ev["run_id"] != "" {
			t.Errorf("%s carries a run id %q; it did not happen inside a run",
				want, ev["run_id"])
		}
	}
}

// budget_exhausted is never emitted, because this console refuses nothing.
//
// It maps, downstream, to a DENIED verdict. Emitting it would put a refusal
// that never happened into a tamper-evident record, which is a worse failure
// than not recording the event at all.
func TestTheConsoleNeverClaimsToHaveDeniedAnything(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "e.ndjson")
	em, err := stack.Open(stack.Config{EventsPath: path, Host: "costcrew.local", Owner: "yurii"})
	if err != nil {
		t.Fatal(err)
	}
	if err := em.Emit("guard_passed", "triage-aws", "high", map[string]any{
		"analyst": "triage-aws", "month": "2026-08", "over": "40.00",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := em.Close(); err != nil {
		t.Fatal(err)
	}
	buf, _ := os.ReadFile(path)
	var ev map[string]any
	_ = json.Unmarshal([]byte(strings.SplitN(string(buf), "\n", 2)[0]), &ev)
	if ev["type"] != "budget_threshold" {
		t.Errorf("going past a guard went out as %q, wanted budget_threshold", ev["type"])
	}
	if strings.Contains(string(buf), "budget_exhausted") {
		t.Error("the stream claims budget_exhausted, which downstream reads as a denial. " +
			"This console records the guard and does not enforce it.")
	}
	if data, _ := ev["data"].(map[string]any); data["enforced"] != false {
		t.Errorf("the event does not say the guard was not enforced: %v", data["enforced"])
	}
}

// An attested runtime lends its identity to the agents inside it, and says so.
//
// Thirty-nine analysts run in one process. A workload attestor checks the user,
// the binary's path and its SHA-256, so it cannot tell triage-aws from
// forecaster: at the level it looks at, they are the same process. What the
// passport can therefore say truthfully is "this agent runs inside a workload
// whose identity was attested, and here is that workload's SPIFFE ID", and the
// labels have to say that it is the RUNTIME's identity rather than letting
// thirty-nine passports imply thirty-nine SVIDs.
func TestAnAttestedRuntimeLendsItsIdentityAndSaysSo(t *testing.T) {
	dir := t.TempDir()
	em, err := stack.Open(stack.Config{
		EventsPath: filepath.Join(dir, "e.ndjson"), Host: "costcrew.local", Owner: "yurii",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer em.Close()

	a := crew.Analyst{Name: "triage-aws", Role: "triage", Desk: "aws", State: "active",
		Owner: "yurii", Attestation: "none"}

	// Unattested process: the passport says none, and an identity graph is
	// right to flag it.
	if got := em.PassportFor(a); got.Attestation.Method != "none" {
		t.Errorf("with no SVID the passport claims %q", got.Attestation.Method)
	}

	em.SetRuntimeIdentity("spiffe://costcrew.local/console")
	p := em.PassportFor(a)
	if p.Attestation.Method != "spiffe-svid" {
		t.Errorf("inside an attested runtime the passport says %q", p.Attestation.Method)
	}
	if p.Attestation.Detail != "spiffe://costcrew.local/console" {
		t.Errorf("the detail names %q, not the workload it runs in", p.Attestation.Detail)
	}
	if p.Labels["attested"] != "the runtime, not this agent" {
		t.Errorf("the passport does not say whose identity this is: %q", p.Labels["attested"])
	}

	// And an agent that records its OWN attestation keeps it: the runtime's
	// identity is a fallback for agents bound to nothing, never an overwrite
	// of something somebody recorded.
	own := a
	own.Attestation = "oidc"
	own.AttestationDetail = "https://login.example.com"
	q := em.PassportFor(own)
	if q.Attestation.Method != "oidc" || q.Attestation.Detail != "https://login.example.com" {
		t.Errorf("a recorded attestation was overwritten by the runtime's: %+v", q.Attestation)
	}
	if _, claims := q.Labels["attested"]; claims {
		t.Error("a passport with its own attestation still claims the runtime's")
	}
}

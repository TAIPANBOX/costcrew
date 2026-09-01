package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/stack"
)

// A live call has to reach the shared bus.
//
// Red first: with the runner emitting nothing, this found an empty file. That
// is not a hypothetical. Measured on a live GCP cluster on 2026-09-01, a crew
// run made 42 model calls costing USD 0.2485 and the bus carried 26 lines, all
// of them written by the CONSOLE at startup and none of them by the run. The
// one half of this console that spends real money was the half the record
// plane could not see.
func TestALiveCallReachesTheSharedBus(t *testing.T) {
	db, task, analyst := runnerDB(t)
	b, path := testBus(t, "gcp.taipanbox.local", "crew-2026-w35")

	e := estimate{Task: task, Analyst: analyst, Engine: "openrouter",
		Model: "deepseek/deepseek-chat", WorstMicros: 2_200}
	res := callResult{Text: "the deliverable", InTokens: 181, OutTokens: 512,
		ActualMicros: 600}
	if err := saveDraft(db, e, res, b); err != nil {
		t.Fatal(err)
	}

	ev := oneEvent(t, path)
	if ev["type"] != "tool_call" {
		t.Errorf("type %q, want tool_call: the estate's own word for a tool "+
			"being called, and the type its record plane maps", ev["type"])
	}
	if got := ev["agent_id"]; got != "agent://gcp.taipanbox.local/y.mercer" {
		t.Errorf("agent_id %q: the analyst that made the call has to be the "+
			"agent on the record, under this installation's trust domain", got)
	}
	if ev["run_id"] == "" || ev["run_id"] == nil {
		t.Errorf("run_id is empty: the record plane refuses a record that " +
			"belongs to no run, and a record nobody can ask for is not a record")
	}
	data, _ := ev["data"].(map[string]any)
	for _, k := range []string{"input_tokens", "output_tokens", "cost_micros", "engine", "model"} {
		if _, ok := data[k]; !ok {
			t.Errorf("the event carries no %q: what a call cost is the only "+
				"reason a finops plane emits one at all", k)
		}
	}
}

// A run with no bus configured stays silent and still works.
//
// The default is off, as it is for the console: nothing is emitted until an
// operator points it somewhere. A runner that failed without a bus would make
// the integration mandatory, which is the opposite of what this contract is.
func TestARunWithNoBusStillSavesItsDeliverable(t *testing.T) {
	db, task, analyst := runnerDB(t)
	if err := saveDraft(db, estimate{Task: task, Analyst: analyst},
		callResult{Text: "x", ActualMicros: 1_000}, bus{}); err != nil {
		t.Fatalf("a runner with no bus configured must still record its work: %v", err)
	}
	as, err := crew.Artifacts(db, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 1 {
		t.Fatalf("wrote %d drafts with no bus, want 1", len(as))
	}
}

// Every call in one invocation belongs to ONE run.
//
// A run is one execution of an agent. Naming each call a run of its own also
// passes every check, which is why it is worth a test: the record plane shards
// and indexes by run, and a query for the run would then answer with one row
// where the answer is however many calls the run made.
func TestEveryCallInOneInvocationSharesTheRunID(t *testing.T) {
	db, tasks, analyst := runnerTasks(t, 3)
	b, path := testBus(t, "gcp.taipanbox.local", "crew-2026-w35")
	for _, task := range tasks {
		if err := saveDraft(db, estimate{Task: task, Analyst: analyst,
			Engine: "openrouter"}, callResult{Text: "x", ActualMicros: 500}, b); err != nil {
			t.Fatal(err)
		}
	}
	runs := map[string]int{}
	for _, ev := range allEvents(t, path) {
		runs[ev["run_id"].(string)]++
	}
	if len(runs) != 1 {
		t.Errorf("three calls in one invocation produced %d run ids (%v): a "+
			"query for the run would answer with a fraction of what it did",
			len(runs), runs)
	}
}

func testBus(t *testing.T, host, run string) (bus, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "costcrew.ndjson")
	em, err := stack.Open(stack.Config{EventsPath: path, Host: host})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { em.Close() })
	return bus{em: em, run: run}, path
}

func allEvents(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the bus file was never written: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("a line on the bus does not parse: %v", err)
		}
		out = append(out, ev)
	}
	return out
}

func oneEvent(t *testing.T, path string) map[string]any {
	t.Helper()
	evs := allEvents(t, path)
	if len(evs) != 1 {
		t.Fatalf("the bus carries %d line(s), want exactly 1", len(evs))
	}
	return evs[0]
}

// The seam: the runner's own wiring, not a bus a test handed it.
//
// Every test above proves saveDraft emits when it is GIVEN a bus. None of them
// can see whether the binary ever builds a real one, and that gap is exactly
// where this defect lived: the emitter existed, the console used it, and the
// runner never opened it. A test that only exercises the injected seam would
// have stayed green through the whole thing.
func TestTheRunnerOpensARealBusFromItsFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "costcrew.ndjson")
	b, err := openBus(path, "gcp.taipanbox.local")
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	if b.em == nil || !b.em.On() {
		t.Fatal("the runner's own flags did not produce a live emitter")
	}
	if b.run == "" {
		t.Error("a run with no id: every call it makes would belong to no run")
	}
	if got := b.em.AgentURI("y.mercer"); got != "agent://gcp.taipanbox.local/y.mercer" {
		t.Errorf("agent uri %q: the runner minted under a domain that is not "+
			"the one it was given", got)
	}
}

// A bus with no trust domain is refused before anything is spent.
//
// The console defaults the host to costcrew.local, which is right for a
// console somebody started by hand and wrong here: measured on a live GCP
// cluster on 2026-09-01, a whole cluster's events carried
// `agent://set-me.invalid/` and the record plane refused every one of them as
// foreign, which is counted and reads downstream as a quiet night.
func TestABusWithNoTrustDomainIsRefusedRatherThanDefaulted(t *testing.T) {
	if _, err := openBus(filepath.Join(t.TempDir(), "costcrew.ndjson"), ""); err == nil {
		t.Error("a bus with no trust domain was accepted: every event it writes " +
			"would be refused downstream and counted as somebody else's")
	}
	if _, err := openBus("", ""); err != nil {
		t.Errorf("no bus at all must be fine, it is the default: %v", err)
	}
}

package main

import (
	"fmt"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/stack"
)

// bus is how a live run tells the estate what it did.
//
// Until 2026-09-01 it did not tell it anything. The console emits an event for
// every action taken through its pages, and the runner, which is the half that
// actually spends money on a model account, emitted nothing at all: a run of
// 42 calls costing USD 0.2485 was measured on a live GCP cluster and reached
// trailryx, heraldyx, idryx and genaryx as silence. docs/live-agents.md had
// named this as step 5 of the executor and it was the step that was not built.
//
// It reuses the console's own emitter rather than writing a second one. One
// file is one hash chain, the vocabulary translation is already there, and a
// second writer with its own opinion of the envelope is how two producers come
// to disagree about the same contract.
type bus struct {
	em *stack.Emitter
	// run is this invocation. One run of this binary is one execution of an
	// agent, which is what the contract means by a run, and every call it
	// makes belongs to that one rather than to a run per call.
	run string
}

// toolCall says an analyst called a model, in the estate's own word for it.
//
// `tool_call` is not this console's invention: it is one of the shared types
// the record plane maps, listed in vocabulary.go's own account of what the
// shared vocabulary is about ("a budget was checked, a policy decided, a tool
// was called, a breaker tripped"). A live model call is that.
//
// Fail-open, like every other emit here: a bus that cannot be written is
// reported and never becomes the reason a deliverable was not saved.
func (b bus) toolCall(e estimate, res callResult) error {
	if b.em == nil || !b.em.On() {
		return nil
	}
	return b.em.Emit("tool_call", e.Analyst.Name, "info", map[string]any{
		"run":           b.run,
		"task":          e.Task.ID,
		"engine":        e.Engine,
		"model":         e.Model,
		"input_tokens":  res.InTokens,
		"output_tokens": res.OutTokens,
		"cost_micros":   res.ActualMicros,
		"worst_micros":  e.WorstMicros,
	}, nil)
}

// openBus prepares this run's reporting, and refuses BEFORE anything is spent.
//
// The trust domain is not defaulted here. The console defaults it to
// costcrew.local when nothing is given, and that default is right for a
// console somebody started by hand to look at. It is wrong for a runner: an
// event minted under a domain the record plane was not given is refused as
// foreign, counted, and reads downstream as a calm night rather than as a
// misconfiguration. Measured on a live GCP cluster on 2026-09-01, where the
// whole cluster's events carried `agent://set-me.invalid/` and the seal
// refused every one of them.
//
// So: a bus with no host is an error, and no bus at all is fine.
func openBus(eventsPath, host string) (bus, error) {
	if eventsPath == "" {
		return bus{}, nil
	}
	if host == "" {
		return bus{}, fmt.Errorf(
			"-stack-events needs -stack-host: an event minted under a trust domain " +
				"the record plane was not given is refused as foreign and counted, " +
				"which reads as a quiet night rather than as a misconfiguration")
	}
	em, err := stack.Open(stack.Config{EventsPath: eventsPath, Host: host})
	if err != nil {
		return bus{}, fmt.Errorf("opening the shared bus at %s: %w", eventsPath, err)
	}
	return bus{em: em, run: newRunID()}, nil
}

func (b bus) close() {
	if b.em != nil {
		_ = b.em.Close()
	}
}

// newRunID names this invocation.
//
// One run of this binary is one execution of an agent. The shape is the
// contract's: lowercase, no colons or slashes, inside 64 bytes, which is why
// it is built rather than borrowed from anything that carries a URI.
func newRunID() string {
	return fmt.Sprintf("crew-%d", time.Now().UTC().Unix())
}

package web

// The agent card's log is read from the agent-event stream, a file on disk
// another process appends to. What it does with each kind of line: a blank
// one and a torn one are skipped, an event the analyst emitted, acted for,
// or is named in is kept, and the newest come first, capped at the limit.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheCardReadsOnlyThisAnalystsEventsNewestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "costcrew.ndjson")
	lines := []string{
		`{"ts":"2026-08-01T10:00:00Z","type":"spend_spike","agent_id":"agent://costcrew.test/triage-aws","severity":"high","data":{"costcrew_type":"anomaly_detected","service":"Amazon EC2"}}`,
		``,
		`{"ts":"2026-08-01T10:01:00Z","type":"torn line`,
		`{"ts":"2026-08-01T10:02:00Z","type":"tool_call","agent_id":"agent://costcrew.test/partner-aws","severity":"info","on_behalf_of":["agent://costcrew.test/triage-aws"],"data":{"tool":"charges_query"}}`,
		`{"ts":"2026-08-01T10:03:00Z","type":"agent_state_changed","agent_id":"agent://costcrew.test/supervisor","severity":"high","data":{"analyst":"triage-aws","state":"suspended"}}`,
		`{"ts":"2026-08-01T10:04:00Z","type":"agent_state_changed","agent_id":"agent://costcrew.test/supervisor","severity":"high","data":{"analyst":"triage-aws-2","state":"suspended"}}`,
		`{"ts":"2026-08-01T10:05:00Z","type":"tool_call","agent_id":"agent://costcrew.test/triage-gcp","severity":"info","data":{"tool":"charges_query"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{eventsPath: path}
	got, err := s.analystEvents("triage-aws", 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%d events, want the three that are about triage-aws: %+v", len(got), got)
	}
	// Newest first.
	if got[0].when != "2026-08-01T10:03:00Z" || got[2].when != "2026-08-01T10:00:00Z" {
		t.Errorf("order %s, %s, %s: want newest first", got[0].when, got[1].when, got[2].when)
	}
	if got[2].Kind != "anomaly_detected → spend_spike" {
		t.Errorf("kind %q: the console's word and the wire's word both belong", got[2].Kind)
	}
	if !strings.HasPrefix(got[1].Detail, "by partner-aws: ") {
		t.Errorf("an event another agent emitted for it reads %q, want it to name who", got[1].Detail)
	}
	if !strings.HasPrefix(got[0].Detail, "by supervisor: ") {
		t.Errorf("an event naming it in its data reads %q", got[0].Detail)
	}
}

func TestTheCardCapsItsLogAndToleratesNoStream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "costcrew.ndjson")
	var b strings.Builder
	for i := 0; i < 45; i++ {
		fmt.Fprintf(&b, `{"ts":"2026-08-01T10:%02d:00Z","type":"tool_call","agent_id":"agent://costcrew.test/triage-aws","data":{}}`+"\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := (&Server{eventsPath: path}).analystEvents("triage-aws", 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 40 || got[0].when != "2026-08-01T10:44:00Z" {
		t.Errorf("%d events starting %q, want the newest 40", len(got), got[0].when)
	}

	// Switched on, nothing written yet: no log and no error.
	got, err = (&Server{eventsPath: filepath.Join(t.TempDir(), "not-yet")}).analystEvents("triage-aws", 40)
	if got != nil || err != nil {
		t.Errorf("a stream not yet written: %v, %v", got, err)
	}
	// Switched off: nothing.
	if got, err := (&Server{}).analystEvents("triage-aws", 40); got != nil || err != nil {
		t.Errorf("no stream configured: %v, %v", got, err)
	}
}

package store_test

// What the journal records for an event: the actor, the severity and the
// delegation chain, written as one readable string. And Tee, which writes
// to every recorder it is given and reports the first refusal without
// letting it stop the others.

import (
	"errors"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/store"
)

func TestTheJournalRecordsWhoActedForWhom(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	data := map[string]any{"task": 7}
	if err := s.AsRecorder().Emit("tool_call", "triage-aws", "info", data,
		[]string{"agent://x/supervisor", "agent://x/partner-aws"}); err != nil {
		t.Fatal(err)
	}
	if _, has := data["actor"]; has {
		t.Error("the caller's map was written into")
	}
	tail, err := s.JournalTail(1)
	if err != nil || len(tail) != 1 {
		t.Fatalf("journal: %v %v", tail, err)
	}
	d := tail[0].Data
	if d["actor"] != "triage-aws" || d["severity"] != "info" ||
		d["on_behalf_of"] != "agent://x/supervisor -> agent://x/partner-aws" {
		t.Errorf("recorded %v", d)
	}
}

type countingRecorder struct {
	calls int
	err   error
}

func (c *countingRecorder) Emit(string, string, string, map[string]any, []string) error {
	c.calls++
	return c.err
}

func TestTeeWritesToEveryRecorderAndReportsTheFirstRefusal(t *testing.T) {
	if store.Tee(nil, nil) != nil {
		t.Error("a tee of nothing is a recorder")
	}
	only := &countingRecorder{}
	if got := store.Tee(nil, only); got != store.Recorder(only) {
		t.Error("a tee of one recorder is not that recorder")
	}
	first := &countingRecorder{err: errors.New("the stream is full")}
	second := &countingRecorder{err: errors.New("second refusal")}
	third := &countingRecorder{}
	err := store.Tee(first, nil, second, third).Emit("x", "a", "info", nil, nil)
	if err == nil || err.Error() != "the stream is full" {
		t.Errorf("tee reported %v, want the first refusal", err)
	}
	if first.calls != 1 || second.calls != 1 || third.calls != 1 {
		t.Errorf("calls %d %d %d: a refusal stopped the recorders after it", first.calls, second.calls, third.calls)
	}
}

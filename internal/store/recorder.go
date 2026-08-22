package store

// The chain has to hold the WORK, not only the sign-ins.
//
// The audit page says "everything the console did, in order, each entry hashed
// against the one before it". Until this existed that was false: the chain
// held user_created, login and role changes, and every decision the console
// actually makes — a finding taken, answered, accepted or dismissed, a sprint
// closed, an explainer published — went only to the agent-event stream, which
// is an append-only NDJSON file and not hash-chained at all.
//
// The two logs answer different questions and both are wanted. The stream is
// what another service in the stack reads and it carries the delegation chain;
// the journal is this installation's own tamper-evident record. A governance
// console that hashes who signed in and not what was decided has the chain
// pointed at the wrong thing.

// Recorder is anomaly.Recorder, restated here so this package does not import
// the one that imports it.
type Recorder interface {
	Emit(kind, actor, severity string, data map[string]any, onBehalfOf []string) error
}

// AsRecorder writes every decision into the hash chain.
//
// The actor and the delegation chain travel with it: "who did this" is the
// part of an audit entry somebody actually needs, and an entry that records a
// change without recording whose it was is a log of things that happened to
// nobody.
func (s *Store) AsRecorder() Recorder { return journalRecorder{s} }

type journalRecorder struct{ st *Store }

func (j journalRecorder) Emit(kind, actor, severity string, data map[string]any, onBehalfOf []string) error {
	// A copy: the caller's map belongs to the caller, and the emitter that
	// runs after this one would otherwise see fields this one added.
	d := make(map[string]any, len(data)+3)
	for k, v := range data {
		d[k] = v
	}
	if actor != "" {
		d["actor"] = actor
	}
	if severity != "" {
		d["severity"] = severity
	}
	if len(onBehalfOf) > 0 {
		chain := ""
		for i, o := range onBehalfOf {
			if i > 0 {
				chain += " -> "
			}
			chain += o
		}
		d["on_behalf_of"] = chain
	}
	_, err := j.st.Journal(kind, 0, d)
	return err
}

// Tee sends one event to several recorders.
//
// The chain and the stream are both written, and neither waits on the other:
// a stack emitter that fails must not stop the tamper-evident record being
// written, which is the one that has to be complete to be worth anything.
func Tee(rs ...Recorder) Recorder {
	live := make([]Recorder, 0, len(rs))
	for _, r := range rs {
		if r != nil {
			live = append(live, r)
		}
	}
	if len(live) == 0 {
		return nil
	}
	if len(live) == 1 {
		return live[0]
	}
	return tee(live)
}

type tee []Recorder

func (t tee) Emit(kind, actor, severity string, data map[string]any, onBehalfOf []string) error {
	var first error
	for _, r := range t {
		if err := r.Emit(kind, actor, severity, data, onBehalfOf); err != nil && first == nil {
			first = err
		}
	}
	return first
}

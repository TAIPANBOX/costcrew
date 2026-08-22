package stack

import "strings"

// The stack already has a word for this, and CostCrew was not using it.
//
// heraldyx and trailryx read the same vocabulary: budget_threshold,
// spend_spike, policy_deny, run_killed, breaker_tripped and a handful more.
// This console invented its own - anomaly_detected, anomaly_triaged,
// agent_hired - and the result was measurable on both sides. trailryx read all
// sixty-nine lines, accepted the envelope, the schema, the agent id and the
// trust domain, and refused every one of them with "an event type this reading
// does not map". heraldyx composed real mail and had to say "an event this
// build does not have a description for".
//
// So where a CostCrew event MEANS one of the shared ones, it is emitted as the
// shared one, and the specific type travels in the payload so nothing is lost
// and this console's own reading of its stream is unchanged.
//
// # What is deliberately NOT mapped, and why
//
// The shared vocabulary is about a RUN: a budget was checked, a policy decided,
// a tool was called, a breaker tripped. CostCrew's other events are about a
// PRACTICE: a finding was triaged, an agent was hired, a sprint was planned.
// Those have no equivalent, and inventing one would be a false claim in a
// record somebody audits. An agent being hired is not a policy decision. A
// sprint being planned is not a tool call. They keep their own names and the
// downstream refuses them, which is the correct outcome rather than a gap:
// trailryx records what agents did, and a roster change is not that.
//
// One mapping was considered and rejected. budget_exhausted maps, in trailryx,
// to a DENIED verdict, and this console denies nothing: it records that an
// agent went past its guard and does not stop it. Emitting it would put a
// refusal that never happened into a tamper-evident record. budget_threshold
// carries a warning and no verdict, which is exactly what is true here.

// shared is the type the estate's other services understand, when there is one.
var shared = map[string]string{
	// A detected anomaly IS a spend spike. It is the same event, found the
	// same way, and it is the one place the two vocabularies say the same
	// thing in different words.
	"anomaly_detected": "spend_spike",
	// An agent past the guard it was given. A warning about a budget, which
	// is what budget_threshold is for.
	"guard_passed": "budget_threshold",
}

// translate returns the wire type and the payload to send with it.
//
// The original type is kept under costcrew_type rather than dropped, because
// "spend_spike" loses which of this console's decisions produced it, and an
// auditor reading the record six months from now should not have to know that
// this console renames things on the way out.
func translate(kind string, data map[string]any) (string, map[string]any) {
	wire, ok := shared[kind]
	if !ok {
		return kind, data
	}
	out := make(map[string]any, len(data)+2)
	for k, v := range data {
		out[k] = v
	}
	out["costcrew_type"] = kind
	// Stamped here, not left to the caller.
	//
	// budget_threshold is the honest word only while this console enforces
	// nothing, and a reader of the record should not have to know that. A
	// caller that forgot the field would produce an event that says a budget
	// was checked and leaves open whether anything was refused, which is the
	// ambiguity the whole mapping exists to avoid. The day a guard does bite,
	// this line is what has to change, and it is one line in one place.
	if wire == "budget_threshold" {
		out["enforced"] = false
	}
	return wire, out
}

// runOf is the run identifier a mapped event needs.
//
// trailryx refuses a record with no run id, and it is right to: its store is
// sharded and indexed by run, and a record belonging to no execution cannot be
// asked for. CostCrew's unit of execution is the detection pass, so a pass is
// a run and every finding it raised belongs to it.
//
// The shape is what the contract allows: at most 64 bytes and no / or : in it,
// which rules out reusing an agent:// URI, and is why this is built rather
// than borrowed.
func runOf(kind string, data map[string]any) string {
	if _, mapped := shared[kind]; !mapped {
		return ""
	}
	// The PASS, not the finding.
	//
	// A run is one execution of an agent, and a detection pass is that: the
	// detector woke, read the estate, and raised what it found. Nine findings
	// from one pass are nine records of one run, and a store that shards and
	// indexes by run should answer a query for that run with all nine. Naming
	// each finding a run of its own would answer with one.
	if pass, ok := data["pass"].(string); ok && pass != "" {
		return clampRun(pass)
	}
	// A finding raised outside a pass still needs an identifier, because a
	// record belonging to no run cannot be asked for at all.
	if id, ok := data["anomaly"].(string); ok && id != "" {
		return clampRun("finding-" + id)
	}
	if who, ok := data["analyst"].(string); ok && who != "" {
		if day, ok := data["month"].(string); ok && day != "" {
			return clampRun("guard-" + who + "-" + day)
		}
		return clampRun("guard-" + who)
	}
	return ""
}

// clampRun keeps an id inside the contract's shape rather than letting the far
// end refuse it.
//
// The shape is [a-z0-9._-], at most 64 bytes: the Agent Passport spec's own
// per-segment character set. LOWERCASE is the part that is easy to miss and
// the part that cost a round trip here: this console's anomaly ids are
// A-27019dfc1208, and trailryx refused nine perfectly good records over the
// capital letter. Nothing upstream of the wire had to change, because the id
// is built here and is a wire concern.
//
// Anything outside the set becomes a hyphen rather than being dropped, so two
// ids that differ only in a disallowed character do not collapse into one.
func clampRun(s string) string {
	s = strings.ToLower(s)
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return string(out)
}

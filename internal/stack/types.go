package stack

import "sort"

// WireTypes is every `type` this console can put on the shared bus, as it
// appears ON THE WIRE.
//
// # Why a list here at all
//
// The estate's registry (agent-passport SPEC 6.2) is a claim about what each
// source writes TODAY, and estate-gates C4 checks that claim against each
// producer's code. Every other producer names its types in one composite
// literal a reader can resolve; this console does not. Its kinds are string
// literals at nine HTTP handlers and two background paths, one of them built
// as `"anomaly_" + string(to)` from a state value, and two of them RENAMED on
// the way out by `shared` in vocabulary.go.
//
// A regular expression over that is inference, and inference is what the
// registry's own history says goes wrong: seven names were reserved for idryx
// that were wrong in both directions. So the repository that knows declares,
// and the declaration is held to the code by a test in this package rather
// than by a reader in another repository guessing at Go.
//
// That division is the registry's own: SPEC 6.2 says of idryx's detector count
// that "what knows is idryx's own scripts/detectors-complete.sh".
//
// # What this is NOT
//
// It is not what this console's own code calls things. `anomaly_detected` and
// `guard_passed` are kinds here and never reach the bus under those names;
// they travel as `spend_spike` and `budget_threshold` with the original kept
// in the payload under `costcrew_type`. A consumer sees this list and nothing
// else.
func WireTypes() []string {
	out := append([]string(nil), wireTypes...)
	sort.Strings(out)
	return out
}

var wireTypes = []string{
	// Translated by `shared` in vocabulary.go. These two are the estate's own
	// words, and are the only types here another producer also emits.
	"spend_spike",      // from anomaly_detected
	"budget_threshold", // from guard_passed

	// This console's own vocabulary, about a PRACTICE rather than a run.
	// vocabulary.go argues why these are deliberately not translated and why a
	// downstream refusing them is the correct outcome rather than a gap.
	"anomaly_triaged",
	"anomaly_explained",
	"anomaly_accepted",
	"anomaly_dismissed",
	"budgets_set",
	"forecast_frozen",
	"explainer_published",
	"sprint_planned",
	// B3-SPEC.md section 2: a deliverable's options block named a class
	// outside the writing role's own job description, so it was saved
	// without its options and the task came back with the reason.
	"option_refused",
	"agent_hired",
	"agent_rebriefed",
	"agent_state_changed",
	"agent_removed",
	"agent_transferred",
	// A generated estate replaced by real data a connector's reader found.
	// About this console's own bookkeeping, not a run: no shared word covers
	// deciding a fixture is done and removing it, so this keeps its own name
	// for the same reason agent_hired does.
	"generated_estate_replaced",
}

package deliver

// The prompt-data policy: how much of an installation's billing data may
// reach a model. CLAUDE.md invariant 70.
//
// Three modes, set per installation (one process serves one installation) by
// -prompt-data or COSTCREW_PROMPT_DATA:
//
//	full        what this console has always sent. The default, and unchanged.
//	masked      every identifier is replaced by a stable pseudonym before a
//	            prompt or a tool result leaves the process; money, dates,
//	            counts and ratios stay; free text that can carry a name is not
//	            sent at all; the model-written SQL tools are not offered.
//	aggregates  no row-level data: per-desk and per-team totals, variances,
//	            KPIs and series sums, with team and desk names masked.
//
// The policy is process-wide on purpose. The prompt is built in four places
// (the runner, the bench, the console's plan-ask and the packet builder both
// call), two of which are in packages this change may not edit, and a mode
// that had to be threaded through every signature would be a mode that some
// caller forgot to pass and so quietly sent in full. With one active policy
// read at the point the text is built, a caller that forgets gets what the
// installation chose, not the most permissive thing.

import (
	"database/sql"
	"fmt"
	"os"
	"sync/atomic"
)

// PromptData is the closed vocabulary of the setting.
type PromptData string

const (
	PromptFull       PromptData = "full"
	PromptMasked     PromptData = "masked"
	PromptAggregates PromptData = "aggregates"
)

// PromptDataEnv backs -prompt-data's default, the way COSTCREW_GATEWAY backs
// -gateway: an installation sets it once.
const PromptDataEnv = "COSTCREW_PROMPT_DATA"

// ParsePromptData accepts exactly the three words, in lower case, with nothing
// around them. A typo is a refusal, never a fall back to full: a setting whose
// whole purpose is to send less must not turn itself off by being misspelled.
func ParsePromptData(raw string) (PromptData, error) {
	switch m := PromptData(raw); m {
	case PromptFull, PromptMasked, PromptAggregates:
		return m, nil
	}
	return "", fmt.Errorf("prompt data %q is not one of full, masked, aggregates "+
		"(-prompt-data, or %s): refusing to start rather than send more than was asked for",
		raw, PromptDataEnv)
}

// PromptDataEnvDefault is -prompt-data's default: the environment variable
// when it is set, "full" otherwise. It returns the variable as written, so a
// typo in it reaches ParsePromptData and is refused there.
func PromptDataEnvDefault() string {
	if v := os.Getenv(PromptDataEnv); v != "" {
		return v
	}
	return string(PromptFull)
}

// WithheldFreeText stands in for text that is not sent. One line, the same
// in both restricting modes, so a reader of a recorded prompt knows something
// was left out and is not left to wonder whether it was empty.
const WithheldFreeText = "[withheld: free text is not sent to the model under this -prompt-data setting]"

// WithheldLabel stands in for a short free-text label (a driver's name) in a
// line that otherwise stays.
const WithheldLabel = "[label withheld]"

// maskFailed is what is sent when the list of names to mask could not be read:
// nothing, said so. A best-effort mask over a half-read list is a leak.
const maskFailed = "[withheld: the names in this installation could not be read, so nothing is sent unmasked]"

// ModeLine is the one line every prompt carries to say which policy it was
// built under, so a recorded prompt can be read without knowing the flags.
func (p *Policy) ModeLine() string {
	switch p.Mode() {
	case PromptMasked:
		return "Prompt data policy: masked. Names of teams, desks, services, agents, people, " +
			"invoices, vendors and resources are replaced by stable tokens such as team-7f3a " +
			"(the same name is always the same token); amounts, dates and counts are real; " +
			"free text is withheld."
	case PromptAggregates:
		return "Prompt data policy: aggregates. Only per-desk and per-team totals, variances, " +
			"KPIs and series sums are sent; team and desk names are stable tokens such as " +
			"team-7f3a; amounts and dates are real; nothing at the level of a row is sent."
	}
	return "Prompt data policy: full. Names, figures and text are sent as they are."
}

// aggregatesNote opens an aggregates packet and says what is not in it.
const aggregatesNote = "Nothing at the level of a single driver, deliverable, resource, agent, model, " +
	"invoice, licence or contract is sent under -prompt-data aggregates, only totals."

// The tool catalogue, per mode. Anything not listed for a restricting mode is
// withheld, so a tool added tomorrow is not offered until somebody decides.
var (
	// masked: every tool except the two whose arguments are SQL the model
	// writes. A model that can write `SELECT agent FROM ai_calls` can select
	// any identifier in the table, in a column or in an expression that
	// rearranges it (substr, a concatenation), and no scrub of the result can
	// be trusted to recognise a name that was cut in two. The safer of the two
	// ways of handling them is to not offer them.
	toolsMasked = map[string]bool{
		"anomaly": true, "series": true, "drivers": true, "team_month": true,
		"budgets": true, "variance": true, "kpis": true, "maturity": true,
		"allocation": true, "showback": true,
	}
	// aggregates: only the tools whose answer is a total per team or per desk,
	// a variance or a KPI. None returns a row of a ledger, a service, a
	// driver or a single anomaly.
	toolsAggregates = map[string]bool{
		"team_month": true, "budgets": true, "variance": true, "kpis": true,
		"maturity": true, "allocation": true, "showback": true,
	}
)

// ToolOffered says whether the tool catalogue offers name under this policy.
func (p *Policy) ToolOffered(name string) bool {
	switch p.Mode() {
	case PromptMasked:
		return toolsMasked[name]
	case PromptAggregates:
		return toolsAggregates[name]
	}
	return true
}

// ----------------------------------------------------------- the active policy

var active atomic.Pointer[Policy]

func init() { active.Store(&Policy{mode: PromptFull}) }

// ActivePolicy is the policy this process builds prompts under. It is never
// nil, and it is "full" until something configures it.
func ActivePolicy() *Policy { return active.Load() }

// SetActivePolicy installs p and returns the function that puts back what was
// there. A nil p is ignored.
func SetActivePolicy(p *Policy) (restore func()) {
	if p == nil {
		return func() {}
	}
	prev := active.Swap(p)
	return func() { active.Store(prev) }
}

// ConfigurePromptData is what a binary's main calls: parse the flag, load or
// create the key in the data directory (masked and aggregates only), install
// the policy. A typo, or a key that cannot be loaded, is an error and the
// active policy is left as it was. The store is bound later, once open
// (BindActivePolicy).
func ConfigurePromptData(raw, dataDir string) (PromptData, error) {
	m, err := ParsePromptData(raw)
	if err != nil {
		return "", err
	}
	p, err := NewPolicy(m, dataDir)
	if err != nil {
		return "", err
	}
	SetActivePolicy(p)
	return m, nil
}

// BindActivePolicy gives the active policy the store whose names it masks.
func BindActivePolicy(db *sql.DB) { ActivePolicy().Bind(db) }

// Mode is the policy's mode.
func (p *Policy) Mode() PromptData {
	if p == nil {
		return PromptFull
	}
	return p.mode
}

// Full is whether nothing is changed.
func (p *Policy) Full() bool { return p.Mode() == PromptFull }

// Aggregates is whether row-level data is withheld altogether.
func (p *Policy) Aggregates() bool { return p.Mode() == PromptAggregates }

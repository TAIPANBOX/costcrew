// Package engines is where the crew's model calls come from.
//
// Grouped by HOW YOU PAY rather than by vendor, because that is the question
// somebody actually has: a subscription you already have, a key you will be
// billed per token for, or an assistant the organisation is already paying
// for. Three answers to "what do I need", not a list of twelve providers.
//
// No key is ever stored here. Each engine names the environment variable its
// credential lives under, so a database copied for inspection carries none of
// them.
package engines

import (
	"os"
	"os/exec"
	"strings"
)

// Family is how the money leaves.
type Family string

const (
	Subscription Family = "subscription" // already paid for, no key at all
	APIKey       Family = "api-key"      // billed per token
	Existing     Family = "existing"     // an assistant the organisation already pays for
)

type Engine struct {
	ID     string
	Name   string
	Family Family

	// When to choose it, what it costs, and where to get what it needs. Every
	// engine answers all three, because an option that does not say what it
	// costs is one somebody picks by accident.
	When string
	Cost string
	How  string
	Doc  string

	EnvVar  string // the credential's environment variable, when it needs one
	Command string // the local command, when it is one
	BaseURL string
	Models  []string
}

var Catalogue = []Engine{
	{
		ID: "claude-cli", Name: "Claude, your own subscription", Family: Subscription,
		When: "You already pay for Claude and want the crew to use that, with no key " +
			"stored anywhere.",
		Cost: "Comes out of the subscription rather than a separate bill. A deep " +
			"analysis is roughly USD 0.30 to 0.60 of usage against it.",
		How:     "Run `claude` in a terminal, then `/login`, once. Nothing to paste here.",
		Doc:     "https://claude.com/claude-code",
		Command: "claude",
	},
	{
		ID: "openrouter", Name: "OpenRouter", Family: APIKey,
		When:    "The simplest start: one key, hundreds of models, and it reports what each call cost.",
		Cost:    "Per token, billed by OpenRouter. Routine work on a cheap model is cents a sprint.",
		How:     "Create a key at openrouter.ai/keys.",
		Doc:     "https://openrouter.ai/keys",
		EnvVar:  "OPENROUTER_API_KEY",
		BaseURL: "https://openrouter.ai/api/v1",
		Models:  []string{"deepseek/deepseek-chat", "moonshotai/kimi-k2", "anthropic/claude-sonnet-4"},
	},
	{
		ID: "anthropic", Name: "Anthropic API", Family: APIKey,
		When:    "You want the strong model directly, billed to your own account.",
		Cost:    "Per token, at Anthropic's published rates.",
		How:     "Create a key in the Anthropic console.",
		Doc:     "https://docs.claude.com/en/api",
		EnvVar:  "ANTHROPIC_API_KEY",
		BaseURL: "https://api.anthropic.com",
		Models:  []string{"claude-opus-5", "claude-sonnet-5"},
	},
	{
		ID: "deepseek", Name: "DeepSeek", Family: APIKey,
		When:    "The cheapest bulk engine, if you would rather go direct than through a router.",
		Cost:    "USD 0.27 per million input tokens, USD 1.10 per million output.",
		How:     "Create a key at platform.deepseek.com.",
		Doc:     "https://platform.deepseek.com/api_keys",
		EnvVar:  "DEEPSEEK_API_KEY",
		BaseURL: "https://api.deepseek.com/v1",
		Models:  []string{"deepseek-chat"},
	},
	{
		ID: "local-cli", Name: "A local assistant already paid for", Family: Existing,
		When: "Your organisation already pays for an assistant that runs on this " +
			"machine under its own sign-in. Nothing outbound to approve, and the " +
			"spend stays on a contract that exists.",
		Cost:    "Whatever that contract already costs. Nothing new is billed.",
		How:     "Give the command it is launched with. Its own sign-in is the credential.",
		Command: "",
	},
}

// Availability is what is actually usable on this machine right now, which is
// a different question from what is in the catalogue.
type Availability struct {
	Engine
	Ready  bool
	Reason string
}

// Check answers honestly and does NOT call anything: it looks for a credential
// in the environment and a command on the path.
//
// Nothing here spends money to find out whether spending money would work. A
// readiness check that costs a token per engine is one nobody runs twice.
func Check(lookup func(string) string, look func(string) (string, error)) []Availability {
	if lookup == nil {
		lookup = os.Getenv
	}
	if look == nil {
		look = exec.LookPath
	}
	out := make([]Availability, 0, len(Catalogue))
	for _, e := range Catalogue {
		a := Availability{Engine: e}
		switch {
		case e.EnvVar != "":
			if v := strings.TrimSpace(lookup(e.EnvVar)); v != "" {
				a.Ready, a.Reason = true, "a key is set in "+e.EnvVar
			} else {
				a.Reason = e.EnvVar + " is not set in this process's environment"
			}
		case e.Command != "":
			if p, err := look(e.Command); err == nil {
				a.Ready, a.Reason = true, e.Command+" is on the path at "+p
			} else {
				a.Reason = e.Command + " is not on this machine's path"
			}
		default:
			a.Reason = "give it a command and it becomes available"
		}
		out = append(out, a)
	}
	return out
}

// Dry reports whether the crew can spend anything at all.
//
// With nothing configured the console still works: every figure the engine
// computes is real, and the analysts simply do not write. Saying that plainly
// is better than a console that looks broken because a key is missing.
func Dry(av []Availability) bool {
	for _, a := range av {
		if a.Ready {
			return false
		}
	}
	return true
}

func ByFamily(av []Availability, f Family) []Availability {
	var out []Availability
	for _, a := range av {
		if a.Family == f {
			out = append(out, a)
		}
	}
	return out
}

func FamilyTitle(f Family) string {
	switch f {
	case Subscription:
		return "A subscription you already have"
	case APIKey:
		return "A key, billed per token"
	case Existing:
		return "An assistant the organisation already pays for"
	}
	return string(f)
}

func FamilyNote(f Family) string {
	switch f {
	case Subscription:
		return "No key is stored anywhere. The tool's own sign-in is the credential."
	case APIKey:
		return "The key lives in an environment variable, never in this console's database. " +
			"Every call is billed to you."
	case Existing:
		return "Nothing outbound to approve and no new bill: the spend stays on a " +
			"contract that already exists."
	}
	return ""
}

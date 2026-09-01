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
	CloudRole    Family = "cloud-role"   // billed to a cloud account, no key to paste
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

	// EnvAny is for an engine whose credential comes from a CHAIN rather than
	// from one variable somebody pastes. Any of these being set is taken as
	// the chain being configured, and the Reason says what that misses.
	EnvAny []string

	// Metered says whether a call on this engine costs NEW money, stated
	// rather than inferred.
	//
	// It used to be inferred from EnvVar being non-empty, which was
	// accidentally right for every engine here until one arrived that bills
	// per token and holds no key: Bedrock takes its credentials from the
	// cloud's own chain. Inferring would have read it as a subscription, and
	// prices.go's own header records what that reading costs: the estimator
	// waves the call through with no bound at all. Unknown is not free, and
	// neither is keyless.
	Metered bool
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
		ID: "openrouter", Name: "OpenRouter", Family: APIKey, Metered: true,
		When:    "The simplest start: one key, hundreds of models, and it reports what each call cost.",
		Cost:    "Per token, billed by OpenRouter. Routine work on a cheap model is cents a sprint.",
		How:     "Create a key at openrouter.ai/keys.",
		Doc:     "https://openrouter.ai/keys",
		EnvVar:  "OPENROUTER_API_KEY",
		BaseURL: "https://openrouter.ai/api/v1",
		Models:  []string{"deepseek/deepseek-chat", "moonshotai/kimi-k2", "anthropic/claude-sonnet-4"},
	},
	{
		ID: "anthropic", Name: "Anthropic API", Family: APIKey, Metered: true,
		When:    "You want the strong model directly, billed to your own account.",
		Cost:    "Per token, at Anthropic's published rates.",
		How:     "Create a key in the Anthropic console.",
		Doc:     "https://docs.claude.com/en/api",
		EnvVar:  "ANTHROPIC_API_KEY",
		BaseURL: "https://api.anthropic.com",
		Models:  []string{"claude-opus-5", "claude-sonnet-5"},
	},
	{
		ID: "deepseek", Name: "DeepSeek", Family: APIKey, Metered: true,
		When:    "The cheapest bulk engine, if you would rather go direct than through a router.",
		Cost:    "USD 0.27 per million input tokens, USD 1.10 per million output.",
		How:     "Create a key at platform.deepseek.com.",
		Doc:     "https://platform.deepseek.com/api_keys",
		EnvVar:  "DEEPSEEK_API_KEY",
		BaseURL: "https://api.deepseek.com/v1",
		Models:  []string{"deepseek-chat"},
	},
	{
		ID: "bedrock", Name: "Amazon Bedrock", Family: CloudRole, Metered: true,
		When: "Your agents already run in AWS and you would rather the model bill " +
			"landed on that account than on a model vendor's, with no key to paste " +
			"or rotate anywhere.",
		Cost: "Per token, billed to your AWS account at Bedrock's on-demand rates " +
			"for the region you call. Nova Micro is the cheapest thing here by an " +
			"order of magnitude.",
		How: "Nothing to paste. The call is signed with whatever credentials the " +
			"workload already has: a profile on a laptop, an instance role on EC2, " +
			"IRSA in EKS. Set AWS_REGION and give the role bedrock:InvokeModel.",
		Doc: "https://docs.aws.amazon.com/bedrock/latest/userguide/",
		// The AWS chain, in the order the SDK resolves it. Any one of these
		// being set is taken as configured; see Check for what it misses.
		EnvAny: []string{"AWS_ACCESS_KEY_ID", "AWS_PROFILE", "AWS_ROLE_ARN",
			"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"},
		// Inference profile ids, not bare model ids, and that is not a detail.
		// Measured 2026-09-01 on a real account: `anthropic.claude-sonnet-5`
		// answers AccessDeniedException while the eu. profile for the same
		// family answers 200. list-foundation-models shows what EXISTS, not
		// what an account may call.
		Models: []string{
			"eu.amazon.nova-micro-v1:0",
			"eu.amazon.nova-lite-v1:0",
			"eu.amazon.nova-pro-v1:0",
		},
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
		case len(e.EnvAny) > 0:
			// An approximation, and it says so. The AWS chain also resolves
			// from the instance metadata service, which sets no variable at
			// all, so a node with a role attached reads as not ready here and
			// works perfectly when called. Reporting "ready" on evidence this
			// console does not have would be the worse error of the two.
			for _, v := range e.EnvAny {
				if strings.TrimSpace(lookup(v)) != "" {
					a.Ready = true
					a.Reason = "the cloud's credential chain is configured (" + v + " is set)"
					break
				}
			}
			if !a.Ready {
				a.Reason = "none of " + strings.Join(e.EnvAny, ", ") + " is set. A role " +
					"attached to the instance sets none of them and still works: this " +
					"reads variables and cannot see the metadata service"
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
	case CloudRole:
		return "Your cloud's own models, billed to that account"
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
	case CloudRole:
		return "No key to paste and none stored here: the call is signed with the " +
			"credentials the cloud already gives this workload. The bill lands on " +
			"that cloud account rather than on a model vendor's."
	}
	return ""
}

// Package stack puts CostCrew on the agent-governance stack directly.
//
// There is no bridge process and no translator. The original needed one
// because it was Python and the shared contract is Go, so its journal had to
// be re-expressed by something standing outside it. Here the product writes
// the shared envelope at the source, which removes a component, removes a
// second hash chain, and removes the window in which the two disagree.
//
// Everything is opt-in and fail-open. Governance that stops the console
// working is governance somebody switches off.
package stack

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
	"github.com/TAIPANBOX/agent-stack-go/passport"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// Source is what every event this product emits says it came from.
const Source = "costcrew"

// Detector is the identity that finds anomalies. A detection has to name an
// actor, and naming the team whose spend moved would be wrong: the team did
// not report anything, the detector did.
const Detector = "detector"

type Config struct {
	// EventsPath is the NDJSON file this installation appends to. Empty means
	// the stack integration is off, which is the default: nothing is emitted
	// until somebody points it somewhere.
	EventsPath string
	// PassportDir is where Agent Passport documents are written.
	PassportDir string
	// Host scopes every agent:// id to this installation, so two deployments
	// do not claim the same agent.
	Host string
	// Owner is the owning team or human on every passport.
	Owner string
	// Attestation is how the ids are bound to a workload. "none" is the honest
	// default and says the id is a name this installation chose.
	Attestation string

	// SpiffeSocket is the workload API this process asks for its own identity.
	SpiffeSocket string
}

func (c Config) On() bool { return c.EventsPath != "" }

// Emitter owns the one writer the chain is allowed to have.
//
// One file is one hash chain and a chain has a single serialization point.
// Two goroutines appending would interleave and fork it, which a verifier
// then reports as tampering rather than as concurrency.
type Emitter struct {
	// runtimeID is the SPIFFE ID this process was issued, empty when it holds none.
	runtimeID string
	cfg       Config
	mu        sync.Mutex
	w         *event.ChainedWriter
}

func Open(cfg Config) (*Emitter, error) {
	if !cfg.On() {
		return &Emitter{cfg: cfg}, nil
	}
	if cfg.Host == "" {
		cfg.Host = "costcrew.local"
	}
	if cfg.Attestation == "" {
		cfg.Attestation = "none"
	}
	if err := os.MkdirAll(filepath.Dir(cfg.EventsPath), 0o755); err != nil {
		return nil, err
	}
	w, err := event.NewChainedWriter(cfg.EventsPath)
	if err != nil {
		return nil, err
	}
	return &Emitter{cfg: cfg, w: w}, nil
}

func (e *Emitter) Close() error {
	if e.w == nil {
		return nil
	}
	return e.w.Close()
}

func (e *Emitter) On() bool { return e.w != nil }

// AgentURI is this installation's name for one analyst.
// host is this installation's trust domain, with the same default the URI
// builder uses, so the two can never disagree about what "ours" means.
func (e *Emitter) host() string {
	if h := e.cfg.Host; h != "" {
		return h
	}
	return "costcrew.local"
}

func (e *Emitter) AgentURI(name string) string {
	host := e.cfg.Host
	if host == "" {
		host = "costcrew.local"
	}
	return "agent://" + host + "/" + name
}

// Emit appends one event.
//
// Fail-open by contract: a write that fails is reported to the caller and
// never becomes the reason a page did not render. The stack's own exporters
// behave the same way, and a verifier surfaces the gap honestly rather than
// the product going quiet.
// Severities the shared envelope allows, agent-passport SPEC 6.1. A CLOSED
// enum there, so a value outside it is not a stylistic choice: a consumer that
// validates refuses the whole line, and the event this console wrote is simply
// not in anybody's record.
//
// Written here rather than trusted from call sites because this package hands
// the value through untouched. `crew.CheckGuards` defaulted to "warning" from
// the day it was written, in the band an analyst between its guard and one and
// a half times it falls into, which is the COMMON one.
var severities = map[string]bool{
	"info": true, "low": true, "medium": true, "high": true, "critical": true,
}

func (e *Emitter) Emit(kind, actor, severity string, data map[string]any, onBehalfOf []string) error {
	if e.w == nil {
		return nil
	}
	if actor == "" {
		actor = Detector
	}
	// Refused rather than corrected. A severity this package chose on a
	// caller's behalf would be this console asserting how bad something is,
	// and the caller is the only thing that knows.
	if !severities[severity] {
		return fmt.Errorf(
			"severity %q is not one of info, low, medium, high, critical; the shared "+
				"envelope's enum is closed and a consumer that validates would refuse "+
				"the whole event", severity)
	}
	// Say it in the estate's own words where the estate has one. See
	// vocabulary.go for what is mapped, what is not, and the one mapping that
	// was rejected for claiming a refusal that never happened.
	wire, payload := translate(kind, data)
	ev := event.Event{
		Schema:     event.SchemaV02,
		TS:         time.Now().UTC().Format(time.RFC3339),
		Source:     Source,
		Type:       wire,
		AgentID:    e.AgentURI(actor),
		RunID:      runOf(kind, payload),
		Severity:   severity,
		Data:       payload,
		OnBehalfOf: onBehalfOf,
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.w.Write(ev)
}

// Severity maps money onto the envelope's five levels.
//
// Money and not z: the notifier's job is to decide whether to interrupt a
// person, and a five-sigma deviation worth three dollars is not worth
// interrupting anyone for. The thresholds are round numbers on purpose,
// because somebody has to be able to argue with them.
func Severity(excess money.Cents) string {
	switch a := excess.Abs(); {
	case a >= money.Cents(5_000_00):
		return event.SeverityCritical
	case a >= money.Cents(1_000_00):
		return event.SeverityHigh
	case a >= money.Cents(200_00):
		return event.SeverityMedium
	default:
		return event.SeverityLow
	}
}

// ------------------------------------------------------------- passports

// WritePassports publishes one Agent Passport per analyst.
//
// The mapping is close to one to one because a job description and a passport
// answer the same question: who is this, what may it do, and on whose behalf.
// Nothing is invented to fill a field. Attestation defaults to "none", which
// states plainly that the id is a name this installation chose rather than
// something bound to a workload.
// PassportFor builds one analyst's document.
//
// It is exported and used by the console as well as by the writer, so what a
// person reads on the agent's card is the SAME document that is published.
// Two builders would drift, and the drift would be invisible: the page would
// keep saying spiffe-svid while the file on disk said none.
// SetRuntimeIdentity records the SPIFFE ID this process was issued, so
// passports can say what the agents inside it run as.
func (e *Emitter) SetRuntimeIdentity(id string) { e.runtimeID = id }

// withRuntimeIdentity builds the passport for an agent that has no attestation
// of its own, inside a process that does.
func (e *Emitter) withRuntimeIdentity(a crew.Analyst) passport.Passport {
	saved := a.Attestation
	a.Attestation = "spiffe-svid"
	a.AttestationDetail = e.runtimeID
	p := e.build(a)
	a.Attestation = saved
	p.Labels["attested"] = "the runtime, not this agent"
	p.Labels["attested_workload"] = e.runtimeID
	return p
}

func (e *Emitter) PassportFor(a crew.Analyst) passport.Passport {
	return e.build(a)
}

func (e *Emitter) build(a crew.Analyst) passport.Passport {
	// The RUNTIME's attested identity, when this process holds one, and only
	// where the analyst records none of its own.
	//
	// This is derived, and the difference from the derivation removed earlier
	// today is the whole point: that one read a permission list and wrote a
	// security claim. This one reads a CERTIFICATE this process was issued
	// after a workload attestor checked the user, the binary's path and its
	// SHA-256. It says "this agent runs inside a workload whose identity was
	// attested, and here is that workload's SPIFFE ID", which is true and
	// which anybody holding the trust bundle can check.
	//
	// It is the RUNTIME's identity, not the agent's. Thirty-nine analysts run
	// in one process, and a workload attestor cannot tell them apart, so the
	// label below says so rather than letting the count imply otherwise.
	if (a.Attestation == "" || a.Attestation == "none") && e.runtimeID != "" {
		return e.withRuntimeIdentity(a)
	}

	// The attestation comes from the ANALYST, not from the installation's
	// flag. The flag is the default a hire form starts from; what was actually
	// chosen at hire time is what the roster holds, and the passport is a
	// statement about this agent rather than about the server that runs it.
	method := a.Attestation
	if method == "" {
		method = e.cfg.Attestation
	}
	if method == "" {
		method = "none"
	}
	// The ANALYST's owner, not the installation's.
	//
	// The document is about this agent, and its owner field is the answer to
	// "who answers for what this spends". That is the account that hired it,
	// which the roster records and a transfer changes. Using the installation
	// flag put a different name in the passport from the one under "hired by"
	// six lines below it on the same card, and neither was labelled well
	// enough for a reader to tell which question each answered.
	owner := a.Owner
	if owner == "" || owner == "unclaimed" {
		owner = e.cfg.Owner
	}
	p := passport.Passport{
		Schema:      passport.RequiredSchema,
		ID:          e.AgentURI(a.Name),
		Owner:       owner,
		DisplayName: a.Role,
		Runtime:     Source,
		// The method AND what makes it checkable. A method alone is a word:
		// see internal/crew/attestation.go for the twelve agents an identity
		// graph stopped asking about because this console had written one.
		Attestation: &passport.Attestation{Method: method, Detail: a.AttestationDetail},
		Labels: map[string]string{
			"desk":                a.Desk,
			"state":               a.State,
			"skills":              strings.Join(a.Skills, ","),
			"rights":              strings.Join(a.Rights, ","),
			"cadence":             a.Cadence,
			"audience":            a.Audience,
			"budget_per_task_usd": a.PerTask.String(),
			"budget_monthly_usd":  a.Monthly.String(),
			"hired":               a.Hired,
			"hired_by":            a.Owner,
		},
	}
	// Whose behalf it acts on, as recorded. Falling back to the supervisor is
	// right for the seeded crew, which is routed that way, but an analyst that
	// was hired under somebody else must not be re-parented by a default.
	switch {
	case a.Parent != "":
		p.Parent = e.AgentURI(a.Parent)
	case a.Name != "supervisor":
		p.Parent = e.AgentURI("supervisor")
	}
	if a.Engine != "" {
		p.Models = []passport.Model{{Provider: a.Engine}}
	}
	// A suspended analyst's reason travels with its identity: the graph that
	// reads these should not have to ask the console why an agent is off the
	// rota.
	if a.Reason != "" {
		p.Labels["reason"] = a.Reason
	}
	for k, v := range p.Labels {
		if strings.TrimSpace(v) == "" {
			delete(p.Labels, k)
		}
	}
	return p
}

func (e *Emitter) WritePassports(roster []crew.Analyst) (int, error) {
	if e.cfg.PassportDir == "" {
		return 0, nil
	}
	if e.cfg.Owner == "" {
		return 0, fmt.Errorf("a passport with no owner is not a valid document; set the owner")
	}
	if err := os.MkdirAll(e.cfg.PassportDir, 0o755); err != nil {
		return 0, err
	}
	sorted := append([]crew.Analyst(nil), roster...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	n := 0
	removed := 0
	keep := make(map[string]bool, len(sorted))
	for _, a := range sorted {
		p := e.PassportFor(a)

		buf, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			return n, err
		}
		// Round-trip through the contract's own parser rather than trusting
		// the struct: this is the same check the identity graph runs on
		// ingest, so a document that fails here would fail there.
		if _, err := passport.Parse(buf); err != nil {
			return n, fmt.Errorf("%s: the document built here is not a valid Passport: %w", a.Name, err)
		}
		if err := os.WriteFile(filepath.Join(e.cfg.PassportDir, a.Name+".json"),
			append(buf, '\n'), 0o644); err != nil {
			return n, err
		}
		keep[a.Name+".json"] = true
		n++
	}

	// An agent taken off the roster takes its identity document with it.
	//
	// Publishing wrote the current crew and left everything else where it was,
	// so a removed agent kept a passport on disk for ever. Everything in this
	// estate reads that DIRECTORY: genaryx's onboard wizard lists it as
	// provisioned, heraldyx reads an owner out of it, idryx enriches an
	// identity from it. A console that can remove an agent and cannot
	// un-publish it is one whose removals are cosmetic everywhere except in
	// its own database.
	//
	// Only files this directory's own naming produces are removed. A
	// directory somebody else also writes into is not one to sweep.
	if entries, err := os.ReadDir(e.cfg.PassportDir); err == nil {
		for _, ent := range entries {
			name := ent.Name()
			if ent.IsDir() || !strings.HasSuffix(name, ".json") || keep[name] {
				continue
			}
			// Ours only: it has to parse as a Passport whose id is in this
			// installation's own trust domain.
			raw, err := os.ReadFile(filepath.Join(e.cfg.PassportDir, name))
			if err != nil {
				continue
			}
			doc, err := passport.Parse(raw)
			if err != nil || !strings.HasPrefix(doc.ID, "agent://"+e.host()+"/") {
				continue
			}
			if err := os.Remove(filepath.Join(e.cfg.PassportDir, name)); err != nil {
				return n, fmt.Errorf("removing the passport of a departed agent (%s): %w",
					name, err)
			}
			removed++
		}
	}
	if removed > 0 {
		log.Printf("costcrew: removed %d passport(s) of agents no longer on the roster", removed)
	}
	return n, nil
}

// Delegation is the chain an analyst acts under: the operator who asked, then
// the supervisor that routed it, then the analyst itself as the actor.
//
// Root first, and the actor is NOT repeated in its own chain: it is already
// the event's agent_id, and repeating it would read as an agent acting on its
// own behalf, which is a different claim.
func (e *Emitter) Delegation(operator, analyst string) []string {
	var chain []string
	if operator != "" {
		host := e.cfg.Host
		if host == "" {
			host = "costcrew.local"
		}
		chain = append(chain, "user://"+host+"/"+operator)
	}
	if analyst != "supervisor" {
		chain = append(chain, e.AgentURI("supervisor"))
	}
	return chain
}

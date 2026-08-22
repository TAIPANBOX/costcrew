// Command stack connects an existing CostCrew crew to the TAIPANBOX
// agent-governance stack, one analyst at a time or a whole desk at once.
//
// It is READ-ONLY against CostCrew. It opens no database, changes no page and
// writes nothing into the installation: it reads the job descriptions and the
// journal that are already on disk, and writes Agent Passports and agent-event
// NDJSON somewhere else. That constraint is deliberate. The Python CostCrew is
// frozen as the reference for the Go port, so a tool that modified it would
// invalidate the parity gate.
//
//	stack list    -crew <dir>
//	stack connect -crew <dir> -out <dir> -owner <who> [-all|-desk aws|-agent a,b]
//	stack emit    -crew <dir> -out <file> [selection]
//
// Nothing here reaches the network and nothing here spends money.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
	"github.com/TAIPANBOX/agent-stack-go/passport"
	"gopkg.in/yaml.v3"
)

// ------------------------------------------------------------ job descriptions

// jd is the subset of CostCrew's jd/<name>.yaml this tool reads. Fields it does
// not map are deliberately absent rather than parsed and dropped, so adding a
// mapping is a visible change here rather than a silent one.
type jd struct {
	Name    string `yaml:"name"`
	Role    string `yaml:"role"`
	Mission string `yaml:"mission"`
	Desk    string `yaml:"desk"`
	Model   struct {
		Provider string `yaml:"provider"`
		Model    string `yaml:"model"`
	} `yaml:"model"`
	Skills struct {
		Base        []string `yaml:"base"`
		Specialized []string `yaml:"specialized"`
	} `yaml:"skills"`
	Permissions []string `yaml:"permissions"`
	Boundaries  struct {
		Tools   []string `yaml:"tools"`
		Actions string   `yaml:"actions"`
	} `yaml:"boundaries"`
	Budget struct {
		PerTaskUSD float64 `yaml:"per_task_usd"`
		MonthlyUSD float64 `yaml:"monthly_usd"`
	} `yaml:"budget"`

	// not from the file
	path    string
	modTime time.Time
}

func loadCrew(dir string) ([]jd, error) {
	glob := filepath.Join(dir, "jd", "*.yaml")
	paths, err := filepath.Glob(glob)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		// A crew of zero is almost always a wrong -crew path, and reporting it
		// as "connected 0 agents" would look like success.
		return nil, fmt.Errorf("no job descriptions under %s (is -crew pointing at a CostCrew checkout?)", glob)
	}
	sort.Strings(paths)

	var out []jd
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var j jd
		if err := yaml.Unmarshal(raw, &j); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		if j.Name == "" {
			return nil, fmt.Errorf("%s: no name field", filepath.Base(p))
		}
		j.path = p
		if fi, err := os.Stat(p); err == nil {
			j.modTime = fi.ModTime().UTC()
		}
		out = append(out, j)
	}
	return out, nil
}

// ---------------------------------------------------------------- selection

type selection struct {
	all    bool
	desk   string
	agents string
}

func (s selection) apply(crew []jd) ([]jd, error) {
	switch {
	case s.all:
		return crew, nil
	case s.desk != "":
		var out []jd
		for _, j := range crew {
			if j.Desk == s.desk {
				out = append(out, j)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("no analyst is on desk %q; desks present: %s",
				s.desk, strings.Join(desks(crew), ", "))
		}
		return out, nil
	case s.agents != "":
		want := map[string]bool{}
		for _, n := range strings.Split(s.agents, ",") {
			if n = strings.TrimSpace(n); n != "" {
				want[n] = true
			}
		}
		var out []jd
		for _, j := range crew {
			if want[j.Name] {
				out = append(out, j)
				delete(want, j.Name)
			}
		}
		if len(want) > 0 {
			var missing []string
			for n := range want {
				missing = append(missing, n)
			}
			sort.Strings(missing)
			// Silently connecting three of four requested agents is how a crew
			// ends up half-governed with nobody noticing.
			return nil, fmt.Errorf("no such analyst: %s", strings.Join(missing, ", "))
		}
		return out, nil
	}
	return nil, fmt.Errorf("choose who to connect: -all, -desk <name>, or -agent <a,b>")
}

func desks(crew []jd) []string {
	seen := map[string]bool{}
	var out []string
	for _, j := range crew {
		if j.Desk != "" && !seen[j.Desk] {
			seen[j.Desk] = true
			out = append(out, j.Desk)
		}
	}
	sort.Strings(out)
	return out
}

// ----------------------------------------------------------------- passports

// agentURI is the stack's canonical identity for one analyst. The host segment
// scopes the id to this installation, so two CostCrew deployments do not claim
// the same agent.
func agentURI(host, name string) string {
	return "agent://" + host + "/" + name
}

// toPassport maps a job description onto the shared Passport document.
//
// Two things it deliberately does NOT do. It does not invent an attestation:
// the default is "none", which is the honest statement that this id is a name
// the installation chose rather than something cryptographically bound to a
// workload. And it does not fill Filesystem, because CostCrew analysts reach a
// figures engine rather than paths, and a declared scope nobody meant is worse
// than an absent one.
func toPassport(j jd, host, owner, supervisor string, attest string) passport.Passport {
	p := passport.Passport{
		Schema:      passport.RequiredSchema,
		ID:          agentURI(host, j.Name),
		Owner:       owner,
		DisplayName: j.Role,
		Runtime:     "costcrew",
		Attestation: &passport.Attestation{Method: attest},
		Labels:      map[string]string{},
	}
	if j.Name != supervisor {
		p.Parent = agentURI(host, supervisor)
	}
	if j.Model.Provider != "" {
		p.Models = []passport.Model{{Provider: j.Model.Provider, Model: j.Model.Model}}
	}
	if !j.modTime.IsZero() {
		p.CreatedAt = j.modTime.Format(time.RFC3339)
	}

	lab := p.Labels
	set := func(k, v string) {
		// YAML block scalars keep their trailing newline, and a label is a
		// single-line value everywhere it is displayed.
		if v = strings.TrimSpace(v); v != "" {
			lab[k] = v
		}
	}
	set("desk", j.Desk)
	set("mission", j.Mission)
	set("actions", j.Boundaries.Actions)
	set("tools", strings.Join(j.Boundaries.Tools, ","))
	set("permissions", strings.Join(j.Permissions, ","))
	set("skills", strings.Join(append(append([]string{}, j.Skills.Base...), j.Skills.Specialized...), ","))
	if j.Budget.PerTaskUSD > 0 {
		set("budget_per_task_usd", fmt.Sprintf("%.2f", j.Budget.PerTaskUSD))
	}
	if j.Budget.MonthlyUSD > 0 {
		set("budget_monthly_usd", fmt.Sprintf("%.2f", j.Budget.MonthlyUSD))
	}
	return p
}

func connect(crewDir, out, host, owner, supervisor, attest string, sel selection, dry bool) error {
	crew, err := loadCrew(crewDir)
	if err != nil {
		return err
	}
	chosen, err := sel.apply(crew)
	if err != nil {
		return err
	}
	if !dry {
		if err := os.MkdirAll(out, 0o755); err != nil {
			return err
		}
	}
	for _, j := range chosen {
		p := toPassport(j, host, owner, supervisor, attest)
		// Round-trip through the contract's own parser rather than trusting the
		// struct: this is the same check Idryx runs on ingest, so a document
		// that fails here would have failed there.
		buf, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			return err
		}
		if _, err := passport.Parse(buf); err != nil {
			return fmt.Errorf("%s: the document this tool built is not a valid Passport: %w", j.Name, err)
		}
		dest := filepath.Join(out, j.Name+".json")
		if dry {
			fmt.Printf("would write %s\n  %s  parent=%s  attestation=%s\n",
				dest, p.ID, orNone(p.Parent), attest)
			continue
		}
		if err := os.WriteFile(dest, append(buf, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Printf("%-22s -> %s\n", j.Name, dest)
	}
	verb := "connected"
	if dry {
		verb = "would connect"
	}
	fmt.Printf("\n%s %d of %d analysts, attestation %q\n", verb, len(chosen), len(crew), attest)
	if attest == "none" {
		fmt.Println("note: attestation \"none\" means the id is a name this installation chose,")
		fmt.Println("      not something bound to a workload. Idryx will read it as declared.")
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "(none, this is the supervisor)"
	}
	return s
}

// ---------------------------------------------------------------------- emit

// journalRecord is CostCrew's own event shape: its hash chain, not the stack's.
type journalRecord struct {
	Event string         `json:"event"`
	TS    float64        `json:"ts"`
	Data  map[string]any `json:"data"`
	Hash  string         `json:"hash"`
	Prev  string         `json:"prev"`
}

// severityOf maps CostCrew's vocabulary onto the envelope's five levels. Only
// events that carry a governance meaning are raised above info; everything
// else stays info so the notifier is not trained to be ignored.
func severityOf(kind string) string {
	switch {
	case strings.Contains(kind, "blocked"), strings.Contains(kind, "suspend"):
		return event.SeverityHigh
	case strings.Contains(kind, "returned"), strings.Contains(kind, "reject"):
		return event.SeverityMedium
	case strings.Contains(kind, "budget"), strings.Contains(kind, "guard"):
		return event.SeverityMedium
	default:
		return event.SeverityInfo
	}
}

func emit(crewDir, outFile, host, supervisor string, sel selection) error {
	crew, err := loadCrew(crewDir)
	if err != nil {
		return err
	}
	chosen, err := sel.apply(crew)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, j := range chosen {
		want[j.Name] = true
	}

	raw, err := os.ReadFile(filepath.Join(crewDir, "events.ndjson"))
	if err != nil {
		return fmt.Errorf("reading the crew's journal: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil {
		return err
	}
	// One file is one chain, and the writer is the single serialization point.
	// That is why this is one process writing one file, never one per agent.
	w, err := event.NewChainedWriter(outFile)
	if err != nil {
		return err
	}
	defer w.Close()

	var written, skipped, malformed int
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r journalRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			malformed++
			continue
		}
		who, _ := r.Data["assignee"].(string)
		if who == "" || !want[who] {
			// An event with no analyst on it is real (a sprint opening, an
			// operator's stamp) but it is not this agent's event, and inventing
			// an agent_id for it would put words in the journal's mouth.
			skipped++
			continue
		}
		ev := event.Event{
			Schema:   event.SchemaV02,
			TS:       time.Unix(int64(r.TS), 0).UTC().Format(time.RFC3339),
			Source:   "costcrew",
			Type:     r.Event,
			AgentID:  agentURI(host, who),
			Severity: severityOf(r.Event),
			Data:     r.Data,
		}
		if who != supervisor {
			ev.OnBehalfOf = []string{agentURI(host, supervisor)}
		}
		if err := w.Write(ev); err != nil {
			return fmt.Errorf("appending %s: %w", r.Event, err)
		}
		written++
	}

	if written == 0 {
		return fmt.Errorf("measured nothing: %d journal lines carried no event for the %d selected analysts",
			skipped, len(chosen))
	}
	fmt.Printf("wrote %d events for %d analysts -> %s\n", written, len(chosen), outFile)
	fmt.Printf("skipped %d lines with no analyst on them", skipped)
	if malformed > 0 {
		fmt.Printf(", %d malformed", malformed)
	}
	fmt.Println()
	fmt.Println("verify with: agent-conform -chain " + outFile)
	return nil
}

// ---------------------------------------------------------------------- list

func list(crewDir string) error {
	crew, err := loadCrew(crewDir)
	if err != nil {
		return err
	}
	fmt.Printf("%-22s %-10s %-28s %10s %10s\n", "ANALYST", "DESK", "ENGINE", "PER TASK", "MONTHLY")
	for _, j := range crew {
		eng := j.Model.Provider
		if j.Model.Model != "" {
			eng += " " + j.Model.Model
		}
		fmt.Printf("%-22s %-10s %-28s %10.2f %10.2f\n",
			j.Name, j.Desk, trunc(eng, 28), j.Budget.PerTaskUSD, j.Budget.MonthlyUSD)
	}
	fmt.Printf("\n%d analysts on %d desks: %s\n", len(crew), len(desks(crew)), strings.Join(desks(crew), ", "))
	return nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// ---------------------------------------------------------------------- main

func usage() {
	fmt.Fprintln(os.Stderr, `stack - connect a CostCrew crew to the agent-governance stack

  stack list    -crew <dir>
  stack connect -crew <dir> -out <dir> -owner <who> [-all | -desk <d> | -agent <a,b>]
  stack emit    -crew <dir> -out <file> [selection]

Read-only against CostCrew. Reaches no network and spends nothing.`)
}

func addSelection(fs *flag.FlagSet, s *selection) {
	fs.BoolVar(&s.all, "all", false, "every analyst")
	fs.StringVar(&s.desk, "desk", "", "everyone on one desk")
	fs.StringVar(&s.agents, "agent", "", "named analysts, comma separated")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "list":
		fs := flag.NewFlagSet("list", flag.ExitOnError)
		crew := fs.String("crew", "", "a CostCrew checkout")
		fs.Parse(os.Args[2:])
		fail(list(*crew))
	case "connect":
		fs := flag.NewFlagSet("connect", flag.ExitOnError)
		crew := fs.String("crew", "", "a CostCrew checkout")
		out := fs.String("out", "passports", "where to write Passport documents")
		host := fs.String("host", "costcrew.local", "installation host, the agent:// authority")
		owner := fs.String("owner", "", "the owning team or human (required)")
		sup := fs.String("supervisor", "supervisor", "the analyst that others act on behalf of")
		attest := fs.String("attestation", "none", "none|oidc|spiffe-svid|enclave-key|mtls-cert")
		dry := fs.Bool("dry-run", false, "print what would be written")
		var sel selection
		addSelection(fs, &sel)
		fs.Parse(os.Args[2:])
		if *owner == "" {
			fmt.Fprintln(os.Stderr, "-owner is required: a Passport with no owner is not a valid document")
			os.Exit(2)
		}
		fail(connect(*crew, *out, *host, *owner, *sup, *attest, sel, *dry))
	case "emit":
		fs := flag.NewFlagSet("emit", flag.ExitOnError)
		crew := fs.String("crew", "", "a CostCrew checkout")
		out := fs.String("out", "events/costcrew.ndjson", "the agent-event NDJSON file to append to")
		host := fs.String("host", "costcrew.local", "installation host, the agent:// authority")
		sup := fs.String("supervisor", "supervisor", "the analyst that others act on behalf of")
		var sel selection
		addSelection(fs, &sel)
		fs.Parse(os.Args[2:])
		fail(emit(*crew, *out, *host, *sup, sel))
	default:
		usage()
		os.Exit(2)
	}
}

func fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "stack:", err)
		os.Exit(1)
	}
}

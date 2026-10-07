// Command idryxsource writes this console's roster as an idryx `agents`
// source, so the identity graph can carry the agents this console hires.
//
// # Why this exists as well as the passports
//
// idryx's -passports flag ENRICHES an identity that is already in the graph:
// it fills in owner, runtime, parent and attestation. It does not create one.
// So a console that writes passports and nothing else is invisible to the
// graph, which is exactly what "CostCrew's agents are not in Agent 360" turned
// out to mean.
//
// This writes the other half, and only the half idryx asks for. Everything
// else about an agent - its guards, its skills, its first-pass rate - stays in
// this console, because the identity graph is about who a thing is and what it
// may reach, not about how well it is doing its job.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// agent is one entry of idryx's `agents` source, and the field names are its
// own rather than this console's.
type agent struct {
	ID         string   `json:"id"`
	Runtime    string   `json:"runtime"`
	OnBehalfOf string   `json:"onBehalfOf"`
	Owner      string   `json:"owner"`
	Created    string   `json:"created"`
	Tools      []string `json:"tools"`
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run is main minus os: the arguments, the two streams and an exit status, so
// a test can read what a person at a terminal would read and see what was
// written to disk.
func run(args []string, stdout, stderr io.Writer) int {
	// Declared through the package-level flag.String calls on a fresh
	// CommandLine, the shape internal/manifest reads components.json against
	// (tools/bench/main.go says why at length); ContinueOnError so a test gets a
	// bad flag back as a status and not as os.Exit.
	flag.CommandLine = flag.NewFlagSet("idryxsource", flag.ContinueOnError)
	flag.CommandLine.SetOutput(stderr)
	dir := flag.String("data", ".", "the console's data directory")
	host := flag.String("host", "costcrew.local", "the agent:// authority, matching -stack-host")
	out := flag.String("out", "-", "where to write it; - is stdout")
	if err := flag.CommandLine.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	st, err := store.Open(*dir)
	if err != nil {
		return fail(stderr, err)
	}
	defer st.Close()

	roster, err := crew.Roster(st.DB())
	if err != nil {
		return fail(stderr, err)
	}
	sort.Slice(roster, func(i, j int) bool { return roster[i].Name < roster[j].Name })

	agents := make([]agent, 0, len(roster))
	for _, a := range roster {
		e := agent{
			ID:      "agent://" + *host + "/" + a.Name,
			Runtime: "costcrew",
			Owner:   a.Owner,
			// The RIGHTS, not the skills. idryx's graph is about what an
			// identity may reach; a skill is what this console asked it to be
			// good at, which is a different question and not one an identity
			// graph can check.
			Tools: append([]string(nil), a.Rights...),
		}
		if a.Parent != "" {
			e.OnBehalfOf = "agent://" + *host + "/" + a.Parent
		}
		if a.Hired != "" {
			// idryx's own format. A date with no time is not what it reads.
			if t, err := time.Parse("2006-01-02", a.Hired); err == nil {
				e.Created = t.UTC().Format(time.RFC3339)
			}
		}
		agents = append(agents, e)
	}

	buf, err := json.MarshalIndent(map[string]any{"agents": agents}, "", "  ")
	if err != nil {
		return fail(stderr, err)
	}
	buf = append(buf, '\n')
	if *out == "-" {
		_, _ = stdout.Write(buf)
		return 0
	}
	if err := os.WriteFile(*out, buf, 0o644); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stderr, "%d agents written to %s\n", len(agents), *out)
	return 0
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "idryxsource:", err)
	return 1
}

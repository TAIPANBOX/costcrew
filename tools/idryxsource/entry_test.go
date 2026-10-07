package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// idryx reads "tools" as the list of what an identity may reach. An agent
// holding no rights reaches nothing, and that is the empty list, not null:
// a reader that ranges over the field or checks its length treats null as
// "not stated", which for an identity graph is the opposite claim.
func TestAnAgentWithNoRightsHasAnEmptyToolsList(t *testing.T) {
	for _, rights := range [][]string{nil, {}} {
		buf, err := json.Marshal(entryFor(crew.Analyst{Name: "idle", Owner: "o", Rights: rights}, "costcrew.test"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(buf), `"tools":[]`) {
			t.Errorf("rights %#v: written as %s, want \"tools\":[]", rights, buf)
		}
	}
}

func TestAnEntryCarriesTheRightsTheAgentHolds(t *testing.T) {
	a := crew.Analyst{Name: "triage-aws", Owner: "pat", Parent: "supervisor",
		Rights: []string{"figures-read", "sql-readonly"}, Hired: "2026-09-01"}
	e := entryFor(a, "costcrew.test")
	if e.ID != "agent://costcrew.test/triage-aws" || e.OnBehalfOf != "agent://costcrew.test/supervisor" {
		t.Errorf("identity: %+v", e)
	}
	if strings.Join(e.Tools, ",") != "figures-read,sql-readonly" {
		t.Errorf("tools %v", e.Tools)
	}
	if e.Created != "2026-09-01T00:00:00Z" {
		t.Errorf("created %q", e.Created)
	}
	// The list is the entry's own, not the roster's slice.
	a.Rights[0] = "changed"
	if e.Tools[0] != "figures-read" {
		t.Errorf("the entry shares its backing array with the roster")
	}
}

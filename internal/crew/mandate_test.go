package crew_test

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

func seeded(t *testing.T) []crew.Analyst {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := crew.SeedRoster(st.DB(), "yurii"); err != nil {
		t.Fatal(err)
	}
	r, err := crew.Roster(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if len(r) != len(world.Crew) {
		t.Fatalf("seeded %d analysts, the fixture has %d", len(r), len(world.Crew))
	}
	return r
}

// Every seeded analyst arrives with a mandate.
//
// The failure this catches is not cosmetic. An agent card that shows no
// rights states that the agent can reach nothing, and a card that shows no
// mission gives a person signing it off nothing to sign off on. Both were
// true of all thirty-six, and both read as "this console has no data" rather
// than as the bug they were.
func TestEverySeededAnalystArrivesWithAMandate(t *testing.T) {
	for _, a := range seeded(t) {
		if a.Mission == "" {
			t.Errorf("%s: no mission", a.Name)
		}
		if a.Cadence == "" || a.Audience == "" {
			t.Errorf("%s: reports %q to %q", a.Name, a.Cadence, a.Audience)
		}
		if a.Hired == "" {
			t.Errorf("%s: no hire date", a.Name)
		}
		// A suspended analyst is the one case where empty rights is the
		// correct answer, and the card says why.
		if len(a.Rights) == 0 && a.State != string(world.Suspended) {
			t.Errorf("%s is %s and holds no rights, so it cannot do the job it was hired for",
				a.Name, a.State)
		}
		for _, r := range a.Rights {
			if !strings.Contains(strings.Join(crew.Rights, ","), r) {
				t.Errorf("%s holds %q, which is not a right this console defines", a.Name, r)
			}
		}
	}
}

// The crew is not thirty-six copies of one job.
//
// Variety is the whole point of the fixture: a console whose agents all report
// weekly to "the desk" never gets its filters, its sorting or its unhappy
// paths looked at, because there is nothing to tell apart.
func TestTheCrewIsVaried(t *testing.T) {
	r := seeded(t)
	count := func(f func(crew.Analyst) string) map[string]int {
		out := map[string]int{}
		for _, a := range r {
			out[f(a)]++
		}
		return out
	}
	for what, got := range map[string]map[string]int{
		"cadence":     count(func(a crew.Analyst) string { return a.Cadence }),
		"audience":    count(func(a crew.Analyst) string { return a.Audience }),
		"attestation": count(func(a crew.Analyst) string { return a.Attestation }),
		"parent":      count(func(a crew.Analyst) string { return a.Parent }),
		"hired":       count(func(a crew.Analyst) string { return a.Hired }),
	} {
		if len(got) < 2 {
			t.Errorf("%s: every analyst has the same value, %v", what, got)
		}
	}
	// The delegation tree has depth: somebody answers to somebody who is not
	// the supervisor.
	depth := false
	for _, a := range r {
		if a.Parent != "" && a.Parent != "supervisor" {
			depth = true
			break
		}
	}
	if !depth {
		t.Error("every analyst answers directly to the supervisor, so the delegation graph is a list")
	}
}

// Backfill fills blanks and touches nothing else.
func TestBackfillLeavesADecisionAlone(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := crew.SeedRoster(st.DB(), "yurii"); err != nil {
		t.Fatal(err)
	}
	// Somebody re-briefed one analyst by hand.
	if _, err := st.DB().Exec(`UPDATE analysts SET mission='watch the azure bill only',
		cadence='on-request', attestation='enclave-key' WHERE name='triage-aws'`); err != nil {
		t.Fatal(err)
	}
	if _, err := crew.BackfillMandate(st.DB(), "yurii"); err != nil {
		t.Fatal(err)
	}
	a, err := crew.GetAnalyst(st.DB(), "triage-aws")
	if err != nil {
		t.Fatal(err)
	}
	if a.Mission != "watch the azure bill only" {
		t.Errorf("backfill overwrote a mission somebody wrote: %q", a.Mission)
	}
	if a.Cadence != "on-request" || a.Attestation != "enclave-key" {
		t.Errorf("backfill overwrote a decision: cadence %q, attestation %q", a.Cadence, a.Attestation)
	}
}

package crew_test

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
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
		"cadence":  count(func(a crew.Analyst) string { return a.Cadence }),
		"audience": count(func(a crew.Analyst) string { return a.Audience }),
		"parent":   count(func(a crew.Analyst) string { return a.Parent }),
		"hired":    count(func(a crew.Analyst) string { return a.Hired }),
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

// Seeding claims no attestation at all.
//
// This test replaces one that asserted the OPPOSITE, and the replacement is
// the point rather than a tidy-up. The old seeding derived a method from an
// agent's permissions - hold close-covered, be issued spiffe-svid - and a test
// asserted that the field varied, which it did, and which was exactly the bug:
// twelve agents carried a security claim computed from a permission list, and
// idryx's bom_incomplete stopped flagging them because the console had told
// the graph they were bound to something.
//
// An attestation is recorded when somebody records it, with the evidence that
// makes it checkable. Nothing about a permission list can produce one.
func TestSeedingClaimsNoAttestation(t *testing.T) {
	for _, a := range seeded(t) {
		if a.Attestation != "none" {
			t.Errorf("%s was seeded claiming %q; nothing attested it", a.Name, a.Attestation)
		}
		if a.AttestationDetail != "" {
			t.Errorf("%s carries evidence %q for an attestation nobody made",
				a.Name, a.AttestationDetail)
		}
	}
}

// A method without its evidence is refused, and evidence without a method too.
func TestAnAttestationHasToCarryItsEvidence(t *testing.T) {
	for _, tc := range []struct {
		method, detail string
		ok             bool
		why            string
	}{
		{"none", "", true, "the honest default"},
		{"none", "spiffe://x/y", false, "nothing attested it, so there is nothing to point at"},
		{"oidc", "", false, "a method on its own is a word"},
		{"oidc", "https://login.example.com", true, "an issuer that can be looked up"},
		{"oidc", "login.example.com", false, "an issuer is an https URL"},
		{"oidc", "http://login.example.com", false, "http is not an issuer"},
		{"spiffe-svid", "", false, "no SPIFFE ID"},
		{"spiffe-svid", "spiffe://example.com/ns/finops/sa/triage", true, "a real SVID"},
		{"spiffe-svid", "example.com/triage", false, "not a SPIFFE ID"},
		{"mtls-cert", strings.Repeat("ab", 32), true, "a SHA-256 fingerprint"},
		{"mtls-cert", "CN=triage-aws,O=Example", true, "a subject DN"},
		{"mtls-cert", "the blue one", false, "neither"},
		{"magic-words", "anything", false, "not a method this contract allows"},
	} {
		err := crew.ValidAttestation(tc.method, tc.detail)
		if tc.ok && err != nil {
			t.Errorf("%s/%q was refused (%s): %v", tc.method, tc.detail, tc.why, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s/%q was accepted, and it is %s", tc.method, tc.detail, tc.why)
		}
	}
}

// Hiring refuses a method with nothing behind it, at the door.
func TestHiringRefusesAnAttestationWithNoEvidence(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := crew.SeedRoster(st.DB(), "yurii"); err != nil {
		t.Fatal(err)
	}
	base := crew.Analyst{
		Name: "night-desk", Role: "night watch", Desk: "aws", Engine: "kimi-standard",
		Skills: []string{"anomaly-triage"}, Rights: []string{"figures-read"},
		PerTask: money.Cents(1200), Monthly: money.Cents(9000),
		Cadence: "daily", Audience: "the desk", Owner: "yurii", Parent: "supervisor",
	}
	bare := base
	bare.Attestation = "spiffe-svid"
	if err := crew.Hire(st.DB(), bare); err == nil {
		t.Error("an agent was hired claiming spiffe-svid with no SPIFFE ID")
	}
	good := base
	good.Attestation = "spiffe-svid"
	good.AttestationDetail = "spiffe://costcrew.local/ns/finops/sa/night-desk"
	if err := crew.Hire(st.DB(), good); err != nil {
		t.Fatalf("an attestation with its evidence was refused: %v", err)
	}
	got, err := crew.GetAnalyst(st.DB(), "night-desk")
	if err != nil {
		t.Fatal(err)
	}
	if got.AttestationDetail != good.AttestationDetail {
		t.Errorf("the evidence did not survive the round trip: %q", got.AttestationDetail)
	}
}

// The migration clears what the console invented and leaves what a person
// recorded.
func TestClearingFabricatedKeepsARecordedOne(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := crew.SeedRoster(st.DB(), "yurii"); err != nil {
		t.Fatal(err)
	}
	// One invented the old way, one recorded properly.
	if _, err := st.DB().Exec(`UPDATE analysts SET attestation='oidc',
		attestation_detail='' WHERE name='forecaster'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE analysts SET attestation='oidc',
		attestation_detail='https://login.example.com' WHERE name='chargeback'`); err != nil {
		t.Fatal(err)
	}
	n, err := crew.ClearFabricated(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("cleared %d, wanted 1", n)
	}
	if a, _ := crew.GetAnalyst(st.DB(), "forecaster"); a.Attestation != "none" {
		t.Errorf("an invented attestation survived: %q", a.Attestation)
	}
	if a, _ := crew.GetAnalyst(st.DB(), "chargeback"); a.Attestation != "oidc" {
		t.Errorf("a recorded attestation was cleared: %q", a.Attestation)
	}
}

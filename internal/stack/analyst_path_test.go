package stack_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	asgpassport "github.com/TAIPANBOX/agent-stack-go/v2/passport"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/stack"
)

// Every agent id this console mints sits under costcrew/. One gateway client
// key bound to agent://<host>/costcrew/* then covers the whole crew, and no
// other agent of the same trust domain.
//
// @decided 2026-10-18 (paraphrased): the analysts, the supervisor and the
// detector are all this console's agents, so all of them carry its path.
func TestAnalystIDsLiveUnderTheCostcrewPath(t *testing.T) {
	cases := map[string]string{
		"triage-aws": "agent://taipanbox.dev/costcrew/triage-aws",
		"supervisor": "agent://taipanbox.dev/costcrew/supervisor",
		"detector":   "agent://taipanbox.dev/costcrew/detector",
	}
	for name, want := range cases {
		if got := stack.AgentURI("taipanbox.dev", name); got != want {
			t.Errorf("AgentURI(%q) = %q, want %q", name, got, want)
		}
	}
	if got, want := stack.AgentURI("", "triage-aws"), "agent://costcrew.local/costcrew/triage-aws"; got != want {
		t.Errorf("with no host, AgentURI = %q, want %q", got, want)
	}
}

// The grammar agent-passport holds every agent:// URI to, at the longest name
// the roster allows and at a host that still fits.
func TestEveryMintedIDAtTheLongestNameIsAPassportURI(t *testing.T) {
	longest := strings.Repeat("a", 40)
	for _, host := range []string{"a.b", "taipanbox.dev", strings.Repeat("h", 197)} {
		got := stack.AgentURI(host, longest)
		if err := asgpassport.ValidateAgentURI(got); err != nil {
			t.Errorf("host of %d bytes: %q is not a passport URI: %v", len(host), got, err)
		}
		if want := "agent://" + host + "/costcrew/" + longest; got != want {
			t.Errorf("host of %d bytes: %q, want %q", len(host), got, want)
		}
	}
}

// A host one byte longer than that produces an id over the 255-byte cap, and
// every event for every analyst would then be refused by the schema. Refused
// at Open, where it can be said, rather than written.
func TestOpenRefusesAHostTooLongForAnAnalystID(t *testing.T) {
	dir := t.TempDir()
	em, err := stack.Open(stack.Config{
		EventsPath:  filepath.Join(dir, "events", "costcrew.ndjson"),
		PassportDir: filepath.Join(dir, "passports"),
		Host:        strings.Repeat("h", 198),
		Owner:       "finops",
	})
	if err == nil {
		em.Close()
		t.Fatal("a host whose analyst ids are over 255 bytes was accepted: " +
			"every event would name an agent no consumer can parse")
	}
}

// The passport sweep removes departed agents of THIS console. An agent of the
// same trust domain that is not under the costcrew path belongs to somebody
// else, so it stays.
func TestSweepingLeavesOtherAgentsOfTheSameTrustDomainAlone(t *testing.T) {
	dir := t.TempDir()
	pdir := filepath.Join(dir, "p")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	other := `{"schema":"taipanbox.dev/agent-passport/v0.1",` +
		`"id":"agent://costcrew.test/treasury-desk/recon","owner":"olena"}`
	if err := os.WriteFile(filepath.Join(pdir, "recon.json"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	em, err := stack.Open(stack.Config{
		EventsPath: filepath.Join(dir, "e.ndjson"), PassportDir: pdir,
		Host: "costcrew.test", Owner: "finops",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer em.Close()
	if _, err := em.WritePassports([]crew.Analyst{
		{Name: "triage-aws", Role: "triage", Desk: "aws", State: "active", Owner: "finops"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pdir, "recon.json")); err != nil {
		t.Errorf("an agent of this trust domain outside the costcrew path was swept: %v", err)
	}
}

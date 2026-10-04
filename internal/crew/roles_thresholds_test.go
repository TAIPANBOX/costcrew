package crew_test

// The seven thresholds in roles.yaml, after the owner decided them on
// 2026-10-04: the two money thresholds are halved, the other five keep their
// draft values and stop being drafts. The provenance field is a closed
// vocabulary now, so a threshold cannot quietly claim an authority it does not
// have, and a public repository never carries the owner's name as a marker.

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// displayCents reads "USD 2,500 per anomaly" or "USD 12,500" as cents, so the
// text a person reads on the card and the figure the code compares can never
// say two different amounts.
func displayCents(t *testing.T, value string) int64 {
	t.Helper()
	rest := strings.TrimPrefix(value, "USD ")
	if rest == value {
		t.Fatalf("threshold display value %q does not start with \"USD \"", value)
	}
	num := strings.Fields(rest)[0]
	n, err := strconv.ParseInt(strings.ReplaceAll(num, ",", ""), 10, 64)
	if err != nil {
		t.Fatalf("threshold display value %q: %v", value, err)
	}
	return n * 100
}

// Red first: the draft said USD 5,000 (500000 cents).
func TestTAnomalyIsTwoAndAHalfThousand(t *testing.T) {
	th, ok := crew.ThresholdFor("T.anomaly")
	if !ok {
		t.Fatal("T.anomaly is missing from roles.yaml")
	}
	if th.ValueCents != 250000 {
		t.Errorf("T.anomaly value_cents = %d, want 250000 (USD 2,500): the owner halved the draft's USD 5,000", th.ValueCents)
	}
	if got := displayCents(t, th.Value); got != th.ValueCents {
		t.Errorf("T.anomaly displays %q (%d cents) but compares %d cents: the card and the code disagree",
			th.Value, got, th.ValueCents)
	}
}

// Red first: the draft said USD 25,000 (2500000 cents). Nothing in the code
// reads T.urgent yet; the number is the supervisor's own text and the card's,
// and it is pinned here so the two money thresholds move together.
func TestTUrgentIsTwelveAndAHalfThousand(t *testing.T) {
	th, ok := crew.ThresholdFor("T.urgent")
	if !ok {
		t.Fatal("T.urgent is missing from roles.yaml")
	}
	if th.ValueCents != 1250000 {
		t.Errorf("T.urgent value_cents = %d, want 1250000 (USD 12,500): the owner halved the draft's USD 25,000", th.ValueCents)
	}
	if got := displayCents(t, th.Value); got != th.ValueCents {
		t.Errorf("T.urgent displays %q (%d cents) but compares %d cents: the card and the code disagree",
			th.Value, got, th.ValueCents)
	}
}

// Red first: all seven carried "@claude 2026-09-02, draft".
func TestEveryThresholdIsMarkedDecidedOnTheDayItWasDecided(t *testing.T) {
	for _, name := range []string{"T.anomaly", "T.firstpass", "T.stale", "T.stale_days", "T.untagged", "T.migration", "T.urgent"} {
		th, ok := crew.ThresholdFor(name)
		if !ok {
			t.Errorf("%s is missing from roles.yaml", name)
			continue
		}
		if !strings.HasPrefix(th.Provenance, "@decided 2026-10-04") {
			t.Errorf("%s provenance = %q, want it to begin @decided 2026-10-04", name, th.Provenance)
		}
	}
}

// The unchanged five keep their draft values: deciding them is not changing
// them, and a reader of the diff should be able to see that nothing else moved.
func TestTheFiveThresholdsTheOwnerKeptKeepTheirValues(t *testing.T) {
	want := map[string]string{
		"T.firstpass":  "80% over two sprints",
		"T.stale":      "3",
		"T.stale_days": "7",
		"T.untagged":   "10% of the desk's month",
		"T.migration":  "15%",
	}
	for name, value := range want {
		th, ok := crew.ThresholdFor(name)
		if !ok {
			t.Errorf("%s is missing from roles.yaml", name)
			continue
		}
		if th.Value != value {
			t.Errorf("%s = %q, want %q: the owner kept this value", name, th.Value, value)
		}
	}
}

// Red first (by compile: crew.ValidProvenance does not exist before this
// change). The vocabulary is `@claude`, `@decided YYYY-MM-DD` and
// `@measured <how> YYYY-MM-DD`; a marker that names the owner is refused because a public
// repository carries paraphrased decisions, never the owner's name as a marker.
func TestProvenanceVocabulary(t *testing.T) {
	good := []string{
		"@claude 2026-09-02, draft",
		"@claude",
		"@decided 2026-10-04",
		"@decided 2026-10-04, halved from USD 5,000",
		"@measured go test ./internal/crew -run TestX 2026-10-04",
	}
	for _, s := range good {
		if err := crew.ValidProvenance(s); err != nil {
			t.Errorf("ValidProvenance(%q) = %v, want accepted", s, err)
		}
	}
	bad := map[string]string{
		"empty":                        "",
		"blank":                        "   ",
		"the owner's name as a marker": "@owner 2026-10-04",
		"decided with no date":         "@decided",
		"decided with a word as date":  "@decided tomorrow",
		"decided with an impossible":   "@decided 2026-13-45",
		"measured with no how":         "@measured 2026-10-04",
		"measured with no date":        "@measured go test ./...",
		"leading space":                " @decided 2026-10-04",
		"claude lookalike":             "@claudex 2026-09-02",
		"decided lookalike":            "@decidedly 2026-10-04",
		"newline smuggling a second":   "@decided 2026-10-04\n@owner 2026-10-04",
		"control character":            "@decided 2026-10-04\x00",
		"no marker at all":             "the owner decided this",
		"far too long":                 "@decided 2026-10-04, " + strings.Repeat("x", 500),
	}
	for name, s := range bad {
		if err := crew.ValidProvenance(s); err == nil {
			t.Errorf("%s: ValidProvenance(%q) accepted it, want refused", name, s)
		}
	}
}

// provenanceLine matches one threshold's provenance line in roles.yaml.
var provenanceLine = regexp.MustCompile(`(?m)^    provenance: ".*"\n`)

// plantedRoles writes roles.yaml to a temp file with its FIRST threshold
// provenance line replaced (matched by pattern, so it plants over whatever
// wording the file carries) and returns the path, for the shell gate's own
// ROLES_YAML override. The embedded copy every other package builds against is
// untouched.
func plantedRoles(t *testing.T, replacement string) string {
	t.Helper()
	src, err := os.ReadFile("roles.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !provenanceLine.Match(src) {
		t.Fatal("roles.yaml has no threshold provenance line to plant over")
	}
	first := true
	out := provenanceLine.ReplaceAllFunc(src, func(m []byte) []byte {
		if first {
			first = false
			return []byte(replacement)
		}
		return m
	})
	dst := t.TempDir() + "/roles.yaml"
	if err := os.WriteFile(dst, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return dst
}

func runRolesGateOn(t *testing.T, path string) (string, error) {
	t.Helper()
	cmd := exec.Command("../../scripts/roles-are-bound.sh")
	cmd.Env = append(os.Environ(), "ROLES_YAML="+path)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Red first: scripts/roles-are-bound.sh read no provenance at all, so a
// threshold claiming the owner's name, or claiming nothing, passed it.
func TestRolesAreBoundRefusesAThresholdWithAnUnrecognisedProvenance(t *testing.T) {
	cases := map[string]string{
		"the owner's name as a marker": `provenance: "@owner 2026-10-04"`,
		"no marker at all":             `provenance: "decided by somebody"`,
		"a decided with no date":       `provenance: "@decided"`,
	}
	for name, replacement := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := runRolesGateOn(t, plantedRoles(t, "    "+replacement+"\n"))
			if err == nil {
				t.Fatalf("the gate passed a threshold with %s:\n%s", name, out)
			}
			if !strings.Contains(out, "UNRECOGNISED PROVENANCE") {
				t.Errorf("the gate failed, but not saying UNRECOGNISED PROVENANCE:\n%s", out)
			}
		})
	}
}

// A threshold with no provenance line at all is the same fault from the other
// side: nothing says where its value came from.
func TestRolesAreBoundRefusesAThresholdWithNoProvenance(t *testing.T) {
	out, err := runRolesGateOn(t, plantedRoles(t, ""))
	if err == nil {
		t.Fatalf("the gate passed a threshold with no provenance line:\n%s", out)
	}
	if !strings.Contains(out, "UNRECOGNISED PROVENANCE") {
		t.Errorf("the gate failed, but not saying UNRECOGNISED PROVENANCE:\n%s", out)
	}
}

// The gate must not fire on a provenance that is in the vocabulary: a measured
// one with its how and date is as good as a decided one.
func TestRolesAreBoundAcceptsAMeasuredProvenance(t *testing.T) {
	out, err := runRolesGateOn(t, plantedRoles(t,
		`    provenance: "@measured go test ./internal/crew -run TestX 2026-10-04"`+"\n"))
	if err != nil {
		t.Fatalf("the gate refused an @measured provenance with its how and date:\n%s", out)
	}
}

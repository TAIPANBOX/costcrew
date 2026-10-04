package crew_test

// The class lists of every analyst family, written from the prose, and the gate
// that refuses an empty one. `@decided 2026-10-04`: eight families had an empty
// decides_alone and five an empty hands_up, each with only prose beside it; the
// lists are written from that prose, and a list that stays empty needs a
// reasoned exemption beside it.

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// What each of the thirteen empty lists became. A list left empty is named with
// nil and must carry its exemption instead (see the test below this one).
var decidedAlone = map[string][]string{
	"finops-partner":         {"commentary.variance", "commentary.showback"},
	"ai-spend-analyst":       {"commentary.variance"},
	"executive-reporter":     {"commentary.variance"},
	"governance-analyst":     nil,
	"data-quality-analyst":   nil,
	"benchmarking-analyst":   nil,
	"sustainability-analyst": nil,
	"intake-triage":          nil,
}

var handedUp = map[string][]string{
	"benchmarking-analyst":   nil,
	"sustainability-analyst": {"explainer.publish"},
	"deep-analysis":          {"agent.*"},
	"intake-triage":          nil,
	"migration-watch":        {"anomaly.accept"},
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func family(t *testing.T, name string) crew.JobDescription {
	t.Helper()
	r, ok := crew.RoleFor(name)
	if !ok {
		t.Fatalf("no role family %q", name)
	}
	return r
}

// Red first: all eight were empty.
func TestTheDecidesAloneListsAreWrittenFromTheProse(t *testing.T) {
	for name, want := range decidedAlone {
		got := family(t, name).DecidesAlone
		if !equalStrings(got, want) {
			t.Errorf("%s decides_alone = %v, want %v", name, got, want)
		}
	}
}

// Red first: all five were empty (deep-analysis, migration-watch and
// sustainability-analyst are written; the other two are exempt).
func TestTheHandsUpListsAreWrittenFromTheProse(t *testing.T) {
	for name, want := range handedUp {
		got := family(t, name).HandsUp
		if !equalStrings(got, want) {
			t.Errorf("%s hands_up = %v, want %v", name, got, want)
		}
	}
}

// Red first: the exemption fields did not exist, and nine lists were empty with
// nothing to say why.
func TestEveryAnalystFamilyHasBothListsOrAReasonedExemption(t *testing.T) {
	const minReason = 40
	for _, r := range crew.AllRoles() {
		if r.Link != "analyst" {
			continue
		}
		for _, c := range []struct {
			field, exempt string
			list          []string
		}{
			{"decides_alone", r.DecidesAloneExempt, r.DecidesAlone},
			{"hands_up", r.HandsUpExempt, r.HandsUp},
		} {
			switch {
			case len(c.list) == 0 && len(strings.TrimSpace(c.exempt)) < minReason:
				t.Errorf("%s: empty %s and no reasoned %s_exempt (%q)", r.Family, c.field, c.field, c.exempt)
			case len(c.list) > 0 && c.exempt != "":
				t.Errorf("%s: lists %s and also carries an exemption for it: %q", r.Family, c.field, c.exempt)
			}
		}
	}
}

// The exemptions are the ones the PR names, and only those: a new one needs a
// decision, not a quiet edit to roles.yaml.
func TestTheExemptionsAreExactlyTheOnesDecided(t *testing.T) {
	wantAlone := map[string]bool{"governance-analyst": true, "data-quality-analyst": true,
		"benchmarking-analyst": true, "sustainability-analyst": true, "intake-triage": true}
	wantUp := map[string]bool{"benchmarking-analyst": true, "intake-triage": true}
	for _, r := range crew.AllRoles() {
		if got := r.DecidesAloneExempt != ""; got != wantAlone[r.Family] {
			t.Errorf("%s: decides_alone exempt = %v, want %v", r.Family, got, wantAlone[r.Family])
		}
		if got := r.HandsUpExempt != ""; got != wantUp[r.Family] {
			t.Errorf("%s: hands_up exempt = %v, want %v", r.Family, got, wantUp[r.Family])
		}
	}
}

// The lists are not decoration: MayDecide and Escalates read them.
func TestMayDecideAndEscalatesFollowTheWrittenLists(t *testing.T) {
	for _, c := range []struct{ role, class string }{
		{"finops-partner", "commentary.variance"},
		{"finops-partner", "commentary.showback"},
		{"ai-spend-analyst", "commentary.variance"},
		{"executive-reporter", "commentary.variance"},
	} {
		if may, why := crew.MayDecide(c.role, c.class); !may {
			t.Errorf("MayDecide(%q, %q) = false (%s), want true", c.role, c.class, why)
		}
	}
	// What an exempt family does not list stays refused, so an exemption is not
	// a licence.
	for _, c := range []struct{ role, class string }{
		{"governance-analyst", "commentary.variance"},
		{"data-quality-analyst", "data.halt"},
		{"sustainability-analyst", "explainer.publish"},
		{"intake-triage", "anomaly.explain"},
	} {
		if may, _ := crew.MayDecide(c.role, c.class); may {
			t.Errorf("MayDecide(%q, %q) = true, want false", c.role, c.class)
		}
	}
	for _, c := range []struct{ role, class, to string }{
		{"sustainability-analyst", "explainer.publish", "supervisor"},
		{"deep-analysis", "agent.*", "owner"},
		{"migration-watch", "anomaly.accept", "supervisor"},
	} {
		if to, ok := crew.Escalates(c.role, c.class); !ok || to != c.to {
			t.Errorf("Escalates(%q, %q) = (%q, %v), want (%q, true)", c.role, c.class, to, ok, c.to)
		}
	}
}

// Red first: the vocabulary of a restricted analyst that "proposes only" was
// empty, so its deliverable could name no option. It hands publishing up now,
// so a deliverable of its own owes an options block naming it, which is the
// shape of "propose only".
func TestARestrictedSustainabilityAnalystNowOwesAnOptionsBlock(t *testing.T) {
	r := family(t, "sustainability-analyst")
	if !crew.ValidClassesFor(r)["explainer.publish"] {
		t.Errorf("ValidClassesFor(sustainability-analyst) = %v, want explainer.publish in it", crew.ValidClassesFor(r))
	}
	if crew.AllowsNoOptions(r) {
		t.Error("a sustainability deliverable may still skip the options block, although it now hands publishing up")
	}
	// And the family that stays exempt in both lists is unchanged: it may still
	// end a deliverable in no options, because it has nothing to attach one to.
	if !crew.AllowsNoOptions(family(t, "benchmarking-analyst")) {
		t.Error("benchmarking-analyst owes an options block, but both its lists are exempt")
	}
}

// Red first: the never list had five entries.
func TestTheNeverListCarriesTheBlockedClause(t *testing.T) {
	never := crew.Never()
	if len(never) != 6 {
		t.Fatalf("Never() has %d entries %v, want 6", len(never), never)
	}
	found := false
	for _, v := range never {
		if v == "act on a task somebody blocked" {
			found = true
		}
	}
	if !found {
		t.Errorf("Never() = %v, missing the clause about a blocked task", never)
	}
	// The card and the prompt show the full sentence; every clause of the list is
	// in it, in words.
	for _, v := range never {
		if !strings.Contains(crew.NeverFullText(), v) {
			t.Errorf("NeverFullText() does not contain the never entry %q", v)
		}
	}
}

// Red first: nothing bound any never clause to a test.
func TestTheBlockedClauseIsBoundToTheRunnersTest(t *testing.T) {
	var test string
	for _, nb := range crew.NeverBindings() {
		if nb.Verb == "act on a task somebody blocked" {
			test = nb.Test
		}
	}
	if test != "TestABlockedTaskIsNotWorkedAround" {
		t.Fatalf("the blocked-task clause is bound to %q, want TestABlockedTaskIsNotWorkedAround", test)
	}
}

// ---------------------------------------------------- the shell gate, planted

// yamlWith writes roles.yaml with one exact edit to a temp file and returns its
// path, for the shell gate's own ROLES_YAML override. The embedded copy every
// other package builds against is untouched.
func yamlWith(t *testing.T, edits ...[2]string) string {
	t.Helper()
	src, err := os.ReadFile("roles.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, e := range edits {
		if !strings.Contains(s, e[0]) {
			t.Fatalf("roles.yaml has no %q to plant over", e[0])
		}
		s = strings.Replace(s, e[0], e[1], 1)
	}
	dst := t.TempDir() + "/roles.yaml"
	if err := os.WriteFile(dst, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return dst
}

// partnerLists is the finops-partner family's decides_alone line with the line
// under it: the same two-class list belongs to the reporter family too, so the
// plant needs the brief's own text beside it to land on the right family.
const partnerLists = "    decides_alone: [\"commentary.variance\", \"commentary.showback\"]\n    decides_alone_text: \"the brief's text.\""

func gateOn(t *testing.T, path string) (string, error) {
	t.Helper()
	cmd := exec.Command("../../scripts/roles-are-bound.sh")
	cmd.Env = append(os.Environ(), "ROLES_YAML="+path)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func requireRefusal(t *testing.T, path, needle string) {
	t.Helper()
	out, err := gateOn(t, path)
	if err == nil {
		t.Fatalf("the gate passed, want it to refuse with %q:\n%s", needle, out)
	}
	if !strings.Contains(out, needle) {
		t.Errorf("the gate failed, but not saying %q:\n%s", needle, out)
	}
}

// Red first: scripts/roles-are-bound.sh read neither list for emptiness.
func TestRolesAreBoundRefusesAnEmptyDecidesAlone(t *testing.T) {
	requireRefusal(t, yamlWith(t, [2]string{
		partnerLists, "    decides_alone: []\n    decides_alone_text: \"the brief's text.\"",
	}), "EMPTY LIST          finops-partner has an empty decides_alone")
}

func TestRolesAreBoundRefusesAnEmptyHandsUp(t *testing.T) {
	requireRefusal(t, yamlWith(t, [2]string{
		`hands_up: ["anomaly.accept"]`, `hands_up: []`,
	}), "EMPTY LIST          migration-watch has an empty hands_up")
}

func TestRolesAreBoundRefusesAnExemptionTooThinToBeAReason(t *testing.T) {
	re := regexp.MustCompile(`(?m)^    decides_alone_exempt: "[^"]*"$`)
	src, _ := os.ReadFile("roles.yaml")
	first := re.FindString(string(src))
	if first == "" {
		t.Fatal("roles.yaml carries no decides_alone_exempt line to thin out")
	}
	requireRefusal(t, yamlWith(t, [2]string{first, `    decides_alone_exempt: "n/a"`}), "THIN EXEMPTION")
}

func TestRolesAreBoundRefusesAnExemptionBesideAListThatIsNotEmpty(t *testing.T) {
	requireRefusal(t, yamlWith(t, [2]string{
		partnerLists,
		`    decides_alone: ["commentary.variance", "commentary.showback"]` + "\n" +
			`    decides_alone_exempt: "left behind after the list was written, which is a claim nobody is checking any more."` + "\n" +
			`    decides_alone_text: "the brief's text."`,
	}), "STALE EXEMPTION     finops-partner lists decides_alone")
}

func TestRolesAreBoundRefusesAnAnalystDecidingAClassItDoesNotOwn(t *testing.T) {
	// anomaly.accept is the supervisor's class, not the analyst link's.
	requireRefusal(t, yamlWith(t, [2]string{
		partnerLists,
		"    decides_alone: [\"commentary.variance\", \"commentary.showback\", \"anomaly.accept\"]\n    decides_alone_text: \"the brief's text.\"",
	}), "finops-partner decides anomaly.accept alone, but that class is owned by supervisor")
}

func TestRolesAreBoundRefusesANeverBindingWhoseTestIsGone(t *testing.T) {
	requireRefusal(t, yamlWith(t, [2]string{
		`test: "TestABlockedTaskIsNotWorkedAround"`, `test: "TestNoSuchTestExistsAnywhere"`,
	}), "DANGLING NEVER")
}

func TestRolesAreBoundRefusesANeverBindingWhoseClauseWasTakenOut(t *testing.T) {
	requireRefusal(t, yamlWith(t, [2]string{
		`  - "act on a task somebody blocked"
`, ``,
	}), "BINDING WITHOUT CLAUSE")
}

// The control: the gate must not refuse a family whose reasoned exemption is
// replaced by another reasoned exemption.
func TestRolesAreBoundAcceptsAReasonedExemptionInOtherWords(t *testing.T) {
	re := regexp.MustCompile(`(?m)^    hands_up_exempt: "[^"]*"$`)
	src, _ := os.ReadFile("roles.yaml")
	first := re.FindString(string(src))
	if first == "" {
		t.Fatal("roles.yaml carries no hands_up_exempt line")
	}
	out, err := gateOn(t, yamlWith(t, [2]string{first,
		`    hands_up_exempt: "this family hands nothing up today, for a reason written out in full so that a later reader can judge it."`}))
	if err != nil {
		t.Fatalf("the gate refused a reasoned exemption in other words:\n%s", out)
	}
}

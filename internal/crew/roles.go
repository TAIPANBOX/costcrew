package crew

// The crew's job descriptions, as data, and the mandate they enforce.
//
// One embedded file, roles.yaml, is the source for three renderings that used
// to each say a version of this themselves: the seeded mission/cadence/
// audience columns (mandate.go's missionFor, cadenceFor, audienceFor), the
// analyst card's "Job description" panel (internal/web/analyst.go), and the
// live runner's prompt packet (tools/run/mandate.go). `@yurii 2026-09-02`:
// "Вони мають вирішувати це все згідно своїх посадових інструкцій. І бажано,
// щоб ці посадові інструкції чітко були виписані, що для супервайзера, що для
// Фінопс-агента, щоб вони також чітко дотримувались."
//
// scripts/roles-are-bound.sh holds this file against the code and against
// world.Crew, both ways. The validation in mustLoadRoles below is a narrower
// copy of part of what that script checks: it exists so a typo in the YAML
// breaks `go test ./...` immediately, rather than only a shell script
// somebody has to remember to run.

import (
	_ "embed"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

//go:embed roles.yaml
var rolesYAML []byte

// JobClass is one decision class: the closed vocabulary ROLES-2026-09.md
// section 1 defines. A class changes something in the estate's record because
// somebody decided it; "owner" is which link may take it without asking up.
type JobClass struct {
	ID      string `yaml:"id"`
	Changes string `yaml:"changes"`
	// Owner is "analyst", "supervisor" or "owner": the one link that may
	// decide this class alone. "nobody" means no link in the crew decides it
	// at all -- purchase, infra.change and vendor.negotiate are always
	// recorded as an OPTION, never a decision the console applies.
	Owner string `yaml:"owner"`
	// UpTo names a threshold below which the class is the analyst's alone;
	// above it, the class is handed up. Optional: most classes carry none.
	UpTo string `yaml:"up_to"`
}

// maxProvenanceLen bounds a provenance line: it is a marker and a short reason,
// not a paragraph, and it is rendered on the card.
const maxProvenanceLen = 200

// ValidProvenance says whether s is a provenance marker this practice accepts,
// and if not, why. The vocabulary is closed:
//
//   - "@claude" (optionally followed by a space and anything): a draft or a
//     reading of the code, to be re-checked;
//   - "@decided YYYY-MM-DD" (optionally followed by "," or a space and a short
//     paraphrase): the owner decided it on that real calendar date;
//   - "@measured <how> YYYY-MM-DD": a run established it, the how is mandatory
//     and the date is the last word.
//
// There is deliberately no marker carrying the owner's name: a public
// repository records a decision as a paraphrase under "@decided", never as an
// attribution. A marker that is empty, padded with whitespace, carries a
// control character or newline, or runs past maxProvenanceLen is refused, so a
// second marker cannot ride in on the first one's line.
func ValidProvenance(s string) error {
	if s == "" {
		return fmt.Errorf("provenance is empty")
	}
	if s != strings.TrimSpace(s) {
		return fmt.Errorf("provenance %.60q has leading or trailing whitespace", s)
	}
	if len(s) > maxProvenanceLen {
		return fmt.Errorf("provenance is %d bytes, over the %d-byte limit", len(s), maxProvenanceLen)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return fmt.Errorf("provenance %.60q carries a control character", s)
		}
	}
	isDate := func(d string) bool {
		_, err := time.Parse("2006-01-02", d)
		return err == nil
	}
	switch {
	case s == "@claude" || strings.HasPrefix(s, "@claude "):
		return nil
	case strings.HasPrefix(s, "@decided "):
		rest := strings.TrimPrefix(s, "@decided ")
		if len(rest) >= 10 && isDate(rest[:10]) && (len(rest) == 10 || rest[10] == ',' || rest[10] == ' ') {
			return nil
		}
		return fmt.Errorf("provenance %.60q: @decided must be followed by a real YYYY-MM-DD date", s)
	case strings.HasPrefix(s, "@measured "):
		rest := strings.TrimPrefix(s, "@measured ")
		i := strings.LastIndex(rest, " ")
		if i > 0 && strings.TrimSpace(rest[:i]) != "" && isDate(rest[i+1:]) {
			return nil
		}
		return fmt.Errorf("provenance %.60q: @measured must read \"@measured <how> YYYY-MM-DD\", with the how", s)
	}
	return fmt.Errorf("provenance %.60q is not @claude, @decided YYYY-MM-DD or @measured <how> YYYY-MM-DD", s)
}

// Threshold is one named parameter (ROLES-2026-09.md section 4), so a number
// is changed in one place and every description that mentions it stays in
// step. Value is the display text; ValueCents is set only for the two money
// thresholds (T.anomaly, T.urgent). finops.Supervise reads T.anomaly's
// ValueCents as the figure a supervisor-selected option may not pass
// (invariants 27 and 53); nothing reads T.urgent's yet, so its figure is the
// supervisor's text and the card's. Provenance is one of the markers
// ValidProvenance accepts, checked at load.
type Threshold struct {
	Name       string `yaml:"name"`
	Meaning    string `yaml:"meaning"`
	Value      string `yaml:"value"`
	ValueCents int64  `yaml:"value_cents"`
	Provenance string `yaml:"provenance"`
}

// JobDescription is one role family's job description: the eight fields
// ROLES-2026-09.md gives every role (mission, reads, cadence, audience, owes,
// decides alone, hands up, quality bar), plus the closed, machine-checked
// class lists that make it enforceable.
//
// DecidesAlone and HandsUp carry only the class ids ROLES-2026-09.md names
// with a backtick inside that role's own bullet (or, where the bullet points
// back at a class the same role's Owes bullet already backticked, that
// class); the *Text fields carry the bullet's prose verbatim, unabridged,
// for the card and the prompt. A role whose bullet names no specific class at
// all ("the analysis", "the report's text") has an empty list and a full
// Text field: nothing here infers a class the document did not name.
type JobDescription struct {
	Family  string   `yaml:"family"`
	Matches []string `yaml:"matches"`
	// Link is "analyst" or "supervisor": which of the two crew links this
	// family sits at, for MayDecide's coarse check.
	Link     string `yaml:"link"`
	Mission  string `yaml:"mission"` // may carry a "{desk}" placeholder; see ForDesk
	Reads    string `yaml:"reads"`
	Cadence  string `yaml:"cadence"`
	Audience string `yaml:"audience"` // may carry a "{desk}" placeholder; see ForDesk
	Owes     string `yaml:"owes"`

	DecidesAlone     []string `yaml:"decides_alone"`
	DecidesAloneText string   `yaml:"decides_alone_text"`
	// DecidesAloneExempt and HandsUpExempt are the reason a family's list is
	// empty: a family whose job truly has nothing to list carries one, and
	// scripts/roles-are-bound.sh refuses an empty list with none, and a
	// non-empty list with one.
	DecidesAloneExempt string   `yaml:"decides_alone_exempt"`
	HandsUp            []string `yaml:"hands_up"`
	HandsUpText        string   `yaml:"hands_up_text"`
	HandsUpExempt      string   `yaml:"hands_up_exempt"`
	QualityBar         string   `yaml:"quality_bar"`
	// Note is a verbatim aside the card and the prompt show beneath the eight
	// fields when it is not empty: why a probation-era role has no cadence
	// bullet, why an on-prem variant repeats a cloud one, and so on.
	Note string `yaml:"note"`

	// The supervisor's own fields. Every other role leaves these empty.
	HandsToOwner           []string `yaml:"hands_to_owner"`
	HandsToOwnerText       string   `yaml:"hands_to_owner_text"`
	HandsToOwnerConditions []string `yaml:"hands_to_owner_conditions"`
	NeverAlso              string   `yaml:"never_also"`
	AudienceNote           string   `yaml:"audience_note"`
}

// ForDesk substitutes the "{desk}" placeholder Mission and Audience may carry
// with the phrase missionFor and audienceFor have always built a per-agent
// mission from: "the X desk", or "the whole estate" for the management desk.
// One substitution point, so the seeded mission column, the card and the
// prompt packet cannot say it three different ways.
func (r JobDescription) ForDesk(desk string) JobDescription {
	where := "the " + desk + " desk"
	if desk == "management" {
		where = "the whole estate"
	}
	r.Mission = strings.ReplaceAll(r.Mission, "{desk}", where)
	r.Audience = strings.ReplaceAll(r.Audience, "{desk}", where)
	return r
}

// NeverBinding pairs one clause of the never list with the test that holds it.
type NeverBinding struct {
	Verb string `yaml:"verb"`
	Test string `yaml:"test"`
}

type rolesFile struct {
	Never         []string         `yaml:"never"`
	NeverBound    []NeverBinding   `yaml:"never_bound"`
	NeverFullText string           `yaml:"never_full_text"`
	Thresholds    []Threshold      `yaml:"thresholds"`
	Classes       []JobClass       `yaml:"classes"`
	Roles         []JobDescription `yaml:"roles"`
}

var roles = mustLoadRoles()

// mustLoadRoles parses the embedded file and fails fast on the three things
// that would otherwise make every reader downstream silently wrong: a class
// naming a threshold that classes: does not define, a role naming a class
// classes: does not define, and a threshold whose provenance is not in the
// vocabulary ValidProvenance defines. scripts/roles-are-bound.sh checks both of
// these again, and more (every class named in CODE, every roster name
// matched, rights, hands_to_owner); this copy exists so a typo breaks
// `go test ./...` on the spot rather than only a shell script somebody has to
// remember to run.
func mustLoadRoles() rolesFile {
	var rf rolesFile
	if err := yaml.Unmarshal(rolesYAML, &rf); err != nil {
		panic("internal/crew/roles.yaml does not parse: " + err.Error())
	}
	classIDs := map[string]bool{}
	for _, c := range rf.Classes {
		if classIDs[c.ID] {
			panic(fmt.Sprintf("internal/crew/roles.yaml: class %q is listed twice", c.ID))
		}
		classIDs[c.ID] = true
	}
	thresholdNames := map[string]bool{}
	for _, t := range rf.Thresholds {
		thresholdNames[t.Name] = true
		if err := ValidProvenance(t.Provenance); err != nil {
			panic(fmt.Sprintf("internal/crew/roles.yaml: threshold %q is not a recognised provenance: %v", t.Name, err))
		}
	}
	for _, c := range rf.Classes {
		if c.UpTo != "" && !thresholdNames[c.UpTo] {
			panic(fmt.Sprintf("internal/crew/roles.yaml: class %q names threshold %q, which thresholds: does not define", c.ID, c.UpTo))
		}
	}
	neverClauses := map[string]bool{}
	for _, v := range rf.Never {
		neverClauses[v] = true
	}
	for _, nb := range rf.NeverBound {
		if !neverClauses[nb.Verb] {
			panic(fmt.Sprintf("internal/crew/roles.yaml: never_bound names %q, which never: does not list", nb.Verb))
		}
		if nb.Test == "" {
			panic(fmt.Sprintf("internal/crew/roles.yaml: never_bound for %q names no test", nb.Verb))
		}
	}
	for _, r := range rf.Roles {
		for _, list := range [][]string{r.DecidesAlone, r.HandsUp, r.HandsToOwner} {
			for _, id := range list {
				if !classIDs[id] {
					panic(fmt.Sprintf("internal/crew/roles.yaml: role %q names class %q, which classes: does not define", r.Family, id))
				}
			}
		}
	}
	return rf
}

// RoleFor resolves a role family name or a roster name to its job
// description: first against every role's own family name, then against its
// matches (a glob over roster names). "investigator-aws" and "investigator"
// both resolve to the cloud investigator family; "investigator-onprem" is a
// separate family, because its cadence differs from the cloud one's (see
// ROLES-2026-09.md section 2.23).
func RoleFor(name string) (JobDescription, bool) {
	for _, r := range roles.Roles {
		if r.Family == name {
			return r, true
		}
	}
	for _, r := range roles.Roles {
		for _, m := range r.Matches {
			if ok, _ := path.Match(m, name); ok {
				return r, true
			}
		}
	}
	return JobDescription{}, false
}

// RoleForDesk is RoleFor followed by ForDesk(desk), which is what every
// caller outside this file wants: a job description with its placeholder
// already substituted for one agent's desk.
func RoleForDesk(name, desk string) (JobDescription, bool) {
	r, ok := RoleFor(name)
	if !ok {
		return JobDescription{}, false
	}
	return r.ForDesk(desk), true
}

// ClassFor looks up one decision class by id.
func ClassFor(id string) (JobClass, bool) {
	for _, c := range roles.Classes {
		if c.ID == id {
			return c, true
		}
	}
	return JobClass{}, false
}

// AllClasses is every decision class, in the order roles.yaml declares them.
func AllClasses() []JobClass { return append([]JobClass(nil), roles.Classes...) }

// AllRoles is every role family, in the order roles.yaml declares them.
func AllRoles() []JobDescription { return append([]JobDescription(nil), roles.Roles...) }

// Never is the verbs every role in the crew never does, written once and
// rendered identically on every card and in every prompt: the five that
// concern decision authority and, since 2026-10-04, "act on a task somebody
// blocked", which the runner enforces.
func Never() []string { return append([]string(nil), roles.Never...) }

// NeverBindings is the never clauses this repository holds with a named test,
// in the order roles.yaml declares them.
func NeverBindings() []NeverBinding { return append([]NeverBinding(nil), roles.NeverBound...) }

// NeverFullText is ROLES-2026-09.md's complete "Never, for every role"
// sentence, all six clauses, as the card and the prompt show it. Never() lists
// the same six. For display only.
func NeverFullText() string { return roles.NeverFullText }

// ThresholdFor looks up one named threshold.
func ThresholdFor(name string) (Threshold, bool) {
	for _, t := range roles.Thresholds {
		if t.Name == name {
			return t, true
		}
	}
	return Threshold{}, false
}

// Classes referenced directly from Go code outside this file, tagged
// "// class:<id>" so scripts/roles-are-bound.sh can hold "every class named in
// code exists in roles.yaml" (B1A-SPEC.md section 3.1) by grepping for the
// tag rather than parsing Go. See crew.go's Post, Return and Approve.
const (
	ClassTaskAccept    = "task.accept"    // class:task.accept
	ClassTaskReturn    = "task.return"    // class:task.return
	ClassSprintApprove = "sprint.approve" // class:sprint.approve
)

// MayDecide answers whether role may decide class alone, and if not, why.
//
// role is one of three shapes:
//
//   - the literal link name "owner". Post, Return and Approve pass this
//     today, because every caller of them today is a person's act (see
//     crew.go), and "today the owner link decides everything that exists"
//     (B1A-SPEC.md section 2) -- everything, that is, except a class nobody
//     in the crew owns, which this console does not decide either way (next
//     paragraph).
//   - the literal link name "analyst" or "supervisor": a coarse check
//     against the class's own Owner field, with no family-specific
//     narrowing. "supervisor" also happens to match the supervisor's own
//     role entry below, and the two agree by construction for the classes
//     the supervisor owns outright. They do NOT answer "may the supervisor
//     select an option of an analyst-owned class": that is
//     SupervisorMaySelect, which reads option.select from the job
//     description, and this check keeps its coarse meaning for its other
//     callers.
//   - a role family or roster name (e.g. "investigator" or
//     "investigator-aws"), resolved via RoleFor and checked against that
//     family's own decides_alone list, which is narrower than "every class
//     the analyst link owns": an investigator decides anomaly.explain but
//     not recommendation.rightsizing, though both are owned by "analyst".
//
// purchase, infra.change and vendor.negotiate are owned by "nobody": no
// link -- owner included -- decides them as a console action.
// ROLES-2026-09.md section 1: "a proposal whose class is purchase,
// infra.change or vendor.negotiate is recorded as an OPTION inside a
// recommendation and is never a decision the console applies."
func MayDecide(role, class string) (bool, string) {
	c, ok := ClassFor(class)
	if !ok {
		return false, fmt.Sprintf("%q is not a decision class this practice defines", class)
	}
	if c.Owner == "nobody" {
		return false, fmt.Sprintf(
			"%s is never a decision the crew or the console makes; it is only ever recorded as an option",
			class)
	}
	switch role {
	case "owner":
		return true, ""
	case "analyst", "supervisor":
		if c.Owner == role {
			return true, ""
		}
		return false, fmt.Sprintf("%s is the %s's to decide, not the %s link's", class, c.Owner, role)
	}
	r, ok := RoleFor(role)
	if !ok {
		return false, fmt.Sprintf("%q is not a role this practice knows", role)
	}
	for _, id := range r.DecidesAlone {
		if id == class {
			return true, ""
		}
	}
	return false, fmt.Sprintf("%s does not decide %s alone; it hands up to the %s", r.Family, class, c.Owner)
}

// SupervisorMaySelect answers whether the supervisor's own pass may apply an
// option of class without asking the owner, and if not, why. It is the one
// question finops.Supervise asks, and it reads roles.yaml for the answer:
//
//   - a class the supervisor owns outright is its to decide (MayDecide's own
//     answer, unchanged);
//   - a class the ANALYST link owns is the supervisor's to select only
//     because its job description lists option.select in decides_alone
//     ("option.select for options inside the analysts' own classes"), so
//     taking that entry out of roles.yaml makes the supervisor carry again;
//   - a class the owner holds, or that nobody in the crew decides, is not.
//
// MayDecide("supervisor", class) is a coarse check on the class's owner field
// and keeps that meaning for its other callers; this is the narrower question
// with the job description's own word in it. It says nothing about the
// figure: the T.anomaly gate is the caller's.
func SupervisorMaySelect(class string) (bool, string) {
	c, ok := ClassFor(class)
	if !ok {
		return false, fmt.Sprintf("%q is not a decision class this practice defines", class)
	}
	if c.Owner != "analyst" {
		return MayDecide("supervisor", class)
	}
	r, ok := RoleFor("supervisor")
	if !ok {
		return false, "roles.yaml has no supervisor job description"
	}
	for _, id := range r.DecidesAlone {
		if id == "option.select" {
			return true, ""
		}
	}
	return false, fmt.Sprintf(
		"%s is the analysts' to decide and the supervisor's job description does not list option.select", class)
}

// Escalates answers who role would hand class up to, when it may not decide
// it alone. ok is false in three cases: class does not exist; class is
// "nobody"'s, which is an OPTION rather than an escalation; or role may
// already decide class alone, which leaves nothing to hand up.
func Escalates(role, class string) (string, bool) {
	if may, _ := MayDecide(role, class); may {
		return "", false
	}
	c, ok := ClassFor(class)
	if !ok || c.Owner == "nobody" {
		return "", false
	}
	return c.Owner, true
}

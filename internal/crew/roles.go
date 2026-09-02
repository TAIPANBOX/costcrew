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

// Threshold is one named parameter (ROLES-2026-09.md section 4), so a number
// is changed in one place and every description that mentions it stays in
// step. Value is the display text; ValueCents is set only for the two money
// thresholds (T.anomaly, T.urgent) and nothing here reads it programmatically
// today -- see B1A-SPEC.md section 5: this step enforces classes, not
// amounts.
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
	HandsUp          []string `yaml:"hands_up"`
	HandsUpText      string   `yaml:"hands_up_text"`
	QualityBar       string   `yaml:"quality_bar"`
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

type rolesFile struct {
	Never         []string         `yaml:"never"`
	NeverFullText string           `yaml:"never_full_text"`
	Thresholds    []Threshold      `yaml:"thresholds"`
	Classes       []JobClass       `yaml:"classes"`
	Roles         []JobDescription `yaml:"roles"`
}

var roles = mustLoadRoles()

// mustLoadRoles parses the embedded file and fails fast on the two things
// that would otherwise make every reader downstream silently wrong: a class
// naming a threshold that classes: does not define, and a role naming a
// class classes: does not define. scripts/roles-are-bound.sh checks both of
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
	}
	for _, c := range rf.Classes {
		if c.UpTo != "" && !thresholdNames[c.UpTo] {
			panic(fmt.Sprintf("internal/crew/roles.yaml: class %q names threshold %q, which thresholds: does not define", c.ID, c.UpTo))
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

// Never is the five verbs every role in the crew never does, written once and
// rendered identically on every card and in every prompt.
func Never() []string { return append([]string(nil), roles.Never...) }

// NeverFullText is ROLES-2026-09.md's complete "Never, for every role"
// sentence, including the sixth clause ("act on a task somebody blocked")
// that is not part of the five-item, gated Never() list. For display only.
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
//     role entry below, and the two agree by construction: the supervisor's
//     decides_alone list contains only classes it owns outright.
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

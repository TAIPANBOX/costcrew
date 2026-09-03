package crew_test

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// PARTNER-BUDGETS-RIGHT-SPEC.md: a role family's own "reads" line
// (roles.yaml) is rendered VERBATIM into every one of its analysts' prompts
// (deliver.JobDescriptionBlock's "Reads: ..." line, built straight from
// crew.JobDescription.Reads), so it is a promise the model is told is true.
// Found live, 2026-09-03: partner-gcp (skill stakeholder-briefing) tried the
// budgets tool because finops-partner's own reads line says "the team's
// budgets," was refused twice (rightsForSkill["stakeholder-briefing"] carried
// no budgets-read), and the task ended blocked with USD 0.1302 spent and no
// deliverable. The tool gate worked exactly as designed (invariant 26); the
// family's own mission and its rights had drifted apart.
//
// readsRequiresRights is the hand-reviewed mapping the spec asks for, built
// by reading every family's reads line once, by hand, rather than parsing
// the prose mechanically (fragile, and explicitly not what was asked for).
// For each family it names only the rights, among the five that gate a
// catalogue tool (tools/run/tools.go), that the reads line's own WORDS name:
//
//	figures-read   never listed: RightsFor grants it to every active analyst
//	               unconditionally, so any reads line that resolves only to
//	               figures-read-gated tools (anomaly, series, drivers,
//	               team_month) is satisfied by construction and proves
//	               nothing about a family being reviewed here.
//	sql-readonly   the literal phrase "the bounded query tool" (charges_query).
//	budgets-read   "budget"/"budgets"/"variance" naming a TEAM's or DESK's
//	               budget the way the budgets or variance tool reads it (the
//	               team_month tool, gated on figures-read alone, already
//	               covers ONE team's own month -- see "investigator" below).
//	kpi-registry   "the KPI library" or "the maturity page" (kpis, maturity).
//	export-data    "allocation"/"unallocated" the way the allocation tool
//	               reads it, or "showback".
//
// A family mapped to nil was read and found to name no catalogue tool by any
// of the above: either its reads line has no tool-shaped noun at all (most
// families -- "the run-rate projection", "the vendor intake"), or the words
// are genuinely ambiguous and this pass declines to guess. Three ambiguous
// cases, deliberately left at nil rather than silently dropped:
//
//   - governance-analyst: "budgets without a team" is an audit finding (which
//     budget ROWS carry no team), a shape neither the budgets tool (one desk,
//     one period) nor team_month (one team) reads.
//   - data-quality-analyst: "unallocated share" plausibly reads as the
//     allocation tool's own Unallocated figure (export-data), or as a SQL
//     aggregate over charges.team (sql-readonly, already held); its own
//     mission is charge-level tracing, which argues for the second reading,
//     but this pass would be guessing either way.
//   - ai-spend-analyst: "ai_calls ... per agent, per model, per unit" names a
//     TABLE no tool in the catalogue reads at all -- charges_query is scoped
//     to charges, drivers and attribution only (invariant 26). That is a
//     missing TOOL, not a missing RIGHT, so there is no right this mapping
//     could name for it; out of this gate's own scope.
//
// TestReadsRightsMappingCoversEveryFamily holds this map's own key set
// against roles.yaml, both ways, so a role family added later and never
// hand-reviewed here fails loudly instead of silently proving nothing.
var readsRequiresRights = map[string][]string{
	"investigator":           {"sql-readonly"}, // "charges through the bounded query tool"
	"investigator-onprem":    {"sql-readonly"}, // same reads text, roles.yaml's own note
	"anomaly-triage":         nil,
	"optimizer":              nil,
	"optimizer-onprem":       nil,
	"reporter":               {"budgets-read", "export-data"}, // "the budgets and variances"; "allocation coverage"
	"reporter-onprem":        {"budgets-read", "export-data"}, // same reads text as reporter
	"capacity-analyst":       nil,
	"finops-partner":         {"budgets-read"}, // "the team's budgets" -- the defect this spec fixes
	"ai-spend-analyst":       nil,              // NOT PROVEN, see above: no tool reads ai_calls at all
	"unit-economics-ai":      nil,
	"saas-portfolio-manager": nil,
	"renewals-analyst":       nil,
	"chargeback-analyst":     {"export-data"}, // "allocation for the period ... the unallocated pots"
	"commitment-analyst":     nil,
	"forecaster":             {"budgets-read"}, // "the budgets"
	"kpi-steward":            {"kpi-registry"}, // "the KPI library and its refusals, the maturity page"
	"executive-reporter":     {"kpi-registry"}, // "the KPI library"
	"governance-analyst":     nil,              // NOT PROVEN, see above: "budgets without a team"
	"data-quality-analyst":   nil,              // NOT PROVEN, see above: "unallocated share"
	"benchmarking-analyst":   {"kpi-registry"}, // "the estate's KPIs"
	"sustainability-analyst": nil,              // "carbon exports" is an import feed, not the export-data right
	"deep-analysis":          {"sql-readonly"}, // "the bounded query tool"
	"intake-triage":          nil,              // "nothing today" -- roles.yaml's own words
	"migration-watch":        nil,
	"supervisor":             nil,
}

// TestReadsRightsMappingCoversEveryFamily holds readsRequiresRights against
// roles.yaml itself, both ways: every family roles.yaml defines has been
// hand-reviewed here (even a nil entry is a reviewed "found nothing"), and
// this map never names a family the file has retired. Without this, a role
// family added later and never reviewed would silently pass
// TestEveryFamilysReadsPromiseIsBackedByARight below by having no entry at
// all, which is exactly the silent gap this whole change exists to close.
func TestReadsRightsMappingCoversEveryFamily(t *testing.T) {
	inFile := map[string]bool{}
	for _, r := range crew.AllRoles() {
		inFile[r.Family] = true
		if _, ok := readsRequiresRights[r.Family]; !ok {
			t.Errorf("roles.yaml family %q has no entry in readsRequiresRights, "+
				"so its reads line was never hand-reviewed against the rights it grants", r.Family)
		}
	}
	for fam := range readsRequiresRights {
		if !inFile[fam] {
			t.Errorf("readsRequiresRights names family %q, which roles.yaml no longer defines", fam)
		}
	}
}

// TestEveryFamilysReadsPromiseIsBackedByARight is the concrete, mechanical
// gate PARTNER-BUDGETS-RIGHT-SPEC.md asks for: for every family, the union
// of its roster members' rights must be a superset of what
// readsRequiresRights says that family's reads line requires. Every member
// of a family is checked (not one representative standing in for all of
// them, the way scripts/roles-are-bound.sh's decides_alone<=rights property
// does), because a shared skill NAME does not guarantee a shared rights set
// (triage-ai's model-routing-review and the other triage desks'
// driver-classification happen to grant the same rights today, but nothing
// requires that to stay true).
//
// RightsFor is asked for "active" rather than each analyst's actually seeded
// state, the same choice TestRosterForTheRolesGate makes for the same
// reason: a Suspended or Restricted analyst's reduced rights are not what
// this property is about, and the fixture seeds two suspended and one
// restricted analyst today (world.go), which would otherwise make this gate
// depend on who happens to be off the rota when it runs.
//
// Each family with a real requirement is its own t.Run case, so a failure
// names the one family that regressed rather than reporting one folded
// "something is wrong somewhere" line.
func TestEveryFamilysReadsPromiseIsBackedByARight(t *testing.T) {
	for family, required := range readsRequiresRights {
		if len(required) == 0 {
			continue
		}
		t.Run(family, func(t *testing.T) {
			matched := 0
			for _, a := range world.Crew {
				fam, ok := crew.RoleFor(a.Name)
				if !ok || fam.Family != family {
					continue
				}
				matched++
				got := crew.RightsFor(a.Skills, "active")
				for _, need := range required {
					if !containsRight(got, need) {
						t.Errorf("%s (family %q) reads %q, which this pass reads as needing "+
							"%q; RightsFor(%v) = %v does not hold it",
							a.Name, family, fam.Reads, need, a.Skills, got)
					}
				}
			}
			if matched == 0 {
				t.Fatalf("no roster member resolves to family %q; readsRequiresRights names "+
					"a family nobody on world.Crew is hired into", family)
			}
		})
	}
}

func containsRight(rights []string, want string) bool {
	for _, r := range rights {
		if r == want {
			return true
		}
	}
	return false
}

package deliver

// PARTNER-BUDGET-RECOMMENDATIONS-SPEC.md: the finops-partner's own packet
// section, citing a provider's own budget recommendation beside the team's
// real, finance-set one. `@yurii 2026-09-03`: «це можна отримувати від
// користувача, або, наприклад, подивитись, які пропозиції дають провайдери
// хмарні».
//
// THE GUARDRAIL, stated here because this is the ONLY place outside
// internal/connectors that reads budget_recommendations. This function reads
// estate.CurrentBudgets and connectors.BudgetRecommendations SEPARATELY and
// joins them in Go; it calls neither estate.BudgetVsActual nor
// crew.SpendInMonth, and nothing it computes is ever written back to the
// `budgets` table or read by a guard. A provider's suggestion is citation
// material for a brief, never a number this console applies -- the same
// status this package's own rightsizing-style recommendations hold for the
// optimizer (a ranked list with a risk sentence, never applied
// automatically) and the same "propose, never invent, a stamp is what
// applies" rule every option class in this console already holds (CLAUDE.md
// invariant 27). See CLAUDE.md invariant 46 and this package's own
// partnerbudget_test.go, whose guardrail tests hold this two ways: a
// structural read of CurrentBudgets/BudgetVsActual/SpendInMonth/headroomOf's
// own source, and a behavioural before/after import.
import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

// notAppliedAnywhereSentence is the one sentence every partner-budget
// section carries, naming which figure is which: the team's own budget, set
// by finance, is what this console measures against; the provider's own
// suggestion is not applied anywhere. Named as a constant because
// scripts/gates-have-teeth.sh's own "drop the sentence" mutant case targets
// this exact line, the same reason driversSectionWindowDays is named above.
const notAppliedAnywhereSentence = "not applied anywhere"

// partnerBudgetSection is finops-partner's own section, gated on the
// "stakeholder-briefing" skill the same way reportingSection and
// forecastingSection gate on theirs (HasString(a.Skills, ...)) rather than
// on the family name: every seeded partner-<desk> analyst carries exactly
// that skill (internal/world/world.go) and no other seeded analyst does, so
// gating on the skill achieves "for the finops-partner family only" the same
// indirect way this file's other skill-gated sections already do it.
//
// For every (team, month) this desk has BOTH a real budget row
// (estate.CurrentBudgets) and a recommendation row
// (connectors.BudgetRecommendations) for, one line: both figures, the gap in
// cents and as a percentage. A team-month pairing with only one side is left
// out -- no invented zero -- and the whole section is absent (an empty
// string, joining nothing into the packet) when nothing pairs at all, the
// rule every other section in this file already holds.
func partnerBudgetSection(db *sql.DB, desk string) string {
	if desk == "" {
		return ""
	}
	real, err := estate.CurrentBudgets(db)
	if err != nil || len(real) == 0 {
		return ""
	}
	recs, err := connectors.BudgetRecommendations(db, desk)
	if err != nil || len(recs) == 0 {
		return ""
	}

	type pair struct {
		team, month string
		real, rec   money.Cents
	}
	var pairs []pair
	for _, r := range recs {
		key := estate.BudgetKey{Source: desk, Team: r.Team, Month: r.Month}
		realC, ok := real[key]
		if !ok {
			// No real budget for this team's month: not shown, never
			// invented. This is the "absent when either side is missing"
			// rule at the level of one pairing, and it is also exactly what
			// the "show a recommendation with no real budget as if it were
			// one" mutant would skip.
			continue
		}
		pairs = append(pairs, pair{r.Team, r.Month, realC, r.RecommendedCents})
	}
	if len(pairs) == 0 {
		return ""
	}
	// Invariant 7: the same estate renders the same way every time. team,
	// then month, ascending, matching connectors.BudgetRecommendations' own
	// base order rather than leaving it to map iteration.
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].team != pairs[j].team {
			return pairs[i].team < pairs[j].team
		}
		return pairs[i].month < pairs[j].month
	})

	var b strings.Builder
	fmt.Fprintf(&b, "The provider's own budget recommendation on %s, cited beside the team's real one\n", desk)
	fmt.Fprintf(&b, "each line: the team's own budget, set by finance, beside what %s suggests, %s\n",
		desk, notAppliedAnywhereSentence)
	for _, p := range pairs {
		gap := p.rec - p.real
		pctStr := "n/a"
		if pct, ok := money.Pct(gap, p.real); ok {
			pctStr = fmt.Sprintf("%+.1f%%", pct)
		}
		fmt.Fprintf(&b, "%-16s %-7s budget:%-10s recommended:%-10s gap:%s (%s)\n",
			p.team, p.month, p.real, p.rec, gap, pctStr)
	}
	return b.String()
}

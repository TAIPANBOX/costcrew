package deliver

// C5-SPEC.md section 2: the packet section for the optimizer roles
// (optimizer-aws/gcp/azure/onprem, skill "rightsizing-analysis"). The
// providers' own recommendation exports (C5's three readers, registered in
// internal/connectors) are the packet's INPUT here, never its output: no
// model is called by this step, and roles.yaml's own hands_up already
// names every row on this list infra.change, a person's decision, never
// this console's.

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
)

// rightsizingLookbackRiskDays is roles.yaml's own words for the optimizer
// family, restated as the number that makes them checkable: "the risk (a
// monthly job looks idle to a fourteen-day window)". AWS Compute
// Optimizer's own default lookback is fourteen days (connectors.go's
// "compute-optimizer" entry: "It sees fourteen days. A monthly batch job
// looks idle to it."), which is the number this threshold is named after.
const rightsizingLookbackRiskDays = 14

// recommendationsSection is the desk's own rightsizing list: every
// provider's recommendation on this desk, ranked by saving, top ten, with
// the risk sentence a lookback of fourteen days or fewer carries and a
// trailing "and N more" when the list is longer. Empty when the desk holds
// no recommendations, the same additive rule every other section in this
// file already holds: a section with nothing to say is absent, not a
// header over nothing.
func recommendationsSection(db *sql.DB, desk string) string {
	if desk == "" {
		return ""
	}
	recs, err := connectors.Recommendations(db, desk)
	if err != nil || len(recs) == 0 {
		return ""
	}

	// Ranked by SAVING, descending -- the mission this whole connector
	// exists for ("propose the smaller size with the saving attached").
	// connectors.RankBySaving carries the comparator itself, and the
	// fault gates-have-teeth.sh plants for this invariant (swap the field
	// it reads from MonthlySavingCents to Current, a size string e.g.
	// "m5.2xlarge", which still compiles -- Go allows > on strings -- and
	// silently reorders the list alphabetically by current size instead
	// of by money) is planted there now, not here: web's /rightsizing
	// page wants this exact order too and used to carry its own,
	// separately-mutation-tested copy of this same comparator, which is
	// the shape PR #34 review found only half-covered.
	connectors.RankBySaving(recs)

	var b strings.Builder
	fmt.Fprintf(&b, "Rightsizing recommendations on %s, ranked by saving\n", desk)
	const topN = 10
	shown := 0
	for i, r := range recs {
		if i >= topN {
			break
		}
		fmt.Fprintf(&b, "%s %s: %s -> %s, %s/mo saved", r.Action, r.Resource, r.Current, r.Recommended,
			r.MonthlySavingCents)
		if r.LookbackDays <= rightsizingLookbackRiskDays {
			fmt.Fprintf(&b, " (a lookback of %d days; a monthly job looks idle to it)\n", r.LookbackDays)
		} else {
			fmt.Fprintf(&b, " (lookback %d days)\n", r.LookbackDays)
		}
		shown++
	}
	if len(recs) > shown {
		fmt.Fprintf(&b, "and %d more\n", len(recs)-shown)
	}
	return b.String()
}

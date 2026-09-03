package web

// C5-SPEC.md section 2's page bullet: a recommendations list per desk with
// the import's file and date, and "none imported" when empty.

import (
	"net/http"
	"sort"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

var tplRightsizing = page("rightsizing.html")

// rightsizingDesk is one desk's own section of the page: its recommendation
// rows, ranked, and the file and date of whichever row was imported most
// recently -- "the import's file and date" the spec's own words ask for.
// Empty (Rows is nil) reads as "none imported" in the template, the same
// additive rule every packet section already holds.
type rightsizingDesk struct {
	Desk               world.Desk
	Rows               []connectors.Recommendation
	LastFile, LastDate string
}

func (s *Server) rightsizing(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	groups := make([]rightsizingDesk, 0, len(world.Desks))
	for _, d := range world.Desks {
		recs, err := connectors.Recommendations(s.db, d.Name)
		if err != nil {
			http.Error(w, "store unavailable", http.StatusInternalServerError)
			return
		}
		// Ranked by saving, descending, ties broken by resource: the same
		// order deliver.recommendationsSection ranks its own packet in, so
		// a person reading this page and an analyst reading its packet see
		// the same list in the same order.
		sort.SliceStable(recs, func(i, j int) bool {
			if recs[i].MonthlySavingCents != recs[j].MonthlySavingCents {
				return recs[i].MonthlySavingCents > recs[j].MonthlySavingCents
			}
			return recs[i].Resource < recs[j].Resource
		})
		g := rightsizingDesk{Desk: d, Rows: recs}
		for _, rec := range recs {
			if rec.ImportedAt > g.LastDate {
				g.LastDate, g.LastFile = rec.ImportedAt, rec.SourceFile
			}
		}
		groups = append(groups, g)
	}

	s.render(w, tplRightsizing, struct {
		shell
		Groups []rightsizingDesk
	}{s.shellFor(r, "Rightsizing", "rightsizing"), groups})
}

package web

// C8-LEADERSHIP-SPEC.md: C8 (costcrew#36, invariant 36) publishes the
// executive pack as an explainer whose Team and Audience are both the fixed
// string "leadership", and called /explainers?audience=leadership "the
// leadership page" -- nothing linked to it, the template still said
// "Explainers" and showed operators a Commission form, and the pack's own
// four numbers existed only as prose inside the body. This is the page that
// spec asks for instead: its own route, its own template, the four figures
// as real tiles, no sidebar entry (invariant 19 is Yurii's call, not mine),
// reached from /kpis and /explainers instead.
//
// finops.Executive, crew.Explainers and renderBody are reused, not copied.
// ?audience=leadership on /explainers keeps working exactly as C8 left it.

import (
	"html/template"
	"net/http"
	"sort"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

var tplLeadership = page("leadership.html")

// leadershipPack is one published pack, C8-LEADERSHIP-SPEC.md section 2's
// own words: "topic, published date and publisher, body via renderBody".
// No Team field: unlike /explainers (which still shows every team's own
// explainer beside this one and so needs the isRealTeam guard on
// "leadership" itself), this page shows leadership packs only, so naming
// the team on every row would say the same word every time.
type leadershipPack struct {
	Topic     string
	Published string
	Publisher string
	Rendered  template.HTML
}

func (s *Server) leadership(w http.ResponseWriter, r *http.Request) {
	if s.guard(w, r) == nil {
		return
	}
	figs, period, previous, err := finops.Executive(s.db)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	list, err := crew.Explainers(s.db)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	var packs []leadershipPack
	for _, e := range list {
		if e.Audience != "leadership" || e.State != "published" {
			continue
		}
		packs = append(packs, leadershipPack{e.Topic, e.Published, e.Publisher, renderBody(e.Body)})
	}
	// Newest published first. crew.Explainers already orders its rows by id
	// DESC, and Published is an RFC3339 string, which sorts lexically the
	// same way it sorts in time, so a STABLE sort on Published alone keeps
	// id DESC as the tiebreak for two packs published in the same second,
	// rather than the map- or query-order invariant 7 refuses to render by.
	sort.SliceStable(packs, func(i, j int) bool { return packs[i].Published > packs[j].Published })

	s.render(w, tplLeadership, struct {
		shell
		Figures  []finops.ExecutiveFigure
		Period   string
		Previous string
		Packs    []leadershipPack
	}{s.shellFor(r, "Leadership", "leadership"), figs, period, previous, packs})
}

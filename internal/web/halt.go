package web

// The desk halt's own console action: C9-SPEC.md section 2, "Lifting: a
// console action on the desk page, operator, CSRF, with a reason,
// journaled; the analysts return to active." Modelled directly on
// roster.go's setAnalystState: guard, then checked (operator + CSRF), then
// the domain call, then the SAME journal event a suspension already uses --
// C9-SPEC.md section 3, "no new wire type".

import (
	"net/http"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

func (s *Server) liftHalt(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	name := r.PathValue("name")
	back := "/desk/" + name
	if !s.checked(w, r, back, u) {
		return
	}
	reason := r.PostFormValue("reason")
	if _, err := crew.LiftHalt(s.db, name, reason, u.Username, s.rec); err != nil {
		s.done(w, r, back, err)
		return
	}
	s.publishPassports()
	redirectMsg(w, r, back, "")
}

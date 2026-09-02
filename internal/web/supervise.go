package web

// The supervisor's pass, from the console. B3-SPEC.md section 4.
//
// `@yurii 2026-09-02`: "він має давати на вибір якісь певні рішення, які він
// вважає за потрібне спочатку супервайзеру, тобто головному агенту, а вже
// той має запитувати юзера, користувача, власника цих агентів, що робити
// далі."

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/TAIPANBOX/costcrew/internal/finops"
)

// superviseSprint is the console button: the runner's -supervise flag,
// reached without a terminal. A person with channel-post -- the supervisor's
// own right, escalation's -- triggers it; gated the same way every other
// write route here is, CSRF and u.May("operator"), because a session is
// always a person acting through the console and the finer-grained
// "channel-post" the spec names is the supervisor ANALYST's own right
// (internal/crew/mandate.go's rightsForSkill), not a second account
// permission this console has never had one of. See this PR's body.
func (s *Server) superviseSprint(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "no such sprint", http.StatusNotFound)
		return
	}
	back := "/sprint/" + strconv.Itoa(id)
	if !s.checked(w, r, back, u) {
		return
	}
	pass, err := finops.Supervise(s.db, id, s.rec)
	if err != nil {
		redirectMsg(w, r, back, err.Error())
		return
	}
	redirectMsg(w, r, back, fmt.Sprintf(
		"%d applied, %d carried, %d decision request(s) written",
		len(pass.Applied), len(pass.Carried), len(pass.Requests)))
}

// decisionOwners is who has an open decision request on this sprint, for the
// sprint page to link to.
func decisionOwners(s *Server, sprintID int) []string {
	rows, err := s.db.Query(`SELECT DISTINCT owner FROM decision_requests WHERE sprint=? ORDER BY owner`, sprintID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var o string
		if rows.Scan(&o) == nil {
			out = append(out, o)
		}
	}
	return out
}

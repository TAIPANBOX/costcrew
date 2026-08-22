package web

import (
	"net/http"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/engines"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

var (
	tplHire    = page("hire.html")
	tplRebrief = page("rebrief.html")
)

func deskNames() []string {
	out := make([]string, 0, len(world.Desks))
	for _, d := range world.Desks {
		out = append(out, d.Name)
	}
	return out
}

func engineNames() []string {
	out := make([]string, 0, len(engines.Catalogue))
	for _, e := range engines.Catalogue {
		out = append(out, e.ID)
	}
	return out
}

func (s *Server) hirePage(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	if !u.May("operator") {
		redirectMsg(w, r, "/staff", "your account may read and export, but not hire")
		return
	}
	s.render(w, tplHire, struct {
		shell
		Desks, Engines, Cadences, Attestations, Skills, Rights []string
		Host                                                   string
	}{s.shellFor(r, "Hire an analyst", "staff"),
		deskNames(), engineNames(), crew.Cadences, crew.Attestations,
		crew.SkillPool, crew.Rights, s.host})
}

// parseAnalyst reads the form once, so hire and re-brief cannot drift apart in
// what they accept.
func parseAnalyst(r *http.Request) (crew.Analyst, error) {
	perTask, err := money.Parse(r.PostFormValue("per_task"))
	if err != nil {
		return crew.Analyst{}, err
	}
	monthly, err := money.Parse(r.PostFormValue("monthly"))
	if err != nil {
		return crew.Analyst{}, err
	}
	return crew.Analyst{
		Name:        r.PostFormValue("name"),
		Role:        strings.TrimSpace(r.PostFormValue("role")),
		Mission:     strings.TrimSpace(r.PostFormValue("mission")),
		Desk:        r.PostFormValue("desk"),
		Engine:      r.PostFormValue("engine"),
		Skills:      r.PostForm["skills"],
		Rights:      r.PostForm["rights"],
		PerTask:     perTask,
		Monthly:     monthly,
		Cadence:     r.PostFormValue("cadence"),
		Audience:    strings.TrimSpace(r.PostFormValue("audience")),
		Parent:      r.PostFormValue("parent"),
		Attestation: r.PostFormValue("attestation"),
	}, nil
}

func (s *Server) hire(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	if !s.checked(w, r, "/staff/new", u) {
		return
	}
	a, err := parseAnalyst(r)
	if err != nil {
		redirectMsg(w, r, "/staff/new", "the guards must be amounts like 15.00")
		return
	}
	a.Owner = u.Username
	if err := crew.Hire(s.db, a); err != nil {
		redirectMsg(w, r, "/staff/new", err.Error())
		return
	}

	// Hiring and registering are the SAME act, which is the whole argument for
	// this form. The passport is published now, not by a job somebody
	// remembers to run.
	s.publishPassports()
	if s.rec != nil {
		_ = s.rec.Emit("agent_hired", a.Name, "info", map[string]any{
			"analyst": a.Name, "desk": a.Desk, "role": a.Role,
			"owner": u.Username, "attestation": a.Attestation,
			"budget_per_task": a.PerTask.String(),
			"budget_monthly":  a.Monthly.String(),
			"rights":          strings.Join(a.Rights, ","),
		}, s.delegation(u.Username, a.Name))
	}
	redirectMsg(w, r, "/staff/"+strings.TrimSpace(a.Name), "")
}

func (s *Server) rebriefPage(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	a, err := crew.GetAnalyst(s.db, r.PathValue("name"))
	if err != nil {
		http.Error(w, "no such analyst", http.StatusNotFound)
		return
	}
	has, hasRight := map[string]bool{}, map[string]bool{}
	for _, x := range a.Skills {
		has[x] = true
	}
	for _, x := range a.Rights {
		hasRight[x] = true
	}
	s.render(w, tplRebrief, struct {
		shell
		A                                                crew.Analyst
		Desks, Engines, Cadences, Skills, Rights, States []string
		Has, HasRight                                    map[string]bool
	}{s.shellFor(r, "Re-brief "+a.Name, "staff"), a,
		deskNames(), engineNames(), crew.Cadences, crew.SkillPool, crew.Rights,
		crew.States, has, hasRight})
}

func (s *Server) rebrief(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	name := r.PathValue("name")
	back := "/staff/" + name + "/edit"
	if !s.checked(w, r, back, u) {
		return
	}
	a, err := parseAnalyst(r)
	if err != nil {
		redirectMsg(w, r, back, "the guards must be amounts like 15.00")
		return
	}
	a.Name = name
	if err := crew.Rebrief(s.db, a); err != nil {
		redirectMsg(w, r, back, err.Error())
		return
	}
	s.publishPassports()
	if s.rec != nil {
		_ = s.rec.Emit("agent_rebriefed", name, "info", map[string]any{
			"analyst": name, "desk": a.Desk,
			"budget_per_task": a.PerTask.String(),
			"budget_monthly":  a.Monthly.String(),
		}, s.delegation(u.Username, name))
	}
	redirectMsg(w, r, "/staff/"+name, "")
}

func (s *Server) setAnalystState(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	name := r.PathValue("name")
	back := "/staff/" + name + "/edit"
	if !s.checked(w, r, back, u) {
		return
	}
	state, reason := r.PostFormValue("state"), r.PostFormValue("reason")
	if err := crew.SetState(s.db, name, state, reason); err != nil {
		redirectMsg(w, r, back, err.Error())
		return
	}
	s.publishPassports()
	if s.rec != nil {
		// A suspension is high severity on purpose: it is the event somebody
		// asks about later, and burying it at info is how it is missed.
		sev := "info"
		if state != "active" {
			sev = "high"
		}
		_ = s.rec.Emit("agent_state_changed", name, sev, map[string]any{
			"analyst": name, "state": state, "reason": reason, "by": u.Username,
		}, s.delegation(u.Username, name))
	}
	redirectMsg(w, r, "/staff/"+name, "")
}

// publishPassports keeps the identity documents in step with the roster.
//
// Fail-quiet: the stack is optional, and a console that refuses to hire
// because an events directory is unwritable is one somebody switches the
// governance off to get past.
func (s *Server) publishPassports() {
	if s.passports == nil {
		return
	}
	roster, err := crew.Roster(s.db)
	if err != nil {
		return
	}
	// Straight through. The conversion that used to sit here dropped the
	// owner, the parent and the attestation on the floor, which are exactly
	// the three fields a passport exists to carry.
	_, _ = s.passports(roster)
}

func (s *Server) delegation(operator, analyst string) []string {
	if s.delegate == nil {
		return nil
	}
	return s.delegate(operator, analyst)
}

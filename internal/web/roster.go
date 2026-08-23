package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/auth"
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
		Needs                                                  map[string]string
		Host                                                   string
	}{s.shellFor(r, "Hire an analyst", "staff"),
		deskNames(), engineNames(), crew.Cadences, crew.Attestations,
		crew.SkillPool, crew.Rights, crew.AttestationNeeds, s.host})
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
		Name:              r.PostFormValue("name"),
		Role:              strings.TrimSpace(r.PostFormValue("role")),
		Mission:           strings.TrimSpace(r.PostFormValue("mission")),
		Desk:              r.PostFormValue("desk"),
		Engine:            r.PostFormValue("engine"),
		Skills:            r.PostForm["skills"],
		Rights:            r.PostForm["rights"],
		PerTask:           perTask,
		Monthly:           monthly,
		Cadence:           r.PostFormValue("cadence"),
		Audience:          strings.TrimSpace(r.PostFormValue("audience")),
		Parent:            r.PostFormValue("parent"),
		Attestation:       r.PostFormValue("attestation"),
		AttestationDetail: strings.TrimSpace(r.PostFormValue("attestation_detail")),
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
	// This page is a control surface and nothing else: everything on it that
	// is worth READING is already on the agent's card. So somebody the POST
	// will refuse is sent to the card rather than handed a form that cannot
	// work, which reads as permission until they have filled it in.
	if !mayManage(u, a) {
		redirectMsg(w, r, "/staff/"+a.Name, "only "+a.Owner+
			", who hired it, or an admin may re-brief it")
		return
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
	// Rebriefing is managing. s.checked asks whether this account may act at
	// all; it does not ask whether it may act on THIS agent, and without the
	// question any operator could rewrite any agent's mission, rights and
	// monthly guard. Removing an agent is loud and recorded. Quietly lifting
	// somebody else's guard from 150.00 to 5000.00 is neither, and a spend
	// control anybody with an account can raise is not a control.
	current, err := crew.GetAnalyst(s.db, name)
	if err != nil {
		http.Error(w, "no such analyst", http.StatusNotFound)
		return
	}
	if !mayManage(u, current) {
		redirectMsg(w, r, back, "only "+current.Owner+
			", who hired it, or an admin may rebrief it")
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
	// The same question rebrief and remove ask. Suspending an agent stops its
	// owner's work, and reactivating a suspended one restarts something
	// somebody stopped on purpose; neither is a decision for a colleague who
	// happens to have an operator account.
	current, err := crew.GetAnalyst(s.db, name)
	if err != nil {
		http.Error(w, "no such analyst", http.StatusNotFound)
		return
	}
	if !mayManage(u, current) {
		redirectMsg(w, r, back, "only "+current.Owner+
			", who hired it, or an admin may change its state")
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

// ------------------------------------------- removing and moving an analyst

// mayManage says whether this account may remove or move this analyst.
//
// The OWNER, not merely an operator. Hiring an agent is taking responsibility
// for what it spends, and the console records who did it; letting anybody with
// the operator role delete somebody else's agent would make that record
// meaningless. An admin is included because an admin has to be able to clean
// up after somebody who has left, and that is exactly the case where the owner
// cannot act.
func mayManage(u *auth.User, a crew.Analyst) bool {
	if u == nil {
		return false
	}
	if u.May("admin") {
		return true
	}
	return u.May("operator") && a.Owner == u.Username
}

func (s *Server) removeAnalyst(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	name := r.PathValue("name")
	back := "/staff/" + name
	if !s.checked(w, r, back, u) {
		return
	}
	a, err := crew.GetAnalyst(s.db, name)
	if err != nil {
		http.Error(w, "no such analyst", http.StatusNotFound)
		return
	}
	if !mayManage(u, a) {
		redirectMsg(w, r, back, "only "+a.Owner+", who hired it, or an admin may take it off the roster")
		return
	}
	// The name typed out, for the same reason the accounts page asks for it:
	// this is one click from every other control on the page.
	if r.PostFormValue("confirm") != name {
		redirectMsg(w, r, back, "to take an analyst off the roster, type its name in the box")
		return
	}
	if err := crew.Remove(s.db, name, u.Username); err != nil {
		redirectMsg(w, r, back, err.Error())
		return
	}
	if s.rec != nil {
		_ = s.rec.Emit("agent_removed", name, "info", map[string]any{
			"analyst": name, "desk": a.Desk, "owner": a.Owner, "removed_by": u.Username,
		}, s.delegation(u.Username, name))
	}
	// The passports are republished so the identity graph stops being told
	// about an agent this installation no longer has.
	s.publishPassports()
	redirectMsg(w, r, "/staff", a.Name+" is off the roster. What it did stays on the board and in the journal.")
}

func (s *Server) transferAnalyst(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	name := r.PathValue("name")
	back := "/staff/" + name
	if !s.checked(w, r, back, u) {
		return
	}
	a, err := crew.GetAnalyst(s.db, name)
	if err != nil {
		http.Error(w, "no such analyst", http.StatusNotFound)
		return
	}
	if !mayManage(u, a) {
		redirectMsg(w, r, back, "only "+a.Owner+", who hired it, or an admin may move it")
		return
	}
	toDesk := r.PostFormValue("desk")
	toOwner := strings.TrimSpace(r.PostFormValue("owner"))
	toParent := r.PostFormValue("parent")
	// An owner has to be an account that exists. Handing an agent to a name
	// nobody can sign in as is handing it to nobody.
	if toOwner != "" && toOwner != a.Owner {
		if who, err := s.au.Get(toOwner); err != nil || who == nil {
			redirectMsg(w, r, back, "there is no account called "+toOwner+
				", and an agent owned by nobody is an agent nobody answers for")
			return
		}
	}
	moved, err := crew.Transfer(s.db, name, toDesk, toOwner, toParent, u.Username)
	if err != nil {
		redirectMsg(w, r, back, err.Error())
		return
	}
	if s.rec != nil {
		_ = s.rec.Emit("agent_transferred", name, "info", map[string]any{
			"analyst": name, "from_desk": a.Desk, "to_desk": toDesk,
			"from_owner": a.Owner, "to_owner": toOwner,
			"open_tasks_moved": moved, "moved_by": u.Username,
		}, s.delegation(u.Username, name))
	}
	s.publishPassports()
	// The handover splits the record, and the message says where the line is.
	//
	// Open work moves, because the new owner takes it on and answers for what
	// it costs from here. Closed work stays with whoever authorised it, which
	// is what makes the owner column a history rather than a second copy of
	// whoever holds the agent today.
	msg := "moved. " + strconv.Itoa(moved) + " open " + plural(moved, "task", "tasks") +
		" moved with it, and what they cost from here is " + toOwner + "'s. " +
		"Closed work stays charged to " + a.Owner + ", who authorised it, and " +
		"keeps the desk it was booked to."
	redirectMsg(w, r, back, msg)
}

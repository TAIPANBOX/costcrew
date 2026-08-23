package web_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// twoOperators sets up an agent owned by one operator and a session for
// another, which is the situation every test in this file is about.
func twoOperators(t *testing.T) (h, other *harness, agent crew.Analyst) {
	t.Helper()
	h = startWith(t, true)
	h.signUp(t, "boss", "boss-password-2026")
	for _, who := range []string{"alice", "bob"} {
		if _, err := h.au.Create(who, who+"-password-2026", "operator"); err != nil {
			t.Fatal(err)
		}
	}
	roster, err := crew.Roster(h.st.DB())
	if err != nil || len(roster) == 0 {
		t.Fatalf("no roster to test with: %v", err)
	}
	agent = roster[0]
	if _, err := h.st.DB().Exec(`UPDATE analysts SET owner=? WHERE name=?`,
		"alice", agent.Name); err != nil {
		t.Fatal(err)
	}
	agent.Owner = "alice"
	return h, h.as(t, "bob", "bob-password-2026"), agent
}

// Rebriefing somebody else's agent is managing it.
//
// remove and transfer both call mayManage. rebrief and setState call only
// s.checked, which asks whether the account may ACT at all, not whether it may
// act on THIS agent. So any operator could rewrite any agent's mission, its
// rights, and its monthly guard.
//
// The guard is the part that matters. Taking somebody's agent off the roster
// is loud, recorded and immediately visible. Raising its monthly allowance
// from 50.00 to 5000.00 is none of those things, and a spend control that
// anybody with an account can lift is not a control.
func TestAnOperatorCannotRebriefSomebodyElsesAgent(t *testing.T) {
	h, bob, agent := twoOperators(t)

	before, err := crew.GetAnalyst(h.st.DB(), agent.Name)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf":     {bob.csrf(t, "/")},
		"name":     {agent.Name},
		"role":     {before.Role},
		"mission":  {"whatever bob wants it to do"},
		"desk":     {before.Desk},
		"engine":   {before.Engine},
		"cadence":  {before.Cadence},
		"audience": {before.Audience},
		"parent":   {before.Parent},
		"per_task": {before.PerTask.String()},
		"monthly":  {"5000.00"}, // the escalation, in money
	}
	code, loc := bob.post(t, "/staff/"+agent.Name+"/update", form)
	if !strings.Contains(loc, "msg=") {
		t.Errorf("bob rebriefed alice's agent and was not refused: %d %s", code, loc)
	}

	after, err := crew.GetAnalyst(h.st.DB(), agent.Name)
	if err != nil {
		t.Fatal(err)
	}
	if after.Monthly != before.Monthly {
		t.Errorf("bob raised the monthly guard on alice's agent %q from %s to %s",
			agent.Name, before.Monthly, after.Monthly)
	}
	if after.Mission != before.Mission {
		t.Errorf("bob rewrote the mission of alice's agent %q", agent.Name)
	}
}

// Suspending somebody else's agent stops their work.
func TestAnOperatorCannotChangeTheStateOfSomebodyElsesAgent(t *testing.T) {
	h, bob, agent := twoOperators(t)

	before, _ := crew.GetAnalyst(h.st.DB(), agent.Name)
	code, loc := bob.post(t, "/staff/"+agent.Name+"/state", url.Values{
		"csrf":   {bob.csrf(t, "/")},
		"state":  {"suspended"},
		"reason": {"because bob felt like it"},
	})
	if !strings.Contains(loc, "msg=") {
		t.Errorf("bob changed the state of alice's agent and was not refused: %d %s",
			code, loc)
	}
	after, _ := crew.GetAnalyst(h.st.DB(), agent.Name)
	if after.State != before.State {
		t.Errorf("bob moved alice's agent %q from %q to %q",
			agent.Name, before.State, after.State)
	}
}

// The two that already check, so a regression in mayManage shows up here too.
func TestAnOperatorCannotRemoveOrTransferSomebodyElsesAgent(t *testing.T) {
	h, bob, agent := twoOperators(t)

	code, loc := bob.post(t, "/staff/"+agent.Name+"/remove", url.Values{
		"csrf": {bob.csrf(t, "/")}, "confirm": {agent.Name},
	})
	if !strings.Contains(loc, "who+hired+it") {
		t.Errorf("bob removing alice's agent was not refused for ownership: %d %s",
			code, loc)
	}
	if a, err := crew.GetAnalyst(h.st.DB(), agent.Name); err != nil || a.Name == "" {
		t.Errorf("bob removed alice's agent %q", agent.Name)
	}

	code, loc = bob.post(t, "/staff/"+agent.Name+"/transfer", url.Values{
		"csrf": {bob.csrf(t, "/")}, "owner": {"bob"}, "desk": {agent.Desk},
	})
	if !strings.Contains(loc, "msg=") {
		t.Errorf("bob transferring alice's agent to himself was not refused: %d %s",
			code, loc)
	}
	if a, _ := crew.GetAnalyst(h.st.DB(), agent.Name); a.Owner != "alice" {
		t.Errorf("bob took alice's agent %q: it is now owned by %q",
			agent.Name, a.Owner)
	}
}

// The owner must still be able to manage their own agent, and an admin
// anybody's.
//
// Every test above proves somebody is refused. A fix that refused EVERYBODY
// would pass all of them and leave the console unusable, so this is the other
// half and it is not optional: the ownership check must be a door, not a wall.
func TestTheOwnerAndAnAdminCanStillManage(t *testing.T) {
	h, _, agent := twoOperators(t)
	alice := h.as(t, "alice", "alice-password-2026")

	before, _ := crew.GetAnalyst(h.st.DB(), agent.Name)

	// The owner rebriefs their own.
	code, loc := alice.post(t, "/staff/"+agent.Name+"/update", url.Values{
		"csrf": {alice.csrf(t, "/")}, "name": {agent.Name},
		"role": {before.Role}, "mission": {"a mission alice chose"},
		"desk": {before.Desk}, "engine": {before.Engine},
		"cadence": {before.Cadence}, "audience": {before.Audience},
		"parent": {before.Parent}, "per_task": {before.PerTask.String()},
		"monthly": {before.Monthly.String()},
	})
	if strings.Contains(loc, "msg=") {
		t.Errorf("alice could not rebrief her own agent: %d %s", code, loc)
	}
	if a, _ := crew.GetAnalyst(h.st.DB(), agent.Name); a.Mission != "a mission alice chose" {
		t.Errorf("alice's rebrief of her own agent did not take: mission is %q",
			a.Mission)
	}

	// The owner suspends their own.
	if _, loc := alice.post(t, "/staff/"+agent.Name+"/state", url.Values{
		"csrf": {alice.csrf(t, "/")}, "state": {"suspended"},
		"reason": {"alice is pausing her own agent"},
	}); strings.Contains(loc, "msg=") {
		t.Errorf("alice could not suspend her own agent: %s", loc)
	}
	if a, _ := crew.GetAnalyst(h.st.DB(), agent.Name); a.State != "suspended" {
		t.Errorf("alice's suspension of her own agent did not take: state is %q",
			a.State)
	}

	// And the admin, on somebody else's, which is the case the rule exists
	// for: cleaning up after a person who has left is exactly when the owner
	// cannot act.
	if _, loc := h.post(t, "/staff/"+agent.Name+"/state", url.Values{
		"csrf": {h.csrf(t, "/")}, "state": {"active"},
		"reason": {"the admin is putting it back to work"},
	}); strings.Contains(loc, "msg=") {
		t.Errorf("the admin could not change the state of somebody else's agent: %s", loc)
	}
	if a, _ := crew.GetAnalyst(h.st.DB(), agent.Name); a.State != "active" {
		t.Errorf("the admin's change did not take: state is %q", a.State)
	}

	// The admin removes somebody else's agent, which is the loudest thing this
	// rule allows and the case it exists for.
	//
	// On an agent with open work, which is most of them, the refusal is about
	// the WORK and not about ownership. That is a different rule and a good
	// one, so the check here is that the admin gets past the ownership gate,
	// and then that removal actually happens on an agent that has nothing open.
	idle := anIdleAgent(t, h)
	if _, err := h.st.DB().Exec(`UPDATE analysts SET owner=? WHERE name=?`,
		"alice", idle); err != nil {
		t.Fatal(err)
	}
	_, loc = h.post(t, "/staff/"+idle+"/remove", url.Values{
		"csrf": {h.csrf(t, "/")}, "confirm": {idle},
	})
	if strings.Contains(loc, "who+hired+it") {
		t.Fatalf("the admin was refused removal of alice's agent for ownership: %s", loc)
	}
	if a, err := crew.GetAnalyst(h.st.DB(), idle); err == nil && a.Name != "" {
		t.Errorf("the admin's removal of alice's agent %q did not take: %s", idle, loc)
	}
}

// anIdleAgent finds one with no open work, because removal is refused while a
// name still has tasks on the board and that rule would otherwise be mistaken
// for the ownership rule.
func anIdleAgent(t *testing.T, h *harness) string {
	t.Helper()
	var name string
	err := h.st.DB().QueryRow(`
		SELECT a.name FROM analysts a
		WHERE NOT EXISTS (
			SELECT 1 FROM tasks t
			WHERE t.assignee = a.name AND t.state NOT IN ('done','posted')
		)
		ORDER BY a.name LIMIT 1`).Scan(&name)
	if err != nil || name == "" {
		t.Skipf("no agent without open work in this fixture: %v", err)
	}
	return name
}

// The re-brief page is a control surface, so it follows the same rule as the
// controls on it.
//
// Serving the form to somebody the POST will refuse is not a security hole,
// because the POST does refuse. It is worse in a quieter way: a form that
// cannot work reads as permission, and the reader finds out only after filling
// it in. The card carries everything on this page that is worth reading, so
// there is nothing to lose by sending them there.
func TestTheRebriefPageFollowsOwnership(t *testing.T) {
	h, bob, agent := twoOperators(t)

	code, _, loc := bob.get(t, "/staff/"+agent.Name+"/edit")
	if code == 200 {
		t.Errorf("bob is served the re-brief form for alice's agent %q",
			agent.Name)
	}
	if !strings.Contains(loc, "who+hired+it") {
		t.Errorf("bob was turned away from the re-brief page without being told "+
			"why: %d %s", code, loc)
	}
	// And the card must not offer the link in the first place. Turning
	// somebody away at the door is the backstop; not sending them to it is the
	// point. The card used CanAct here, which asks whether the account may act
	// at all, so every operator was shown a re-brief link on every agent.
	if _, card, _ := bob.get(t, "/staff/"+agent.Name); strings.Contains(card,
		`/staff/`+agent.Name+`/edit`) {
		t.Errorf("alice's agent card offers bob a re-brief link he cannot use")
	}

	// And the owner still gets it, or the page is useless.
	alice := h.as(t, "alice", "alice-password-2026")
	if code, body, loc := alice.get(t, "/staff/"+agent.Name+"/edit"); code != 200 {
		t.Errorf("alice cannot open the re-brief page for her own agent: %d %s",
			code, loc)
	} else if !strings.Contains(body, `action="/staff/`+agent.Name+`/update"`) {
		t.Error("alice's re-brief page carries no update form")
	}
	if _, card, _ := alice.get(t, "/staff/"+agent.Name); !strings.Contains(card,
		`/staff/`+agent.Name+`/edit`) {
		t.Error("alice's own agent card offers her no way to re-brief it")
	}
}

package web_test

import (
	"net/url"
	"strconv"
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

// Hiring makes you the owner, checked by hiring rather than by reading.
//
// Every ownership rule in this file rests on the owner field meaning "the
// account that hired it". If hire wrote something else, or wrote the
// installation's configured owner instead of the person signing the form, then
// mayManage would be checking a name that has nothing to do with who acted.
func TestHiringMakesYouTheOwner(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "boss", "boss-password-2026")
	if _, err := h.au.Create("alice", "alice-password-2026", "operator"); err != nil {
		t.Fatal(err)
	}
	alice := h.as(t, "alice", "alice-password-2026")

	const name = "alices-hire"
	code, loc := alice.post(t, "/staff/create", url.Values{
		"csrf": {alice.csrf(t, "/staff/new")}, "name": {name},
		"role": {"analyst"}, "mission": {"something alice needs done"},
		"desk": {"ai"}, "engine": {"claude-strong"}, "cadence": {"daily"},
		"audience": {"the desk"}, "parent": {""},
		"per_task": {"1.00"}, "monthly": {"50.00"},
		"attestation": {"none"}, "attestation_detail": {""},
	})
	if strings.Contains(loc, "msg=") {
		t.Fatalf("alice could not hire: %d %s", code, loc)
	}
	a, err := crew.GetAnalyst(h.st.DB(), name)
	if err != nil {
		t.Fatalf("the hire did not land: %v", err)
	}
	if a.Owner != "alice" {
		t.Fatalf("alice hired %q and its owner is %q; every ownership rule "+
			"here reads that field", name, a.Owner)
	}
	// And the rule follows from it immediately: she can manage what she hired.
	if _, _, loc := alice.get(t, "/staff/"+name+"/edit"); strings.Contains(loc, "who+hired+it") {
		t.Errorf("alice cannot re-brief the agent she just hired: %s", loc)
	}
}

// A transfer moves the authority with the agent, in both directions.
//
// mayManage reads the owner field at the moment of the request, so this should
// follow. "Should follow" is how the rebrief hole survived, and the failure
// here would be silent in the worse direction: an owner who handed an agent
// over and kept the ability to change its guard is a control that looks
// transferred and is not.
func TestATransferMovesTheAuthorityWithTheAgent(t *testing.T) {
	h, bob, agent := twoOperators(t)
	alice := h.as(t, "alice", "alice-password-2026")

	// Alice, who owns it, hands it to bob.
	_, loc := alice.post(t, "/staff/"+agent.Name+"/transfer", url.Values{
		"csrf": {alice.csrf(t, "/")}, "owner": {"bob"},
		"desk": {agent.Desk}, "parent": {agent.Parent},
	})
	if strings.Contains(loc, "msg=") && !strings.Contains(loc, "moved") {
		t.Fatalf("alice could not transfer her own agent: %s", loc)
	}
	after, err := crew.GetAnalyst(h.st.DB(), agent.Name)
	if err != nil || after.Owner != "bob" {
		t.Fatalf("after the transfer the owner is %q, not bob (%v)", after.Owner, err)
	}

	// Bob can now manage it.
	if _, _, loc := bob.get(t, "/staff/"+agent.Name+"/edit"); strings.Contains(loc, "who+hired+it") {
		t.Errorf("bob was given the agent and cannot re-brief it: %s", loc)
	}

	// And alice cannot, which is the half that would be silent if it were
	// wrong: she handed it over and the console still let her raise its guard.
	before := after.Monthly
	_, loc = alice.post(t, "/staff/"+agent.Name+"/update", url.Values{
		"csrf": {alice.csrf(t, "/")}, "name": {agent.Name},
		"role": {after.Role}, "mission": {after.Mission}, "desk": {after.Desk},
		"engine": {after.Engine}, "cadence": {after.Cadence},
		"audience": {after.Audience}, "parent": {after.Parent},
		"per_task": {after.PerTask.String()}, "monthly": {"9000.00"},
	})
	if !strings.Contains(loc, "who+hired+it") {
		t.Errorf("alice, who gave the agent away, was not refused as a "+
			"non-owner: %s", loc)
	}
	if now, _ := crew.GetAnalyst(h.st.DB(), agent.Name); now.Monthly != before {
		t.Errorf("alice raised the guard on an agent she no longer owns, "+
			"from %s to %s", before, now.Monthly)
	}
	// Nor take it back.
	_, loc = alice.post(t, "/staff/"+agent.Name+"/transfer", url.Values{
		"csrf": {alice.csrf(t, "/")}, "owner": {"alice"}, "desk": {after.Desk},
	})
	if !strings.Contains(loc, "who+hired+it") {
		t.Errorf("alice took back an agent she had given away: %s", loc)
	}
	if now, _ := crew.GetAnalyst(h.st.DB(), agent.Name); now.Owner != "bob" {
		t.Errorf("the agent is owned by %q again", now.Owner)
	}
}

// The spend follows the agent, which is the point of a transfer.
//
// @yurii 2026-08-22, when the transfer was asked for: "Відповідно, витрати
// також мають переходити на інших власників агента." An agent that changes
// hands while its cost stays billed to the previous owner would make the
// owners page a record of who hired somebody once, rather than of who answers
// for what is running now.
func TestATransferMovesTheSpendWithTheAgent(t *testing.T) {
	h, _, agent := twoOperators(t)
	alice := h.as(t, "alice", "alice-password-2026")

	sc, err := crew.Scoreboards(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	spent, open := sc[agent.Name].Spent, sc[agent.Name].Open
	if spent == 0 {
		t.Skip("this fixture's first agent has never been charged; the test " +
			"cannot see the spend move")
	}

	_, before, _ := h.get(t, "/owners")
	if !strings.Contains(before, ">alice<") {
		t.Fatal("alice does not appear on the owners page before the transfer")
	}
	if !strings.Contains(before, spent.String()) {
		t.Fatalf("alice's row does not carry the agent's %s before the transfer",
			spent)
	}

	if _, loc := alice.post(t, "/staff/"+agent.Name+"/transfer", url.Values{
		"csrf": {alice.csrf(t, "/")}, "owner": {"bob"},
		"desk": {agent.Desk}, "parent": {agent.Parent},
	}); strings.Contains(loc, "who+hired+it") {
		t.Fatalf("alice could not transfer her own agent: %s", loc)
	}

	_, after, _ := h.get(t, "/owners")
	bobRow := rowFor(after, "bob")
	if bobRow == "" {
		t.Fatal("bob does not appear on the owners page after being given an agent")
	}
	if !strings.Contains(bobRow, spent.String()) {
		t.Errorf("bob was given the agent and its %s did not come with it; "+
			"his row is %q", spent, bobRow)
	}
	// Alice owns nothing now, so she is off the page rather than sitting there
	// still charged for it.
	if aliceRow := rowFor(after, "alice"); aliceRow != "" {
		if strings.Contains(aliceRow, spent.String()) {
			t.Errorf("alice is still charged %s for an agent she gave away: %q",
				spent, aliceRow)
		}
	}
	// And the open work went too, or the board would hold tasks charged to one
	// owner and assigned under another.
	if open > 0 && !strings.Contains(bobRow, ">"+itoa(open)+"<") {
		t.Logf("bob's row: %q", bobRow)
	}
}

// rowFor returns the table row an owner's name appears in, or "".
func rowFor(body, who string) string {
	i := strings.Index(body, ">"+who+"<")
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(body[:i], "<tr>")
	end := strings.Index(body[i:], "</tr>")
	if start < 0 || end < 0 {
		return ""
	}
	return body[start : i+end]
}

func itoa(n int) string { return strconv.Itoa(n) }

// The transfer message and the owners page must not contradict each other.
//
// The transfer says "work already charged stays where it was charged", which
// is true of the DESK on tasks already closed. The owners page attributes an
// agent's whole lifetime spend to whoever owns it NOW, because tasks record
// only the assignee and never who owned the agent at the time, so past spend
// cannot be attributed to a past owner at all.
//
// Both are true about different dimensions and together they read as a
// contradiction: a reader is told the charge stayed put and then sees it move.
// So each has to name the dimension it is talking about.
func TestTheTransferAndTheOwnersPageAgreeOnWhatMoves(t *testing.T) {
	h, _, agent := twoOperators(t)
	alice := h.as(t, "alice", "alice-password-2026")

	_, loc := alice.post(t, "/staff/"+agent.Name+"/transfer", url.Values{
		"csrf": {alice.csrf(t, "/")}, "owner": {"bob"},
		"desk": {agent.Desk}, "parent": {agent.Parent},
	})
	// The message must say which thing stays, not just that something does.
	if strings.Contains(loc, "stays+where+it+was+charged") &&
		!strings.Contains(loc, "desk") {
		t.Errorf("the transfer says charged work stays put without saying that "+
			"it means the DESK, while the owners page moves the whole figure "+
			"to the new owner: %s", loc)
	}
	// And the owners page has to say that a transferred agent brings its
	// record, or the new owner's total looks like money they spent.
	_, body, _ := h.get(t, "/owners")
	if !strings.Contains(body, "transferred") {
		t.Error("the owners page does not explain that an agent brings its " +
			"whole record when it changes hands, so the last column reads as " +
			"what that person spent")
	}
}

package web_test

// B3-SPEC.md section 6's fourth and sixth named tests, through the console:
// the owner's stamp is what applies a key decision, CSRF and role checks as
// every write route has, and a refusal needs a reason.

import (
	"net/url"
	"strconv"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// plantCarriedOption is a sprint, a posted deliverable and one option
// already carried to owner: the state finops.Supervise itself leaves a
// hands-up option in (supervise_test.go proves the routing there), so this
// test starts from where the owner's page actually begins.
func plantCarriedOption(t *testing.T, h *harness, owner string) (artifact, ordinal, sprintID int) {
	t.Helper()
	return plantCarriedOptionOfClass(t, h, owner, "period.close", "close August")
}

// plantCarriedOptionOfClass is plantCarriedOption with the class named,
// because one test (TestOnlyTheOwnersStampAppliesAKeyDecision) needs a
// SECOND carried option that does not collide with the first one's own side
// effect: applying period.close twice in one sprint closes an already-closed
// period, which finops.Apply then refuses, and a mutant that wrongly calls
// Apply from Post would appear to do nothing for exactly that reason rather
// than because Post itself is innocent.
func plantCarriedOptionOfClass(t *testing.T, h *harness, owner, class, summary string) (artifact, ordinal, sprintID int) {
	t.Helper()
	db := h.st.DB()
	sres, err := db.Exec(`INSERT INTO sprints (label, start, finish, state, goal)
		VALUES ('2026-W98', '2026-09-01', '2026-09-07', 'active', 'a goal')`)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := sres.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	tres, err := db.Exec(`INSERT INTO tasks
		(sprint, title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated, owner)
		VALUES (?, 'a task', 'a goal', 'investigator-aws', 'aws', 'active', 0, 0,
		        datetime('now'), datetime('now'), ?)`, sid, owner)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := tres.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	ares, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created)
		VALUES (?, 'investigator-aws', 'a deliverable', 'body', 'posted', datetime('now'))`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	artID, err := ares.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state)
		VALUES (?, 1, ?, ?, 500000, 0, 'low', 'the owner', '[]', 'carried')`,
		artID, class, summary); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO decision_requests (artifact, sprint, owner, lapses, created)
		VALUES (?,?,?,?,datetime('now'))`, artID, sid, owner, "2026-09-14"); err != nil {
		t.Fatal(err)
	}
	return int(artID), 1, int(sid)
}

func optionState(t *testing.T, h *harness, artifact, ordinal int) crew.OptionState {
	t.Helper()
	o, err := crew.GetOption(h.st.DB(), artifact, ordinal)
	if err != nil {
		t.Fatal(err)
	}
	return o.State
}

// Red first, against the code before this step: there is no /option route at
// all, so nothing could apply a carried option through anybody's form, let
// alone check whose form it was.
func TestOnlyTheOwnersStampAppliesAKeyDecision(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026") // first account: admin
	if _, err := h.au.Create("owner1", "owner1-password-2026", "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.au.Create("rando", "rando-password-2026", "operator"); err != nil {
		t.Fatal(err)
	}
	artID, ordinal, _ := plantCarriedOption(t, h, "owner1")
	path := "/option/" + strconv.Itoa(artID) + "/" + strconv.Itoa(ordinal) + "/apply"

	// An operator who is not the owner is refused, and applies nothing.
	rando := h.as(t, "rando", "rando-password-2026")
	code, loc := rando.post(t, path, url.Values{"csrf": {rando.csrf(t, "/board")}})
	if code != 303 {
		t.Fatalf("rando's apply answered %d, want a redirect", code)
	}
	if optionState(t, h, artID, ordinal) != crew.OptionCarried {
		t.Fatalf("an operator who is not the owner applied a carried option (redirected to %s)", loc)
	}

	// The owner applies it.
	owner := h.as(t, "owner1", "owner1-password-2026")
	code, loc = owner.post(t, path, url.Values{"csrf": {owner.csrf(t, "/board")}})
	if code != 303 {
		t.Fatalf("owner1's apply answered %d, want a redirect", code)
	}
	if got := optionState(t, h, artID, ordinal); got != crew.OptionApplied {
		t.Fatalf("owner1's own stamp did not apply the option: state %q (redirected to %s)", got, loc)
	}
	period, err := crew.GetOption(h.st.DB(), artID, ordinal)
	if err != nil {
		t.Fatal(err)
	}
	if period.DecidedBy != "owner1" {
		t.Errorf("decided_by %q, want owner1", period.DecidedBy)
	}

	// And an analyst's or operator's Post -- the ordinary deliverable stamp
	// -- never applies an option. This is the everyday shape, not the one
	// above: a fresh DRAFT deliverable whose options block named one open
	// option, the same as saveDraft leaves one in. Posting it (draft ->
	// posted) is the routine action every reviewer takes, and it must not
	// also apply the option, which is the property this checks directly
	// rather than by re-posting an already-posted artifact (crew.Post
	// refuses that with ErrSettled before it reaches anything this test
	// could observe).
	art2, ord2 := plantDraftWithOpenOption(t, h, "owner1", "driver.recurring", "a weekly batch job")
	code, loc = h.post(t, "/artifact/"+strconv.Itoa(art2)+"/post",
		url.Values{"csrf": {h.csrf(t, "/board")}})
	if code != 303 {
		t.Fatalf("posting the deliverable answered %d", code)
	}
	if got := optionState(t, h, art2, ord2); got != crew.OptionOpen {
		t.Errorf("posting the deliverable changed the option's state to %q "+
			"(redirected to %s); Post must apply nothing", got, loc)
	}
}

// plantDraftWithOpenOption is a task and a DRAFT deliverable -- not yet
// posted -- carrying one OPEN option: what saveDraft itself leaves behind
// when the options block is legal, and the shape crew.Post's everyday
// caller (a reviewer clicking "Post it" on work.go's task page) actually
// meets.
func plantDraftWithOpenOption(t *testing.T, h *harness, owner, class, summary string) (artifact, ordinal int) {
	t.Helper()
	db := h.st.DB()
	tres, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated, owner)
		VALUES ('a task', 'a goal', 'investigator-aws', 'aws', 'active', 0, 0,
		        datetime('now'), datetime('now'), ?)`, owner)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := tres.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	ares, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created)
		VALUES (?, 'investigator-aws', 'a deliverable', 'body', 'draft', datetime('now'))`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	artID, err := ares.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state)
		VALUES (?, 1, ?, ?, 10000, 0, 'low', 'nothing', '[]', 'open')`,
		artID, class, summary); err != nil {
		t.Fatal(err)
	}
	return int(artID), 1
}

// Red first, against the code before this step: with no /option route, there
// is nothing that could refuse an empty reason either.
func TestARefusalNeedsAReason(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	if _, err := h.au.Create("owner1", "owner1-password-2026", "operator"); err != nil {
		t.Fatal(err)
	}
	artID, ordinal, _ := plantCarriedOption(t, h, "owner1")
	path := "/option/" + strconv.Itoa(artID) + "/" + strconv.Itoa(ordinal) + "/refuse"
	owner := h.as(t, "owner1", "owner1-password-2026")

	code, _ := owner.post(t, path, url.Values{"csrf": {owner.csrf(t, "/board")}, "reason": {""}})
	if code != 303 {
		t.Fatalf("refusing with no reason answered %d", code)
	}
	if got := optionState(t, h, artID, ordinal); got != crew.OptionCarried {
		t.Fatalf("a reasonless refusal changed the option's state to %q", got)
	}

	code, _ = owner.post(t, path, url.Values{
		"csrf": {owner.csrf(t, "/board")}, "reason": {"the desk closed this itself already"},
	})
	if code != 303 {
		t.Fatalf("refusing with a reason answered %d", code)
	}
	got, err := crew.GetOption(h.st.DB(), artID, ordinal)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != crew.OptionRefused {
		t.Fatalf("state %q, want refused", got.State)
	}
	if got.Reason != "the desk closed this itself already" {
		t.Errorf("reason %q was not recorded", got.Reason)
	}
}

// Red first (test d from the review): the owner applying one of two carried
// alternatives of ONE deliverable marks the sibling not_chosen, and the
// decision request posts once nothing of it is left carried.
func TestApplyingOneCarriedOptionMarksItsSiblingNotChosen(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	if _, err := h.au.Create("owner1", "owner1-password-2026", "operator"); err != nil {
		t.Fatal(err)
	}
	artID, ord1, ord2, sprintID := plantTwoCarriedOptions(t, h, "owner1")
	owner := h.as(t, "owner1", "owner1-password-2026")

	path := "/option/" + strconv.Itoa(artID) + "/" + strconv.Itoa(ord1) + "/apply"
	code, loc := owner.post(t, path, url.Values{"csrf": {owner.csrf(t, "/board")}})
	if code != 303 {
		t.Fatalf("applying option %d answered %d", ord1, code)
	}

	got1 := optionState(t, h, artID, ord1)
	got2 := optionState(t, h, artID, ord2)
	if got1 != crew.OptionApplied {
		t.Errorf("option %d state %q, want applied (redirected to %s)", ord1, got1, loc)
	}
	if got2 != crew.OptionNotChosen {
		t.Errorf("option %d (the sibling) state %q, want not_chosen", ord2, got2)
	}

	// And nothing of this deliverable is carried any more: the decision
	// request has nothing left to ask about (PostDecisionRequestIfComplete's
	// own unit-level behaviour is decision_test.go's -- not repeated here).
	remaining, err := crew.CarriedOptionsFor(h.st.DB(), sprintID, "owner1")
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Errorf("%d options still carried for owner1 after the choice was made, want 0", len(remaining))
	}
}

// plantTwoCarriedOptions is a sprint, a posted deliverable with TWO carried
// alternatives on it, and the decision request that already carries both --
// what finops.Supervise itself leaves behind for a deliverable whose choice
// it could not decide (supervise_test.go proves the routing there).
func plantTwoCarriedOptions(t *testing.T, h *harness, owner string) (artifact, ordinal1, ordinal2, sprintID int) {
	t.Helper()
	db := h.st.DB()
	sres, err := db.Exec(`INSERT INTO sprints (label, start, finish, state, goal)
		VALUES ('2026-W95', '2026-08-11', '2026-08-17', 'active', 'a goal')`)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := sres.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	tres, err := db.Exec(`INSERT INTO tasks
		(sprint, title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated, owner)
		VALUES (?, 'a task', 'a goal', 'investigator-aws', 'aws', 'active', 0, 0,
		        datetime('now'), datetime('now'), ?)`, sid, owner)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := tres.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	ares, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created)
		VALUES (?, 'investigator-aws', 'a deliverable', 'body', 'posted', datetime('now'))`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	artID, err := ares.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state)
		VALUES (?, 1, 'period.close', 'close August', 500000, 0, 'low', 'the owner', '[]', 'carried')`,
		artID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state)
		VALUES (?, 2, 'budget.set', 'raise the budget instead', 400000, 0, 'low', 'the owner', '[]', 'carried')`,
		artID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO decision_requests (artifact, sprint, owner, lapses, created)
		VALUES (?,?,?,?,datetime('now'))`, artID, sid, owner, "2026-08-24"); err != nil {
		t.Fatal(err)
	}
	return int(artID), 1, 2, int(sid)
}

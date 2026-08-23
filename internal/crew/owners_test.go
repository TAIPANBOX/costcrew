package crew_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// seeded gives a database with the roster in it and an auth to make accounts
// against, which is what every test here starts from.
func withRoster(t *testing.T) (*sql.DB, *auth.Auth) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := crew.SeedRoster(st.DB(), "installer"); err != nil {
		t.Fatal(err)
	}
	if err := crew.EnsureOwnershipHistory(st.DB()); err != nil {
		t.Fatal(err)
	}
	au, err := auth.New(st, dir)
	if err != nil {
		t.Fatal(err)
	}
	return st.DB(), au
}

func ownerCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	rows, err := db.Query(`SELECT COALESCE(owner,''), COUNT(*) FROM analysts GROUP BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var who string
		var n int
		if err := rows.Scan(&who, &n); err != nil {
			t.Fatal(err)
		}
		out[who] = n
	}
	return out
}

func TestSeedOwnersPlacesTheWholeRoster(t *testing.T) {
	db, au := withRoster(t)
	accounts, moved, err := crew.SeedOwners(db, au, "installer")
	if err != nil {
		t.Fatal(err)
	}
	if accounts != len(crew.Owners) {
		t.Errorf("created %d accounts, the list has %d", accounts, len(crew.Owners))
	}
	counts := ownerCounts(t, db)
	// Nobody is left holding the installer's name. This is the failure that
	// happened: a desk with no entry in the map kept every agent on it, and
	// twelve of thirty-nine sat under the seeding account while the page
	// looked plausible.
	if n := counts["installer"]; n != 0 {
		t.Errorf("%d agents are still owned by the seeding account; a desk with "+
			"no owner in the map keeps everything on it", n)
	}
	var placed int
	for _, who := range crew.Owners {
		placed += counts[who]
	}
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM analysts`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if placed != total || moved != total {
		t.Errorf("placed %d and reported %d moved, of %d on the roster",
			placed, moved, total)
	}
}

// Every desk the estate actually has must have an owner.
//
// This is the test that would have caught it: the map covered the five cloud
// desks and not "management", which holds the practice roles, so a fifth of
// the roster stayed with the installer and the page still rendered five
// owners and looked right.
func TestEveryDeskHasAnOwner(t *testing.T) {
	db, _ := withRoster(t)
	owners := crew.DeskOwners()
	rows, err := db.Query(`SELECT DISTINCT COALESCE(desk,'') FROM analysts`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var desk string
		if err := rows.Scan(&desk); err != nil {
			t.Fatal(err)
		}
		if desk == "" {
			continue
		}
		if owners[desk] == "" {
			t.Errorf("the %q desk has agents and no owner, so every one of "+
				"them stays with whoever seeded the installation", desk)
		}
	}
}

func TestSeedOwnersIsIdempotent(t *testing.T) {
	db, au := withRoster(t)
	if _, _, err := crew.SeedOwners(db, au, "installer"); err != nil {
		t.Fatal(err)
	}
	first := ownerCounts(t, db)

	accounts, moved, err := crew.SeedOwners(db, au, "installer")
	if err != nil {
		t.Fatal(err)
	}
	if accounts != 0 || moved != 0 {
		t.Errorf("a second run created %d accounts and moved %d agents; it runs "+
			"on every start, so it has to do nothing the second time",
			accounts, moved)
	}
	for who, n := range first {
		if ownerCounts(t, db)[who] != n {
			t.Errorf("%s held %d agents and now holds %d", who, n,
				ownerCounts(t, db)[who])
		}
	}
}

// A placement a person made must survive.
//
// SeedOwners runs on every start. If it re-homed by desk unconditionally, then
// every transfer anybody made would be undone the next time the console
// restarted, silently, and the person who made it would find their agent back
// where it started with no record of why.
func TestSeedOwnersDoesNotUndoAPlacementAPersonMade(t *testing.T) {
	db, au := withRoster(t)
	if _, _, err := crew.SeedOwners(db, au, "installer"); err != nil {
		t.Fatal(err)
	}
	var name, was string
	if err := db.QueryRow(
		`SELECT name, owner FROM analysts ORDER BY name LIMIT 1`).Scan(&name, &was); err != nil {
		t.Fatal(err)
	}
	if _, err := au.Create("someone.else", "a-password-long-enough-2026", "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := crew.Transfer(db, name, "", "someone.else", "", "a-person"); err != nil {
		t.Fatal(err)
	}

	if _, moved, err := crew.SeedOwners(db, au, "installer"); err != nil {
		t.Fatal(err)
	} else if moved != 0 {
		t.Errorf("a restart moved %d agents", moved)
	}
	var now string
	if err := db.QueryRow(`SELECT owner FROM analysts WHERE name=?`, name).Scan(&now); err != nil {
		t.Fatal(err)
	}
	if now != "someone.else" {
		t.Errorf("a restart put %s back with %q, undoing a transfer a person "+
			"made; it was %q before the restart", name, now, "someone.else")
	}
}

// The accounts it creates cannot be signed in to.
//
// An owner has to be an account that exists, because an agent owned by a name
// nobody can sign in as is an agent nobody answers for. That is not a reason
// to leave five signable accounts on somebody's machine, and the failure mode
// is quiet: an empty password, or one derived from the name, would put five
// working logins on every installation and nothing on any page would say so.
func TestSeedOwnersCreatesAccountsNobodyCanUse(t *testing.T) {
	db, au := withRoster(t)
	if _, _, err := crew.SeedOwners(db, au, "installer"); err != nil {
		t.Fatal(err)
	}
	for _, who := range crew.Owners {
		// The guesses somebody would actually try, and the ones a lazy
		// implementation would have used.
		for _, pw := range []string{
			"", who, "password", "changeme", who + who,
			strings.TrimSuffix(who, ".ashby"), // the initial alone
		} {
			if u, _, err := au.Authenticate(who, pw); err == nil && u != nil {
				t.Errorf("signed in as %q with %q", who, pw)
			}
		}
		// And the account is really there, or the loop above proves nothing.
		if has, err := au.Exists(who); err != nil || !has {
			t.Errorf("%s was not created (%v)", who, err)
		}
	}
}

// Two installations must not get the same password.
//
// Checked at the source, because it is not observable downstream. The first
// version of this test compared the stored hashes of two installations and was
// worthless: those carry a per-hash salt, so a HARD-CODED password produces a
// different hash every time and the test passed against exactly the mistake it
// was written to catch. Planting that fault is what showed it.
func TestUnusablePasswordIsDifferentEveryTime(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		pw, err := crew.UnusablePassword()
		if err != nil {
			t.Fatal(err)
		}
		if seen[pw] {
			t.Fatalf("the same password came back twice in %d draws; one string "+
				"would then open every installation of this console", i+1)
		}
		seen[pw] = true
		if len(pw) < 40 {
			t.Errorf("a %d-character password is short enough to be attacked "+
				"offline once somebody has the database", len(pw))
		}
	}
}

// An account somebody has already set up is left exactly as it is.
//
// This runs on every start. If it reset the password of an account that
// already existed, an owner who had been given a real one would be locked out
// by a restart, and the console would not say why. If it reset the ROLE, an
// admin among the owners would be quietly demoted to operator.
//
// The guarantee is doubled and this test does not care which half holds: the
// Exists check here skips it, and auth.create refuses a username that is taken
// whatever the caller does. Removing the check in this package leaves the test
// green, which is the correct answer to "is the account safe" and the wrong
// answer to "did I write the check", so the count below is what covers the
// second question.
func TestSeedOwnersLeavesAnExistingAccountAlone(t *testing.T) {
	db, au := withRoster(t)
	const who, pw = "t.langley", "a-real-password-somebody-set-2026"
	if ok, err := au.Create(who, pw, "admin"); err != nil || !ok {
		t.Fatalf("setting the account up first: %v %v", ok, err)
	}

	if accounts, _, err := crew.SeedOwners(db, au, "installer"); err != nil {
		t.Fatal(err)
	} else if accounts != len(crew.Owners)-1 {
		t.Errorf("created %d accounts; one of the five already existed",
			accounts)
	}
	// Still able to sign in, with the password a person chose.
	if u, _, err := au.Authenticate(who, pw); err != nil || u == nil {
		t.Fatalf("%s can no longer sign in after a restart: %v", who, err)
	} else if u.Role != "admin" {
		t.Errorf("%s was an admin and a restart made them %q", who, u.Role)
	}
}

// The charges move only for the agents it placed.
//
// Work already recorded against somebody else is not the seeding account's to
// hand over, and a sweep that took it would silently rewrite what a person had
// authorised.
func TestSeedOwnersMovesOnlyTheChargesItPlaced(t *testing.T) {
	db, au := withRoster(t)
	if err := seedSomeWork(db); err != nil {
		t.Fatal(err)
	}
	// One charge that belongs to a person, not to the installer.
	if _, err := db.Exec(
		`UPDATE tasks SET owner='a.person' WHERE id=(SELECT MIN(id) FROM tasks)`); err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := db.QueryRow(
		`SELECT COALESCE(SUM(spent_cents),0) FROM tasks`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	if _, _, err := crew.SeedOwners(db, au, "installer"); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM tasks WHERE owner='a.person'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("the charge recorded against a.person was swept up: %d left", n)
	}
	if n := countTasksOwnedBy(t, db, "installer"); n != 0 {
		t.Errorf("%d charges are still against the seeding account", n)
	}
	// And no money appeared or vanished on the way.
	var after int64
	if err := db.QueryRow(
		`SELECT COALESCE(SUM(spent_cents),0) FROM tasks`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("the estate was worth %d and is now worth %d", before, after)
	}
}

func countTasksOwnedBy(t *testing.T, db *sql.DB, who string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE owner=?`, who).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// seedSomeWork puts charges on the board owned by the seeding account, which
// is the state an installation is in before owners exist.
func seedSomeWork(db *sql.DB) error {
	rows, err := db.Query(`SELECT name FROM analysts ORDER BY name LIMIT 6`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		names = append(names, n)
	}
	for i, n := range names {
		if _, err := db.Exec(`INSERT INTO tasks
			(sprint, title, assignee, desk, state, budget_cents, spent_cents, owner)
			VALUES (1,?,?,'ai','posted',1000,?,'installer')`,
			"work "+n, n, int64((i+1)*137)); err != nil {
			return err
		}
	}
	return nil
}

// A new charge is stamped with whoever owns the agent at that moment.
//
// This is the write half of the feature and the one everything else rests on:
// if the stamp is wrong or missing, the history is a column of empty strings
// and every owner figure falls back to being unattributable.
//
// The owner is read at the charge rather than passed in, because a caller
// holding an Analyst loaded before a transfer would stamp the previous owner
// and record a handover as though it had not happened. That is what the second
// half of this test checks.
func TestANewChargeIsStampedWithTheCurrentOwner(t *testing.T) {
	db, au := withRoster(t)
	if _, _, err := crew.SeedOwners(db, au, "installer"); err != nil {
		t.Fatal(err)
	}
	var agent, owner string
	if err := db.QueryRow(
		`SELECT name, owner FROM analysts ORDER BY name LIMIT 1`).Scan(&agent, &owner); err != nil {
		t.Fatal(err)
	}
	if owner == "" {
		t.Fatal("the first agent has no owner, so this test cannot see a stamp")
	}

	id, err := crew.FromAnomaly(db, "an-1", "a new piece of work",
		"something that will be charged", agent, "ai", money.Cents(1500))
	if err != nil {
		t.Fatal(err)
	}
	if got := taskOwner(t, db, id); got != owner {
		t.Errorf("a charge against %s was stamped %q; the agent is owned by %q",
			agent, got, owner)
	}

	// Now hand the agent over and charge it again. The second charge belongs
	// to the new owner, and the first still belongs to the old one.
	if _, err := au.Create("n.baxter", "a-password-long-enough-2026", "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := crew.Transfer(db, agent, "", "n.baxter", "", "a-person"); err != nil {
		t.Fatal(err)
	}
	id2, err := crew.FromAnomaly(db, "an-2", "work after the handover",
		"charged to whoever holds it now", agent, "ai", money.Cents(1500))
	if err != nil {
		t.Fatal(err)
	}
	if got := taskOwner(t, db, id2); got != "n.baxter" {
		t.Errorf("a charge made after the handover was stamped %q, not the new "+
			"owner: the owner is being read from something stale", got)
	}
	// The first one moved with the transfer, because it is still open. That is
	// the rule, and it is asserted here so a change to it cannot pass silently.
	if got := taskOwner(t, db, id); got != "n.baxter" {
		t.Errorf("the first charge is still open and did not move with the "+
			"agent: it is stamped %q", got)
	}
}

func taskOwner(t *testing.T, db *sql.DB, id int) string {
	t.Helper()
	var who string
	if err := db.QueryRow(`SELECT COALESCE(owner,'') FROM tasks WHERE id=?`,
		id).Scan(&who); err != nil {
		t.Fatal(err)
	}
	return who
}

// An installation started with no -stack-owner flag still gets its agents
// placed.
//
// SeedRoster substitutes "unclaimed" for an empty owner. SeedOwners was then
// handed the empty configured value, matched nothing, and left all thirty-nine
// agents unplaced while cheerfully reporting five owner accounts created.
// Which is every installation anybody tries for the first time, and the log
// line said "0 agent(s) placed" in the same sentence as the success.
func TestSeedOwnersPlacesTheRosterWithNoOwnerConfigured(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	// Seeded the way a bare `costcrew -data ./local` seeds it: no owner given.
	if _, err := crew.SeedRoster(st.DB(), ""); err != nil {
		t.Fatal(err)
	}
	if err := crew.EnsureOwnershipHistory(st.DB()); err != nil {
		t.Fatal(err)
	}
	au, err := auth.New(st, dir)
	if err != nil {
		t.Fatal(err)
	}

	_, moved, err := crew.SeedOwners(st.DB(), au, crew.SeededOwner(""))
	if err != nil {
		t.Fatal(err)
	}
	var total int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM analysts`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if moved != total {
		t.Errorf("placed %d of %d agents on an installation started with no "+
			"-stack-owner", moved, total)
	}
	var stranded int
	if err := st.DB().QueryRow(
		`SELECT COUNT(*) FROM analysts WHERE owner='unclaimed' OR owner=''`).Scan(&stranded); err != nil {
		t.Fatal(err)
	}
	if stranded != 0 {
		t.Errorf("%d agents are still unclaimed after the owners were seeded", stranded)
	}
}

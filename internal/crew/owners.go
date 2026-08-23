package crew

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"sort"
)

// Owners is who answers for the agents in this demo estate.
//
// An initial, a dot and a surname, all lower case, because first names alone
// stop working the moment there are two Johns and an estate that grows owners
// is exactly where this page earns its place. Two of these ARE two Johns, so
// the shape is doing its job in the fixture rather than only in principle.
//
// What is written here is what is shown: there is no separate display form, so
// there is nothing that can drift into disagreeing about who somebody is, and
// nothing to decide about capitals on one page and not another.
//
// The surnames are invented and deliberately not the real ones belonging to
// the people whose first names these are. Fixture data ends up in screenshots,
// and a screenshot is a poor place to publish somebody's family name.
var Owners = []string{
	"y.mercer",    // Yurii
	"t.langley",   // Tania
	"a.whitfield", // Anna
	"j.ashby",     // Jack
	"j.calder",    // John
}

// ownerOfDesk maps a desk to the person who answers for its agents.
//
// By desk, because that is the unit the estate is already organised in: an
// owner who holds half of one desk and a third of another answers for nothing
// anybody can hold a conversation about. The AI desk stays with the
// installation's owner, since it is the crew running the console itself.
// DeskOwners is ownerOfDesk, for a test that needs to know which desks this
// list claims to cover. Returned as a copy: a caller that could edit the map
// could silently re-home half the estate.
func DeskOwners() map[string]string {
	out := make(map[string]string, len(ownerOfDesk))
	for k, v := range ownerOfDesk {
		out[k] = v
	}
	return out
}

var ownerOfDesk = map[string]string{
	"ai":     "y.mercer",
	"aws":    "t.langley",
	"gcp":    "a.whitfield",
	"azure":  "j.ashby",
	"onprem": "j.calder",
	"saas":   "j.calder",
	// The practice roles: supervisor, forecaster, governance, KPI steward and
	// the rest. They are not cloud-specific, so they sit with whoever runs the
	// practice rather than being split by role, which would leave every owner
	// holding a handful of jobs and nobody holding a conversation.
	"management": "y.mercer",
}

// AccountMaker is the part of the auth package this needs, named here so the
// crew package does not depend on the whole of it.
type AccountMaker interface {
	// Exists rather than Get, because this only needs to know whether to
	// create one. Taking the whole *auth.User would put the password hash in
	// reach of a package that has no business holding it.
	Exists(username string) (bool, error)
	Create(username, password, role string) (bool, error)
}

// UnusablePassword is a password nobody holds: 32 random bytes, returned once
// and never recorded anywhere.
//
// A named function rather than four lines inline, because the property that
// matters here is only testable at the source. Comparing stored hashes proves
// nothing: they carry a per-hash salt, so a HARD-CODED password still produces
// a different hash on every installation and every check downstream passes
// while one string opens every CostCrew anywhere.
func UnusablePassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(b), nil
}

// SeedOwners gives the estate more than one person to answer for it.
//
// The accounts are created with a password nobody holds: 32 random bytes,
// discarded immediately. An owner has to be an account that exists, because an
// agent owned by a name nobody can sign in as is an agent nobody answers for,
// but that is not a reason to put five signable accounts with known passwords
// on somebody's machine. An admin gives one a real password with
// -set-password when a person actually needs to sign in.
//
// Both halves are idempotent and neither overrides a decision already made:
// an existing account is left alone, and an agent is only moved if it is still
// owned by whoever seeded it.
func SeedOwners(db *sql.DB, mk AccountMaker, seededBy string) (accounts, moved int, err error) {
	for _, who := range Owners {
		has, err := mk.Exists(who)
		if err != nil {
			return accounts, moved, fmt.Errorf("looking up %s: %w", who, err)
		}
		if has {
			continue // already there, with whatever role and password it has
		}
		pw, err := UnusablePassword()
		if err != nil {
			return accounts, moved, err
		}
		ok, err := mk.Create(who, pw, "operator")
		if err != nil {
			return accounts, moved, fmt.Errorf("creating %s: %w", who, err)
		}
		if ok {
			accounts++
		}
	}

	// Distribute, but only what nobody has deliberately placed. An agent whose
	// owner is not the seeding account has been hired or transferred by a
	// person, and this must not undo that.
	desks := make([]string, 0, len(ownerOfDesk))
	for d := range ownerOfDesk {
		desks = append(desks, d)
	}
	sort.Strings(desks) // a map range would make the journal a different story each run

	for _, desk := range desks {
		res, err := db.Exec(`UPDATE analysts SET owner=? WHERE desk=? AND owner=?`,
			ownerOfDesk[desk], desk, seededBy)
		if err != nil {
			return accounts, moved, fmt.Errorf("placing the %s desk: %w", desk, err)
		}
		n, _ := res.RowsAffected()
		moved += int(n)
	}
	// The charges already recorded against the seeding account follow, once.
	// They were stamped when there was only one owner, so leaving them would
	// show four people owning agents that have never cost anything and one
	// person carrying every charge in the estate.
	if moved > 0 {
		if _, err := db.Exec(`
			UPDATE tasks
			   SET owner = COALESCE(
			       (SELECT a.owner FROM analysts a WHERE a.name = tasks.assignee),
			       tasks.owner)
			 WHERE owner = ?`, seededBy); err != nil {
			return accounts, moved, fmt.Errorf("moving the charges: %w", err)
		}
	}
	return accounts, moved, nil
}

package crew

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// Analyst is one member of the crew as the console holds it, which is a
// different thing from the fixture's static list: hiring, suspending and
// re-briefing all have to persist, and a Go slice cannot be edited by a form.
//
// The governance fields are the point of this type existing. In the original,
// hiring an analyst and registering it with the governance stack were two
// unrelated acts, and the second one mostly did not happen. Here they are the
// same act, because at hire time the operator already has every answer in
// their head: they have just decided this analyst's desk, its rights and its
// budget. Asking again three screens later is how the metadata ends up empty.
type Analyst struct {
	Name     string
	Role     string
	Mission  string
	Desk     string
	Engine   string
	State    string
	Reason   string
	Skills   []string
	Rights   []string
	PerTask  money.Cents
	Monthly  money.Cents
	Cadence  string // daily, weekly, fortnightly, monthly, on-request
	Audience string

	// Governance, decided at hire time and never separately.
	Owner       string // the account that hired it
	Parent      string // who it acts on behalf of
	Attestation string // none, oidc, spiffe-svid, enclave-key, mtls-cert
	// The evidence that makes the method checkable: an issuer, a SPIFFE ID, a
	// fingerprint. Empty is only valid alongside "none"; see attestation.go
	// for why a method without one is worse than no method at all.
	AttestationDetail string
	Hired             string
}

// ensureRoster creates the table and adds any column a newer build expects.
//
// CREATE TABLE IF NOT EXISTS does nothing to a table that already exists, so a
// console installed before a column was added never gets it and every query
// naming it fails. SQLite has no ADD COLUMN IF NOT EXISTS, and the duplicate
// error is the normal path here rather than a problem: it means the column is
// already where it should be.
func ensureRoster(db *sql.DB) error {
	if _, err := db.Exec(RosterSchema); err != nil {
		return err
	}
	for _, col := range []string{
		"attestation_detail TEXT",
	} {
		if _, err := db.Exec("ALTER TABLE analysts ADD COLUMN " + col); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("adding %s: %w", col, err)
		}
	}
	return nil
}

const RosterSchema = `
CREATE TABLE IF NOT EXISTS analysts(
  name TEXT PRIMARY KEY, role TEXT, mission TEXT, desk TEXT, engine TEXT,
  state TEXT NOT NULL, reason TEXT, skills TEXT, rights TEXT,
  per_task_cents INTEGER, monthly_cents INTEGER, cadence TEXT, audience TEXT,
  owner TEXT, parent TEXT, attestation TEXT, attestation_detail TEXT, hired TEXT);
`

var (
	Cadences = []string{"daily", "weekly", "fortnightly", "monthly", "on-request"}
	States   = []string{"active", "suspended", "restricted", "probation", "onboarding"}
	Rights   = []string{
		"figures-read", "sql-readonly", "budgets-read",
		"propose-only", "close-covered", "channel-post", "publish-explainer",
		"export-data", "kpi-registry",
	}
)

// SkillPool is what the hire form offers: every skill this console knows how
// to back with rights, and nothing else.
//
// It used to be written out by hand, fifteen entries deep while
// rightsForSkill already carried thirty-eight, and thirty of the roster's own
// forty-five skill strings were never offered to a person hiring by hand.
// Deriving it from rightsForSkill instead means the two can no longer drift:
// a skill this console cannot back with a right can never be offered, and a
// skill it can back can never go missing from the form.
// TestSkillPoolIsExactlyTheSkillsWithRights holds the two together.
var SkillPool = sortedSkillKeys(rightsForSkill)

func sortedSkillKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for s := range m {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// SeedRoster copies the fixture's crew into the store, once.
func SeedRoster(db *sql.DB, owner string) (int, error) {
	if err := ensureRoster(db); err != nil {
		return 0, err
	}
	var have int
	if err := db.QueryRow(`SELECT COUNT(*) FROM analysts`).Scan(&have); err != nil {
		return 0, err
	}
	if have > 0 {
		return 0, nil
	}
	if owner == "" {
		owner = "unclaimed"
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// Which desks have a partner, so the delegation tree can route through one
	// where it exists instead of flattening everything onto the supervisor.
	hasPartner := map[string]bool{}
	for _, a := range world.Crew {
		if strings.HasPrefix(a.Name, "partner-") {
			hasPartner[a.Desk] = true
		}
	}

	n := 0
	for i, a := range world.Crew {
		rights := RightsFor(a.Skills, string(a.State))
		if _, err := tx.Exec(`INSERT INTO analysts
			(name, role, mission, desk, engine, state, reason, skills, rights,
			 per_task_cents, monthly_cents, cadence, audience, owner, parent,
			 attestation, attestation_detail, hired)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			a.Name, a.Role, missionFor(a), a.Desk, a.Engine, string(a.State), nullIf(a.Reason),
			strings.Join(a.Skills, ","), strings.Join(rights, ","),
			int64(money.MustParse(a.PerTaskUSD)),
			int64(money.MustParse(a.MonthlyUSD)),
			cadenceFor(a.Name, a.Role), audienceFor(a.Name, a.Desk),
			owner, nullIf(parentFor(a.Name, a.Desk, hasPartner)),
			// NOT derived. See attestation.go: reading a permission list and
			// writing a security claim is how twelve agents came to be
			// unflagged by an identity graph that had been told they were
			// bound to something.
			"none", "", hiredOn(i)); err != nil {
			return n, err
		}
		n++
	}
	return n, tx.Commit()
}

func Roster(db *sql.DB) ([]Analyst, error) {
	rows, err := db.Query(`SELECT name, COALESCE(role,''), COALESCE(mission,''),
		COALESCE(desk,''), COALESCE(engine,''), state, COALESCE(reason,''),
		COALESCE(skills,''), COALESCE(rights,''), COALESCE(per_task_cents,0),
		COALESCE(monthly_cents,0), COALESCE(cadence,''), COALESCE(audience,''),
		COALESCE(owner,''), COALESCE(parent,''), COALESCE(attestation,'none'),
		COALESCE(attestation_detail,''), COALESCE(hired,'')
		FROM analysts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Analyst
	for rows.Next() {
		var a Analyst
		var skills, rights string
		var perTask, monthly int64
		if err := rows.Scan(&a.Name, &a.Role, &a.Mission, &a.Desk, &a.Engine,
			&a.State, &a.Reason, &skills, &rights, &perTask, &monthly,
			&a.Cadence, &a.Audience, &a.Owner, &a.Parent, &a.Attestation,
			&a.AttestationDetail,
			&a.Hired); err != nil {
			return nil, err
		}
		a.Skills = splitList(skills)
		a.Rights = splitList(rights)
		a.PerTask, a.Monthly = money.Cents(perTask), money.Cents(monthly)
		out = append(out, a)
	}
	return out, rows.Err()
}

func GetAnalyst(db *sql.DB, name string) (Analyst, error) {
	all, err := Roster(db)
	if err != nil {
		return Analyst{}, err
	}
	for _, a := range all {
		if a.Name == name {
			return a, nil
		}
	}
	return Analyst{}, fmt.Errorf("no such analyst: %q", name)
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// ------------------------------------------------------------------- hire

var nameOK = func(s string) bool {
	if len(s) < 3 || len(s) > 40 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
			return false
		}
	}
	return true
}

// Hire creates an analyst and its governance identity in ONE act.
//
// The name is constrained because it becomes an agent:// URI, and a URI is a
// contract other services parse. Letting a form produce "Reporter (AWS desk)"
// as an identifier is how a shared event stream fills with ids nobody can
// match.
func Hire(db *sql.DB, a Analyst) error {
	// Trimmed, never LOWERCASED. Silently normalising an identifier means the
	// agent:// URI that reaches the event stream is not the one the operator
	// typed and saw, which is a surprise nobody wants to find in an audit. The
	// form says lower-case; typing otherwise is told, not corrected.
	a.Name = strings.TrimSpace(a.Name)
	if !nameOK(a.Name) {
		return fmt.Errorf("a name becomes part of an agent:// identity, so it must be " +
			"3 to 40 characters of lower-case letters, digits and hyphens")
	}
	if strings.TrimSpace(a.Role) == "" {
		return fmt.Errorf("an analyst with no role is one nobody can review")
	}
	if a.PerTask <= 0 || a.Monthly <= 0 {
		return fmt.Errorf("both guards must be above zero: an analyst with no ceiling " +
			"is one that cannot be stopped by anything except somebody noticing")
	}
	if a.PerTask > a.Monthly {
		return fmt.Errorf("the per-task guard (%s) is above the monthly one (%s), so "+
			"the monthly ceiling could never be reached", a.PerTask, a.Monthly)
	}
	if err := ValidAttestation(a.Attestation, a.AttestationDetail); err != nil {
		return err
	}
	if !contains(Cadences, a.Cadence) {
		return fmt.Errorf("cadence must be one of %s", strings.Join(Cadences, ", "))
	}
	if _, err := GetAnalyst(db, a.Name); err == nil {
		return fmt.Errorf("%q is already on the crew", a.Name)
	}
	if a.Parent == a.Name {
		return fmt.Errorf("an analyst cannot act on its own behalf")
	}
	if err := ensureRoster(db); err != nil {
		return err
	}
	_, err := db.Exec(`INSERT INTO analysts
		(name, role, mission, desk, engine, state, reason, skills, rights,
		 per_task_cents, monthly_cents, cadence, audience, owner, parent,
		 attestation, attestation_detail, hired)
		VALUES (?,?,?,?,?,?,NULL,?,?,?,?,?,?,?,?,?,?,?)`,
		a.Name, a.Role, a.Mission, a.Desk, a.Engine, "active",
		strings.Join(a.Skills, ","), strings.Join(a.Rights, ","),
		int64(a.PerTask), int64(a.Monthly), a.Cadence, a.Audience,
		a.Owner, nullIf(a.Parent), a.Attestation, strings.TrimSpace(a.AttestationDetail),
		time.Now().UTC().Format("2006-01-02"))
	return err
}

// SetState takes an analyst off the rota, or puts it back.
//
// Anything other than active REQUIRES a reason, and the reason is shown on the
// card. Suspension does not touch a single thing the analyst already did: it
// is a pause, never an undo.
func SetState(db *sql.DB, name, state, reason string) error {
	if !contains(States, state) {
		return fmt.Errorf("no such state: %q", state)
	}
	if state != "active" && strings.TrimSpace(reason) == "" {
		return fmt.Errorf("taking an analyst off the rota needs a reason: without one " +
			"nobody can tell a decision from an oversight")
	}
	if _, err := GetAnalyst(db, name); err != nil {
		return err
	}
	if state == "active" {
		reason = ""
	}
	_, err := db.Exec(`UPDATE analysts SET state=?, reason=? WHERE name=?`,
		state, nullIf(reason), name)
	return err
}

// Rebrief changes what an analyst is for. Guards and identity are re-checked,
// because an edit that could not have been hired is one that should not stand.
func Rebrief(db *sql.DB, a Analyst) error {
	cur, err := GetAnalyst(db, a.Name)
	if err != nil {
		return err
	}
	if a.PerTask <= 0 || a.Monthly <= 0 || a.PerTask > a.Monthly {
		return fmt.Errorf("the guards must both be above zero and the per-task one " +
			"must not exceed the monthly one")
	}
	if !contains(Cadences, a.Cadence) {
		return fmt.Errorf("cadence must be one of %s", strings.Join(Cadences, ", "))
	}
	_, err = db.Exec(`UPDATE analysts SET role=?, mission=?, desk=?, engine=?,
		skills=?, rights=?, per_task_cents=?, monthly_cents=?, cadence=?, audience=?
		WHERE name=?`,
		a.Role, a.Mission, a.Desk, a.Engine,
		strings.Join(a.Skills, ","), strings.Join(a.Rights, ","),
		int64(a.PerTask), int64(a.Monthly), a.Cadence, a.Audience, cur.Name)
	return err
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ActiveNames is the rota: who can actually be given work.
func ActiveNames(db *sql.DB) ([]string, error) {
	all, err := Roster(db)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range all {
		if a.State == "active" {
			out = append(out, a.Name)
		}
	}
	return out, nil
}

// ------------------------------------------------------ removing an analyst

// ErrHasWork is returned when an analyst still has work on the board.
var ErrHasWork = errors.New("this analyst still has open work")

// OpenWork counts what an analyst still owes, so a caller can say what has to
// happen before it can be removed rather than just refusing.
func OpenWork(db *sql.DB, name string) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM tasks
		WHERE assignee = ? AND state IN ('queued','active','blocked','returned')`, name).Scan(&n)
	return n, err
}

// Remove takes an analyst off the roster.
//
// Three things it refuses, and each is a way a console loses track of money:
//
//   - an analyst with open work. The work does not disappear with it, it
//     becomes work assigned to a name nobody can open, and the crew page then
//     reports spend against an agent that is not on the roster. Reassign or
//     transfer first, and the error says which.
//   - an analyst that other analysts act under. Removing it orphans them, and
//     a delegation chain with a missing link is a chain that proves nothing.
//   - the supervisor, which every default parent points at.
//
// What it does NOT do is delete the analyst's history. Its finished tasks, its
// artifacts and its journal entries stay exactly where they are: an agent
// being taken off the rota does not unspend what it spent, and a console that
// tidied that away would be one whose totals changed when somebody resigned.
func Remove(db *sql.DB, name, by string) error {
	if _, err := GetAnalyst(db, name); err != nil {
		return err
	}
	if name == "supervisor" {
		return errors.New("the supervisor is what every other analyst acts under; " +
			"removing it would orphan the whole crew")
	}
	open, err := OpenWork(db, name)
	if err != nil {
		return err
	}
	if open > 0 {
		return fmt.Errorf("%w: %d open %s. Reassign or close them first, "+
			"or the board will hold work charged to a name nobody can open",
			ErrHasWork, open, plural(open, "task", "tasks"))
	}
	var children int
	if err := db.QueryRow(`SELECT COUNT(*) FROM analysts WHERE parent = ?`, name).
		Scan(&children); err != nil {
		return err
	}
	if children > 0 {
		return fmt.Errorf("%d %s act under this one. Move them first: a delegation "+
			"chain with a missing link proves nothing",
			children, plural(children, "analyst", "analysts"))
	}
	if _, err := db.Exec(`DELETE FROM analysts WHERE name = ?`, name); err != nil {
		return err
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ----------------------------------------------------------- transferring

// Transfer moves an analyst to another desk, another owner, or both.
//
// What moves with it is its OPEN work. Its finished work does not.
//
// That split is the whole design decision here, and it is not a shortcut. A
// closed month has been charged: the chargeback page exists to stop an
// allocation moving after somebody was told what they owed, and re-attributing
// a finished task would move money out of a period that has been frozen and
// invoiced. So the past stays where it was charged, the open work follows the
// agent, and the agent's card shows both figures rather than one that quietly
// spans two owners.
//
// The transfer itself is recorded, so the card can say when the split
// happened and a reader is never left to guess which desk a number belongs to.
func Transfer(db *sql.DB, name, toDesk, toOwner, toParent, by string) (moved int, err error) {
	a, err := GetAnalyst(db, name)
	if err != nil {
		return 0, err
	}
	if toDesk == "" {
		toDesk = a.Desk
	}
	if toOwner == "" {
		toOwner = a.Owner
	}
	if toDesk == a.Desk && toOwner == a.Owner && (toParent == "" || toParent == a.Parent) {
		return 0, errors.New("nothing would change: pick a different desk, owner or parent")
	}
	if toParent == name {
		return 0, errors.New("an analyst cannot act under itself")
	}
	if toParent != "" && toParent != a.Parent {
		p, err := GetAnalyst(db, toParent)
		if err != nil {
			return 0, fmt.Errorf("no analyst called %q to act under", toParent)
		}
		// One hop is enough to catch the loop that actually happens: A under
		// B, then B under A. A deeper cycle needs a walk, and the roster is
		// shallow by construction.
		if p.Parent == name {
			return 0, fmt.Errorf("%s already acts under %s: that would be a loop", toParent, name)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	parent := a.Parent
	switch {
	case toParent != "":
		parent = toParent
	case toDesk != a.Desk && parent == "partner-"+a.Desk:
		// It answered to its old desk's partner, and it is not on that desk
		// any more. Left alone, the delegation chain would say an agent on the
		// gcp desk reports to the aws partner, which is a claim nobody made.
		//
		// Only when the parent was exactly the old desk's partner: a parent
		// somebody chose is a decision, and a transfer does not get to
		// overwrite decisions it was not asked about.
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM analysts WHERE name = ?`,
			"partner-"+toDesk).Scan(&exists); err == nil && exists > 0 {
			parent = "partner-" + toDesk
		} else {
			parent = "supervisor"
		}
	}
	if _, err := tx.Exec(`UPDATE analysts SET desk=?, owner=?, parent=? WHERE name=?`,
		toDesk, toOwner, nullIf(parent), name); err != nil {
		return 0, err
	}
	// Open work follows. Its desk moves with it, because a task's desk is
	// where its cost is charged and the work is now being done somewhere else.
	// The owner moves with the desk, and on OPEN work only. Closed work keeps
	// the owner recorded on it, which is what makes the column a history: the
	// person who answered for a charge in July still answers for it in
	// September, whoever holds the agent now.
	res, err := tx.Exec(`UPDATE tasks SET desk = ?, owner = ?
		WHERE assignee = ? AND state IN ('queued','active','blocked','returned')`,
		toDesk, toOwner, name)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(n), nil
}

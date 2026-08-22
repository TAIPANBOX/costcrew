package crew

import (
	"database/sql"
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
	Hired       string
}

const RosterSchema = `
CREATE TABLE IF NOT EXISTS analysts(
  name TEXT PRIMARY KEY, role TEXT, mission TEXT, desk TEXT, engine TEXT,
  state TEXT NOT NULL, reason TEXT, skills TEXT, rights TEXT,
  per_task_cents INTEGER, monthly_cents INTEGER, cadence TEXT, audience TEXT,
  owner TEXT, parent TEXT, attestation TEXT, hired TEXT);
`

var (
	Cadences     = []string{"daily", "weekly", "fortnightly", "monthly", "on-request"}
	Attestations = []string{"none", "oidc", "spiffe-svid", "enclave-key", "mtls-cert"}
	States       = []string{"active", "suspended", "restricted", "probation", "onboarding"}
	Rights       = []string{
		"figures-read", "sql-readonly", "budgets-read", "requests-read",
		"propose-only", "close-covered", "channel-post", "publish-explainer",
		"export-data", "kpi-registry",
	}
	SkillPool = []string{
		"variance-commentary", "anomaly-triage", "driver-classification",
		"rightsizing-analysis", "commitment-modelling", "forecasting-commentary",
		"forecast-accuracy", "unit-economics", "exec-reporting",
		"showback-narration", "capacity-estimation", "ai-spend-analysis",
		"licence-reconciliation", "allocation-rules", "period-close",
	}
)

// SeedRoster copies the fixture's crew into the store, once.
func SeedRoster(db *sql.DB, owner string) (int, error) {
	if _, err := db.Exec(RosterSchema); err != nil {
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
			 attestation, hired)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			a.Name, a.Role, missionFor(a), a.Desk, a.Engine, string(a.State), nullIf(a.Reason),
			strings.Join(a.Skills, ","), strings.Join(rights, ","),
			int64(money.MustParse(a.PerTaskUSD)),
			int64(money.MustParse(a.MonthlyUSD)),
			cadenceFor(a.Name, a.Role), audienceFor(a.Name, a.Desk),
			owner, nullIf(parentFor(a.Name, a.Desk, hasPartner)),
			attestationFor(a.Name, rights), hiredOn(i)); err != nil {
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
		COALESCE(hired,'') FROM analysts ORDER BY name`)
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
	if !contains(Attestations, a.Attestation) {
		return fmt.Errorf("attestation must be one of %s", strings.Join(Attestations, ", "))
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
	if _, err := db.Exec(RosterSchema); err != nil {
		return err
	}
	_, err := db.Exec(`INSERT INTO analysts
		(name, role, mission, desk, engine, state, reason, skills, rights,
		 per_task_cents, monthly_cents, cadence, audience, owner, parent,
		 attestation, hired)
		VALUES (?,?,?,?,?,?,NULL,?,?,?,?,?,?,?,?,?,?)`,
		a.Name, a.Role, a.Mission, a.Desk, a.Engine, "active",
		strings.Join(a.Skills, ","), strings.Join(a.Rights, ","),
		int64(a.PerTask), int64(a.Monthly), a.Cadence, a.Audience,
		a.Owner, nullIf(a.Parent), a.Attestation,
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

package crew

import (
	"database/sql"
	"sort"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/world"
)

// A seeded analyst arrives with a mandate, not with blanks.
//
// The roster used to seed every agent with no mission, no rights, one cadence
// and one audience, which made thirty-nine different jobs render as
// thirty-nine identical cards. Worse, it was not merely thin: an agent whose
// card shows no rights is claiming it can reach nothing, and that claim was
// false for every one of them.
//
// Everything below is DERIVED from what the fixture already says, so there is
// one place a job is described and the mandate follows from it. Nothing here
// is random: the same crew produces the same roster on every machine.

// rightsForSkill is what a skill needs to be exercised at all.
//
// Read it the other way round to see why it is here: if an analyst holds
// "rightsizing-analysis" and has not been granted the right to read the
// figures, it cannot do the one thing it was hired for, and the console would
// have been lying about it on every page.
var rightsForSkill = map[string][]string{
	"variance-commentary":      {"figures-read", "propose-only"},
	"anomaly-triage":           {"figures-read", "propose-only"},
	"driver-classification":    {"figures-read", "sql-readonly"},
	"rightsizing-analysis":     {"figures-read", "sql-readonly", "propose-only"},
	"commitment-modelling":     {"figures-read", "budgets-read"},
	"forecasting-commentary":   {"figures-read", "budgets-read", "propose-only"},
	"forecast-accuracy":        {"figures-read", "budgets-read"},
	"unit-economics":           {"figures-read", "sql-readonly"},
	"exec-reporting":           {"figures-read", "budgets-read", "export-data"},
	"showback-narration":       {"figures-read", "publish-explainer"},
	"capacity-estimation":      {"figures-read", "sql-readonly"},
	"ai-spend-analysis":        {"figures-read", "sql-readonly"},
	"licence-reconciliation":   {"figures-read", "sql-readonly"},
	"allocation-rules":         {"figures-read", "budgets-read", "export-data"},
	"period-close":             {"budgets-read", "close-covered"},
	"true-up":                  {"budgets-read", "close-covered"},
	"kpi-benchmarking":         {"figures-read", "kpi-registry"},
	"stakeholder-briefing":     {"figures-read", "channel-post", "budgets-read"},
	"decision-framing":         {"figures-read", "export-data", "channel-post", "kpi-registry"},
	"token-economics":          {"figures-read", "sql-readonly"},
	"cost-per-outcome":         {"figures-read", "sql-readonly"},
	"sprint-planning":          {"figures-read", "budgets-read"},
	"routing":                  {"figures-read"},
	"escalation":               {"channel-post"},
	"policy-review":            {"figures-read", "budgets-read"},
	"evidence-assembly":        {"figures-read", "export-data"},
	"data-quality-checks":      {"figures-read", "sql-readonly"},
	"tag-coverage":             {"figures-read", "sql-readonly"},
	"vendor-benchmarking":      {"figures-read"},
	"peer-comparison":          {"figures-read", "kpi-registry"},
	"renewal-calendar":         {"figures-read", "budgets-read"},
	"renewal-negotiation-prep": {"figures-read", "propose-only"},
	"depreciation-modelling":   {"figures-read", "sql-readonly"},
	"model-routing-review":     {"figures-read", "sql-readonly"},
	"maturity-assessment":      {"figures-read", "kpi-registry"},
	"waterline-tracking":       {"figures-read", "budgets-read"},
	"sustainability-reporting": {"figures-read", "export-data"},
	"carbon-accounting":        {"figures-read", "sql-readonly"},

	// These six were on the roster in world.go and absent here, so the three
	// analysts holding them (deep-analysis, migration-watch, intake-triage)
	// were seeded with the figures-read floor and nothing their own mission
	// needed. Rights follow the same vocabulary already in use above.
	"root-cause-analysis":    {"figures-read", "sql-readonly"},
	"migration-tracking":     {"figures-read", "sql-readonly"},
	"step-detection":         {"figures-read", "sql-readonly"},
	"scenario-modelling":     {"figures-read", "budgets-read"},
	"intake-reading":         {"figures-read"},
	"request-classification": {"figures-read"},
}

// RightsFor is the union of what an analyst's skills need, sorted so the same
// crew always produces the same document.
//
// A SUSPENDED agent gets nothing. Leaving its rights in place would put a card
// on the screen that says it may read the figures while the console refuses
// every request it makes, and the card is the thing people trust.
func RightsFor(skills []string, state string) []string {
	if state == string(world.Suspended) {
		return nil
	}
	seen := map[string]bool{"figures-read": true}
	for _, s := range skills {
		for _, r := range rightsForSkill[s] {
			seen[r] = true
		}
	}
	// Restricted means exactly one thing here: it may still look, and it may
	// no longer say anything in anybody's name.
	if state == string(world.Restricted) {
		delete(seen, "channel-post")
		delete(seen, "publish-explainer")
		delete(seen, "export-data")
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// cadenceFor: how often this job reports, which follows from the job.
//
// Read from roles.yaml (ROLES-2026-09.md section 2's "Cadence, audience"
// bullet per role family), by roster name. role is unused: it always has
// been, since the family the cadence follows from is carried in name, not in
// the human-readable role string on the fixture. Kept in the signature so
// SeedRoster and BackfillMandate, which pass it positionally, do not change.
//
// A name that matches no role family (a hire made by hand, before a family
// existed for it) falls back to "weekly", which was the unconditional
// default before this read from data at all.
func cadenceFor(name, role string) string {
	if r, ok := RoleFor(name); ok && r.Cadence != "" {
		return r.Cadence
	}
	return "weekly"
}

// audienceFor: who actually reads it. Read from roles.yaml the same way, with
// "{desk}" substituted for this agent's own desk (ForDesk). Falls back to the
// same "the X desk" default the console has always produced for a name no
// role family matches.
func audienceFor(name, desk string) string {
	if r, ok := RoleForDesk(name, desk); ok && r.Audience != "" {
		return r.Audience
	}
	return "the " + desk + " desk"
}

// parentFor builds a delegation tree with real depth.
//
// Every agent under one supervisor is a list drawn as a graph. A desk analyst
// answers to its desk's FinOps partner, the partner answers to the supervisor,
// and the console can then show a chain that means something.
func parentFor(name, desk string, hasPartner map[string]bool) string {
	if name == "supervisor" {
		return ""
	}
	if strings.HasPrefix(name, "partner-") {
		return "supervisor"
	}
	if hasPartner[desk] {
		return "partner-" + desk
	}
	return "supervisor"
}

// missionFor is the one sentence somebody reads before deciding whether this
// agent should exist. Read from roles.yaml (ROLES-2026-09.md section 2's
// "Mission" bullet per role family), with "{desk}" substituted for this
// agent's own desk (ForDesk) -- the same substitution this function has
// always done itself, moved into one place so the seeded mission column, the
// card and the prompt packet cannot say it three different ways.
//
// A name that matches no role family (a hire made by hand, before a family
// existed for it) falls back to the same generic sentence this function has
// always produced for one: <role> for <desk>.
func missionFor(a world.Agent) string {
	if r, ok := RoleForDesk(a.Name, a.Desk); ok && r.Mission != "" {
		return r.Mission
	}
	return a.Role + " for " + whereFor(a.Desk) + "."
}

// whereFor is the phrase ForDesk substitutes for "{desk}", duplicated here
// (rather than calling ForDesk on an empty JobDescription) only for the
// no-role-matched fallback above, which needs the phrase and nothing else.
func whereFor(desk string) string {
	if desk == "management" {
		return "the whole estate"
	}
	return "the " + desk + " desk"
}

// hiredOn staggers the hire dates deterministically.
//
// Thirty-nine agents hired on the same day is not a crew, it is a seed
// script, and every page that sorts or filters by tenure would sort by
// nothing.
func hiredOn(i int) string {
	// Spread backwards from the estate's last day, roughly one every twelve
	// days, so the newest are weeks old and the oldest a bit over a year.
	return world.DayBefore(world.LastDay, 12*i+3)
}

// BackfillMandate fills in what an older seeding left blank.
//
// It only ever writes into an EMPTY column, so an analyst somebody hired or
// re-briefed through the console keeps every word of what was decided about
// it. An installation that has been running since before this existed should
// gain the mandate without having its roster replaced.
func BackfillMandate(db *sql.DB, owner string) (int, error) {
	// Where each name sits in the fixture, so a hire date can be staggered the
	// same way a fresh seeding would stagger it. A name the fixture does not
	// know was hired through the console and keeps its own date.
	order := map[string]int{}
	for i, a := range world.Crew {
		order[a.Name] = i
	}
	rows, err := db.Query(`SELECT name, COALESCE(role,''), COALESCE(desk,''),
		COALESCE(state,''), COALESCE(skills,''), COALESCE(mission,''),
		COALESCE(rights,''), COALESCE(parent,''), COALESCE(hired,''), COALESCE(owner,''),
		COALESCE(cadence,''), COALESCE(audience,'')
		FROM analysts`)
	if err != nil {
		return 0, err
	}
	type row struct {
		name, role, desk, state, skills, mission, rights, parent, hired, owner string
		cadence, audience                                                      string
	}
	var all []row
	hasPartner := map[string]bool{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.name, &r.role, &r.desk, &r.state, &r.skills,
			&r.mission, &r.rights, &r.parent, &r.hired, &r.owner,
			&r.cadence, &r.audience); err != nil {
			rows.Close()
			return 0, err
		}
		if strings.HasPrefix(r.name, "partner-") {
			hasPartner[r.desk] = true
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	n := 0
	for _, r := range all {
		flatDate := r.hired == "" || r.hired == world.LastDay
		unowned := r.owner == "" || r.owner == "unclaimed"
		if r.mission != "" && r.rights != "" && !flatDate && !unowned {
			continue
		}
		skills := splitList(r.skills)
		a := world.Agent{Name: r.name, Role: r.role, Desk: r.desk, Skills: skills}
		rights := RightsFor(skills, r.state)
		// The parent is only rewritten when it is the flat default, never when
		// somebody chose one.
		parent := r.parent
		if parent == "" || parent == "supervisor" {
			parent = parentFor(r.name, r.desk, hasPartner)
		}
		// A hire date shared by the whole crew is the seed script's date, not a
		// fact about anybody. Only that exact value is replaced.
		hired := r.hired
		if flatDate {
			if i, ok := order[r.name]; ok {
				hired = hiredOn(i)
			}
		}
		owned := r.owner
		if unowned && owner != "" {
			owned = owner
		}
		// Nothing is written when nothing would change.
		//
		// The skip at the top of the loop asks whether mission and rights are
		// non-empty. Two analysts are SUSPENDED, RightsFor returns nothing for
		// a suspended agent on purpose, and so their rights column is
		// legitimately empty and can never be filled. They came back through
		// here on every start, were rewritten with the values they already
		// had, and every start reported "filled in the mandate for 2
		// analysts". SQLite counts the rows an UPDATE MATCHED rather than the
		// rows whose values differ, so the number was true about the statement
		// and false about the estate: it was the second line an operator read
		// on a fresh install, about work that was never missing, forever.
		//
		// So the question is not "is it filled" but "would writing change
		// anything", which is the only form that converges when the correct
		// answer for a column is empty. The CASE guards in the statement below
		// are mirrored here, and the mirroring is the fragile part: a column
		// added there without a line here starts the loop over again.
		wantMission, wantRights := r.mission, r.rights
		if wantMission == "" {
			wantMission = missionFor(a)
		}
		if wantRights == "" {
			wantRights = strings.Join(rights, ",")
		}
		wantCadence, wantAudience := r.cadence, r.audience
		if wantCadence == "" || wantCadence == "weekly" {
			wantCadence = cadenceFor(r.name, r.role)
		}
		if wantAudience == "" || wantAudience == "the desk" {
			wantAudience = audienceFor(r.name, r.desk)
		}
		if wantMission == r.mission && wantRights == r.rights &&
			wantCadence == r.cadence && wantAudience == r.audience &&
			parent == r.parent && hired == r.hired && owned == r.owner {
			continue
		}
		if _, err := db.Exec(`UPDATE analysts SET
			mission   = CASE WHEN COALESCE(mission,'')   = '' THEN ? ELSE mission END,
			rights    = CASE WHEN COALESCE(rights,'')    = '' THEN ? ELSE rights END,
			cadence   = CASE WHEN COALESCE(cadence,'')   IN ('','weekly') THEN ? ELSE cadence END,
			audience  = CASE WHEN COALESCE(audience,'')  IN ('','the desk') THEN ? ELSE audience END,
			parent    = ?,
			hired     = ?,
			owner     = ?
			WHERE name = ?`,
			missionFor(a), strings.Join(rights, ","),
			cadenceFor(r.name, r.role), audienceFor(r.name, r.desk),
			nullIf(parent), hired, owned, r.name); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// DropRetiredRights removes a right this console no longer has anything behind.
//
// `requests-read` named an intake queue of questions people had asked. No such
// table was ever built, and the decision has been made not to build one, so
// nine agents held a permission to read a thing that does not exist. It read
// as a capability on their card, it travelled into the identity graph as a
// tool, and it could never be exercised or refused.
//
// The rights list is a claim about what an agent can reach. A claim about
// something absent is the same fault as an attestation nothing attested, in
// a smaller coat.
func DropRetiredRights(db *sql.DB) (int, error) {
	if err := ensureRoster(db); err != nil {
		return 0, err
	}
	rows, err := db.Query(`SELECT name, COALESCE(rights,'') FROM analysts`)
	if err != nil {
		return 0, err
	}
	type change struct{ name, rights string }
	var changes []change
	for rows.Next() {
		var name, rights string
		if err := rows.Scan(&name, &rights); err != nil {
			rows.Close()
			return 0, err
		}
		kept := make([]string, 0, 8)
		dropped := false
		for _, r := range splitList(rights) {
			if _, retired := retiredRights[r]; retired {
				dropped = true
				continue
			}
			kept = append(kept, r)
		}
		if dropped {
			changes = append(changes, change{name, strings.Join(kept, ",")})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, c := range changes {
		if _, err := db.Exec(`UPDATE analysts SET rights=? WHERE name=?`, c.rights, c.name); err != nil {
			return 0, err
		}
	}
	return len(changes), nil
}

// retiredRights are rights this console once granted and no longer has a
// subject for.
var retiredRights = map[string]struct{}{
	"requests-read": {},
}

// retiredSkillNames maps a skill string this console has renamed to its
// replacement.
//
// "carbon-reporting" and "efficiency-metrics" named the sustainability
// analyst's two skills before rightsForSkill existed for either, so both
// silently granted nothing beyond the figures-read floor. "carbon-accounting"
// and "sustainability-reporting" are the names rightsForSkill actually
// defines rights for; the roster in world.go was moved onto them, and this
// map is what carries an installation seeded under the old names there too.
var retiredSkillNames = map[string]string{
	"carbon-reporting":   "carbon-accounting",
	"efficiency-metrics": "sustainability-reporting",
}

// RenameRetiredSkills rewrites a skill name this console has retired, on
// every analyst that still carries it, and tops its rights up to whatever the
// new name grants that the old one, absent from rightsForSkill, never did.
//
// Like DropRetiredRights, it only ever ADDS rights the renamed skill earns;
// a right somebody granted by hand, or that another skill already earns, is
// never removed. RightsFor still applies the state's own rules (a Restricted
// analyst still loses channel-post, publish-explainer and export-data), so
// the merge cannot hand back something the state forbids.
func RenameRetiredSkills(db *sql.DB) (int, error) {
	if err := ensureRoster(db); err != nil {
		return 0, err
	}
	rows, err := db.Query(`SELECT name, COALESCE(skills,''), COALESCE(rights,''), state FROM analysts`)
	if err != nil {
		return 0, err
	}
	type change struct{ name, skills, rights string }
	var changes []change
	for rows.Next() {
		var name, skills, rights, state string
		if err := rows.Scan(&name, &skills, &rights, &state); err != nil {
			rows.Close()
			return 0, err
		}
		list := splitList(skills)
		renamed := false
		for i, s := range list {
			if next, ok := retiredSkillNames[s]; ok {
				list[i] = next
				renamed = true
			}
		}
		if !renamed {
			continue
		}
		sort.Strings(list)
		have := map[string]bool{}
		for _, r := range splitList(rights) {
			have[r] = true
		}
		for _, r := range RightsFor(list, state) {
			have[r] = true
		}
		merged := make([]string, 0, len(have))
		for r := range have {
			merged = append(merged, r)
		}
		sort.Strings(merged)
		changes = append(changes, change{name, strings.Join(list, ","), strings.Join(merged, ",")})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, c := range changes {
		if _, err := db.Exec(`UPDATE analysts SET skills=?, rights=? WHERE name=?`,
			c.skills, c.rights, c.name); err != nil {
			return 0, err
		}
	}
	return len(changes), nil
}

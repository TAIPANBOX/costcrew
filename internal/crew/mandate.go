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
// and one audience, which made thirty-six different jobs render as thirty-six
// identical cards. Worse, it was not merely thin: an agent whose card shows no
// rights is claiming it can reach nothing, and that claim was false for every
// one of them.
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
	"allocation-rules":         {"figures-read", "budgets-read"},
	"period-close":             {"budgets-read", "close-covered"},
	"true-up":                  {"budgets-read", "close-covered"},
	"kpi-benchmarking":         {"figures-read", "kpi-registry"},
	"stakeholder-briefing":     {"figures-read", "channel-post"},
	"decision-framing":         {"figures-read", "export-data", "channel-post"},
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
	"peer-comparison":          {"figures-read"},
	"renewal-calendar":         {"figures-read", "budgets-read"},
	"renewal-negotiation-prep": {"figures-read", "propose-only"},
	"depreciation-modelling":   {"figures-read", "sql-readonly"},
	"model-routing-review":     {"figures-read", "sql-readonly"},
	"maturity-assessment":      {"figures-read", "kpi-registry"},
	"waterline-tracking":       {"figures-read", "budgets-read"},
	"sustainability-reporting": {"figures-read", "export-data"},
	"carbon-accounting":        {"figures-read", "sql-readonly"},
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
// Triage looks every day because an anomaly a week old has already been paid.
// A forecast reported daily is noise. Nobody chose "weekly for everyone"; it
// was the absence of a choice.
func cadenceFor(name, role string) string {
	switch {
	case strings.HasPrefix(name, "triage-"), strings.HasPrefix(name, "investigator-"):
		return "daily"
	case strings.HasPrefix(name, "partner-"), name == "exec-reporter":
		return "fortnightly"
	case name == "forecaster", name == "governance", name == "kpi-steward",
		name == "sustainability", name == "benchmarking":
		return "monthly"
	case name == "supervisor":
		return "on-request"
	}
	return "weekly"
}

// audienceFor: who actually reads it.
func audienceFor(name, desk string) string {
	switch {
	case name == "supervisor":
		return "whoever asked"
	case name == "exec-reporter", name == "governance":
		return "the executive pack"
	case strings.HasPrefix(name, "partner-"):
		return "the teams on the " + desk + " desk"
	case desk == "management":
		return "the FinOps lead"
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
// agent should exist. Built from the job, so it cannot contradict it.
func missionFor(a world.Agent) string {
	where := "the " + a.Desk + " desk"
	if a.Desk == "management" {
		where = "the whole estate"
	}
	switch {
	case a.Name == "supervisor":
		return "Plan the crew's week and route work to the desk that owns it. " +
			"It plans; it does not execute, and nothing it proposes reaches the estate without a person."
	case strings.HasPrefix(a.Name, "investigator-"):
		return "Explain, within a day, every movement in " + where + "'s bill that the detector raised, " +
			"and say which of them was somebody's decision rather than a fault."
	case strings.HasPrefix(a.Name, "optimizer-"):
		return "Find resources on " + where + " that are paid for at a size nobody uses, " +
			"and propose the smaller size with the saving attached."
	case strings.HasPrefix(a.Name, "reporter-"):
		return "Write what " + where + " cost this period in language the teams paying for it can act on."
	case strings.HasPrefix(a.Name, "capacity-"):
		return "Say what " + where + " will need next quarter, and how far the last such answer was out."
	case strings.HasPrefix(a.Name, "triage-"):
		return "Take every new finding on " + where + " within the day, decide whether it is real, " +
			"and put a named cause on it or say plainly that none is established."
	case strings.HasPrefix(a.Name, "partner-"):
		return "Own the relationship with the teams spending on " + where + ": " +
			"carry their questions in, and carry the answers back in their own terms."
	}
	switch a.Name {
	case "ai-spend":
		return "Watch what the organisation's own agents cost, this crew included, and say when a model choice stopped paying for itself."
	case "unit-econ-ai":
		return "Turn AI spend into a cost per outcome, so a rising bill can be told apart from a rising workload."
	case "saas-manager":
		return "Keep the licence estate honest: what is issued, what is used, and what renews before anybody has decided to renew it."
	case "renewals":
		return "Prepare every renewal before the vendor does, with the usage evidence and a benchmark attached."
	case "chargeback":
		return "Split every shared cost across the teams that caused it, so the total charged equals the invoice, to the cent."
	case "commitments":
		return "Model what to commit to and for how long, and track how much of what was committed is actually being used."
	case "forecaster":
		return "Say what the estate will cost this month, freeze it, and then be scored against what it actually cost."
	case "kpi-steward":
		return "Keep the KPI definitions stable, and refuse to report one whose inputs are not there rather than reporting a zero."
	case "exec-reporter":
		return "Give the executive pack the four numbers that decide something, and the reason each one moved."
	case "governance":
		return "Assemble the evidence that the estate's own rules are being followed, and name the ones that are not."
	case "data-quality":
		return "Check that what the console reports can be traced to a charge, and stop the crew when it cannot."
	case "benchmarking":
		return "Compare this estate against its peers on the few measures where the comparison is fair."
	case "sustainability":
		return "Report the estate's energy and carbon alongside its cost, using the providers' own published factors."
	}
	return a.Role + " for " + where + "."
}

// hiredOn staggers the hire dates deterministically.
//
// Thirty-six agents hired on the same day is not a crew, it is a seed script,
// and every page that sorts or filters by tenure would sort by nothing.
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
		COALESCE(rights,''), COALESCE(parent,''), COALESCE(hired,''), COALESCE(owner,'')
		FROM analysts`)
	if err != nil {
		return 0, err
	}
	type row struct {
		name, role, desk, state, skills, mission, rights, parent, hired, owner string
	}
	var all []row
	hasPartner := map[string]bool{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.name, &r.role, &r.desk, &r.state, &r.skills,
			&r.mission, &r.rights, &r.parent, &r.hired, &r.owner); err != nil {
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

package crew_test

// Tests for B1a: the crew's job descriptions as data, and the mandate
// enforced. See go-to-market-2026-09/B1A-SPEC.md section 4 for the red-first
// requirement these were written against: run before internal/crew/roles.yaml
// and internal/crew/roles.go existed, TestEveryRoleHasAJobDescription and
// TestARoleCannotDecideAClassItDoesNotOwn both fail to COMPILE ("undefined:
// crew.RoleFor", "undefined: crew.MayDecide"), which is the verbatim red
// state B1A-SPEC.md describes as "fails today: no file" / "fails today: no
// MayDecide".

import (
	"errors"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// Every one of the 39 seeded analysts resolves to a role family, and every
// role family in roles.yaml matches at least one of them: a family for a
// role nobody has is dead text. B1A-SPEC.md section 3.2.
func TestEveryRoleHasAJobDescription(t *testing.T) {
	matched := map[string]bool{}
	for _, a := range world.Crew {
		r, ok := crew.RoleForDesk(a.Name, a.Desk)
		if !ok {
			t.Errorf("%s: no job description matches this roster name", a.Name)
			continue
		}
		matched[r.Family] = true
		if r.Mission == "" {
			t.Errorf("%s (%s): no mission", a.Name, r.Family)
		}
		if strings.Contains(r.Mission, "{desk}") || strings.Contains(r.Audience, "{desk}") {
			t.Errorf("%s (%s): a \"{desk}\" placeholder survived RoleForDesk", a.Name, r.Family)
		}
		if r.Cadence == "" || r.Audience == "" {
			t.Errorf("%s (%s): reports %q to %q", a.Name, r.Family, r.Cadence, r.Audience)
		}
	}
	for _, r := range crew.AllRoles() {
		if !matched[r.Family] {
			t.Errorf("role %q matches no roster name: a family for a role nobody has is dead text", r.Family)
		}
	}
	// ROLES-2026-09.md: "25 role families, the supervisor" and "35 decision
	// classes". Both counted here so a role or class silently dropped while
	// editing roles.yaml shows up as a number rather than only as a missing
	// name nobody happened to check for.
	if got := len(crew.AllRoles()); got != 26 {
		t.Errorf("roles.yaml declares %d role families, want 26 (25 analyst families and the supervisor)", got)
	}
	if got := len(crew.AllClasses()); got != 35 {
		t.Errorf("roles.yaml declares %d decision classes, want 35", got)
	}
	if got := len(crew.Never()); got != 5 {
		t.Errorf("roles.yaml's never: list has %d entries, want 5 (\"the five nevers\")", got)
	}
}

// A role decides the classes its own job description lists, and nothing
// else: an investigator decides anomaly.explain but hands period.close up to
// the owner, who owns it. B1A-SPEC.md section 2.3 / section 3.3.
func TestARoleCannotDecideAClassItDoesNotOwn(t *testing.T) {
	if ok, reason := crew.MayDecide("investigator-aws", "anomaly.explain"); !ok {
		t.Errorf("investigator-aws should decide anomaly.explain alone: %s", reason)
	}
	// Nothing to hand up for a class the role already decides alone.
	if to, escalates := crew.Escalates("investigator-aws", "anomaly.explain"); escalates {
		t.Errorf(`Escalates(investigator-aws, anomaly.explain) = %q, true, want "", false: it decides this alone`, to)
	}

	// The coarse links, "analyst" and "supervisor", agree with a class's own
	// Owner field in both directions: true for a class the link owns, false
	// (with a reason) for one it does not. driver.recurring is supervisor's;
	// commentary.variance is analyst's.
	if ok, reason := crew.MayDecide("supervisor", "driver.recurring"); !ok {
		t.Errorf(`MayDecide("supervisor", "driver.recurring") = false (%s), want true`, reason)
	}
	if ok, reason := crew.MayDecide("analyst", "commentary.variance"); !ok {
		t.Errorf(`MayDecide("analyst", "commentary.variance") = false (%s), want true`, reason)
	}
	if ok, reason := crew.MayDecide("analyst", "driver.recurring"); ok || reason == "" {
		t.Errorf(`MayDecide("analyst", "driver.recurring") = %v (%q), want false and a reason`, ok, reason)
	}
	ok, reason := crew.MayDecide("investigator-aws", "period.close")
	if ok {
		t.Fatal("investigator-aws was allowed to decide period.close, which it does not own")
	}
	if reason == "" {
		t.Error("a refusal with no reason cannot be told apart from a bug")
	}
	to, escalates := crew.Escalates("investigator-aws", "period.close")
	if !escalates || to != "owner" {
		t.Errorf(`Escalates(investigator-aws, period.close) = %q, %v, want "owner", true`, to, escalates)
	}

	// A class this practice does not define.
	if ok, reason := crew.MayDecide("investigator-aws", "no-such-class"); ok || reason == "" {
		t.Errorf("a class this practice does not define: ok=%v reason=%q, want false and a reason", ok, reason)
	}
	if to, escalates := crew.Escalates("investigator-aws", "no-such-class"); escalates {
		t.Errorf(`Escalates(investigator-aws, no-such-class) = %q, true, want "", false`, to)
	}

	// purchase, infra.change and vendor.negotiate are nobody's, in the crew:
	// not even the owner decides them here, because they are never a
	// decision the console applies, only an option. ROLES-2026-09.md
	// section 1.
	for _, class := range []string{"purchase", "infra.change", "vendor.negotiate"} {
		if ok, _ := crew.MayDecide("owner", class); ok {
			t.Errorf("%s was allowed as a decision for the owner link; it must only ever be an option", class)
		}
		if _, escalates := crew.Escalates("owner", class); escalates {
			t.Errorf("%s escalated somewhere; it is an option, not an escalation", class)
		}
	}

	// The owner link decides everything else that exists, today (every
	// caller of Post, Return and Approve is a person's act; see crew.go).
	if ok, reason := crew.MayDecide("owner", "period.close"); !ok {
		t.Errorf("the owner should decide everything that exists: %s", reason)
	}
	if ok, reason := crew.MayDecide("owner", "anomaly.explain"); !ok {
		t.Errorf("the owner should decide everything that exists: %s", reason)
	}

	// A role name this practice does not know.
	if ok, _ := crew.MayDecide("no-such-role", "anomaly.explain"); ok {
		t.Error("a role this practice does not know was allowed to decide")
	}

	// "supervisor" the literal link name takes MayDecide's coarse path (the
	// switch matches it before RoleFor ever runs), and that path only agrees
	// with the supervisor's own, family-specific decides_alone list because
	// the data keeps them in step: every class the supervisor's role entry
	// decides alone is a class classes: says the supervisor owns outright.
	// This is that invariant, checked directly on the data rather than by
	// two calls that would always take the same branch.
	sup, ok := crew.RoleFor("supervisor")
	if !ok {
		t.Fatal(`no role family matches "supervisor"`)
	}
	for _, id := range sup.DecidesAlone {
		c, ok := crew.ClassFor(id)
		if !ok {
			t.Errorf("supervisor decides alone %q, which classes: does not define", id)
			continue
		}
		if c.Owner != "supervisor" {
			t.Errorf("supervisor decides alone %q, but it is owned by %q: "+
				"MayDecide(\"supervisor\", %q) would disagree with MayDecide(\"supervisor-family-name\", %q)",
				id, c.Owner, id, id)
		}
	}
}

// The refusal path Post, Return and Approve carry is reachable: a link that
// may not decide the class they stand for is refused before either function
// touches the database, and "owner" -- what every real caller passes today
// -- gets past that check to the ordinary refusal underneath.
// B1A-SPEC.md section 2.3, second bullet.
func TestPostReturnApproveRefuseALinkThatMayNotDecide(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	if _, err := db.Exec(crew.Schema); err != nil {
		t.Fatal(err)
	}

	// task.accept and task.return are the supervisor's; sprint.approve is
	// the owner's. "analyst" and "supervisor" are refused before either
	// function ever queries for the artifact or the plan, which is why a
	// bogus id and an empty plan are enough to prove it.
	if err := crew.Post(db, 999, "somebody", "analyst"); !errors.Is(err, crew.ErrMayNotDecide) {
		t.Errorf("Post(analyst) = %v, want ErrMayNotDecide", err)
	}
	if err := crew.Return(db, 999, "a reason", "analyst"); !errors.Is(err, crew.ErrMayNotDecide) {
		t.Errorf("Return(analyst) = %v, want ErrMayNotDecide", err)
	}
	if _, err := crew.Approve(db, crew.Plan{}, "supervisor"); !errors.Is(err, crew.ErrMayNotDecide) {
		t.Errorf("Approve(supervisor) = %v, want ErrMayNotDecide", err)
	}

	// "owner" -- what internal/web/work.go and internal/web/planning.go both
	// pass -- gets past the link check. Post and Return then refuse for the
	// ordinary reason, no such artifact; Approve refuses for having nothing
	// to plan. Neither error is ErrMayNotDecide.
	if err := crew.Post(db, 999, "somebody", "owner"); !errors.Is(err, crew.ErrNotFound) {
		t.Errorf("Post(owner) on a missing artifact = %v, want ErrNotFound (past the link check)", err)
	}
	if err := crew.Return(db, 999, "a reason", "owner"); !errors.Is(err, crew.ErrNotFound) {
		t.Errorf("Return(owner) on a missing artifact = %v, want ErrNotFound (past the link check)", err)
	}
	if _, err := crew.Approve(db, crew.Plan{}, "owner"); err == nil || errors.Is(err, crew.ErrMayNotDecide) {
		t.Errorf("Approve(owner) with an empty plan = %v, want refused for having nothing to plan (past the link check)", err)
	}
}

// TestRosterForTheRolesGate dumps every roster name, whether it resolves to
// a role, and the rights its skills would earn if it were active, one line
// each starting "ROSTER ", so scripts/roles-are-bound.sh can read them with
// `go test -run TestRosterForTheRolesGate -v` (the same pattern
// TestFeatureBindingsHold's shell counterpart uses in reverse:
// features-are-bound.sh is read FROM a Go test; this Go test is read BY a
// shell one).
//
// "would earn if active", not each analyst's actual current rights: two
// roster members are Suspended or Restricted today, and whether a role's
// mandate fits what its skills back is a property of the JOB, not of one
// member's moment-to-moment state. Migration-watch is Suspended and its
// decides_alone (driver.one-time) needs figures-read; RightsFor(_, "suspended")
// returns nil, which would make an honestly-designed role fail a
// state-blind reading of its own rights.
func TestRosterForTheRolesGate(t *testing.T) {
	if len(world.Crew) != 39 {
		t.Fatalf("world.Crew has %d analysts, want 39: this test's own count would silently stop meaning anything", len(world.Crew))
	}
	for _, a := range world.Crew {
		rights := crew.RightsFor(a.Skills, "active")
		t.Logf("ROSTER %s RIGHTS %s", a.Name, strings.Join(rights, ","))
		if _, ok := crew.RoleFor(a.Name); !ok {
			t.Errorf("%s: no role family matches this roster name", a.Name)
		}
	}
}

// The two lookups the card and the prompt packet both read past MayDecide
// and Escalates: the full "Never, for every role" sentence (six clauses,
// one more than the five-item, gated Never() list; see roles.yaml's own
// comment on never_full_text) and a named threshold's display value.
func TestNeverFullTextAndThresholdFor(t *testing.T) {
	full := crew.NeverFullText()
	if full == "" {
		t.Fatal("NeverFullText is empty")
	}
	for _, verb := range crew.Never() {
		if !strings.Contains(full, verb) {
			t.Errorf("NeverFullText() = %q, missing the never: entry %q", full, verb)
		}
	}
	if !strings.Contains(full, "act on a task somebody blocked") {
		t.Errorf("NeverFullText() = %q, missing the sixth clause the five-item list does not carry", full)
	}

	th, ok := crew.ThresholdFor("T.anomaly")
	if !ok {
		t.Fatal(`ThresholdFor("T.anomaly") found nothing`)
	}
	if th.Value == "" || th.Provenance == "" {
		t.Errorf("T.anomaly: value %q, provenance %q, want both set", th.Value, th.Provenance)
	}
	if !strings.HasPrefix(th.Provenance, "@claude") {
		t.Errorf("T.anomaly's provenance is %q, want it marked @claude: B1A-SPEC.md section 5 says the draft values are mine", th.Provenance)
	}
	if _, ok := crew.ThresholdFor("T.no-such-threshold"); ok {
		t.Error(`ThresholdFor("T.no-such-threshold") found something`)
	}
}

// A name no role family's own name and no matches list carries resolves to
// nothing, in both RoleFor and RoleForDesk: a hire made by hand, before a
// family existed for it, and the fallback missionFor, cadenceFor and
// audienceFor all still produce for it (see mandate.go).
func TestRoleForOnANameNothingMatches(t *testing.T) {
	if _, ok := crew.RoleFor("a-name-from-nowhere"); ok {
		t.Error(`RoleFor("a-name-from-nowhere") found a role family`)
	}
	if _, ok := crew.RoleForDesk("a-name-from-nowhere", "aws"); ok {
		t.Error(`RoleForDesk("a-name-from-nowhere", "aws") found a role family`)
	}
}

// C6-SPEC.md, the Gherkin scenario "the negotiation is a person's"
// (features/renewals.feature). `@yurii 2026-09-02`: "переговори з вендером
// проводити він сам особі не може". vendor.negotiate is owned by "nobody"
// in roles.yaml, the same as purchase and infra.change, so MayDecide already
// refuses it for every link this practice has, including the owner's own --
// this test names that refusal for the two SaaS roles specifically, rather
// than trusting the generic property to have been checked on their behalf.
func TestRenewalNegotiationIsNeverDecidedInsideTheConsole(t *testing.T) {
	for _, role := range []string{"saas-portfolio-manager", "renewals-analyst", "supervisor", "owner"} {
		if may, reason := crew.MayDecide(role, "vendor.negotiate"); may {
			t.Errorf("MayDecide(%q, \"vendor.negotiate\") = true, want false (%s)", role, reason)
		}
	}
	// Escalates is false too: vendor.negotiate is not something a role hands
	// UP to a decider either, because "nobody" is not a decider -- it is
	// recorded as an option and stops there (roles.go's own MayDecide
	// comment: "it is only ever recorded as an option").
	if to, ok := crew.Escalates("renewals-analyst", "vendor.negotiate"); ok {
		t.Errorf(`Escalates("renewals-analyst", "vendor.negotiate") = (%q, true), want ok=false`, to)
	}
}

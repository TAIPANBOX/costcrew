package finops_test

// costcrew#74: the box's AI spend arrives labelled by the gateway's x_unit
// (the FOCUS reader writes it as the charge's Team), and nothing allocated by
// it. A unit-keyed allocation.rule is how a person says "this unit is a team
// we charge back, under this business unit"; an analyst proposes it, only a
// stamp applies it, and the showback then carries one row per unit.
//
// Every figure below is worked by hand from unitFocus, not read back from the
// code under test:
//
//	aws 2026-09-02   1.234500 + 0.005000 = 1.239500  -> 123.95 -> 124 cents
//	gcp 2026-09-02   0.755000 + 0.755000 = 1.510000  -> 151 cents
//	gcp 2026-09-03   0.333333                        ->  33 cents
//
// so aws is 124, gcp is 184 and the whole import is 308.

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

const (
	unitFocusHeader = "BilledCost,EffectiveCost,BillingCurrency,ChargePeriodStart,ChargePeriodEnd," +
		"ChargeDescription,ProviderName,PublisherName,InvoiceIssuerName,ServiceName,ServiceCategory," +
		"ResourceId,ResourceName,SubAccountId,SubAccountName,x_run_id,x_parent_run_id,x_agent_id," +
		"x_model,x_tokens_in,x_tokens_out,x_blocked,x_cost_basis,x_outcome,x_unit,x_tool_calls"
	unitMonth = "2026-09"

	awsCents   = 124
	gcpCents   = 184
	totalCents = awsCents + gcpCents // 308
)

func unitFocusRow(cost, day, unit string) string {
	return fmt.Sprintf("%s,%s,USD,%sT10:00:00Z,%sT10:00:00Z,desc,Anthropic,Anthropic,Anthropic,"+
		"LLM inference,AI,agent://a/b/c,agent://a/b/c,run-1,run-1,run-1,,agent://a/b/c,"+
		"claude-haiku-4-5,100,50,false,settled,,%s,0", cost, cost, day, day, unit)
}

// importTwoUnits reads a FOCUS file with two customer units into db.
func importTwoUnits(t *testing.T, db *sql.DB, replaceGenerated bool) {
	t.Helper()
	rows := []string{
		unitFocusRow("1.234500", "2026-09-02", "aws"),
		unitFocusRow("0.005000", "2026-09-02", "aws"),
		unitFocusRow("0.755000", "2026-09-02", "gcp"),
		unitFocusRow("0.755000", "2026-09-02", "gcp"),
		unitFocusRow("0.333333", "2026-09-03", "gcp"),
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "units.csv"),
		[]byte(unitFocusHeader+"\n"+strings.Join(rows, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := connectors.Save(db, "tokenfuse-focus", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := connectors.Import(db, "tokenfuse-focus", false,
		connectors.ImportOptions{ReplaceGenerated: replaceGenerated}); err != nil {
		t.Fatal(err)
	}
}

// unitStoreDB is a console that holds nothing but the two-unit import, with
// every plane a stamp and a close reach.
func unitStoreDB(t *testing.T) (*sql.DB, int) {
	t.Helper()
	db := bareDB(t)
	importTwoUnits(t, db, false)
	if err := finops.SeedRules(db); err != nil {
		t.Fatal(err)
	}
	for _, sch := range []string{crew.Schema, anomaly.Schema, crew.RosterSchema} {
		if _, err := db.Exec(sch); err != nil {
			t.Fatal(err)
		}
	}
	if err := crew.EnsureArtifactProvenance(db); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO sprints (label, start, finish, state, goal)
		VALUES ('2026-W99', '2026-09-01', '2026-09-07', 'active', 'a goal')`)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return db, int(sid)
}

func unitTarget(unit, businessUnit string) map[string]any {
	return map[string]any{"unit": unit, "business_unit": businessUnit}
}

func mustUnitRules(t *testing.T, db *sql.DB) []finops.UnitRule {
	t.Helper()
	rules, err := finops.UnitRules(db)
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func sumLoaded(rows []finops.ShowbackRow) money.Cents {
	var s money.Cents
	for _, r := range rows {
		s += r.Loaded()
	}
	return s
}

func showbackRow(rows []finops.ShowbackRow, team string) (finops.ShowbackRow, bool) {
	for _, r := range rows {
		if r.Team == team {
			return r, true
		}
	}
	return finops.ShowbackRow{}, false
}

// The charges the reader wrote are the ground truth the rest of this file
// balances against, computed here by SQL and by hand, not by the allocation.
func TestTheImportIsWhatTheFixtureSaysItIs(t *testing.T) {
	db, _ := unitStoreDB(t)
	var aws, gcp, all int64
	db.QueryRow(`SELECT COALESCE(SUM(billed_cents),0) FROM charges WHERE team='aws'`).Scan(&aws)
	db.QueryRow(`SELECT COALESCE(SUM(billed_cents),0) FROM charges WHERE team='gcp'`).Scan(&gcp)
	db.QueryRow(`SELECT COALESCE(SUM(billed_cents),0) FROM charges
		WHERE provenance='tokenfuse-focus'`).Scan(&all)
	if aws != awsCents || gcp != gcpCents || all != totalCents {
		t.Fatalf("the fixture imports as aws %d, gcp %d, total %d; worked by hand it is %d, %d, %d",
			aws, gcp, all, awsCents, gcpCents, totalCents)
	}
}

// ---------------------------------------------------------------- the stamp

func TestAUnitRuleIsAppliedByAStampAndRecordsWhoStampedIt(t *testing.T) {
	db, _ := unitStoreDB(t)
	opt := plantAllocationRuleOption(t, db, "ai", "allocation.rule",
		"charge unit aws back as Customer One", unitTarget("aws", "Customer One"))

	if got := mustUnitRules(t, db); len(got) != 0 {
		t.Fatalf("a rule exists before any stamp: %+v", got)
	}
	if err := finops.Apply(db, opt, "owner1", nil); err != nil {
		t.Fatal(err)
	}
	got := mustUnitRules(t, db)
	if len(got) != 1 {
		t.Fatalf("after the stamp there are %d unit rules, want 1: %+v", len(got), got)
	}
	r := got[0]
	if r.Unit != "aws" || r.BusinessUnit != "Customer One" {
		t.Errorf("the rule is %q under %q, want aws under Customer One", r.Unit, r.BusinessUnit)
	}
	if r.DecidedBy != "owner1" {
		t.Errorf("the rule says %q stamped it, want owner1", r.DecidedBy)
	}
	if r.Artifact != opt.Artifact || r.Ordinal != opt.Ordinal {
		t.Errorf("the rule points at option %d/%d, want %d/%d (the option that carried it)",
			r.Artifact, r.Ordinal, opt.Artifact, opt.Ordinal)
	}
	if o := mustGetOption(t, db, opt.Artifact, opt.Ordinal); o.State != crew.OptionApplied {
		t.Errorf("option state %q, want applied", o.State)
	}
}

func TestAStampOnADifferentBusinessUnitReplacesTheRuleRatherThanAddingASecond(t *testing.T) {
	db, _ := unitStoreDB(t)
	first := plantAllocationRuleOption(t, db, "ai", "allocation.rule", "aws under One",
		unitTarget("aws", "Customer One"))
	second := plantAllocationRuleOption(t, db, "ai", "allocation.rule", "aws under Two",
		unitTarget("aws", "Customer Two"))
	if err := finops.Apply(db, first, "owner1", nil); err != nil {
		t.Fatal(err)
	}
	if err := finops.Apply(db, second, "owner2", nil); err != nil {
		t.Fatal(err)
	}
	got := mustUnitRules(t, db)
	if len(got) != 1 || got[0].BusinessUnit != "Customer Two" || got[0].DecidedBy != "owner2" {
		t.Fatalf("after a second stamp the rules are %+v, want exactly aws under Customer Two by owner2", got)
	}
}

func TestAUnitRuleForAUnitWithNoRowsIsRefusedAndTheOptionStaysOpen(t *testing.T) {
	db, _ := unitStoreDB(t)
	opt := plantAllocationRuleOption(t, db, "ai", "allocation.rule", "a unit nobody sent",
		unitTarget("ghost", "Nobody"))

	err := finops.Apply(db, opt, "owner1", nil)
	if err == nil {
		t.Fatal("a rule for a unit that has no rows was applied")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("the refusal does not name the unit: %v", err)
	}
	if got := mustUnitRules(t, db); len(got) != 0 {
		t.Errorf("a refused rule was written anyway: %+v", got)
	}
	if o := mustGetOption(t, db, opt.Artifact, opt.Ordinal); o.State == crew.OptionApplied {
		t.Error("the option was marked applied although its rule was refused")
	}
}

// Rows a reader did not write are not a unit: a generated charge with a team
// on it is fixture data, and a rule on it would charge a customer for a
// planted number.
func TestAUnitRuleOnRowsTheTokenFuseReaderDidNotWriteIsRefused(t *testing.T) {
	db, _ := unitStoreDB(t)
	mustExecArgs(t, db, `INSERT INTO charges(source, day, service, team, category, billed_cents,
		quantity, unit, meter, model, provenance)
		VALUES ('ai','2026-09-04','LLM inference','acme','Usage',500,1,'tokens','m','mdl',NULL)`)
	opt := plantAllocationRuleOption(t, db, "ai", "allocation.rule", "a generated unit",
		unitTarget("acme", "Acme"))
	if err := finops.Apply(db, opt, "owner1", nil); err == nil {
		t.Fatal("a rule was applied to a team whose rows no reader wrote")
	}
	if got := mustUnitRules(t, db); len(got) != 0 {
		t.Errorf("a refused rule was written anyway: %+v", got)
	}
}

// A unit that is also a roster team would merge into that team's showback row.
func TestAUnitRuleForARosterTeamIsRefused(t *testing.T) {
	db, _ := unitStoreDB(t)
	mustExecArgs(t, db, `INSERT INTO charges(source, day, service, team, category, billed_cents,
		quantity, unit, meter, model, provenance)
		VALUES ('ai','2026-09-04','LLM inference',?, 'Usage',500,1,'tokens','m','mdl','tokenfuse-focus')`,
		world.Teams[0].Name)
	opt := plantAllocationRuleOption(t, db, "ai", "allocation.rule", "a unit named like a team",
		unitTarget(world.Teams[0].Name, "Engineering again"))
	if err := finops.Apply(db, opt, "owner1", nil); err == nil {
		t.Fatalf("a unit rule was applied to the roster team %q", world.Teams[0].Name)
	}
	if got := mustUnitRules(t, db); len(got) != 0 {
		t.Errorf("a refused rule was written anyway: %+v", got)
	}
}

// A target with a unit and a rule id is two different rules at once; it must
// not be read as whichever shape the decoder met first.
func TestATargetNamingBothAUnitAndARuleIsRefusedAtApplyToo(t *testing.T) {
	db, _ := unitStoreDB(t)
	opt := plantAllocationRuleOption(t, db, "ai", "allocation.rule", "both shapes",
		map[string]any{"unit": "aws", "business_unit": "One", "rule_id": 1, "method": "even-split"})
	if err := finops.Apply(db, opt, "owner1", nil); err == nil {
		t.Fatal("a target naming both a unit and a rule id was applied")
	}
	if got := mustUnitRules(t, db); len(got) != 0 {
		t.Errorf("a rule was written from an ambiguous target: %+v", got)
	}
}

// -------------------------------------------------------- impossible without

// The supervisor's pass carries an allocation.rule to its owner at any
// figure; it never applies one, unit-keyed or not.
func TestTheSupervisorNeverAppliesAUnitRule(t *testing.T) {
	db, sprint := unitStoreDB(t)
	opt := plantPostedOptionWithTarget(t, db, sprint, "ai", "owner1", "allocation.rule",
		"charge unit aws back as Customer One", 0, "low",
		`{"unit":"aws","business_unit":"Customer One"}`)

	pass, err := finops.Supervise(db, sprint, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Applied) != 0 {
		t.Fatalf("the supervisor applied %v on its own", pass.Applied)
	}
	if len(pass.Carried) != 1 || pass.Carried[0].Class != "allocation.rule" {
		t.Fatalf("carried %v, want the allocation.rule handed to its owner", pass.Carried)
	}
	if got := mustUnitRules(t, db); len(got) != 0 {
		t.Fatalf("a unit rule exists after the supervisor's pass and no stamp: %+v", got)
	}
	if o := mustGetOption(t, db, opt.Artifact, opt.Ordinal); o.State != crew.OptionCarried {
		t.Errorf("option state %q, want carried to the owner", o.State)
	}
}

// Saving a deliverable stores the proposal; it is not the stamp.
func TestSavingAUnitRuleProposalWritesNoRule(t *testing.T) {
	db, _ := unitStoreDB(t)
	body := "## Unit aws has no rule\n\n```options\n" +
		`{"options": [{"class": "allocation.rule", "summary": "charge unit aws back as Customer One", ` +
		`"target": {"unit": "aws", "business_unit": "Customer One"}, ` +
		`"figure_cents": 124, "saving_cents": 0, "risk": "low", "needs": "the owner's stamp"}]}` +
		"\n```\n"
	res, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated)
		VALUES ('Close the books, 2026-09', 'g', 'chargeback', 'ai', 'active', 0, 0,
		        datetime('now'), datetime('now'))`)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	ares, err := db.Exec(`INSERT INTO artifacts (task, author, title, body, state, created)
		VALUES (?, 'chargeback', 'the close pack', ?, 'draft', datetime('now'))`, taskID, body)
	if err != nil {
		t.Fatal(err)
	}
	artID, _ := ares.LastInsertId()

	refused, reason, err := crew.ValidateAndSaveOptions(db, int(artID), "chargeback", body, nil)
	if err != nil || refused {
		t.Fatalf("the proposal was not saved: refused=%v reason=%q err=%v", refused, reason, err)
	}
	if got := mustUnitRules(t, db); len(got) != 0 {
		t.Fatalf("saving a proposal wrote a rule: %+v", got)
	}
	opts, err := crew.Options(db, int(artID))
	if err != nil || len(opts) != 1 || opts[0].State != crew.OptionOpen {
		t.Fatalf("the saved proposal is %+v (err %v), want one open option", opts, err)
	}
}

// A proposal for a unit nobody has charged anything is refused when the
// analyst writes it, so it never reaches the owner as something to stamp.
func TestAProposalForAUnitWithNoRowsIsRefusedWhenItIsWritten(t *testing.T) {
	db, _ := unitStoreDB(t)
	body := "## A unit\n\n```options\n" +
		`{"options": [{"class": "allocation.rule", "summary": "charge a ghost back", ` +
		`"target": {"unit": "ghost", "business_unit": "Nobody"}, ` +
		`"figure_cents": 0, "saving_cents": 0, "risk": "low", "needs": "the owner's stamp"}]}` +
		"\n```\n"
	res, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated)
		VALUES ('Close the books, 2026-09', 'g', 'chargeback', 'ai', 'active', 0, 0,
		        datetime('now'), datetime('now'))`)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	ares, err := db.Exec(`INSERT INTO artifacts (task, author, title, body, state, created)
		VALUES (?, 'chargeback', 'the close pack', ?, 'draft', datetime('now'))`, taskID, body)
	if err != nil {
		t.Fatal(err)
	}
	artID, _ := ares.LastInsertId()
	refused, reason, err := crew.ValidateAndSaveOptions(db, int(artID), "chargeback", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !refused || !strings.Contains(reason, "ghost") {
		t.Fatalf("a proposal for a unit with no rows was accepted (refused=%v, reason %q)", refused, reason)
	}
	if opts, _ := crew.Options(db, int(artID)); len(opts) != 0 {
		t.Errorf("%d options stored despite the refusal", len(opts))
	}
}

// ---------------------------------------------------------------- showback

func TestBeforeAnyRuleTheUnitsAreOneVisibleUnruledRowAndTheFileBalances(t *testing.T) {
	db, _ := unitStoreDB(t)
	rows, err := finops.Showback(db, unitMonth)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Team != finops.UnruledTeam {
		t.Fatalf("with no rule the showback is %+v, want one %q row", rows, finops.UnruledTeam)
	}
	if rows[0].Loaded() != totalCents {
		t.Errorf("the unruled row carries %s, want 3.08 (both units)", rows[0].Loaded())
	}
	if got := sumLoaded(rows); got != totalCents {
		t.Errorf("the showback sums to %s against an import of 3.08: money was dropped", got)
	}
}

func TestAfterTheStampsTheShowbackHasOneRowPerUnitAndBalancesToTheCent(t *testing.T) {
	db, _ := unitStoreDB(t)
	for _, u := range []struct{ unit, bu string }{{"aws", "Customer One"}, {"gcp", "Customer Two"}} {
		opt := plantAllocationRuleOption(t, db, "ai", "allocation.rule", u.unit+" as "+u.bu,
			unitTarget(u.unit, u.bu))
		if err := finops.Apply(db, opt, "owner1", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := finops.Close(db, unitMonth, "owner1"); err != nil {
		t.Fatal(err)
	}

	rows, err := finops.Showback(db, unitMonth)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("showback has %d rows, want exactly the two units: %+v", len(rows), rows)
	}
	aws, ok := showbackRow(rows, "aws")
	if !ok || aws.BusinessUnit != "Customer One" || aws.Loaded() != awsCents {
		t.Errorf("aws row is %+v (found %v), want Customer One at 1.24", aws, ok)
	}
	gcp, ok := showbackRow(rows, "gcp")
	if !ok || gcp.BusinessUnit != "Customer Two" || gcp.Loaded() != gcpCents {
		t.Errorf("gcp row is %+v (found %v), want Customer Two at 1.84", gcp, ok)
	}
	if _, stray := showbackRow(rows, finops.UnruledTeam); stray {
		t.Error("an unruled row remains although both units have a rule")
	}

	// The balance, against the import and against the frozen period.
	if got := sumLoaded(rows); got != totalCents {
		t.Errorf("the showback sums to %s, the import is 3.08", got)
	}
	frozen, err := finops.FrozenPeriod(db, unitMonth)
	if err != nil {
		t.Fatal(err)
	}
	if !frozen.Closed || frozen.Total != totalCents {
		t.Errorf("the closed period carries %s (closed=%v), want 3.08", frozen.Total, frozen.Closed)
	}
	frozenTeams := map[string]money.Cents{}
	for _, f := range frozen.Teams {
		frozenTeams[f.Team] += f.Loaded()
	}
	if frozenTeams["aws"] != awsCents || frozenTeams["gcp"] != gcpCents {
		t.Errorf("the frozen rows are %v, want aws 1.24 and gcp 1.84", frozenTeams)
	}
}

func TestOnlyTheRuledUnitGetsItsOwnRowAndTheRestStaysVisibleInOne(t *testing.T) {
	db, _ := unitStoreDB(t)
	opt := plantAllocationRuleOption(t, db, "ai", "allocation.rule", "aws as One", unitTarget("aws", "Customer One"))
	if err := finops.Apply(db, opt, "owner1", nil); err != nil {
		t.Fatal(err)
	}
	rows, err := finops.Showback(db, unitMonth)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want aws and the unruled remainder: %+v", len(rows), rows)
	}
	if aws, ok := showbackRow(rows, "aws"); !ok || aws.Loaded() != awsCents {
		t.Errorf("aws row %+v (found %v), want 1.24", aws, ok)
	}
	if rest, ok := showbackRow(rows, finops.UnruledTeam); !ok || rest.Loaded() != gcpCents {
		t.Errorf("unruled row %+v (found %v), want gcp's 1.84", rest, ok)
	}
	if got := sumLoaded(rows); got != totalCents {
		t.Errorf("the showback sums to %s, the import is 3.08", got)
	}
}

func TestUnitsListsEveryUnitWithItsRuleOrTheLackOfOne(t *testing.T) {
	db, _ := unitStoreDB(t)
	opt := plantAllocationRuleOption(t, db, "ai", "allocation.rule", "gcp as Two", unitTarget("gcp", "Customer Two"))
	if err := finops.Apply(db, opt, "owner1", nil); err != nil {
		t.Fatal(err)
	}
	units, err := finops.Units(db, unitMonth)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 2 || units[0].Unit != "aws" || units[1].Unit != "gcp" {
		t.Fatalf("units are %+v, want aws then gcp (sorted by name)", units)
	}
	if units[0].Ruled || units[0].Loaded() != awsCents {
		t.Errorf("aws is %+v, want unruled at 1.24", units[0])
	}
	if !units[1].Ruled || units[1].BusinessUnit != "Customer Two" || units[1].Loaded() != gcpCents {
		t.Errorf("gcp is %+v, want ruled under Customer Two at 1.84", units[1])
	}
}

// Beside the fixture's teams: the generated estate is untouched by the
// units, which are added after the roster rows in a fixed order. The reader
// refuses to mix, so the two real unit rows are planted straight into the
// ledger here, on purpose.
func TestUnitRowsSitBesideTheFixturesTeamsWithoutMovingThem(t *testing.T) {
	db := seeded(t)
	month := aMonth(t, db)
	before, err := finops.Showback(db, month)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 {
		t.Fatal("the fixture has no showback rows; this test cannot see the property")
	}

	for _, u := range []struct {
		unit  string
		cents int
	}{{"aws", awsCents}, {"gcp", gcpCents}} {
		mustExecArgs(t, db, `INSERT INTO charges(source, day, service, team, category, billed_cents,
			quantity, unit, meter, model, provenance)
			VALUES ('ai', ?, 'LLM inference', ?, 'Usage', ?, 1, 'tokens', 'm', 'mdl', 'tokenfuse-focus')`,
			month+"-15", u.unit, u.cents)
	}
	for _, sch := range []string{crew.Schema, anomaly.Schema, crew.RosterSchema} {
		if _, err := db.Exec(sch); err != nil {
			t.Fatal(err)
		}
	}
	if err := crew.EnsureArtifactProvenance(db); err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct{ unit, bu string }{{"aws", "Customer One"}, {"gcp", "Customer Two"}} {
		opt := plantAllocationRuleOption(t, db, "ai", "allocation.rule", u.unit, unitTarget(u.unit, u.bu))
		if err := finops.Apply(db, opt, "owner1", nil); err != nil {
			t.Fatal(err)
		}
	}

	after, err := finops.Showback(db, month)
	if err != nil {
		t.Fatal(err)
	}
	roster := map[string]bool{}
	for _, tm := range world.Teams {
		roster[tm.Name] = true
	}
	var rosterAfter []finops.ShowbackRow
	for _, r := range after {
		if roster[r.Team] {
			rosterAfter = append(rosterAfter, r)
		}
	}
	// The units take a share of the desk's shared pots, so a roster team's
	// figure on the ai desk may move; what must not move is the SET of
	// roster rows and the order they come in.
	if len(rosterAfter) != len(before) {
		t.Fatalf("the roster rows went from %d to %d when two units were added", len(before), len(rosterAfter))
	}
	for i := range before {
		if before[i].Team != rosterAfter[i].Team || before[i].BusinessUnit != rosterAfter[i].BusinessUnit {
			t.Errorf("roster row %d moved: %+v became %+v", i, before[i], rosterAfter[i])
		}
	}
	if len(after) != len(before)+2 {
		t.Fatalf("got %d rows, want the fixture's %d plus the two units: %+v", len(after), len(before), after)
	}
	if after[len(after)-2].Team != "aws" || after[len(after)-1].Team != "gcp" {
		t.Errorf("the units are not last and in name order: %+v", after[len(after)-2:])
	}
	if _, stray := showbackRow(after, finops.UnruledTeam); stray {
		t.Error("an unruled row is present although both units have a rule")
	}

	a, err := finops.Allocate(db, month)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sumLoaded(after)+a.Unallocated, a.Direct+a.Shared; got != want {
		t.Errorf("the showback plus the unallocated remainder is %s, the bill is %s", got, want)
	}
}

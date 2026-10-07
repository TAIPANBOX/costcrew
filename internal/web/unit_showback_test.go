package web_test

// costcrew#74, through the routes it was found on: the box's AI spend arrives
// labelled by the gateway's unit, and /export/showback.csv listed only the
// ten teams of the generated estate, so a customer's units were dropped from
// the file a FinOps team sends. A unit-keyed allocation.rule, stamped by the
// owner, is what puts one row per unit there.
//
// The import below is worked by hand: aws 1.24, gcp 1.84, the whole of it
// 3.08 (the same figures internal/finops/unitrules_test.go balances against).

import (
	"encoding/csv"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/connectors"
)

const webUnitHeader = "BilledCost,EffectiveCost,BillingCurrency,ChargePeriodStart,ChargePeriodEnd," +
	"ChargeDescription,ProviderName,PublisherName,InvoiceIssuerName,ServiceName,ServiceCategory," +
	"ResourceId,ResourceName,SubAccountId,SubAccountName,x_run_id,x_parent_run_id,x_agent_id," +
	"x_model,x_tokens_in,x_tokens_out,x_blocked,x_cost_basis,x_outcome,x_unit,x_tool_calls"

func webUnitRow(cost, day, unit string) string {
	return fmt.Sprintf("%s,%s,USD,%sT10:00:00Z,%sT10:00:00Z,desc,Anthropic,Anthropic,Anthropic,"+
		"LLM inference,AI,agent://a/b/c,agent://a/b/c,run-1,run-1,run-1,,agent://a/b/c,"+
		"claude-haiku-4-5,100,50,false,settled,,%s,0", cost, cost, day, day, csvCell(unit))
}

func csvCell(s string) string {
	if strings.ContainsAny(s, ",\"\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// startWithUnits is the console after the operator replaced the generated
// estate with a two-unit FOCUS export, the way the appliance's was. extra is
// further rows (a hostile unit, say) appended to the same file.
func startWithUnits(t *testing.T, extra ...string) *harness {
	t.Helper()
	h := start(t)
	rows := []string{
		webUnitRow("1.234500", "2026-09-02", "aws"),
		webUnitRow("0.005000", "2026-09-02", "aws"),
		webUnitRow("0.755000", "2026-09-02", "gcp"),
		webUnitRow("0.755000", "2026-09-02", "gcp"),
		webUnitRow("0.333333", "2026-09-03", "gcp"),
	}
	rows = append(rows, extra...)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "units.csv"),
		[]byte(webUnitHeader+"\n"+strings.Join(rows, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := connectors.Save(h.st.DB(), "tokenfuse-focus", map[string]string{"path": dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := connectors.Import(h.st.DB(), "tokenfuse-focus", false,
		connectors.ImportOptions{ReplaceGenerated: true}); err != nil {
		t.Fatal(err)
	}
	return h
}

// plantCarriedUnitRule is a unit-keyed allocation.rule option carried to
// owner, in a deliverable of its own: two options in one deliverable are
// alternatives, and applying one would mark the other not_chosen.
func plantCarriedUnitRule(t *testing.T, h *harness, sprintID int, owner, unit, businessUnit string) (artifact, ordinal int) {
	t.Helper()
	db := h.st.DB()
	tres, err := db.Exec(`INSERT INTO tasks
		(sprint, title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated, owner)
		VALUES (?, 'Close the books, 2026-09', 'a goal', 'chargeback', 'ai', 'active', 0, 0,
		        datetime('now'), datetime('now'), ?)`, sprintID, owner)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := tres.LastInsertId()
	ares, err := db.Exec(`INSERT INTO artifacts (task, author, title, body, state, created)
		VALUES (?, 'chargeback', 'the close pack', 'body', 'posted', datetime('now'))`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	artID, _ := ares.LastInsertId()
	target := fmt.Sprintf(`{"unit":%q,"business_unit":%q}`, unit, businessUnit)
	if _, err := db.Exec(`INSERT INTO artifact_options
		(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, target, state)
		VALUES (?, 1, 'allocation.rule', ?, 124, 0, 'low', 'the owner', '[]', ?, 'carried')`,
		artID, "charge unit "+unit+" back as "+businessUnit, target); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO decision_requests (artifact, sprint, owner, lapses, created)
		VALUES (?,?,?,?,datetime('now'))`, artID, sprintID, owner, "2026-09-14"); err != nil {
		t.Fatal(err)
	}
	return int(artID), 1
}

func newUnitSprint(t *testing.T, h *harness) int {
	t.Helper()
	res, err := h.st.DB().Exec(`INSERT INTO sprints (label, start, finish, state, goal)
		VALUES ('2026-W98', '2026-09-01', '2026-09-07', 'active', 'a goal')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return int(id)
}

func unitRuleCount(t *testing.T, h *harness) int {
	t.Helper()
	var n int
	if err := h.st.DB().QueryRow(`SELECT COUNT(*) FROM unit_rules`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func showbackRows(t *testing.T, h *harness, period string) [][]string {
	t.Helper()
	code, body, _ := h.get(t, "/export/showback.csv?period="+period)
	if code != 200 {
		t.Fatalf("showback.csv answered %d", code)
	}
	recs, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("showback.csv is not CSV: %v\n%s", err, body)
	}
	if len(recs) == 0 || strings.Join(recs[0], ",") !=
		"period,team,business_unit,direct_usd,allocated_usd,fully_loaded_usd" {
		t.Fatalf("showback.csv header changed: %v", recs)
	}
	return recs[1:]
}

func stampPath(art, ord int) string {
	return "/option/" + strconv.Itoa(art) + "/" + strconv.Itoa(ord) + "/apply"
}

func TestTheShowbackCarriesOneRowPerUnitOnlyAfterTheOwnersStamp(t *testing.T) {
	h := startWithUnits(t)
	h.signUp(t, "boss", "boss-password-2026") // first account: admin
	if _, err := h.au.Create("owner1", "owner1-password-2026", "operator"); err != nil {
		t.Fatal(err)
	}
	owner := h.as(t, "owner1", "owner1-password-2026")
	sprint := newUnitSprint(t, h)
	awsArt, awsOrd := plantCarriedUnitRule(t, h, sprint, "owner1", "aws", "Customer One")
	gcpArt, gcpOrd := plantCarriedUnitRule(t, h, sprint, "owner1", "gcp", "Customer Two")

	// Before the stamp the money is in the file, as one visible line.
	before := showbackRows(t, h, "2026-09")
	if len(before) != 1 || before[0][1] != "(unruled units)" || before[0][5] != "3.08" {
		t.Fatalf("before any stamp the showback is %v, want one (unruled units) row of 3.08", before)
	}

	// The stamp, by the owner, through the route.
	for _, o := range [][2]int{{awsArt, awsOrd}, {gcpArt, gcpOrd}} {
		code, loc := owner.post(t, stampPath(o[0], o[1]), url.Values{"csrf": {owner.csrf(t, "/board")}})
		if code != 303 || strings.Contains(loc, "msg=") {
			t.Fatalf("the owner's stamp: %d %s", code, loc)
		}
	}
	if n := unitRuleCount(t, h); n != 2 {
		t.Fatalf("%d unit rules after two stamps, want 2", n)
	}

	// Close the period, then read the file.
	if code, loc := owner.post(t, "/chargeback/close",
		url.Values{"csrf": {owner.csrf(t, "/chargeback?period=2026-09")}, "period": {"2026-09"}}); code != 303 ||
		strings.Contains(loc, "msg=") {
		t.Fatalf("closing the period: %d %s", code, loc)
	}
	rows := showbackRows(t, h, "2026-09")
	want := [][]string{
		{"2026-09", "aws", "Customer One", "1.24", "0.00", "1.24"},
		{"2026-09", "gcp", "Customer Two", "1.84", "0.00", "1.84"},
	}
	if fmt.Sprint(rows) != fmt.Sprint(want) {
		t.Fatalf("the showback is\n  %v\nwant\n  %v", rows, want)
	}

	// The balance, against SQL over what the reader wrote.
	var imported int64
	h.st.DB().QueryRow(`SELECT SUM(billed_cents) FROM charges WHERE provenance='tokenfuse-focus'`).Scan(&imported)
	var sum int64
	for _, r := range rows {
		f, err := strconv.ParseFloat(r[5], 64)
		if err != nil {
			t.Fatal(err)
		}
		sum += int64(f*100 + 0.5)
	}
	if imported != 308 || sum != imported {
		t.Errorf("the showback sums to %d cents against an import of %d (worked by hand: 308)", sum, imported)
	}

	// /chargeback carries the same rows, frozen, and names the unit rules.
	_, page, _ := h.get(t, "/chargeback?period=2026-09")
	for _, s := range []string{"closed", ">aws<", ">gcp<", "1.24", "1.84", "3.08", "Customer One", "Customer Two"} {
		if !strings.Contains(page, s) {
			t.Errorf("/chargeback does not carry %q after the stamps and the close:\n%s", s, page)
		}
	}
}

func TestAnUnstampedUnitIsNamedAsUnruledOnTheChargebackPage(t *testing.T) {
	h := startWithUnits(t)
	h.signUp(t, "boss", "boss-password-2026")
	_, page, _ := h.get(t, "/chargeback?period=2026-09")
	for _, s := range []string{"Customer units", ">aws<", ">gcp<", "no rule yet", "1.24", "1.84"} {
		if !strings.Contains(page, s) {
			t.Errorf("/chargeback does not say %q for units nobody has ruled on:\n%s", s, page)
		}
	}
	if strings.Contains(page, "Customer One") {
		t.Error("/chargeback names a business unit nobody has stamped")
	}
}

func TestOnlyTheOwnerOrAnAdminCanStampAUnitRule(t *testing.T) {
	h := startWithUnits(t)
	h.signUp(t, "boss", "boss-password-2026")
	for _, u := range []string{"owner1", "rando"} {
		if _, err := h.au.Create(u, u+"-password-2026", "operator"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.au.Create("eyes", "eyes-password-2026", "viewer"); err != nil {
		t.Fatal(err)
	}
	sprint := newUnitSprint(t, h)
	art, ord := plantCarriedUnitRule(t, h, sprint, "owner1", "aws", "Customer One")

	for _, who := range []string{"rando", "eyes"} {
		c := h.as(t, who, who+"-password-2026")
		code, _ := c.post(t, stampPath(art, ord), url.Values{"csrf": {c.csrf(t, "/board")}})
		if code != 303 {
			t.Fatalf("%s: answered %d, want a redirect", who, code)
		}
		if n := unitRuleCount(t, h); n != 0 {
			t.Fatalf("%s's stamp wrote %d unit rule(s)", who, n)
		}
	}
	// And nobody reaches it without the token.
	owner := h.as(t, "owner1", "owner1-password-2026")
	owner.post(t, stampPath(art, ord), url.Values{"csrf": {"not-the-token"}})
	if n := unitRuleCount(t, h); n != 0 {
		t.Fatalf("a stamp without a valid CSRF token wrote %d unit rule(s)", n)
	}
	// The owner's own stamp, last, so the refusals above were not vacuous.
	owner.post(t, stampPath(art, ord), url.Values{"csrf": {owner.csrf(t, "/board")}})
	if n := unitRuleCount(t, h); n != 1 {
		t.Fatalf("the owner's own stamp wrote %d unit rule(s), want 1", n)
	}
}

// A unit name comes out of somebody else's file. The unruled line is one
// aggregate and prints no name, so a formula in a unit cannot reach a
// spreadsheet through the showback; the page escapes it.
func TestAHostileUnitNameNeverReachesTheShowbackFileOrTheMarkup(t *testing.T) {
	hostile := `=HYPERLINK("http://x","click")<script>alert(1)</script>`
	h := startWithUnits(t, webUnitRow("0.010000", "2026-09-04", hostile))
	h.signUp(t, "boss", "boss-password-2026")

	_, body, _ := h.get(t, "/export/showback.csv?period=2026-09")
	if strings.Contains(body, "HYPERLINK") {
		t.Errorf("an unruled unit's name reached the showback file:\n%s", body)
	}
	_, page, _ := h.get(t, "/chargeback?period=2026-09")
	if strings.Contains(page, "<script>alert(1)</script>") {
		t.Errorf("a unit name was rendered as markup on /chargeback:\n%s", page)
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Errorf("the hostile unit does not appear, escaped, on /chargeback:\n%s", page)
	}
}

// The fixture's own showback must not change because a unit-aware reader
// exists: on the generated estate there are no units and no extra row.
func TestTheGeneratedEstatesShowbackIsUntouchedByUnits(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	_, page, _ := h.get(t, "/chargeback")
	if strings.Contains(page, "Customer units") {
		t.Error("the generated estate's /chargeback shows a Customer units panel")
	}
	_, body, _ := h.get(t, "/export/showback.csv")
	if strings.Contains(body, "(unruled units)") {
		t.Errorf("the generated estate's showback carries an unruled row:\n%s", body)
	}
}

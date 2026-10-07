package crew_test

// costcrew#74: allocation.rule gains a second target shape, a unit-keyed
// one: {"unit": ..., "business_unit": ...}, where unit is the Team the
// TokenFuse reader wrote from a file's x_unit. A unit name comes out of a
// file somebody else produced and ends up in a CSV a spreadsheet opens, so
// the save-time gate is strict about what a name may look like, and a
// proposal for a unit that has no rows is refused when it is written rather
// than carried to an owner to stamp.

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/estate"
)

// unitRuleDB is optionsTestDB with a charges table holding the rows two
// units arrived on, both written by the TokenFuse reader (provenance set),
// and a third team a reader did not write.
func unitRuleDB(t *testing.T) *sql.DB {
	t.Helper()
	db := optionsTestDB(t)
	if _, err := db.Exec(estate.SeedSchema); err != nil {
		t.Fatal(err)
	}
	longUnit := strings.Repeat("u", 128)
	for _, team := range []string{"aws", "acme eu", "клієнт-1", longUnit, "-leading-dash"} {
		if _, err := db.Exec(`INSERT INTO charges(source, day, service, team, category, billed_cents,
			quantity, unit, meter, model, provenance)
			VALUES ('ai','2026-09-02','LLM inference',?, 'Usage',100,1,'tokens','m','mdl','tokenfuse-focus')`,
			team); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO charges(source, day, service, team, category, billed_cents,
		quantity, unit, meter, model, provenance)
		VALUES ('ai','2026-09-02','LLM inference','generated-team','Usage',100,1,'tokens','m','mdl',NULL)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func unitSaves(t *testing.T, db *sql.DB, target string) (refused bool, reason string, stored int) {
	t.Helper()
	taskID := plantTaskAs(t, db, "chargeback", "ai")
	body := allocationRuleOption(target)
	artID := plantDraftArtifactAs(t, db, taskID, "chargeback", body)
	refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "chargeback", body, nil)
	if err != nil {
		t.Fatalf("ValidateAndSaveOptions returned an error rather than a refusal: %v", err)
	}
	opts, err := crew.Options(db, artID)
	if err != nil {
		t.Fatal(err)
	}
	return refused, reason, len(opts)
}

func TestUnitRuleTargetHostileInputs(t *testing.T) {
	long := strings.Repeat("x", 129)
	cases := []struct {
		name, target, reasonHas string
	}{
		{"no business_unit", `{"unit": "aws"}`, "business_unit"},
		{"an empty unit", `{"unit": "", "business_unit": "One"}`, "unit"},
		{"a whitespace-only unit", `{"unit": "   ", "business_unit": "One"}`, "unit"},
		{"a unit with a leading space", `{"unit": " aws", "business_unit": "One"}`, "unit"},
		{"a unit with a trailing space", `{"unit": "aws ", "business_unit": "One"}`, "unit"},
		{"a unit with a newline", `{"unit": "a\nb", "business_unit": "One"}`, "unit"},
		{"a unit with a NUL", `{"unit": "a\u0000b", "business_unit": "One"}`, "unit"},
		{"a unit with a line separator", `{"unit": "a\u2028b", "business_unit": "One"}`, "unit"},
		{"a unit with a bidi override", `{"unit": "a\u202eb", "business_unit": "One"}`, "unit"},
		{"a unit 129 bytes long", `{"unit": "` + long + `", "business_unit": "One"}`, "unit"},
		{"a unit that is a spreadsheet formula", `{"unit": "=HYPERLINK(\"http://x\")", "business_unit": "One"}`, "formula"},
		{"a unit starting with plus", `{"unit": "+1", "business_unit": "One"}`, "formula"},
		{"a unit starting with minus", `{"unit": "-1", "business_unit": "One"}`, "formula"},
		{"a unit starting with at", `{"unit": "@SUM(1)", "business_unit": "One"}`, "formula"},
		{"a unit that is a number", `{"unit": 5, "business_unit": "One"}`, "unit"},
		{"a unit that is an array", `{"unit": ["aws"], "business_unit": "One"}`, "unit"},
		{"a null unit", `{"unit": null, "business_unit": "One"}`, "unit"},
		{"an empty business_unit", `{"unit": "aws", "business_unit": ""}`, "business_unit"},
		{"a whitespace business_unit", `{"unit": "aws", "business_unit": "  "}`, "business_unit"},
		{"a business_unit 81 bytes long", `{"unit": "aws", "business_unit": "` + strings.Repeat("b", 81) + `"}`, "business_unit"},
		{"a business_unit that is a formula", `{"unit": "aws", "business_unit": "=1+1"}`, "formula"},
		{"a business_unit with a control character", `{"unit": "aws", "business_unit": "a\tb"}`, "business_unit"},
		{"a business_unit that is a number", `{"unit": "aws", "business_unit": 7}`, "business_unit"},
		{"a unit and a rule id", `{"unit": "aws", "business_unit": "One", "rule_id": 1}`, "both"},
		{"a unit and a method", `{"unit": "aws", "business_unit": "One", "method": "even-split"}`, "both"},
		{"a unit and a share", `{"unit": "aws", "business_unit": "One", "share": 0.5}`, "both"},
		{"a field nobody reads", `{"unit": "aws", "business_unit": "One", "note": "x"}`, "note"},
		{"a unit that has no rows", `{"unit": "ghost", "business_unit": "One"}`, "ghost"},
		{"a unit only a generated row carries", `{"unit": "generated-team", "business_unit": "One"}`, "generated-team"},
		{"a unit that is a roster team", `{"unit": "ml-platform", "business_unit": "One"}`, "ml-platform"},
		{"a 1 MB target", `{"unit": "aws", "business_unit": "One", "padding": "` + strings.Repeat("x", 1_100_000) + `"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := unitRuleDB(t)
			// the roster-team case needs rows to be refused for the right reason
			if _, err := db.Exec(`INSERT INTO charges(source, day, service, team, category, billed_cents,
				quantity, unit, meter, model, provenance)
				VALUES ('ai','2026-09-02','LLM inference','ml-platform','Usage',100,1,'tokens','m','mdl','tokenfuse-focus')`); err != nil {
				t.Fatal(err)
			}
			refused, reason, stored := unitSaves(t, db, c.target)
			if !refused {
				t.Fatalf("%s: accepted", c.name)
			}
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s: refused with no reason", c.name)
			}
			if c.reasonHas != "" && !strings.Contains(reason, c.reasonHas) {
				t.Errorf("%s: the reason %q does not name %q", c.name, reason, c.reasonHas)
			}
			if stored != 0 {
				t.Errorf("%s: %d options stored despite the refusal", c.name, stored)
			}
		})
	}
}

func TestUnitRuleTargetBoundariesAreAccepted(t *testing.T) {
	cases := []struct{ name, target string }{
		{"an ordinary unit", `{"unit": "aws", "business_unit": "Customer One"}`},
		{"a unit with an inner space", `{"unit": "acme eu", "business_unit": "Customer One"}`},
		{"a unit in Cyrillic", `{"unit": "клієнт-1", "business_unit": "Відділ продажів"}`},
		{"a unit of exactly 128 bytes", `{"unit": "` + strings.Repeat("u", 128) + `", "business_unit": "One"}`},
		{"a business_unit of exactly 80 bytes", `{"unit": "aws", "business_unit": "` + strings.Repeat("b", 80) + `"}`},
		// A dash that is not the first character is a name, not a formula.
		{"a dash inside a unit", `{"unit": "acme-eu", "business_unit": "One-Two"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := unitRuleDB(t)
			if _, err := db.Exec(`INSERT INTO charges(source, day, service, team, category, billed_cents,
				quantity, unit, meter, model, provenance)
				VALUES ('ai','2026-09-02','LLM inference','acme-eu','Usage',100,1,'tokens','m','mdl','tokenfuse-focus')`); err != nil {
				t.Fatal(err)
			}
			refused, reason, stored := unitSaves(t, db, c.target)
			if refused {
				t.Fatalf("%s: refused: %s", c.name, reason)
			}
			if stored != 1 {
				t.Errorf("%s: %d options stored, want 1", c.name, stored)
			}
		})
	}
}

// The unit-shaped target is kept verbatim on the option, like the other
// targets, so the stamp applies what the analyst wrote and nothing else.
func TestAUnitRuleTargetIsCarriedOnTheOptionVerbatim(t *testing.T) {
	db := unitRuleDB(t)
	taskID := plantTaskAs(t, db, "chargeback", "ai")
	body := allocationRuleOption(`{"unit": "aws", "business_unit": "Customer One"}`)
	artID := plantDraftArtifactAs(t, db, taskID, "chargeback", body)
	refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "chargeback", body, nil)
	if err != nil || refused {
		t.Fatalf("refused=%v reason=%q err=%v", refused, reason, err)
	}
	opts, err := crew.Options(db, artID)
	if err != nil || len(opts) != 1 {
		t.Fatalf("options %v err %v", opts, err)
	}
	tgt, isUnit, why := crew.ParseUnitTarget(opts[0].Target)
	if !isUnit || why != "" || tgt.Unit != "aws" || tgt.BusinessUnit != "Customer One" {
		t.Errorf("the stored target parses as %+v (unit=%v, why=%q)", tgt, isUnit, why)
	}
}

// The existing rule-id shape is not a unit target and must still be read as
// the rule-id shape.
func TestTheRuleIdShapeIsNotMistakenForAUnitTarget(t *testing.T) {
	_, isUnit, why := crew.ParseUnitTarget([]byte(`{"rule_id": 1, "method": "even-split", "share": 0.5}`))
	if isUnit || why != "" {
		t.Errorf("a rule-id target parsed as a unit target (unit=%v, why=%q)", isUnit, why)
	}
	_, isUnit, _ = crew.ParseUnitTarget(nil)
	if isUnit {
		t.Error("no target parsed as a unit target")
	}
	_, isUnit, _ = crew.ParseUnitTarget([]byte(`[1,2]`))
	if isUnit {
		t.Error("a JSON array parsed as a unit target")
	}
}

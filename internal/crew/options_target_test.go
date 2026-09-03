package crew_test

// allocation.rule's own structured target: C2-SPEC.md section 2. "options
// carry a target object ... validated by ValidateAndSaveOptions for that
// class only (absent target refused with the reason)". Red against
// unchanged code: today allocation.rule needs no target at all, so every
// case here that expects a refusal currently passes unrefused, and
// crew.Option carries no Target field yet, so this file does not compile.

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// allocationRuleOption builds a one-option deliverable body naming
// allocation.rule, with target as a raw JSON fragment (or "" to omit the
// field entirely) so each hostile case controls exactly one thing.
func allocationRuleOption(targetJSON string) string {
	target := ""
	if targetJSON != "" {
		target = `"target": ` + targetJSON + `, `
	}
	return "## The pot with no owner\nPurchase on aws has no team to carry it.\n\n```options\n" +
		`{"options": [{"class": "allocation.rule", "summary": "split Purchase on aws by usage", ` +
		target + `"figure_cents": 50000, "saving_cents": 0, "risk": "low", "needs": "the owner's stamp"}]}` +
		"\n```\n"
}

// plantTaskAs and plantDraftArtifactAs generalise plantPlainTask and
// plantDraftArtifact (both fixed to investigator-aws in options_test.go) to
// an arbitrary assignee/author and desk, for the chargeback-analyst
// family's own tests.
func plantTaskAs(t *testing.T, db *sql.DB, assignee, desk string) int {
	t.Helper()
	res, err := db.Exec(`INSERT INTO tasks
		(title, goal, assignee, desk, state, budget_cents, spent_cents, created, updated)
		VALUES ('close the books', 'write the close pack', ?, ?, 'active', 0, 0,
		        datetime('now'), datetime('now'))`, assignee, desk)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return int(id)
}

func plantDraftArtifactAs(t *testing.T, db *sql.DB, taskID int, author, body string) int {
	t.Helper()
	res, err := db.Exec(`INSERT INTO artifacts
		(task, author, title, body, state, created)
		VALUES (?, ?, 'the close pack', ?, 'draft', datetime('now'))`,
		taskID, author, body)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return int(id)
}

func TestAllocationRuleWithNoTargetIsRefused(t *testing.T) {
	db := optionsTestDB(t)
	taskID := plantTaskAs(t, db, "chargeback", "management")
	body := allocationRuleOption("")
	artID := plantDraftArtifactAs(t, db, taskID, "chargeback", body)

	refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "chargeback", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !refused {
		t.Fatal("allocation.rule with no target was accepted")
	}
	if !strings.Contains(reason, "target") {
		t.Errorf("reason %q does not name the target", reason)
	}
	opts, err := crew.Options(db, artID)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 0 {
		t.Errorf("%d options stored despite the refusal", len(opts))
	}
}

func TestAllocationRuleTargetHostileInputs(t *testing.T) {
	cases := []struct {
		name   string
		target string
	}{
		{"share above 1", `{"rule_id": 1, "method": "proportional-usage", "share": 1.5}`},
		{"negative share", `{"rule_id": 1, "method": "proportional-usage", "share": -0.2}`},
		{"a 1 MB target", `{"rule_id": 1, "method": "proportional-usage", "share": 0.5, ` +
			`"padding": "` + strings.Repeat("x", 1_100_000) + `"}`},
		{"no rule_id at all", `{"method": "proportional-usage", "share": 0.5}`},
		{"a zero rule_id", `{"rule_id": 0, "method": "proportional-usage", "share": 0.5}`},
		{"a negative rule_id", `{"rule_id": -3, "method": "proportional-usage", "share": 0.5}`},
		{"no method", `{"rule_id": 1, "share": 0.5}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := optionsTestDB(t)
			taskID := plantTaskAs(t, db, "chargeback", "management")
			body := allocationRuleOption(c.target)
			artID := plantDraftArtifactAs(t, db, taskID, "chargeback", body)

			refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "chargeback", body, nil)
			if err != nil {
				t.Fatalf("ValidateAndSaveOptions returned an error rather than a refusal: %v", err)
			}
			if !refused {
				t.Fatalf("%s: want refused, got accepted", c.name)
			}
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s: refused with no reason", c.name)
			}
			opts, err := crew.Options(db, artID)
			if err != nil {
				t.Fatal(err)
			}
			if len(opts) != 0 {
				t.Errorf("%s: %d options stored despite the refusal", c.name, len(opts))
			}
		})
	}
}

// A rule id this store has never heard of is not this function's job to
// catch: it has no rules table to check against without importing
// internal/finops, which would cycle (finops already imports crew). It is
// refused later, when the option is actually applied and finops.SetRule
// itself refuses an unknown id -- see
// TestApplyAllocationRuleWithAnUnknownRuleIDFails in internal/finops.
func TestAllocationRuleTargetIsAcceptedEvenWithAnUnknownRuleId(t *testing.T) {
	db := optionsTestDB(t)
	taskID := plantTaskAs(t, db, "chargeback", "management")
	body := allocationRuleOption(`{"rule_id": 999999, "method": "proportional-usage", "share": 0.5}`)
	artID := plantDraftArtifactAs(t, db, taskID, "chargeback", body)

	refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "chargeback", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if refused {
		t.Fatalf("a structurally valid target naming an unknown rule id was refused at save "+
			"time: %s", reason)
	}
}

func TestAllocationRuleWithAValidTargetIsSaved(t *testing.T) {
	db := optionsTestDB(t)
	taskID := plantTaskAs(t, db, "chargeback", "management")
	body := allocationRuleOption(`{"rule_id": 1, "method": "proportional-usage", "share": 0.5}`)
	artID := plantDraftArtifactAs(t, db, taskID, "chargeback", body)

	refused, reason, err := crew.ValidateAndSaveOptions(db, artID, "chargeback", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if refused {
		t.Fatalf("a well-formed allocation.rule target was refused: %s", reason)
	}
	opts, err := crew.Options(db, artID)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 1 {
		t.Fatalf("got %d options, want 1", len(opts))
	}
	if len(opts[0].Target) == 0 {
		t.Fatal("the saved option's Target is empty")
	}
	if !strings.Contains(string(opts[0].Target), `"rule_id"`) {
		t.Errorf("Target %q does not carry rule_id", string(opts[0].Target))
	}
}

package web_test

// C4-SPEC.md section 2's page-level property, proved through the actual
// HTTP surface the way TestTheAIPageReadsARealImport already proves the AI
// page's own real-vs-generated switch: the SaaS page's Commitments panel
// shows real figures once a connector has written one, and says so, rather
// than the generated waterline.

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const commitmentPageCSV = "BilledCost,EffectiveCost,BillingCurrency,ChargePeriodStart,ChargePeriodEnd," +
	"ChargeDescription,ProviderName,PublisherName,InvoiceIssuerName,ServiceName,ServiceCategory," +
	"ResourceId,ResourceName,SubAccountId,SubAccountName,x_run_id,x_parent_run_id,x_agent_id," +
	"x_model,x_tokens_in,x_tokens_out,x_blocked,x_cost_basis,x_outcome,x_unit,x_tool_calls," +
	"ChargeCategory,CommitmentDiscountId,CommitmentDiscountType,CommitmentDiscountStatus," +
	"CommitmentDiscountQuantity,CommitmentDiscountUnit\n" +
	// One ordinary usage row, real cost the commitment's own coverage is
	// measured against.
	"1500.000000,1500.000000,USD,2026-09-02T10:00:00Z,2026-09-02T10:00:00Z,call,Anthropic,Anthropic," +
	"Anthropic,LLM inference,AI,agent://a/b/c,agent://a/b/c,run-1,run-1,run-1,,agent://a/b/c," +
	"claude-haiku-4-5,1000,500,false,settled,,,0,,,,,,\n" +
	// One Purchase row: the commitment itself.
	"1000.000000,1000.000000,USD,2026-01-01T00:00:00Z,2027-01-01T00:00:00Z,commitment purchase," +
	"Anthropic,Anthropic,Anthropic,Reserved Capacity,AI,,,sub-1,,,,,,,,false,,,,0," +
	"Purchase,page-test-cud-1,cud,Used,700,normalized-hours\n"

func TestTheSaaSPageReadsARealCommitment(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "commitments.csv"), []byte(commitmentPageCSV), 0o644); err != nil {
		t.Fatal(err)
	}

	saveCSRF := admin.csrf(t, "/connectors/tokenfuse-focus")
	code, loc := admin.post(t, "/connectors/tokenfuse-focus/save", url.Values{
		"path": {dir}, "csrf": {saveCSRF},
	})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("saving the path: %d %s", code, loc)
	}
	importCSRF := admin.csrf(t, "/connectors/tokenfuse-focus")
	code, loc = admin.post(t, "/connectors/tokenfuse-focus/import", url.Values{
		"csrf": {importCSRF}, "replace-generated": {"yes"},
	})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("importing with -replace-generated: %d %s", code, loc)
	}

	status, body, _ := admin.get(t, "/saas")
	if status != 200 {
		t.Fatalf("GET /saas: %d", status)
	}
	if !strings.Contains(body, "page-test-cud-1") {
		t.Errorf("GET /saas does not name the real commitment's own id:\n%s", body)
	}
	if !strings.Contains(body, "Read from a connector") {
		t.Errorf("GET /saas does not say the commitments are real:\n%s", body)
	}
	if strings.Contains(body, "Compute Savings Plan") {
		t.Error("GET /saas still shows a generated commitment (Compute Savings Plan) after a real import")
	}
}

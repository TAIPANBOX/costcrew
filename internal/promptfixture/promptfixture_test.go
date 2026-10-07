package promptfixture

import (
	"strings"
	"testing"
)

// The fixture is only worth gating against if it is populated and every text
// column of it has been decided about.
func TestTheFixtureBuildsAndEveryTextColumnIsClassified(t *testing.T) {
	st, err := Build(t.TempDir())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer st.Close()
	db := st.DB()

	un, err := UnclassifiedColumns(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(un) > 0 {
		t.Errorf("text columns nobody has classified as identifier, free text, plain or secret: %s\n"+
			"decide each one in promptfixture.Classes; an identifier that is not named there is one "+
			"the prompt-data gate cannot look for", strings.Join(un, ", "))
	}

	ids, err := Of(db, ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(Distinct(ids)) < 100 {
		t.Errorf("only %d distinct identifiers in the fixture; it is meant to be a whole installation", len(Distinct(ids)))
	}
	have := map[string]bool{}
	for _, v := range ids {
		have[v.Origin] = true
	}
	for _, want := range []string{
		"charges.service", "charges.team", "charges.source", "charges.invoice_id", "ai_calls.agent",
		"ai_calls.run_id", "ai_calls.model", "commitments.id", "licences.vendor", "licences.product",
		"recommendations.resource", "recommendations.id", "users.username", "artifact_options.decided_by",
		"artifacts.stamper", "tasks.owner", "teams.owner", "analysts.name", "chargeback.closed_by",
		"forecasts.frozen_by", "budget_recommendations.team", "desk_halts.desk", "drivers.scope",
	} {
		if !have[want] {
			t.Errorf("the fixture holds no identifier in %s, so no section that prints it is being looked at", want)
		}
	}
	free, err := Of(db, Free)
	if err != nil {
		t.Fatal(err)
	}
	var markers int
	for _, v := range free {
		for _, m := range []string{PastBodyMarker, RefusalMarker, SummaryMarker, DriverMarker, OperatorGoalText} {
			if strings.Contains(v.Text, m) {
				markers++
				break
			}
		}
	}
	if markers < 5 {
		t.Errorf("only %d free-text values carry a marker; the gate needs a marker in each kind of free text", markers)
	}
}

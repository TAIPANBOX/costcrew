// Package promptfixture is TEST SUPPORT: one fully populated installation,
// and an independent enumeration of every identifier in it.
//
// It exists for the gate that holds the `-prompt-data` policy (CLAUDE.md
// invariant 70). That gate has to build every packet section and every tool
// result in every mode and require that no real identifier from the store is
// in the output. For the gate to mean anything, two things must be true that
// no single package's own test fixture gives it:
//
//  1. Every section and every tool must have something to say. A section that
//     prints nothing for lack of data is a section the gate never looked at,
//     so Build fills every table a section or a tool reads: the generated
//     estate, a roster with owners, console accounts, AI calls with agent
//     URIs, run ids and invoice ids, commitments, licences, recommendations,
//     budget recommendations, a closed period, posted deliverables with
//     options in every fate, and free text carrying a marker no real
//     deliverable would contain.
//
//  2. The list of identifiers the gate checks against must NOT be the list
//     the masker was written from. If both came from one person's idea of
//     "the identifier columns", a column that person forgot would be missing
//     from both and the gate would be green over a leak. So Identifiers walks
//     the SCHEMA and treats every text column as an identifier unless this
//     file says, by name, that it is not (Classes). A column added tomorrow
//     is unclassified, and UnclassifiedColumns names it, which the gate turns
//     into a failure: the new column has to be decided about before anything
//     passes.
//
// Nothing outside a _test.go file imports this package.
package promptfixture

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/detect"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/history"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

// Markers planted in free text. A real deliverable would never say these, so
// finding one in a prompt is finding that free text left the process.
const (
	PastBodyMarker   = "ZEBRA-QUOKKA-BODY"
	RefusalMarker    = "ZEBRA-QUOKKA-REFUSAL"
	SummaryMarker    = "ZEBRA-QUOKKA-SUMMARY"
	DriverMarker     = "ZEBRA-QUOKKA-DRIVER"
	OperatorGoalText = "ZEBRA-QUOKKA-GOAL"
)

// Users are the console accounts Build creates: names, so a prompt that
// leaks "applied by alice" is a leak the gate can see.
var Users = []string{"alice", "bob", "carol-finops"}

// Build opens a store in dir and fills it. The caller closes it.
func Build(dir string) (*store.Store, error) {
	st, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := fill(st); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

func fill(st *store.Store) error {
	db := st.DB()

	// Console accounts. The hash and the session token are secrets, and
	// Secrets below says the gate must never find them in a prompt either.
	roles := map[string]string{"alice": "admin", "bob": "operator", "carol-finops": "viewer"}
	for _, u := range Users {
		if _, err := db.Exec(`INSERT INTO users(username, pw_hash, role, created) VALUES (?,?,?,1)`,
			u, "scrypt$"+SecretHash+"$"+u, roles[u]); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`INSERT INTO sessions(token_hash, username, created, expires) VALUES (?,?,1,2)`,
		SecretToken, "alice"); err != nil {
		return err
	}

	if _, err := estate.Seed(db); err != nil {
		return err
	}
	for _, f := range []func() error{
		func() error { return connectors.EnsureFocusSchema(db) },
		func() error { return connectors.EnsureRecommendationsSchema(db) },
		func() error { return connectors.EnsureLicenceSchema(db) },
		func() error { return connectors.EnsureBudgetRecommendationsSchema(db) },
		func() error { return estate.SeedBudgets(db) },
		func() error { return finops.SeedRules(db) },
		func() error { _, err := crew.SeedRoster(db, "alice"); return err },
		func() error { return crew.EnsureTeamOwner(db) },
		func() error { return crew.EnsureOwnershipHistory(db) },
		func() error { return crew.EnsureOptionTarget(db) },
		func() error { return crew.EnsureOptionBehalf(db) },
	} {
		if err := f(); err != nil {
			return err
		}
	}
	rec := st.AsRecorder()
	if _, _, err := anomaly.Run(db, time.Now(), detect.Default(), rec); err != nil {
		return err
	}
	list, err := anomaly.List(db, anomaly.Filter{})
	if err != nil {
		return err
	}
	// A typed hint on the two largest anomalies (invariant 76): one answered
	// by a named model, one that failed with a reason, so the gate sees the
	// hint section, the model column and the reason column.
	if err := anomaly.EnsureHintColumns(db); err != nil {
		return err
	}
	if len(list) >= 2 {
		if err := anomaly.SaveHint(db, list[0].ID, anomaly.Hint{Class: "runaway_agent", Probability: 0.81,
			Backend: anomaly.BackendOwnModel, Model: HintModel, AnswerID: "ans-planted-0001",
			At: "2026-09-01T00:00:00Z"}); err != nil {
			return err
		}
		if err := anomaly.SaveHint(db, list[1].ID, anomaly.Hint{
			Reason: "typryx refused the ask: HTTP 429, over_hourly_cap", At: "2026-09-01T00:00:00Z"}); err != nil {
			return err
		}
	}
	var seeds []crew.AnomalySeed
	for _, a := range list {
		seeds = append(seeds, crew.AnomalySeed{ID: a.ID, Source: a.Source, Service: a.Service,
			Day: a.Day, Direction: a.Direction, Excess: a.Excess})
	}
	if _, _, _, err := crew.Seed(db, seeds); err != nil {
		return err
	}
	if _, err := history.Seed(db, rec); err != nil {
		return err
	}

	// Who owns what: teams with a named owner, tasks with an owner.
	for team, owner := range map[string]string{"ml-platform": "alice", "research": "bob"} {
		if _, err := db.Exec(`INSERT OR REPLACE INTO teams(name, owner) VALUES (?,?)`, team, owner); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`UPDATE tasks SET owner='alice' WHERE id%2=0`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE tasks SET owner='bob' WHERE id%2=1`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE sprints SET goal=? WHERE id=(SELECT MIN(id) FROM sprints)`,
		OperatorGoalText+" for the ml-platform team"); err != nil {
		return err
	}

	// Invoices, on the generated ledger and on the AI calls.
	if _, err := db.Exec(`UPDATE charges SET invoice_id='INV-2026-07-00417'
		WHERE source='aws' AND substr(day,1,7)='2026-07' AND service='Amazon EC2'`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE charges SET invoice_id='INV-2026-07-00533'
		WHERE source='gcp' AND substr(day,1,7)='2026-07' AND service='BigQuery'`); err != nil {
		return err
	}

	if err := plantAI(db); err != nil {
		return err
	}
	if err := plantCommitments(db); err != nil {
		return err
	}
	if err := importCSVs(db); err != nil {
		return err
	}
	if err := plantDeliverables(db); err != nil {
		return err
	}

	// Customer units (costcrew#74) in the closed period: one a person gave a
	// business unit, one with no rule yet, so the close pack lists both and
	// the gate looks at a unit name and a business unit name.
	if err := plantUnits(db); err != nil {
		return err
	}
	// A closed period, so the close pack has a true-up and chargeback has a
	// closed_by; a frozen forecast with a frozen_by.
	if err := finops.Close(db, "2026-07", "alice"); err != nil {
		return err
	}
	// history.Seed froze some months already; one more, by a person.
	if _, err := db.Exec(`UPDATE forecasts SET frozen_by='bob' WHERE rowid=(SELECT MIN(rowid) FROM forecasts)`); err != nil {
		return err
	}
	if _, err := crew.SettlePlanAsk(db, "Sprint 2026-09", "2026-09", "supervisor", 4200,
		crew.PlanAskAccepted, ""); err != nil {
		return err
	}
	if _, err := db.Exec(crew.HaltSchema); err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO desk_halts
		(desk, reason, started, applied_by, owner, suspended) VALUES
		('onprem', 'untagged share crossed T.untagged', '2026-09-01', 'supervisor', 'alice', '')`); err != nil {
		return err
	}
	return nil
}

// HintModel is the model a planted typed hint names; a model is a name, and
// the gate requires it absent from every masked or aggregates packet.
const HintModel = "hint-model-planted-7b"

// SecretHash and SecretToken are values the gate requires to be absent from
// every output in every mode, including full: nothing a prompt is for needs
// a password hash or a session token.
const (
	SecretHash  = "9f2c-planted-password-hash"
	SecretToken = "sess-planted-session-token-7731"
)

func plantAI(db *sql.DB) error {
	type call struct {
		agent, runID, model, outcome, invoice string
		micros                                int64
	}
	calls := []call{
		{"agent://taipanbox.dev/costcrew/triage-aws", "run-triage-01", "claude-haiku-4-5", "case_resolved", "INV-AI-2026-08-0091", 3_500_000},
		{"agent://taipanbox.dev/costcrew/forecaster", "run-forecast-01", "claude-sonnet-4-5", "", "INV-AI-2026-08-0091", 10_500_000},
		{"agent://taipanbox.dev/costcrew/untagged-bot", "run-bot-07", "claude-opus-4-5", "", "", 7_000_000},
	}
	for i, c := range calls {
		var outcome, invoice any
		if c.outcome != "" {
			outcome = c.outcome
		}
		if c.invoice != "" {
			invoice = c.invoice
		}
		day := fmt.Sprintf("2026-08-%02d", 3+i)
		if _, err := db.Exec(`INSERT INTO ai_calls
			(file_sha256, row_no, ts, day, team, agent, run_id, parent_run_id,
			 provider, model, tokens_in, tokens_out, billed_microusd, blocked, basis,
			 outcome, tool_calls, invoice_id, key_id, block_reason)
			VALUES ('planted-fixture',?,?,?,?,?,?,?,'Anthropic',?,1000,500,?,0,'settled',?,0,?,?,'')`,
			i+1, day+"T00:00:00Z", day, "ml-platform", c.agent, c.runID, c.runID,
			c.model, c.micros, outcome, invoice, fmt.Sprintf("fixture-credential-%02d", i+1)); err != nil {
			return err
		}
		if _, err := db.Exec(`INSERT INTO charges
			(source, day, service, team, category, billed_cents, quantity, unit, meter, model, provenance)
			VALUES ('ai',?,'Anthropic API',NULL,'Usage',?,0,'tokens',?,?,'planted-fixture')`,
			day, (c.micros+5_000)/10_000, c.model, c.model); err != nil {
			return err
		}
	}
	return nil
}

func plantUnits(db *sql.DB) error {
	for _, u := range []struct {
		unit  string
		cents int
	}{{"acme-retail-unit", 41_200}, {"globex-labs-unit", 9_900}} {
		if _, err := db.Exec(`INSERT INTO charges
			(source, day, service, team, category, billed_cents, quantity, unit, meter, model, provenance)
			VALUES ('ai','2026-07-12','LLM inference',?,'Usage',?,1,'tokens','m','mdl','tokenfuse-focus')`,
			u.unit, u.cents); err != nil {
			return err
		}
	}
	_, err := db.Exec(`INSERT INTO unit_rules(unit, business_unit, decided_by, artifact, ordinal, applied_at)
		VALUES ('acme-retail-unit','Northwind Retail Division','bob',1,1,'2026-07-20 09:00:00')`)
	return err
}

func plantCommitments(db *sql.DB) error {
	rows := []struct {
		id, kind, source string
		cents            int64
	}{
		{"ri-aws-m5-4412", "reserved-instance", "aws", 460_000},
		{"cud-gcp-n2-0099", "cud", "gcp", 210_000},
		{"sp-compute-aws-7", "savings-plan", "aws", 90_000},
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO commitments
			(id, kind, status, quantity, unit, source, date_start, date_end, monthly_cents)
			VALUES (?,?,'Used',700,'hours',?,'2026-01-01','2026-10-20',?)`,
			r.id, r.kind, r.source, r.cents); err != nil {
			return err
		}
	}
	return nil
}

// importCSVs reads the connectors' own committed fixtures through the real
// readers, so the rows have exactly the shape production writes.
func importCSVs(db *sql.DB) error {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return fmt.Errorf("cannot locate the connectors' testdata")
	}
	testdata := filepath.Join(filepath.Dir(thisFile), "..", "connectors", "testdata")
	for _, c := range []struct{ id, file string }{
		{"saas-seats", "saas-seats-2026-09-03.csv"},
		{"aws-rightsizing", "aws-rightsizing-2026-09-02.csv"},
		{"gcp-recommender", "gcp-recommender-2026-09-02.csv"},
		{"azure-advisor", "azure-advisor-2026-09-02.csv"},
		{"aws-budgets-recommended", "aws-budgets-recommended-2026-09-03.csv"},
		{"gcp-cost-recommender-budget", "gcp-cost-recommender-budget-2026-09-03.csv"},
		{"azure-advisor-budget", "azure-advisor-budget-2026-09-03.csv"},
	} {
		data, err := os.ReadFile(filepath.Join(testdata, c.file))
		if err != nil {
			return err
		}
		dir, err := os.MkdirTemp("", "promptfixture-"+c.id+"-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		if err := os.WriteFile(filepath.Join(dir, c.file), data, 0o644); err != nil {
			return err
		}
		if err := connectors.Save(db, c.id, map[string]string{"path": dir}); err != nil {
			return fmt.Errorf("%s: %w", c.id, err)
		}
		if _, err := connectors.Import(db, c.id, true, connectors.ImportOptions{}); err != nil {
			return fmt.Errorf("%s: %w", c.id, err)
		}
	}
	// The provider usage readers (invariant 80) read a folder of JSON, so
	// each one's whole fixture folder is copied, and their two tables then
	// stand in this installation for the column check to classify.
	for _, c := range []struct{ id, dir string }{
		{"anthropic-usage", "anthropic"}, {"openai-usage", "openai"},
	} {
		src := filepath.Join(testdata, "provider-usage", c.dir)
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		dir, err := os.MkdirTemp("", "promptfixture-"+c.id+"-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		for _, e := range entries {
			data, err := os.ReadFile(filepath.Join(src, e.Name()))
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o644); err != nil {
				return err
			}
		}
		if err := connectors.Save(db, c.id, map[string]string{"path": dir}); err != nil {
			return fmt.Errorf("%s: %w", c.id, err)
		}
		if _, err := connectors.Import(db, c.id, false, connectors.ImportOptions{}); err != nil {
			return fmt.Errorf("%s: %w", c.id, err)
		}
	}
	return nil
}

// plantDeliverables writes posted deliverables by two analysts on two desks,
// with options in every fate, and a carried option waiting on an owner. Their
// bodies, summaries and reasons carry the markers.
func plantDeliverables(db *sql.DB) error {
	mkTask := func(title, desk, anom string) (int64, error) {
		var a any
		if anom != "" {
			a = anom
		}
		res, err := db.Exec(`INSERT INTO tasks
			(title, goal, assignee, desk, state, budget_cents, spent_cents, anomaly, owner, created, updated)
			VALUES (?, 'say what happened', 'investigator-gcp', ?, 'posted', 0, 0, ?, 'alice',
			        datetime('now'), datetime('now'))`, title, desk, a)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	mkArt := func(task int64, author, body, stamped string) (int64, error) {
		res, err := db.Exec(`INSERT INTO artifacts
			(task, author, title, body, state, created, stamped, stamper)
			VALUES (?,?,'a deliverable',?, 'posted', ?, ?, 'bob')`, task, author, body, stamped, stamped)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	mkOpt := func(art int64, ord int, class, state, decidedBy, reason string) error {
		_, err := db.Exec(`INSERT INTO artifact_options
			(artifact, ordinal, class, summary, figure_cents, saving_cents, risk, needs, evidence, state, decided_by, reason)
			VALUES (?,?,?,?,0,0,'low','a person to check Amazon EC2','[]',?,?,?)`,
			art, ord, class, SummaryMarker+" about the ml-platform team", state, decidedBy, reason)
		return err
	}

	// An anomaly to hang the "last posted explanation on this service" off.
	var anomID, svc, desk string
	if err := db.QueryRow(`SELECT id, service, source FROM anomalies ORDER BY id LIMIT 1`).
		Scan(&anomID, &svc, &desk); err != nil {
		return err
	}
	t1, err := mkTask("Explain the "+svc+" move", desk, anomID)
	if err != nil {
		return err
	}
	if _, err := mkArt(t1, "triage-"+desk, PastBodyMarker+": "+svc+" rose for the ml-platform team, ask bob.", "2026-08-02T09:00:00Z"); err != nil {
		return err
	}

	// The analyst's own history, with each fate.
	own, err := mkTask("Explain the "+svc+" move in July", desk, "")
	if err != nil {
		return err
	}
	for i, fate := range []struct{ state, by, reason string }{
		{"applied", "alice", ""},
		{"refused", "bob", RefusalMarker + " carol-finops said no"},
		{"not_chosen", "", RefusalMarker + " a sibling was chosen"},
		{"carried", "", ""},
	} {
		a, err := mkArt(own, "investigator-"+desk, PastBodyMarker+" number "+fmt.Sprint(i), fmt.Sprintf("2026-07-%02dT10:00:00Z", 10+i))
		if err != nil {
			return err
		}
		if err := mkOpt(a, 1, "anomaly.explain", fate.state, fate.by, fate.reason); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`UPDATE tasks SET desk=? WHERE id=?`, desk, own); err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO decision_requests(artifact, sprint, owner, lapses, created)
		VALUES (1, 1, 'alice', '2026-09-30', '2026-09-01')`); err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT INTO drivers(date_start, date_end, scope, label, kind, source)
		VALUES ('2026-08-01','2026-08-31','*',?, 'recurring', ?)`, DriverMarker+" for the ml-platform team", desk); err != nil {
		return err
	}
	return nil
}

// ------------------------------------------------------- the enumeration

// Class says what a column holds.
type Class string

const (
	// ID is an identifier: a team, a desk, a service, an agent, a person, an
	// invoice, a vendor, a resource. Its distinct values must not appear in
	// any masked or aggregates output.
	ID Class = "identifier"
	// Free is text a person or a model typed. It must not be sent at all
	// under masked, so its marker must not appear.
	Free Class = "free text"
	// Generated is text this console composes from names it can mask and from
	// the role vocabulary it shares with every installation (a task's title
	// and goal, an artifact's title, a sprint's label), or configuration. It
	// is the one class that may legitimately reach a prompt, scrubbed by the
	// mask like everything else; the free-text marker check still applies to
	// it, so a typed goal planted in one is still caught.
	Generated Class = "generated text"
	// Plain is not an identifier: a date, an enum, a number, a hash that
	// names nothing, a configuration sentence every installation shares.
	Plain Class = "plain"
	// Secret must appear in no output of any mode.
	Secret Class = "secret"
)

// Classes is the only place this package says a column is NOT an identifier.
// A text column that is absent from it is UNCLASSIFIED, and the gate fails
// on that, so adding a column to the schema cannot quietly add an identifier
// that nobody decided about.
var Classes = map[string]Class{
	"ai_calls.file_sha256": Plain, "ai_calls.ts": Plain, "ai_calls.day": Plain,
	"ai_calls.team": ID, "ai_calls.agent": ID, "ai_calls.run_id": ID, "ai_calls.parent_run_id": ID,
	"ai_calls.provider": ID, "ai_calls.model": ID, "ai_calls.basis": Plain,
	"ai_calls.outcome": Free, "ai_calls.invoice_id": ID,
	// Invariant 89: TokenFuse 1.7.0's two columns. A key id is the name an
	// operator gave a credential; a block reason is one of the Breaker's
	// closed set of wire strings.
	"ai_calls.key_id": ID, "ai_calls.block_reason": Plain,

	"allocation_rules.source": ID, "allocation_rules.category": Plain,
	"allocation_rules.method": Plain, "allocation_rules.note": Free,

	"analysts.name": ID, "analysts.role": Plain, "analysts.mission": Plain, "analysts.desk": ID,
	"analysts.engine": Plain, "analysts.state": Plain, "analysts.reason": Free,
	"analysts.skills": Plain, "analysts.rights": Plain, "analysts.cadence": Plain,
	"analysts.audience": Plain, "analysts.owner": ID, "analysts.parent": ID,
	"analysts.attestation": Plain, "analysts.attestation_detail": Free, "analysts.hired": Plain,

	"anomalies.id": Plain, "anomalies.source": ID, "anomalies.team": ID, "anomalies.service": ID,
	"anomalies.day": Plain, "anomalies.direction": Plain, "anomalies.rule": Plain,
	"anomalies.rule_version": Plain, "anomalies.driver": Free, "anomalies.caused_by": ID,
	"anomalies.caused_by_kind": Plain, "anomalies.handled_by": ID, "anomalies.state": Plain,
	"anomalies.reason": Free, "anomalies.detected_at": Plain, "anomalies.closed_at": Plain,
	// Invariant 76: typryx's hint. The class and the backend are closed
	// vocabularies, the model is a name like ai_calls.model, the answer id
	// is typryx's own, and the reason is a sentence this console composes.
	"anomalies.hint_class": Plain, "anomalies.hint_backend": Plain, "anomalies.hint_model": ID,
	"anomalies.hint_answer_id": ID, "anomalies.hint_reason": Generated, "anomalies.hint_at": Plain,
	"anomalies.hint_fields_sent": Plain, // invariant 92: template field names, never values

	"artifact_options.class": Plain, "artifact_options.summary": Free, "artifact_options.risk": Plain,
	"artifact_options.needs": Free, "artifact_options.evidence": Free, "artifact_options.target": Plain,
	"artifact_options.state": Plain, "artifact_options.decided_by": ID,
	"artifact_options.decided_at": Plain, "artifact_options.reason": Free,
	"artifact_options.on_behalf_of": ID, "artifact_options.behalf_reason": Free,

	"artifacts.author": ID, "artifacts.title": Generated, "artifacts.body": Free, "artifacts.state": Plain,
	"artifacts.reason": Free, "artifacts.created": Plain, "artifacts.stamped": Plain,
	"artifacts.stamper": ID, "artifacts.source": Plain,

	"attribution.source": ID, "attribution.team": ID, "attribution.service": ID,
	"attribution.day_start": Plain, "attribution.day_end": Plain, "attribution.agent": ID,
	"attribution.confidence": Plain,

	"budget_recommendations.provider": ID, "budget_recommendations.team": ID,
	"budget_recommendations.month": Plain, "budget_recommendations.source_file": ID,
	"budget_recommendations.imported_at": Plain,

	"budgets.source": ID, "budgets.team": ID, "budgets.month": Plain,

	"chargeback.period": Plain, "chargeback.source": ID, "chargeback.team": ID,
	"chargeback.frozen_at": Plain, "chargeback.closed_by": ID,

	"charges.source": ID, "charges.day": Plain, "charges.service": ID, "charges.team": ID,
	"charges.category": Plain, "charges.unit": Plain, "charges.meter": Plain, "charges.model": ID,
	"charges.invoice_id": ID, "charges.provenance": Plain,

	"comments.author": ID, "comments.body": Free, "comments.created": Plain,

	"commitments.id": ID, "commitments.kind": Plain, "commitments.status": Plain,
	"commitments.unit": Plain, "commitments.source": ID, "commitments.date_start": Plain,
	"commitments.date_end": Plain,

	"decision_requests.owner": ID, "decision_requests.lapses": Plain, "decision_requests.created": Plain,

	"desk_halts.desk": ID, "desk_halts.reason": Free, "desk_halts.started": Plain,
	"desk_halts.applied_by": ID, "desk_halts.owner": ID, "desk_halts.suspended": ID,

	"drivers.date_start": Plain, "drivers.date_end": Plain, "drivers.scope": ID,
	"drivers.label": Free, "drivers.kind": Plain, "drivers.source": ID,

	"explainers.team": ID, "explainers.topic": Generated, "explainers.audience": Plain,
	"explainers.author": ID, "explainers.body": Free, "explainers.state": Plain,
	"explainers.reason": Free, "explainers.created": Plain, "explainers.published": Plain,
	"explainers.publisher": ID,

	"forecasts.period": Plain, "forecasts.source": ID, "forecasts.basis": Free,
	"forecasts.frozen_at": Plain, "forecasts.frozen_by": ID,

	"licences.vendor": ID, "licences.product": ID, "licences.renewal_date": Plain,
	"licences.provenance": Plain,

	"plan_asks.sprint_label": Generated, "plan_asks.month": Plain, "plan_asks.analyst": ID,
	"plan_asks.outcome": Plain, "plan_asks.reason": Free, "plan_asks.created": Plain,

	"recommendations.id": ID, "recommendations.provider": ID, "recommendations.desk": ID,
	"recommendations.resource": ID, "recommendations.action": Plain, "recommendations.current": Plain,
	"recommendations.recommended": Plain, "recommendations.source_file": ID,
	"recommendations.imported_at": Plain,

	// invariant 80: the provider's own usage and cost. A model, a key and a
	// workspace or project name something; the rest is a date, an enum or a hash.
	"provider_usage.connector": Plain, "provider_usage.report": Plain, "provider_usage.day": Plain,
	"provider_usage.model": ID, "provider_usage.token_type": Plain,
	"provider_usage.api_key_id": ID, "provider_usage.scope": ID,
	"provider_usage_days.connector": Plain, "provider_usage_days.report": Plain,
	"provider_usage_days.day": Plain, "provider_usage_days.source_file": ID,
	"provider_usage_days.file_sha256": Plain, "provider_usage_days.imported_at": Plain,

	"sessions.token_hash": Secret, "sessions.username": ID,

	"sprints.label": Generated, "sprints.start": Plain, "sprints.finish": Plain, "sprints.state": Plain,
	"sprints.goal": Generated,

	"tasks.title": Generated, "tasks.goal": Generated, "tasks.assignee": ID, "tasks.desk": ID,
	"tasks.state": Plain, "tasks.reason": Free, "tasks.anomaly": Plain, "tasks.created": Plain,
	"tasks.updated": Plain, "tasks.owner": ID,

	"teams.name": ID, "teams.owner": ID,

	"users.username": ID, "users.pw_hash": Secret, "users.role": Plain,

	"settings.key": Plain, "settings.value": Plain,

	"connections.id": Plain, "connections.config": Free, "connections.last_result": Free,
	"connections.last_test": Plain, "connections.last_import": Plain, "connections.last_import_result": Free,

	"unit_rules.unit": ID, "unit_rules.business_unit": ID, "unit_rules.decided_by": ID,
	"unit_rules.applied_at": Plain,
}

// NotIdentifiers are values that are real strings in an ID column but name
// nothing of the installation's: the placeholder owner, the role-desk of the
// supervisor, and the supervisor itself, which is a job and not a person.
// They are the same in every installation and the job descriptions use them
// as ordinary words.
//
// The FOCUS charge categories are here as well: the generated estate has a
// service called "Tax", and an allocation rule is for the category of the same
// name. The word is the specification's and not the installation's.
var NotIdentifiers = map[string]bool{
	"supervisor": true, "unclaimed": true, "management": true, "owner": true,
	"Usage": true, "Purchase": true, "Tax": true, "Credit": true, "Adjustment": true,
}

// Value is one string found in the store and where it was found.
type Value struct {
	Text   string
	Origin string // table.column
}

// UnclassifiedColumns lists every text column of every table that Classes
// does not mention.
func UnclassifiedColumns(db *sql.DB) ([]string, error) {
	var out []string
	err := walkColumns(db, func(table, column string) error {
		if _, ok := Classes[table+"."+column]; !ok {
			out = append(out, table+"."+column)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// Of returns every distinct non-empty (value, column) pair of class c. A value
// found in two columns comes back twice, once per origin, so a caller can say
// where it was found; use Distinct for the values alone.
func Of(db *sql.DB, c Class) ([]Value, error) {
	var out []Value
	err := walkColumns(db, func(table, column string) error {
		if Classes[table+"."+column] != c {
			return nil
		}
		rows, err := db.Query(fmt.Sprintf(`SELECT DISTINCT %q FROM %q WHERE %q IS NOT NULL AND %q <> ''`,
			column, table, column, column))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v sql.NullString
			if err := rows.Scan(&v); err != nil {
				return err
			}
			if !v.Valid || v.String == "" {
				continue
			}
			if c == ID && NotIdentifiers[v.String] {
				continue
			}
			out = append(out, Value{v.String, table + "." + column})
		}
		return rows.Err()
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Text != out[j].Text {
			return out[i].Text < out[j].Text
		}
		return out[i].Origin < out[j].Origin
	})
	return out, err
}

// Distinct is the values of vs once each, in order.
func Distinct(vs []Value) []string {
	var out []string
	for i, v := range vs {
		if i > 0 && vs[i-1].Text == v.Text {
			continue
		}
		out = append(out, v.Text)
	}
	return out
}

// walkColumns visits every column whose declared type is TEXT (or has none),
// in every table.
func walkColumns(db *sql.DB, visit func(table, column string) error) error {
	tables, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return err
	}
	var names []string
	for tables.Next() {
		var n string
		if err := tables.Scan(&n); err != nil {
			tables.Close()
			return err
		}
		names = append(names, n)
	}
	tables.Close()
	for _, t := range names {
		cols, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%q)`, t))
		if err != nil {
			return err
		}
		type col struct{ name, typ string }
		var cs []col
		for cols.Next() {
			var cid, notnull, pk int
			var name, typ string
			var dflt sql.NullString
			if err := cols.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				cols.Close()
				return err
			}
			cs = append(cs, col{name, strings.ToUpper(typ)})
		}
		cols.Close()
		for _, c := range cs {
			if c.typ == "TEXT" || c.typ == "" {
				if err := visit(t, c.name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ------------------------------------------------------------ the checker

// Checker finds what must not be in a prompt: every identifier the schema walk
// found, the free text, the markers and the secrets.
type Checker struct {
	ids     []string
	idsAt   map[string]string
	free    []string
	secrets []string
}

// NewChecker enumerates db.
func NewChecker(db *sql.DB) (*Checker, error) {
	c := &Checker{idsAt: map[string]string{}, secrets: []string{SecretHash, SecretToken}}
	vals, err := Of(db, ID)
	if err != nil {
		return nil, err
	}
	for _, v := range vals {
		if _, dup := c.idsAt[v.Text]; !dup {
			c.ids = append(c.ids, v.Text)
			c.idsAt[v.Text] = v.Origin
		}
	}
	free, err := Of(db, Free)
	if err != nil {
		return nil, err
	}
	for _, v := range Distinct(free) {
		if len(v) >= 24 {
			c.free = append(c.free, v)
		}
	}
	return c, nil
}

// Identifiers is how many distinct identifiers it looks for.
func (c *Checker) Identifiers() int { return len(c.ids) }

// FreeTextMarkers are the strings planted in typed text.
var FreeTextMarkers = []string{PastBodyMarker, RefusalMarker, SummaryMarker, DriverMarker}

// Leaks names every identifier, free-text value, marker and secret still in
// text, ignoring the working analyst's own name (own), which a restricting
// mode leaves on purpose.
func (c *Checker) Leaks(text, own string) []string {
	if own != "" {
		text = strings.ReplaceAll(text, own, "")
	}
	var out []string
	for _, id := range c.ids {
		if len(id) < 2 {
			continue
		}
		if Holds(text, id) {
			out = append(out, fmt.Sprintf("identifier %q (%s)", id, c.idsAt[id]))
		}
	}
	for _, f := range c.free {
		if strings.Contains(text, f[:24]) {
			out = append(out, fmt.Sprintf("free text %q...", f[:24]))
		}
	}
	for _, m := range append([]string{OperatorGoalText}, FreeTextMarkers...) {
		if strings.Contains(text, m) {
			out = append(out, "free-text marker "+m)
		}
	}
	for _, s := range c.secrets {
		if strings.Contains(text, s) {
			out = append(out, "secret "+s)
		}
	}
	return out
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// Holds reports whether text contains id as a whole word (neither neighbour a
// letter or a digit), or, for an identifier long enough that it cannot be an
// accident of a longer word, as a plain substring. This is deliberately not
// the masker's own boundary rule written a second time: it is the stricter
// reading ("is the name anywhere in the text a person could see it"), and
// where the two differ the gate is the one that fails.
func Holds(text, id string) bool {
	if len(id) >= 6 && strings.Contains(text, id) {
		return true
	}
	for from := 0; from < len(text); {
		i := strings.Index(text[from:], id)
		if i < 0 {
			return false
		}
		s, e := from+i, from+i+len(id)
		okBefore, okAfter := true, true
		if s > 0 {
			r, _ := utf8.DecodeLastRuneInString(text[:s])
			okBefore = !isWordRune(r)
		}
		if e < len(text) {
			r, _ := utf8.DecodeRuneInString(text[e:])
			okAfter = !isWordRune(r)
		}
		if okBefore && okAfter {
			return true
		}
		from = s + 1
	}
	return false
}

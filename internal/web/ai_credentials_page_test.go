package web_test

// Invariant 90: /ai, the AI desk's anomaly pages and the KPI library read a
// credential as a credential, say why the gateway blocked a call, point at the
// gateway connector rather than saying no gateway reader exists, and never
// link a gateway's agent id to a card this console does not have.

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// importExportsWeb copies the named fixtures (internal/connectors/testdata)
// into one folder, edited by edit when given, and imports it through the
// connector's own form.
func importExportsWeb(t *testing.T, admin *harness, edit func(name, body string) string, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join("..", "connectors", "testdata", n))
		if err != nil {
			t.Fatal(err)
		}
		body := string(data)
		if edit != nil {
			body = edit(n, body)
		}
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	code, loc := admin.post(t, "/connectors/tokenfuse-focus/save", url.Values{
		"path": {dir}, "csrf": {admin.csrf(t, "/connectors/tokenfuse-focus")},
	})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("saving the path: %d %s", code, loc)
	}
	code, loc = admin.post(t, "/connectors/tokenfuse-focus/import", url.Values{
		"csrf": {admin.csrf(t, "/connectors/tokenfuse-focus")}, "replace-generated": {"yes"},
	})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("importing: %d %s", code, loc)
	}
}

// agentRow is the By agent table's row for one agent, so an assertion about
// it cannot be satisfied by another row's markup.
func agentRow(t *testing.T, body, agent string) string {
	t.Helper()
	i := strings.Index(body, "<td>"+agent)
	if i < 0 {
		t.Fatalf("no By agent row for %s", agent)
	}
	j := strings.Index(body[i:], "</tr>")
	return body[i : i+j]
}

func TestTheAIPageNamesACredentialAndTheReasonForEachBlock(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")
	importExportsWeb(t, admin, nil, "tokenfuse-focus-1.7.0-2026-10-07.csv")

	_, body, _ := admin.get(t, "/ai")
	key := agentRow(t, body, "key:imposter")
	for _, want := range []string{"credential, not an agent", "1 identity_mismatch",
		"refused for identity: never forwarded, nothing reserved or spent"} {
		if !strings.Contains(key, want) {
			t.Errorf("key:imposter's row does not say %q", want)
		}
	}
	if strings.Contains(key, "reserved amount not carried") {
		t.Error("key:imposter's row says a reserved amount is missing for a call refused for identity")
	}
	flint := agentRow(t, body, "agent://acme.example/finops/flint")
	if strings.Contains(flint, "credential, not an agent") {
		t.Error("an agent's row is labelled a credential")
	}
	if !strings.Contains(flint, "1 budget_exceeded") || !strings.Contains(flint, "reserved amount not carried in this export") {
		t.Error("flint's budget refusal does not name its reason and the missing reservation")
	}
	for _, want := range []string{"<h2>Blocked, by the gateway\u0027s reason</h2>", "<code>budget_exceeded</code>",
		"<code>identity_mismatch</code>", "filed under the credential, not the agent it claimed",
		"filed under the credential that sent it"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /ai does not contain %q", want)
		}
	}
}

func TestTheAIPageNotesAMonthMixingExportsFromBeforeAndAfter170(t *testing.T) {
	const note = "This month mixes"
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")
	importExportsWeb(t, admin, nil, "tokenfuse-focus-1.7.0-2026-10-07.csv")
	if _, body, _ := admin.get(t, "/ai"); strings.Contains(body, note) {
		t.Fatal("one 1.7.0 export alone is noted as a mix")
	}

	// The older export, moved an hour later, is four OTHER calls: nothing
	// supersedes anything, and the month holds rows from both exports.
	h2 := start(t)
	h2.signUp(t, "boss", "boss-password-2026")
	admin2 := h2.as(t, "boss", "boss-password-2026")
	importExportsWeb(t, admin2, func(name, body string) string {
		if strings.Contains(name, "before") {
			return strings.ReplaceAll(body, "T10:", "T11:")
		}
		return body
	}, "tokenfuse-focus-1.7.0-2026-10-07.csv", "tokenfuse-focus-before-1.7.0-2026-10-07.csv")
	_, body, _ := admin2.get(t, "/ai")
	for _, want := range []string{note + " 4 calls from an export written before TokenFuse 1.7.0 with 4 from 1.7.0 or later",
		"keeps its older settlement"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /ai does not contain %q", want)
		}
	}
}

func TestNothingSaysNoGatewayReaderExists(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")
	stale := regexp.MustCompile(`carry an agent header|do not carry an agent header`)
	const link = `href="/connectors/tokenfuse-focus"`

	_, ai, _ := admin.get(t, "/ai")
	if stale.MatchString(ai) || !strings.Contains(ai, link) {
		t.Error("/ai, before any import, says no gateway reader exists or does not point at the connector")
	}
	_, kpis, _ := admin.get(t, "/kpis")
	if stale.MatchString(kpis) || !strings.Contains(kpis, "/connectors/tokenfuse-focus") {
		t.Error("/kpis' agent-attribution refusal says no gateway reader exists or does not name the connector")
	}

	plantAnomalyForTelling(t, h, "A-AI-TEAM", "ml-platform", 2)
	if _, err := h.st.DB().Exec(`UPDATE anomalies SET source='ai', service='Anthropic API',
		caused_by='ml-platform', caused_by_kind='team' WHERE id='A-AI-TEAM'`); err != nil {
		t.Fatal(err)
	}
	_, page, _ := admin.get(t, "/anomalies/A-AI-TEAM")
	if stale.MatchString(page) || !strings.Contains(page, link) {
		t.Error("a team-grain AI anomaly says no gateway reader exists or does not point at the connector")
	}
	plantAnomalyForTelling(t, h, "A-AWS-TEAM", "ml-platform", 2)
	if _, err := h.st.DB().Exec(`UPDATE anomalies SET caused_by='ml-platform', caused_by_kind='team'
		WHERE id='A-AWS-TEAM'`); err != nil {
		t.Fatal(err)
	}
	_, page, _ = admin.get(t, "/anomalies/A-AWS-TEAM")
	if stale.MatchString(page) || strings.Contains(page, link) || !strings.Contains(page, "never the agent that ran it") {
		t.Error("a cloud desk's team-grain anomaly points at the AI gateway connector, or says no reader exists")
	}
}

func TestACausedByAgentOffTheRosterIsNotLinkedToAStaffCard(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")
	const gateway = "agent://acme.example/finops/flint"
	plantAnomalyForTelling(t, h, "A-GW", "", 2)
	if _, err := h.st.DB().Exec(`UPDATE anomalies SET source='ai', caused_by=?, caused_by_kind='agent'
		WHERE id='A-GW'`, gateway); err != nil {
		t.Fatal(err)
	}
	var analyst string
	if err := h.st.DB().QueryRow(`SELECT name FROM analysts ORDER BY name LIMIT 1`).Scan(&analyst); err != nil {
		t.Fatal(err)
	}
	plantAnomalyForTelling(t, h, "A-CREW", "", 2)
	if _, err := h.st.DB().Exec(`UPDATE anomalies SET caused_by=?, caused_by_kind='agent'
		WHERE id='A-CREW'`, analyst); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := admin.get(t, "/staff/"+url.PathEscape(gateway)); status == 200 {
		t.Fatalf("sanity: /staff answers for a gateway agent id")
	}
	for _, p := range []string{"/anomalies", "/anomalies/A-GW"} {
		_, body, _ := admin.get(t, p)
		if strings.Contains(body, `href="/staff/`+gateway) {
			t.Errorf("%s links the gateway's agent id to a /staff card that does not exist", p)
		}
		if p == "/anomalies/A-GW" && !strings.Contains(body, gateway) {
			t.Errorf("%s no longer names the agent at all", p)
		}
	}
	for _, p := range []string{"/anomalies", "/anomalies/A-CREW"} {
		_, body, _ := admin.get(t, p)
		if !strings.Contains(body, `href="/staff/`+analyst+`"`) {
			t.Errorf("%s no longer links a roster analyst's own card", p)
		}
	}
}

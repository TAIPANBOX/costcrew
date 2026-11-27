package web_test

// Three routes nothing drove end to end: the general-ledger export of a
// CLOSED period, the analyst's passport document, and the supervisor's pass
// started from the sprint page.

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/TAIPANBOX/agent-stack-go/v2/passport"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/stack"
	"github.com/TAIPANBOX/costcrew/internal/web"
)

func TestTheLedgerExportCarriesOnlyAClosedPeriod(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	months, err := finops.Months(h.st.DB())
	if err != nil || len(months) < 2 {
		t.Fatalf("months %v, %v", months, err)
	}
	closed, open := months[1], months[0]
	if err := finops.Close(h.st.DB(), closed, "y.mercer"); err != nil {
		t.Fatal(err)
	}

	code, body, _ := h.get(t, "/export/gl.csv?period="+open)
	if code != http.StatusOK || !strings.Contains(body, "not closed: nothing to post to a ledger") {
		t.Errorf("an open period's ledger file: %d %q", code, body)
	}

	code, body, _ = h.get(t, "/export/gl.csv?period="+closed)
	if code != http.StatusOK {
		t.Fatalf("GET /export/gl.csv: %d", code)
	}
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("the ledger file is not CSV: %v", err)
	}
	if got := strings.Join(rows[0], ","); got != "period,source,cost_centre,amount_usd,frozen_at,closed_by" {
		t.Fatalf("header %q", got)
	}
	frozen, err := finops.FrozenPeriod(h.st.DB(), closed)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows)-1 != len(frozen.Teams) || len(frozen.Teams) == 0 {
		t.Fatalf("%d ledger lines for %d frozen teams", len(rows)-1, len(frozen.Teams))
	}
	for i, r := range rows[1:] {
		want := frozen.Teams[i]
		if r[0] != closed || r[1] != want.Source || r[2] != want.Team ||
			r[3] != want.Loaded().String() || r[5] != "y.mercer" || r[4] == "" {
			t.Errorf("line %d: %v, want %s %s %s %s closed by y.mercer", i+1, r,
				closed, want.Source, want.Team, want.Loaded())
		}
	}
}

func TestThePassportDocumentIsServedAsPublished(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	// Switched off: the route says so rather than inventing a document.
	code, body, _ := h.get(t, "/staff/triage-aws/passport.json")
	if code != http.StatusNotFound || !strings.Contains(body, "governance plane is switched off") {
		t.Errorf("passport with the plane off: %d %q", code, body)
	}

	em, err := stack.Open(stack.Config{EventsPath: filepath.Join(t.TempDir(), "events.ndjson"), Host: "costcrew.test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { em.Close() })
	srv := httptest.NewServer(web.New(h.st, h.au, web.Stack{Host: "costcrew.test", PassportFor: em.PassportFor}))
	t.Cleanup(srv.Close)
	on := (&harness{srv: srv, au: h.au, st: h.st}).as(t, "owner", "owner-password-2026")

	resp, err := on.c.Get(srv.URL + "/staff/triage-aws/passport.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("passport: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var got passport.Passport
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("the passport is not JSON: %v", err)
	}
	a, err := crew.GetAnalyst(h.st.DB(), "triage-aws")
	if err != nil {
		t.Fatal(err)
	}
	if want := em.PassportFor(a); got.ID != want.ID || got.Owner != want.Owner || got.ID == "" {
		t.Errorf("served %q owned by %q, the writer publishes %q owned by %q", got.ID, got.Owner, want.ID, want.Owner)
	}

	code, body, _ = on.get(t, "/staff/nobody-here/passport.json")
	if code != http.StatusNotFound || !strings.Contains(body, "no such analyst") {
		t.Errorf("an unknown analyst's passport: %d %q", code, body)
	}
}

func TestTheSupervisorsPassRunsFromTheSprintPage(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	var id int
	if err := h.st.DB().QueryRow(`SELECT id FROM sprints ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	back := "/sprint/" + strconv.Itoa(id)
	code, loc := h.post(t, back+"/supervise", url.Values{"csrf": {h.csrf(t, back)}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, back+"?msg=") {
		t.Fatalf("POST supervise: %d %s", code, loc)
	}
	msg, _ := url.QueryUnescape(strings.TrimPrefix(loc, back+"?msg="))
	if !strings.Contains(msg, " applied, ") || !strings.Contains(msg, " carried, ") ||
		!strings.Contains(msg, "decision request(s) written") {
		t.Errorf("the pass reported %q, want what it applied, carried and wrote", msg)
	}

	if code, _ := h.post(t, "/sprint/not-a-number/supervise", url.Values{"csrf": {h.csrf(t, back)}}); code != http.StatusNotFound {
		t.Errorf("a sprint id that is not a number: %d, want 404", code)
	}
}

package web_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/connectors"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/detect"
	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/web"
)

// These are the tests the unit suites cannot stand in for. A rule enforced in
// a function is not enforced in the product until the request that reaches it
// is refused, and between the two sit routing, the session, the CSRF check and
// the role gate, each of which can quietly let something through.

type harness struct {
	srv *httptest.Server
	au  *auth.Auth
	st  *store.Store
	c   *http.Client
}

func start(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	if _, err := estate.Seed(st.DB()); err != nil {
		t.Fatal(err)
	}
	if err := estate.SeedBudgets(st.DB()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := anomaly.Run(st.DB(), time.Now(), detect.Default(), nil); err != nil {
		t.Fatal(err)
	}
	// The harness seeds what production seeds. A test store that is missing a
	// plane the real one always has does not test the real one.
	var seeds []crew.AnomalySeed
	list, err := anomaly.List(st.DB(), anomaly.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		seeds = append(seeds, crew.AnomalySeed{
			ID: a.ID, Source: a.Source, Service: a.Service,
			Day: a.Day, Direction: a.Direction, Excess: a.Excess,
		})
	}
	if _, _, _, err := crew.Seed(st.DB(), seeds); err != nil {
		t.Fatal(err)
	}
	if err := finops.SeedRules(st.DB()); err != nil {
		t.Fatal(err)
	}
	au, err := auth.New(st, dir)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(web.New(st, au, nil, "costcrew.test", ""))
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	return &harness{srv: srv, au: au, st: st, c: &http.Client{
		Jar: jar,
		// The route's own answer is the thing under test, so a redirect is
		// reported rather than followed.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (h *harness) get(t *testing.T, path string) (int, string, string) {
	t.Helper()
	resp, err := h.c.Get(h.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body), resp.Header.Get("Location")
}

func (h *harness) csrf(t *testing.T, path string) string {
	t.Helper()
	_, body, _ := h.get(t, path)
	if m := csrfRe.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

func (h *harness) post(t *testing.T, path string, form url.Values) (int, string) {
	t.Helper()
	resp, err := h.c.PostForm(h.srv.URL+path, form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Location")
}

func (h *harness) signUp(t *testing.T, user, pw string) {
	t.Helper()
	code, loc := h.post(t, "/signup", url.Values{
		"username": {user}, "password": {pw}, "csrf": {h.csrf(t, "/signup")},
	})
	if code != http.StatusSeeOther || strings.Contains(loc, "msg=") {
		t.Fatalf("signup failed: %d %s", code, loc)
	}
}

func (h *harness) anyAnomaly(t *testing.T) anomaly.Anomaly {
	t.Helper()
	list, err := anomaly.List(h.st.DB(), anomaly.Filter{State: anomaly.Open})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 {
		t.Fatal("the seeded estate produced no open anomalies")
	}
	return list[0]
}

// ------------------------------------------------------------------- access

func TestEveryPageRefusesAStranger(t *testing.T) {
	h := start(t)
	// Claim the installation first. Where a stranger is SENT depends on
	// whether anybody owns this console yet, and the thing under test here is
	// that they are refused, not which door they are shown.
	h.signUp(t, "owner", "owner-password-2026")

	stranger := &harness{srv: h.srv, au: h.au, st: h.st, c: &http.Client{
		Jar: newJar(t),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
	for _, path := range []string{"/", "/anomalies", "/budgets", "/staff", "/board", "/sprints",
		"/allocation", "/chargeback", "/results",
		"/connectors", "/engines", "/accounts", "/audit",
		"/kpis", "/utilisation", "/saas", "/ai"} {
		code, _, loc := stranger.get(t, path)
		if code != http.StatusSeeOther || loc != "/login" {
			t.Errorf("GET %s without a session: %d to %q, want 303 to /login", path, code, loc)
		}
	}
	// The anomaly page too: an id is not a credential.
	code, _, loc := stranger.get(t, "/anomalies/A-anything")
	if code != http.StatusSeeOther || loc != "/login" {
		t.Errorf("GET an anomaly without a session: %d to %q", code, loc)
	}
}

func TestSignedInPagesRender(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	for _, tc := range []struct{ path, wants string }{
		{"/", "Overview"},
		{"/anomalies", "Anomalies"},
		{"/budgets", "Budgets"},
		{"/staff", "Crew"},
		{"/board", "Board"},
		{"/sprints", "Sprints"},
		{"/allocation", "Allocation"},
		{"/chargeback", "Chargeback"},
		{"/results", "Results"},
		{"/connectors", "Connectors"},
		{"/engines", "Engines"},
		{"/accounts", "Accounts"},
		{"/audit", "Audit"},
		{"/kpis", "KPIs"},
		{"/utilisation", "Utilisation"},
		{"/saas", "SaaS"},
		{"/ai", "AI spend"},
	} {
		code, body, _ := h.get(t, tc.path)
		if code != http.StatusOK {
			t.Errorf("GET %s: %d", tc.path, code)
			continue
		}
		if !strings.Contains(body, tc.wants) {
			t.Errorf("GET %s does not mention %q", tc.path, tc.wants)
		}
	}
}

// The list has to actually show the estate's findings, not an empty table with
// the right headings.
func TestTheAnomalyListShowsRealFindings(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	a := h.anyAnomaly(t)

	_, body, _ := h.get(t, "/anomalies")
	for _, want := range []string{a.ID, a.Service, a.Excess.String()} {
		if !strings.Contains(body, want) {
			t.Errorf("the anomaly list does not contain %q", want)
		}
	}
	// The grain is on the page, not only in the database.
	if !strings.Contains(body, "grain") && !strings.Contains(body, a.CausedByKind) {
		t.Error("the list does not say what grain caused-by is at")
	}
}

func TestTheAnomalyPageShowsItsRuleAndGrain(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	a := h.anyAnomaly(t)

	code, body, _ := h.get(t, "/anomalies/"+a.ID)
	if code != http.StatusOK {
		t.Fatalf("GET the anomaly page: %d", code)
	}
	for _, want := range []string{
		a.Amount.String(),   // what it was
		a.Baseline.String(), // what was expected
		"robust deviations", // the rule, in words
		"grain:",            // and how far the attribution reaches
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the anomaly page does not contain %q", want)
		}
	}
}

func TestAnUnknownAnomalyIsNotFound(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	code, _, _ := h.get(t, "/anomalies/A-000000000000")
	if code != http.StatusNotFound {
		t.Errorf("an unknown anomaly returned %d, want 404", code)
	}
}

// -------------------------------------------------------------- the actions

// The rule the unit tests prove in a function, proved here through the form,
// which is where somebody actually skips it.
func TestDismissingThroughTheFormNeedsAReason(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	a := h.anyAnomaly(t)
	path := "/anomalies/" + a.ID

	for _, blank := range []string{"", "   "} {
		code, loc := h.post(t, path+"/dismiss", url.Values{
			"reason": {blank}, "csrf": {h.csrf(t, path)},
		})
		if code != http.StatusSeeOther {
			t.Fatalf("dismiss returned %d", code)
		}
		if !strings.Contains(loc, "msg=") {
			t.Errorf("dismiss with %q was accepted silently", blank)
		}
		after, err := anomaly.Get(h.st.DB(), a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.State != anomaly.Open {
			t.Fatalf("a reasonless dismissal moved the anomaly to %q", after.State)
		}
	}
}

func TestDismissingWithAReasonWorksAndSticks(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	a := h.anyAnomaly(t)
	path := "/anomalies/" + a.ID
	const why = "Load test agreed with the platform team on the 12th"

	code, loc := h.post(t, path+"/dismiss", url.Values{
		"reason": {why}, "csrf": {h.csrf(t, path)},
	})
	if code != http.StatusSeeOther || strings.Contains(loc, "msg=") {
		t.Fatalf("dismiss refused: %d %s", code, loc)
	}
	after, err := anomaly.Get(h.st.DB(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != anomaly.Dismissed || after.Reason != why {
		t.Fatalf("after dismissal: state %q, reason %q", after.State, after.Reason)
	}

	// A closed anomaly offers no further decisions on the page itself, not
	// merely refuses them on submit.
	_, body, _ := h.get(t, path)
	if strings.Contains(body, `action="`+path+`/dismiss"`) {
		t.Error("the page still offers a dismiss form on a closed anomaly")
	}
	if !strings.Contains(body, "Closed") {
		t.Error("the page does not say the anomaly is closed")
	}
}

func TestAssignThenExplainMovesItThroughTheStates(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	a := h.anyAnomaly(t)
	path := "/anomalies/" + a.ID

	if _, loc := h.post(t, path+"/assign", url.Values{
		"analyst": {"triage-aws"}, "csrf": {h.csrf(t, path)},
	}); strings.Contains(loc, "msg=") {
		t.Fatalf("assign refused: %s", loc)
	}
	got, _ := anomaly.Get(h.st.DB(), a.ID)
	if got.State != anomaly.Triaged || got.HandledBy != "triage-aws" {
		t.Fatalf("after assign: %q, handled by %q", got.State, got.HandledBy)
	}

	if _, loc := h.post(t, path+"/explain", url.Values{
		"reason": {"Retry loop in the batch job; the fix shipped on the 13th"},
		"csrf":   {h.csrf(t, path)},
	}); strings.Contains(loc, "msg=") {
		t.Fatalf("explain refused: %s", loc)
	}
	got, _ = anomaly.Get(h.st.DB(), a.ID)
	if got.State != anomaly.Explained {
		t.Fatalf("after explain: %q", got.State)
	}
	// Assigning does not overwrite the owner with an explanation.
	if got.HandledBy != "triage-aws" {
		t.Errorf("the owner was lost during explain: %q", got.HandledBy)
	}
}

// --------------------------------------------------------------- the gates

func TestAnActionWithoutACSRFTokenIsRefused(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	a := h.anyAnomaly(t)
	path := "/anomalies/" + a.ID

	for _, token := range []string{"", "not-the-token"} {
		_, loc := h.post(t, path+"/dismiss", url.Values{
			"reason": {"a perfectly good reason"}, "csrf": {token},
		})
		if !strings.Contains(loc, "msg=") {
			t.Errorf("a dismissal with csrf %q was accepted", token)
		}
	}
	got, _ := anomaly.Get(h.st.DB(), a.ID)
	if got.State != anomaly.Open {
		t.Fatalf("a cross-site request changed the anomaly to %q", got.State)
	}
}

func TestAStrangerCannotAct(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	a := h.anyAnomaly(t)
	token := h.csrf(t, "/anomalies/"+a.ID)

	// A fresh client with no session, carrying a token it should not have.
	jar, _ := cookiejar.New(nil)
	stranger := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := stranger.PostForm(h.srv.URL+"/anomalies/"+a.ID+"/dismiss",
		url.Values{"reason": {"not mine to close"}, "csrf": {token}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("a session-less action went to %q, want /login", loc)
	}
	got, _ := anomaly.Get(h.st.DB(), a.ID)
	if got.State != anomaly.Open {
		t.Fatalf("a session-less request changed the anomaly to %q", got.State)
	}
}

// The line between reading and acting is drawn where money and state are, and
// a viewer is on the other side of it.
func TestAViewerMayReadButNotAct(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	a := h.anyAnomaly(t)

	if ok, err := h.au.Create("watcher", "watcher-password-2026", "viewer"); err != nil || !ok {
		t.Fatalf("creating a viewer: %v %v", ok, err)
	}
	jar, _ := cookiejar.New(nil)
	viewer := &harness{srv: h.srv, au: h.au, st: h.st, c: &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
	if code, loc := viewer.post(t, "/login", url.Values{
		"username": {"watcher"}, "password": {"watcher-password-2026"},
		"csrf": {viewer.csrf(t, "/login")},
	}); code != http.StatusSeeOther || strings.Contains(loc, "msg=") {
		t.Fatalf("the viewer could not sign in: %d %s", code, loc)
	}

	if code, _, _ := viewer.get(t, "/anomalies"); code != http.StatusOK {
		t.Errorf("the viewer cannot read the anomaly list: %d", code)
	}

	path := "/anomalies/" + a.ID
	_, loc := viewer.post(t, path+"/dismiss", url.Values{
		"reason": {"looks fine to me"}, "csrf": {viewer.csrf(t, path)},
	})
	if !strings.Contains(loc, "msg=") {
		t.Error("a viewer's dismissal was accepted")
	}
	got, _ := anomaly.Get(h.st.DB(), a.ID)
	if got.State != anomaly.Open {
		t.Fatalf("a viewer moved the anomaly to %q", got.State)
	}
}

// ------------------------------------------------------------------ filters

func TestTheStateFilterNarrowsTheList(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	a := h.anyAnomaly(t)
	path := "/anomalies/" + a.ID

	if _, loc := h.post(t, path+"/dismiss", url.Values{
		"reason": {"Duplicate of the incident on the 12th"}, "csrf": {h.csrf(t, path)},
	}); strings.Contains(loc, "msg=") {
		t.Fatalf("dismiss refused: %s", loc)
	}

	_, open, _ := h.get(t, "/anomalies?state=open")
	if strings.Contains(open, a.ID) {
		t.Error("a dismissed anomaly still appears under the open filter")
	}
	_, dismissed, _ := h.get(t, "/anomalies?state=dismissed")
	if !strings.Contains(dismissed, a.ID) {
		t.Error("the dismissed anomaly does not appear under the dismissed filter")
	}
}

func TestTheCSVExportCarriesTheOpenMonthHonestly(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	code, body, _ := h.get(t, "/export/budget.csv?source=aws")
	if code != http.StatusOK {
		t.Fatalf("the export returned %d", code)
	}
	if !strings.Contains(body, "open, month to date") {
		t.Error("the export does not mark the open month, so a partial total " +
			"reads as a full one and every team looks thrifty")
	}
	if !strings.HasSuffix(strings.SplitN(body, "\n", 2)[0], "\r") {
		t.Error("the export is not CRLF, which some spreadsheets will ask about")
	}
}

// The first run must not be a dead end.
//
// With no account created, a sign-in form is two fields and no credentials
// that could work. This was found by handing the console over and being asked
// what the password was, which is the cheapest kind of test and the one no
// suite here was running.
func TestAnUnclaimedInstallationSendsYouToSignUp(t *testing.T) {
	h := start(t) // no signUp: nobody has claimed it

	for _, path := range []string{"/", "/anomalies", "/budgets", "/staff", "/board", "/sprints",
		"/allocation", "/chargeback", "/results",
		"/connectors", "/engines", "/accounts", "/audit",
		"/kpis", "/utilisation", "/saas", "/ai"} {
		code, _, loc := h.get(t, path)
		if code != http.StatusSeeOther || loc != "/signup" {
			t.Errorf("GET %s on an unclaimed install: %d to %q, want 303 to /signup",
				path, code, loc)
		}
	}
	// And the sign-in page itself refuses to be a dead end.
	code, _, loc := h.get(t, "/login")
	if code != http.StatusSeeOther || loc != "/signup" {
		t.Errorf("GET /login on an unclaimed install: %d to %q, want 303 to /signup", code, loc)
	}
	// The sign-up page is reachable and offers the form.
	code, body, _ := h.get(t, "/signup")
	if code != http.StatusOK || !strings.Contains(body, `name="username"`) {
		t.Errorf("GET /signup: %d, form present: %v", code, strings.Contains(body, `name="username"`))
	}
}

// Once somebody has claimed it, the doors swap over: sign-up closes and the
// entry point becomes sign-in.
func TestOnceClaimedTheEntryPointBecomesSignIn(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	stranger := &harness{srv: h.srv, au: h.au, st: h.st, c: &http.Client{
		Jar: newJar(t),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
	code, _, loc := stranger.get(t, "/")
	if code != http.StatusSeeOther || loc != "/login" {
		t.Errorf("GET / on a claimed install: %d to %q, want 303 to /login", code, loc)
	}
	// Sign-up is closed, so a second person cannot quietly claim admin.
	_, _, loc = stranger.get(t, "/signup")
	if loc != "/login?msg=registration+is+closed" && !strings.HasPrefix(loc, "/login") {
		t.Errorf("GET /signup on a claimed install went to %q", loc)
	}
}

func newJar(t *testing.T) *cookiejar.Jar {
	t.Helper()
	j, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// ------------------------------------------------------------------- ops

// The rule that matters most on the connectors page: a call that costs money
// does not happen because somebody clicked past a screen.
func TestAMeteredConnectorRefusesWithoutAnExplicitYes(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	const metered = "aws-cost-explorer"
	path := "/connectors/" + metered

	_, body, _ := h.get(t, path)
	if !strings.Contains(body, "costs money") {
		t.Error("the page does not say the connector is billed per call")
	}

	// No confirmation: the refusal has to name the cost, not just say no.
	_, loc := h.post(t, path+"/import", url.Values{"csrf": {h.csrf(t, path)}})
	if !strings.Contains(loc, "msg=") {
		t.Fatal("a metered import ran without confirmation")
	}
	if !strings.Contains(loc, "costs+money") && !strings.Contains(loc, "costs%20money") {
		t.Errorf("the refusal does not say why: %s", loc)
	}

	// Configure it, so the next assertion is about the METERING rather than
	// about a missing field: an unconfigured connector correctly says it is
	// unconfigured, which is a different sentence.
	if _, loc := h.post(t, path+"/save", url.Values{
		"profile": {"stack-k8s"}, "csrf": {h.csrf(t, path)},
	}); strings.Contains(loc, "msg=") {
		t.Fatalf("saving the connector config failed: %s", loc)
	}

	// A test must not call it either. Free connectors get tested; billed ones
	// get described.
	if _, loc := h.post(t, path+"/test", url.Values{"csrf": {h.csrf(t, path)}}); strings.Contains(loc, "msg=") {
		t.Errorf("testing a metered connector errored: %s", loc)
	}
	_, body, _ = h.get(t, path)
	if !strings.Contains(body, "NOT called") {
		t.Error("the test result does not say the metered connector was left alone")
	}
}

// A documented connector must say so rather than look finished.
func TestADocumentedConnectorSaysItIsNotBuilt(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	path := "/connectors/opencost"
	_, body, _ := h.get(t, path)
	if !strings.Contains(body, "documented") {
		t.Error("a documented connector does not say so on its own page")
	}
	if _, loc := h.post(t, path+"/import", url.Values{"csrf": {h.csrf(t, path)}}); !strings.Contains(loc, "msg=") {
		t.Error("importing from a connector with no reader was accepted")
	}
}

// Every connector answers the two questions that decide whether an
// integration is a good idea, plus the one nobody else prints.
func TestEveryConnectorSaysWhatItCannotDo(t *testing.T) {
	for _, c := range connectors.Catalogue {
		if strings.TrimSpace(c.Feeds) == "" {
			t.Errorf("%s does not say what it fills; a connector that fills nothing is a demo", c.ID)
		}
		if strings.TrimSpace(c.CostNote) == "" {
			t.Errorf("%s does not say what running it costs", c.ID)
		}
		if len(strings.TrimSpace(c.Cannot)) < 25 {
			t.Errorf("%s does not say what it CANNOT do, which is the half that "+
				"stops somebody planning around a limit they find later", c.ID)
		}
	}
}

// A secret must never reach the page, only whether it is set.
func TestASecretIsNeverRendered(t *testing.T) {
	t.Setenv("ANTHROPIC_ADMIN_KEY", "sk-ant-admin-thismustnotappear")
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	_, body, _ := h.get(t, "/connectors/anthropic-usage")
	if strings.Contains(body, "thismustnotappear") {
		t.Fatal("the connector page rendered the secret itself")
	}
	if !strings.Contains(body, "It is set") {
		t.Error("the page does not say whether the credential is present")
	}
}

// Engines are grouped by how you pay, and the dry state is stated rather than
// looking like a broken console.
func TestEnginesExplainThemselves(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	_, body, _ := h.get(t, "/engines")
	for _, want := range []string{
		"A subscription you already have",
		"A key, billed per token",
		"An assistant the organisation already pays for",
		"When to pick it",
		"What it costs",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the engines page does not contain %q", want)
		}
	}
}

// Managing accounts is a HIGHER bar than acting: an operator can spend the
// budget and still not hand somebody else the ability to.
func TestAnOperatorCannotManageAccounts(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	if ok, err := h.au.Create("hands", "hands-password-2026", "operator"); err != nil || !ok {
		t.Fatalf("creating an operator: %v %v", ok, err)
	}
	op := &harness{srv: h.srv, au: h.au, st: h.st, c: &http.Client{
		Jar: newJar(t),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
	if code, loc := op.post(t, "/login", url.Values{
		"username": {"hands"}, "password": {"hands-password-2026"},
		"csrf": {op.csrf(t, "/login")},
	}); code != http.StatusSeeOther || strings.Contains(loc, "msg=") {
		t.Fatalf("the operator could not sign in: %d %s", code, loc)
	}
	_, loc := op.post(t, "/accounts/create", url.Values{
		"username": {"smuggled"}, "password": {"smuggled-password-1"},
		"role": {"admin"}, "csrf": {op.csrf(t, "/accounts")},
	})
	if !strings.Contains(loc, "msg=") {
		t.Error("an operator created an account")
	}
	if u, _ := h.au.Get("smuggled"); u != nil {
		t.Fatal("an operator created an ADMIN account")
	}
}

// An installation with no admin cannot be managed by anybody, and the only
// fix is the database.
func TestTheLastAdminCannotDemoteThemselves(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	_, loc := h.post(t, "/accounts/role", url.Values{
		"username": {"owner"}, "role": {"viewer"}, "csrf": {h.csrf(t, "/accounts")},
	})
	if !strings.Contains(loc, "msg=") {
		t.Fatal("the only admin demoted themselves")
	}
	u, err := h.au.Get("owner")
	if err != nil || u == nil || u.Role != "admin" {
		t.Fatalf("the owner is now %v", u)
	}
}

// The audit page has to say verified or say where it broke. "Broken" with no
// position is a sentence nobody can act on.
func TestTheAuditPageVerifiesTheChain(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	_, body, _ := h.get(t, "/audit")
	if !strings.Contains(body, "verified") {
		t.Error("the audit page does not report the chain as verified")
	}
	if !strings.Contains(body, "user_registered") {
		t.Error("the journal does not contain the registration that just happened")
	}
}

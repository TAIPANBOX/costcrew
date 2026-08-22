package web_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	"github.com/TAIPANBOX/costcrew/internal/history"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/web"
	"github.com/TAIPANBOX/costcrew/internal/world"
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

// start is the console as somebody finds it: seeded, and with a past.
func start(t *testing.T) *harness { return startWith(t, true) }

// startBare is the console on its very first morning, before anything has been
// decided. Two behaviours only exist there: a KPI that refuses because nothing
// has been frozen, and the first freeze of a month. Weakening those tests to
// fit a seeded harness would delete the only coverage that state has.
func startBare(t *testing.T) *harness { return startWith(t, false) }

// startStream is the console wired to a given agent-event stream, so a test
// can put a known line in it and read the page back.
func startStream(t *testing.T, path string) *harness {
	return startFull(t, true, path)
}

func startWith(t *testing.T, withHistory bool) *harness {
	return startFull(t, withHistory, "")
}

func startFull(t *testing.T, withHistory bool, eventsPath string) *harness {
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
	if _, err := crew.SeedRoster(st.DB(), "owner"); err != nil {
		t.Fatal(err)
	}
	// And the past, because production has one. A harness whose anomalies are
	// all open and whose forecast table is empty cannot see a page that only
	// goes wrong once something has been decided.
	if withHistory {
		if _, err := history.Seed(st.DB(), st.AsRecorder()); err != nil {
			t.Fatal(err)
		}
	}
	au, err := auth.New(st, dir)
	if err != nil {
		t.Fatal(err)
	}

	// The chain records the work here as it does in production. A harness
	// with no recorder cannot see whether a decision was written down.
	srv := httptest.NewServer(web.New(st, au, web.Stack{
		Host: "costcrew.test", EventsPath: eventsPath, Recorder: st.AsRecorder(),
	}))
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
		"/kpis", "/utilisation", "/saas", "/ai", "/staff/new",
		"/forecast", "/explainers", "/sprint/plan"} {
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
		{"/forecast", "Forecast"},
		{"/explainers", "Explainers"},
		{"/teams", "Teams"},
		{"/desks", "Desks"},
		{"/team/ml-platform", "ml-platform"},
		{"/desk/aws", "aws"},
		{"/staff/triage-aws", "triage-aws"},
		{"/sprint/plan", "sprint"},
	} {
		code, body, _ := h.get(t, tc.path)
		if code != http.StatusOK {
			t.Errorf("GET %s: %d", tc.path, code)
			continue
		}
		if !strings.Contains(body, tc.wants) {
			t.Errorf("GET %s does not mention %q", tc.path, tc.wants)
		}
		// WHOLE, not merely started.
		//
		// html/template reports a missing field while it is writing, so a page
		// with one bad reference used to go out as a 200 that stopped
		// mid-document, with every figure above the break intact. This test
		// passed against exactly that, because the marker it looked for sat
		// above the line that failed.
		if !strings.HasSuffix(strings.TrimSpace(body), "</html>") {
			t.Errorf("GET %s stopped mid-document: it does not end with </html>", tc.path)
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
		"/kpis", "/utilisation", "/saas", "/ai", "/staff/new",
		"/forecast", "/explainers", "/sprint/plan"} {
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

// ------------------------------------------------------------------ hiring

// The argument this form exists for: hiring an analyst and registering it with
// the governance stack are the SAME act. In the original they were two, and
// the second one mostly did not happen.
func TestHiringAsksForGovernanceOnTheSameForm(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	_, body, _ := h.get(t, "/staff/new")
	for _, want := range []string{
		"agent://", "Acts on behalf of", "Attestation",
		"none is the honest default", "Rights", "Per task",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the hire form does not ask about %q", want)
		}
	}
}

func TestHiringWorksAndTheAnalystAppears(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	form := url.Values{
		"name": {"investigator-oracle"}, "role": {"Investigator (oracle desk)"},
		"mission": {"Watch the Oracle estate."}, "desk": {"aws"},
		"engine": {"openrouter"}, "per_task": {"15.00"}, "monthly": {"90.00"},
		"cadence": {"weekly"}, "audience": {"the desk"},
		"parent": {"supervisor"}, "attestation": {"none"},
		"skills": {"variance-commentary", "anomaly-triage"},
		"rights": {"figures-read", "propose-only"},
		"csrf":   {h.csrf(t, "/staff/new")},
	}
	if _, loc := h.post(t, "/staff/create", form); strings.Contains(loc, "msg=") {
		t.Fatalf("hiring was refused: %s", loc)
	}
	a, err := crew.GetAnalyst(h.st.DB(), "investigator-oracle")
	if err != nil {
		t.Fatalf("the analyst was not stored: %v", err)
	}
	if a.Owner != "owner" || a.Parent != "supervisor" || a.Attestation != "none" {
		t.Errorf("governance was not recorded: owner %q, parent %q, attestation %q",
			a.Owner, a.Parent, a.Attestation)
	}
	if len(a.Rights) != 2 || len(a.Skills) != 2 {
		t.Errorf("rights %v, skills %v", a.Rights, a.Skills)
	}
	// It has to be assignable immediately: somebody hired this morning appears
	// in this morning's menu, not after a restart.
	_, body, _ := h.get(t, "/anomalies/"+h.anyAnomaly(t).ID)
	if !strings.Contains(body, "investigator-oracle") {
		t.Error("a newly hired analyst does not appear in the assign menu")
	}
}

// A name becomes part of an agent:// URI, which other services parse.
func TestAnUnusableNameIsRefused(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	for _, bad := range []string{"Reporter (AWS desk)", "ab", "has spaces", "UPPER"} {
		form := url.Values{
			"name": {bad}, "role": {"x"}, "desk": {"aws"}, "engine": {"openrouter"},
			"per_task": {"15.00"}, "monthly": {"90.00"}, "cadence": {"weekly"},
			"attestation": {"none"}, "csrf": {h.csrf(t, "/staff/new")},
		}
		if _, loc := h.post(t, "/staff/create", form); !strings.Contains(loc, "msg=") {
			t.Errorf("the name %q was accepted into an agent:// identity", bad)
		}
	}
}

// An analyst with no ceiling is one nothing can stop except somebody noticing,
// and a per-task guard above the monthly one is a ceiling that can never bite.
func TestTheGuardsMustBeCoherent(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	cases := []struct{ perTask, monthly, why string }{
		{"0.00", "90.00", "no per-task ceiling"},
		{"15.00", "0.00", "no monthly ceiling"},
		{"200.00", "90.00", "per-task above monthly"},
	}
	for _, c := range cases {
		form := url.Values{
			"name": {"guard-test"}, "role": {"x"}, "desk": {"aws"},
			"engine": {"openrouter"}, "per_task": {c.perTask}, "monthly": {c.monthly},
			"cadence": {"weekly"}, "attestation": {"none"},
			"csrf": {h.csrf(t, "/staff/new")},
		}
		if _, loc := h.post(t, "/staff/create", form); !strings.Contains(loc, "msg=") {
			t.Errorf("%s was accepted", c.why)
		}
	}
}

// Taking somebody off the rota is a decision, and a decision with no reason
// cannot be told from an oversight.
func TestSuspendingNeedsAReasonAndDoesNotUndoAnything(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	before, err := crew.Tasks(h.st.DB(), crew.TaskFilter{Assignee: "triage-aws"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/staff/triage-aws/state"

	if _, loc := h.post(t, path, url.Values{
		"state": {"suspended"}, "reason": {"   "}, "csrf": {h.csrf(t, "/staff/triage-aws/edit")},
	}); !strings.Contains(loc, "msg=") {
		t.Fatal("an analyst was suspended with no reason")
	}
	a, _ := crew.GetAnalyst(h.st.DB(), "triage-aws")
	if a.State != "active" {
		t.Fatalf("a reasonless suspension went through: %q", a.State)
	}

	const why = "Tagging feed has been stale since the 9th; paused until it returns"
	if _, loc := h.post(t, path, url.Values{
		"state": {"suspended"}, "reason": {why}, "csrf": {h.csrf(t, "/staff/triage-aws/edit")},
	}); strings.Contains(loc, "msg=") {
		t.Fatalf("suspension with a reason was refused: %s", loc)
	}
	a, _ = crew.GetAnalyst(h.st.DB(), "triage-aws")
	if a.State != "suspended" || a.Reason != why {
		t.Fatalf("state %q, reason %q", a.State, a.Reason)
	}

	// A pause, never an undo: nothing it already did is touched.
	after, _ := crew.Tasks(h.st.DB(), crew.TaskFilter{Assignee: "triage-aws"})
	if len(after) != len(before) {
		t.Fatalf("suspension changed the analyst's work: %d tasks then %d",
			len(before), len(after))
	}
	// And it leaves the rota.
	for _, n := range mustActive(t, h) {
		if n == "triage-aws" {
			t.Fatal("a suspended analyst is still assignable")
		}
	}
}

func mustActive(t *testing.T, h *harness) []string {
	t.Helper()
	names, err := crew.ActiveNames(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestAViewerCannotHire(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	if ok, err := h.au.Create("reader", "reader-password-2026", "viewer"); err != nil || !ok {
		t.Fatalf("creating a viewer: %v %v", ok, err)
	}
	v := &harness{srv: h.srv, au: h.au, st: h.st, c: &http.Client{
		Jar: newJar(t),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
	if code, _ := v.post(t, "/login", url.Values{
		"username": {"reader"}, "password": {"reader-password-2026"},
		"csrf": {v.csrf(t, "/login")},
	}); code != http.StatusSeeOther {
		t.Fatalf("the viewer could not sign in: %d", code)
	}
	if _, _, loc := v.get(t, "/staff/new"); !strings.Contains(loc, "msg=") {
		t.Error("a viewer reached the hire form")
	}
}

// ------------------------------------------------------- forecast, plan, tell

// The loop worth having: a KPI that refuses until the practice does the thing
// it measures, and then reports.
func TestFreezingAForecastTurnsARefusingKPIIntoAReportingOne(t *testing.T) {
	h := startBare(t)
	h.signUp(t, "owner", "owner-password-2026")

	_, body, _ := h.get(t, "/kpis")
	if !strings.Contains(body, "no frozen forecast has reached the end of its month") {
		t.Fatal("forecast accuracy does not begin as a refusal")
	}

	// Freeze a month that has already finished, so it can be scored at once.
	months, err := finops.Months(h.st.DB())
	if err != nil || len(months) < 3 {
		t.Fatalf("months: %v %v", months, err)
	}
	closedMonth := months[2]
	if err := finops.Freeze(h.st.DB(), closedMonth, "owner"); err != nil {
		t.Fatal(err)
	}

	acc, scored, has, err := finops.Accuracy(h.st.DB(), months[0])
	if err != nil {
		t.Fatal(err)
	}
	if !has || scored == 0 {
		t.Fatal("a frozen month that has finished was not scored")
	}
	if acc < 0 || acc > 500 {
		t.Fatalf("an implausible accuracy: %.1f%%", acc)
	}

	_, body, _ = h.get(t, "/kpis")
	if strings.Contains(body, "no frozen forecast has reached the end of its month") {
		t.Error("the KPI still refuses after a month was frozen and scored")
	}
}

// Re-freezing would move a number somebody has already been shown.
func TestAFrozenForecastCannotBeRefrozen(t *testing.T) {
	h := startBare(t)
	h.signUp(t, "owner", "owner-password-2026")
	if _, loc := h.post(t, "/forecast/freeze", url.Values{
		"period": {"2026-08"}, "csrf": {h.csrf(t, "/forecast")},
	}); strings.Contains(loc, "msg=") {
		t.Fatalf("the first freeze was refused: %s", loc)
	}
	if _, loc := h.post(t, "/forecast/freeze", url.Values{
		"period": {"2026-08"}, "csrf": {h.csrf(t, "/forecast")},
	}); !strings.Contains(loc, "msg=") {
		t.Error("a frozen month was frozen again")
	}
}

// A plan is a proposal. Nothing reaches the board until somebody approves it.
func TestPlanningIsAProposalUntilApproved(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	before, err := crew.Sprints(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	_, body, _ := h.get(t, "/sprint/plan")
	if !strings.Contains(body, "Because") {
		t.Error("the plan does not say why each item is proposed")
	}
	// Looking at a plan must not create it.
	after, _ := crew.Sprints(h.st.DB())
	if len(after) != len(before) {
		t.Fatal("opening the planning page created a sprint")
	}
}

// An explainer is written for a team, and nothing written in a team's name
// reaches them until a person has read it.
func TestAnExplainerIsPublishedByAPerson(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	if _, loc := h.post(t, "/explainers/commission", url.Values{
		"team": {"ml-platform"}, "author": {"reporter-aws"},
		"topic": {"Why your EC2 bill moved in July"}, "amount": {"1840.00"},
		"csrf": {h.csrf(t, "/explainers")},
	}); strings.Contains(loc, "msg=") {
		t.Fatalf("commissioning was refused: %s", loc)
	}
	list, err := crew.Explainers(h.st.DB())
	if err != nil || len(list) == 0 {
		t.Fatalf("no explainer was created: %v", err)
	}
	e := list[0]
	if e.State != "draft" {
		t.Errorf("a commissioned explainer arrived as %q, not a draft", e.State)
	}
	// It is written for the team, in the team's language.
	for _, want := range []string{"not a criticism", "found", "one person could do"} {
		if !strings.Contains(e.Body, want) {
			t.Errorf("the draft does not contain %q, so it is not written for the team", want)
		}
	}

	if err := crew.ReturnExplainer(h.st.DB(), e.ID, ""); err == nil {
		t.Error("an explainer was returned with no reason")
	}

	if _, loc := h.post(t, "/explainers/"+strconv.Itoa(e.ID)+"/publish", url.Values{
		"csrf": {h.csrf(t, "/explainers")},
	}); strings.Contains(loc, "msg=") {
		t.Fatalf("publishing was refused: %s", loc)
	}
	got, _ := crew.GetExplainer(h.st.DB(), e.ID)
	if got.State != "published" || got.Publisher != "owner" {
		t.Fatalf("state %q, published by %q: the stamp is a PERSON's act",
			got.State, got.Publisher)
	}
}

// ------------------------------------------------------------- sorting

// cellsIn pulls the text of one column out of a rendered table, in the order
// the page put them, so a test can say what the sort actually did rather than
// that the page still returned 200.
func cellsIn(body, pattern string) []string {
	re := regexp.MustCompile(pattern)
	var out []string
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func sorted(xs []string, ascending bool) bool {
	for i := 1; i < len(xs); i++ {
		if ascending && xs[i] < xs[i-1] {
			return false
		}
		if !ascending && xs[i] > xs[i-1] {
			return false
		}
	}
	return true
}

// A sorted column has to come back sorted, in BOTH directions, on every table
// that offers the control. Rendering the header is not the feature.
func TestClickingAColumnActuallySortsIt(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	for _, tc := range []struct{ path, col, pattern string }{
		{"/anomalies", "day", `<td class="tight">(\d{4}-\d{2}-\d{2})</td>`},
		{"/allocation", "team", `<td><a href="/team/([a-z0-9-]+)">`},
		{"/utilisation", "team", `<td class="tight"><a href="/team/([a-z0-9-]+)">`},
		{"/saas", "vendor", `<td><strong>([A-Za-z][A-Za-z0-9 .-]*)</strong>`},
		{"/staff", "name", `<td><a href="/staff/([a-z0-9._-]+)">`},
	} {
		asc := tc.path + "?sort=" + tc.col + "&dir=asc"
		desc := tc.path + "?sort=" + tc.col + "&dir=desc"

		_, up, _ := h.get(t, asc)
		_, down, _ := h.get(t, desc)
		a, d := cellsIn(up, tc.pattern), cellsIn(down, tc.pattern)

		if len(a) < 2 {
			t.Errorf("%s: pattern matched %d rows, cannot tell whether it sorted", asc, len(a))
			continue
		}
		if !sorted(a, true) {
			t.Errorf("%s: not ascending: %v", asc, a[:min(6, len(a))])
		}
		if !sorted(d, false) {
			t.Errorf("%s: not descending: %v", desc, d[:min(6, len(d))])
		}
	}
}

// A page that sorts a package-level fixture must sort a COPY of it.
//
// The fault this catches is invisible on the page that caused it: one reader
// sorts SaaS by vendor, and every other reader, and every later request on
// every other page, silently gets the fixture in that order for the rest of
// the process's life.
func TestSortingDoesNotReorderTheFixtureItself(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	before := make([]string, 0, len(world.Licences))
	for _, l := range world.Licences {
		before = append(before, l.Vendor+"/"+l.Product)
	}

	for _, p := range []string{
		"/saas?sort=vendor&dir=desc", "/saas?csort=hourly&csortdir=asc",
		"/utilisation?sort=saving&dir=asc", "/ai?sort=tokens&dir=asc",
	} {
		if code, _, _ := h.get(t, p); code != http.StatusOK {
			t.Fatalf("GET %s: %d", p, code)
		}
	}

	for i, l := range world.Licences {
		if got := l.Vendor + "/" + l.Product; got != before[i] {
			t.Fatalf("the fixture was reordered by a request: row %d was %q, is now %q",
				i, before[i], got)
		}
	}
}

// Two tables on one page sort independently. Sharing one parameter makes a
// click on the second table quietly reorder the first.
func TestTwoTablesOnAPageSortIndependently(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	plain, _, _ := h.get(t, "/saas")
	_ = plain
	_, base, _ := h.get(t, "/saas")
	_, moved, _ := h.get(t, "/saas?csort=name&csortdir=asc")

	pat := `<td><strong>([A-Za-z][A-Za-z0-9 .-]*)</strong>`
	if a, b := cellsIn(base, pat), cellsIn(moved, pat); len(a) > 1 && strings.Join(a, "|") != strings.Join(b, "|") {
		t.Errorf("sorting the commitments table also reordered the licence table:\n %v\n %v",
			a[:min(4, len(a))], b[:min(4, len(b))])
	}
}

// ------------------------------------------------- one question, one answer

// number pulls the first decimal figure out of a fragment.
func number(s string) string {
	m := regexp.MustCompile(`-?[0-9][0-9,]*\.[0-9]+|-?[0-9][0-9,]*`).FindString(s)
	return strings.ReplaceAll(m, ",", "")
}

// fragment returns the text around the first occurrence of a marker.
func fragment(body, marker string, n int) string {
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	end := i + len(marker) + n
	if end > len(body) {
		end = len(body)
	}
	return body[i+len(marker) : end]
}

// The same question asked on two pages gets the same answer.
//
// This is not a style point. Forecast accuracy read 11.7% over 84 month-desks
// on one page and 11.9% over 78 on another, and both were arithmetically
// correct: one had been handed the page's filter where the estate's open month
// belonged. Two right answers that disagree are worse than one wrong one,
// because a reader cannot tell which to act on and quietly stops trusting
// both.
func TestTwoPagesNeverDisagreeAboutOneNumber(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	// Forecast accuracy: the forecast page against the KPI page.
	_, fc, _ := h.get(t, "/forecast")
	_, kp, _ := h.get(t, "/kpis")

	fAcc := number(fragment(fc, "Average error across", 40))         // the count
	fPct := number(fragment(fc, "scored month-desks: <strong>", 20)) // the percentage

	row := fragment(kp, `id="kpi-forecast-accuracy"`, 700)
	kAcc := number(fragment(row, "Across", 40))
	kPct := number(fragment(row, `<td class="num">`, 40))

	if fAcc == "" || kAcc == "" {
		t.Skip("nothing has been scored in this fixture, so there is no number to compare")
	}
	if fAcc != kAcc {
		t.Errorf("scored month-desks: the forecast page says %s, the KPI page says %s", fAcc, kAcc)
	}
	if fPct != "" && kPct != "" && fPct != kPct {
		t.Errorf("average error: the forecast page says %s%%, the KPI page says %s%%", fPct, kPct)
	}
}

// The crew's own cost appears on three pages, and it is one number.
//
// The complaint this answers is not that a figure was wrong, it is that a
// figure appeared in exactly one place and so could not be checked at all. A
// console whose numbers cannot be checked against each other is a console
// whose numbers are believed or not on faith.
func TestTheCrewsCostIsTheSameOnEveryPageThatShowsIt(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	_, staff, _ := h.get(t, "/staff")
	_, kpis, _ := h.get(t, "/kpis")
	_, ai, _ := h.get(t, "/ai")

	fromStaff := number(fragment(staff, `<div class="k">What the crew cost</div>`, 120))
	kpiRow := fragment(kpis, `id="kpi-crew-cost"`, 900)
	fromKPI := number(fragment(kpiRow, `<td class="num">`, 40))
	fromAI := number(fragment(ai, "This crew is a separate bill: <strong>", 40))

	if fromStaff == "" || fromAI == "" {
		t.Fatalf("the crew's cost is missing from a page: staff %q, ai %q", fromStaff, fromAI)
	}
	if fromStaff != fromAI {
		t.Errorf("the crew cost %s on /staff and %s on /ai", fromStaff, fromAI)
	}
	if fromKPI != "" && fromKPI != fromStaff {
		t.Errorf("the crew cost %s on /staff and %s on /kpis", fromStaff, fromKPI)
	}
}

// A team's spend on its own page is what the estate list says it spent.
func TestATeamsSpendAgreesWithTheEstateList(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	_, teams, _ := h.get(t, "/teams")
	names := regexp.MustCompile(`<a href="/team/([a-z0-9-]+)">`).FindAllStringSubmatch(teams, -1)
	if len(names) == 0 {
		t.Fatal("the estate list names no teams, so this measured nothing")
	}
	checked := 0
	for _, m := range names {
		name := m[1]
		// The fully loaded column on the list, against the fully loaded tile
		// on the team's own page. The same question, asked twice.
		//
		// Both are addressed by a data attribute rather than by counting
		// cells: this test first "passed" against the wrong column because
		// one cell wrapped its figure in <strong> and the count silently ran
		// into the next row.
		row := fragment(teams, `<tr data-team="`+name+`">`, 900)
		listed := number(fragment(row, `data-col="loaded"><strong>`, 40))
		_, page, _ := h.get(t, "/team/"+name)
		own := number(fragment(page, `data-tile="loaded"><div class="k">Fully loaded</div><div class="v">`, 40))
		if listed == "" || own == "" {
			continue
		}
		if listed != own {
			t.Errorf("%s: the estate list says %s, its own page says %s", name, listed, own)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no team could be compared, so this measured nothing")
	}
}

// The agent card reads the AGENT-EVENT stream, not the installation's journal.
//
// They are two logs answering two questions. The store's chain records who
// signed in and what changed about the installation and never names an
// analyst; the agent events record what the agents did and carry the
// delegation chain another service in the stack reads. A card showing the
// wrong one looks like an answer and is not.
func TestTheAgentCardReadsTheAgentEventStream(t *testing.T) {
	dir := t.TempDir()
	stream := filepath.Join(dir, "events.ndjson")
	line := `{"schema":"taipanbox.dev/agent-event/v0.2","ts":"2026-08-20T09:15:00Z",` +
		`"source":"costcrew","type":"anomaly_triaged","agent_id":"agent://costcrew.test/supervisor",` +
		`"severity":"medium","on_behalf_of":["user://costcrew.test/owner"],` +
		`"data":{"anomaly":"A-1234","assigned_to":"triage-aws"}}` + "\n"
	if err := os.WriteFile(stream, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	h := startStream(t, stream)
	h.signUp(t, "owner", "owner-password-2026")

	code, body, _ := h.get(t, "/staff/triage-aws")
	if code != http.StatusOK {
		t.Fatalf("GET /staff/triage-aws: %d", code)
	}
	events := body[strings.Index(body, "<h2>Events"):]
	if !strings.Contains(events, "anomaly_triaged") {
		t.Error("the card does not show the event that names this agent")
	}
	// It was the supervisor that acted, and the card says so rather than
	// letting the row read as this agent's own doing.
	if !strings.Contains(events, "by supervisor") {
		t.Error("the card does not say which agent emitted the event")
	}
	if !strings.Contains(events, "2026-08-20 09:15") {
		t.Error("the card does not show the event's own timestamp")
	}
}

// The chain records what was DECIDED, not only who signed in.
//
// The audit page says "everything the console did, in order, each entry hashed
// against the one before it". That was false for most of this console's life:
// the chain held user_created, login and role changes, and every decision -
// a finding taken, answered, accepted, dismissed - went only to the
// agent-event stream, which is not hash-chained. A governance console that
// hashes sign-ins and not decisions has the chain pointed at the wrong thing.
func TestTheChainRecordsDecisionsAndNotOnlySignIns(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	open, err := anomaly.List(h.st.DB(), anomaly.Filter{State: anomaly.Open})
	if err != nil || len(open) == 0 {
		t.Skip("nothing open in this fixture to decide about")
	}
	id := open[0].ID

	before, _ := h.st.JournalTail(500)
	page := "/anomalies/" + id
	if _, loc := h.post(t, page+"/assign", url.Values{
		"analyst": {"triage-aws"}, "csrf": {h.csrf(t, page)},
	}); strings.Contains(loc, "msg=") {
		t.Fatalf("assign was refused: %s", loc)
	}
	if _, loc := h.post(t, page+"/dismiss", url.Values{
		"reason": {"A one-day spike that reversed. Nothing to recover."},
		"csrf":   {h.csrf(t, page)},
	}); strings.Contains(loc, "msg=") {
		t.Fatalf("dismiss was refused: %s", loc)
	}

	after, err := h.st.JournalTail(500)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) <= len(before) {
		t.Fatalf("two decisions were made and the chain grew by %d entries",
			len(after)-len(before))
	}
	kinds := map[string]map[string]any{}
	for _, r := range after {
		kinds[r.Event] = r.Data
	}
	for _, want := range []string{"anomaly_triaged", "anomaly_dismissed"} {
		d, ok := kinds[want]
		if !ok {
			t.Errorf("the chain has no %s entry", want)
			continue
		}
		// Who did it is the part of an audit entry somebody needs.
		if d["actor"] == nil {
			t.Errorf("%s is in the chain with no actor", want)
		}
	}
	// And it still verifies: appending decisions must not break the hash.
	if ok, _, breakAt, err := h.st.VerifyChain(); err != nil || !ok {
		t.Errorf("the chain no longer verifies after two decisions: %v at %s", err, breakAt)
	}
}

// Every name the console prints is a way in.
//
// The rule is the one the estate pages were built for: a cell that names a
// team, a desk or an analyst and does not open it makes the reader go back to
// the top and filter by hand. It kept being half-true, because each new page
// had to remember, so this checks every page at once.
func TestEveryNameOnAPageIsALinkIntoIt(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	// Names that exist, and the page that should open for each.
	known := map[string]string{}
	for _, tm := range world.Teams {
		known[tm.Name] = "/team/" + tm.Name
	}
	for _, d := range world.Desks {
		known[d.Name] = "/desk/" + d.Name
	}
	roster, err := crew.Roster(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range roster {
		known[a.Name] = "/staff/" + a.Name
	}

	// A bare cell is one whose whole content is a known name, in a row that
	// does not already link that name.
	//
	// The per-ROW check matters: the desks table has a "kind" column whose
	// value for the SaaS desk is the word "saas", which is also a desk name.
	// Flagging it would be asking the page to link a category to an entity
	// that merely shares its spelling.
	rowRe := regexp.MustCompile(`(?s)<tr[^>]*>.*?</tr>`)
	cell := regexp.MustCompile(`<td[^>]*>([a-z0-9][a-z0-9._-]{2,})</td>`)
	for _, path := range []string{
		"/", "/anomalies", "/budgets", "/staff", "/board", "/sprints",
		"/allocation", "/chargeback", "/utilisation", "/saas", "/ai",
		"/forecast", "/teams", "/desks", "/team/ml-platform", "/desk/aws",
		"/sprint/plan", "/connectors",
	} {
		code, body, _ := h.get(t, path)
		if code != http.StatusOK {
			t.Errorf("GET %s: %d", path, code)
			continue
		}
		for _, row := range rowRe.FindAllString(body, -1) {
			for _, m := range cell.FindAllStringSubmatch(row, -1) {
				to, ok := known[m[1]]
				if !ok || strings.Contains(row, `href="`+to+`"`) {
					continue
				}
				t.Errorf("%s prints %q as plain text; it should open %s", path, m[1], to)
			}
		}
	}
}

package web_test

import (
	"fmt"
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
	"github.com/TAIPANBOX/costcrew/internal/money"
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
	// The harness seeds what production seeds: charges.provenance and
	// ai_calls have to exist before the AI page's queries run, on every
	// store, whether or not anything has ever imported through the
	// tokenfuse-focus reader, the same as in cmd/costcrew/main.go.
	if err := connectors.EnsureFocusSchema(st.DB()); err != nil {
		t.Fatal(err)
	}
	// recommendations, the same reason: /rightsizing reads it on every
	// render whether or not any of the three rightsizing readers has ever
	// been pointed at a folder.
	if err := connectors.EnsureRecommendationsSchema(st.DB()); err != nil {
		t.Fatal(err)
	}
	// licences: the SaaS page reads it on every render, whether or not
	// anything has ever imported through the saas-seats reader, the same as
	// ai_calls above.
	if err := connectors.EnsureLicenceSchema(st.DB()); err != nil {
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

// as returns a second client signed in as somebody else, so a test can check
// what a role may do rather than what a function allows.
func (h *harness) as(t *testing.T, user, pw string) *harness {
	t.Helper()
	other := &harness{srv: h.srv, au: h.au, st: h.st, c: &http.Client{
		Jar: newJar(t),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
	code, loc := other.post(t, "/login", url.Values{
		"username": {user}, "password": {pw}, "csrf": {other.csrf(t, "/login")},
	})
	if code != http.StatusSeeOther || strings.Contains(loc, "msg=") {
		t.Fatalf("signing in as %s: %d %s", user, code, loc)
	}
	return other
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
		{"/rightsizing", "Rightsizing"},
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

	// A test must not call it either. aws-cost-explorer has no reader (like
	// every connector today), so it is honestly Documented, and that is the
	// reason a test result gives FIRST: nothing runs whether or not somebody
	// would have confirmed the cost.
	if _, loc := h.post(t, path+"/test", url.Values{"csrf": {h.csrf(t, path)}}); strings.Contains(loc, "msg=") {
		t.Errorf("testing a metered connector errored: %s", loc)
	}
	_, body, _ = h.get(t, path)
	if !strings.Contains(body, "Nothing was called") {
		t.Error("the test result does not say the connector was left alone")
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

// The sprint form's goal field reaches crew.Propose and comes back in the
// proposal, both in what the page shows and in what the box re-displays for
// the approve step to carry forward (B4-SPEC.md section 3).
func TestThePlanFormCarriesAGoalIntoTheProposal(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	_, body, _ := h.get(t, "/sprint/plan")
	if !strings.Contains(body, `name="goal"`) {
		t.Fatal("the plan page has no goal field")
	}

	_, body, _ = h.get(t, "/sprint/plan?goal=commitment-modelling+for+this+sprint")
	if !strings.Contains(body, "the sprint goal names commitment-modelling") {
		t.Error("typing a goal did not change the proposal: no item names it as why")
	}
	if !strings.Contains(body, `value="commitment-modelling for this sprint"`) {
		t.Error("the goal box does not echo what was typed")
	}

	// The approve form's hidden goal field carries the same text through to
	// Approve, which recomputes the proposal from label/start/end/goal
	// rather than trusting anything the browser sent about the items
	// themselves.
	hidden := func(field string) string {
		m := regexp.MustCompile(`name="` + field + `" value="([^"]*)"`).FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("the approve form has no %s field to read back", field)
		}
		return m[1]
	}
	label, start, end := hidden("label"), hidden("start"), hidden("end")
	csrf := h.csrf(t, "/sprint/plan?goal=commitment-modelling+for+this+sprint")
	code, loc := h.post(t, "/sprint/plan", url.Values{
		"csrf": {csrf}, "label": {label}, "start": {start}, "end": {end},
		"goal": {"commitment-modelling for this sprint"},
	})
	if strings.Contains(loc, "msg=") {
		t.Fatalf("approving with a goal was refused: %d %s", code, loc)
	}
	tasks, err := crew.Tasks(h.st.DB(), crew.TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tk := range tasks {
		if tk.Assignee == "commitments" && strings.Contains(tk.Goal, "commitment-modelling for this sprint") {
			found = true
		}
	}
	if !found {
		t.Error("approving did not create a task for the commitments analyst from the typed goal")
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
		{"/allocation", "team", `<td><a href="/team/([a-z0-9-]+)[^"]*">`},
		{"/utilisation", "team", `<td class="tight"><a href="/team/([a-z0-9-]+)[^"]*">`},
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

	// And how many tasks are open. The overview counted everything that was
	// not "done" and reported 309 while the crew page reported 77, because
	// posted work is finished and only one page knew it.
	_, overview, _ := h.get(t, "/")
	openOnOverview := number(fragment(overview, `<div class="k">On the crew</div>`, 60))
	openOnStaff := number(fragment(staff, `<div class="k">On the desks now</div>`, 60))
	if openOnOverview == "" || openOnStaff == "" {
		t.Fatalf("open tasks missing: overview %q, staff %q", openOnOverview, openOnStaff)
	}
	if openOnOverview != openOnStaff {
		t.Errorf("open tasks: the overview says %s, the crew page says %s",
			openOnOverview, openOnStaff)
	}
}

// A team's spend on its own page is what the estate list says it spent.
func TestATeamsSpendAgreesWithTheEstateList(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	_, teams, _ := h.get(t, "/teams")
	names := regexp.MustCompile(`<a href="/team/([a-z0-9-]+)[^"]*">`).FindAllStringSubmatch(teams, -1)
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
	// Definition lists too: the task page prints its desk in a <dd>, so a
	// check that only reads table cells passed it for three commits.
	dd := regexp.MustCompile(`<dd[^>]*>([a-z0-9][a-z0-9._-]{2,})</dd>`)
	for _, path := range []string{
		"/", "/anomalies", "/budgets", "/staff", "/board", "/sprints",
		"/allocation", "/chargeback", "/utilisation", "/saas", "/ai",
		"/forecast", "/teams", "/desks", "/team/ml-platform", "/desk/aws",
		"/sprint/plan", "/connectors", "/sprint/1", "/task/1",
		"/connectors/aws-cost-explorer", "/staff/triage-aws", "/explainers",
	} {
		code, body, _ := h.get(t, path)
		if code != http.StatusOK {
			t.Errorf("GET %s: %d", path, code)
			continue
		}
		for _, row := range rowRe.FindAllString(body, -1) {
			for _, m := range cell.FindAllStringSubmatch(row, -1) {
				to, ok := known[m[1]]
				// The href may now carry the period the reader is looking at,
				// so the match is on the path and not on the whole attribute.
				// It said "prints saas as plain text" when saas was linked
				// perfectly well, only with ?period= after it.
				if !ok || strings.Contains(row, `href="`+to+`"`) ||
					strings.Contains(row, `href="`+to+`?`) {
					continue
				}
				t.Errorf("%s prints %q as plain text; it should open %s", path, m[1], to)
			}
		}
		for _, m := range dd.FindAllStringSubmatch(body, -1) {
			if to, ok := known[m[1]]; ok && !strings.Contains(body, `href="`+to+`"`) {
				t.Errorf("%s prints %q as plain text in a definition list; it should open %s",
					path, m[1], to)
			}
		}
	}
}

// No KPI may be incapable of failing.
//
// The page's own headline is that a library where everything reports a number
// is one where several are invented. "What the crew cost" carried a hard-coded
// pass, so it announced that it met its target while the crew was returning
// forty pence in the pound.
//
// The check is structural rather than about one KPI: every reported KPI must
// have a target it could miss, and the way to prove that here is that not all
// of them pass. A library where every single one meets is either a very good
// practice or a set of assertions, and this fixture is deliberately not the
// former.
func TestNotEveryKPIPasses(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	_, body, _ := h.get(t, "/kpis")
	rows := regexp.MustCompile(`(?s)<tr id="kpi-([a-z-]+)">(.*?)</tr>`).FindAllStringSubmatch(body, -1)
	if len(rows) < 5 {
		t.Fatalf("the KPI page has %d rows, so this measured nothing", len(rows))
	}
	var meets, below, blocked int
	for _, r := range rows {
		switch {
		case strings.Contains(r[2], "cannot be computed"):
			blocked++
		case strings.Contains(r[2], ">meets<"):
			meets++
		case strings.Contains(r[2], ">below<"):
			below++
		}
	}
	if below == 0 {
		t.Error("every KPI that reports a number meets its target, which is a library of assertions")
	}
	if blocked == 0 {
		t.Error("no KPI refuses; the refusals are the part that shows the rest are measured")
	}
	// And the crew-cost one specifically, because it is the one that lied.
	crew := regexp.MustCompile(`(?s)<tr id="kpi-crew-cost">(.*?)</tr>`).FindStringSubmatch(body)
	if crew == nil {
		t.Fatal("the crew-cost KPI is missing")
	}
	if !strings.Contains(crew[1], "return of") {
		t.Error("the crew-cost KPI does not say what it returned, so its verdict cannot be checked")
	}
}

// ------------------------------------------------------ removing an account

// Removing an account is an admin's job, it needs the name typed, and there
// are two accounts it must always refuse.
func TestRemovingAnAccount(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	if err := h.au.SetRole("owner", "admin"); err != nil {
		t.Fatal(err)
	}
	for _, who := range []struct{ name, role string }{
		{"reader", "viewer"}, {"hand", "operator"}, {"second", "admin"},
	} {
		if ok, err := h.au.Create(who.name, who.name+"-password-2026", who.role); err != nil || !ok {
			t.Fatalf("creating %s: %v %v", who.name, ok, err)
		}
	}

	// The name has to be typed. A button on its own is one misclick from an
	// outage, and a confirm dialog is one click nobody reads.
	if _, loc := h.post(t, "/accounts/remove", url.Values{
		"username": {"reader"}, "csrf": {h.csrf(t, "/accounts")},
	}); !strings.Contains(loc, "type+its+name") {
		t.Errorf("removing without typing the name was not refused: %s", loc)
	}
	if u, _ := h.au.Get("reader"); u == nil {
		t.Fatal("the account was removed without the name being typed")
	}

	// Typed, and gone.
	if _, loc := h.post(t, "/accounts/remove", url.Values{
		"username": {"reader"}, "confirm": {"reader"}, "csrf": {h.csrf(t, "/accounts")},
	}); strings.Contains(loc, "msg=") {
		t.Fatalf("removing was refused: %s", loc)
	}
	if u, err := h.au.Get("reader"); err != nil || u != nil {
		t.Error("the account is still there after being removed")
	}

	// Not yourself, however admin you are.
	if _, loc := h.post(t, "/accounts/remove", url.Values{
		"username": {"owner"}, "confirm": {"owner"}, "csrf": {h.csrf(t, "/accounts")},
	}); !strings.Contains(loc, "signed+in+as") {
		t.Errorf("removing your own account was not refused: %s", loc)
	}

	// And not the last admin. With "owner" and "second" both admins, removing
	// "second" is allowed; once it is gone, "owner" is the only one left, and
	// the guard is the one that stops an installation nobody can manage.
	if _, loc := h.post(t, "/accounts/remove", url.Values{
		"username": {"second"}, "confirm": {"second"}, "csrf": {h.csrf(t, "/accounts")},
	}); strings.Contains(loc, "msg=") {
		t.Fatalf("removing the second admin was refused: %s", loc)
	}
	if err := h.au.Delete("owner"); err == nil {
		t.Error("the last admin was removed, and now nobody can manage this installation")
	}

	// The chain still records what the removed account did.
	tail, _ := h.st.JournalTail(200)
	var removed bool
	for _, r := range tail {
		if r.Event == "user_removed" && r.Data["username"] == "reader" {
			removed = true
		}
	}
	if !removed {
		t.Error("the removal is not in the journal")
	}
}

// An operator may act. It may not hand somebody else the ability to, and it
// may not take it away either.
func TestAnOperatorCannotRemoveAnAccount(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	if ok, err := h.au.Create("hand", "hand-password-2026", "operator"); err != nil || !ok {
		t.Fatal(err)
	}
	if ok, err := h.au.Create("reader", "reader-password-2026", "viewer"); err != nil || !ok {
		t.Fatal(err)
	}
	op := h.as(t, "hand", "hand-password-2026")

	if _, loc := op.post(t, "/accounts/remove", url.Values{
		"username": {"reader"}, "confirm": {"reader"}, "csrf": {op.csrf(t, "/accounts")},
	}); !strings.Contains(loc, "admin") {
		t.Errorf("an operator removed an account: %s", loc)
	}
	if u, _ := h.au.Get("reader"); u == nil {
		t.Error("the account is gone, removed by an operator")
	}
}

// --------------------------------------------- removing and moving an agent

// Only the owner, or an admin. An operator who did not hire it may not delete
// somebody else's agent: hiring one is taking responsibility for what it
// spends, and the console records who did, which would mean nothing if anybody
// with the operator role could undo it.
func TestOnlyTheOwnerOrAnAdminMayRemoveAnAgent(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	if ok, err := h.au.Create("hand", "hand-password-2026", "operator"); err != nil || !ok {
		t.Fatal(err)
	}

	// An analyst owned by "owner" with nothing open, so the only thing that
	// can refuse the removal is the rule under test.
	a := crew.Analyst{
		Name: "night-desk", Role: "after-hours variance", Desk: "aws",
		Engine: "kimi-standard", State: "active", Skills: []string{"anomaly-triage"},
		PerTask: money.Cents(1500), Monthly: money.Cents(4000),
		Cadence: "daily", Audience: "the desk", Owner: "owner",
		Parent: "supervisor", Attestation: "none", Hired: "2026-08-01",
	}
	if err := crew.Hire(h.st.DB(), a); err != nil {
		t.Fatal(err)
	}

	other := h.as(t, "hand", "hand-password-2026")
	if _, loc := other.post(t, "/staff/night-desk/remove", url.Values{
		"confirm": {"night-desk"}, "csrf": {other.csrf(t, "/staff/night-desk")},
	}); !strings.Contains(loc, "who+hired+it") {
		t.Errorf("an operator who does not own the agent removed it: %s", loc)
	}
	if _, err := crew.GetAnalyst(h.st.DB(), "night-desk"); err != nil {
		t.Fatal("the agent is gone, removed by somebody who does not own it")
	}

	// The owner may, once the name is typed.
	if _, loc := h.post(t, "/staff/night-desk/remove", url.Values{
		"csrf": {h.csrf(t, "/staff/night-desk")},
	}); !strings.Contains(loc, "type+its+name") {
		t.Errorf("removing without typing the name was not refused: %s", loc)
	}
	if _, loc := h.post(t, "/staff/night-desk/remove", url.Values{
		"confirm": {"night-desk"}, "csrf": {h.csrf(t, "/staff/night-desk")},
	}); strings.Contains(loc, "msg=only") {
		t.Fatalf("the owner was refused: %s", loc)
	}
	if _, err := crew.GetAnalyst(h.st.DB(), "night-desk"); err == nil {
		t.Error("the owner removed it and it is still on the roster")
	}
}

// An agent with open work is not removed, because the work would outlive it.
func TestAnAgentWithOpenWorkIsNotRemoved(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	// Somebody on the seeded roster who has open work.
	roster, err := crew.Roster(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	var name string
	for _, a := range roster {
		if n, _ := crew.OpenWork(h.st.DB(), a.Name); n > 0 && a.Name != "supervisor" {
			name = a.Name
			break
		}
	}
	if name == "" {
		t.Skip("nobody on this roster has open work")
	}
	if _, loc := h.post(t, "/staff/"+name+"/remove", url.Values{
		"confirm": {name}, "csrf": {h.csrf(t, "/staff/"+name)},
	}); !strings.Contains(loc, "open") {
		t.Errorf("an agent with open work was removed: %s", loc)
	}
	if _, err := crew.GetAnalyst(h.st.DB(), name); err != nil {
		t.Error("the agent is gone and its open work is charged to a name nobody can open")
	}
}

// A transfer moves the agent and its OPEN work, and leaves what was already
// charged where it was charged.
//
// The second half is the design decision, not an omission: a closed month has
// been invoiced, and moving money out of one after a team was told what it
// owed is what the chargeback page exists to prevent.
func TestATransferMovesOpenWorkAndLeavesChargedWorkAlone(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	roster, err := crew.Roster(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	var name, from string
	for _, a := range roster {
		if n, _ := crew.OpenWork(h.st.DB(), a.Name); n > 0 && a.Desk != "gcp" {
			name, from = a.Name, a.Desk
			break
		}
	}
	if name == "" {
		t.Skip("nobody on this roster has open work off the gcp desk")
	}
	count := func(desk, states string) int {
		var n int
		_ = h.st.DB().QueryRow(`SELECT COUNT(*) FROM tasks WHERE assignee=? AND desk=?
			AND state `+states, name, desk).Scan(&n)
		return n
	}
	openBefore := count(from, `IN ('queued','active','blocked','returned')`)
	chargedBefore := count(from, `NOT IN ('queued','active','blocked','returned')`)

	if _, loc := h.post(t, "/staff/"+name+"/transfer", url.Values{
		"desk": {"gcp"}, "csrf": {h.csrf(t, "/staff/"+name)},
	}); strings.Contains(loc, "msg=only") || strings.Contains(loc, "nothing+would") {
		t.Fatalf("the transfer was refused: %s", loc)
	}

	a, err := crew.GetAnalyst(h.st.DB(), name)
	if err != nil {
		t.Fatal(err)
	}
	if a.Desk != "gcp" {
		t.Errorf("%s is still on the %s desk", name, a.Desk)
	}
	if moved := count("gcp", `IN ('queued','active','blocked','returned')`); moved != openBefore {
		t.Errorf("%d open tasks moved, %d were open before", moved, openBefore)
	}
	if left := count(from, `NOT IN ('queued','active','blocked','returned')`); left != chargedBefore {
		t.Errorf("work already charged to %s changed: %d rows, was %d. "+
			"A closed month has been invoiced and must not move.", from, left, chargedBefore)
	}
	if stillOpen := count(from, `IN ('queued','active','blocked','returned')`); stillOpen != 0 {
		t.Errorf("%d open tasks stayed on %s after the agent left it", stillOpen, from)
	}
}

// Moving desks moves who it answers to, when that was the desk's own partner.
//
// Left alone, the chain said an agent on the gcp desk reported to the aws
// partner: a claim nobody made, on the page whose whole job is showing who
// acts for whom. A parent somebody CHOSE is a decision, and a transfer does
// not overwrite decisions it was not asked about, so only the desk default
// follows the desk.
func TestMovingDeskMovesTheDefaultParent(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	a := crew.Analyst{
		Name: "night-desk", Role: "night watch", Desk: "aws", Engine: "kimi-standard",
		State: "active", Skills: []string{"anomaly-triage"},
		PerTask: money.Cents(1200), Monthly: money.Cents(9000),
		Cadence: "daily", Audience: "the morning shift", Owner: "owner",
		Parent: "partner-aws", Attestation: "none", Hired: "2026-08-22",
	}
	if err := crew.Hire(h.st.DB(), a); err != nil {
		t.Fatal(err)
	}
	if _, loc := h.post(t, "/staff/night-desk/transfer", url.Values{
		"desk": {"gcp"}, "csrf": {h.csrf(t, "/staff/night-desk")},
	}); strings.Contains(loc, "msg=only") {
		t.Fatalf("the transfer was refused: %s", loc)
	}
	got, err := crew.GetAnalyst(h.st.DB(), "night-desk")
	if err != nil {
		t.Fatal(err)
	}
	if got.Parent != "partner-gcp" {
		t.Errorf("on the gcp desk it answers to %q", got.Parent)
	}

	// A chosen parent survives a later move.
	if _, loc := h.post(t, "/staff/night-desk/transfer", url.Values{
		"desk": {"azure"}, "parent": {"forecaster"}, "csrf": {h.csrf(t, "/staff/night-desk")},
	}); strings.Contains(loc, "msg=only") {
		t.Fatalf("the second transfer was refused: %s", loc)
	}
	if _, loc := h.post(t, "/staff/night-desk/transfer", url.Values{
		"desk": {"onprem"}, "csrf": {h.csrf(t, "/staff/night-desk")},
	}); strings.Contains(loc, "msg=only") {
		t.Fatalf("the third transfer was refused: %s", loc)
	}
	got, _ = crew.GetAnalyst(h.st.DB(), "night-desk")
	if got.Parent != "forecaster" {
		t.Errorf("a chosen parent was overwritten by a desk move: it now answers to %q", got.Parent)
	}
}

// Every table sits in something that scrolls.
//
// This is what keeps the console usable on a phone. A table of nine columns
// in a 375px viewport has to scroll SIDEWAYS INSIDE ITS OWN BOX; without the
// container it pushes the whole document wide instead, and then every page,
// including the ones with no table on them, scrolls horizontally and the
// headings run off the screen.
//
// It is a template invariant rather than a layout one, which is why a test can
// hold it: the failure is always a new page whose author did not know, and it
// is invisible on a desktop.
func TestEveryTableCanScrollInsideItsOwnBox(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	// The opening tag of a table, and what encloses it.
	open := regexp.MustCompile(`(?s)<div class="scroll">\s*<table`)
	any := regexp.MustCompile(`<table`)

	for _, path := range []string{
		"/", "/anomalies", "/budgets", "/staff", "/board", "/sprints", "/allocation",
		"/chargeback", "/results", "/kpis", "/utilisation", "/saas", "/ai", "/forecast",
		"/explainers", "/connectors", "/engines", "/accounts", "/audit", "/teams",
		"/desks", "/team/ml-platform", "/desk/aws", "/staff/triage-aws", "/sprint/plan",
		"/rightsizing",
	} {
		code, body, _ := h.get(t, path)
		if code != http.StatusOK {
			t.Errorf("GET %s: %d", path, code)
			continue
		}
		tables := len(any.FindAllString(body, -1))
		wrapped := len(open.FindAllString(body, -1))
		if tables != wrapped {
			t.Errorf("%s has %d tables and %d of them are inside a scrolling box; "+
				"the rest will push the whole page sideways on a phone", path, tables, wrapped)
		}
	}
}

// The light palette is defined where a viewer with no explicit choice will
// find it.
//
// Most people never set a theme, so the browser reports "system" and nothing
// is stamped on the document. A colour whose only definition sits inside a
// prefers-color-scheme block or a [data-theme] selector simply does not apply
// in that state, and the page renders one theme's text on the other's ground.
func TestTheLightPaletteIsOnBareRoot(t *testing.T) {
	css, err := os.ReadFile("assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	s := string(css)
	i := strings.Index(s, ":root {")
	if i < 0 {
		t.Fatal("there is no bare :root block, so a viewer who has chosen nothing gets nothing")
	}
	end := strings.Index(s[i:], "}")
	bare := s[i : i+end]

	// Every token the dark block redefines must already exist on bare :root.
	j := strings.Index(s, "@media (prefers-color-scheme: dark)")
	if j < 0 {
		t.Skip("this stylesheet does not define a dark theme")
	}
	darkEnd := strings.Index(s[j:], "\n  }")
	dark := s[j : j+darkEnd]

	for _, m := range regexp.MustCompile(`--([a-z0-9-]+):`).FindAllStringSubmatch(dark, -1) {
		if !strings.Contains(bare, "--"+m[1]+":") {
			t.Errorf("--%s is defined only for dark; a viewer on the default "+
				"system setting in a light browser gets no value for it", m[1])
		}
	}
}

// The money lives in services, so a service is a page.
//
// Every other level the console shows - a team, a desk, an agent - is a way of
// grouping the same charges. A reader could open all three and not the thing
// that actually costs, and "Amazon EC2" appeared as plain text on six pages.
func TestAServiceIsSomethingYouCanOpen(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	code, list, _ := h.get(t, "/services")
	if code != http.StatusOK {
		t.Fatalf("GET /services: %d", code)
	}
	names := regexp.MustCompile(`<a href="/service/([^"?]+)[^"]*"`).FindAllStringSubmatch(list, -1)
	if len(names) < 10 {
		t.Fatalf("the services list names %d services", len(names))
	}
	// The shares on the list add up, so the page is a reading of one bill and
	// not a set of unrelated rows.
	var share float64
	for _, m := range regexp.MustCompile(`<td class="num">(\d+\.\d)%`).FindAllStringSubmatch(list, -1) {
		var v float64
		_, _ = fmt.Sscanf(m[1], "%f", &v)
		share += v
	}
	if share < 99 || share > 101 {
		t.Errorf("the service shares total %.1f%%, so they are not shares of one bill", share)
	}

	// And each one opens, with the money on it agreeing with the list.
	for _, m := range names[:5] {
		path := "/service/" + m[1]
		code, page, _ := h.get(t, path)
		if code != http.StatusOK {
			t.Errorf("GET %s: %d", path, code)
			continue
		}
		row := fragment(list, `<a href="/service/`+m[1], 700)
		listed := number(fragment(row, `data-col="amount">`, 30))
		own := number(fragment(page, `<div class="k">This month</div><div class="v">`, 30))
		if listed != "" && own != "" && listed != own {
			t.Errorf("%s: the list says %s, its own page says %s", path, listed, own)
		}
	}
}

// Search finds a thing by name, whatever kind of thing it is.
func TestSearchFindsAcrossEveryKind(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	for _, tc := range []struct{ q, wantKind, wantURL string }{
		{"BigQuery", "service", "/service/BigQuery"},
		{"ml-platform", "team", "/team/ml-platform"},
		{"aws", "desk", "/desk/aws"},
		{"triage-aws", "agent", "/staff/triage-aws"},
	} {
		_, body, _ := h.get(t, "/search?q="+url.QueryEscape(tc.q))
		if !strings.Contains(body, `href="`+tc.wantURL+`"`) {
			t.Errorf("searching %q does not offer %s", tc.q, tc.wantURL)
		}
		if !strings.Contains(body, `>`+tc.wantKind+`<`) {
			t.Errorf("searching %q does not say it found a %s", tc.q, tc.wantKind)
		}
	}
	// An exact match comes first. Somebody typing a whole name has already
	// told you which one they meant.
	_, body, _ := h.get(t, "/search?q=aws")
	first := regexp.MustCompile(`<a href="(/[a-z]+/[^"]+)"`).FindStringSubmatch(body)
	if first == nil || first[1] != "/desk/aws" {
		got := "nothing"
		if first != nil {
			got = first[1]
		}
		t.Errorf("searching the exact name \"aws\" put %s first", got)
	}
	// And nothing is not an error.
	_, empty, _ := h.get(t, "/search?q=zzzznotathing")
	if !strings.Contains(empty, "Nothing by that name") {
		t.Error("a search with no matches does not say so")
	}
}

// The links an alert carries have to land.
//
// heraldyx watches the agent-event stream this console writes and mails a
// person when something needs them tonight. Every message ends with three
// links into "the operator's own console", addressed by what the stack knows -
// an agent:// URI, an owner - rather than by this console's row ids. All three
// answered 404, which makes an alert that arrives at two in the morning worse
// than no alert: somebody is awake, worried, and has to find it by hand.
func TestTheLinksAnAlertCarriesLand(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	for _, tc := range []struct{ path, wantPrefix string }{
		// Exactly as heraldyx writes them, unescaped, because that is what
		// arrives and the console does not get to require otherwise.
		{"/a/agent://costcrew.test/triage-aws", "/staff/triage-aws"},
		{"/a/agent:/costcrew.test/triage-aws", "/staff/triage-aws"}, // after the mux collapses //
		{"/i/anomaly_triaged:agent://costcrew.test/triage-aws", "/"},
		{"/o/owner", "/o/owner"},
	} {
		code, _, loc := h.get(t, tc.path)
		switch code {
		case http.StatusOK:
			if tc.wantPrefix != tc.path {
				t.Errorf("GET %s answered directly; expected a redirect to %s", tc.path, tc.wantPrefix)
			}
		case http.StatusSeeOther:
			if !strings.HasPrefix(loc, tc.wantPrefix) {
				t.Errorf("GET %s went to %s, wanted %s", tc.path, loc, tc.wantPrefix)
			}
		default:
			t.Errorf("GET %s: %d, so an alert's own link is broken", tc.path, code)
		}
	}

	// An agent named in an old alert and since removed says so, rather than
	// answering 404, which reads as a broken console instead of a decision.
	_, _, loc := h.get(t, "/a/agent://costcrew.test/nobody-by-that-name")
	if !strings.Contains(loc, "/staff") || !strings.Contains(loc, "msg=") {
		t.Errorf("a link to a removed agent went to %q with no explanation", loc)
	}
}

// The owner page is the third link, and a view the console did not have.
func TestAnOwnerPageShowsEverythingTheyAnswerFor(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	code, body, _ := h.get(t, "/o/owner")
	if code != http.StatusOK {
		t.Fatalf("GET /o/owner: %d", code)
	}
	roster, err := crew.Roster(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, a := range roster {
		if a.Owner == "owner" {
			want++
		}
	}
	if want == 0 {
		t.Skip("nobody on this roster is owned by the signed-in account")
	}
	got := number(fragment(body, `<div class="k">Agents</div><div class="v big">`, 30))
	if got != strconv.Itoa(want) {
		t.Errorf("the owner page counts %s agents, the roster says %d", got, want)
	}
	// Every one of them is a link, because that is the point of the page.
	links := regexp.MustCompile(`href="/staff/[a-z0-9-]+"`).FindAllString(body, -1)
	if len(links) < want {
		t.Errorf("%d agents listed and %d of them open", want, len(links))
	}
}

// A table somebody will scan is a table they can sort.
//
// This kept being remembered per page and forgotten per page: the audit, the
// KPIs, the accounts and the sprint plan each shipped unsortable and each was
// found by looking rather than by anything that would have said so.
//
// The bar is four data rows. Below that the order is not a question anybody
// has, and a two-row breakdown with clickable headings is noise; above it,
// somebody is looking for the largest, the newest or the one name they came
// for, and a fixed order answers none of the three.
func TestATableWorthScanningCanBeSorted(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	const bar = 4
	// Any table, however it is opened. Matching only the bare <table> tag
	// would let a table opt out of this check by carrying any attribute at
	// all, which is the opposite of what the exemption below is for.
	table := regexp.MustCompile(`(?s)<table([^>]*)>(.*?)</table>`)
	// A table may declare that its order IS its meaning, and it has to say
	// why. The anomaly-state table is a lifecycle from "nobody has looked" to
	// closed; sorting it by count would lose the only thing the column says.
	// Requiring a reason is what stops this becoming a way to skip the check.
	declared := regexp.MustCompile(`data-order="([^"]{20,})"`)
	row := regexp.MustCompile(`<tr[ >]`)
	head := regexp.MustCompile(`<thead>`)
	sortable := regexp.MustCompile(`class="sortcol"`)
	empty := regexp.MustCompile(`class="empty"`)

	for _, path := range []string{
		"/", "/anomalies", "/budgets", "/staff", "/sprints", "/allocation",
		"/chargeback", "/kpis", "/utilisation", "/saas", "/ai", "/forecast",
		"/connectors", "/accounts", "/audit", "/teams", "/desks", "/services",
		"/team/ml-platform", "/desk/aws", "/staff/triage-aws", "/sprint/20",
		"/sprint/plan", "/o/owner",
	} {
		code, body, _ := h.get(t, path)
		if code != http.StatusOK {
			t.Errorf("GET %s: %d", path, code)
			continue
		}
		for i, m := range table.FindAllStringSubmatch(body, -1) {
			attrs, inner := m[1], m[2]
			if declared.MatchString(attrs) {
				continue
			}
			if strings.Contains(attrs, "data-order") {
				t.Errorf("%s: table %d declares data-order with no reason worth reading",
					path, i+1)
				continue
			}
			rows := len(row.FindAllString(inner, -1)) - len(head.FindAllString(inner, -1))
			if rows < bar || empty.MatchString(inner) {
				continue
			}
			if !sortable.MatchString(inner) {
				// Name the first heading, so the failure says WHICH table.
				h1 := regexp.MustCompile(`<th[^>]*>([^<]{1,24})`).FindStringSubmatch(inner)
				which := "?"
				if h1 != nil {
					which = strings.TrimSpace(h1[1])
				}
				t.Errorf("%s: table %d (%q) has %d rows and no sortable heading",
					path, i+1, which, rows)
			}
		}
	}
}

// ------------------------------------------------------------ the keyboard

// The first thing focus lands on is the way past the navigation.
//
// Twenty-four links stand between the top of every page and its content, so
// without this a keyboard user pressed Tab twenty-four times on every page
// before reaching anything they came for. It is the single most-felt
// accessibility fault a console with a sidebar has, and it is invisible to
// anybody using a mouse, which is why it survived this long.
func TestEveryPageOpensWithAWayPastTheNavigation(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	// Anything focusable, in document order.
	focusable := regexp.MustCompile(`<(a\s[^>]*href|button|select|textarea|input(?:\s[^>]*)?)[^>]*>`)

	for _, path := range []string{
		"/", "/anomalies", "/budgets", "/staff", "/board", "/sprints", "/services",
		"/allocation", "/chargeback", "/results", "/kpis", "/utilisation", "/saas",
		"/ai", "/forecast", "/explainers", "/connectors", "/accounts", "/audit",
		"/teams", "/desks", "/team/ml-platform", "/desk/aws", "/staff/triage-aws",
		"/service/BigQuery", "/o/owner", "/search", "/task/1", "/sprint/1",
	} {
		code, body, _ := h.get(t, path)
		if code != http.StatusOK {
			t.Errorf("GET %s: %d", path, code)
			continue
		}
		first := focusable.FindString(body)
		if !strings.Contains(first, `class="skip"`) {
			t.Errorf("%s: the first thing focus reaches is %q, not the skip link",
				path, strings.TrimSpace(first))
			continue
		}
		// And it has to go somewhere. A skip link pointing at an id that is
		// not on the page moves focus nowhere and is worse than none, because
		// the person has now spent a keystroke believing it worked.
		target := regexp.MustCompile(`class="skip" href="#([a-z-]+)"`).FindStringSubmatch(first)
		if target == nil {
			t.Errorf("%s: the skip link has no fragment target", path)
			continue
		}
		if !strings.Contains(body, `id="`+target[1]+`"`) {
			t.Errorf("%s: the skip link points at #%s, which is not on the page", path, target[1])
		}
		// The target has to be able to hold focus, or the browser moves the
		// viewport and leaves the focus ring behind in the navigation.
		anchor := regexp.MustCompile(`<[a-z]+ id="` + target[1] + `"[^>]*>`).FindString(body)
		if !strings.Contains(anchor, `tabindex="-1"`) {
			t.Errorf("%s: %q cannot take focus, so the skip scrolls without moving focus",
				path, anchor)
		}
	}
}

// Nothing sets a positive tabindex.
//
// One positive tabindex anywhere reorders the WHOLE document: every element
// carrying one comes before every element that does not, wherever it sits on
// the page. It is the one authoring mistake that cannot be contained to the
// component that made it.
func TestNothingJumpsTheQueueWithAPositiveTabindex(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")

	bad := regexp.MustCompile(`tabindex="([1-9][0-9]*)"`)
	for _, path := range []string{
		"/", "/anomalies", "/staff/new", "/accounts", "/explainers", "/task/1",
		"/connectors/aws-cost-explorer", "/search", "/sprint/plan",
	} {
		_, body, _ := h.get(t, path)
		if m := bad.FindStringSubmatch(body); m != nil {
			t.Errorf("%s sets tabindex=%s, which reorders every focusable element "+
				"on the page and not only its own", path, m[1])
		}
	}
}

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
	"github.com/TAIPANBOX/costcrew/internal/detect"
	"github.com/TAIPANBOX/costcrew/internal/estate"
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
	if _, _, err := anomaly.Run(st.DB(), time.Now(), detect.Default()); err != nil {
		t.Fatal(err)
	}
	au, err := auth.New(st, dir)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(web.New(st, au))
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
	for _, path := range []string{"/", "/anomalies", "/budgets", "/crew"} {
		code, _, loc := h.get(t, path)
		if code != http.StatusSeeOther || loc != "/login" {
			t.Errorf("GET %s without a session: %d to %q, want 303 to /login", path, code, loc)
		}
	}
	// The anomaly page too: an id is not a credential.
	code, _, loc := h.get(t, "/anomalies/A-anything")
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
		{"/crew", "Crew"},
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

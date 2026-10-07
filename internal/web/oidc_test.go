package web_test

// Invariant 74: sign-in through the organisation's identity provider, end to
// end through the console's own routes, against an identity provider running
// in this process (internal/sso/ssotest). Nothing here reaches a network.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/sso"
	"github.com/TAIPANBOX/costcrew/internal/sso/ssotest"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/web"
)

const ssoRoles = "finops-viewers=viewer;finops-operators=operator;finops-admins=admin"

// startSSO is a console with sign-in through idp and nothing seeded: these
// tests are about who gets in, and every page past the door is held elsewhere.
func startSSO(t *testing.T, idp *ssotest.Provider, only bool) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	au, err := auth.New(st, dir)
	if err != nil {
		t.Fatal(err)
	}
	// The redirect URL names the console's own address, which exists only
	// once the server is listening.
	var handler http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	cfg, err := sso.Load(sso.Inputs{
		Issuer: idp.Issuer(), ClientID: ssotest.ClientID, SecretEnv: ssotest.ClientSecret,
		RedirectURL: srv.URL + sso.CallbackPath, Roles: ssoRoles, Only: only,
	})
	if err != nil {
		t.Fatal(err)
	}
	prov, err := sso.New(st.DB(), *cfg)
	if err != nil {
		t.Fatal(err)
	}
	handler = web.New(st, au, web.Stack{Host: "costcrew.test", Recorder: st.AsRecorder(), OIDC: prov})
	return &harness{srv: srv, au: au, st: st, c: &http.Client{
		Jar: newJar(t),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// browser is another browser on the same console: its own cookie jar.
func (h *harness) browser(t *testing.T) *harness {
	t.Helper()
	return &harness{srv: h.srv, au: h.au, st: h.st, c: &http.Client{
		Jar: newJar(t),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// ssoSignIn follows one whole sign-in: the link, the provider, the callback.
// It returns the callback's status and Location.
func (h *harness) ssoSignIn(t *testing.T, idp *ssotest.Provider, g ssotest.Grant) (int, string) {
	t.Helper()
	code, _, loc := h.get(t, sso.StartPath)
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, idp.Issuer()+"/authorize?") {
		t.Fatalf("GET %s = %d %q; want a redirect to the provider", sso.StartPath, code, loc)
	}
	authCode, state := idp.Authorize(t, loc, g)
	code, _, loc = h.get(t, sso.CallbackPath+"?"+url.Values{"code": {authCode}, "state": {state}}.Encode())
	return code, loc
}

// sessionUser is who this browser is signed in as, read through auth from the
// cookie it holds, or nil.
func (h *harness) sessionUser(t *testing.T) *auth.User {
	t.Helper()
	u, _ := url.Parse(h.srv.URL)
	for _, c := range h.c.Jar.Cookies(u) {
		if c.Name == auth.SessionCookie && c.Value != "" {
			user, err := h.au.SessionUser(c.Value)
			if err != nil {
				t.Fatal(err)
			}
			return user
		}
	}
	return nil
}

func TestSignInThroughTheProviderCreatesTheAccountAndASession(t *testing.T) {
	idp := ssotest.New(t)
	h := startSSO(t, idp, false)
	code, loc := h.ssoSignIn(t, idp, ssotest.Grant{Claims: map[string]any{"groups": []string{"finops-admins"}}})
	if code != http.StatusSeeOther || loc != "/" {
		t.Fatalf("callback = %d %q; want 303 to /", code, loc)
	}
	u := h.sessionUser(t)
	if u == nil || u.Username != "alice@example.test" || u.Role != "admin" {
		t.Fatalf("signed in as %+v; want alice@example.test, admin", u)
	}
	// The session is a real one: a guarded page answers.
	if code, _, _ := h.get(t, "/accounts"); code != http.StatusOK {
		t.Fatalf("GET /accounts as the new admin = %d", code)
	}
	// The state cookie was spent.
	pu, _ := url.Parse(h.srv.URL + sso.CallbackPath)
	for _, c := range h.c.Jar.Cookies(pu) {
		if c.Name == "costcrew_oidc_state" {
			t.Error("the state cookie outlived the callback")
		}
	}
}

func TestEveryRefusedSignInLeavesNoSessionAndNoAccount(t *testing.T) {
	now := time.Now()
	cases := map[string]struct {
		g    ssotest.Grant
		want string
	}{
		"wrong audience":    {ssotest.Grant{Claims: map[string]any{"aud": "another-client"}}, sso.MsgStartAgain},
		"expired token":     {ssotest.Grant{Claims: map[string]any{"exp": now.Add(-time.Minute).Unix()}}, sso.MsgStartAgain},
		"bad signature":     {ssotest.Grant{BadSignature: true}, sso.MsgStartAgain},
		"missing nonce":     {ssotest.Grant{Drop: []string{"nonce"}}, sso.MsgStartAgain},
		"iat in the future": {ssotest.Grant{Claims: map[string]any{"iat": now.Add(time.Hour).Unix()}}, sso.MsgStartAgain},
		"unmapped group":    {ssotest.Grant{Claims: map[string]any{"groups": []string{"everyone"}}}, auth.ExternalNoAccess},
		"no groups at all":  {ssotest.Grant{Drop: []string{"groups"}}, auth.ExternalNoAccess},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			idp := ssotest.New(t)
			h := startSSO(t, idp, false)
			code, loc := h.ssoSignIn(t, idp, c.g)
			if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/login?msg=") {
				t.Fatalf("callback = %d %q; want a redirect back to /login with a message", code, loc)
			}
			if msg, _ := url.QueryUnescape(strings.TrimPrefix(loc, "/login?msg=")); msg != c.want {
				t.Errorf("the person is shown %q, want %q", msg, c.want)
			}
			if u := h.sessionUser(t); u != nil {
				t.Fatalf("a refused sign-in left a session for %s", u.Username)
			}
			if n, _ := h.au.Count(); n != 0 {
				t.Fatalf("a refused sign-in left %d account(s)", n)
			}
		})
	}
}

func TestAReplayedCallbackIsRefused(t *testing.T) {
	idp := ssotest.New(t)
	h := startSSO(t, idp, false)
	_, _, loc := h.get(t, sso.StartPath)
	authCode, state := idp.Authorize(t, loc, ssotest.Grant{})
	cb := sso.CallbackPath + "?" + url.Values{"code": {authCode}, "state": {state}}.Encode()
	// The attacker has the redirect URL and the state cookie value.
	stolen := h.browser(t)
	su, _ := url.Parse(h.srv.URL + sso.StartPath)
	stolen.c.Jar.SetCookies(su, []*http.Cookie{{Name: "costcrew_oidc_state", Value: state, Path: sso.StartPath}})

	if code, _, loc := h.get(t, cb); code != http.StatusSeeOther || loc != "/" {
		t.Fatalf("the first callback = %d %q", code, loc)
	}
	code, _, loc2 := stolen.get(t, cb)
	if code != http.StatusSeeOther || !strings.HasPrefix(loc2, "/login?msg=") || stolen.sessionUser(t) != nil {
		t.Fatalf("the replayed callback = %d %q, session %v; want refused", code, loc2, stolen.sessionUser(t))
	}
}

func TestACallbackInABrowserThatDidNotStartItIsRefused(t *testing.T) {
	idp := ssotest.New(t)
	h := startSSO(t, idp, false)
	_, _, loc := h.get(t, sso.StartPath)
	authCode, state := idp.Authorize(t, loc, ssotest.Grant{Claims: map[string]any{"groups": "finops-admins"}})
	// The victim's browser never started a sign-in; an attacker sends it the
	// attacker's own callback URL (a login CSRF).
	victim := h.browser(t)
	code, _, loc2 := victim.get(t, sso.CallbackPath+"?"+url.Values{"code": {authCode}, "state": {state}}.Encode())
	if code != http.StatusSeeOther || !strings.HasPrefix(loc2, "/login?msg=") {
		t.Fatalf("callback without the state cookie = %d %q; want refused", code, loc2)
	}
	if victim.sessionUser(t) != nil {
		t.Fatal("the victim's browser was signed in as the attacker")
	}
}

func TestARoleDowngradeAtTheProviderAppliesAtTheNextSignIn(t *testing.T) {
	idp := ssotest.New(t)
	h := startSSO(t, idp, false)
	h.ssoSignIn(t, idp, ssotest.Grant{Claims: map[string]any{"groups": []string{"finops-admins"}}})
	if u := h.sessionUser(t); u == nil || u.Role != "admin" {
		t.Fatalf("first sign-in: %+v", u)
	}
	if _, body, _ := h.get(t, "/accounts"); !strings.Contains(body, `action="/accounts/role"`) {
		t.Fatal("the admin is not served the account controls, so the check below measures nothing")
	}
	later := h.browser(t)
	later.ssoSignIn(t, idp, ssotest.Grant{Claims: map[string]any{"groups": []string{"finops-viewers"}}})
	if u := later.sessionUser(t); u == nil || u.Role != "viewer" {
		t.Fatalf("after the provider moved her to viewers: %+v", u)
	}
	// The role lives on the account, so the older session is a viewer now too.
	if u := h.sessionUser(t); u == nil || u.Role != "viewer" {
		t.Fatalf("the earlier session still carries %+v", u)
	}
	// And the page says so: the account controls an admin was served are gone.
	_, body, _ := h.get(t, "/accounts")
	if strings.Contains(body, `action="/accounts/role"`) {
		t.Fatal("a downgraded admin is still served the account controls")
	}
}

func TestRemovalFromTheGroupEndsEverySessionAtTheNextSignIn(t *testing.T) {
	idp := ssotest.New(t)
	h := startSSO(t, idp, false)
	h.ssoSignIn(t, idp, ssotest.Grant{Claims: map[string]any{"groups": []string{"finops-operators"}}})
	if h.sessionUser(t) == nil {
		t.Fatal("not signed in")
	}
	later := h.browser(t)
	code, loc := later.ssoSignIn(t, idp, ssotest.Grant{Claims: map[string]any{"groups": []string{}}})
	if code != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
		t.Fatalf("sign-in with no mapped group = %d %q; want refused", code, loc)
	}
	if u := h.sessionUser(t); u != nil {
		t.Fatalf("the session from before the removal still signs in as %s", u.Username)
	}
	if code, _, loc := h.get(t, "/accounts"); code != http.StatusSeeOther || !strings.HasPrefix(loc, "/login") {
		t.Fatalf("GET /accounts with the ended session = %d %q; want turned away", code, loc)
	}
}

func TestAProviderThatIsDownLeavesPasswordSignInWorking(t *testing.T) {
	idp := ssotest.New(t)
	h := startSSO(t, idp, false)
	if _, err := h.au.SetPassword("local-admin", "local-admin-password", "admin", false); err != nil {
		t.Fatal(err)
	}
	idp.Down()
	code, _, loc := h.get(t, sso.StartPath)
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/login?msg=") {
		t.Fatalf("GET %s with the provider down = %d %q; want back to /login with a message", sso.StartPath, code, loc)
	}
	if msg, _ := url.QueryUnescape(strings.TrimPrefix(loc, "/login?msg=")); msg != sso.MsgUnreachable {
		t.Errorf("shown %q, want %q", msg, sso.MsgUnreachable)
	}
	h.as(t, "local-admin", "local-admin-password")
}

func TestOIDCOnlyRefusesPasswordsExceptTheCommandLinesBreakGlass(t *testing.T) {
	idp := ssotest.New(t)
	h := startSSO(t, idp, true)
	if ok, err := h.au.Create("local-operator", "local-operator-pw", "operator"); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := h.au.SetPassword("glass", "glass-password-2026", "admin", false); err != nil {
		t.Fatal(err)
	}
	code, loc := h.post(t, "/login", url.Values{
		"username": {"local-operator"}, "password": {"local-operator-pw"}, "csrf": {h.csrf(t, "/login")},
	})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/login?msg=") || h.sessionUser(t) != nil {
		t.Fatalf("a local password under -oidc-only = %d %q; want refused", code, loc)
	}
	if msg, _ := url.QueryUnescape(strings.TrimPrefix(loc, "/login?msg=")); msg != auth.LoginRefused {
		t.Errorf("shown %q; want the one refusal every failed sign-in shows", msg)
	}
	h.as(t, "glass", "glass-password-2026")
	// And without -oidc-only the same local password works.
	idp2 := ssotest.New(t)
	h2 := startSSO(t, idp2, false)
	if ok, err := h2.au.Create("local-operator", "local-operator-pw", "operator"); err != nil || !ok {
		t.Fatal(err)
	}
	h2.as(t, "local-operator", "local-operator-pw")
}

func TestRegistrationIsClosedWhileAProviderIsConfigured(t *testing.T) {
	idp := ssotest.New(t)
	h := startSSO(t, idp, false)
	// No admin and no account at all: without a provider this is exactly when
	// /signup is open to the first comer (invariant 10).
	if code, _, loc := h.get(t, "/login"); code != http.StatusOK {
		t.Fatalf("GET /login on an empty installation = %d %q; want the sign-in page, not /signup", code, loc)
	}
	if code, _, loc := h.get(t, "/signup"); code != http.StatusSeeOther || !strings.Contains(loc, "registration") {
		t.Fatalf("GET /signup = %d %q; want closed", code, loc)
	}
	code, loc := h.post(t, "/signup", url.Values{
		"username": {"first"}, "password": {"first-password-1"}, "csrf": {h.csrf(t, "/login")},
	})
	if code != http.StatusSeeOther || !strings.Contains(loc, "registration") {
		t.Fatalf("POST /signup = %d %q; want closed", code, loc)
	}
	if n, _ := h.au.Count(); n != 0 {
		t.Fatalf("POST /signup created %d account(s)", n)
	}
	if code, _, loc := h.get(t, "/board"); code != http.StatusSeeOther || loc != "/login" {
		t.Fatalf("a stranger is sent to %q (%d); want /login", loc, code)
	}
}

var formActionRe = regexp.MustCompile(`<form[^>]*action="([^"]*)"`)

// The console's Content-Security-Policy says form-action 'self', and a browser
// applies form-action to the redirect after a form submission. So the way to
// the provider is a link, and every form on the page posts to this console.
func TestTheSignInPageReachesTheProviderByALinkNotAForm(t *testing.T) {
	idp := ssotest.New(t)
	h := startSSO(t, idp, false)
	code, body, _ := h.get(t, "/login")
	if code != http.StatusOK {
		t.Fatalf("GET /login = %d", code)
	}
	if !strings.Contains(body, `<a class="button" href="`+sso.StartPath+`"`) {
		t.Fatalf("the sign-in page has no link to %s:\n%s", sso.StartPath, body)
	}
	for _, m := range formActionRe.FindAllStringSubmatch(body, -1) {
		if !strings.HasPrefix(m[1], "/") || strings.HasPrefix(m[1], "//") {
			t.Errorf("a form posts to %q, off this console", m[1])
		}
	}
	if strings.Contains(body, idp.Issuer()) {
		t.Error("the sign-in page names the provider's address itself; the redirect is the server's job")
	}
	// The start is a GET that answers a redirect: a navigation, not a submission.
	if code, _, loc := h.get(t, sso.StartPath); code != http.StatusSeeOther || !strings.HasPrefix(loc, idp.Issuer()) {
		t.Fatalf("GET %s = %d %q", sso.StartPath, code, loc)
	}
}

func TestWithNoProviderTheOIDCRoutesAreNotThere(t *testing.T) {
	h := startBare(t)
	for _, p := range []string{sso.StartPath, sso.CallbackPath + "?code=x&state=y"} {
		if code, _, _ := h.get(t, p); code != http.StatusNotFound {
			t.Errorf("GET %s with no provider configured = %d, want 404", p, code)
		}
	}
	if _, body, _ := h.get(t, "/login"); strings.Contains(body, sso.StartPath) {
		t.Error("the sign-in page offers the provider when none is configured")
	}
}

func TestTheClientSecretAppearsInNoPageAndNoJournalLine(t *testing.T) {
	idp := ssotest.New(t)
	h := startSSO(t, idp, false)
	h.ssoSignIn(t, idp, ssotest.Grant{Claims: map[string]any{"groups": "finops-admins"}})
	h.browser(t).ssoSignIn(t, idp, ssotest.Grant{Claims: map[string]any{"aud": "x"}})
	idp.TokenRedirect = "http://127.0.0.1:1/never"
	h.browser(t).ssoSignIn(t, idp, ssotest.Grant{})
	var seen []string
	for _, p := range []string{"/login", "/accounts", "/audit"} {
		resp, err := h.c.Get(h.srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		seen = append(seen, string(b))
	}
	j, err := os.ReadFile(h.st.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	seen = append(seen, string(j))
	if !strings.Contains(string(j), "external_sign_in_failed") {
		t.Fatal("the refused sign-ins were not journaled, so this measured nothing")
	}
	for _, s := range seen {
		if strings.Contains(s, ssotest.ClientSecret) {
			t.Fatal("the client secret appears in a page or the journal")
		}
	}
}

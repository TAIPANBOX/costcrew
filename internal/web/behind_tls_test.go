package web_test

// Invariant 49: -behind-tls marks every cookie this server issues Secure,
// because the documented deployment (-addr's own help text: put a proxy in
// front for TLS) means this process only ever sees plain HTTP on loopback,
// so r.TLS is nil on every request even when the browser's own connection is
// HTTPS end to end. Before this, Secure was set from r.TLS != nil alone, so
// in that documented shape the session cookie was never Secure.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/web"
)

// authOnly builds the console with only the auth tables present: enough to
// exercise /signup, /login and /logout, which is everything a cookie's own
// Secure attribute needs. The full estate harness in web_test.go seeds far
// more than these three routes read, and every extra seed step is one more
// thing that could hide an unrelated failure inside a test about a cookie.
func authOnly(t *testing.T, behindTLS bool) *httptest.Server {
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
	srv := httptest.NewServer(web.New(st, au, web.Stack{
		Host: "costcrew.test", Recorder: st.AsRecorder(), BehindTLS: behindTLS,
	}))
	t.Cleanup(srv.Close)
	return srv
}

// noRedirectClient never follows a redirect and carries no cookie jar, so the
// response under test is always the one that actually set the cookie, not
// whatever page the redirect lands on. A jar-based client is deliberately not
// used here: net/http/cookiejar enforces the Secure attribute itself (it
// will not replay a Secure cookie over a plain http:// origin), which would
// make a bug in THIS server's own Secure bit invisible behind the jar's.
func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func rawGet(t *testing.T, srv *httptest.Server, path string) string {
	t.Helper()
	resp, err := noRedirectClient().Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func rawPostForm(t *testing.T, srv *httptest.Server, path string, form url.Values) *http.Response {
	t.Helper()
	resp, err := noRedirectClient().PostForm(srv.URL+path, form)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func csrfFrom(t *testing.T, srv *httptest.Server, path string) string {
	t.Helper()
	body := rawGet(t, srv, path)
	m := csrfRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("GET %s carried no csrf field", path)
	}
	return m[1]
}

// signUpRaw creates the first account, becoming this installation's admin,
// and returns the response so a caller that cares about ITS cookie can look.
func signUpRaw(t *testing.T, srv *httptest.Server, user, pw string) *http.Response {
	t.Helper()
	return rawPostForm(t, srv, "/signup", url.Values{
		"username": {user}, "password": {pw}, "csrf": {csrfFrom(t, srv, "/signup")},
	})
}

// sessionCookie picks out the session cookie from a response's own Set-Cookie
// headers, using net/http's own parser rather than a regexp over the raw
// header text, so this test is reading exactly what a browser would.
func sessionCookie(t *testing.T, resp *http.Response, label string) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookie {
			return c
		}
	}
	t.Fatalf("%s set no %s cookie", label, auth.SessionCookie)
	return nil
}

// (a) With the flag on, a login over plain HTTP issues a session cookie with
// Secure=true.
func TestLoginOverPlainHTTPIsSecureWhenBehindTLS(t *testing.T) {
	srv := authOnly(t, true)
	signUpRaw(t, srv, "owner", "owner-password-2026").Body.Close()

	resp := rawPostForm(t, srv, "/login", url.Values{
		"username": {"owner"}, "password": {"owner-password-2026"},
		"csrf": {csrfFrom(t, srv, "/login")},
	})
	defer resp.Body.Close()

	c := sessionCookie(t, resp, "POST /login")
	if !c.Secure {
		t.Errorf("session cookie from a login over plain HTTP with -behind-tls on: Secure=%v, want true", c.Secure)
	}
}

// (b) With the flag off, nothing changes: Secure=false over plain HTTP. The
// negative control -- without it, (a) passing would prove nothing, because
// the flag might not be reaching this decision at all.
func TestLoginOverPlainHTTPStaysInsecureWithoutBehindTLS(t *testing.T) {
	srv := authOnly(t, false)
	signUpRaw(t, srv, "owner", "owner-password-2026").Body.Close()

	resp := rawPostForm(t, srv, "/login", url.Values{
		"username": {"owner"}, "password": {"owner-password-2026"},
		"csrf": {csrfFrom(t, srv, "/login")},
	})
	defer resp.Body.Close()

	c := sessionCookie(t, resp, "POST /login")
	if c.Secure {
		t.Errorf("session cookie from a login over plain HTTP with -behind-tls off: Secure=%v, want false (unchanged behaviour)", c.Secure)
	}
}

// (c) Every cookie the server sets carries Secure under the flag, not only
// the session one: enumerate the Set-Cookie headers of every response that
// sets one (signup, login, logout) rather than trusting the session cookie
// alone to stand for all of them.
func TestEveryCookieCarriesSecureUnderTheFlag(t *testing.T) {
	srv := authOnly(t, true)

	checked := 0
	check := func(resp *http.Response, label string) {
		t.Helper()
		defer resp.Body.Close()
		cookies := resp.Cookies()
		if len(cookies) == 0 {
			t.Fatalf("%s set no cookie at all; nothing to enumerate", label)
		}
		for _, c := range cookies {
			checked++
			if !c.Secure {
				t.Errorf("%s set %s with Secure=false while -behind-tls is on", label, c.Name)
			}
		}
	}

	check(signUpRaw(t, srv, "owner", "owner-password-2026"), "POST /signup")

	loginCSRF := csrfFrom(t, srv, "/login")
	check(rawPostForm(t, srv, "/login", url.Values{
		"username": {"owner"}, "password": {"owner-password-2026"}, "csrf": {loginCSRF},
	}), "POST /login")

	check(rawPostForm(t, srv, "/logout", url.Values{}), "POST /logout")

	if checked < 3 {
		t.Fatalf("only enumerated %d Set-Cookie header(s) across signup, login and logout; "+
			"this measured nothing", checked)
	}
}

// A future cookie added anywhere in this package must go through setCookie,
// the one place -behind-tls's Secure decision is made, or -behind-tls would
// simply never reach it. This walks the package's own non-test source for
// http.SetCookie call sites the same way guarded_test.go and web_test.go
// walk it for routes and CSRF checks, and requires there to be exactly one:
// the one inside setCookie itself.
func TestEveryCookieGoesThroughOneSecurePosture(t *testing.T) {
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no .go file found in internal/web; this scan measured nothing")
	}
	count := 0
	for _, f := range entries {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		count += strings.Count(string(b), "http.SetCookie(")
	}
	if count != 1 {
		t.Errorf("found %d call site(s) of http.SetCookie in internal/web's own source, want exactly 1 "+
			"(inside Server.setCookie); a second call site is a cookie -behind-tls never reaches", count)
	}
}

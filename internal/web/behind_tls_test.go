package web_test

// Invariant 49: -behind-tls marks every cookie this server issues Secure,
// because the documented deployment (-addr's own help text: put a proxy in
// front for TLS) means this process only ever sees plain HTTP on loopback,
// so r.TLS is nil on every request even when the browser's own connection is
// HTTPS end to end. Before this, Secure was set from r.TLS != nil alone, so
// in that documented shape the session cookie was never Secure.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
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
//
// It starts from srv.Client() rather than a bare &http.Client{}: for a plain
// httptest.Server that is the same as the zero value, but for
// httptest.NewTLSServer (finding 3, TestLoginOverRealTLSIsSecureWithoutTheFlag)
// it is the one client configured to trust that server's own self-signed
// certificate, so the same helper serves both kinds of server.
func noRedirectClient(srv *httptest.Server) *http.Client {
	c := *srv.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &c
}

func rawGet(t *testing.T, srv *httptest.Server, path string) string {
	t.Helper()
	resp, err := noRedirectClient(srv).Get(srv.URL + path)
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
	resp, err := noRedirectClient(srv).PostForm(srv.URL+path, form)
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
// simply never reach it. This is a TEST asserting that today's source holds
// that shape, not a compiler-enforced guarantee (invariant 49's own wording
// was softened to match, Fable finding 2): it walks the package's own
// non-test source with go/parser and go/ast, the same way guarded_test.go
// and web_test.go walk it textually for routes and CSRF checks, and requires
// two things neither of which a plain grep for the literal "http.SetCookie("
// caught (Fable finding 2, both measured against this exact test before this
// fix): exactly one occurrence of the bare identifier SetCookie -- which
// also catches an alias, `sc := http.SetCookie; sc(w, c)`, the same
// SelectorExpr as a direct call -- and zero occurrences of the literal
// header name "Set-Cookie", which catches a handler writing the header
// directly, `w.Header().Add("Set-Cookie", c.String())`, bypassing
// http.SetCookie (and this package's own setCookie) entirely. Parsing rather
// than a plain string count is also why a comment mentioning "http.SetCookie"
// in prose, such as this file's or setCookie's own doc comment, does not
// count: go/ast never walks into a *ast.CommentGroup's text as an Ident or a
// BasicLit.
func TestEveryCookieGoesThroughOneSecurePosture(t *testing.T) {
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no .go file found in internal/web; this scan measured nothing")
	}
	identCount, literalCount := 0, 0
	fset := token.NewFileSet()
	for _, f := range entries {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", f, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				if x.Name == "SetCookie" {
					identCount++
				}
			case *ast.BasicLit:
				if x.Kind == token.STRING {
					if v, err := strconv.Unquote(x.Value); err == nil && strings.EqualFold(v, "Set-Cookie") {
						literalCount++
					}
				}
			}
			return true
		})
	}
	if identCount != 1 {
		t.Errorf("found %d occurrence(s) of the identifier SetCookie in internal/web's own "+
			"non-test source, want exactly 1 (inside Server.setCookie); a second one -- a direct "+
			"call or an alias such as `sc := http.SetCookie` -- is a cookie -behind-tls never reaches",
			identCount)
	}
	if literalCount != 0 {
		t.Errorf("found %d occurrence(s) of the literal \"Set-Cookie\" in internal/web's own "+
			"non-test source, want 0; a handler writing the header directly (for example "+
			"w.Header().Add(\"Set-Cookie\", ...)) sets a cookie -behind-tls never reaches", literalCount)
	}
}

// (e) Even with the flag off, a real TLS handshake this process itself
// terminates marks the cookie Secure through r.TLS != nil alone: the branch
// setCookie's own comment describes ("r.TLS != nil is true only when this
// process terminated the TLS connection itself") but that no test before
// this one actually exercised -- every other case here talks over plain
// HTTP, where r.TLS is always nil regardless of -behind-tls, so a mutant
// dropping the r.TLS half of `c.Secure = s.behindTLS || r.TLS != nil`
// (leaving only s.behindTLS) passed all four of them (Fable finding 3,
// measured). httptest.NewTLSServer is the one place in this suite that
// actually terminates TLS.
func TestLoginOverRealTLSIsSecureWithoutTheFlag(t *testing.T) {
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
	srv := httptest.NewTLSServer(web.New(st, au, web.Stack{
		Host: "costcrew.test", Recorder: st.AsRecorder(), BehindTLS: false,
	}))
	t.Cleanup(srv.Close)

	signUpRaw(t, srv, "owner", "owner-password-2026").Body.Close()

	resp := rawPostForm(t, srv, "/login", url.Values{
		"username": {"owner"}, "password": {"owner-password-2026"},
		"csrf": {csrfFrom(t, srv, "/login")},
	})
	defer resp.Body.Close()

	c := sessionCookie(t, resp, "POST /login over a real TLS handshake")
	if !c.Secure {
		t.Errorf("session cookie from a login this process itself TLS-terminated, "+
			"-behind-tls off: Secure=%v, want true (r.TLS != nil)", c.Secure)
	}
}

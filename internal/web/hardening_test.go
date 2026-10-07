package web_test

// Invariant 59: the HTTP surface is hardened at its edge. Three defects, found
// by reading the code on 2026-10-07, each with a test that was red first:
//
//  1. /export/results.html wrote values that can come from an imported file
//     into a downloadable page with a raw %s.
//  2. No response carried a Content-Security-Policy, X-Frame-Options,
//     X-Content-Type-Options or Referrer-Policy.
//  3. No request body was capped (only /intake/check read 2 MB of one, and
//     even that let a multipart upload spool to disk without a bound), and the
//     server set a header timeout and nothing else.

import (
	"bytes"
	"crypto/tls"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/web"
)

// ---------------------------------------------------------------- 1: export

// plantHostileAnomaly puts into the anomalies table exactly what an imported
// FOCUS file can put there: connectors/tokenfusefocus.go takes ServiceName and
// x_agent_id from the file's own columns, and anomaly detection copies them
// into service and caused_by. The row is planted directly because the thing
// under test is what the export does with a row, not how the row got there.
func plantHostileAnomaly(t *testing.T, h *harness, payload func(field string) string) {
	t.Helper()
	_, err := h.st.DB().Exec(`INSERT INTO anomalies(
		id, source, team, service, day, direction, amount_cents, baseline_cents,
		excess_cents, z, rule, rule_version, driver, caused_by, caused_by_kind,
		handled_by, state, reason, detected_at)
		VALUES('A-hostile0001', ?, 'ml', ?, ?, 'up', 999999999999, 1, 999999999998,
		9.9, 'r', 'v1', '', ?, ?, '', 'open', '', '2026-09-01T00:00:00Z')`,
		payload("source"), payload("service"), payload("day"),
		payload("caused_by"), payload("kind"))
	if err != nil {
		t.Fatal(err)
	}
}

func hostile(field string) string { return "<script>alert(" + field + ")</script>" }

// A CSV row whose service is a script tag must reach the downloadable report as
// text. The report is a file a person opens in a browser, often after saving
// it, so there is no console around it and no later chance to escape.
func TestResultsExportEscapesWhatAnImportedRowCarries(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	plantHostileAnomaly(t, h, hostile)

	code, body, _ := h.get(t, "/export/results.html")
	if code != 200 {
		t.Fatalf("GET /export/results.html: %d", code)
	}
	// The table row must be there at all, or an absent tag proves nothing.
	if !strings.Contains(body, "alert(service)") {
		t.Fatalf("the planted anomaly never reached the export, so this test "+
			"measured nothing:\n%s", body)
	}
	for _, field := range []string{"source", "service", "day", "caused_by", "kind"} {
		raw := hostile(field)
		if strings.Contains(body, raw) {
			t.Errorf("%s is written into /export/results.html raw: %q appears verbatim",
				field, raw)
		}
		escaped := "&lt;script&gt;alert(" + field + ")&lt;/script&gt;"
		if !strings.Contains(body, escaped) {
			t.Errorf("%s is not shown escaped: want %q in the export", field, escaped)
		}
	}
	if strings.Contains(body, "<script") {
		t.Errorf("the export carries a <script tag after a hostile import")
	}
}

// The same page, with no hostile row, must still say what it said: this is the
// guard against a fix that escapes by deleting.
func TestResultsExportStillSaysWhatItSaid(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	code, body, _ := h.get(t, "/export/results.html")
	if code != 200 {
		t.Fatalf("GET /export/results.html: %d", code)
	}
	for _, want := range []string{
		"<!doctype html>", "CostCrew results", "The headline", "Found this period",
		"Still unexplained", "By desk", "Decisions needed", "</main></body></html>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the export lost %q", want)
		}
	}
}

// The writer of the downloadable page may not be a hand-built string. The
// check is structural: exportResultsHTML must not call any fmt.Fprint*, any
// io.WriteString or any .Write of its own, so the only way a value reaches the
// page is through html/template, which escapes by context. A repo-wide rule
// ("no %s of a non-constant into HTML") was tried and rejected: authPage
// interpolates two deliberately pre-escaped fragments, and pages.go builds SVG
// path data with %.1f, so such a rule would fire on code that is correct and
// get deleted. This one names the function whose defect it is.
func TestResultsExportHasNoHandWrittenHTMLWriter(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "practice.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "exportResultsHTML" {
			fn = f
		}
	}
	if fn == nil {
		t.Fatal("exportResultsHTML is not in practice.go; this test measured nothing")
	}
	executes := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		name := sel.Sel.Name
		switch {
		case strings.HasPrefix(name, "Fprint"), name == "WriteString", name == "Write",
			name == "Sprintf", name == "Sprint":
			t.Errorf("exportResultsHTML calls %s at %s: a value written this way is "+
				"not escaped; render through the html/template instead",
				name, fset.Position(call.Pos()))
		case name == "Execute" || name == "ExecuteTemplate":
			executes++
		}
		return true
	})
	if executes != 1 {
		t.Errorf("exportResultsHTML executes a template %d times, want exactly 1", executes)
	}
}

// And the template package itself: text/template does not escape, and an import
// of it anywhere in the package that serves pages is how this class comes back.
func TestNoPageIsBuiltWithTextTemplate(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no source found: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			if imp.Path.Value == `"text/template"` {
				t.Errorf("%s imports text/template, which does not escape", f)
			}
		}
	}
}

// ------------------------------------------------------- 2: security headers

var routeTable = regexp.MustCompile(`(?m)^\s*s\.mux\.HandleFunc\("(GET|POST) ([^"]+)"`)

// every route in the route table, public or not, plus the paths ServeHTTP
// answers before the mux and one that does not exist.
func allRoutes(t *testing.T) (gets, posts []string) {
	t.Helper()
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range routeTable.FindAllStringSubmatch(string(src), -1) {
		if m[1] == "GET" {
			gets = append(gets, concrete(m[2]))
		} else {
			posts = append(posts, concrete(m[2]))
		}
	}
	if len(gets) < 50 || len(posts) < 30 {
		t.Fatalf("found %d GET and %d POST routes in server.go; the scan is broken",
			len(gets), len(posts))
	}
	gets = append(gets, "/a/agent://costcrew.test/x", "/i/x", "/no-such-page")
	return gets, posts
}

// What every response must carry, whatever route produced it. The problems are
// returned, not reported one by one: a missing middleware fails on every route
// at once, and four hundred lines say less than one line per header.
func securityHeaderProblems(resp *http.Response, wantHSTS bool) []string {
	var out []string
	get := func(k string) string { return resp.Header.Get(k) }
	if got := get("X-Content-Type-Options"); got != "nosniff" {
		out = append(out, "X-Content-Type-Options = "+strconv.Quote(got)+", want nosniff")
	}
	if got := get("X-Frame-Options"); got != "DENY" {
		out = append(out, "X-Frame-Options = "+strconv.Quote(got)+", want DENY")
	}
	if got := get("Referrer-Policy"); got != "same-origin" {
		out = append(out, "Referrer-Policy = "+strconv.Quote(got)+", want same-origin")
	}
	if get("Content-Security-Policy") == "" {
		out = append(out, "no Content-Security-Policy")
	}
	if got := get("Strict-Transport-Security"); wantHSTS && got == "" {
		out = append(out, "no Strict-Transport-Security where TLS is in front")
	} else if !wantHSTS && got != "" {
		out = append(out, "Strict-Transport-Security = "+strconv.Quote(got)+" on a plain-HTTP deployment")
	}
	return out
}

func requireSecurityHeaders(t *testing.T, what string, resp *http.Response, wantHSTS bool) {
	t.Helper()
	for _, p := range securityHeaderProblems(resp, wantHSTS) {
		t.Errorf("%s: %s", what, p)
	}
}

// Every route, as a stranger and as a signed-in user, GET and POST. A header
// set by one handler is a header the next handler can forget, so the check is
// over the whole table, including the answers that are errors.
func TestEveryRouteCarriesTheSecurityHeaders(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	stranger := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	gets, posts := allRoutes(t)
	token := h.csrf(t, "/cadence")

	failures := map[string][]string{} // problem -> the requests that had it
	checked := 0
	record := func(what string, resp *http.Response, err error) {
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		checked++
		for _, p := range securityHeaderProblems(resp, false) {
			failures[p] = append(failures[p], what)
		}
	}
	for _, p := range gets {
		resp, err := stranger.Get(h.srv.URL + p)
		record("stranger GET "+p, resp, err)
		resp, err = h.c.Get(h.srv.URL + p)
		record("member GET "+p, resp, err)
	}
	for _, p := range posts {
		resp, err := stranger.PostForm(h.srv.URL+p, url.Values{})
		record("stranger POST "+p, resp, err)
		resp, err = h.c.PostForm(h.srv.URL+p, url.Values{"csrf": {token}})
		record("member POST "+p, resp, err)
	}
	for problem, who := range failures {
		t.Errorf("%d of %d responses: %s (first: %s)", len(who), checked, problem, who[0])
	}
	if checked < 2*(len(gets)+len(posts)) {
		t.Fatalf("checked %d responses, want %d", checked, 2*(len(gets)+len(posts)))
	}
	t.Logf("checked %d responses over %d GET and %d POST routes", checked, len(gets), len(posts))
}

// The policy must not undo itself. Every directive is checked for what it
// refuses, because a policy that allows scripts is a header that looks like a
// defence.
func TestTheContentSecurityPolicyAllowsNoScript(t *testing.T) {
	h := start(t)
	resp, err := h.c.Get(h.srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	dirs := map[string]string{}
	for _, d := range strings.Split(csp, ";") {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		name, val, _ := strings.Cut(d, " ")
		dirs[name] = val
	}
	want := map[string]string{
		"default-src":     "'none'",
		"frame-ancestors": "'none'",
		"form-action":     "'self'",
		"base-uri":        "'none'",
	}
	for k, v := range want {
		if dirs[k] != v {
			t.Errorf("CSP %s = %q, want %q (policy: %s)", k, dirs[k], v, csp)
		}
	}
	if _, set := dirs["script-src"]; set {
		t.Errorf("CSP names a script-src (%q): no page here runs a script, so none is allowed",
			dirs["script-src"])
	}
	for k, v := range dirs {
		if strings.Contains(v, "unsafe-eval") || strings.Contains(v, "*") ||
			strings.Contains(v, "http:") || strings.Contains(v, "https:") {
			t.Errorf("CSP %s = %q is wider than this console needs", k, v)
		}
		if strings.Contains(v, "'unsafe-inline'") && k != "style-src" {
			t.Errorf("CSP %s allows inline content; only style-src may, for the "+
				"style= attributes the templates carry", k)
		}
	}
	if dirs["style-src"] != "'self' 'unsafe-inline'" {
		t.Errorf("CSP style-src = %q, want 'self' 'unsafe-inline'", dirs["style-src"])
	}
}

// HSTS is a promise about TLS, so it is made only where TLS is in front: the
// operator's -behind-tls, or a handshake this process terminated itself.
func TestStrictTransportSecurityFollowsTheCookiePosture(t *testing.T) {
	build := func(behind bool) *web.Server {
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
		return web.New(st, au, web.Stack{Host: "costcrew.test",
			Recorder: st.AsRecorder(), BehindTLS: behind})
	}
	do := func(s *web.Server, tlsState *tls.ConnectionState) *http.Response {
		req := httptest.NewRequest("GET", "/healthz", nil)
		req.TLS = tlsState
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec.Result()
	}
	requireSecurityHeaders(t, "plain, no flag", do(build(false), nil), false)
	requireSecurityHeaders(t, "-behind-tls", do(build(true), nil), true)
	requireSecurityHeaders(t, "TLS terminated here", do(build(false), &tls.ConnectionState{}), true)

	hsts := do(build(true), nil).Header.Get("Strict-Transport-Security")
	if !strings.HasPrefix(hsts, "max-age=") || strings.Contains(hsts, "includeSubDomains") ||
		strings.Contains(hsts, "preload") {
		t.Errorf("Strict-Transport-Security = %q: want a max-age and nothing that "+
			"reaches beyond this host", hsts)
	}
}

// The policy forbids every script, so no page may rely on one. Every page the
// console serves is read for the things a script-less policy would silently
// break: a <script>, an inline event handler, a javascript: URL, and any
// resource that is not this origin's stylesheet. A CSP that breaks the UI is a
// defect, and the way it breaks is a button that does nothing.
var scriptish = regexp.MustCompile(`(?i)<script|\son[a-z]+\s*=|javascript:|<iframe|<object|<embed|@import|<img|<form[^>]*\saction="https?:`)

func TestNoPageReliesOnWhatThePolicyForbids(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	gets, _ := allRoutes(t)
	pages := 0
	for _, p := range gets {
		if strings.HasPrefix(p, "/export/") || strings.HasPrefix(p, "/intake/template") ||
			strings.HasSuffix(p, ".json") {
			continue
		}
		code, body, _ := h.get(t, p)
		if code != 200 {
			continue
		}
		pages++
		if loc := scriptish.FindString(body); loc != "" {
			t.Errorf("GET %s relies on %q, which the Content-Security-Policy forbids", p, loc)
		}
		for _, m := range regexp.MustCompile(`<link[^>]*>`).FindAllString(body, -1) {
			if !strings.Contains(m, `href="/static/app.css"`) {
				t.Errorf("GET %s loads %s, which is not this origin's stylesheet", p, m)
			}
		}
	}
	if pages < 25 {
		t.Fatalf("only %d pages came back 200; the scan measured too little", pages)
	}

	// The templates themselves, for pages the fixture does not reach.
	files, _ := filepath.Glob("templates/*.html")
	if len(files) < 30 {
		t.Fatalf("found %d templates; the scan is broken", len(files))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if loc := scriptish.FindString(string(b)); loc != "" {
			t.Errorf("%s relies on %q, which the Content-Security-Policy forbids", f, loc)
		}
	}
}

// Sign out was the one control that needed a script: a link whose onclick
// submitted a hidden form. Without a script it must still sign somebody out.
func TestSignOutWorksWithoutAScript(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	_, body, _ := h.get(t, "/")
	m := regexp.MustCompile(`(?s)<form[^>]*action="/logout"[^>]*>(.*?)</form>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("the layout has no logout form")
	}
	if !regexp.MustCompile(`<button[^>]*type="submit"[^>]*>\s*Sign out`).MatchString(m[1]) {
		t.Errorf("the logout form has no visible submit button saying Sign out: %s", m[1])
	}
	if regexp.MustCompile(`<form[^>]*action="/logout"[^>]*\shidden`).MatchString(body) {
		t.Errorf("the logout form is hidden, so nothing without a script can submit it")
	}
	code, loc := h.post(t, "/logout", url.Values{"csrf": {h.csrf(t, "/")}})
	if code != http.StatusSeeOther {
		t.Fatalf("POST /logout: %d", code)
	}
	_ = loc
	if code, _, _ := h.get(t, "/board"); code != http.StatusSeeOther {
		t.Errorf("after signing out /board answered %d, want a redirect", code)
	}
}

// ---------------------------------------------------------- 3: bodies, limits

func cadenceState(t *testing.T, h *harness) (bool, string) {
	t.Helper()
	on, ceiling, _, _, err := crew.CadenceSettings(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	return on, ceiling.String()
}

func sendBody(t *testing.T, h *harness, path, contentType string, body io.Reader, length int64) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", h.srv.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = length // -1 sends the body chunked, with no declared size
	req.Header.Set("Content-Type", contentType)
	resp, err := h.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// padded is a cadence form with a field the handler never reads, as large as
// asked. Without a cap the handler reads all of it and turns the switch on;
// with one, the request never reaches the handler.
func padded(csrf string, pad int) []byte {
	var b bytes.Buffer
	b.WriteString("csrf=" + csrf + "&enabled=on&ceiling=1.00&pad=")
	b.Write(bytes.Repeat([]byte("a"), pad))
	return b.Bytes()
}

const form = "application/x-www-form-urlencoded"

func TestAnOversizedPostIsRefusedAndChangesNothing(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	token := h.csrf(t, "/cadence")

	for _, c := range []struct {
		name  string
		size  int
		chunk bool // no declared length: the body is sent chunked
	}{
		{"declared", 1<<20 + 1, false},
		{"far over", 8 << 20, false},
		{"chunked", 1<<20 + 1, true},
	} {
		body := padded(token, c.size)
		length := int64(len(body))
		var r io.Reader = bytes.NewReader(body)
		if c.chunk {
			r, length = io.MultiReader(r), -1 // MultiReader hides the length
		}
		resp := sendBody(t, h, "/cadence", form, r, length)
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: a %d byte POST answered %d, want 413", c.name, len(body), resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("%s: the refusal is %q, want a plain message", c.name, ct)
		}
		if !strings.Contains(string(msg), "too large") {
			t.Errorf("%s: the refusal says %q, want it to say the request is too large", c.name, msg)
		}
		requireSecurityHeaders(t, c.name+" 413", resp, false)
		if on, _ := cadenceState(t, h); on {
			t.Errorf("%s: the oversized POST was refused with 413 but the cadence switch "+
				"is on: the handler ran", c.name)
		}
	}
}

func TestANormalPostStillWorks(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	token := h.csrf(t, "/cadence")

	// Exactly at the cap is inside it.
	prefix := len(padded(token, 0))
	body := padded(token, 1<<20-prefix)
	if len(body) != 1<<20 {
		t.Fatalf("test body is %d bytes, want exactly 1 MiB", len(body))
	}
	resp := sendBody(t, h, "/cadence", form, bytes.NewReader(body), int64(len(body)))
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a POST of exactly 1 MiB answered %d, want the handler's redirect", resp.StatusCode)
	}
	if on, ceiling := cadenceState(t, h); !on || ceiling != "1.00" {
		t.Errorf("cadence = on %v, ceiling %s after a normal POST; the handler did not run", on, ceiling)
	}
}

func multipartUpload(t *testing.T, token string, file []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("csrf", token)
	fw, err := mw.CreateFormFile("upload", "budgets.csv")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(file)
	mw.Close()
	return &buf, mw.FormDataContentType()
}

// budgetsFile is a valid budgets CSV padded with blank lines to exactly n
// bytes. Blank lines are skipped by the reader, so what is being sized is the
// upload and not the work of parsing it.
func budgetsFile(n int) []byte {
	head := "platform,team,month,budget_usd\naws,sre-platform,2026-09,960\n"
	return append([]byte(head), bytes.Repeat([]byte("\n"), n-len(head))...)
}

func budgetRows(t *testing.T, h *harness) int {
	t.Helper()
	var n int
	if err := h.st.DB().QueryRow(`SELECT COUNT(*) FROM budgets`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

const maxIntake = 2 << 20 // internal/web/intake.go's own cap on the file

// The intake keeps its own, larger cap, and the global one does not apply to it.
func TestIntakeStillAcceptsAFileUpToItsOwnCap(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	token := h.csrf(t, "/intake")

	buf, ct := multipartUpload(t, token, budgetsFile(maxIntake))
	if buf.Len() <= 1<<20 {
		t.Fatalf("test body is %d bytes: it does not reach beyond the global cap", buf.Len())
	}
	size := buf.Len()
	resp := sendBody(t, h, "/intake/check", ct, buf, int64(size))
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("a %d byte upload (a file of exactly %d) answered %d, want the preview",
			size, maxIntake, resp.StatusCode)
	}
	if !strings.Contains(string(body), "sre-platform") {
		t.Errorf("the preview does not show the uploaded row")
	}
}

// One byte over the file's cap used to be read as the first 2 MB of it, a file
// cut mid-row and previewed as if it were whole.
func TestIntakeRefusesAFileOverItsCapInsteadOfCuttingIt(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	token := h.csrf(t, "/intake")

	buf, ct := multipartUpload(t, token, budgetsFile(maxIntake+1))
	resp := sendBody(t, h, "/intake/check", ct, buf, int64(buf.Len()))
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a file of %d bytes answered %d, want a redirect with the reason",
			maxIntake+1, resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "msg=") ||
		!strings.Contains(strings.ToLower(loc), "2+mb") && !strings.Contains(strings.ToLower(loc), "2%20mb") {
		t.Errorf("Location %q does not say the file is over 2 MB", loc)
	}
}

func TestAnOversizedIntakeUploadIsRefusedAndChangesNothing(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	token := h.csrf(t, "/intake")
	before := budgetRows(t, h)

	buf, ct := multipartUpload(t, token, budgetsFile(maxIntake+(1<<20)))
	size := buf.Len()
	resp := sendBody(t, h, "/intake/check", ct, buf, int64(size))
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a %d byte upload answered %d, want 413", size, resp.StatusCode)
	}
	if got := budgetRows(t, h); got != before {
		t.Errorf("budgets went from %d to %d rows", before, got)
	}
}

// /intake/apply carries the file back inside a form field, URL-encoded, and a
// browser turns each newline into %0D%0A: six bytes for one. Its cap is what
// that honestly takes for a file of exactly the file cap, not the global 1 MiB
// that would refuse a legitimate round trip.
func TestIntakeApplyAcceptsTheEncodedFileItCheckedAndRefusesMore(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	token := h.csrf(t, "/intake")

	file := string(budgetsFile(maxIntake))
	v := url.Values{"csrf": {token}, "file": {strings.ReplaceAll(file, "\n", "\r\n")}, "fingerprint": {"x"}}
	body := v.Encode()
	if len(body) < 5<<20 {
		t.Fatalf("encoded body is %d bytes; this test is meant to sit between 5 MiB and the cap", len(body))
	}
	resp := sendBody(t, h, "/intake/apply", form, strings.NewReader(body), int64(len(body)))
	resp.Body.Close()
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		t.Errorf("a file of exactly the cap, encoded as a browser encodes it (%d bytes), "+
			"was refused as too large", len(body))
	}

	over := padded(token, 13<<20)
	resp = sendBody(t, h, "/intake/apply", form, bytes.NewReader(over), int64(len(over)))
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a %d byte POST to /intake/apply answered %d, want 413", len(over), resp.StatusCode)
	}
}

// The login form is the one a stranger can reach, so it is the one that
// matters most to cap.
func TestAStrangerCannotMakeTheLoginFormReadMegabytes(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	stranger := &harness{srv: h.srv, c: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
	body := []byte("username=owner&password=" + strconv.Itoa(0) + strings.Repeat("a", 2<<20))
	resp := sendBody(t, stranger, "/login", form, bytes.NewReader(body), int64(len(body)))
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a 2 MiB POST to /login answered %d, want 413", resp.StatusCode)
	}
}

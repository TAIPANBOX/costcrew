package web_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Routes that answer without a session, and the reason each one may.
//
// Everything else on this console is behind a login. This list exists so that
// staying outside it is a DECISION somebody wrote down, not an omission in a
// handler nobody looked at twice.
var publicRoutes = map[string]string{
	"/login":    "the way in",
	"/logout":   "must work from a session already broken",
	"/healthz":  "a load balancer has no password",
	"/static/":  "stylesheet and icons, no estate data",
	"/signup":   "the first-run setup, which by definition precedes any account",
	"/calendar": "an alias that redirects to /board, where the guard is",
	"/stats":    "an alias that redirects to /staff, where the guard is",
}

// turnedAway is where the guard sends a stranger. A fresh install has no
// account yet, so it sends them to set one up rather than to sign in; both
// are the guard doing its job, and a test that only knew about /login would
// have to be disabled on exactly the install where it matters most.
func turnedAway(loc string) bool {
	return strings.HasPrefix(loc, "/login") || strings.HasPrefix(loc, "/signup")
}

// Registrations, not prose.
//
// This matched HandleFunc anywhere in the file, including inside a comment, so
// documenting a route in a sentence made this test demand that the route exist
// and be guarded. Found by scripts/gates-have-teeth.sh, whose pass-case plants
// exactly that comment: a gate that fires on prose gets deleted the first time
// somebody writes a helpful line above a handler.
var routeRe = regexp.MustCompile(`(?m)^\s*s\.mux\.HandleFunc\("(GET|POST) ([^"]+)"`)

// Every route requires a session unless it is on the list above.
//
// The failure this catches is the quiet kind: a handler added without the
// guard line every neighbour has. It happened to the CSV template, which
// served the estate's platform and team vocabulary to anyone who could reach
// the port, because it was a download rather than a page and so did not look
// like something that needed a login.
func TestEveryRouteRequiresASession(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	h := startWith(t, false) // no login: that is the point
	seen := 0
	for _, m := range routeRe.FindAllStringSubmatch(string(src), -1) {
		method, pattern := m[1], m[2]
		if method != "GET" {
			continue // POST routes are covered by the CSRF tests
		}
		if _, public := publicRoutes[pattern]; public {
			continue
		}
		if skip := publicPrefix(pattern); skip {
			continue
		}
		seen++
		path := concrete(pattern)
		code, _, loc := h.get(t, path)
		if code != 303 || !turnedAway(loc) {
			t.Errorf("GET %s answered %d (Location %q) with no session; "+
				"every route not on publicRoutes must turn a stranger away",
				path, code, loc)
		}
	}
	// A regexp that matched nothing would make this test pass by doing nothing,
	// which is the failure mode of every source-scanning gate.
	if seen < 20 {
		t.Fatalf("only found %d guarded routes in server.go; the scan is broken, "+
			"not the routes", seen)
	}
	t.Logf("checked %d routes, all behind a session", seen)
}

func publicPrefix(p string) bool {
	for pub := range publicRoutes {
		if strings.HasSuffix(pub, "/") && strings.HasPrefix(p, pub) {
			return true
		}
	}
	return false
}

// concrete turns a pattern into a path that can actually be requested. The
// value does not need to exist: a stranger must be turned away BEFORE the
// handler looks anything up, and a 404 for an unknown id would mean the
// lookup happened first.
func concrete(pattern string) string {
	out := pattern
	for _, sub := range [][2]string{
		{"{name}", "budgets.csv"}, {"{id}", "1"}, {"{uri}", "x"},
		{"{ref}", "x"}, {"{owner}", "yurii"}, {"{month}", "2026-07"},
		{"{team}", "ml"}, {"{source}", "aws"}, {"{desk}", "aws"},
		{"{service}", "s3"}, {"{key}", "k"}, {"{$}", ""},
		{"{artifact}", "1"}, {"{ordinal}", "1"},
	} {
		out = strings.ReplaceAll(out, sub[0], sub[1])
	}
	// Anything left is a placeholder this list does not know about.
	if i := strings.Index(out, "{"); i >= 0 {
		out = out[:i] + "x"
	}
	return out
}

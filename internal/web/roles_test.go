package web_test

import (
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/auth"
)

var postRouteRe = regexp.MustCompile(`HandleFunc\("POST ([^"]+)"`)

// Routes a viewer may POST to, and why each one.
//
// A viewer is somebody given the console to read. Everything else on this
// list writes to the estate, and the list exists so that any exception is a
// decision somebody wrote down.
var viewerMayPost = map[string]string{
	"/login":  "signing in is how one becomes anybody at all",
	"/logout": "must work from any session, including one being revoked",
	"/signup": "only open while the installation has no accounts",
}

// A viewer must not be able to write.
//
// Thirteen of the console's twenty-one role checks only fill a template field
// like CanAct, which hides a button. A hidden button is not a closed door: the
// form is gone from the page and the handler behind it is not, so a viewer who
// knows the path can still post to it. This test does exactly that, with a
// real viewer session and a real CSRF token, which is the only way to tell the
// two apart.
func TestAViewerCannotWrite(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "boss", "boss-password-2026")
	if _, err := h.au.Create("looker", "looker-password-2026", "viewer"); err != nil {
		t.Fatal(err)
	}
	v := h.as(t, "looker", "looker-password-2026")

	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, m := range postRouteRe.FindAllStringSubmatch(string(src), -1) {
		pattern := m[1]
		if _, ok := viewerMayPost[pattern]; ok {
			continue
		}
		path := concrete(pattern)
		checked++

		// A real token for this viewer's own session. Without it the refusal
		// would be the CSRF check firing, which proves nothing about roles.
		form := url.Values{"csrf": {v.csrf(t, "/")}}
		code, loc := v.post(t, path, form)

		if refusedForRole(code, loc) {
			continue
		}
		t.Errorf("a viewer posted to %s and was not refused: %d %s",
			path, code, loc)
	}
	if checked < 20 {
		t.Fatalf("only checked %d write routes; the scan is broken, not the "+
			"routes", checked)
	}
	t.Logf("checked %d write routes against a viewer session", checked)
}

// The exact words the console refuses with, because ANY message is not good
// enough.
//
// This test first accepted any redirect carrying a msg=. Removing the role
// check from the one chokepoint then turned only one of twenty-seven routes
// red: the rest still redirected with a message, about a missing form field or
// an id that does not exist, and the test read those as refusals. It would
// have passed on a console that let a viewer write to any route whose handler
// happened to complain about something else first.
//
// So the refusal has to be THE refusal.
var roleRefusals = []string{
	"may+read+and+export%2C+but+not+act", // operator required
	"managing+accounts+is+an+admin",      // admin required
}

func refusedForRole(code int, loc string) bool {
	if code == 403 {
		return true
	}
	if code == 303 && (strings.HasPrefix(loc, "/login") || strings.HasPrefix(loc, "/signup")) {
		return true
	}
	if code != 303 {
		return false
	}
	for _, want := range roleRefusals {
		if strings.Contains(loc, want) {
			return true
		}
	}
	return false
}

// An operator must not be able to climb.
//
// TestAnOperatorCannotManageAccounts already covers /accounts/create. This
// covers the rest of the ladder, which is the part that matters: an operator
// who can change a ROLE does not need to create anything, they promote
// themselves and the whole hierarchy is one rung. It also checks the refusal
// with the words the console actually refuses with, rather than for the
// presence of any message at all.
func TestAnOperatorCannotEscalateThroughAccounts(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "boss", "boss-password-2026")
	if _, err := h.au.Create("hand", "hand-password-2026", "operator"); err != nil {
		t.Fatal(err)
	}
	op := h.as(t, "hand", "hand-password-2026")

	// Each attempt is the real one an operator would make to escalate, not a
	// blank post: a handler that refuses because a field is missing would look
	// like a handler that refuses because of the role.
	attempts := []struct {
		what string
		path string
		form url.Values
	}{
		{"promote themselves to admin", "/accounts/role",
			url.Values{"username": {"hand"}, "role": {"admin"}}},
		{"create a second admin", "/accounts/create",
			url.Values{"username": {"backdoor"}, "password": {"backdoor-password-2026"},
				"role": {"admin"}}},
		{"create an operator", "/accounts/create",
			url.Values{"username": {"mate"}, "password": {"mate-password-2026"},
				"role": {"operator"}}},
		{"remove the admin", "/accounts/remove",
			url.Values{"username": {"boss"}}},
		{"demote the admin", "/accounts/role",
			url.Values{"username": {"boss"}, "role": {"viewer"}}},
	}
	for _, a := range attempts {
		form := url.Values{"csrf": {op.csrf(t, "/")}}
		for k, vs := range a.form {
			form[k] = vs
		}
		code, loc := op.post(t, a.path, form)
		if !refusedForRole(code, loc) {
			t.Errorf("an operator could %s via %s: %d %s", a.what, a.path, code, loc)
		}
	}

	// The refusal has to have actually held, not merely have been reported.
	// A handler that writes and then redirects with a complaint would pass
	// every check above.
	if u, err := h.au.Get("hand"); err != nil || u.Role != "operator" {
		t.Errorf("the operator's own role is now %v (err %v); it must still be "+
			"operator", roleOf(u), err)
	}
	if u, err := h.au.Get("boss"); err != nil || u.Role != "admin" {
		t.Errorf("the admin's role is now %v (err %v); an operator changed it",
			roleOf(u), err)
	}
	for _, ghost := range []string{"backdoor", "mate"} {
		if u, err := h.au.Get(ghost); err == nil && u != nil {
			t.Errorf("an operator created the account %q with role %v",
				ghost, roleOf(u))
		}
	}
}

func roleOf(u *auth.User) string {
	if u == nil {
		return "(no account)"
	}
	return u.Role
}

// What a viewer can READ is a separate question from what they can write, and
// nothing was asking it.
//
// The write side is held by one chokepoint. The read side is per-handler, so
// a page that lists something a viewer should not see would be an omission in
// one file with nothing above it. /accounts is the case that matters: it lists
// every username and role on the installation, which is the map somebody needs
// before they try anything.
func TestWhatAViewerCanRead(t *testing.T) {
	h := startWith(t, true)
	h.signUp(t, "boss", "boss-password-2026")
	if _, err := h.au.Create("looker", "looker-password-2026", "viewer"); err != nil {
		t.Fatal(err)
	}
	v := h.as(t, "looker", "looker-password-2026")

	code, body, _ := v.get(t, "/accounts")
	if code != 200 {
		// Refusing outright is a defensible answer too; this test is about
		// knowing which answer is given, not about forcing one.
		t.Logf("a viewer is refused /accounts outright: %d", code)
		return
	}
	// It answers. Then it must not hand over the controls, and it must not
	// pretend they are there.
	for _, control := range []string{
		`action="/accounts/create"`,
		`action="/accounts/role"`,
		`action="/accounts/remove"`,
	} {
		if strings.Contains(body, control) {
			t.Errorf("a viewer is served the form %s; the handler refuses the "+
				"post, but a form that cannot work is an invitation to find "+
				"out, and it tells the reader the console thinks they may",
				control)
		}
	}
	t.Logf("a viewer reads /accounts and is served no account-management form")
}

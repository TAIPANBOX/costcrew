package web_test

// The account forms, each refusal the handler makes and each change it lets
// through. Managing accounts is an admin's job, and each of these is a way the
// job can go wrong that a person would actually hit.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func covHasMsg(loc, msg string) bool {
	return strings.Contains(loc, url.QueryEscape(msg))
}

func TestCovAccountFormsSayWhyEachChangeIsRefused(t *testing.T) {
	h := start(t)
	h.signUp(t, "owner", "owner-password-2026")
	if _, err := h.au.Create("ops", "ops-password-2026", "operator"); err != nil {
		t.Fatal(err)
	}

	// A form with no session token is refused before any change is made.
	code, loc := h.post(t, "/accounts/create", url.Values{
		"username": {"ghost"}, "password": {"ghost-password-2026"}, "role": {"viewer"}})
	if code != http.StatusSeeOther || !covHasMsg(loc, "reload the page and try again") {
		t.Errorf("a create with no csrf token: %d %s, want the reload message", code, loc)
	}

	// A body that does not parse as a form is refused before the token is read.
	bad, err := h.c.Post(h.srv.URL+"/accounts/create", "application/x-www-form-urlencoded",
		strings.NewReader("username=%zz"))
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if !covHasMsg(bad.Header.Get("Location"), "reload the page and try again") {
		t.Errorf("an unparseable form: %d %s, want the reload message", bad.StatusCode, bad.Header.Get("Location"))
	}

	// An operator may read the page and acts on nothing in it.
	op := h.as(t, "ops", "ops-password-2026")
	code, loc = op.post(t, "/accounts/create", url.Values{
		"csrf": {op.csrf(t, "/")}, "username": {"mate"},
		"password": {"mate-password-2026"}, "role": {"viewer"}})
	if code != http.StatusSeeOther || !covHasMsg(loc, "managing accounts is an admin's job") {
		t.Errorf("an operator's create: %d %s, want the admin's-job message", code, loc)
	}

	// The admin's own forms. A name already taken, and a password under ten
	// characters, are both the same refusal.
	csrf := h.csrf(t, "/accounts")
	code, loc = h.post(t, "/accounts/create", url.Values{
		"csrf": {csrf}, "username": {"ops"}, "password": {"another-password-2026"}, "role": {"viewer"}})
	if code != http.StatusSeeOther || !covHasMsg(loc, "that name is taken, or the password is under ten characters") {
		t.Errorf("a create over a taken name: %d %s", code, loc)
	}
	code, loc = h.post(t, "/accounts/create", url.Values{
		"csrf": {csrf}, "username": {"short"}, "password": {"tiny"}, "role": {"viewer"}})
	if code != http.StatusSeeOther || !covHasMsg(loc, "that name is taken, or the password is under ten characters") {
		t.Errorf("a create with a short password: %d %s", code, loc)
	}
	code, loc = h.post(t, "/accounts/create", url.Values{
		"csrf": {csrf}, "username": {"viewer-two"}, "password": {"viewer-password-2026"}, "role": {"viewer"}})
	if code != http.StatusSeeOther || strings.Contains(loc, "msg=") {
		t.Errorf("a good create: %d %s, want no message", code, loc)
	}

	// Removing needs the name typed in the box. A mismatch changes nothing.
	code, loc = h.post(t, "/accounts/remove", url.Values{
		"csrf": {csrf}, "username": {"viewer-two"}, "confirm": {"viewer-tw0"}})
	if code != http.StatusSeeOther || !covHasMsg(loc, "to remove an account, type its name in the box beside the button") {
		t.Errorf("a remove with a mistyped name: %d %s", code, loc)
	}
	// Not yourself: the session that could undo it would be gone.
	code, loc = h.post(t, "/accounts/remove", url.Values{
		"csrf": {csrf}, "username": {"owner"}, "confirm": {"owner"}})
	if code != http.StatusSeeOther || !covHasMsg(loc, "you cannot remove the account you are signed in as") {
		t.Errorf("removing the signed-in account: %d %s", code, loc)
	}

	// The only admin cannot demote themselves out of the seat.
	code, loc = h.post(t, "/accounts/role", url.Values{
		"csrf": {csrf}, "username": {"owner"}, "role": {"viewer"}})
	if code != http.StatusSeeOther || !covHasMsg(loc, "you are the only admin") {
		t.Errorf("the only admin demoting themselves: %d %s", code, loc)
	}

	// The changes that are allowed go through.
	code, loc = h.post(t, "/accounts/role", url.Values{
		"csrf": {csrf}, "username": {"ops"}, "role": {"viewer"}})
	if code != http.StatusSeeOther || strings.Contains(loc, "msg=") {
		t.Errorf("a role change for another account: %d %s, want no message", code, loc)
	}
	code, loc = h.post(t, "/accounts/remove", url.Values{
		"csrf": {csrf}, "username": {"viewer-two"}, "confirm": {"viewer-two"}})
	if code != http.StatusSeeOther || strings.Contains(loc, "msg=") {
		t.Errorf("removing another account by its typed name: %d %s, want no message", code, loc)
	}

	accounts, err := h.au.List()
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]string{}
	for _, a := range accounts {
		roles[a.Username] = a.Role
	}
	if roles["ops"] != "viewer" {
		t.Errorf("ops is %q after the role change, want viewer", roles["ops"])
	}
	if _, still := roles["viewer-two"]; still {
		t.Errorf("viewer-two is still an account after its removal")
	}
}

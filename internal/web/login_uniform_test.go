package web_test

// Invariant 62 through the real form. auth's own test holds the sentence; this
// holds what a stranger can actually observe: the status and the redirect the
// login form answers with, for a name that does not exist, a wrong password,
// and an account that is locked.

import (
	"net/http"
	"net/url"
	"testing"
)

type loginAnswer struct {
	status   int
	location string
	cookies  int
}

func TestEveryFailedSignInLooksTheSameFromOutside(t *testing.T) {
	srv := authOnly(t, false)
	signUpRaw(t, srv, "owner", "owner-password-2026").Body.Close()

	login := func(name, pw string) loginAnswer {
		t.Helper()
		resp := rawPostForm(t, srv, "/login", url.Values{
			"username": {name}, "password": {pw}, "csrf": {csrfFrom(t, srv, "/login")},
		})
		defer resp.Body.Close()
		return loginAnswer{resp.StatusCode, resp.Header.Get("Location"), len(resp.Cookies())}
	}

	unknown := login("nobody-by-this-name", "whatever-password-1")
	wrong := login("owner", "not-the-password-1")
	login("owner", "not-the-password-2")
	login("owner", "not-the-password-3") // the third failure locks the account
	locked := login("owner", "owner-password-2026")

	if unknown.status != http.StatusSeeOther || unknown.location == "" {
		t.Fatalf("an unknown account was not turned back to the form: %+v", unknown)
	}
	if unknown.cookies != 0 {
		t.Fatalf("a failed sign-in set a cookie: %+v", unknown)
	}
	if wrong != unknown {
		t.Errorf("a wrong password answers %+v, an unknown account %+v", wrong, unknown)
	}
	if locked != unknown {
		t.Errorf("a locked account answers %+v, an unknown account %+v", locked, unknown)
	}

	// The lock is real: the right password did not get in.
	if locked.cookies != 0 {
		t.Errorf("the right password set a cookie on a locked account: %+v", locked)
	}
}

package web_test

import (
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/store"
	"github.com/TAIPANBOX/costcrew/internal/web"
)

// An address that listens only on the loopback interface. Both the start-up
// refusal (cmd/costcrew) and the cookie posture (setCookie) read this one
// rule, so they cannot disagree about which deployment is exposed.
func TestLoopbackListenIsOnlyTheLoopbackInterface(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8321", "127.0.0.2:8321", "[::1]:8321", "localhost:8321"} {
		if !web.LoopbackListen(addr) {
			t.Errorf("LoopbackListen(%q) = false, want true", addr)
		}
	}
	// Anything that listens beyond loopback, and anything that cannot be read
	// as host:port: an address the console cannot place is treated as exposed.
	for _, addr := range []string{"0.0.0.0:8321", ":8321", "[::]:8321", "192.168.1.5:8321",
		"costcrew.example:8321", "127.0.0.1", "", "not an address"} {
		if web.LoopbackListen(addr) {
			t.Errorf("LoopbackListen(%q) = true, want false", addr)
		}
	}
}

// The environment twin of -insecure-http, read the way -behind-tls's own
// twin is read: 1, true or yes after trimming and lower-casing; anything else
// is off, so a typo does not switch plain HTTP on.
func TestInsecureHTTPEnvDefaultReadsTheEnvironment(t *testing.T) {
	cases := map[string]bool{"": false, "0": false, "no": false, "1": true, "true": true, " YES ": true}
	for v, want := range cases {
		t.Setenv("COSTCREW_INSECURE_HTTP", v)
		if got := web.InsecureHTTPEnvDefault(); got != want {
			t.Errorf("COSTCREW_INSECURE_HTTP=%q: InsecureHTTPEnvDefault() = %v, want %v", v, got, want)
		}
	}
}

// On a listener that is not loopback every cookie is Secure, whatever
// -behind-tls says: the browser is not on this machine, so its cookie must
// not travel as plain text. The flag is not needed for this, only the address.
func TestExposedListenerMarksEveryCookieSecureWithoutTheFlag(t *testing.T) {
	srv := exposedOnly(t)
	signUpRaw(t, srv, "owner", "owner-password-2026").Body.Close()

	resp := rawPostForm(t, srv, "/login", url.Values{
		"username": {"owner"}, "password": {"owner-password-2026"},
		"csrf": {csrfFrom(t, srv, "/login")},
	})
	defer resp.Body.Close()

	c := sessionCookie(t, resp, "POST /login")
	if !c.Secure {
		t.Errorf("session cookie from a login on a non-loopback listener, -behind-tls off: Secure=%v, want true", c.Secure)
	}
}

// exposedOnly is authOnly for a console whose listen address is not loopback.
func exposedOnly(t *testing.T) *httptest.Server {
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
		Host: "costcrew.test", Recorder: st.AsRecorder(), Exposed: true,
	}))
	t.Cleanup(srv.Close)
	return srv
}

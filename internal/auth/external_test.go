package auth_test

// Invariant 74: an identity provider decides access at every sign-in. These
// hold the account side of that: what is created, what changes, what ends,
// and what a provider can never take over.

import (
	"os"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/auth"
)

const iss = "https://idp.example.test"

func signInExternal(t *testing.T, a *auth.Auth, sub, name, role string) (*auth.User, string) {
	t.Helper()
	u, why, err := a.SignInExternal(iss, sub, name, role)
	if err != nil {
		t.Fatalf("SignInExternal(%s, %s, %s): %v", sub, name, role, err)
	}
	return u, why
}

func TestTheFirstExternalSignInCreatesTheAccountAtTheMappedRole(t *testing.T) {
	a, _ := open(t, t.TempDir())
	u, why := signInExternal(t, a, "sub-1", "alice@example.test", "operator")
	if u == nil || why != "" {
		t.Fatalf("refused: %q", why)
	}
	got, err := a.Get("alice@example.test")
	if err != nil || got == nil || got.Role != "operator" {
		t.Fatalf("account = %+v, %v; want alice@example.test at operator", got, err)
	}
	if ext, _ := a.External("alice@example.test"); !ext {
		t.Error("the account is not recorded as linked to the provider")
	}
}

func TestARoleChangeAtTheProviderAppliesAtTheNextSignIn(t *testing.T) {
	a, _ := open(t, t.TempDir())
	for _, role := range []string{"admin", "viewer", "operator", "operator"} {
		u, why := signInExternal(t, a, "sub-1", "alice@example.test", role)
		if u == nil || u.Role != role {
			t.Fatalf("after signing in mapped to %s the account is %+v (%q)", role, u, why)
		}
		if got, _ := a.Get("alice@example.test"); got.Role != role {
			t.Fatalf("stored role %q, want %q", got.Role, role)
		}
	}
}

func TestNoMappedRoleRefusesAndEndsEverySession(t *testing.T) {
	a, _ := open(t, t.TempDir())
	signInExternal(t, a, "sub-1", "alice@example.test", "admin")
	tokens := []string{}
	for range 3 {
		tok, err := a.StartSession("alice@example.test")
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, tok)
	}
	// A local account's session must not be touched by somebody else's
	// offboarding.
	if ok, err := a.Create("bob", "bob-password-2026", "viewer"); err != nil || !ok {
		t.Fatal(err)
	}
	bobTok, _ := a.StartSession("bob")

	u, why := signInExternal(t, a, "sub-1", "alice@example.test", "")
	if u != nil || why != auth.ExternalNoAccess {
		t.Fatalf("removed from every mapped group: signed in as %+v, %q", u, why)
	}
	for i, tok := range tokens {
		if still, _ := a.SessionUser(tok); still != nil {
			t.Errorf("session %d still signs in as %s after the provider removed access", i, still.Username)
		}
	}
	if still, _ := a.SessionUser(bobTok); still == nil {
		t.Error("another account's session ended too")
	}
}

func TestNoMappedRoleNeverCreatesAnAccount(t *testing.T) {
	a, _ := open(t, t.TempDir())
	before, _ := a.Count()
	u, why := signInExternal(t, a, "sub-9", "mallory@example.test", "")
	if u != nil || why != auth.ExternalNoAccess {
		t.Fatalf("an unmapped identity: %+v, %q", u, why)
	}
	if after, _ := a.Count(); after != before {
		t.Fatalf("accounts %d -> %d: an unmapped identity got an account (a default role by another name)", before, after)
	}
}

func TestALocalAccountIsNeverAdoptedByName(t *testing.T) {
	a, _ := open(t, t.TempDir())
	if ok, err := a.Create("root@example.test", "root-password-2026", "admin"); err != nil || !ok {
		t.Fatal(err)
	}
	u, why := signInExternal(t, a, "sub-attacker", "root@example.test", "viewer")
	if u != nil || why != auth.ExternalNameTaken {
		t.Fatalf("an identity named like a local admin signed in as %+v (%q)", u, why)
	}
	if got, _ := a.Get("root@example.test"); got.Role != "admin" {
		t.Errorf("the local account's role changed to %q", got.Role)
	}
	// Nor is one identity's account taken by another subject with the same name.
	signInExternal(t, a, "sub-1", "alice@example.test", "viewer")
	u, why = signInExternal(t, a, "sub-2", "alice@example.test", "admin")
	if u != nil || why != auth.ExternalNameTaken {
		t.Fatalf("a second subject took alice's account: %+v (%q)", u, why)
	}
}

func TestTheSubjectNotTheNameIsTheLink(t *testing.T) {
	a, _ := open(t, t.TempDir())
	signInExternal(t, a, "sub-1", "alice@example.test", "viewer")
	// Her email changed at the provider; the subject did not.
	u, _ := signInExternal(t, a, "sub-1", "alice.new@example.test", "viewer")
	if u == nil || u.Username != "alice@example.test" {
		t.Fatalf("a renamed identity signed in as %+v; want her existing account", u)
	}
	if got, _ := a.Get("alice.new@example.test"); got != nil {
		t.Error("a second account was created for the same subject")
	}
}

func TestAnExternalAccountHasNoUsablePassword(t *testing.T) {
	a, _ := open(t, t.TempDir())
	signInExternal(t, a, "sub-1", "alice@example.test", "admin")
	for _, pw := range []string{"", "external", "$", "external$$", "x"} {
		if u, _, err := a.Authenticate("alice@example.test", pw); err != nil || u != nil {
			t.Fatalf("password %q signed in to a provider's account: %+v %v", pw, u, err)
		}
	}
}

func TestAnAccountAnAdminRemovedComesBackAtTheMappedRole(t *testing.T) {
	a, _ := open(t, t.TempDir())
	signInExternal(t, a, "sub-1", "alice@example.test", "operator")
	if ok, err := a.Create("admin", "admin-password-2026", "admin"); err != nil || !ok {
		t.Fatal(err)
	}
	if err := a.Delete("alice@example.test"); err != nil {
		t.Fatal(err)
	}
	u, why := signInExternal(t, a, "sub-1", "alice@example.test", "viewer")
	if u == nil || u.Role != "viewer" {
		t.Fatalf("after removal and a new sign-in: %+v (%q)", u, why)
	}
}

func TestExternalSignInRefusesWhatIsNotAnIdentity(t *testing.T) {
	a, _ := open(t, t.TempDir())
	for _, c := range [][4]string{
		{"", "sub", "name", "viewer"}, {iss, "", "name", "viewer"},
		{iss, "sub", " ", "viewer"}, {iss, "sub", "name", "root"},
	} {
		if u, _, err := a.SignInExternal(c[0], c[1], c[2], c[3]); err == nil || u != nil {
			t.Errorf("SignInExternal%v = %+v, %v; want an error", c, u, err)
		}
	}
}

// -------------------------------------------------------------- break glass

func TestUnderOIDCOnlyAPasswordSignsInOnlyToABreakGlassAccount(t *testing.T) {
	a, _ := open(t, t.TempDir())
	if ok, err := a.Create("signed-up", "signed-up-password", "admin"); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := a.SetPassword("glass", "glass-password-2026", "admin", false); err != nil {
		t.Fatal(err)
	}
	// Reset from the command line: a signed-up account becomes break-glass too.
	if _, err := a.SetPassword("reset-later", "reset-password-1", "viewer", false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetPassword("reset-later", "reset-password-2", "viewer", false); err != nil {
		t.Fatal(err)
	}

	u, why, err := a.AuthenticateBreakGlass("signed-up", "signed-up-password")
	if err != nil || u != nil || why != auth.LoginRefused {
		t.Fatalf("a signed-up account's right password under -oidc-only: %+v %q %v", u, why, err)
	}
	_, unknown, _ := a.AuthenticateBreakGlass("nobody", "whatever-password")
	_, wrong, _ := a.AuthenticateBreakGlass("glass", "not-the-password")
	if unknown != why || wrong != why {
		t.Errorf("the refusals differ: %q / %q / %q (invariant 62)", why, unknown, wrong)
	}
	for name, pw := range map[string]string{"glass": "glass-password-2026", "reset-later": "reset-password-2"} {
		if u, why, err := a.AuthenticateBreakGlass(name, pw); err != nil || u == nil {
			t.Errorf("break-glass %s refused: %q %v", name, why, err)
		}
	}
}

// The account side records every refusal and every change in the chain, with
// no token in it.
func TestExternalSignInIsJournaled(t *testing.T) {
	dir := t.TempDir()
	a, st := open(t, dir)
	signInExternal(t, a, "sub-1", "alice@example.test", "admin")
	signInExternal(t, a, "sub-1", "alice@example.test", "viewer")
	signInExternal(t, a, "sub-1", "alice@example.test", "")
	b, err := os.ReadFile(st.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	j := string(b)
	for _, want := range []string{`"user_created"`, `"identity provider"`, `"user_role_changed"`,
		`"external_access_refused"`, `"sessions_ended"`} {
		if !strings.Contains(j, want) {
			t.Errorf("the journal does not record %s", want)
		}
	}
}

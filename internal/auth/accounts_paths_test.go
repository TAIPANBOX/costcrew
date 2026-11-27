package auth_test

// The command line's SetPassword and the joining-code half of Register: the
// refusals each one owes before anything is written, and the role a new
// account gets.

import (
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/auth"
)

func TestSetPasswordRefusals(t *testing.T) {
	a := fresh(t)
	if _, err := a.SetPassword("  ", "a-long-enough-password", "admin", false); err == nil ||
		!strings.Contains(err.Error(), "needs a name") {
		t.Errorf("blank name: %v", err)
	}
	if _, err := a.SetPassword("ops", "short", "admin", false); err == nil ||
		!strings.Contains(err.Error(), "at least 10 characters") {
		t.Errorf("short password: %v", err)
	}
	if u, _ := a.Get("ops"); u != nil {
		t.Fatal("a refused SetPassword created the account")
	}
	// A demo account may be weak, and only when the caller says so.
	created, err := a.SetPassword("demo", "short", "viewer", true)
	if err != nil || !created {
		t.Fatalf("weak demo password: created=%v err=%v", created, err)
	}
	if u, _, _ := a.Authenticate("demo", "short"); u == nil {
		t.Error("the weak demo password does not sign in")
	}
}

// A role the console does not know is not stored: the break-glass account a
// command line creates is an admin, because that is what it exists for.
func TestSetPasswordWithAnUnknownRoleCreatesAnAdmin(t *testing.T) {
	a := fresh(t)
	created, err := a.SetPassword("breakglass", "a-long-enough-password", "superuser", false)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	u, err := a.Get("breakglass")
	if err != nil || u == nil {
		t.Fatal(err)
	}
	if u.Role != "admin" {
		t.Errorf("role %q, want admin", u.Role)
	}
	if ext, err := a.External("breakglass"); err != nil || ext {
		t.Errorf("a command-line account reads as external: %v %v", ext, err)
	}
}

func TestRegisterWithAJoiningCode(t *testing.T) {
	t.Setenv("COSTCREW_SIGNUP_CODE", "join-us-2026")
	a := fresh(t)

	// Even the first account needs the code once one is set.
	if ok, msg, err := a.Register("first", "a-long-enough-password", "nope"); err != nil || ok ||
		msg != "that joining code is not right" {
		t.Fatalf("first account, wrong code: ok=%v msg=%q err=%v", ok, msg, err)
	}
	if ok, msg, err := a.Register("first", "a-long-enough-password", "join-us-2026"); err != nil || !ok ||
		!strings.Contains(msg, "you are the admin") {
		t.Fatalf("first account: ok=%v msg=%q err=%v", ok, msg, err)
	}

	if ok, msg, _ := a.Register("second", "a-long-enough-password", "join-us-2025"); ok ||
		msg != "that joining code is not right" {
		t.Errorf("second account, wrong code: ok=%v msg=%q", ok, msg)
	}
	if ok, msg, _ := a.Register("second", "short", "join-us-2026"); ok ||
		!strings.Contains(msg, "at least 10 characters") {
		t.Errorf("short password: ok=%v msg=%q", ok, msg)
	}
	ok, msg, err := a.Register("second", "a-long-enough-password", "join-us-2026")
	if err != nil || !ok || msg != "account created; an admin can raise your role" {
		t.Fatalf("second account: ok=%v msg=%q err=%v", ok, msg, err)
	}
	u, err := a.Get("second")
	if err != nil || u == nil {
		t.Fatal(err)
	}
	if u.Role != "viewer" {
		t.Errorf("a joining code handed out %q, want viewer", u.Role)
	}
	if ok, msg, _ := a.Register("second", "another-long-password", "join-us-2026"); ok ||
		!strings.Contains(msg, "that name is taken") {
		t.Errorf("a taken name: ok=%v msg=%q", ok, msg)
	}
}

func TestAUserWithNoAccountMayDoNothing(t *testing.T) {
	var nobody *auth.User
	if nobody.May("viewer") {
		t.Error("a nil user may read")
	}
	if !(&auth.User{Role: "operator"}).May("viewer") || (&auth.User{Role: "operator"}).May("admin") {
		t.Error("an operator's rank is wrong")
	}
}

func TestBehindTLSEnvDefault(t *testing.T) {
	for v, want := range map[string]bool{"1": true, " TRUE ": true, "yes": true, "": false, "0": false, "on": false} {
		t.Setenv("COSTCREW_BEHIND_TLS", v)
		if got := auth.BehindTLSEnvDefault(); got != want {
			t.Errorf("COSTCREW_BEHIND_TLS=%q: %v, want %v", v, got, want)
		}
	}
}

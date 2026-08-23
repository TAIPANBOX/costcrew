package auth_test

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/store"
)

func fresh(t *testing.T) *auth.Auth {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a, err := auth.New(st, dir)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// An installation with accounts nobody can sign in to is still an
// installation nobody has claimed.
//
// The estate seeds five owner accounts so that agents can be owned by names
// that exist. Every one has a password of 32 random bytes that was thrown
// away. Counting ACCOUNTS made a brand-new installation answer "registration
// is closed" to the first person who opened it: the page offered the form and
// the POST behind it refused, and nothing said why. Nobody could get in at
// all.
func TestSignupIsOpenUntilThereIsAnAdmin(t *testing.T) {
	a := fresh(t)
	for _, who := range []string{"y.mercer", "t.langley", "a.whitfield"} {
		// A password nobody holds, exactly as the seeding does it.
		pw := "K7f2Qa9xLm4Rt8Wp1Zc6Nv3Bd5Hj0Ys2Ue7Ik4Ol9Pq"
		if ok, err := a.Create(who, pw, "operator"); err != nil || !ok {
			t.Fatalf("seeding %s: %v %v", who, ok, err)
		}
	}
	open, err := a.SignupOpen()
	if err != nil {
		t.Fatal(err)
	}
	if !open {
		t.Error("signup is closed on an installation with no admin, so the " +
			"first person to open it cannot get in")
	}
	ok, msg, err := a.Register("tania", "a-password-she-picks-2026", "")
	if err != nil || !ok {
		t.Fatalf("the first person could not register: %v %q", err, msg)
	}
	// And she is the admin, or she has an account and can still do nothing.
	u, err := a.Get("tania")
	if err != nil || u == nil {
		t.Fatal(err)
	}
	if u.Role != "admin" {
		t.Errorf("the first person to register is %q; on an unclaimed "+
			"installation they have to be the admin", u.Role)
	}
	// Now it closes, because now there is somebody who can hand out accounts.
	if open, _ := a.SignupOpen(); open {
		t.Error("signup is still open after an admin exists")
	}
	if ok, msg, _ := a.Register("stranger", "another-password-2026", ""); ok {
		t.Errorf("a stranger registered after the installation was claimed: %q", msg)
	}
}

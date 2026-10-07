package auth_test

// Invariant 62: every failed sign-in says the same thing. A locked account
// used to answer "locked for another Ns after repeated failures" while an
// unknown name and a wrong password answered "unknown account or wrong
// password", so anybody could ask the login form which names exist: the ones
// that eventually answer "locked" are real. The lockout itself is unchanged.

import (
	"regexp"
	"testing"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/auth"
)

const strongPW = "alice-password-2026"

func aliceWithLockableState(t *testing.T) (*auth.Auth, func(string, string) (string, bool)) {
	t.Helper()
	a, _ := open(t, t.TempDir())
	if ok, err := a.Create("alice", strongPW, "operator"); err != nil || !ok {
		t.Fatalf("creating alice: %v %v", ok, err)
	}
	try := func(name, pw string) (string, bool) {
		u, why, err := a.Authenticate(name, pw)
		if err != nil {
			t.Fatalf("Authenticate(%q): %v", name, err)
		}
		return why, u != nil
	}
	return a, try
}

func TestEveryFailedSignInSaysTheSame(t *testing.T) {
	_, try := aliceWithLockableState(t)

	unknown, ok := try("nobody-by-this-name", "whatever-password-1")
	if ok || unknown == "" {
		t.Fatalf("an unknown account: signed in=%v reason=%q", ok, unknown)
	}
	wrong, ok := try("alice", "not-her-password-1")
	if ok || wrong == "" {
		t.Fatalf("a wrong password: signed in=%v reason=%q", ok, wrong)
	}
	// Two more wrong ones reach the lock (3 failures), then ask again, with the
	// RIGHT password: a locked account refuses even that.
	try("alice", "not-her-password-2")
	try("alice", "not-her-password-3")
	locked, ok := try("alice", strongPW)
	if ok {
		t.Fatal("the account is not locked after three failures: the lockout itself is gone")
	}

	if wrong != unknown {
		t.Errorf("wrong password says %q, unknown account says %q", wrong, unknown)
	}
	if locked != unknown {
		t.Errorf("a locked account says %q, an unknown one says %q", locked, unknown)
	}
	if regexp.MustCompile(`(?i)lock|\d+\s*s\b|another|repeated`).MatchString(locked) {
		t.Errorf("the refusal for a locked account still says it is locked or for how long: %q", locked)
	}
}

// Same shape from the other direction: the text must not depend on how many
// failures have been counted, at any step of the count.
func TestTheRefusalDoesNotChangeAsFailuresAccumulate(t *testing.T) {
	_, try := aliceWithLockableState(t)
	first, _ := try("alice", "bad-password-0")
	for i := 1; i <= 7; i++ {
		got, ok := try("alice", "bad-password-"+string(rune('0'+i)))
		if ok || got != first {
			t.Errorf("attempt %d: signed in=%v, said %q; the first attempt said %q", i+1, ok, got, first)
		}
	}
}

// What did not change: three failures lock the account, the lock holds against
// the right password, the failures are journalled, and the lock expires.
func TestTheLockoutItselfIsStillThere(t *testing.T) {
	a, st := open(t, t.TempDir())
	if ok, err := a.Create("alice", strongPW, "operator"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	for i := 0; i < 3; i++ {
		if u, _, _ := a.Authenticate("alice", "wrong-password-xx"); u != nil {
			t.Fatal("a wrong password signed in")
		}
	}
	if u, _, _ := a.Authenticate("alice", strongPW); u != nil {
		t.Error("the right password signed in while the account was locked")
	}
	u, err := a.Get("alice")
	if err != nil || u == nil {
		t.Fatalf("reading alice: found=%v %v", u != nil, err)
	}
	if u.Failed != 3 || u.LockedUntil <= float64(time.Now().Unix()) {
		t.Fatalf("lock state after three failures: failed=%d locked_until=%v", u.Failed, u.LockedUntil)
	}

	failures := 0
	recs, err := st.JournalTail(50)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Event == "login_failed" {
			failures++
		}
	}
	if failures != 3 {
		t.Errorf("login_failed journal entries: %d, want 3 (the locked attempt is refused before counting)", failures)
	}

	// The lock runs out: moved into the past by hand, the clock is not injectable.
	if _, err := st.DB().Exec(`UPDATE users SET locked_until=? WHERE username='alice'`,
		float64(time.Now().Add(-time.Second).UnixNano())/1e9); err != nil {
		t.Fatal(err)
	}
	if u, why, err := a.Authenticate("alice", strongPW); err != nil || u == nil {
		t.Errorf("the right password after the lock expired: found=%v %q %v", u != nil, why, err)
	}
}

// An unknown name already paid one password hash so it could not be told from
// a real check by its timing. A locked account used to return before any hash
// at all, which made it the fast one of the three; it now pays the same work.
// Measured as a ratio of minimums over several tries, so a loaded machine
// slows both sides and the comparison survives it.
func TestALockedAccountCostsAsMuchAsAnUnknownOne(t *testing.T) {
	a, try := aliceWithLockableState(t)
	for i := 0; i < 3; i++ {
		try("alice", "wrong-password-xx")
	}
	timeIt := func(name string) time.Duration {
		best := time.Hour
		for i := 0; i < 4; i++ {
			start := time.Now()
			if u, _, err := a.Authenticate(name, strongPW); err != nil || u != nil {
				t.Fatalf("Authenticate(%q) = found=%v, %v", name, u != nil, err)
			}
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}
	unknown := timeIt("nobody-by-this-name")
	locked := timeIt("alice")
	if unknown < 2*time.Millisecond {
		t.Fatalf("an unknown account took %v: no password hash ran, so this measured nothing", unknown)
	}
	if locked < unknown/2 {
		t.Errorf("a locked account answers in %v against %v for an unknown one: it skips the password hash", locked, unknown)
	}
}

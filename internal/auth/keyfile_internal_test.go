package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The owner rule for the session key, judged on the stat it is handed. A test
// injects the mode and the owner of the file and the uid of the running process,
// so the rule is shown without a file that another account owns. The rule is
// PostgreSQL's ssl_key_file rule, the same one idryx and mockryx apply to their
// keys: owned by this user and 0600 or stricter, or owned by root and 0640 or
// stricter (a Kubernetes Secret mount), and nothing else.

const keyFileMe = 501

func TestAFileOwnedByThisUserMustBeOwnerOnly(t *testing.T) {
	for _, c := range []struct {
		mode os.FileMode
		ok   bool
	}{
		{0o600, true}, {0o400, true}, {0o200, true},
		{0o640, false}, {0o604, false}, {0o660, false}, {0o644, false},
		{0o700, false}, {0o620, false},
	} {
		err := keyFileRefusal("linux", "k", keyFileStat{Mode: c.mode, UID: keyFileMe, Regular: true}, keyFileMe)
		if (err == nil) != c.ok {
			t.Errorf("owned by this user, mode %04o: err = %v, want accepted = %v", c.mode, err, c.ok)
			continue
		}
		if err != nil && !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("owned by this user, mode %04o: refusal %q does not name chmod 600", c.mode, err)
		}
	}
}

func TestAFileOwnedByRootMayBeGroupReadableAtMost0640(t *testing.T) {
	for _, c := range []struct {
		mode os.FileMode
		ok   bool
	}{
		{0o640, true}, {0o600, true}, {0o440, true}, {0o400, true},
		{0o644, false}, {0o660, false}, {0o666, false}, {0o650, false},
		{0o740, false}, {0o750, false},
	} {
		err := keyFileRefusal("linux", "k", keyFileStat{Mode: c.mode, UID: 0, Regular: true}, keyFileMe)
		if (err == nil) != c.ok {
			t.Errorf("owned by root, mode %04o: err = %v, want accepted = %v", c.mode, err, c.ok)
		}
	}
}

// A process running as root judges a root-owned file by the stricter rule, the
// owner being compared with the running user first, as PostgreSQL does.
func TestARootRunningProcessJudgesARootFileByTheOwnerRule(t *testing.T) {
	if err := keyFileRefusal("linux", "k", keyFileStat{Mode: 0o640, UID: 0, Regular: true}, 0); err == nil {
		t.Errorf("root owns a 0640 key and runs as root: accepted, want the owner-only rule")
	}
	if err := keyFileRefusal("linux", "k", keyFileStat{Mode: 0o600, UID: 0, Regular: true}, 0); err != nil {
		t.Errorf("root owns a 0600 key and runs as root: refused: %v", err)
	}
}

func TestAFileOwnedByAnotherAccountIsRefusedWhateverItsMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o400, 0o640} {
		err := keyFileRefusal("linux", "k", keyFileStat{Mode: mode, UID: 1000, Regular: true}, keyFileMe)
		if err == nil {
			t.Errorf("owned by uid 1000, mode %04o, run as %d: accepted, want refused", mode, keyFileMe)
			continue
		}
		if !strings.Contains(err.Error(), "uid 1000") {
			t.Errorf("refusal %q does not name the owning uid", err)
		}
	}
}

func TestAFileWhoseOwnerCannotBeReadIsRefused(t *testing.T) {
	if err := keyFileRefusal("linux", "k", keyFileStat{Mode: 0o600, UID: -1, Regular: true}, keyFileMe); err == nil {
		t.Errorf("a key whose owner cannot be read was accepted")
	}
}

// Windows reports a file as 0666 whatever its ACL, so the mode and owner rules
// cannot be applied there. The regular-file rule still is.
func TestWindowsSkipsTheModeAndTheOwner(t *testing.T) {
	if err := keyFileRefusal("windows", "k", keyFileStat{Mode: 0o666, UID: -1, Regular: true}, -1); err != nil {
		t.Errorf("windows, a regular file: refused: %v", err)
	}
	if err := keyFileRefusal("windows", "k", keyFileStat{Mode: 0o666, UID: -1, Regular: false}, -1); err == nil {
		t.Errorf("windows, a file that is not regular: accepted")
	}
}

func TestANonRegularFileIsRefusedWithChmodNamed(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		err := keyFileRefusal(goos, "k", keyFileStat{Mode: 0o600, UID: keyFileMe, Regular: false}, keyFileMe)
		if err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("%s, not a regular file: err = %v, want a refusal naming chmod 600", goos, err)
		}
	}
}

// The rule is judged on the stat of the file the secret is read from. The path
// on disk says 0600; the open file, as an injected stat reports it, says 0644. A
// rule that reads the path instead accepts, and the test refuses that.
func TestTheModeIsJudgedOnTheOpenFileNotThePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".session-secret")
	if err := os.WriteFile(path, bytes.Repeat([]byte{4}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := keyFileOps{
		open: openKeyFile,
		stat: func(*os.File) (keyFileStat, error) {
			return keyFileStat{Mode: 0o644, UID: keyFileMe, Regular: true}, nil
		},
		euid: func() int { return keyFileMe },
		goos: "linux",
	}
	if _, err := readSessionSecretWith(path, ops); err == nil {
		t.Errorf("the open file is 0644 but the key was read: the path's mode decided, want the open file's")
	}
}

// The account that runs the process comes from the injected uid, not from the
// operating system, so the owner rule can be shown for any account.
func TestTheRunningAccountIsTheInjectedOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".session-secret")
	if err := os.WriteFile(path, bytes.Repeat([]byte{4}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := keyFileOps{
		open: openKeyFile,
		stat: func(*os.File) (keyFileStat, error) {
			return keyFileStat{Mode: 0o600, UID: keyFileMe, Regular: true}, nil
		},
		euid: func() int { return 502 },
		goos: "linux",
	}
	if _, err := readSessionSecretWith(path, ops); err == nil {
		t.Errorf("the file belongs to uid %d and the process runs as 502: read, want refused", keyFileMe)
	}
}

// What is judged is what is read: an owner-only file owned by this user is read
// through the same open file, and the bytes returned are the bytes in the file.
func TestAnAcceptedKeyIsReadFromTheFileJudged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".session-secret")
	want := bytes.Repeat([]byte{9}, 32)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	ops := keyFileOps{
		open: openKeyFile,
		stat: func(*os.File) (keyFileStat, error) {
			return keyFileStat{Mode: 0o600, UID: keyFileMe, Regular: true}, nil
		},
		euid: func() int { return keyFileMe },
		goos: "linux",
	}
	got, err := readSessionSecretWith(path, ops)
	if err != nil {
		t.Fatalf("an owner-only key owned by this user was refused: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the key read is not the key in the file")
	}
}

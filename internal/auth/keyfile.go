package auth

import (
	"fmt"
	"io"
	"os"
	"runtime"
)

// keyFileStat is what the owner rule reads from the open session key: its mode,
// the uid that owns it (-1 where the system has no numeric owner) and whether it
// is a regular file.
type keyFileStat struct {
	Mode    os.FileMode
	UID     int
	Regular bool
}

// keyFileOps is what readSessionSecret touches. Production uses liveKeyFileOps;
// a test injects the stat of the open file and the account the process runs as,
// so the rule is shown for any mode and any owner.
type keyFileOps struct {
	open func(path string) (*os.File, error)
	stat func(f *os.File) (keyFileStat, error)
	euid func() int
	goos string
}

var liveKeyFileOps = keyFileOps{
	open: openKeyFile,
	stat: statKeyFile,
	euid: os.Getuid,
	goos: runtime.GOOS,
}

// readSessionSecret reads the signing key (invariant 110, c).
func readSessionSecret(path string) ([]byte, error) {
	return readSessionSecretWith(path, liveKeyFileOps)
}

// readSessionSecretWith opens the key once, judges the open file, and reads the
// same file. A symbolic link at the path is refused by the open itself, and a
// missing file comes back as fs.ErrNotExist for the caller to create. Judging the
// open file rather than the path means nothing that happens to the path after the
// open can change what is read.
func readSessionSecretWith(path string, ops keyFileOps) ([]byte, error) {
	f, err := ops.open(path)
	if err != nil {
		if linkRefused(err) {
			return nil, fmt.Errorf("%s is a symbolic link: refusing to start. Put the key in a "+
				"plain file of its own and chmod 600 it", path)
		}
		return nil, err
	}
	defer f.Close()
	st, err := ops.stat(f)
	if err != nil {
		return nil, err
	}
	if err := keyFileRefusal(ops.goos, path, st, ops.euid()); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

// keyFileRefusal applies the owner rule to a key file's stat. euid is the uid of
// the running process.
//
// The rule is the one PostgreSQL applies to the private key a server loads (its
// check_ssl_key_file_permissions), and the one idryx and mockryx apply to theirs.
// The file must be a regular file. A file owned by this account must be 0600 or
// stricter. A file owned by root may be group-readable, 0640 or stricter, which is
// how a Kubernetes Secret mount is owned. Any other owner is refused. As PostgreSQL
// does, the owner is compared with this account first, so a process running as
// root judges a root-owned file by the stricter rule.
//
// Windows skips the mode and the owner: Go reports a file there as 0666 whatever
// its ACL. The regular-file rule still applies.
func keyFileRefusal(goos, path string, st keyFileStat, euid int) error {
	if !st.Regular {
		return fmt.Errorf("%s is not a regular file (a directory, a device or a fifo counts): "+
			"refusing to start. Put the key in a plain file of its own and chmod 600 it", path)
	}
	if goos == "windows" {
		return nil
	}
	perm := st.Mode.Perm()
	switch {
	case st.UID < 0:
		return fmt.Errorf("%s: the owner cannot be checked on this system, so the key is refused: "+
			"chmod 600 it", path)
	case st.UID == euid:
		if perm&^0o600 != 0 {
			return fmt.Errorf("%s has mode %04o: a file this account owns must be owner-only, 0600 or "+
				"stricter: refusing to start. chmod 600 %s", path, perm, path)
		}
	case st.UID == 0:
		if perm&^0o640 != 0 {
			return fmt.Errorf("%s has mode %04o: a file owned by root may be group-readable at most, "+
				"0640 or stricter (chmod 640): refusing to start", path, perm)
		}
	default:
		return fmt.Errorf("%s is owned by uid %d, which is neither this account (uid %d) nor root: "+
			"refusing to start. chown it to this account and chmod 600 it", path, st.UID, euid)
	}
	return nil
}

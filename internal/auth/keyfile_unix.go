//go:build unix

package auth

import (
	"errors"
	"os"
	"syscall"
)

// openKeyFile opens the session key read-only. O_NOFOLLOW makes a symbolic link at
// the path fail the open, which is the refusal; O_NONBLOCK stops a fifo at the
// path from holding the start until a writer arrives, and a regular file is not
// affected by it. The file the rule judges is the file this returns.
func openKeyFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// linkRefused reports whether an open failed because the path's last component is
// a symbolic link.
func linkRefused(err error) bool { return errors.Is(err, syscall.ELOOP) }

// statKeyFile reads the owner rule's inputs from the open file.
func statKeyFile(f *os.File) (keyFileStat, error) {
	info, err := f.Stat()
	if err != nil {
		return keyFileStat{}, err
	}
	st := keyFileStat{Mode: info.Mode(), UID: -1, Regular: info.Mode().IsRegular()}
	if sys, ok := info.Sys().(*syscall.Stat_t); ok {
		st.UID = int(sys.Uid)
	}
	return st, nil
}

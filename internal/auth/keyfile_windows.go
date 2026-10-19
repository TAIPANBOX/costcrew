//go:build windows

package auth

import "os"

// openKeyFile opens the session key as it is. A windows symbolic link is followed
// here: this rule does not refuse links on windows (a named limit).
func openKeyFile(path string) (*os.File, error) { return os.Open(path) }

// linkRefused is false on windows, where openKeyFile follows links.
func linkRefused(err error) bool { return false }

// statKeyFile reads what the regular-file rule needs. There is no numeric owner
// the owner rule can use on windows, so the owner is -1 and the mode is not judged.
func statKeyFile(f *os.File) (keyFileStat, error) {
	info, err := f.Stat()
	if err != nil {
		return keyFileStat{}, err
	}
	return keyFileStat{Mode: info.Mode(), UID: -1, Regular: info.Mode().IsRegular()}, nil
}

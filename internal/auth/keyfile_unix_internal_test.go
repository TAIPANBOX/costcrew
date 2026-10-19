//go:build unix

package auth

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A symbolic link at the path is not opened: the open itself fails, which is the
// refusal. Before this the link was followed and the file it named was read.
func TestASymbolicLinkIsNotOpened(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, bytes.Repeat([]byte{5}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".session-secret")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	f, err := openKeyFile(link)
	if err == nil {
		f.Close()
		t.Fatalf("openKeyFile followed a symbolic link")
	}
	if !errors.Is(err, syscall.ELOOP) {
		t.Errorf("open of a link: err = %v, want ELOOP", err)
	}
	if !linkRefused(err) {
		t.Errorf("linkRefused(%v) = false, want true", err)
	}
}

// A FIFO at the path must not stop the start by blocking the open until a writer
// arrives. Opened without blocking it is refused as not a regular file.
func TestAFIFOIsRefusedWithoutBlockingTheStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".session-secret")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("no fifo here: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readSessionSecretWith(path, liveKeyFileOps)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("a fifo was read as the session key")
		}
		if !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("refusal %q does not name chmod 600", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the start blocked on a fifo: the open must not wait for a writer")
	}
}

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The binary starts from an empty folder.
//
// Every other test in this repo seeds a store first, which means the ORDER of
// the startup steps is the one thing none of them can see. A migration placed
// before the step that creates its table passed all 235 of them and then failed
// on the first run from an empty folder, with the console exiting before it
// ever listened. That is what somebody copying the folder onto a stick would
// have found, and nothing here would have caught it.
//
// Red first: with EnsureArtifactProvenance called before crew.Seed, this fails
// with "no such table: artifacts".
//
// The log goes to a FILE rather than a strings.Builder: the exec pipe writes
// from its own goroutine, and reading a Builder while it is being written is a
// race the test would report before it reported anything useful.
func TestTheConsoleStartsFromAnEmptyFolder(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "costcrew")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building: %v\n%s", err, out)
	}

	data := filepath.Join(dir, "data")
	logPath := filepath.Join(dir, "boot.log")
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	cmd := exec.Command(bin, "-addr", "127.0.0.1:0", "-data", data)
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	if !waitForLine(logPath, "listening", 60*time.Second) {
		b, _ := os.ReadFile(logPath)
		t.Fatalf("the console never reached listening from an empty folder:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(data, "app.db")); err != nil {
		t.Errorf("no database after a clean start: %v", err)
	}
}

// Fable finding 1 (costcrew.md): the operator-facing "-behind-tls set" line
// said what the flag does to a cookie's Secure bit, but not what happens to
// an operator who sets it and then still reaches this process directly over
// plain HTTP (no proxy, or a browser pointed straight at -addr): the browser
// itself drops every Secure cookie it is handed over a non-localhost http
// origin, login answers 303 to itself forever, and neither this log nor the
// page said why. One sentence fixes that.
//
// Red first: before that sentence exists, this fails because the line stops
// at "what TLS this process itself terminates" and never mentions a browser
// or a dropped cookie at all.
func TestBehindTLSLogNamesThePlainHTTPCookieDrop(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "costcrew")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building: %v\n%s", err, out)
	}

	data := filepath.Join(dir, "data")
	logPath := filepath.Join(dir, "boot.log")
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	cmd := exec.Command(bin, "-addr", "127.0.0.1:0", "-data", data, "-behind-tls")
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	if !waitForLine(logPath, "listening", 60*time.Second) {
		b, _ := os.ReadFile(logPath)
		t.Fatalf("the console never reached listening with -behind-tls set:\n%s", b)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "-behind-tls set") ||
		!strings.Contains(string(b), "drop every cookie") {
		t.Errorf("the -behind-tls startup line does not warn an operator that a "+
			"browser reaching this address directly over plain HTTP (localhost "+
			"excepted) will drop every cookie and nobody will be able to sign in; "+
			"got:\n%s", b)
	}
}

func waitForLine(path, word string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil &&
			strings.Contains(string(b), word) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

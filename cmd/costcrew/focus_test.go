package main

// Invariant 104: a launcher sets the gateway's export folders on the command
// line, the console reads them at start, and a mistake in that configuration
// stops the start rather than an import an hour later.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTheConsoleReadsTheGatewaysExportFromFocusDir(t *testing.T) {
	bin := buildConsole(t)
	dir := t.TempDir()
	focus := filepath.Join(dir, "focus")
	if err := os.MkdirAll(focus, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "connectors", "testdata", "tokenfuse-focus-runs-2026-10-14.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(focus, "focus-1.csv"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "boot.log")
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := exec.Command(bin, "-addr", "127.0.0.1:0", "-data", filepath.Join(dir, "data"),
		"-focus-dir", focus, "-focus-every", "0", "-focus-replace-generated")
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	if !waitForLine(logPath, "imported the gateway's export: Read 1 file", 90*time.Second) {
		b, _ := os.ReadFile(logPath)
		t.Fatalf("the console did not read -focus-dir at start:\n%s", b)
	}
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), "reading the gateway's export from "+focus+", once, at start") {
		t.Errorf("the start does not say where it reads the gateway's export from:\n%s", b)
	}
}

func TestTheConsoleWithoutFocusDirSaysTheConnectorIsLeftToItsPage(t *testing.T) {
	bin := buildConsole(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "boot.log")
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := exec.Command(bin, "-addr", "127.0.0.1:0", "-data", filepath.Join(dir, "data"))
	cmd.Env = append(os.Environ(), "COSTCREW_FOCUS_DIR=", "COSTCREW_FOCUS_DIR_OPENAI=")
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	if !waitForLine(logPath, "no -focus-dir; the AI pages read the gateway only when a person imports", 60*time.Second) {
		b, _ := os.ReadFile(logPath)
		t.Fatalf("the start does not say the gateway's export is left to the connector's page:\n%s", b)
	}
}

func TestAMisconfiguredFocusDirRefusesToStart(t *testing.T) {
	bin := buildConsole(t)
	dir := t.TempDir()
	for name, c := range map[string]struct {
		args []string
		env  []string
		want string
	}{
		"a second door with no main folder": {[]string{"-focus-dir-openai", dir}, nil, "needs -focus-dir"},
		"a main folder that is not there":   {[]string{"-focus-dir", filepath.Join(dir, "absent")}, nil, "-focus-dir "},
		"an interval of seconds":            {[]string{"-focus-dir", dir, "-focus-every", "5s"}, nil, "at least 1m0s"},
		"the environment's folder missing":  {nil, []string{"COSTCREW_FOCUS_DIR=" + filepath.Join(dir, "absent")}, "-focus-dir "},
	} {
		t.Run(name, func(t *testing.T) {
			args := append([]string{"-addr", "127.0.0.1:0", "-data", filepath.Join(t.TempDir(), "data")}, c.args...)
			var env []string
			if c.env != nil {
				env = append(os.Environ(), c.env...)
			}
			out, exited := refused(t, bin, env, args...)
			if !exited || !strings.Contains(out, c.want) {
				t.Errorf("the console started, or did not say %q:\n%s", c.want, out)
			}
		})
	}
}

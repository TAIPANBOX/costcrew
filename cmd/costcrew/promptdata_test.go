package main

// -prompt-data on the console (CLAUDE.md invariant 70). The console builds one
// prompt of its own, the supervisor's plan-ask, and the setting governs it as
// it governs the runner's. The vocabulary is closed: a misspelling refuses to
// start, before a store is opened or a listener is bound.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// refused runs the console with args and reports whether it exited by itself
// with output, rather than starting to listen: a console that accepts a
// misspelling does not exit, and the test must not wait on it forever.
func refused(t *testing.T, bin string, env []string, args ...string) (out string, exited bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	if env != nil {
		cmd.Env = env
	}
	b, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return string(b), false
	}
	return string(b), err != nil
}

func buildConsole(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "costcrew")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building: %v\n%s", err, out)
	}
	return bin
}

func TestAMisspeltPromptDataFlagRefusesToStartTheConsole(t *testing.T) {
	bin := buildConsole(t)
	for _, bad := range []string{"maskd", "Masked", "aggregate", ""} {
		data := filepath.Join(t.TempDir(), "data")
		out, exited := refused(t, bin, nil, "-addr", "127.0.0.1:0", "-data", data, "-prompt-data", bad)
		if !exited {
			t.Errorf("-prompt-data %q started the console: %s", bad, out)
			continue
		}
		if !strings.Contains(string(out), "full, masked, aggregates") {
			t.Errorf("-prompt-data %q was refused without naming the three modes: %s", bad, out)
		}
		if _, statErr := os.Stat(filepath.Join(data, "app.db")); statErr == nil {
			t.Errorf("-prompt-data %q opened a store before it refused", bad)
		}
	}

	// the environment twin goes through the same parser
	data := filepath.Join(t.TempDir(), "data")
	if out, exited := refused(t, bin, append(os.Environ(), "COSTCREW_PROMPT_DATA=maskd"),
		"-addr", "127.0.0.1:0", "-data", data); !exited {
		t.Errorf("COSTCREW_PROMPT_DATA=maskd started the console: %s", out)
	}
}

func TestTheConsoleUnderMaskedSaysSoAndKeepsItsKeyInTheDataDir(t *testing.T) {
	bin := buildConsole(t)
	data := filepath.Join(t.TempDir(), "data")
	logPath := filepath.Join(t.TempDir(), "boot.log")
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := exec.Command(bin, "-addr", "127.0.0.1:0", "-data", data, "-prompt-data", "masked")
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	if !waitForLine(logPath, "listening", 60*time.Second) {
		b, _ := os.ReadFile(logPath)
		t.Fatalf("the console never reached listening under masked:\n%s", b)
	}
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), "-prompt-data masked") {
		t.Errorf("the log does not say the console is under masked:\n%s", b)
	}
	fi, err := os.Stat(filepath.Join(data, "prompt-data.key"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("no 0600 key in the data directory: %v %v", fi, err)
	}
}

func TestTheConsoleWithNothingSetIsFullAndMakesNoKey(t *testing.T) {
	bin := buildConsole(t)
	data := filepath.Join(t.TempDir(), "data")
	logPath := filepath.Join(t.TempDir(), "boot.log")
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := exec.Command(bin, "-addr", "127.0.0.1:0", "-data", data)
	cmd.Env = append(os.Environ(), "COSTCREW_PROMPT_DATA=")
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	if !waitForLine(logPath, "listening", 60*time.Second) {
		b, _ := os.ReadFile(logPath)
		t.Fatalf("the console never reached listening:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(data, "prompt-data.key")); err == nil {
		t.Error("the default made a key it has no use for")
	}
	b, _ := os.ReadFile(logPath)
	if strings.Contains(string(b), "-prompt-data") {
		t.Errorf("the default console mentions a setting nobody made:\n%s", b)
	}
}

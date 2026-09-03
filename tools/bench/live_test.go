package main

// Coordinator review of PR #25, 2026-09-03: B6 put the TokenFuse gateway in
// tools/run's own call path so every crew call is metered per agent, and a
// bench with its own private Anthropic caller (a key read straight from the
// environment, no gateway) reopens exactly the hole B6 closed. Until the
// shared caller exists (one call path for tools/run and tools/bench, in
// internal/deliver -- not done tonight), -live with any real engine refuses
// outright, and this package holds no way to make an HTTP request at all:
// the same property tools/run/main_test.go's TestThisBinaryCannotSpend
// proves about tools/run/main.go, checked here the same way.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveWithARealEngineRefusesUntilTheSharedCallerExists(t *testing.T) {
	dir := t.TempDir()
	code, _, errOut := runArgs(t, "-dir", dir, "-live", "-engine", "anthropic")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, errOut)
	}
	want := "the bench is not wired through the TokenFuse gateway yet"
	if !strings.Contains(errOut, want) {
		t.Errorf("refusal does not say %q: %s", want, errOut)
	}
	if strings.Contains(errOut, "ANTHROPIC_API_KEY") {
		t.Errorf("the refusal still comes from an attempted call rather than a "+
			"pre-flight refusal: %s", errOut)
	}
}

// No PRODUCTION file in this package (a _test.go file never ships in the
// built binary, so it is out of scope here the same way it is out of scope
// for what "this binary can spend" means in tools/run/main_test.go) may
// import net/http or read a model provider's key: the refusal above must
// be structural, not a runtime check a future edit could route around.
//
// Restricted to non-test files for a second reason as well as the first:
// this very list of forbidden substrings, and the sentence this test's own
// sibling asserts about them (TestLiveWithARealEngineRefusesUntilThe
// SharedCallerExists), would otherwise flag this file for quoting itself.
func TestNoFileInThisPackageCanMakeAnHTTPRequest(t *testing.T) {
	entries, err := filepath.Glob("*.go")
	if err != nil || len(entries) == 0 {
		t.Fatal("no .go files found under tools/bench; this test measured nothing")
	}
	checked := 0
	for _, f := range entries {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, forbidden := range []string{
			"net/http", "http.Client", "http.Get", "http.Post", "http.NewRequest",
			"ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "os.Getenv",
		} {
			if strings.Contains(string(b), forbidden) {
				t.Errorf("%s contains %q: this package is supposed to hold no way "+
					"to make an HTTP request or read a credential", f, forbidden)
			}
		}
	}
	if checked == 0 {
		t.Fatal("every .go file under tools/bench looked like a test file; this test measured nothing")
	}
}

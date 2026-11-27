package engines

// Check reads readiness from the environment and the path, and calls
// nothing. Each kind of engine, ready and not, and what Dry makes of it.

import (
	"errors"
	"strings"
	"testing"
)

func availability(av []Availability, id string) Availability {
	for _, a := range av {
		if a.Engine.ID == id {
			return a
		}
	}
	return Availability{}
}

func TestCheckReadsEachKindOfEngine(t *testing.T) {
	env := map[string]string{"ANTHROPIC_API_KEY": "test-value", "AWS_PROFILE": "lab", "OPENROUTER_API_KEY": "  "}
	look := func(cmd string) (string, error) {
		if cmd == "claude" {
			return "/opt/bin/claude", nil
		}
		return "", errors.New("not found")
	}
	av := Check(func(k string) string { return env[k] }, look)
	if len(av) != EngineCount() {
		t.Fatalf("%d answers for %d engines", len(av), EngineCount())
	}
	for _, c := range []struct {
		id     string
		ready  bool
		reason string
	}{
		{"anthropic", true, "a key is set in ANTHROPIC_API_KEY"},
		{"openrouter", false, "OPENROUTER_API_KEY is not set"}, // blank is not set
		{"claude-cli", true, "claude is on the path at /opt/bin/claude"},
		{"bedrock", true, "(AWS_PROFILE is set)"},
	} {
		a := availability(av, c.id)
		if a.Ready != c.ready || !strings.Contains(a.Reason, c.reason) {
			t.Errorf("%s: ready=%v %q, want ready=%v and %q", c.id, a.Ready, a.Reason, c.ready, c.reason)
		}
	}
	if Dry(av) {
		t.Error("Dry with a key set")
	}
}

func TestCheckWithNothingConfiguredIsDry(t *testing.T) {
	av := Check(func(string) string { return "" }, func(string) (string, error) { return "", errors.New("no") })
	if !Dry(av) {
		for _, a := range av {
			if a.Ready {
				t.Errorf("%s reads ready with nothing configured: %s", a.Engine.ID, a.Reason)
			}
		}
	}
	if a := availability(av, "claude-cli"); !strings.Contains(a.Reason, "claude is not on this machine's path") {
		t.Errorf("claude-cli: %q", a.Reason)
	}
	if a := availability(av, "bedrock"); a.Ready || !strings.Contains(a.Reason, "cannot see the metadata service") {
		t.Errorf("bedrock with no variable: ready=%v %q", a.Ready, a.Reason)
	}
}

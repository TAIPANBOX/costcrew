package connectors

// Test's other answers: an unknown connector, a documented one that still
// names the environment variable it would need, a built one with nothing
// configured, and a built one whose reader refuses what it was pointed at.
// Each is recorded on the connection, because the page reads it back from
// there, and a recorded "ok" next to a refusal is the lie this guards.

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestTestRefusesAnUnknownConnector(t *testing.T) {
	db := openFocusStore(t).DB()
	if _, _, err := Test(db, "no-such-connector", func(string) string { return "" }); err == nil ||
		!strings.Contains(err.Error(), "no such connector") {
		t.Fatalf("unknown connector: %v", err)
	}
}

func TestTestOnADocumentedConnectorCallsNothing(t *testing.T) {
	db := openFocusStore(t).DB()
	var asked []string
	env := func(name string) string { asked = append(asked, name); return "" }
	result, ok, err := Test(db, "openrouter-usage", env)
	if err != nil {
		t.Fatal(err)
	}
	if ok || !strings.Contains(result, "documented, not built") {
		t.Errorf("documented connector: ok=%v result=%q", ok, result)
	}
	if strings.Join(asked, ",") != "OPENROUTER_API_KEY" {
		t.Errorf("the secret was looked for under %v, want only OPENROUTER_API_KEY", asked)
	}
	c, err := Load(db, "openrouter-usage")
	if err != nil {
		t.Fatal(err)
	}
	if c.LastResult != result || c.OK || c.LastTest == "" {
		t.Errorf("recorded: last_result=%q ok=%v last_test=%q", c.LastResult, c.OK, c.LastTest)
	}
}

func TestTestNamesWhatIsStillNeeded(t *testing.T) {
	db := openFocusStore(t).DB()
	result, ok, err := Test(db, "tokenfuse-focus", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Errorf("an unconfigured connector tested ok: %q", result)
	}
	if !strings.HasPrefix(result, "Not configured yet. Still needed: Folder tokenfuse focus-export wrote to") {
		t.Errorf("result %q does not name the missing folder", result)
	}
	// The optional second door is not demanded.
	if strings.Contains(result, "OpenAI-wire") {
		t.Errorf("result %q demands an optional input", result)
	}
}

func TestTestRecordsTheReadersRefusalAsNotOK(t *testing.T) {
	db := openFocusStore(t).DB()
	gone := filepath.Join(t.TempDir(), "never-synced")
	if err := Save(db, "tokenfuse-focus", map[string]string{"path": gone}); err != nil {
		t.Fatal(err)
	}
	result, ok, err := Test(db, "tokenfuse-focus", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Errorf("a folder that does not exist tested ok: %q", result)
	}
	if !strings.Contains(result, "never-synced") {
		t.Errorf("the refusal %q does not name the folder it could not read", result)
	}
	c, err := Load(db, "tokenfuse-focus")
	if err != nil {
		t.Fatal(err)
	}
	if c.OK || c.LastResult != result {
		t.Errorf("recorded ok=%v last_result=%q, want the refusal and not ok", c.OK, c.LastResult)
	}
}

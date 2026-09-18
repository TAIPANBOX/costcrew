package web_test

// costcrew#66, replayed through the route it was found on: POST
// /connectors/tokenfuse-focus/import with replace-generated=yes, on a
// console wired to the shared bus.
//
// On the appliance proving run of 2026-09-17 that request redirected with
// "that did not work: journaling the generated estate's replacement:
// severity \"\" is not one of info, low, medium, high, critical", and the
// store kept its generated charges. The reader itself was fine (its /test
// had read 277 rows, 4 agents); only the journaling of the replacement
// failed, and it failed only on a console whose recorder is teed onto the
// stack emitter, which no harness in this suite had ever been.

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	asgevent "github.com/TAIPANBOX/agent-stack-go/event"
)

func TestReplacingTheGeneratedEstateLandsWhenTheConsoleIsOnTheBus(t *testing.T) {
	h, events := startOnTheBus(t)
	h.signUp(t, "boss", "boss-password-2026")
	admin := h.as(t, "boss", "boss-password-2026")

	src := filepath.Join("..", "connectors", "testdata", "tokenfuse-focus-2026-09-02.csv")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "focus.csv"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	saveCSRF := admin.csrf(t, "/connectors/tokenfuse-focus")
	code, loc := admin.post(t, "/connectors/tokenfuse-focus/save", url.Values{
		"path": {dir}, "csrf": {saveCSRF},
	})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("saving the path: %d %s", code, loc)
	}

	var generated int
	h.st.DB().QueryRow(`SELECT COUNT(*) FROM charges WHERE provenance IS NULL`).Scan(&generated)
	if generated == 0 {
		t.Fatal("sanity: the harness holds no generated charges to replace")
	}

	importCSRF := admin.csrf(t, "/connectors/tokenfuse-focus")
	code, loc = admin.post(t, "/connectors/tokenfuse-focus/import", url.Values{
		"csrf": {importCSRF}, "replace-generated": {"yes"},
	})
	if code != 303 {
		t.Fatalf("importing with replace-generated=yes: %d %s", code, loc)
	}
	if strings.Contains(loc, "msg=") {
		msg, _ := url.QueryUnescape(loc)
		t.Fatalf("the import was refused on a console that is on the bus: %s", msg)
	}

	// Landed, not rolled back: the redirect alone cannot tell the two apart,
	// and the rollback is exactly what the appliance saw.
	var stillGenerated, real int
	h.st.DB().QueryRow(`SELECT COUNT(*) FROM charges WHERE provenance IS NULL`).Scan(&stillGenerated)
	h.st.DB().QueryRow(`SELECT COUNT(*) FROM charges WHERE provenance='tokenfuse-focus'`).Scan(&real)
	if stillGenerated != 0 || real == 0 {
		t.Errorf("after the import: %d generated charges left, %d real ones; the "+
			"replacement did not land", stillGenerated, real)
	}

	// And the bus carries the replacement, credited to the operator, with a
	// severity the envelope accepts.
	raw, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for i, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		ev, err := asgevent.Unmarshal([]byte(line))
		if err != nil {
			t.Fatalf("line %d: the contract refused it: %v", i+1, err)
		}
		if ev.Type != "generated_estate_replaced" {
			continue
		}
		found++
		if ev.Severity != asgevent.SeverityInfo {
			t.Errorf("generated_estate_replaced went out with severity %q, want %q",
				ev.Severity, asgevent.SeverityInfo)
		}
		if ev.AgentID != "agent://costcrew.test/boss" {
			t.Errorf("the replacement is credited to %q, not to the operator who asked", ev.AgentID)
		}
	}
	if found != 1 {
		t.Errorf("the bus carries %d generated_estate_replaced events, want exactly 1", found)
	}
}

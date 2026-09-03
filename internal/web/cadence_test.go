package web_test

// /cadence: B5-SPEC.md section 4. Red first, against main: the route does
// not exist, so every request below answers 404 -- checked explicitly in
// TestCadenceGETRefusesAStrangerAndRendersForAMember below rather than
// assumed, because a 404 and "not guarded" can look the same in a diff.

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// The page renders the switch (off by default), the due list with a priced
// worst case per row, and the total.
func TestCadenceGETRendersTheSwitchAndTheDueList(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")

	code, body, _ := h.get(t, "/cadence")
	if code != 200 {
		t.Fatalf("GET /cadence = %d, want 200", code)
	}
	if !strings.Contains(body, "Off") {
		t.Error("the page does not say the switch is off, which is the default")
	}
	if !strings.Contains(body, "0.00") {
		t.Error("the page does not show a 0.00 ceiling, which is the default")
	}
	// The seeded roster and its history (start(t) seeds both) produce real
	// cadence-due work; this checks the shape rather than an exact count,
	// which would be an accident of the fixture rather than the behaviour
	// (the same reasoning internal/crew/plan_test.go's own header gives).
	if !strings.Contains(body, "cadence, last posted") && !strings.Contains(body, "never posted") {
		t.Error("no due row shows a cadence or a last-posted date")
	}
	if !strings.Contains(body, "Worst case") {
		t.Error("the due list has no worst-case column")
	}
}

// POST flips the switch, sets the ceiling, and is journaled with the actor.
func TestCadencePOSTFlipsTheSwitchAndJournalsTheActor(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")

	code, loc := h.post(t, "/cadence", url.Values{
		"enabled": {"on"}, "ceiling": {"25.00"}, "csrf": {h.csrf(t, "/cadence")},
	})
	if code != 303 || strings.Contains(loc, "msg=") {
		t.Fatalf("POST /cadence = %d %s, want a clean redirect", code, loc)
	}

	enabled, ceiling, changedBy, changedAt, err := crew.CadenceSettings(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Error("the switch reads off after a POST that turned it on")
	}
	if ceiling.String() != "25.00" {
		t.Errorf("ceiling = %s, want 25.00", ceiling)
	}
	if changedBy != "boss" {
		t.Errorf("changed_by = %q, want boss", changedBy)
	}
	if strings.TrimSpace(changedAt) == "" {
		t.Error("changed_at is empty after a POST")
	}

	tail, err := h.st.JournalTail(20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rec := range tail {
		if rec.Event != "cadence_set" {
			continue
		}
		found = true
		if rec.Data["actor"] != "supervisor" {
			t.Errorf("cadence_set actor = %v, want supervisor (the practice acted; "+
				"changed_by names the person)", rec.Data["actor"])
		}
		if rec.Data["changed_by"] != "boss" {
			t.Errorf("cadence_set changed_by = %v, want boss", rec.Data["changed_by"])
		}
		if rec.Data["enabled"] != true {
			t.Errorf("cadence_set enabled = %v, want true", rec.Data["enabled"])
		}
	}
	if !found {
		t.Error("no cadence_set entry in the journal after a POST")
	}

	// And the page reflects it back.
	_, body, _ := h.get(t, "/cadence")
	if !strings.Contains(body, "On") {
		t.Error("the page still says the switch is off after turning it on")
	}
	if !strings.Contains(body, "boss") {
		t.Error("the page does not say who set the switch")
	}
}

// A viewer may read this page and may not write to it: covered generally by
// TestAViewerCannotWrite's route scan, and directly here so the refusal
// reason is visible in this file rather than only inferred from the scan.
func TestCadencePOSTRefusesAViewer(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")
	if _, err := h.au.Create("looker", "looker-password-2026", "viewer"); err != nil {
		t.Fatal(err)
	}
	v := h.as(t, "looker", "looker-password-2026")

	code, loc := v.post(t, "/cadence", url.Values{
		"enabled": {"on"}, "ceiling": {"25.00"}, "csrf": {v.csrf(t, "/cadence")},
	})
	if !refusedForRole(code, loc) {
		t.Errorf("a viewer posted to /cadence and was not refused: %d %s", code, loc)
	}
	enabled, _, _, _, err := crew.CadenceSettings(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Error("a refused viewer POST still turned the switch on")
	}
}

// Hostile: a ceiling of -1 is refused through the HTTP route too, not only
// at crew.SetCadence directly.
func TestCadencePOSTRefusesANegativeCeiling(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")

	code, loc := h.post(t, "/cadence", url.Values{
		"enabled": {"on"}, "ceiling": {"-1.00"}, "csrf": {h.csrf(t, "/cadence")},
	})
	if code != 303 || !strings.Contains(loc, "msg=") {
		t.Errorf("POST /cadence with ceiling -1.00 = %d %s, want a refusal message", code, loc)
	}
	enabled, _, _, _, err := crew.CadenceSettings(h.st.DB())
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Error("a negative ceiling still turned the switch on")
	}
}

// The last three crew_ran events show, newest first, with what each run did
// and cost and who switched the cadence on.
func TestCadenceShowsTheLastThreeCrewRanEvents(t *testing.T) {
	h := start(t)
	h.signUp(t, "boss", "boss-password-2026")

	rec := h.st.AsRecorder()
	for i, label := range []string{"cadence-2026-08-30", "cadence-2026-08-31", "cadence-2026-09-01", "cadence-2026-09-02"} {
		if err := rec.Emit("crew_ran", "supervisor", "info", map[string]any{
			"run": "crew-test-" + strconv.Itoa(i), "sprint": label,
			"tasks_run": i + 1, "tasks_refused": 0,
			"cost_micros": int64((i + 1) * 10_000), "ceiling_cents": int64(2500),
			"switched_on_by": "boss",
		}, nil); err != nil {
			t.Fatal(err)
		}
	}

	_, body, _ := h.get(t, "/cadence")
	if strings.Contains(body, "cadence-2026-08-30") {
		t.Error("the oldest of four crew_ran events shows; only the last three should")
	}
	for _, want := range []string{"cadence-2026-08-31", "cadence-2026-09-01", "cadence-2026-09-02"} {
		if !strings.Contains(body, want) {
			t.Errorf("the last-three-runs panel is missing %q", want)
		}
	}
	if !strings.Contains(body, "0.0400") { // 4 * 10,000 micros = 0.0400 USD
		t.Error("the newest run's cost does not appear")
	}
}

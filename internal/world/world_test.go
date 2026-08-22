package world

import (
	"strings"
	"testing"
)

// The fixture is data, and data rots quietly. These tests are what stop a
// half-edited world from looking finished.

func TestOrgHasEnoughVariety(t *testing.T) {
	if len(Teams) < 8 {
		t.Errorf("%d teams; a console with fewer than eight never shows the hard cases", len(Teams))
	}
	if len(Crew) < 24 {
		t.Errorf("%d analysts; the crew is meant to be several dozen", len(Crew))
	}
	if len(Desks) < 5 {
		t.Errorf("%d desks", len(Desks))
	}
}

func TestEveryTeamAndDeskIsDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, x := range Teams {
		if seen[x.Name] {
			t.Errorf("team %q appears twice", x.Name)
		}
		seen[x.Name] = true
	}
	seen = map[string]bool{}
	for _, d := range Desks {
		if seen[d.Name] {
			t.Errorf("desk %q appears twice", d.Name)
		}
		seen[d.Name] = true
	}
	seen = map[string]bool{}
	for _, a := range Crew {
		if seen[a.Name] {
			t.Errorf("analyst %q appears twice", a.Name)
		}
		seen[a.Name] = true
	}
}

// The rule the product enforces in its own suspend flow: a state that is not
// "active" is a decision somebody made, and a decision without a reason cannot
// be reviewed, appealed or undone by anyone who was not there.
func TestEveryNonActiveStateCarriesAReason(t *testing.T) {
	for _, a := range Crew {
		if a.State == Active {
			if a.Reason != "" {
				t.Errorf("%s is active but carries a reason; that reads as a problem when there is none", a.Name)
			}
			continue
		}
		if strings.TrimSpace(a.Reason) == "" {
			t.Errorf("%s is %q with no reason", a.Name, a.State)
		}
		if len(a.Reason) < 20 {
			t.Errorf("%s: %q is too short to tell anyone anything", a.Name, a.Reason)
		}
	}
}

// A fixture where everyone is fine exercises none of the screens that matter.
func TestTheCrewIsNotUniformlyHealthy(t *testing.T) {
	states := map[AgentState]int{}
	for _, a := range Crew {
		states[a.State]++
	}
	for _, want := range []AgentState{Suspended, Onboarding, Restricted, OverGuard, Probation} {
		if states[want] == 0 {
			t.Errorf("no analyst is in state %q, so that path is never rendered", want)
		}
	}
	if states[Active] < len(Crew)/2 {
		t.Errorf("only %d of %d analysts are active; the fixture should look like a working crew, not a crisis",
			states[Active], len(Crew))
	}
}

func TestEveryAgentIsOnAKnownDesk(t *testing.T) {
	known := map[string]bool{"management": true}
	for _, d := range Desks {
		known[d.Name] = true
	}
	for _, a := range Crew {
		if !known[a.Desk] {
			t.Errorf("%s is on desk %q, which does not exist", a.Name, a.Desk)
		}
	}
}

// ------------------------------------------------------------------ events

// The point of the fixture: a detector can be scored, in both directions.
func TestGroundTruthHasBothAnswers(t *testing.T) {
	if len(MustDetect()) < 6 {
		t.Errorf("%d events to find; too few to say a detector works", len(MustDetect()))
	}
	if len(MustIgnore()) < 4 {
		t.Errorf("%d events to leave alone; without these, a detector that flags "+
			"everything scores full marks", len(MustIgnore()))
	}
}

func TestEveryEventIsWellFormed(t *testing.T) {
	ids := map[string]bool{}
	teams, sources := map[string]bool{}, map[string]bool{}
	for _, x := range Teams {
		teams[x.Name] = true
	}
	for _, d := range Desks {
		sources[d.Name] = true
	}
	for _, e := range Planted {
		if ids[e.ID] {
			t.Errorf("event id %q appears twice", e.ID)
		}
		ids[e.ID] = true
		if !sources[e.Source] {
			t.Errorf("%s: source %q does not exist", e.ID, e.Source)
		}
		if !teams[e.Team] {
			t.Errorf("%s: team %q does not exist", e.ID, e.Team)
		}
		if len(e.Day) != 10 || e.Day[4] != '-' || e.Day[7] != '-' {
			t.Errorf("%s: %q is not an ISO date", e.ID, e.Day)
		}
		if e.Service == "" {
			t.Errorf("%s: no service", e.ID)
		}
		if e.Factor <= 0 {
			t.Errorf("%s: factor %v", e.ID, e.Factor)
		}
		// Every event explains itself. An unexplained fixture entry is one
		// nobody can correct when the detector disagrees with it.
		if len(strings.TrimSpace(e.Why)) < 40 {
			t.Errorf("%s: Why is too short to settle an argument with a detector", e.ID)
		}
	}
}

// Shape and direction have to agree, or the fixture is asserting something it
// does not mean: a "drop" with a factor above one is a spike with a wrong label.
func TestShapeAgreesWithDirection(t *testing.T) {
	for _, e := range Planted {
		switch e.Shape {
		case Drop:
			if e.Factor >= 1 {
				t.Errorf("%s is a drop with factor %v", e.ID, e.Factor)
			}
			if e.Excess > 0 {
				t.Errorf("%s is a drop but its excess is positive", e.ID)
			}
		case Natural:
			if e.Factor != 1 {
				t.Errorf("%s is natural but carries factor %v; a planted change is not a control", e.ID, e.Factor)
			}
			if e.Detect {
				t.Errorf("%s is natural and marked detect; nothing was done to that day", e.ID)
			}
		case Spike, Step, Ramp:
			if e.Factor <= 1 {
				t.Errorf("%s is a %s with factor %v", e.ID, e.Shape, e.Factor)
			}
			if e.Excess < 0 {
				t.Errorf("%s is a %s but its excess is negative", e.ID, e.Shape)
			}
		default:
			t.Errorf("%s has unknown shape %q", e.ID, e.Shape)
		}
	}
}

// Both directions must be represented among the events to FIND. A fixture of
// spikes only lets a one-sided detector look complete, and a feed that stopped
// delivering is the failure a one-sided detector never sees.
func TestBothDirectionsMustBeFound(t *testing.T) {
	var ups, downs int
	for _, e := range MustDetect() {
		if e.Shape == Drop {
			downs++
		} else {
			ups++
		}
	}
	if ups == 0 || downs == 0 {
		t.Fatalf("events to find: %d up, %d down; both are needed", ups, downs)
	}
}

// Some anomalies are an agent's own spend, and that is the case the whole
// governance stack exists for.
func TestSomeAnomaliesAreCausedByAnAgent(t *testing.T) {
	crew := map[string]bool{}
	for _, a := range Crew {
		crew[a.Name] = true
	}
	n := 0
	for _, e := range Planted {
		if e.CausedBy == "" {
			continue
		}
		n++
		if !crew[e.CausedBy] {
			t.Errorf("%s is caused by %q, who is not on the crew", e.ID, e.CausedBy)
		}
	}
	if n == 0 {
		t.Error("no anomaly is attributed to an agent, so the caused-by column is never exercised")
	}
}

// A driver that explains an event must not, by itself, make it disappear: the
// fixture has to contain at least one explained event that is still reported,
// or the annotate-never-hide rule is untested.
func TestAnExplainedEventIsStillReported(t *testing.T) {
	for _, e := range Planted {
		if e.Driver != "" && e.Detect {
			return
		}
	}
	t.Fatal("every event with a driver is marked do-not-detect; nothing tests that " +
		"a known cause annotates an anomaly rather than hiding it")
}

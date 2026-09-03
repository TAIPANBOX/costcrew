package main

import (
	"testing"
)

// The real fixture holds exactly two anomalies with a driver label (E02 on
// gcp/GKE, E04 on onprem/Batch cluster: internal/world/world.go's own
// Planted events, the two whose Detect is true and whose Driver is set).
// -skill triage matches only E02: the roster has no triage-onprem
// (confirmed by reading internal/world/world.go's buildCrew, which builds
// investigator-onprem/optimizer-onprem/reporter-onprem for the on-premises
// desk and no triage- variant at all). -skill investigate matches both.
func TestSelectKnownCasesOnTheRealFixture(t *testing.T) {
	db := seededTestDB(t)

	triage, total, eligible, err := selectKnownCases(db, "triage", 20, 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("the fixture carries %d anomalies with a driver, want 2 (E02, E04)", total)
	}
	if eligible != 1 || len(triage) != 1 {
		t.Fatalf("triage: eligible=%d cases=%d, want 1 (only E02/gcp has a triage analyst)",
			eligible, len(triage))
	}
	if triage[0].Anomaly.Source != "gcp" {
		t.Errorf("triage's one case is on desk %q, want gcp", triage[0].Anomaly.Source)
	}
	if triage[0].Analyst.Name != "triage-gcp" {
		t.Errorf("triage's case was assigned to %q, want triage-gcp", triage[0].Analyst.Name)
	}

	investigate, total2, eligible2, err := selectKnownCases(db, "investigate", 20, 1)
	if err != nil {
		t.Fatal(err)
	}
	if total2 != 2 || eligible2 != 2 || len(investigate) != 2 {
		t.Fatalf("investigate: total=%d eligible=%d cases=%d, want 2, 2, 2", total2, eligible2, len(investigate))
	}
}

// B7-SPEC.md section 5's own boundary: "-n larger than the fixture holds
// is clamped and said". The "said" half is main.go's job (it builds the
// note from these return values); this is the clamp itself.
func TestSelectKnownCasesClampsNLargerThanTheFixture(t *testing.T) {
	db := seededTestDB(t)
	cases, total, eligible, err := selectKnownCases(db, "investigate", 1_000_000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != eligible {
		t.Errorf("asked for a million cases and got %d, want exactly the %d eligible ones",
			len(cases), eligible)
	}
	if total != 2 || eligible != 2 {
		t.Errorf("total=%d eligible=%d, want 2, 2", total, eligible)
	}
}

// A skill with no eligible analyst for either known case (there is no such
// skill on this fixture, so this proves the OTHER half: n=1 still returns
// only what is actually eligible, never padding with an ineligible case).
func TestSelectKnownCasesNeverReturnsMoreThanRequested(t *testing.T) {
	db := seededTestDB(t)
	cases, _, eligible, err := selectKnownCases(db, "investigate", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 {
		t.Fatalf("asked for 1, got %d", len(cases))
	}
	if eligible != 2 {
		t.Errorf("eligible=%d, want 2: -n capped the CASES returned, not the count reported", eligible)
	}
}

// The seed is what makes case selection reproducible: the same seed
// against the same store, twice, must choose the same case.
func TestSelectKnownCasesIsReproducible(t *testing.T) {
	db := seededTestDB(t)
	a, _, _, err := selectKnownCases(db, "investigate", 1, 42)
	if err != nil {
		t.Fatal(err)
	}
	b, _, _, err := selectKnownCases(db, "investigate", 1, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || len(b) != 1 || a[0].Anomaly.ID != b[0].Anomaly.ID {
		t.Errorf("two selections with the same seed (42) disagree: %v vs %v", a, b)
	}
}

// -skill values outside "triage"/"investigate" are refused rather than
// silently matching nothing.
func TestRolePrefixForSkillRefusesAnUnknownSkill(t *testing.T) {
	if _, err := rolePrefixForSkill("optimize"); err == nil {
		t.Error("an unknown -skill value was accepted")
	}
}

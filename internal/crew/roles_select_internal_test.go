package crew

import "testing"

// SupervisorMaySelect is roles.yaml read for one question: may the
// supervisor's own pass apply an option of this class without asking the
// owner. The supervisor's own classes are its own; the analyst link's
// classes are its only through option.select, which is read from the
// supervisor's decides_alone list rather than assumed.
func TestSupervisorMaySelectReadsOptionSelectFromTheJobDescription(t *testing.T) {
	for _, class := range []string{"recommendation.rightsizing", "anomaly.dismiss", "anomaly.explain", "driver.one-time"} {
		if ok, why := SupervisorMaySelect(class); !ok {
			t.Errorf("SupervisorMaySelect(%q) = false (%s), want true while option.select is listed", class, why)
		}
	}
	for _, class := range []string{"period.close", "budget.set", "purchase", "infra.change", "vendor.negotiate", "no.such.class"} {
		if ok, _ := SupervisorMaySelect(class); ok {
			t.Errorf("SupervisorMaySelect(%q) = true, want false: not the analysts' or the supervisor's", class)
		}
	}
	// Its own classes stay its own, as MayDecide already says.
	if ok, why := SupervisorMaySelect("driver.recurring"); !ok {
		t.Errorf("SupervisorMaySelect(driver.recurring) = false (%s), want true", why)
	}

	// Take option.select out of the supervisor's decides_alone and the
	// analysts' classes are no longer its to select; its own still are.
	saved := roles
	t.Cleanup(func() { roles = saved })
	edited := saved
	edited.Roles = append([]JobDescription(nil), saved.Roles...)
	found := false
	for i, r := range edited.Roles {
		if r.Family != "supervisor" {
			continue
		}
		found = true
		var kept []string
		for _, id := range r.DecidesAlone {
			if id != "option.select" {
				kept = append(kept, id)
			}
		}
		if len(kept) == len(r.DecidesAlone) {
			t.Fatal("the supervisor's decides_alone does not list option.select to begin with")
		}
		edited.Roles[i].DecidesAlone = kept
	}
	if !found {
		t.Fatal("no supervisor family in roles.yaml")
	}
	roles = edited
	if ok, _ := SupervisorMaySelect("recommendation.rightsizing"); ok {
		t.Errorf("SupervisorMaySelect(recommendation.rightsizing) = true with option.select removed, want false")
	}
	if ok, why := SupervisorMaySelect("driver.recurring"); !ok {
		t.Errorf("SupervisorMaySelect(driver.recurring) = false (%s) with option.select removed, want true: its own class", why)
	}
}

// MayDecide's own meaning for the literal "supervisor" is unchanged: a coarse
// check on the class's owner field. Other callers depend on it.
func TestMayDecideForTheSupervisorStillOnlyChecksTheClassOwner(t *testing.T) {
	if ok, _ := MayDecide("supervisor", "recommendation.rightsizing"); ok {
		t.Errorf(`MayDecide("supervisor", "recommendation.rightsizing") = true: its meaning must not widen`)
	}
	if ok, _ := MayDecide("supervisor", "driver.recurring"); !ok {
		t.Errorf(`MayDecide("supervisor", "driver.recurring") = false, want true`)
	}
}

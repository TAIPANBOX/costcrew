package crew

import (
	"sort"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/world"
)

// rosterSkills is every distinct skill string world.Crew actually hands out.
// Both tests below read the roster this way rather than trusting SkillPool,
// because SkillPool is the thing under test in the second one.
func rosterSkills() map[string]bool {
	out := map[string]bool{}
	for _, a := range world.Crew {
		for _, s := range a.Skills {
			out[s] = true
		}
	}
	return out
}

// Every skill the roster actually hires an analyst with must resolve to a
// right, or that analyst is silently granted nothing beyond the figures-read
// floor while its card claims a skill nobody backed with a permission.
//
// Nine skills were on the roster and absent from rightsForSkill: an analyst
// with any of them held only figures-read, and the card never said why.
func TestEverySkillOnTheRosterHasRights(t *testing.T) {
	var missing []string
	for s := range rosterSkills() {
		if _, ok := rightsForSkill[s]; !ok {
			missing = append(missing, s)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d roster skill(s) have no rightsForSkill entry, so an analyst "+
			"holding one gets figures-read and nothing else: %v", len(missing), missing)
	}
}

// SkillPool is what the hire form offers, and rightsForSkill is what a skill
// actually does. A pool wider than the map offers a skill that grants
// nothing; a pool narrower than it hides a skill the map already knows how
// to back with rights. The two must name exactly the same set.
func TestSkillPoolIsExactlyTheSkillsWithRights(t *testing.T) {
	want := make([]string, 0, len(rightsForSkill))
	for s := range rightsForSkill {
		want = append(want, s)
	}
	sort.Strings(want)

	got := append([]string(nil), SkillPool...)
	sort.Strings(got)

	if len(got) != len(want) {
		t.Fatalf("SkillPool has %d entries, rightsForSkill has %d: %v vs %v",
			len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SkillPool[%d] = %q, rightsForSkill's sorted keys give %q", i, got[i], want[i])
		}
	}
}

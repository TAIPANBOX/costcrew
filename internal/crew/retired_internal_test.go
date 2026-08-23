package crew

import "testing"

// No skill may grant a right this console has retired.
//
// This is the invariant that stops a retirement from quietly undoing itself.
// Clearing the right off every roster is a one-off; RightsFor DERIVES rights
// from skills and runs on every hire and every backfill, so one skill still
// listing a retired right hands it straight back out, while the migration
// reports a confident zero because it already ran and found nothing.
func TestNoSkillGrantsARetiredRight(t *testing.T) {
	for skill, rights := range rightsForSkill {
		for _, r := range rights {
			if _, retired := retiredRights[r]; retired {
				t.Errorf("skill %q still grants %q, which this console retired: "+
					"the next hire gets it back", skill, r)
			}
		}
	}
}

// And the vocabulary, which is the other way one gets in: Rights is what the
// roster editor offers, so a retired right left there is one click from being
// granted again by hand.
func TestVocabularyOffersNoRetiredRight(t *testing.T) {
	for _, r := range Rights {
		if _, retired := retiredRights[r]; retired {
			t.Errorf("the rights vocabulary still offers %q, which was retired: "+
				"the roster editor would list it as a choice", r)
		}
	}
}

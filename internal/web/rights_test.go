package web

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// Every right this console can grant has an explanation on the card.
//
// The agent card renders a right by looking it up. A right with no entry shows
// as a bare token like "kpi-registry", which tells the person signing off on
// the agent nothing about what it can actually reach, and looks like a slug
// that leaked out of the database rather than a permission somebody chose.
func TestEveryGrantableRightIsExplained(t *testing.T) {
	for _, r := range crew.Rights {
		if rightMeans[r] == "" {
			t.Errorf("right %q can be granted and has no explanation: a card "+
				"shows it as a bare token", r)
		}
	}
}

// And the other direction, which is the one that rots quietly: an explanation
// for a right nothing can grant is a line describing a power no agent has.
func TestNoExplanationOutlivesItsRight(t *testing.T) {
	grantable := make(map[string]bool, len(crew.Rights))
	for _, r := range crew.Rights {
		grantable[r] = true
	}
	for r := range rightMeans {
		if !grantable[r] {
			t.Errorf("the card explains %q, which this console can no longer "+
				"grant: it describes a power no agent has", r)
		}
	}
}

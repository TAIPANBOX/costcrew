package world_test

import (
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/world"
)

// The same estate must render in the same order every time.
//
// AIUnits builds its rows in a map and returned them in map order, which Go
// randomises on purpose. Two installs of the same binary over the same seeded
// data therefore listed the AI desk differently, and so did two requests to
// the same process wherever the chosen sort had ties.
//
// This is the defect the parity gate was built to catch. It found exactly this
// shape in the Python version, in a GROUP BY with no ORDER BY, and the Go port
// reintroduced it in a different idiom.
func TestAIUnitsAreOrderedTheSameEveryCall(t *testing.T) {
	first := world.AIUnits()
	if len(first) < 2 {
		t.Fatalf("only %d AI units; this test cannot see an ordering bug in "+
			"fewer than two rows", len(first))
	}
	// Several calls, because a map with few keys can repeat its order by luck
	// and a single comparison would then pass on a broken function.
	for i := 0; i < 20; i++ {
		again := world.AIUnits()
		if len(again) != len(first) {
			t.Fatalf("call %d returned %d units, the first returned %d",
				i, len(again), len(first))
		}
		for j := range first {
			if again[j] != first[j] {
				t.Fatalf("call %d differs at row %d: first %v, now %v\n"+
					"the rows are the same set in a different order, so every "+
					"page built from them is unstable", i, j, first[j], again[j])
			}
		}
	}
}

package stack_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/crew"
)

// Invariant 63 tightens the console's OWN files (app.db, its journal, the data
// directory). These two are the other half of the sentence: the passport
// documents and the -stack-events file are written for OTHER services to read,
// heraldyx, idryx, genaryx and trailryx among them, usually running as another
// account or in another container. Making them 0600 would silence the
// integration without an error anywhere on this side. This is a guard on a
// deliberate non-change, green on the code before the invariant existed; it
// goes red the day a sweep for "every file 0600" reaches them.
func TestSharedFilesStayReadableByOtherServices(t *testing.T) {
	em, events, pass := open(t)
	if err := em.Emit("anomaly_detected", "detector", "medium",
		map[string]any{"anomaly": "A-0000000000"}, nil); err != nil {
		t.Fatal(err)
	}
	if n, err := em.WritePassports([]crew.Analyst{
		{Name: "triage-aws", Role: "triage", Desk: "aws", State: "active", Owner: "finops"},
	}); err != nil || n != 1 {
		t.Fatalf("passports written: %d %v", n, err)
	}

	for label, path := range map[string]string{
		"the -stack-events file": events,
		"a published passport":   filepath.Join(pass, "triage-aws.json"),
		"the passport directory": pass,
		"the events directory":   filepath.Dir(events),
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		// Group and other read (and, for a directory, traverse): what a reader
		// under a different uid needs.
		need := os.FileMode(0o044)
		if fi.IsDir() {
			need = 0o055
		}
		if got := fi.Mode().Perm(); got&need != need {
			t.Errorf("%s has mode %04o: other services can no longer read it", label, got)
		}
	}
}

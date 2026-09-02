package connectors

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/TAIPANBOX/costcrew/internal/store"
)

// Built means a reader exists, and nothing else earns the word.
//
// The catalogue used to write Status by hand on every entry, and the package
// comment already said Built means "there is a reader and a test." No reader
// existed anywhere in the module for any of them; seven entries said Built
// anyway. This is the invariant that makes that impossible again: Status is
// derived from the readers registry in exactly one place, so an entry can
// never claim more than the registry backs.
func TestBuiltMeansAReaderExists(t *testing.T) {
	for _, c := range Catalogue {
		_, hasReader := readers[c.ID]
		wantBuilt := hasReader
		gotBuilt := c.Status == Built
		if gotBuilt != wantBuilt {
			t.Errorf("%s: Status=%q but a reader is registered=%v; Built must hold exactly "+
				"when a reader exists", c.ID, c.Status, hasReader)
		}
	}
}

// Test() says a documented connector is not built, before it ever gets to
// whether it is configured or metered: with the registry empty, that is
// every real connector today, and Test must not describe any of them as
// something a green tick would make sense on.
func TestTestDescribesAnUndocumentedConnectorHonestly(t *testing.T) {
	st := openTestStore(t)
	result, ok, err := Test(st.DB(), "aws-cost-explorer", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("Test() reported ok for a connector with no reader")
	}
	if !strings.Contains(result, "documented, not built") {
		t.Errorf("result = %q, want it to say documented, not built", result)
	}
}

// A documented connector refuses Import with the same words it always has:
// there is nothing to run because there is no reader.
func TestImportRefusesADocumentedConnector(t *testing.T) {
	st := openTestStore(t)
	// opencost has never claimed Built and carries no reader either way.
	if _, ok := readers["opencost"]; ok {
		t.Fatal("test assumes opencost has no reader; the registry has grown one")
	}
	if _, err := Import(st.DB(), "opencost", true, ImportOptions{}); err == nil {
		t.Error("importing a connector with no reader was accepted")
	}
}

// A metered connector refuses Import without an explicit confirmation, and
// that gate holds regardless of whether a reader exists: the danger is the
// spend, not whether the code happens to have a reader today, and once a
// reader is written this must still stop an accidental call before it runs.
func TestImportRefusesAMeteredConnectorWithoutConfirmation(t *testing.T) {
	st := openTestStore(t)
	c, ok := Get("aws-cost-explorer")
	if !ok || !c.Metered {
		t.Fatal("test assumes aws-cost-explorer exists and is metered")
	}

	if _, err := Import(st.DB(), "aws-cost-explorer", false, ImportOptions{}); err == nil {
		t.Fatal("a metered import ran without confirmation")
	} else if !strings.Contains(err.Error(), "costs money") {
		t.Errorf("the refusal does not name the cost: %v", err)
	}

	// Register a fake reader for the duration of this test, so the metered
	// gate is proven to hold even when there IS something to call: without
	// this, the test would only prove that an unbuilt connector refuses,
	// which TestImportRefusesADocumentedConnector already covers.
	readers["aws-cost-explorer"] = func(db *sql.DB, cfg map[string]string, opt ImportOptions) (string, error) {
		return "should never be called without confirmation", nil
	}
	defer delete(readers, "aws-cost-explorer")

	if _, err := Import(st.DB(), "aws-cost-explorer", false, ImportOptions{}); err == nil {
		t.Error("a metered import with a reader present still ran without confirmation")
	}
}

// The header counts and the catalogue must always agree: Counts() is not a
// second, separately-maintained tally of what Status already says.
func TestCountsMatchTheCatalogue(t *testing.T) {
	wantBuilt, wantDocumented, wantMetered := 0, 0, 0
	for _, c := range Catalogue {
		if c.Status == Built {
			wantBuilt++
		} else {
			wantDocumented++
		}
		if c.Metered {
			wantMetered++
		}
	}
	built, documented, metered := Counts()
	if built != wantBuilt || documented != wantDocumented || metered != wantMetered {
		t.Errorf("Counts() = (%d, %d, %d), counting Catalogue by hand gives (%d, %d, %d)",
			built, documented, metered, wantBuilt, wantDocumented, wantMetered)
	}
	if built+documented != len(Catalogue) {
		t.Errorf("built+documented = %d, len(Catalogue) = %d", built+documented, len(Catalogue))
	}
}

// A reader that IS registered gets called, with the saved config, and its
// sentence is what Import returns. Nothing exercises this path in production
// today (the registry is empty), so this is the only proof the wiring works.
func TestImportCallsARegisteredReader(t *testing.T) {
	st := openTestStore(t)
	if err := Save(st.DB(), "opencost", map[string]string{"url": "http://localhost:9003"}); err != nil {
		t.Fatal(err)
	}
	var gotCfg map[string]string
	readers["opencost"] = func(db *sql.DB, cfg map[string]string, opt ImportOptions) (string, error) {
		gotCfg = cfg
		return "Read 3 files, 2026-08-01 to 2026-08-31, 900 rows", nil
	}
	defer delete(readers, "opencost")

	msg, err := Import(st.DB(), "opencost", false, ImportOptions{})
	if err != nil {
		t.Fatalf("a registered reader was not called: %v", err)
	}
	if msg != "Read 3 files, 2026-08-01 to 2026-08-31, 900 rows" {
		t.Errorf("Import returned %q, want the reader's own sentence", msg)
	}
	if gotCfg["url"] != "http://localhost:9003" {
		t.Errorf("the reader was not handed the saved config: %v", gotCfg)
	}
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}
